package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Verification methods: how a reference scores a seller's answer.
const (
	// MethodTF is teacher forcing: the reference reads the seller's answer
	// token by token and reports, at every position, whether the seller's
	// token is its own first choice.
	MethodTF = "tf"
	// MethodDiv is the first-divergence check, for references that report
	// log-probabilities only for what they generate themselves: the seller's
	// answer is walked along the reference's own greedy answer, and the one
	// position where they part is checked.
	MethodDiv = "div"
	// MethodNone is a reference that can neither continue a given text nor
	// report log-probabilities (Claude, Gemini today): the model's identity
	// is not audited, only the bill.
	MethodNone = "none"
)

// Verify kinds, as coordinator.yaml names them (references[].verify).
const (
	VerifyVLLM           = "vllm"            // self-hosted vLLM/SGLang: prompt_logprobs, one call per question
	VerifyDeepSeekPrefix = "deepseek-prefix" // DeepSeek API: beta prefix completion, one call per token
	VerifyOpenAIDiv      = "openai-div"      // OpenAI API: generation logprobs, first divergence
	VerifyNone           = "none"
)

// VerifyKinds lists the valid references[].verify values.
var VerifyKinds = []string{VerifyVLLM, VerifyDeepSeekPrefix, VerifyOpenAIDiv, VerifyNone}

// Question is one challenge as the seller received it, after the node's
// request rewriting (the messages an honest node sent its model).
type Question struct {
	Messages  []Message
	MaxTokens int
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Score is one answer scored against a reference.
type Score struct {
	Method string `json:"method"`
	// Tokens is how many positions were scored (tf), or how many of the
	// reference's tokens the answer followed before parting (div).
	Tokens int `json:"tokens"`
	// Top1 is the share of the seller's tokens that are the reference's
	// first choice (tf).
	Top1 float64 `json:"top1,omitempty"`
	// Top20 is the share found among the reference's 20 first choices (tf).
	Top20 float64 `json:"top20,omitempty"`
	// BigGaps is the share of tokens more than 2 nats below the first choice
	// (tf with real log-probabilities; DeepSeek reports ranks only).
	BigGaps float64 `json:"big_gaps,omitempty"`
	// EndAgrees: the seller stopped where the reference would stop too (tf;
	// nil when the seller ran into its output limit).
	EndAgrees *bool `json:"end_agrees,omitempty"`
	// Diverged, DivKind, DivRank and DivGap describe the first divergence
	// (div). DivRank is 1-based; -1 means not among the reference's top 20.
	Diverged bool    `json:"diverged,omitempty"`
	DivKind  string  `json:"div_kind,omitempty"`
	DivRank  int     `json:"div_rank,omitempty"`
	DivGap   float64 `json:"div_gap,omitempty"`
	Fail     bool    `json:"fail,omitempty"` // div: the divergence is not a near tie
	// RefPrompt and RefCompletion are the reference's own token counts for
	// the prompt and (approximately, tf) the answer: a bound on the bill.
	RefPrompt     uint64 `json:"ref_prompt,omitempty"`
	RefCompletion uint64 `json:"ref_completion,omitempty"`
	Calls         int    `json:"calls"` // reference API calls spent
}

// Verifier scores a seller's answer to one question against a reference.
type Verifier interface {
	Method() string
	// Score reads answer (the seller's text; stopped: it ended on its own,
	// not at the output limit) and scores it. maxScored caps the scored
	// positions (0: all).
	Score(ctx context.Context, q Question, answer string, stopped bool, maxScored int) (*Score, error)
	// Generate answers q as the reference itself would (greedy), which is
	// what an honest seller returns; calibration scores these.
	Generate(ctx context.Context, q Question) (text string, stopped bool, err error)
}

// NewVerifier returns the verifier for a references[].verify kind. baseURL is
// the reference's OpenAI-compatible base URL (up to and including /v1);
// params are extra fields for every chat request (e.g. reasoning_effort:
// none for a model that thinks by default), the same the challenges carry.
func NewVerifier(kind, baseURL, apiKey, model string, params map[string]any) (Verifier, error) {
	api := &chatAPI{base: strings.TrimRight(baseURL, "/"), key: apiKey, model: model, params: params}
	switch kind {
	case VerifyVLLM:
		return &vllmTF{api}, nil
	case VerifyDeepSeekPrefix:
		return &deepseekTF{api: api, beta: strings.TrimSuffix(api.base, "/v1") + "/beta"}, nil
	case VerifyOpenAIDiv:
		return &openaiDiv{api}, nil
	case VerifyNone, "":
		return noVerifier{api}, nil
	}
	return nil, fmt.Errorf("unknown verify kind %q (want one of %s)", kind, strings.Join(VerifyKinds, ", "))
}

// ---- the reference's HTTP API ----

type chatAPI struct {
	base, key, model string
	params           map[string]any
	client           *http.Client
}

// chat sends a chat completion request, with the reference's params.
func (c *chatAPI) chat(ctx context.Context, url string, body map[string]any, out *chatResponse) error {
	for k, v := range c.params {
		if _, set := body[k]; !set {
			body[k] = v
		}
	}
	return c.post(ctx, url, body, out)
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("reference API: HTTP %d: %s", e.status, e.body) }

// post sends a JSON body and decodes the JSON answer, retrying rate limits
// and server errors a few times.
func (c *chatAPI) post(ctx context.Context, url string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	hc := c.client
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	var last error
	for attempt := range 5 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		resp, err := hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = &apiError{resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 300)]))}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				continue
			}
			return last
		}
		return json.Unmarshal(raw, out)
	}
	return last
}

