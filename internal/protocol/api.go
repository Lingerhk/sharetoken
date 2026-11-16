package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The APIs a buyer's request may be in. The ticket names one; the node reads
// the request and writes every response event in it.
const (
	APIChat      = "chat"      // OpenAI chat completions
	APIMessages  = "messages"  // Anthropic Messages (Claude Code)
	APIResponses = "responses" // OpenAI Responses (Codex)
)

// AllAPIs lists the APIs in a fixed order.
var AllAPIs = []string{APIChat, APIMessages, APIResponses}

// NormalAPI maps the empty API of an older request to APIChat.
func NormalAPI(api string) string {
	if api == "" {
		return APIChat
	}
	return api
}

// ValidAPI reports whether api is one of the APIs (the empty one included).
func ValidAPI(api string) bool { return slices.Contains(AllAPIs, NormalAPI(api)) }

// Prices are the rates of one model, in micro-credits per million tokens.
type Prices struct {
	In, Out, CacheRead, CacheWrite uint64
}

// PromptCeiling is the most one prompt token can cost: the input price, or
// the cache write price where that is higher. Output caps and holds assume it,
// so a prompt that turns out to be written to the cache stays covered.
func (p Prices) PromptCeiling() uint64 { return max(p.In, p.CacheWrite) }

// Usage is the token accounting of one job. PromptTokens counts all input,
// the cached part included; CacheReadTokens and CacheWriteTokens are the part
// of it read from and written to the model server's prompt cache.
// ReasoningTokens is the part of CompletionTokens the model server reported as
// reasoning, which it may not show.
type Usage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	CacheReadTokens  uint64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens uint64 `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  uint64 `json:"reasoning_tokens,omitempty"`
}

// ChunkInfo is what the node and the buyer each extract from one streamed
// event payload.
type ChunkInfo struct {
	Content           string
	Reasoning         string
	ToolArgs          string
	ToolCalls         []ToolCallDelta // chat only
	FinishReason      string
	Usage             *Usage
	AnthropicThinking []json.RawMessage // chat only: thinking blocks passed back
	// Hidden reports that the model reasoned in a way the event does not show
	// in full (encrypted, redacted or omitted thinking, reported reasoning
	// tokens): the output may cost more than its visible text.
	Hidden bool

	msgUsage *messagesUsage // Messages: the usage fields this event carries
}

// Text is everything in the event that consumed completion tokens and the
// buyer can see.
func (c ChunkInfo) Text() string { return c.Content + c.Reasoning + c.ToolArgs }

// ParseEvent extracts what an event payload of api carries.
func ParseEvent(api string, payload []byte) (ChunkInfo, error) {
	switch NormalAPI(api) {
	case APIChat:
		return ParseChunk(payload)
	case APIMessages:
		return parseMessagesEvent(payload)
	case APIResponses:
		return parseResponsesEvent(payload)
	}
	return ChunkInfo{}, fmt.Errorf("protocol: unknown API %q", api)
}

// messagesUsage is a Messages usage object; nil fields were absent.
type messagesUsage struct {
	InputTokens         *uint64 `json:"input_tokens"`
	OutputTokens        *uint64 `json:"output_tokens"`
	CacheCreationTokens *uint64 `json:"cache_creation_input_tokens"`
	CacheReadTokens     *uint64 `json:"cache_read_input_tokens"`
}

func parseMessagesEvent(payload []byte) (ChunkInfo, error) {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Usage *messagesUsage `json:"usage"`
		} `json:"message"`
		ContentBlock struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			Name     string          `json:"name"`
			ID       string          `json:"id"`
			Input    json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage *messagesUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return ChunkInfo{}, err
	}
	var info ChunkInfo
	switch ev.Type {
	case "message_start":
		info.msgUsage = ev.Message.Usage
	case "content_block_start":
		switch cb := ev.ContentBlock; cb.Type {
		case "text":
			info.Content = cb.Text
		case "thinking":
			info.Reasoning, info.Hidden = cb.Thinking, true
		case "redacted_thinking":
			info.Hidden = true
		case "tool_use":
			if in := bytes.TrimSpace(cb.Input); len(in) > 0 && !bytes.Equal(in, []byte("{}")) && !bytes.Equal(in, []byte("null")) {
				info.ToolArgs = string(in)
			}
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			info.Content = ev.Delta.Text
		case "thinking_delta":
			info.Reasoning = ev.Delta.Thinking
		case "input_json_delta":
			info.ToolArgs = ev.Delta.PartialJSON
		}
	case "message_delta":
		info.FinishReason = ev.Delta.StopReason
		info.msgUsage = ev.Usage
	}
	if u := info.msgUsage; u != nil {
		info.Usage = u.usage()
	}
	return info, nil
}

func (u *messagesUsage) usage() *Usage {
	v := func(p *uint64) uint64 {
		if p == nil {
			return 0
		}
		return *p
	}
	cr, cw := v(u.CacheReadTokens), v(u.CacheCreationTokens)
	return &Usage{PromptTokens: v(u.InputTokens) + cr + cw, CompletionTokens: v(u.OutputTokens), CacheReadTokens: cr, CacheWriteTokens: cw}
}

func parseResponsesEvent(payload []byte) (ChunkInfo, error) {
	var ev struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Item  struct {
			Type string `json:"type"`
		} `json:"item"`
		Response struct {
			Status            string `json:"status"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Output []struct {
				Type string `json:"type"`
			} `json:"output"`
			Usage *struct {
				InputTokens        uint64 `json:"input_tokens"`
				OutputTokens       uint64 `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens        uint64 `json:"cached_tokens"`
					CacheCreationTokens uint64 `json:"cache_creation_tokens"`
				} `json:"input_tokens_details"`
				OutputTokensDetails struct {
					ReasoningTokens uint64 `json:"reasoning_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return ChunkInfo{}, err
	}
	var info ChunkInfo
	switch ev.Type {
	case "response.output_text.delta", "response.refusal.delta":
		info.Content = ev.Delta
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		info.Reasoning = ev.Delta
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		info.ToolArgs = ev.Delta
	case "response.output_item.added":
		info.Hidden = ev.Item.Type == "reasoning" || ev.Item.Type == "compaction"
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		r := ev.Response
		info.FinishReason = r.Status
		if r.IncompleteDetails.Reason != "" {
			info.FinishReason = r.IncompleteDetails.Reason
		}
		for _, o := range r.Output {
			if o.Type == "reasoning" || o.Type == "compaction" {
				info.Hidden = true
			}
		}
		if u := r.Usage; u != nil {
			info.Usage = &Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens,
				CacheReadTokens: u.InputTokensDetails.CachedTokens, CacheWriteTokens: u.InputTokensDetails.CacheCreationTokens,
				ReasoningTokens: u.OutputTokensDetails.ReasoningTokens}
			info.Hidden = info.Hidden || u.OutputTokensDetails.ReasoningTokens > 0
		}
	}
	return info, nil
}

