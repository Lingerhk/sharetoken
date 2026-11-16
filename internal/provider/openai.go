package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// openAIUpstream is a provider that speaks OpenAI chat completions itself,
// and with responses set the Responses API too (OpenAI's own API): the
// request goes out almost as it came, and the stream comes back as it is.
type openAIUpstream struct {
	base   string
	header http.Header
	// ts and auth, for an OAuth login: the header is made from the current
	// token for each request instead.
	ts   TokenSource
	auth func(tok *Token) http.Header
	// tokensField is the name this API has for the output limit: OpenAI's
	// current models take max_completion_tokens and refuse max_tokens.
	tokensField string
	limits      outputLimits
	responses   bool
	// messages, when set, is the base URL of the provider's Anthropic
	// Messages endpoint (DeepSeek has one): Messages requests go there.
	messages   string
	messagesUp *anthropicUpstream // made once: it keeps the limits it learns
	messagesMu sync.Mutex
	// thoughtSignatures: Gemini wants the thought signature it gave with each
	// tool call back with it; calls that lost theirs (converted from another
	// API, or from another model) get the placeholder Gemini accepts.
	thoughtSignatures bool
}

func (u *openAIUpstream) APIs() []string {
	apis := []string{protocol.APIChat}
	if u.messages != "" {
		apis = append(apis, protocol.APIMessages)
	}
	if u.responses {
		apis = append(apis, protocol.APIResponses)
	}
	return apis
}

func (u *openAIUpstream) Stream(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	switch {
	case r.API == protocol.APIResponses && u.responses:
		return u.limits.retry(func() error {
			out, err := limitOutput(dropConvertedReasoning(r.Body), &u.limits, "max_output_tokens", "max_output_tokens")
			if err != nil {
				return err
			}
			return retryWithoutReasoning(out, func(body []byte) error {
				on, ended := responsesPassthrough(emit)
				if _, err := u.post(ctx, "/responses", body, on); err != nil {
					return err
				}
				if !ended() {
					return fmt.Errorf("the stream ended before the response did")
				}
				return nil
			})
		}, r.Body)
	case r.API == protocol.APIMessages && u.messages != "":
		return u.messagesUpstream().Stream(ctx, r, emit)
	case r.API != protocol.APIChat:
		return fmt.Errorf("provider: cannot send a %s request", r.API)
	}
	return u.limits.retry(func() error {
		out, err := limitOutput(r.Body, &u.limits, "max_tokens", u.tokensField)
		if err != nil {
			return err
		}
		if u.thoughtSignatures {
			out = signToolCalls(out)
		}
		// The answer is complete with a finish_reason or the closing [DONE]:
		// a stream that ends before either was cut on the way.
		finished := false
		done, err := u.post(ctx, "/chat/completions", out, func(data []byte) error {
			if msg, ok := chatError(data); ok {
				return &node.StatusError{Status: http.StatusBadGateway, Body: clip(msg, 300)}
			}
			finished = finished || chatFinished(data)
			return emit(append([]byte(nil), data...))
		})
		if err == nil && !finished && !done {
			err = fmt.Errorf("the stream ended before the answer did")
		}
		return err
	}, r.Body)
}

// post sends body to path and calls on with each event payload. done tells
// whether the "[DONE]" line that closes a chat stream came.
func (u *openAIUpstream) post(ctx context.Context, path string, body []byte, on func([]byte) error) (done bool, err error) {
	if u.ts == nil {
		return postSSEDone(ctx, u.base+path, u.header, body, on)
	}
	err = withToken(ctx, u.ts, func(tok *Token) error {
		done, err = postSSEDone(ctx, u.base+path, u.auth(tok), body, on)
		return err
	})
	return done, err
}

