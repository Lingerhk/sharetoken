package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"sharetoken/node"
	"sharetoken/protocol"
)

// TokenSource gives the current OAuth token of a provider, renewing it when
// it is about to expire. force renews it even if it looks valid: the server
// has just refused it.
type TokenSource interface {
	Token(ctx context.Context, force bool) (*Token, error)
}

// ErrKeyRefused is returned when the provider does not accept the key or login.
var ErrKeyRefused = errors.New("the provider refused the credentials")

// caps is the output limit each of the entry's models sets for itself.
func (e *Entry) caps() map[string]uint64 {
	var out map[string]uint64
	for _, m := range e.Models {
		if m.MaxOutputTokens > 0 {
			if out == nil {
				out = map[string]uint64{}
			}
			out[m.UpstreamModel] = m.MaxOutputTokens
		}
	}
	return out
}

// Upstream is the model server this provider is to a node. ts is needed for
// an OAuth login only.
func (e *Entry) Upstream(ts TokenSource) (node.Upstream, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if e.Auth == AuthOAuth {
		if ts == nil {
			return nil, fmt.Errorf("provider %s: no token source", e.Name)
		}
		up, err := oauthUpstream(e, ts)
		if err != nil {
			return nil, err
		}
		return &billable{up: up}, nil
	}
	var up node.Upstream
	switch e.Type {
	case KindAnthropic:
		up = &anthropicUpstream{base: e.URL(), limits: outputLimits{set: e.caps()}, header: e.keyHeader()}
	case KindOpenAI:
		up = &openAIUpstream{base: e.URL(), header: e.keyHeader(), limits: outputLimits{set: e.caps()}, tokensField: "max_completion_tokens"}
	default:
		up = &openAIUpstream{base: e.URL(), header: e.keyHeader(), limits: outputLimits{set: e.caps()}}
	}
	return &billable{up: up}, nil
}

// billable keeps the token counts a provider reports within what a buyer
// accepts. A buyer checks the claimed completion tokens against the output
// it received (protocol.UsagePlausible) and rejects the whole job when the
// claim is far above it. Models that reason out of sight report tokens the
// buyer never saw: those cannot be billed, and claiming them would lose the
// job. The count the provider gave stays in the chunk as
// completion_tokens_reported.
type billable struct{ up node.Upstream }

func (b *billable) Stream(ctx context.Context, body []byte, emit func([]byte) error) error {
	var seen strings.Builder
	return b.up.Stream(ctx, body, func(payload []byte) error {
		info, err := protocol.ParseChunk(payload)
		if err != nil {
			return emit(payload)
		}
		seen.WriteString(info.Text())
		if info.Usage == nil {
			return emit(payload)
		}
		limit := 2*protocol.EstimateTokens(seen.String()) + 64
		if info.Usage.CompletionTokens <= limit {
			return emit(payload)
		}
		var chunk map[string]json.RawMessage
		var usage map[string]json.RawMessage
		if json.Unmarshal(payload, &chunk) != nil || json.Unmarshal(chunk["usage"], &usage) != nil {
			return emit(payload)
		}
		usage["completion_tokens_reported"] = usage["completion_tokens"]
		usage["completion_tokens"], _ = json.Marshal(limit)
		usage["total_tokens"], _ = json.Marshal(info.Usage.PromptTokens + limit)
		chunk["usage"], _ = json.Marshal(usage)
		out, err := json.Marshal(chunk)
		if err != nil {
			return emit(payload)
		}
		return emit(out)
	})
}

// keyHeader is what authenticates an API-key request.
func (e *Entry) keyHeader() http.Header {
	if e.Type == KindAnthropic {
		h := bearer("", e.Headers)
		h.Set("x-api-key", e.APIKey)
		return h
	}
	return bearer(e.APIKey, e.Headers)
}