// UsageMeter follows the usage a stream reports. Chat and Responses report it
// once, at the end; Messages reports the input at the start and the output
// (sometimes the input again) at the end.
type UsageMeter struct {
	u      *Usage
	msg    messagesUsage
	hidden bool
}

// Add takes one event's information.
func (m *UsageMeter) Add(info ChunkInfo) {
	m.hidden = m.hidden || info.Hidden
	if u := info.msgUsage; u != nil {
		set := func(dst **uint64, src *uint64) {
			if src != nil && (*src > 0 || *dst == nil) {
				*dst = src
			}
		}
		set(&m.msg.InputTokens, u.InputTokens)
		set(&m.msg.OutputTokens, u.OutputTokens)
		set(&m.msg.CacheReadTokens, u.CacheReadTokens)
		set(&m.msg.CacheCreationTokens, u.CacheCreationTokens)
		m.u = m.msg.usage()
		return
	}
	if info.Usage != nil {
		u := *info.Usage
		m.u = &u
		m.hidden = m.hidden || u.ReasoningTokens > 0
	}
}

// Usage is the last usage reported, or nil when there was none.
func (m *UsageMeter) Usage() *Usage {
	if m.u == nil {
		return nil
	}
	u := *m.u
	return &u
}

// Hidden reports whether the stream showed signs of reasoning it does not
// show in full.
func (m *UsageMeter) Hidden() bool { return m.hidden }

