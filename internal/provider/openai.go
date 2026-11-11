package provider

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"sharetoken/internal/node"
)

// openAIUpstream is a provider that speaks OpenAI chat completions itself:
// the request goes out almost as it came, and the stream comes back as it is.
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
}

func (u *openAIUpstream) Stream(ctx context.Context, body []byte, emit func([]byte) error) error {
	return u.limits.retry(func() error {
		out, err := limitOutput(body, &u.limits, u.tokensField)
		if err != nil {
			return err
		}
		pass := func(data []byte) error { return emit(append([]byte(nil), data...)) }
		if u.ts == nil {
			return postSSE(ctx, u.base+"/chat/completions", u.header, out, pass)
		}
		return withToken(ctx, u.ts, func(tok *Token) error {
			return postSSE(ctx, u.base+"/chat/completions", u.auth(tok), out, pass)
		})
	}, body)
}

// outputLimits are the output limits of a provider's models: those the user
// set, and those the provider told when it refused a request for asking too
// much. A node asks for as much output as the buyer's reservation covers,
// and many models take less.
type outputLimits struct {
	set     map[string]uint64 // upstream model → limit, from the configuration
	mu      sync.Mutex
	learned map[string]uint64
}

func (l *outputLimits) of(model string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if v := l.learned[model]; v > 0 {
		return v
	}
	return l.set[model]
}

// limitPatterns find the limit in a provider's refusal, e.g.
// "This model supports at most 16384 completion tokens",
// "the valid range of max_tokens is [1, 8192]",
// "max_tokens: 32768 > 8192, which is the maximum allowed".
var limitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`at most (\d+)`),
	regexp.MustCompile(`\[\d+, ?(\d+)\]`),
	regexp.MustCompile(`> ?(\d+)`),
	regexp.MustCompile(`maximum[^\d]{0,60}(\d+)`),
}

// retry runs do, and once more when the provider refused the request for its
// output limit and said what the model's limit is. Such a refusal comes
// before any output, so nothing was sent on.
func (l *outputLimits) retry(do func() error, body []byte) error {
	err := do()
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest || !strings.Contains(se.Body, "max_") && !strings.Contains(se.Body, "output tokens") {
		return err
	}
	var req struct {
		Model     string `json:"model"`
		MaxTokens uint64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(body, &req)
	for _, re := range limitPatterns {
		m := re.FindStringSubmatch(se.Body)
		if m == nil {
			continue
		}
		limit, _ := strconv.ParseUint(m[1], 10, 64)
		if limit == 0 || limit >= req.MaxTokens || limit >= l.orMax(req.Model) {
			continue
		}
		l.mu.Lock()
		if l.learned == nil {
			l.learned = map[string]uint64{}
		}
		l.learned[req.Model] = limit
		l.mu.Unlock()
		return do()
	}
	return err
}

func (l *outputLimits) orMax(model string) uint64 {
	if v := l.of(model); v > 0 {
		return v
	}
	return math.MaxUint64
}

// limitOutput lowers max_tokens to the model's own limit, and renames the
// field when the API calls it something else.
func limitOutput(body []byte, limits *outputLimits, field string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	var model string
	var limit uint64
	_ = json.Unmarshal(m["model"], &model)
	_ = json.Unmarshal(m["max_tokens"], &limit)
	c := limits.of(model)
	if c == 0 && (field == "" || field == "max_tokens") {
		return body, nil
	}
	if c > 0 && (limit == 0 || limit > c) {
		limit = c
	}
	delete(m, "max_tokens")
	if field == "" {
		field = "max_tokens"
	}
	if limit > 0 {
		m[field], _ = json.Marshal(limit)
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
