package buyer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/protocol"
)

// loopBody is a request in api whose conversation ends in turns of tool use:
// each asks for name(args) and gets result(i) back.
func loopBody(api string, turns int, name, args string, result func(i int) string) []byte {
	var req map[string]any
	switch api {
	case protocol.APIChat:
		msgs := []any{map[string]any{"role": "user", "content": "fix the build"}}
		for i := range turns {
			id := fmt.Sprintf("call_%d", i)
			msgs = append(msgs,
				map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}},
				map[string]any{"role": "tool", "tool_call_id": id, "content": result(i)})
		}
		req = map[string]any{"model": "m", "messages": msgs, "stream": true}
	case protocol.APIMessages:
		msgs := []any{map[string]any{"role": "user", "content": "fix the build"}}
		for i := range turns {
			id := fmt.Sprintf("toolu_%d", i)
			var input any
			_ = json.Unmarshal([]byte(args), &input)
			msgs = append(msgs,
				map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Checking."}, map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}},
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": result(i)}}})
		}
		req = map[string]any{"model": "m", "max_tokens": 1000, "messages": msgs, "stream": true}
	case protocol.APIResponses:
		input := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "fix the build"}}}}
		for i := range turns {
			id := fmt.Sprintf("call_%d", i)
			input = append(input,
				map[string]any{"type": "reasoning", "summary": []any{}},
				map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": args},
				map[string]any{"type": "function_call_output", "call_id": id, "output": result(i)})
		}
		req = map[string]any{"model": "m", "input": input, "stream": true}
	}
	b, _ := json.Marshal(req)
	return b
}

func same(int) string { return "Wall time: 0.0123 seconds\nmain.go:3: undefined: x" }

func TestToolLoopOf(t *testing.T) {
	for _, api := range protocol.AllAPIs {
		cases := []struct {
			name  string
			body  []byte
			loops bool
		}{
			{"eight same turns", loopBody(api, 8, "shell", `{"cmd":"go build","dir":"."}`, same), true},
			{"seven same turns", loopBody(api, 7, "shell", `{"cmd":"go build"}`, same), false},
			{"results that change", loopBody(api, 12, "shell", `{"cmd":"go build"}`, func(i int) string { return fmt.Sprintf("%d errors", i) }), false},
			{"waiting", loopBody(api, 12, "sleep", `{"seconds":30}`, same), false},
			{"bare duration", loopBody(api, 12, "ci_status", `{"timeout_ms":3000}`, same), false},
		}
		for _, c := range cases {
			loop, ok := toolLoopOf(api, c.body)
			if ok != c.loops {
				t.Errorf("%s %s: loop=%v, want %v", api, c.name, ok, c.loops)
			}
			if ok && (loop.turns != 8 || !strings.Contains(loop.notice(), "8 times") || !strings.Contains(loop.called(), "go build")) {
				t.Errorf("%s %s: %+v %q", api, c.name, loop, loop.notice())
			}
		}
	}
	// Arguments written in another key order are the same call.
	body := loopBody(protocol.APIChat, 4, "shell", `{"cmd":"go build","dir":"."}`, same)
	more := loopBody(protocol.APIChat, 4, "shell", `{"dir":".","cmd":"go build"}`, same)
	var a, b map[string]any
	_ = json.Unmarshal(body, &a)
	_ = json.Unmarshal(more, &b)
	a["messages"] = append(a["messages"].([]any), b["messages"].([]any)[1:]...)
	joined, _ := json.Marshal(a)
	if _, ok := toolLoopOf(protocol.APIChat, joined); !ok {
		t.Error("arguments in another order must count as the same call")
	}
}

func TestSteadyResult(t *testing.T) {
	a := steadyResult("Chunk ID: 3f9a\nWall time: 0.0246 seconds\nok")
	b := steadyResult("Chunk ID: 77b0\nWall time: 1.5 seconds\nok")
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
}

// A request stuck in a tool loop is answered by the proxy itself, in each
// API, streamed or not, and reaches no node (the proxy has no client here).
func TestProxyAnswersToolLoops(t *testing.T) {
	srv := httptest.NewServer((&Proxy{}).Handler())
	defer srv.Close()
	for _, api := range protocol.AllAPIs {
		path := map[string]string{protocol.APIChat: "/v1/chat/completions", protocol.APIMessages: "/v1/messages", protocol.APIResponses: "/v1/responses"}[api]
		for _, stream := range []bool{true, false} {
			body := loopBody(api, 9, "shell", `{"cmd":"go build"}`, same)
			if !stream {
				body = bytes.Replace(body, []byte(`"stream":true`), []byte(`"stream":false`), 1)
			}
			resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get(HeaderLoopGuard) != "tool_loop" {
				t.Fatalf("%s stream=%v: %d %s", api, stream, resp.StatusCode, buf.String())
			}
			out := buf.Bytes()
			if stream {
				var payloads [][]byte
				for _, line := range strings.Split(buf.String(), "\n") {
					if p, ok := strings.CutPrefix(line, "data: "); ok && p != "[DONE]" {
						payloads = append(payloads, []byte(p))
					}
				}
				if out, err = apicompat.Aggregate(api, payloads); err != nil {
					t.Fatalf("%s: the stream does not aggregate: %v\n%s", api, err, buf.String())
				}
			}
			if !strings.Contains(string(out), "same tool call 9 times") {
				t.Errorf("%s stream=%v: the notice is missing: %s", api, stream, out)
			}
		}
	}
	// Off, the request goes on to the network (here: a proxy with no
	// client would panic, so only the check itself is asked).
	if _, ok := (&Proxy{NoLoopGuard: true}).toolLoopAnswer(ChatRequest{API: protocol.APIChat, Body: loopBody(protocol.APIChat, 9, "shell", `{}`, same)}); ok {
		t.Fatal("the guard is off")
	}
}
