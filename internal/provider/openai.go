package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
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
	// systemRole: the provider's chat API has no "developer" role (DeepSeek,
	// Moonshot, Zhipu): such a message goes as "system".
	systemRole bool
	// reasoningBack: DeepSeek's thinking mode wants reasoning_content back on
	// every assistant message of the history.
	reasoningBack bool
	refused       refusedParams
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
				return u.refused.retry(body, func(body []byte) error {
					on, ended := responsesPassthrough(emit)
					if _, err := u.post(ctx, "/responses", body, on); err != nil {
						return err
					}
					if !ended() {
						return fmt.Errorf("the stream ended before the response did")
					}
					return nil
				})
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
		if u.systemRole || u.reasoningBack {
			out = strictChat(out, u.systemRole, u.reasoningBack)
		}
		return u.refused.retry(out, func(body []byte) error {
			// The answer is complete with a finish_reason or the closing [DONE]:
			// a stream that ends before either was cut on the way.
			finished, called := false, false
			done, err := u.post(ctx, "/chat/completions", body, func(data []byte) error {
				if msg, ok := chatError(data); ok {
					return &node.StatusError{Status: http.StatusBadGateway, Body: clip(msg, 300)}
				}
				finished = finished || chatFinished(data)
				data = append([]byte(nil), data...)
				// Gemini's chat API ends an answer made of tool calls with
				// "stop"; a client that goes by the finish reason would not
				// run them.
				called = called || bytes.Contains(data, []byte(`"tool_calls"`))
				if called {
					data = bytes.Replace(data, []byte(`"finish_reason":"stop"`), []byte(`"finish_reason":"tool_calls"`), 1)
				}
				return emit(data)
			})
			if err == nil && !finished && !done {
				err = fmt.Errorf("the stream ended before the answer did")
			}
			return err
		})
	}, r.Body)
}

// strictChatHosts are the chat APIs that refuse the "developer" role; a
// custom provider may point at one.
var strictChatHosts = map[string]bool{"api.deepseek.com": true, "api.kimi.com": true, "api.moonshot.cn": true,
	"api.moonshot.ai": true, "open.bigmodel.cn": true, "api.z.ai": true}

// chatQuirks sets what the chat API at u.base needs beyond the kind's own.
func (u *openAIUpstream) chatQuirks(kind string) *openAIUpstream {
	host := ""
	if parsed, err := url.Parse(u.base); err == nil {
		host = strings.ToLower(parsed.Hostname())
	}
	deepseek := kind == KindDeepSeek || host == "api.deepseek.com"
	u.systemRole = deepseek || strictChatHosts[host]
	u.reasoningBack = deepseek
	return u
}

