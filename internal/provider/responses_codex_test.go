package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/node"
)

// additional_tools declarations keep their IDs (a UUID-shaped one compacted
// as the backend takes it), and so do references to them; other IDs and
// references go as before.
func TestCodexKeepsAdditionalToolsIDs(t *testing.T) {
	req, _, _, err := codexRequest([]byte(`{"model":"m","input":[
		{"type":"additional_tools","id":"at_0123abcd-4567-89ab-cdef-0123456789ab","tools":[]},
		{"type":"additional_tools","id":"at_custom","tools":[]},
		{"type":"item_reference","id":"at_0123abcd-4567-89ab-cdef-0123456789ab"},
		{"type":"item_reference","id":"at_custom"},
		{"type":"item_reference","id":"msg_0"},
		{"type":"message","id":"msg_1","role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	input := req["input"].([]any)
	var got []string
	for _, it := range input {
		m := it.(map[string]any)
		got = append(got, fmt.Sprint(m["type"], ":", m["id"]))
	}
	want := "additional_tools:at_0123abcd456789abcdef0123456789ab additional_tools:at_custom " +
		"item_reference:at_0123abcd456789abcdef0123456789ab item_reference:at_custom message:<nil>"
	if strings.Join(got, " ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
	for _, id := range []string{"at_0123abcd-4567-89ab-cdef-012345678zzz", "at_0123abcd-4567-89ab-cdef-0123456789a", "x_0123abcd-4567-89ab-cdef-0123456789ab"} {
		if compactToolsID(id) != id {
			t.Errorf("%s compacted", id)
		}
	}
}

// Codex's external context, an output without a call, goes to the backend
// as the user message it stands for; on the OpenAI API path as well.
func TestCodexExternalContext(t *testing.T) {
	body := `{"model":"m","input":[
		{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"Chrome"},
		{"type":"function_call_output","namespace":"agent","call_id":"call_1","name":"browser_info","output":"kept"}]}`
	req, _, _, err := codexRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	first := req["input"].([]any)[0].(map[string]any)
	if text := first["content"].([]any)[0].(map[string]any)["text"]; first["role"] != "user" || text != "<external_browser_info>Chrome</external_browser_info>" {
		t.Fatalf("first item %v", first)
	}
	wantAll(t, asJSON(t, req["input"]), `"call_id":"call_1"`, `"output":"kept"`)

	out := string(dropConvertedReasoning([]byte(body)))
	wantAll(t, out, `"role":"user"`, `external_browser_info`, `"output":"kept"`)
	if strings.Contains(out, `"output":"Chrome"`) {
		t.Fatalf("external context left as an output: %s", out)
	}
}

func TestResponsesStreamError(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, event string
		status      int
		body        string
		retry       bool
	}{
		{"context in response.failed", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}`,
			400, `"code":"context_length_exceeded"`, false},
		{"context in error event", `{"type":"error","code":"context_too_large","message":"too large"}`, 400, `"code":"context_too_large"`, false},
		{"context in nested error", `{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"m"}}`, 400, `"type":"invalid_request_error"`, false},
		{"overloaded", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"busy"}}}`, 503, `"code":"server_is_overloaded"`, false},
		{"slow down", `{"type":"error","error":{"code":"slow_down","message":"slow"}}`, 503, `slow_down`, false},
		{"usage limit", `{"type":"error","error":{"type":"usage_limit_reached","message":"limit","resets_in_seconds":120}}`, 429, `usage_limit_reached`, true},
		{"rate limit code", `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"slow","resets_in_seconds":30}}}`, 429, `rate_limit_exceeded`, true},
		{"other", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"oops"}}}`, 502, `response.failed: server_error oops`, false},
	} {
		se := responsesStreamError([]byte(tc.event))
		if se.Status != tc.status || !strings.Contains(se.Body, tc.body) {
			t.Errorf("%s: %d %s", tc.name, se.Status, se.Body)
		}
		if tc.retry != se.RetryAt.After(now) {
			t.Errorf("%s: retry at %v", tc.name, se.RetryAt)
		}
	}
	// The Codex stream fails with it.
	err := (&codexStream{emit: discard}).event([]byte(`{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"m"}}}`))
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest || !strings.Contains(se.Body, "context_length_exceeded") {
		t.Fatalf("codex stream: %v", err)
	}
}

// ChatGPT's edge 507 for a request it could not buffer to retry is the same
// connection reset as its 503, and is asked again as one.
func TestCodexEdgeReset(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInsufficientStorage)
		_, _ = io.WriteString(w, "exceeded request buffer limit while retrying upstream")
	}))
	defer srv.Close()
	err := streamCodex(context.Background(), srv.URL, http.Header{}, []byte(`{"model":"m","input":"hi"}`), discard)
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusServiceUnavailable {
		t.Fatalf("err %v", err)
	}
	if _, again := transientWait(err, 0); !again {
		t.Fatal("not retried")
	}
	if err := edgeReset(&node.StatusError{Status: http.StatusInsufficientStorage, Body: "disk full"}); err.(*node.StatusError).Status != http.StatusInsufficientStorage {
		t.Fatal("another 507 changed")
	}
}

