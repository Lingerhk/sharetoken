package apicompat

import (
	"encoding/json"
	"testing"
)

// chatPayloads runs events through tr and returns the chunk payloads.
func chatPayloads(t *testing.T, tr Translator, events ...string) [][]byte {
	t.Helper()
	var out [][]byte
	emit := func(p []byte) error { out = append(out, append([]byte(nil), p...)); return nil }
	for _, e := range events {
		if err := tr.Event([]byte(e), emit); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Close(emit); err != nil {
		t.Fatal(err)
	}
	return out
}

type chatCompletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Message struct {
			Role              string              `json:"role"`
			Content           *string             `json:"content"`
			ReasoningContent  string              `json:"reasoning_content"`
			Refusal           string              `json:"refusal"`
			AnthropicThinking []chatThinkingBlock `json:"anthropic_thinking"`
			ToolCalls         []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage map[string]any `json:"usage"`
}

func chatAggregated(t *testing.T, payloads [][]byte) chatCompletion {
	t.Helper()
	out, err := Aggregate(Chat, payloads)
	if err != nil {
		t.Fatal(err)
	}
	var c chatCompletion
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatal(err)
	}
	if c.Object != "chat.completion" || len(c.Choices) != 1 || c.Choices[0].Message.Role != "assistant" || c.Created == 0 {
		t.Fatalf("shape: %s", out)
	}
	return c
}

// A Messages stream, aggregated: everything the chunks carried.
func TestAggregateChatFromMessages(t *testing.T) {
	_, tr := chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"go"}]`), "client-model")
	c := chatAggregated(t, chatPayloads(t, tr,
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"Hi"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"cat\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
		`{"type":"message_stop"}`))
	m := c.Choices[0].Message
	if c.Model != "client-model" || *m.Content != "Hi" || m.ReasoningContent != "plan" || c.Choices[0].FinishReason != "tool_calls" ||
		len(m.AnthropicThinking) != 1 || m.AnthropicThinking[0].Signature != "sig" || m.AnthropicThinking[0].Thinking != "plan" {
		t.Fatalf("%+v", c)
	}
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != "toolu_1" || m.ToolCalls[0].Type != "function" || m.ToolCalls[0].Function.Name != "lookup" || m.ToolCalls[0].Function.Arguments != `{"q":"cat"}` {
		t.Fatalf("calls %+v", m.ToolCalls)
	}
	if chatJSON(t, c.Usage) != `{"completion_tokens":42,"prompt_tokens":17,"prompt_tokens_details":{"cache_creation_tokens":2,"cached_tokens":5},"total_tokens":59}` {
		t.Fatalf("usage %v", c.Usage)
	}
}

// A Responses stream with parallel calls whose arguments interleave.
func TestAggregateChatFromResponses(t *testing.T) {
	_, tr := chatConvert(t, Responses, chatBody(t, "gpt-5.4", `"messages":[{"role":"user","content":"go"}]`), "")
	c := chatAggregated(t, chatPayloads(t, tr,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"why"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"a","name":"f"}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"b","name":"python__st"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"y\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"x\":1}"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"2}"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":20,"output_tokens":30,"output_tokens_details":{"reasoning_tokens":12}}}}`))
	m := c.Choices[0].Message
	if c.Model != "gpt-5.4" || m.Content != nil || m.ReasoningContent != "why" || c.Choices[0].FinishReason != "tool_calls" || len(m.ToolCalls) != 2 {
		t.Fatalf("%+v", c)
	}
	if m.ToolCalls[0].Function.Arguments != `{"x":1}` || m.ToolCalls[1].Function.Name != "python" || m.ToolCalls[1].Function.Arguments != `{"y":2}` {
		t.Fatalf("calls %+v", m.ToolCalls)
	}
	if chatUsageNumber(t, c.Usage, "completion_tokens_details", "reasoning_tokens") != 12 || chatUsageNumber(t, c.Usage, "total_tokens") != 50 {
		t.Fatalf("usage %v", c.Usage)
	}
}

func TestAggregateChat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		payloads []string
		check    func(t *testing.T, c chatCompletion)
	}{
		{name: "text, [DONE] and empty payloads skipped, no finish is stop", payloads: []string{
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":5,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"a"}}]}`,
			``, `[DONE]`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":5,"model":"m","choices":[{"index":0,"delta":{"content":"b","reasoning":"r"}}]}`},
			check: func(t *testing.T, c chatCompletion) {
				m := c.Choices[0].Message
				if c.ID != "chatcmpl-1" || *m.Content != "ab" || m.ReasoningContent != "r" || c.Choices[0].FinishReason != "stop" || c.Usage != nil {
					t.Fatalf("%+v", c)
				}
			}},
		{name: "a refusal leaves no content", payloads: []string{
			`{"id":"x","created":1,"choices":[{"index":0,"delta":{"refusal":"no"}}]}`,
			`{"id":"x","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
			`{"id":"x","created":1,"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`},
			check: func(t *testing.T, c chatCompletion) {
				m := c.Choices[0].Message
				if m.Content != nil || m.Refusal != "no" || c.Choices[0].FinishReason != "content_filter" || c.Usage["total_tokens"] != float64(2) {
					t.Fatalf("%+v", c)
				}
			}},
		{name: "tool calls by index, in order of appearance", payloads: []string{
			`{"id":"x","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"g","arguments":""}}]}}]}`,
			`{"id":"x","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{"}}]}}]}`,
			`{"id":"x","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}},{"index":1,"function":{"arguments":"[]"}}]}}]}`},
			check: func(t *testing.T, c chatCompletion) {
				m := c.Choices[0].Message
				if len(m.ToolCalls) != 2 || m.ToolCalls[0].ID != "b" || m.ToolCalls[0].Function.Arguments != "[]" || m.ToolCalls[1].Function.Arguments != "{}" || c.Choices[0].FinishReason != "tool_calls" || m.Content != nil {
					t.Fatalf("%+v", c)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ps [][]byte
			for _, p := range tc.payloads {
				ps = append(ps, []byte(p))
			}
			tc.check(t, chatAggregated(t, ps))
		})
	}
	if _, err := Aggregate(Chat, [][]byte{[]byte(`{"choices":`)}); err == nil {
		t.Error("aggregated an unreadable chunk")
	}
	if _, err := Aggregate(Chat, [][]byte{[]byte(`[DONE]`)}); err == nil {
		t.Error("aggregated nothing")
	}
}