// strictChat adapts a chat request's messages to a provider stricter than
// OpenAI: "developer" messages become "system" ones (systemRole), and an
// assistant message without reasoning_content gets a blank one
// (reasoningBack). DeepSeek refuses a history whose assistant messages lost
// their reasoning ("The reasoning_content in the thinking mode must be passed
// back to the API"), as clients that follow OpenAI's API drop it; it takes a
// single space in its place.
func strictChat(body []byte, systemRole, reasoningBack bool) []byte {
	var req map[string]json.RawMessage
	var messages []map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil || json.Unmarshal(req["messages"], &messages) != nil {
		return body
	}
	changed := false
	for _, m := range messages {
		var role string
		_ = json.Unmarshal(m["role"], &role)
		switch {
		case systemRole && role == "developer":
			m["role"], changed = json.RawMessage(`"system"`), true
		case reasoningBack && role == "assistant":
			var reasoning string
			if _ = json.Unmarshal(m["reasoning_content"], &reasoning); reasoning == "" {
				m["reasoning_content"], changed = json.RawMessage(`" "`), true
			}
		}
	}
	if !changed {
		return body
	}
	req["messages"], _ = json.Marshal(messages)
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// refusedParams are the settings each model refused: OpenAI's reasoning
// models take no temperature or top_p, others no penalties or reasoning
// effort, and say so with
//
//	"Unsupported parameter: 'top_p' is not supported with this model."
//	"Unsupported value: 'temperature' does not support 0.2 with this model. Only the default (1) value is supported."
//
// A buyer's tool sends them without knowing the model (and a conversion
// carries them over from another API), so the request goes again without the
// setting, and later requests for the model leave it out from the start.
//
// Two refusals are repaired rather than dropped. A reasoning effort the model
// does not have ("'none' is not supported with the 'gpt-5-mini' model.
// Supported values are: 'minimal', 'low', 'medium', and 'high'.") becomes the
// nearest one it has. And a chat API without structured output (DeepSeek:
// "This response_format type is unavailable now") gets JSON mode with the
// schema in a system message, which is how such a model is asked for one.
// Gemini's "none" becomes "minimal" the same way.
type refusedParams struct {
	mu      sync.Mutex
	byModel map[string]*modelRepairs
}

type modelRepairs struct {
	drop      []string
	effort    map[string]string // an effort the model refused -> the one sent instead
	plainJSON bool              // json_schema goes as json_object
}

// droppable are the settings a request may lose: the model then uses the
// one value it has. Nothing that changes what the answer is made of (stop,
// tools, the output limit) is among them.
var droppable = map[string]bool{"temperature": true, "top_p": true, "presence_penalty": true, "frequency_penalty": true,
	"reasoning_effort": true, "parallel_tool_calls": true}

var (
	refusedParamPattern  = regexp.MustCompile(`(?i)unsupported (?:parameter|value): '([a-z_]+)'`)
	refusedEffortPattern = regexp.MustCompile(`(?i)unsupported value: '([a-z]+)' is not supported with [^.]*model\. supported values are: ([^.]*)\.`)
	quotedWord           = regexp.MustCompile(`'([a-z]+)'`)
)

// efforts in order: a refused one becomes the nearest the model has, the
// lower of two equally near.
var efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

func nearestEffort(want string, have []string) string {
	at := slices.Index(efforts, want)
	best, dist := "", len(efforts)
	for _, h := range have {
		i := slices.Index(efforts, h)
		if i < 0 || h == want {
			continue
		}
		d := i - at
		if d < 0 {
			d = -d
		}
		if d < dist {
			best, dist = h, d
		}
	}
	if at < 0 {
		return ""
	}
	return best
}

// requestEffort reads the reasoning effort of a chat ("reasoning_effort") or
// Responses ("reasoning.effort") request.
func requestEffort(req map[string]json.RawMessage) string {
	var effort string
	if json.Unmarshal(req["reasoning_effort"], &effort) == nil && effort != "" {
		return effort
	}
	var reasoning map[string]json.RawMessage
	if json.Unmarshal(req["reasoning"], &reasoning) == nil {
		_ = json.Unmarshal(reasoning["effort"], &effort)
	}
	return effort
}

func setRequestEffort(req map[string]json.RawMessage, effort string) {
	value, _ := json.Marshal(effort)
	if _, chat := req["reasoning_effort"]; chat {
		req["reasoning_effort"] = value
		return
	}
	var reasoning map[string]json.RawMessage
	if json.Unmarshal(req["reasoning"], &reasoning) == nil && reasoning != nil {
		reasoning["effort"] = value
		req["reasoning"], _ = json.Marshal(reasoning)
	}
}

// plainJSON turns a chat request's json_schema response format into JSON
// mode, with the schema in a system message at the front. It reports whether
// the request had one.
func plainJSON(req map[string]json.RawMessage) bool {
	var format struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	var messages []json.RawMessage
	if json.Unmarshal(req["response_format"], &format) != nil || format.Type != "json_schema" || json.Unmarshal(req["messages"], &messages) != nil {
		return false
	}
	text := "Answer with a single JSON object and nothing else."
	if len(format.JSONSchema.Schema) > 0 {
		text = "Answer with a single JSON object and nothing else. It must match this JSON Schema:\n" + string(format.JSONSchema.Schema)
	}
	note, _ := json.Marshal(map[string]string{"role": "system", "content": text})
	req["messages"], _ = json.Marshal(append([]json.RawMessage{note}, messages...))
	req["response_format"] = json.RawMessage(`{"type":"json_object"}`)
	return true
}

// apply makes the repairs known for the model. It reports whether req changed.
func (m *modelRepairs) apply(req map[string]json.RawMessage) bool {
	if m == nil {
		return false
	}
	changed := false
	for _, name := range m.drop {
		if _, ok := req[name]; ok {
			delete(req, name)
			changed = true
		}
	}
	if to := m.effort[requestEffort(req)]; to != "" {
		setRequestEffort(req, to)
		changed = true
	}
	if m.plainJSON && plainJSON(req) {
		changed = true
	}
	return changed
}

// repair changes req the way the refusal asks, and notes it in learnt. It
// reports whether there was anything to change.
func repairRefused(req map[string]json.RawMessage, refusal string, learnt *modelRepairs) bool {
	if m := refusedEffortPattern.FindStringSubmatch(refusal); m != nil {
		from := strings.ToLower(m[1])
		var have []string
		for _, q := range quotedWord.FindAllStringSubmatch(strings.ToLower(m[2]), -1) {
			have = append(have, q[1])
		}
		if to := nearestEffort(from, have); to != "" && requestEffort(req) == from {
			setRequestEffort(req, to)
			if learnt.effort == nil {
				learnt.effort = map[string]string{}
			}
			learnt.effort[from] = to
			return true
		}
		return false
	}
	if m := refusedParamPattern.FindStringSubmatch(refusal); m != nil && droppable[m[1]] {
		if _, sent := req[m[1]]; sent {
			delete(req, m[1])
			learnt.drop = append(learnt.drop, m[1])
			return true
		}
		return false
	}
	// Gemini 3 cannot think less than "minimal", and says of "none" only
	// that an argument is invalid.
	if strings.Contains(refusal, "INVALID_ARGUMENT") && requestEffort(req) == "none" {
		setRequestEffort(req, "minimal")
		if learnt.effort == nil {
			learnt.effort = map[string]string{}
		}
		learnt.effort["none"] = "minimal"
		return true
	}
	if strings.Contains(refusal, "response_format") && !learnt.plainJSON && plainJSON(req) {
		learnt.plainJSON = true
		return true
	}
	return false
}

func (p *refusedParams) retry(body []byte, do func([]byte) error) error {
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return do(body)
	}
	var model string
	_ = json.Unmarshal(req["model"], &model)
	p.mu.Lock()
	known := p.byModel[model]
	changed := known.apply(req)
	p.mu.Unlock()
	if changed {
		body, _ = json.Marshal(req)
	}
	learnt := &modelRepairs{}
	repaired := false
	err := do(body)
	for range len(droppable) + 2 {
		var se *node.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusBadRequest || !repairRefused(req, se.Body, learnt) {
			break
		}
		repaired = true
		body, _ = json.Marshal(req)
		err = do(body)
	}
	// Kept only when the provider then took the request: a refusal that
	// merely quotes the buyer's text comes again, and teaches nothing.
	var se *node.StatusError
	if repaired && !(errors.As(err, &se) && se.Status == http.StatusBadRequest) {
		p.mu.Lock()
		if p.byModel == nil {
			p.byModel = map[string]*modelRepairs{}
		}
		m := p.byModel[model]
		if m == nil {
			m = &modelRepairs{}
			p.byModel[model] = m
		}
		for _, name := range learnt.drop {
			if !slices.Contains(m.drop, name) {
				m.drop = append(m.drop, name)
			}
		}
		for from, to := range learnt.effort {
			if m.effort == nil {
				m.effort = map[string]string{}
			}
			m.effort[from] = to
		}
		m.plainJSON = m.plainJSON || learnt.plainJSON
		p.mu.Unlock()
	}
	return err
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
		u.messagesUp = &anthropicUpstream{base: u.messages, limits: outputLimits{set: u.limits.set}, header: u.messagesHeader(), passback: true}
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
