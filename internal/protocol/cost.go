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
//
// Fields are matched by their exact names, as the node matches them when it
// builds the model server's request (encoding/json would also take
// "Messages" for "messages", and the later of the two). And the estimate is
// never below a floor set by the body's size, so that what a request carries
// in fields the estimate does not know still counts: a node may bill up to
// promptBound(estimate), and pays its model server for all of it.
func EstimatePromptTokens(api string, body []byte) (uint64, error) {
	var (
		n      uint64
		opaque int
		err    error
	)
	switch NormalAPI(api) {
	case APIMessages:
		n, opaque, err = estimateMessages(body)
	case APIResponses:
		n, opaque, err = estimateResponses(body)
	default:
		n, opaque, err = estimateChat(body)
	}
	if err != nil {
		return 0, err
	}
	return max(n, sizeFloor(len(body), opaque)), nil
}

// floorBytesPerToken is deliberately high (JSON and code run 3 to 4 bytes a
// token): the floor only has to keep promptBound above what a model server
// can count in len(body) bytes, not to estimate them.
const floorBytesPerToken = 8

// sizeFloor is the least a body of size bytes is estimated at; opaque are the
// bytes of attachments, which are estimated by kind (an image costs the same
// whatever its size).
func sizeFloor(size, opaque int) uint64 {
	if opaque >= size {
		return 0
	}
	return uint64(size-opaque) / floorBytesPerToken
}

// fields decodes a JSON object keeping its keys as they are written.
func fields(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("protocol: the request is not a JSON object")
	}
	return m, nil
}

// items decodes a JSON array of objects (nil when raw is not one).
func items(raw json.RawMessage) []map[string]json.RawMessage {
	var list []map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &list) != nil {
		return nil
	}
	return list
}

// estimateChat estimates an OpenAI chat-completions body.
func estimateChat(body []byte) (n uint64, opaque int, err error) {
	req, err := fields(body)
	if err != nil {
		return 0, 0, err
	}
	if raw := req["messages"]; len(raw) > 0 && string(raw) != "null" {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return 0, 0, err
		}
		for _, one := range list {
			m, err := fields(one)
			if err != nil {
				return 0, 0, err
			}
			att, skipped := attachmentTokens(m["content"])
			n += perMessageOverhead + EstimateTokens(messageText(m["content"])) + att
			opaque += skipped
			for _, block := range items(m["anthropic_thinking"]) {
				var s string
				if json.Unmarshal(block["thinking"], &s) == nil {
					n += EstimateTokens(s)
				}
			}
		}
	}
	if tools := req["tools"]; len(tools) > 0 && string(tools) != "null" {
		n += EstimateTokens(string(tools))
	}
	return n, opaque, nil
}

func messageText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			var text string
			if json.Unmarshal(p["text"], &text) == nil {
				b.WriteString(text)
			}
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