// messagesUpstream is the provider's Messages endpoint, the same one for
// every request, so an output limit it learnt holds for the next.
func (u *openAIUpstream) messagesUpstream() *anthropicUpstream {
	u.messagesMu.Lock()
	defer u.messagesMu.Unlock()
	if u.messagesUp == nil {
		u.messagesUp = &anthropicUpstream{base: u.messages, limits: outputLimits{set: u.limits.set}, header: u.messagesHeader()}
	}
	return u.messagesUp
}

// chatFinished reports whether a chat chunk carries a finish_reason.
func chatFinished(data []byte) bool {
	if !bytes.Contains(data, []byte(`"finish_reason"`)) {
		return false
	}
	info, err := protocol.ParseChunk(data)
	return err == nil && info.FinishReason != ""
}

// messagesHeader is the key header for the provider's Messages endpoint.
func (u *openAIUpstream) messagesHeader() http.Header {
	h := u.header.Clone()
	if auth := h.Get("Authorization"); auth != "" {
		h.Set("x-api-key", strings.TrimPrefix(auth, "Bearer "))
		h.Del("Authorization")
	}
	return h
}

// signToolCalls gives each assistant tool call without a thought signature
// Gemini's placeholder one (in Gemini's OpenAI API, extra_content.google).
func signToolCalls(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_calls"`)) {
		return body
	}
	req, err := decodeObject(body)
	if err != nil {
		return body
	}
	changed := false
	for _, m := range list(req["messages"]) {
		msg, _ := m.(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		for _, c := range list(msg["tool_calls"]) {
			call, _ := c.(map[string]any)
			if call == nil {
				continue
			}
			extra, _ := call["extra_content"].(map[string]any)
			if extra == nil {
				extra = map[string]any{}
			}
			google, _ := extra["google"].(map[string]any)
			if google == nil {
				google = map[string]any{}
			}
			if sig, _ := google["thought_signature"].(string); sig != "" {
				continue
			}
			google["thought_signature"] = skipThoughtSignature
			extra["google"] = google
			call["extra_content"] = extra
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// chatError reads an error a chat server sent inside its stream.
func chatError(data []byte) (string, bool) {
	var ev struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &ev) != nil || ev.Error == nil {
		return "", false
	}
	return strings.TrimSpace(fmt.Sprint(ev.Error.Type, " ", ev.Error.Message)), true
}

// responsesPassthrough passes a Responses stream on, failing on its error
// events. ended tells whether the terminal event came: a stream without it
// was cut on the way.
func responsesPassthrough(emit func([]byte) error) (on func([]byte) error, ended func() bool) {
	done := false
	on = func(data []byte) error {
		typ, msg := eventType(data)
		switch typ {
		case "error", "response.failed":
			return &node.StatusError{Status: http.StatusBadGateway, Body: clip(typ+": "+msg, 300)}
		case "response.completed", "response.incomplete", "response.done":
			done = true
		}
		return emit(append([]byte(nil), data...))
	}
	return on, func() bool { return done }
}

// outputLimits are the output limits of a provider's models: those the user
// set, and those the provider told when it refused a request for asking too
// much. A node asks for as much output as the buyer's reservation covers,
// and many models take less.
type outputLimits struct {
	set     map[string]uint64 // upstream model → limit, from the configuration
	mu      sync.Mutex
	learned map[string]learnedLimit
}

// learnedLimit is a limit a provider told, and when: it is asked again after
// learnedLimitTTL, as providers raise their limits.
type learnedLimit struct {
	limit uint64
	at    time.Time
}

const (
	learnedLimitTTL = 24 * time.Hour
	// minLearnedLimit: no model worth serving writes less, so a smaller
	// number in a refusal is not a model's limit.
	minLearnedLimit = 1024
)

func (l *outputLimits) of(model string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, ok := l.learned[model]; ok {
		if timeNow().Sub(v.at) < learnedLimitTTL {
			return v.limit
		}
		delete(l.learned, model)
	}
	return l.set[model]
}

func (l *outputLimits) learn(model string, limit uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.learned == nil {
		l.learned = map[string]learnedLimit{}
	}
	l.learned[model] = learnedLimit{limit: limit, at: timeNow()}
}

// forget drops what learn(model, limit) stored, unless it changed since.
func (l *outputLimits) forget(model string, limit uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, ok := l.learned[model]; ok && v.limit == limit {
		delete(l.learned, model)
	}
}

// limitPatterns are the refusals of a request for asking more output than
// the model writes, with the model's limit:
//
//	OpenAI     "max_tokens is too large: 32768. This model supports at most 16384 completion tokens"
//	OpenAI     "Invalid 'max_output_tokens': integer above maximum value. Expected a value <= 128000"
//	DeepSeek   "the valid range of max_tokens is [1, 8192]"
//	Anthropic  "max_tokens: 32768 > 8192, which is the maximum allowed number of output tokens"
//
// Nothing else is read as a limit: a provider quotes the buyer's own text in
// other refusals, and a number in that is not the model's limit.
var limitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`max_(?:completion_|output_)?tokens is too large: \d+\. This model supports at most (\d+) completion tokens`),
	regexp.MustCompile(`Invalid 'max_(?:completion_|output_)?tokens': integer above maximum value\. Expected a value <= (\d+)`),
	regexp.MustCompile(`the valid range of max_(?:completion_|output_)?tokens is \[\d+, ?(\d+)\]`),
	regexp.MustCompile(`max_tokens: \d+ > (\d+), which is the maximum allowed number of output tokens`),
}