type topLogprob struct {
	Token   string  `json:"token"`
	Bytes   []int   `json:"bytes"`
	Logprob float64 `json:"logprob"`
}

type tokenLogprob struct {
	topLogprob
	Top []topLogprob `json:"top_logprobs"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
		Logprobs     *struct {
			Content []tokenLogprob `json:"content"`
		} `json:"logprobs"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     uint64 `json:"prompt_tokens"`
		CompletionTokens uint64 `json:"completion_tokens"`
	} `json:"usage"`
	// vLLM: one entry per prompt token (the first is null), keyed by token ID.
	PromptLogprobs []map[string]vllmLogprob `json:"prompt_logprobs"`
}

func (r *chatResponse) choice() (content, finish string, lp []tokenLogprob, err error) {
	if len(r.Choices) == 0 {
		return "", "", nil, errors.New("reference API: no choices in the answer")
	}
	c := r.Choices[0]
	if c.Logprobs != nil {
		lp = c.Logprobs.Content
	}
	return c.Message.Content, c.FinishReason, lp, nil
}

func tokenBytes(t topLogprob) []byte {
	if t.Bytes == nil {
		return []byte(t.Token)
	}
	b := make([]byte, len(t.Bytes))
	for i, v := range t.Bytes {
		b[i] = byte(v)
	}
	return b
}

// generate is the reference's own greedy answer.
func (c *chatAPI) generate(ctx context.Context, q Question, logprobs bool) (*chatResponse, error) {
	body := map[string]any{"model": c.model, "messages": q.Messages, "temperature": 0, "max_tokens": q.MaxTokens}
	if logprobs {
		body["logprobs"], body["top_logprobs"] = true, 20
	}
	var r chatResponse
	return &r, c.chat(ctx, c.base+"/chat/completions", body, &r)
}

func generateText(ctx context.Context, c *chatAPI, q Question) (string, bool, error) {
	r, err := c.generate(ctx, q, false)
	if err != nil {
		return "", false, err
	}
	text, finish, _, err := r.choice()
	return text, finish != "length", err
}

// ---- V1 on DeepSeek: beta prefix completion, one call per token ----

type deepseekTF struct {
	api  *chatAPI
	beta string
}

func (d *deepseekTF) Method() string { return MethodTF }

func (d *deepseekTF) Generate(ctx context.Context, q Question) (string, bool, error) {
	return generateText(ctx, d.api, q)
}

