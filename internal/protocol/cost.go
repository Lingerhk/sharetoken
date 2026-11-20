package protocol

import (
	"encoding/json"
	"errors"
	"math"
	"math/bits"
	"sharetoken/internal/receipt"
	"strings"
	"unicode"
)

// ErrOverflow is returned when a money computation would overflow uint64.
var ErrOverflow = errors.New("protocol: arithmetic overflow")

const perMillion = 1_000_000

// CostMicros is ceil((prompt*priceIn + completion*priceOut) / 1e6) in
// micro-credits. Prices are per million tokens. Overflow is an error, never a
// silent wrap.
func CostMicros(promptTokens, completionTokens, priceIn, priceOut uint64) (uint64, error) {
	a, err := mul(promptTokens, priceIn)
	if err != nil {
		return 0, err
	}
	b, err := mul(completionTokens, priceOut)
	if err != nil {
		return 0, err
	}
	sum, carry := bits.Add64(a, b, 0)
	if carry != 0 {
		return 0, ErrOverflow
	}
	q := sum / perMillion
	if sum%perMillion != 0 {
		q++
	}
	return q, nil
}

// Cost is ceil((uncached prompt*In + cache reads*CacheRead + cache writes*
// CacheWrite + completion*Out) / 1e6) in micro-credits. Overflow, and cache
// counts above the prompt, are errors.
func Cost(u Usage, p Prices) (uint64, error) {
	if u.CacheReadTokens > u.PromptTokens || u.CacheWriteTokens > u.PromptTokens-u.CacheReadTokens {
		return 0, errors.New("protocol: cached tokens exceed the prompt")
	}
	plain := u.PromptTokens - u.CacheReadTokens - u.CacheWriteTokens
	var sum uint64
	for _, term := range [][2]uint64{{plain, p.In}, {u.CacheReadTokens, p.CacheRead}, {u.CacheWriteTokens, p.CacheWrite}, {u.CompletionTokens, p.Out}} {
		v, err := mul(term[0], term[1])
		if err != nil {
			return 0, err
		}
		var carry uint64
		if sum, carry = bits.Add64(sum, v, 0); carry != 0 {
			return 0, ErrOverflow
		}
	}
	q := sum / perMillion
	if sum%perMillion != 0 {
		q++
	}
	return q, nil
}

// TicketCost prices usage at a ticket's rates, capped at its reservation:
// exactly what settlement will charge. A job between share partners is free.
func TicketCost(t *Ticket, u Usage) uint64 {
	if t.Shared {
		return 0
	}
	c, err := Cost(u, t.Prices())
	if err != nil || c > t.ReservedMicros {
		return t.ReservedMicros
	}
	return c
}

// ReceiptUsage is the usage a receipt bills.
func ReceiptUsage(r *receipt.Receipt) Usage {
	return Usage{PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens}
}

// MaxOutputTokens is the largest completion length c for which
// CostMicros(promptTokens, c) <= reservedMicros. The node caps max_tokens with
// it, so a job can never cost more than the buyer reserved.
func MaxOutputTokens(reservedMicros, promptTokens, priceIn, priceOut uint64) uint64 {
	budget, err := mul(reservedMicros, perMillion)
	if err != nil {
		return math.MaxUint64
	}
	promptCost, err := mul(promptTokens, priceIn)
	if err != nil || promptCost >= budget {
		return 0
	}
	if priceOut == 0 {
		return math.MaxUint64
	}
	return (budget - promptCost) / priceOut
}

func mul(a, b uint64) (uint64, error) {
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		return 0, ErrOverflow
	}
	return lo, nil
}

// EstimateTokens is a tokenizer-free estimate: about 4 characters per token for
// Latin text and 1.5 per token for CJK. It is used only for budget caps and
// plausibility checks, never for billing. For open-weight models a buyer can
// replace it with the model's public tokenizer and check usage exactly.
func EstimateTokens(s string) uint64 {
	var latin, cjk uint64
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			cjk++
		} else {
			latin++
		}
	}
	return (latin+3)/4 + (cjk*2+2)/3
}

const perMessageOverhead = 4 // role markers and separators in chat templates

// EstimatePromptTokens estimates the prompt size of a request in api from
// its text, tool definitions and attachments.
func EstimatePromptTokens(api string, body []byte) (uint64, error) {
	switch NormalAPI(api) {
	case APIMessages:
		return estimateMessages(body)
	case APIResponses:
		return estimateResponses(body)
	}
	return estimateChat(body)
}

// estimateChat estimates an OpenAI chat-completions body.
func estimateChat(body []byte) (uint64, error) {
	var req struct {
		Messages []struct {
			Content  json.RawMessage `json:"content"`
			Thinking []struct {
				Thinking string `json:"thinking"`
			} `json:"anthropic_thinking"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, err
	}
	var n uint64
	for _, m := range req.Messages {
		n += perMessageOverhead + EstimateTokens(messageText(m.Content)) + attachmentTokens(m.Content)
		for _, block := range m.Thinking {
			n += EstimateTokens(block.Thinking)
		}
	}
	if len(req.Tools) > 0 {
		n += EstimateTokens(string(req.Tools))
	}
	return n, nil
}

func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

// PaddedPrompt is the prompt allowance a node assumes when capping output
// (chat templates add tokens the estimate does not see).
func PaddedPrompt(est uint64) uint64 { return est + est/2 + 64 }

// HoldFor is the reservation for a capped request: enough that a node at
// these prices still allows maxTokens of output
// (MaxOutputTokens(HoldFor(...), PaddedPrompt(promptEst), …) >= maxTokens),
// with a more generous prompt allowance than PaddedPrompt so that a prompt
// that tokenizes larger than estimated is still paid in full.
func HoldFor(promptEst, maxTokens, priceIn, priceOut uint64) uint64 {
	pin, err1 := mul(2*promptEst+128, priceIn)
	pout, err2 := mul(maxTokens, priceOut)
	if err1 != nil || err2 != nil || pin+pout < pin {
		return math.MaxUint64
	}
	return (pin+pout+perMillion-1)/perMillion + 1
}