// retry runs do, and once more when the provider refused the request for its
// output limit and said what the model's limit is. Such a refusal comes
// before any output, so nothing was sent on. The limit is kept for later
// requests only if the provider then took the request: a refusal that merely
// quotes text of the buyer's that looks like a limit is given again for the
// second request, and teaches nothing.
func (l *outputLimits) retry(do func() error, body []byte) error {
	err := do()
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		return err
	}
	var req struct {
		Model           string `json:"model"`
		MaxTokens       uint64 `json:"max_tokens"`
		MaxOutputTokens uint64 `json:"max_output_tokens"`
	}
	_ = json.Unmarshal(body, &req)
	req.MaxTokens = max(req.MaxTokens, req.MaxOutputTokens)
	for _, re := range limitPatterns {
		m := re.FindStringSubmatch(se.Body)
		if m == nil {
			continue
		}
		limit, _ := strconv.ParseUint(m[1], 10, 64)
		if limit < minLearnedLimit || limit >= req.MaxTokens || limit >= l.orMax(req.Model) {
			continue
		}
		l.learn(req.Model, limit)
		err := do()
		if errors.As(err, &se) && se.Status == http.StatusBadRequest {
			l.forget(req.Model, limit)
		}
		return err
	}
	return err
}

func (l *outputLimits) orMax(model string) uint64 {
	if v := l.of(model); v > 0 {
		return v
	}
	return math.MaxUint64
}

// limitOutput lowers the output limit (field from) to the model's own limit,
// and renames the field to what the API calls it (to; empty: unchanged).
func limitOutput(body []byte, limits *outputLimits, from, to string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	var model string
	var limit uint64
	_ = json.Unmarshal(m["model"], &model)
	_ = json.Unmarshal(m[from], &limit)
	c := limits.of(model)
	if to == "" {
		to = from
	}
	if c == 0 && to == from {
		return body, nil
	}
	if c > 0 && (limit == 0 || limit > c) {
		limit = c
	}
	delete(m, from)
	if limit > 0 {
		m[to], _ = json.Marshal(limit)
	}
	return json.Marshal(m)
}

func bearer(key string, extra map[string]string) http.Header {
	h := http.Header{}
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

// openAIModels lists GET {base}/models. Gemini's OpenAI endpoint names its
// models "models/<id>"; chat takes the bare ID.
func openAIModels(ctx context.Context, base string, header http.Header) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, base+"/models", header, &out); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, m := range out.Data {
		ids = append(ids, strings.TrimPrefix(m.ID, "models/"))
	}
	return ids, nil
}
