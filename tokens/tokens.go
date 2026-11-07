// Package tokens counts tokens for usage verification: deciding disputes and
// checking audited jobs.
//
// Two counters:
//
//   - Heuristic needs nothing but is an estimate (about 4 Latin characters or
//     1.5 CJK characters per token), so checks built on it use loose bounds and
//     catch only gross over-billing.
//   - Reference asks an inference server running the same open-weight model.
//     The prompt is counted by sending the original request with max_tokens=1
//     and reading usage.prompt_tokens: same tokenizer, same chat template, so
//     the count is what an honest node reports. Generated text is counted by
//     difference, prompt_tokens(text as a user message) - prompt_tokens(empty
//     message), which cancels the template overhead.
//
// The reference method needs only the OpenAI-compatible chat API every
// self-hosted server has (Ollama, vLLM, llama.cpp, SGLang), and it is checked
// against Ollama: prompt_tokens stays the full count when the prompt cache hits.
package tokens

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sharetoken/protocol"
)

// Counter counts tokens under a marketplace model's tokenizer.
type Counter interface {
	// CountPrompt counts the prompt of an OpenAI chat-completions request body.
	CountPrompt(ctx context.Context, model string, body []byte) (uint64, error)
	// CountText counts generated text.
	CountText(ctx context.Context, model, text string) (uint64, error)
	// Exact reports whether counts for model come from its real tokenizer.
	Exact(model string) bool
}

// Heuristic estimates without a tokenizer.
type Heuristic struct{}

// CountPrompt implements Counter.
func (Heuristic) CountPrompt(_ context.Context, _ string, body []byte) (uint64, error) {
	return protocol.EstimatePromptTokens(body)
}

// CountText implements Counter.
func (Heuristic) CountText(_ context.Context, _ string, text string) (uint64, error) {
	return protocol.EstimateTokens(text), nil
}

// Exact implements Counter.
func (Heuristic) Exact(string) bool { return false }

// Allowance is the most usage a verifier accepts for counted tokens: tight for
// an exact count (template and special-token slack only), loose for an estimate.
func Allowance(exact bool, countedPrompt, countedCompletion uint64) (prompt, completion uint64) {
	if exact {
		return countedPrompt + countedPrompt/20 + 16, countedCompletion + countedCompletion/20 + 16
	}
	return 3*countedPrompt + 512, 2*countedCompletion + 64
}

// Server is an OpenAI-compatible inference server running a model.
type Server struct {
	BaseURL string // up to and including /v1
	Model   string // the server's name for the model
	APIKey  string // optional
}

// Reference counts with reference servers, one per marketplace model, and
// falls back (to Heuristic by default) for models it has no server for.
type Reference struct {
	Servers  map[string]Server
	Fallback Counter
	Client   *http.Client
}

func (r *Reference) fallback() Counter {
	if r.Fallback != nil {
		return r.Fallback
	}
	return Heuristic{}
}

// Exact implements Counter.
func (r *Reference) Exact(model string) bool {
	if _, ok := r.Servers[model]; ok {
		return true
	}
	return r.fallback().Exact(model)
}

// CountPrompt implements Counter.
func (r *Reference) CountPrompt(ctx context.Context, model string, body []byte) (uint64, error) {
	srv, ok := r.Servers[model]
	if !ok {
		return r.fallback().CountPrompt(ctx, model, body)
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return 0, fmt.Errorf("tokens: request is not a JSON object: %w", err)
	}
	req := map[string]any{"messages": in["messages"]}
	for _, k := range []string{"tools", "tool_choice", "response_format"} {
		if v, ok := in[k]; ok {
			req[k] = v
		}
	}
	return r.promptTokens(ctx, srv, req)
}

// CountText implements Counter.
func (r *Reference) CountText(ctx context.Context, model, text string) (uint64, error) {
	srv, ok := r.Servers[model]
	if !ok {
		return r.fallback().CountText(ctx, model, text)
	}
	if text == "" {
		return 0, nil
	}
	msg := func(s string) map[string]any {
		return map[string]any{"messages": []map[string]string{{"role": "user", "content": s}}}
	}
	with, err := r.promptTokens(ctx, srv, msg(text))
	if err != nil {
		return 0, err
	}
	without, err := r.promptTokens(ctx, srv, msg(""))
	if err != nil {
		return 0, err
	}
	if with < without {
		return 0, errors.New("tokens: reference server returned inconsistent counts")
	}
	return with - without, nil
}

// promptTokens sends req with max_tokens=1 and returns usage.prompt_tokens.
func (r *Reference) promptTokens(ctx context.Context, srv Server, req map[string]any) (uint64, error) {
	req["model"] = srv.Model
	req["max_tokens"] = 1
	req["temperature"] = 0
	req["stream"] = false
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(srv.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if srv.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+srv.APIKey)
	}
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(hr)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("tokens: reference server status %d: %s", resp.StatusCode, bytes.TrimSpace(data[:min(len(data), 256)]))
	}
	var out struct {
		Usage *protocol.Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Usage == nil || out.Usage.PromptTokens == 0 {
		return 0, errors.New("tokens: reference server reported no usage")
	}
	return out.Usage.PromptTokens, nil
}
