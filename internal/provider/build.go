package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
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
	client, err := e.httpClient()
	if err != nil {
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
		return wrapOutbound(&outboundUpstream{up: up, client: client, owner: e.ID}), nil
	}
	var up node.Upstream
	switch e.Type {
	case KindAnthropic:
		up = &anthropicUpstream{base: e.URL(), limits: outputLimits{set: e.caps()}, header: e.keyHeader(), entry: e}
	case KindOpenAI:
		up = &openAIUpstream{base: e.URL(), header: e.keyHeader(), limits: outputLimits{set: e.caps()}, tokensField: "max_completion_tokens", responses: true}
	case KindDeepSeek:
		// DeepSeek also serves the Anthropic Messages API, at /anthropic.
		up = (&openAIUpstream{base: e.URL(), header: e.keyHeader(), limits: outputLimits{set: e.caps()}, messages: strings.TrimRight(e.URL(), "/") + "/anthropic"}).chatQuirks(e.Type)
	case KindGemini:
		up = &openAIUpstream{base: e.URL(), header: e.keyHeader(), limits: outputLimits{set: e.caps()}, thoughtSignatures: true}
	default:
		up = (&openAIUpstream{base: e.URL(), header: e.keyHeader(), limits: outputLimits{set: e.caps()}}).chatQuirks(e.Type)
	}
	return wrapOutbound(&outboundUpstream{up: up, client: client, owner: e.ID}), nil
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

// ListModels returns the catalogue. DiscoverModels also distinguishes an
// upstream response from a fallback list that has not verified model access.
func (e *Entry) ListModels(ctx context.Context, ts TokenSource) ([]string, error) {
	r, err := e.DiscoverModels(ctx, ts)
	return r.Models, err
}

func (e *Entry) listModels(ctx context.Context, ts TokenSource) ([]string, error) {
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
	Models       []string `json:"models"`
	Source       string   `json:"source"`
	Verification string   `json:"verification"`
	Warning      string   `json:"warning,omitempty"`
	Balance      string   `json:"balance,omitempty"` // the account's balance in words, where the provider tells
	// Unusable: the credentials are right, but the account cannot pay for requests.
	Unusable bool `json:"unusable,omitempty"`
}

// DiscoverModels keeps a usable catalogue without presenting fallback IDs as
// evidence that the upstream accepted the credentials or a Messages request.
func (e *Entry) DiscoverModels(ctx context.Context, ts TokenSource) (CheckResult, error) {
	r := CheckResult{Models: []string{}, Source: "upstream", Verification: "unverified"}
	client, err := e.httpClient()
	if err != nil {
		return r, err
	}
	ms, err := e.listModels(withHTTPClient(ctx, client), ts)
	if err == nil && len(ms) > 0 {
		r.Models = ms
		r.Verification = "credentials"
		return r, nil
	}
	if e.Type != KindAnthropic || e.Auth != AuthOAuth {
		return r, err
	}
	if errors.Is(err, ErrKeyRefused) || ctx.Err() != nil {
		r.Verification = "failed"
		return r, err
	}
	r.Models = append([]string(nil), claudeOAuthModels...)
	r.Source = "fallback"
	r.Warning = "Model access has not been verified; run a provider test."
	if err != nil {
		r.Warning += " " + clip(err.Error(), 200)
	}
	return r, nil
}

// Check asks the provider whether it accepts the credentials, by listing the
// models, and reads the balance where there is one to read.
func (e *Entry) Check(ctx context.Context, ts TokenSource) (CheckResult, error) {
	r, err := e.DiscoverModels(ctx, ts)
	if err != nil {
		return r, err
	}
	if e.Type == KindDeepSeek && e.Auth == AuthAPIKey {
		client, _ := e.httpClient()
		if text, usable, err := deepseekBalance(withHTTPClient(ctx, client), e.URL(), e.keyHeader()); err == nil {
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
	Reported       uint64 `json:"completion_tokens_reported,omitempty"`
	PromptReported uint64 `json:"prompt_tokens_reported,omitempty"`
	Chunks         int    `json:"chunks"`
}

// Probe sends one small chat request through up, the way a job would
// (converted when up does not speak chat), and reads the answer the way a
// buyer would.
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
	var meter protocol.UsageMeter
	var text strings.Builder
	read := func(payload []byte) error {
		info, err := protocol.ParseChunk(payload)
		if err != nil {
			return fmt.Errorf("the provider's answer is not an OpenAI chunk: %w", err)
		}
		r.Chunks++
		r.Text += info.Content
		r.Reasoning += info.Reasoning
		text.WriteString(info.Text())
		if info.FinishReason != "" {
			r.FinishReason = info.FinishReason
		}
		meter.Add(info)
		return nil
	}
	req := &node.Request{API: protocol.APIChat, Body: body, Limit: 512}
	emit := read
	var tr apicompat.Translator
	if !slices.Contains(up.APIs(), protocol.APIChat) {
		target, ok := apicompat.Target(protocol.APIChat, up.APIs())
		if !ok {
			return r, fmt.Errorf("the provider speaks none of the APIs this client converts to")
		}
		conv, err := apicompat.Convert(protocol.APIChat, target, body, apicompat.Options{Model: model})
		if err != nil {
			return r, err
		}
		req = &node.Request{API: target, Body: conv.Body, Limit: 512}
		tr = conv.Translator
		emit = func(p []byte) error { return tr.Event(p, read) }
	}
	err := up.Stream(ctx, req, emit)
	if err == nil && tr != nil {
		err = tr.Close(read)
	}
	if u := meter.Usage(); u != nil {
		r.PromptTokens, r.CompletionTokens = u.PromptTokens, u.CompletionTokens
		// What the buyer's rules let the seller bill: hidden reasoning up to
		// the job's limit, visible output by its text.
		billed := protocol.BillableUsage(*u, u.PromptTokens, protocol.EstimateTokens(text.String()), meter.Hidden(), 512)
		if billed.CompletionTokens < u.CompletionTokens {
			r.Reported, r.CompletionTokens = u.CompletionTokens, billed.CompletionTokens
		}
	}
	if err == nil && r.Chunks == 0 {
		err = errors.New("the provider answered with an empty stream")
	}
	return r, err
}
