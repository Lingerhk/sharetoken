package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// A chat request with everything the converters have to carry over (the
// provider package's test request).
const chatFullRequest = `{"model":"m-1","stream":true,"stream_options":{"include_usage":true},"max_tokens":900,"temperature":0.4,"top_p":0.9,"stop":["END"],
"messages":[
 {"role":"system","content":"Be brief."},
 {"role":"user","content":[{"type":"text","text":"What is in the picture?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]},
 {"role":"assistant","content":"Let me look.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"cat\"}"}}]},
 {"role":"tool","tool_call_id":"call_1","content":"a cat"},
 {"role":"user","content":"And now?"}],
"tools":[{"type":"function","function":{"name":"lookup","description":"Look something up","parameters":{"type":"object","properties":{"q":{"type":["string","null"],"minLength":1}},"required":["q","gone"],"additionalProperties":false}}}],
"tool_choice":"required"}`

// chatBody builds a chat request body from a JSON object, adding what the
// node always sets.
func chatBody(t *testing.T, model, rest string) []byte {
	t.Helper()
	body := `{"model":"` + model + `","stream":true,"stream_options":{"include_usage":true}`
	if rest != "" {
		body += "," + rest
	}
	body += "}"
	if !json.Valid([]byte(body)) {
		t.Fatalf("invalid test body: %s", body)
	}
	return []byte(body)
}

// chatConvert converts body and returns the upstream request decoded.
func chatConvert(t *testing.T, to string, body []byte, model string) (map[string]any, Translator) {
	t.Helper()
	c, err := Convert(Chat, to, body, Options{Model: model})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(c.Body, &req); err != nil {
		t.Fatalf("body: %v", err)
	}
	return req, c.Translator
}

// chatJSON encodes v with sorted keys and without HTML escapes.
func chatJSON(t *testing.T, v any) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func chatWant(t *testing.T, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(got, p) {
			t.Errorf("missing %s in\n%s", p, got)
		}
	}
}

func chatWantNot(t *testing.T, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(got, p) {
			t.Errorf("unexpected %s in\n%s", p, got)
		}
	}
}

// chatResult is what a client reads from a converted stream.
type chatResult struct {
	chunks    []string
	text      string
	reasoning string
	refusal   string
	finish    string
	calls     []chatTestCall
	thinking  []chatThinkingBlock
	usage     map[string]any
	model     string
	err       error // from Event or Close
}

type chatTestCall struct{ ID, Name, Args string }

// chatStream feeds events to tr, closes it, and checks the chunks are what
// an OpenAI stream with include_usage looks like.
func chatStream(t *testing.T, tr Translator, events ...string) chatResult {
	t.Helper()
	var res chatResult
	var payloads [][]byte
	emit := func(p []byte) error {
		payloads = append(payloads, append([]byte(nil), p...))
		return nil
	}
	for _, e := range events {
		if err := tr.Event([]byte(e), emit); err != nil {
			res.err = err
			break
		}
	}
	if res.err == nil {
		res.err = tr.Close(emit)
	}
	byIndex := map[int]int{}
	var id string
	finishAt, usageAt := -1, -1
	for n, p := range payloads {
		res.chunks = append(res.chunks, string(p))
		var c struct {
			ID      string         `json:"id"`
			Object  string         `json:"object"`
			Model   string         `json:"model"`
			Usage   map[string]any `json:"usage"`
			Choices []struct {
				Delta struct {
					Role              string              `json:"role"`
					Content           string              `json:"content"`
					ReasoningContent  string              `json:"reasoning_content"`
					Refusal           string              `json:"refusal"`
					AnthropicThinking []chatThinkingBlock `json:"anthropic_thinking"`
					ToolCalls         []struct {
						Index    *int            `json:"index"`
						ID       string          `json:"id"`
						Function json.RawMessage `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(p, &c); err != nil {
			t.Fatalf("not a chunk: %s", p)
		}
		if c.Object != "chat.completion.chunk" || !strings.HasPrefix(c.ID, "chatcmpl-") {
			t.Fatalf("bad chunk head: %s", p)
		}
		if id == "" {
			id = c.ID
		} else if c.ID != id {
			t.Fatalf("chunk ids differ: %s", p)
		}
		res.model = c.Model
		if c.Usage != nil {
			if len(c.Choices) != 0 || usageAt >= 0 {
				t.Fatalf("usage chunk must have no choices and come once: %s", p)
			}
			res.usage, usageAt = c.Usage, n
		}
		for _, ch := range c.Choices {
			d := ch.Delta
			if (d.Role != "") != (n == 0) {
				t.Fatalf("role must be on the first chunk only (chunk %d): %s", n, p)
			}
			res.text += d.Content
			res.reasoning += d.ReasoningContent
			res.refusal += d.Refusal
			res.thinking = append(res.thinking, d.AnthropicThinking...)
			for _, tc := range d.ToolCalls {
				if tc.Index == nil {
					t.Fatalf("tool call delta without index: %s", p)
				}
				var f struct {
					Name      *string `json:"name"`
					Arguments string  `json:"arguments"`
				}
				_ = json.Unmarshal(tc.Function, &f)
				i, ok := byIndex[*tc.Index]
				if !ok {
					i = len(res.calls)
					byIndex[*tc.Index] = i
					res.calls = append(res.calls, chatTestCall{})
					if f.Name == nil || *f.Name == "" || tc.ID == "" {
						t.Fatalf("a tool call must start with its id and name: %s", p)
					}
				} else if f.Name != nil {
					t.Fatalf("the name must not be repeated: %s", p)
				}
				if tc.ID != "" {
					res.calls[i].ID = tc.ID
				}
				if f.Name != nil {
					res.calls[i].Name += *f.Name
				}
				res.calls[i].Args += f.Arguments
			}
			if ch.FinishReason != nil {
				if finishAt >= 0 {
					t.Fatalf("two finish chunks: %s", p)
				}
				res.finish, finishAt = *ch.FinishReason, n
			}
		}
	}
	if finishAt >= 0 && (usageAt != finishAt+1 || usageAt != len(payloads)-1) {
		t.Fatalf("the usage chunk must follow the finish chunk and end the stream:\n%s", strings.Join(res.chunks, "\n"))
	}
	if res.err == nil && finishAt < 0 {
		t.Fatalf("a stream that ended well must be finished:\n%s", strings.Join(res.chunks, "\n"))
	}
	return res
}

func chatUsageNumber(t *testing.T, u map[string]any, path ...string) uint64 {
	t.Helper()
	var cur any = u
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		cur = m[p]
	}
	f, _ := cur.(float64)
	return uint64(f)
}