// ListModels asks the provider which models the credentials may use. Logins
// that have no such list get the models known to work with them.
func (e *Entry) ListModels(ctx context.Context, ts TokenSource) ([]string, error) {
	var (
		ms  []string
		err error
	)
	switch {
	case e.Auth == AuthOAuth:
		ms, err = oauthModels(ctx, e, ts)
	case e.Type == KindAnthropic:
		ms, err = anthropicModels(ctx, e.URL(), e.keyHeader())
	default:
		ms, err = openAIModels(ctx, e.URL(), e.keyHeader())
	}
	var se *node.StatusError
	if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrKeyRefused, se.Status, clip(se.Body, 200))
	}
	return ms, err
}

// CheckResult is what Check found out.
type CheckResult struct {
	Models  []string `json:"models"`
	Balance string   `json:"balance,omitempty"` // the account's balance in words, where the provider tells
	// Unusable: the credentials are right, but the account cannot pay for requests.
	Unusable bool `json:"unusable,omitempty"`
}

// Check asks the provider whether it accepts the credentials, by listing the
// models, and reads the balance where there is one to read.
func (e *Entry) Check(ctx context.Context, ts TokenSource) (CheckResult, error) {
	var r CheckResult
	ms, err := e.ListModels(ctx, ts)
	if err != nil {
		return r, err
	}
	r.Models = ms
	if e.Type == KindDeepSeek && e.Auth == AuthAPIKey {
		if text, usable, err := deepseekBalance(ctx, e.URL(), e.keyHeader()); err == nil {
			r.Balance, r.Unusable = text, !usable
		}
	}
	return r, nil
}

// deepseekBalance reads GET /user/balance:
//
//	{"is_available": true, "balance_infos": [{"currency": "CNY", "total_balance": "110.00", …}]}
func deepseekBalance(ctx context.Context, base string, header http.Header) (string, bool, error) {
	var out struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency     string `json:"currency"`
			TotalBalance string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := getJSON(ctx, base+"/user/balance", header, &out); err != nil {
		return "", false, err
	}
	var parts []string
	for _, b := range out.BalanceInfos {
		parts = append(parts, b.TotalBalance+" "+b.Currency)
	}
	return strings.Join(parts, ", "), out.IsAvailable, nil
}

// ProbeResult is the answer to a test request.
type ProbeResult struct {
	Text             string `json:"text"`
	Reasoning        string `json:"reasoning,omitempty"`
	FinishReason     string `json:"finish_reason"`
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	// Reported is the completion count the provider gave when it is more than
	// can be billed: the model produced output the buyer does not get to see.
	Reported uint64 `json:"completion_tokens_reported,omitempty"`
	Chunks   int    `json:"chunks"`
}

// Probe sends one small request through up, the way a job would, and reads
// the answer the way a buyer would.
func Probe(ctx context.Context, up node.Upstream, model, prompt string) (ProbeResult, error) {
	var r ProbeResult
	if prompt == "" {
		prompt = "Reply with the single word: pong"
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": true, "max_tokens": 512,
		"stream_options": map[string]bool{"include_usage": true},
		"messages":       []map[string]string{{"role": "user", "content": prompt}},
	})
	err := up.Stream(ctx, body, func(payload []byte) error {
		info, err := protocol.ParseChunk(payload)
		if err != nil {
			return fmt.Errorf("the provider's answer is not an OpenAI chunk: %w", err)
		}
		r.Chunks++
		r.Text += info.Content
		r.Reasoning += info.Reasoning
		if info.FinishReason != "" {
			r.FinishReason = info.FinishReason
		}
		if info.Usage != nil {
			r.PromptTokens, r.CompletionTokens = info.Usage.PromptTokens, info.Usage.CompletionTokens
			var raw struct {
				Usage struct {
					Reported uint64 `json:"completion_tokens_reported"`
				} `json:"usage"`
			}
			_ = json.Unmarshal(payload, &raw)
			r.Reported = raw.Usage.Reported
		}
		return nil
	})
	if err == nil && r.Chunks == 0 {
		err = errors.New("the provider answered with an empty stream")
	}
	return r, err
}