// RequestedOutput is the output limit a request asks for (0: none).
func RequestedOutput(api string, body []byte) uint64 {
	var r struct {
		MaxTokens           uint64 `json:"max_tokens"`
		MaxCompletionTokens uint64 `json:"max_completion_tokens"`
		MaxOutputTokens     uint64 `json:"max_output_tokens"`
	}
	if json.Unmarshal(body, &r) != nil {
		return 0
	}
	switch NormalAPI(api) {
	case APIResponses:
		return r.MaxOutputTokens
	case APIMessages:
		return r.MaxTokens
	}
	if r.MaxCompletionTokens > 0 && (r.MaxTokens == 0 || r.MaxCompletionTokens < r.MaxTokens) {
		return r.MaxCompletionTokens
	}
	return r.MaxTokens
}

// Truncated reports whether a finish reason of api says the answer ran into
// its output limit rather than ending on its own.
func Truncated(api, finish string) bool {
	switch NormalAPI(api) {
	case APIMessages:
		return finish == "max_tokens" || finish == "model_context_window_exceeded"
	case APIResponses:
		return finish == "max_output_tokens" || finish == "incomplete"
	}
	return finish == "length"
}

// HardMaxOutput caps a single job's output whatever the budget: coding agents
// (Messages, Responses) write long answers, chat clients rarely do.
func HardMaxOutput(api string) uint64 {
	if NormalAPI(api) == APIChat {
		return 32768
	}
	return 128000
}

// OutputLimit is the output-token ceiling of the job of ticket t: what the
// reservation covers after a padded prompt estimate, at most HardMaxOutput,
// and at most what the request asks for. The node asks the model server for
// no more; the buyer computes the same number.
func OutputLimit(t *Ticket, estPrompt, requested uint64) uint64 {
	limit := min(MaxOutputTokens(t.ReservedMicros, PaddedPrompt(estPrompt), t.Prices().PromptCeiling(), t.PriceOutMicros), HardMaxOutput(t.API))
	if requested > 0 {
		limit = min(limit, requested)
	}
	return limit
}

// UsagePlausible rejects usage claims far above what the buyer can see. The
// bounds are loose on purpose (tokenizers and chat templates vary); the check
// exists to catch inflated billing, not to second-guess honest tokenizers.
//
// Output the response shows is bounded by its text. A response that shows
// signs of reasoning it does not reveal in full (hidden) may be billed what
// the model server reported, up to the job's output limit (the most the model
// server could have produced). Cached prompt tokens are part of the prompt.
func UsagePlausible(u Usage, estPrompt, estVisible uint64, hidden bool, outputLimit uint64) error {
	if u.PromptTokens > promptBound(estPrompt) {
		return fmt.Errorf("claimed %d prompt tokens, estimate %d", u.PromptTokens, estPrompt)
	}
	if u.CacheReadTokens > u.PromptTokens || u.CacheWriteTokens > u.PromptTokens-u.CacheReadTokens {
		return fmt.Errorf("claimed %d cache read and %d cache write tokens of %d prompt tokens", u.CacheReadTokens, u.CacheWriteTokens, u.PromptTokens)
	}
	if b := completionBound(estVisible, hidden, outputLimit); u.CompletionTokens > b {
		return fmt.Errorf("claimed %d completion tokens, at most %d accepted (visible output estimate %d, hidden reasoning %t)", u.CompletionTokens, b, estVisible, hidden)
	}
	return nil
}