// next asks for the reference's 20 first choices after prefix. ends reports
// that the reference stops there instead.
func (d *deepseekTF) next(ctx context.Context, q Question, prefix string) (cands [][]byte, ends bool, prompt uint64, err error) {
	msgs := make([]map[string]any, 0, len(q.Messages)+1)
	for _, m := range q.Messages {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	if prefix != "" {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": prefix, "prefix": true})
	}
	var r chatResponse
	err = d.api.chat(ctx, d.beta+"/chat/completions", map[string]any{
		"model": d.api.model, "messages": msgs, "max_tokens": 1, "temperature": 0, "logprobs": true, "top_logprobs": 20,
	}, &r)
	if err != nil {
		return nil, false, 0, err
	}
	_, finish, lp, err := r.choice()
	if err != nil {
		return nil, false, 0, err
	}
	if r.Usage != nil {
		prompt = r.Usage.PromptTokens
	}
	if len(lp) == 0 {
		return nil, finish == "stop", prompt, nil
	}
	for _, t := range lp[0].Top {
		cands = append(cands, tokenBytes(t))
	}
	return cands, false, prompt, nil
}

// fallbackStep is how far a miss advances: a word, a number of up to three
// digits, one other character, or a run of white space.
var fallbackStep = regexp.MustCompile(`^(?: ?[A-Za-z]+| ?[0-9]{1,3}| ?[^\sA-Za-z0-9]|\s+)`)

func (d *deepseekTF) Score(ctx context.Context, q Question, answer string, stopped bool, maxScored int) (*Score, error) {
	s := &Score{Method: MethodTF}
	ans := []byte(answer)
	pos, hits, in20 := 0, 0, 0
	for pos < len(ans) && (maxScored <= 0 || s.Tokens < maxScored) {
		cands, _, prompt, err := d.next(ctx, q, string(ans[:pos]))
		s.Calls++
		if err != nil {
			return nil, err
		}
		if pos == 0 {
			s.RefPrompt = prompt
		}
		s.Tokens++
		rem := ans[pos:]
		step := 0
		for k, c := range cands {
			if len(c) > 0 && bytes.HasPrefix(rem, c) {
				if k == 0 {
					hits++
				}
				in20++
				step = charBoundary(rem, len(c))
				break
			}
		}
		if step == 0 {
			step = len(fallbackStep.Find(rem))
			if step == 0 {
				step = charBoundary(rem, 1)
			}
		}
		pos += step
	}
	if s.Tokens > 0 {
		s.RefCompletion = uint64(s.Tokens)
	}
	if stopped && pos >= len(ans) && (maxScored <= 0 || s.Tokens < maxScored) {
		_, ends, _, err := d.next(ctx, q, answer)
		s.Calls++
		if err != nil {
			return nil, err
		}
		s.EndAgrees = &ends
		s.Tokens++ // the end is one more position: a hit if the reference ends there too
		if ends {
			hits++
			in20++
		}
	}
	if s.Tokens > 0 {
		s.Top1 = float64(hits) / float64(s.Tokens)
		s.Top20 = float64(in20) / float64(s.Tokens)
	}
	return s, nil
}

// charBoundary extends a byte match of n bytes at the start of b to the next
// UTF-8 character boundary: a prefix sent back must be valid text.
func charBoundary(b []byte, n int) int {
	n = min(n, len(b))
	for n < len(b) && !utf8.Valid(b[:n]) {
		n++
	}
	return n
}

// ---- V1 on vLLM: prompt_logprobs, one call per question ----

type vllmTF struct{ api *chatAPI }

type vllmLogprob struct {
	Logprob float64 `json:"logprob"`
	Rank    int     `json:"rank"`
}

func (v *vllmTF) Method() string { return MethodTF }

func (v *vllmTF) Generate(ctx context.Context, q Question) (string, bool, error) {
	return generateText(ctx, v.api, q)
}

// tokenize renders and tokenizes messages with the server's chat template.
func (v *vllmTF) tokenize(ctx context.Context, msgs []Message, continueFinal bool) ([]int, error) {
	body := map[string]any{"model": v.api.model, "messages": msgs}
	if continueFinal {
		body["add_generation_prompt"], body["continue_final_message"] = false, true
	} else {
		body["add_generation_prompt"] = true
	}
	var r struct {
		Tokens []int `json:"tokens"`
	}
	// /tokenize sits beside /v1, not under it.
	err := v.api.post(ctx, strings.TrimSuffix(v.api.base, "/v1")+"/tokenize", body, &r)
	return r.Tokens, err
}

func (v *vllmTF) Score(ctx context.Context, q Question, answer string, stopped bool, maxScored int) (*Score, error) {
	s := &Score{Method: MethodTF}
	head, err := v.tokenize(ctx, q.Messages, false)
	if err != nil {
		return nil, err
	}
	full := append(append([]Message(nil), q.Messages...), Message{Role: "assistant", Content: answer})
	all, err := v.tokenize(ctx, full, true)
	if err != nil {
		return nil, err
	}
	s.Calls += 2
	if len(all) < len(head) || !slicesEqual(all[:len(head)], head) {
		return nil, errors.New("reference: the chat template does not extend the prompt with the answer")
	}
	var r chatResponse
	err = v.api.chat(ctx, v.api.base+"/chat/completions", map[string]any{
		"model": v.api.model, "messages": full, "add_generation_prompt": false, "continue_final_message": true,
		"max_tokens": 1, "temperature": 0, "prompt_logprobs": 20,
	}, &r)
	s.Calls++
	if err != nil {
		return nil, err
	}
	if len(r.PromptLogprobs) != len(all) {
		return nil, fmt.Errorf("reference: %d prompt logprobs for %d tokens", len(r.PromptLogprobs), len(all))
	}
	s.RefPrompt, s.RefCompletion = uint64(len(head)), uint64(len(all)-len(head))
	hits, in20, gaps := 0, 0, 0
	for i := len(head); i < len(all) && (maxScored <= 0 || s.Tokens < maxScored); i++ {
		pos := r.PromptLogprobs[i]
		got, ok := pos[strconv.Itoa(all[i])]
		if !ok {
			return nil, fmt.Errorf("reference: no logprob for token %d at %d", all[i], i)
		}
		best := got.Logprob
		for _, e := range pos {
			if e.Rank == 1 {
				best = e.Logprob
			}
		}
		s.Tokens++
		if got.Rank == 1 {
			hits++
		}
		if got.Rank <= 20 {
			in20++
		}
		if best-got.Logprob > 2 {
			gaps++
		}
	}
	if stopped && (maxScored <= 0 || s.Tokens < maxScored) {
		content, finish, _, err := r.choice()
		if err != nil {
			return nil, err
		}
		ends := content == "" && finish == "stop"
		s.EndAgrees = &ends
		s.Tokens++
		if ends {
			hits++
			in20++
		}
	}
	if s.Tokens > 0 {
		n := float64(s.Tokens)
		s.Top1, s.Top20, s.BigGaps = float64(hits)/n, float64(in20)/n, float64(gaps)/n
	}
	return s, nil
}

func slicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- V2 on OpenAI: first divergence from the reference's greedy answer ----

type openaiDiv struct{ api *chatAPI }

// divGap is the largest log-probability gap below the first choice that
// still counts as a near tie at the first divergence.
const divGap = 1.0

func (o *openaiDiv) Method() string { return MethodDiv }

func (o *openaiDiv) Generate(ctx context.Context, q Question) (string, bool, error) {
	return generateText(ctx, o.api, q)
}

func (o *openaiDiv) Score(ctx context.Context, q Question, answer string, stopped bool, _ int) (*Score, error) {
	r, err := o.api.generate(ctx, q, true)
	if err != nil {
		return nil, err
	}
	_, finish, lp, err := r.choice()
	if err != nil {
		return nil, err
	}
	if len(lp) == 0 {
		return nil, errors.New("reference: no logprobs in the answer (the model does not report them)")
	}
	s := firstDivergence(lp, finish, []byte(answer), stopped)
	s.Calls = 1
	if r.Usage != nil {
		s.RefPrompt, s.RefCompletion = r.Usage.PromptTokens, r.Usage.CompletionTokens
	}
	return s, nil
}

// firstDivergence walks the seller's answer along the reference's tokens and
// checks the first position where they part.
func firstDivergence(ref []tokenLogprob, refFinish string, ans []byte, stopped bool) *Score {
	s := &Score{Method: MethodDiv}
	pos := 0
	for i, t := range ref {
		tb := tokenBytes(t.topLogprob)
		if bytes.HasPrefix(ans[pos:], tb) {
			pos += len(tb)
			continue
		}
		s.Tokens, s.Diverged = i, true
		rem := ans[pos:]
		if len(rem) == 0 {
			if !stopped { // the seller hit its limit sooner: nothing to compare
				s.Diverged, s.DivKind = false, "seller truncated"
				return s
			}
			s.DivKind, s.DivRank, s.Fail = "seller stopped early", -1, true
			return s
		}
		s.DivKind, s.DivRank = "token", -1
		for k, c := range t.Top {
			if cb := tokenBytes(c); len(cb) > 0 && bytes.HasPrefix(rem, cb) {
				s.DivRank, s.DivGap = k+1, t.Top[0].Logprob-c.Logprob
				break
			}
		}
		s.Fail = s.DivRank < 0 || s.DivGap > divGap
		return s
	}
	s.Tokens = len(ref)
	if pos < len(ans) && refFinish != "length" {
		s.Diverged, s.DivKind, s.DivRank, s.Fail = true, "seller went on", -1, true
		return s
	}
	s.DivKind = "none"
	return s
}

// ---- V0: nothing to check the model against ----

type noVerifier struct{ api *chatAPI }

func (noVerifier) Method() string { return MethodNone }

func (n noVerifier) Generate(ctx context.Context, q Question) (string, bool, error) {
	return generateText(ctx, n.api, q)
}

// Score only counts the prompt's tokens, for the bill.
func (n noVerifier) Score(ctx context.Context, q Question, _ string, _ bool, _ int) (*Score, error) {
	s := &Score{Method: MethodNone}
	if n.api == nil {
		return s, nil
	}
	r, err := n.api.generate(ctx, Question{Messages: q.Messages, MaxTokens: 1}, false)
	if err != nil {
		return nil, err
	}
	s.Calls = 1
	if r.Usage != nil {
		s.RefPrompt = r.Usage.PromptTokens
	}
	return s, nil
}
