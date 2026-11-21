package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Strict OpenAI-compatible servers each refuse a part of a chat request that
// OpenAI's own API takes, and say so in a 400 (after magpie's
// strict_refused.go, chat_text_parts.go, token_field.go, prefill.go and
// thinking_back.go). refusedParams asks such a server once more with that
// part repaired, and repairs it before the first try from then on, for that
// provider and model:
//
//   - "strict" on a function tool ("Unsupported Chat Completions API field:
//     tools[0].function.strict"): it goes, false being its default anyway;
//   - a message's content as a list of text parts where a string alone is
//     taken (a Pydantic server's "Input should be a valid string"): the
//     parts are joined into one string;
//   - the output limit under the name the server does not read ("Use
//     'max_completion_tokens' instead", or both names refused together): it
//     goes under the other name;
//   - a conversation that ends with the assistant's message, for a model
//     that does not continue one ("does not support assistant message
//     prefill"): a user turn asking it to continue goes after it;
//   - a history whose assistant messages lost their reasoning, for a
//     DeepSeek or Kimi model behind a relay ("The reasoning_content in the
//     thinking mode must be passed back to the API"): each gets a blank one,
//     as api.deepseek.com's are given from the start (strictChat).
var (
	toolStrictRefusal = regexp.MustCompile(`(?i)(?:unsupported|unknown|unrecognized|not (?:supported|allowed|permitted)|extra (?:fields?|inputs?|parameters?)|forbidden)[^\n{}]{0,120}?\btools(?:\[\d+\]|\.\d+)(?:\.[^\s.'"\x60]+)?\.strict\b|\btools(?:\[\d+\]|\.\d+)(?:\.[^\s.'"\x60]+)?\.strict\b['"\x60]?[^\n{}]{0,40}?(?:unsupported|unknown|unrecognized|not (?:supported|allowed|permitted)|extra_forbidden|forbidden)|"loc"\s*:\s*\[\s*(?:"body"\s*,\s*)?"tools"\s*,\s*\d+\s*,\s*(?:"function"\s*,\s*)?"strict"\s*\][^{}]*"type"\s*:\s*"extra_forbidden"|"type"\s*:\s*"extra_forbidden"[^{}]*"loc"\s*:\s*\[\s*(?:"body"\s*,\s*)?"tools"\s*,\s*\d+\s*,\s*(?:"function"\s*,\s*)?"strict"\s*\]`)
	textPartsRefusal  = regexp.MustCompile(`(?i)input should be a valid string|messages\[\d+\]\.content[^\n]{0,60}(?:must be|should be|expected)(?: a| of type)? string|content[^\n]{0,40}(?:must be|should be) (?:a )?string|invalid type for 'messages\[\d+\]\.content'`)
	tokenFieldAsked   = regexp.MustCompile("(?i)\\buse\\s+['\"`]?(max_completion_tokens|max_tokens)['\"`]?")
	tokenFieldsBoth   = regexp.MustCompile(`(?i)max_(?:completion_)?tokens[^\n]{0,60}(?:mutually exclusive|cannot (?:both )?be (?:set|used|specified) (?:together|at the same time)|not both)|(?:mutually exclusive|only one of)[^\n]{0,60}max_(?:completion_)?tokens`)
	prefillRefusal    = regexp.MustCompile(`(?i)does not support (?:assistant message )?prefill|requests ending with a model turn are not supported|last message cannot be from the assistant`)
	thinkingBackWords = regexp.MustCompile("(?i)`?(?:content\\[\\]\\.thinking|reasoning_content)`? in the thinking mode must be passed back")
	reasoningMissing  = regexp.MustCompile(`(?i)\breasoning_content\b[^"]{0,40}\b(?:is (?:missing|required)|must be (?:passed|provided|included))|\b(?:missing|required)\b[^"]{0,80}\breasoning_content\b`)
)

// continueText is the user turn put after a conversation that ends with the
// assistant's message, for a model that takes no prefill.
const continueText = "Continue."

const (
	maxTokensField       = "max_tokens"
	completionTokenField = "max_completion_tokens"
)