func promptBound(est uint64) uint64 { return 3*est + 512 }

func completionBound(estVisible uint64, hidden bool, outputLimit uint64) uint64 {
	b := 2*estVisible + 64
	if hidden {
		b = max(b, outputLimit)
	}
	return b
}

// BillableUsage lowers a model server's report to what UsagePlausible
// accepts: a node signs this, so an honest node's receipt is never rejected
// for a model server that counts more than the buyer can check. What cannot
// be billed is the seller's loss.
func BillableUsage(u Usage, estPrompt, estVisible uint64, hidden bool, outputLimit uint64) Usage {
	u = u.CapPrompt(promptBound(estPrompt))
	u.CacheReadTokens = min(u.CacheReadTokens, u.PromptTokens)
	u.CacheWriteTokens = min(u.CacheWriteTokens, u.PromptTokens-u.CacheReadTokens)
	u.CompletionTokens = min(u.CompletionTokens, completionBound(estVisible, hidden, outputLimit))
	u.ReasoningTokens = min(u.ReasoningTokens, u.CompletionTokens)
	return u
}

// CapPrompt lowers the prompt tokens to at most limit, the uncached part
// first, then cache writes, then cache reads (the cheapest are kept longest
// out of fairness to the seller: they are what it is most sure it served).
func (u Usage) CapPrompt(limit uint64) Usage {
	u.CacheReadTokens = min(u.CacheReadTokens, u.PromptTokens)
	u.CacheWriteTokens = min(u.CacheWriteTokens, u.PromptTokens-u.CacheReadTokens)
	if u.PromptTokens <= limit {
		return u
	}
	over := u.PromptTokens - limit
	plain := u.PromptTokens - u.CacheReadTokens - u.CacheWriteTokens
	cut := min(over, plain)
	over -= cut
	w := min(over, u.CacheWriteTokens)
	u.CacheWriteTokens -= w
	over -= w
	u.CacheReadTokens -= min(over, u.CacheReadTokens)
	u.PromptTokens = limit
	return u
}

// RequestHeader carries the HTTP headers of a buyer's request that matter to
// the model server (anthropic-beta, anthropic-version, …) and sharetoken's own
// (HeaderOp), lower-case. It travels sealed with the body.
type RequestHeader map[string]string

// HeaderOp names an operation other than a plain request: OpCompact.
const HeaderOp = "x-sharetoken-op"

// OpCompact is a Codex /responses/compact request: the answer is one
// compaction item, in a response.completed event.
const OpCompact = "compact"

const maxRequestHeader = 16 << 10

// JoinRequest is the plaintext of a request: the header as JSON, length
// prefixed, then the body as the client sent it.
func JoinRequest(h RequestHeader, body []byte) ([]byte, error) {
	var hj []byte
	if len(h) > 0 {
		var err error
		if hj, err = json.Marshal(h); err != nil {
			return nil, err
		}
	}
	if len(hj) > maxRequestHeader {
		return nil, Errorf(CodeBadRequest, "request headers exceed %d bytes", maxRequestHeader)
	}
	out := binary.AppendUvarint(make([]byte, 0, len(hj)+len(body)+3), uint64(len(hj)))
	return append(append(out, hj...), body...), nil
}

// SplitRequest reverses JoinRequest.
func SplitRequest(plain []byte) (RequestHeader, []byte, error) {
	n, k := binary.Uvarint(plain)
	if k <= 0 || n > maxRequestHeader || n > uint64(len(plain)-k) {
		return nil, nil, errors.New("protocol: bad request header length")
	}
	var h RequestHeader
	if n > 0 {
		if err := json.Unmarshal(plain[k:k+int(n)], &h); err != nil {
			return nil, nil, fmt.Errorf("protocol: bad request header: %w", err)
		}
	}
	return h, plain[k+int(n):], nil
}

// Get returns a header's value; names are case-insensitive.
func (h RequestHeader) Get(name string) string { return h[strings.ToLower(name)] }