func TestRefusedForeignReasoning(t *testing.T) {
	for _, body := range []string{
		"The encrypted content for item rs_1 could not be verified.",
		"Could not decrypt the provided encrypted_content.",
		"invalid encrypted summary",
		"Could not decode the compaction blob",
		`{"error":{"code":"invalid_encrypted_content"}}`,
	} {
		if !refusedForeignReasoning(&node.StatusError{Status: 400, Body: body}) {
			t.Errorf("not read as foreign reasoning: %s", body)
		}
	}
	if refusedForeignReasoning(&node.StatusError{Status: 400, Body: "bad model"}) ||
		refusedForeignReasoning(&node.StatusError{Status: 500, Body: "Could not decode the compaction blob"}) {
		t.Error("read as foreign reasoning")
	}
	// The compaction blob refusal, which never says "encrypted", strips.
	var sent []string
	body := `{"input":[{"type":"reasoning","encrypted_content":"X"},{"type":"message","role":"user","content":"hi"}]}`
	_ = retryWithoutReasoning([]byte(body), func(b []byte) error {
		sent = append(sent, string(b))
		if strings.Contains(string(b), "reasoning") {
			return &node.StatusError{Status: 400, Body: "Could not decode the compaction blob"}
		}
		return nil
	})
	if len(sent) != 2 || strings.Contains(sent[1], "reasoning") {
		t.Fatalf("sent %v", sent)
	}
}

func TestUnsupportedPattern(t *testing.T) {
	for p, bad := range map[string]bool{
		`^[a-z]+$`:           false,
		`^\d{3}-\w+\s*$`:     false,
		`(?:a|b)`:            false,
		`(?P<n>x)`:           false,
		`[(?=]`:              false,
		`\.\-\/`:             false,
		`^(?!foo).*`:         true,
		`(?=a)`:              true,
		`(?<=a)b`:            true,
		`(?<name>x)`:         true,
		`(?'name'x)`:         true,
		`\p{L}+`:             true,
		`a\R`:                true,
		`trailing\`:          true,
		`(?`:                 true,
		`^(?:[^\x00-\x1f])$`: false,
	} {
		if unsupportedPattern(p) != bad {
			t.Errorf("%q: want unsupported=%v", p, bad)
		}
	}
}

// Patterns Python's re refuses leave the tool schemas, with the
// patternProperties keys that are such patterns; an object that lost such a
// key no longer refuses other properties. Instance values stay.
func TestCodexCleansToolPatterns(t *testing.T) {
	req, _, _, err := codexRequest([]byte(`{"model":"m","input":[
		{"type":"additional_tools","id":"at_x","tools":[{"type":"function","name":"g","parameters":{"type":"object","properties":{"s":{"type":"string","pattern":"\\p{L}"}}}}]}],
		"tools":[{"type":"function","name":"f","parameters":{"type":"object",
			"properties":{
				"id":{"type":"string","pattern":"^(?!0)[0-9]+$"},
				"ok":{"type":"string","pattern":"^[a-z]+$"},
				"list":{"type":"array","items":{"type":"string","pattern":"(?<=a)b"}},
				"choice":{"anyOf":[{"type":"string","pattern":"(?<n>x)"}]},
				"def":{"type":"string","default":"(?=keep)","enum":["(?=keep)"]}},
			"$defs":{"d":{"type":"string","pattern":"a\\R"}},
			"patternProperties":{"^(?=x)":{"type":"string"},"^y":{"type":"string","pattern":"(?!z)"}},
			"additionalProperties":false}},
			{"type":"namespace","name":"ns","tools":[{"type":"function","name":"h","parameters":{"type":"object","properties":{"q":{"type":"string","pattern":"(?=q)"}}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := asJSON(t, req)
	for _, gone := range []string{`(?!0)`, `(?<=a)`, `(?<n>`, `a\\R`, `^(?=x)`, `(?!z)`, `\\p{L}`, `(?=q)`, `"additionalProperties":false`} {
		if strings.Contains(got, gone) {
			t.Errorf("%s left: %s", gone, got)
		}
	}
	wantAll(t, got, `"pattern":"^[a-z]+$"`, `"default":"(?=keep)"`, `"enum":["(?=keep)"]`, `"^y":{"type":"string"}`, `"additionalProperties":true`)

	// A pattern-only change keeps additionalProperties: false.
	b, ok := withoutToolPatterns([]byte(`{"tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"a":{"type":"string","pattern":"(?=a)"}},"additionalProperties":false}}]}`))
	if !ok || strings.Contains(string(b), "(?=a)") || !strings.Contains(string(b), `"additionalProperties":false`) {
		t.Fatalf("cleaned %s, %v", b, ok)
	}
	if _, ok := withoutToolPatterns([]byte(`{"tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"a":{"type":"string","pattern":"^a$"}}}}]}`)); ok {
		t.Fatal("changed a schema Python reads")
	}
}

// A request an upstream refused for a tool schema's regular expression goes
// again without the ones Python cannot read: on the OpenAI API path too,
// which does not clean them up front.
func TestRetryWithoutToolPatterns(t *testing.T) {
	var sent []string
	body := `{"tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"a":{"type":"string","pattern":"(?<=a)"}}}}],"input":"hi"}`
	err := retryWithoutReasoning([]byte(body), func(b []byte) error {
		sent = append(sent, string(b))
		if strings.Contains(string(b), "pattern") {
			return &node.StatusError{Status: 400, Body: `{"error":{"message":"Invalid schema for function 'f': 'pattern' is not a valid regex: look-behind requires fixed-width pattern"}}`}
		}
		return nil
	})
	if err != nil || len(sent) != 2 {
		t.Fatalf("err %v, sent %v", err, sent)
	}
	if refusedPattern(&node.StatusError{Status: 400, Body: "The encrypted content could not be verified"}) {
		t.Fatal("encrypted refusal read as a pattern one")
	}
}