// repairShape changes req the way a strict server's refusal asks (see
// above), and notes it in learnt. It reports whether there was anything to
// change.
func repairShape(req map[string]json.RawMessage, refusal string, learnt *modelRepairs) bool {
	switch {
	case toolStrictRefusal.MatchString(refusal):
		if !learnt.noToolStrict && dropToolStrict(req) {
			learnt.noToolStrict = true
			return true
		}
	case textPartsRefusal.MatchString(refusal):
		if !learnt.textAsString && joinTextParts(req) {
			learnt.textAsString = true
			return true
		}
	case prefillRefusal.MatchString(refusal):
		if !learnt.userLast && continueAfterAssistant(req) {
			learnt.userLast = true
			return true
		}
	case thinkingBackWords.MatchString(refusal) || reasoningMissing.MatchString(refusal):
		if !learnt.reasoningBack && reasoningBack(req) {
			learnt.reasoningBack = true
			return true
		}
	}
	return repairTokenField(req, refusal, learnt)
}

// repairTokenField renames the output limit when the refusal names the
// other field as the one to send, or refuses both together (most servers
// that know one read max_tokens).
func repairTokenField(req map[string]json.RawMessage, refusal string, learnt *modelRepairs) bool {
	want := ""
	if m := tokenFieldAsked.FindStringSubmatch(refusal); m != nil {
		want = strings.ToLower(m[1])
	} else if tokenFieldsBoth.MatchString(refusal) {
		want = maxTokensField
	}
	if want == "" || learnt.tokenField == want || !withTokenField(req, want) {
		return false
	}
	learnt.tokenField = want
	return true
}

// withTokenField names a chat request's output limit want; a request that
// names both keeps the lower. It reports whether req changed.
func withTokenField(req map[string]json.RawMessage, want string) bool {
	other := maxTokensField
	if want == maxTokensField {
		other = completionTokenField
	}
	from, ok := req[other]
	if !ok {
		return false
	}
	delete(req, other)
	var a, b uint64
	if json.Unmarshal(from, &a) == nil && json.Unmarshal(req[want], &b) == nil && b > 0 && b < a {
		return true // the lower one stays
	}
	req[want] = from
	return true
}

// dropToolStrict deletes strict from a chat or Responses request's function
// tools, and reports whether there was any.
func dropToolStrict(req map[string]json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(req["tools"], &tools) != nil {
		return false
	}
	changed := false
	for _, t := range tools {
		if _, ok := t["strict"]; ok { // Responses
			delete(t, "strict")
			changed = true
		}
		var fn map[string]json.RawMessage
		if json.Unmarshal(t["function"], &fn) != nil || fn == nil {
			continue
		}
		if _, ok := fn["strict"]; ok { // chat
			delete(fn, "strict")
			t["function"], _ = json.Marshal(fn)
			changed = true
		}
	}
	if changed {
		req["tools"], _ = json.Marshal(tools)
	}
	return changed
}

// chatMessages reads a chat request's messages; edit changes them and
// reports whether it did, and they are written back when it did.
func chatMessages(req map[string]json.RawMessage, edit func([]map[string]json.RawMessage) ([]map[string]json.RawMessage, bool)) bool {
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(req["messages"], &msgs) != nil || len(msgs) == 0 {
		return false
	}
	msgs, changed := edit(msgs)
	if changed {
		req["messages"], _ = json.Marshal(msgs)
	}
	return changed
}

// joinTextParts turns each message content that is a list of plain text
// parts into the string they say, a newline between parts (as vLLM's chat
// template renders such a list). A list with anything else (an image, a
// cache mark) stays as it is.
func joinTextParts(req map[string]json.RawMessage) bool {
	return chatMessages(req, func(msgs []map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
		changed := false
		for _, m := range msgs {
			if text, ok := plainTextParts(m["content"]); ok {
				m["content"], _ = json.Marshal(text)
				changed = true
			}
		}
		return msgs, changed
	})
}

// plainTextParts is the text of a content list whose every part is
// {"type":"text","text":"…"} and nothing more.
func plainTextParts(raw json.RawMessage) (string, bool) {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", false
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		var typ, text string
		if len(p) != 2 || json.Unmarshal(p["type"], &typ) != nil || typ != "text" || json.Unmarshal(p["text"], &text) != nil {
			return "", false
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n"), true
}

// continueAfterAssistant puts a user turn after a conversation that ends
// with the assistant's message (not with its tool calls).
func continueAfterAssistant(req map[string]json.RawMessage) bool {
	return chatMessages(req, func(msgs []map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
		last := msgs[len(msgs)-1]
		var role string
		_ = json.Unmarshal(last["role"], &role)
		var calls []json.RawMessage
		if role != "assistant" || json.Unmarshal(last["tool_calls"], &calls) == nil && len(calls) > 0 {
			return msgs, false
		}
		turn := map[string]json.RawMessage{"role": json.RawMessage(`"user"`)}
		turn["content"], _ = json.Marshal(continueText)
		return append(msgs, turn), true
	})
}

// reasoningBack gives each assistant message of the history without
// reasoning_content a blank one, which DeepSeek and Kimi take in its place.
func reasoningBack(req map[string]json.RawMessage) bool {
	return chatMessages(req, func(msgs []map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
		changed := false
		for _, m := range msgs {
			if fillReasoning(m) {
				changed = true
			}
		}
		return msgs, changed
	})
}

// fillReasoning gives an assistant message without reasoning_content a
// blank one, and reports whether it did.
func fillReasoning(m map[string]json.RawMessage) bool {
	var role, reasoning string
	_ = json.Unmarshal(m["role"], &role)
	if role != "assistant" {
		return false
	}
	if _ = json.Unmarshal(m["reasoning_content"], &reasoning); reasoning != "" {
		return false
	}
	m["reasoning_content"] = json.RawMessage(`" "`)
	return true
}

// chatCallIDs gives every tool call of a chat request that came with no id
// one, and the tool messages after it that answer with no tool_call_id the
// same, in the calls' order (magpie's chat_call_ids.go). A model server that
// sometimes streams a call without an id leaves the client holding "", and a
// strict one refuses every later request of the conversation for it. The id
// comes from the call's place and what it says, never at random, so the next
// turn sends the same history and the server's prompt cache still holds. A
// request whose calls all have ids comes back as it was.
func chatCallIDs(body []byte) []byte {
	if !strings.Contains(string(body), `"tool_calls"`) {
		return body
	}
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	changed := chatMessages(req, func(msgs []map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
		changed := false
		var open []string // the latest call turn's made ids no result has taken yet
		for at, m := range msgs {
			var role string
			_ = json.Unmarshal(m["role"], &role)
			switch role {
			case "assistant":
				var calls []map[string]json.RawMessage
				if json.Unmarshal(m["tool_calls"], &calls) != nil || len(calls) == 0 {
					continue
				}
				open = nil
				for i, c := range calls {
					if stringField(c["id"]) != "" {
						continue
					}
					var fn struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					}
					_ = json.Unmarshal(c["function"], &fn)
					id := placedCallID(at, i, fn.Name, string(fn.Arguments))
					c["id"], _ = json.Marshal(id)
					open = append(open, id)
				}
				if open != nil {
					m["tool_calls"], _ = json.Marshal(calls)
					changed = true
				}
			case "tool":
				if len(open) > 0 && stringField(m["tool_call_id"]) == "" {
					m["tool_call_id"], _ = json.Marshal(open[0])
					open = open[1:]
					changed = true
				}
			}
		}
		return msgs, changed
	})
	if !changed {
		return body
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// stringField reads a JSON string: "" for null, none or another kind.
func stringField(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// placedCallID is the id chatCallIDs gives the i-th call of the message at
// msg: the same for the same call at the same place on every request.
func placedCallID(msg, i int, name, args string) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(msg) + "." + strconv.Itoa(i) + "\x00" + name + "\x00" + args))
	return "call_" + hex.EncodeToString(sum[:12])
}
