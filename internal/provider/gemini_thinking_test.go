package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// The reasoning effort becomes a thinkingLevel on Gemini 3 and later, at a
// level the model takes, and a budget within the model's bounds on 2.5.
func TestGeminiThinking(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		max           uint64
		want          string // the thinkingConfig as JSON, "" for none
	}{
		// No effort: the model's own default.
		{"gemini-3.1-pro-preview", "", 0, ""},
		{"gemini-3.1-pro-preview", "auto", 0, ""},
		// Gemini 3 cannot stop thinking: none is low, and no thoughts back.
		{"gemini-3-flash-preview", "none", 0, `{"thinkingLevel":"low"}`},
		// Minimal goes as low: 3.7 and 3.8 Flash refuse it.
		{"gemini-3.8-flash", "minimal", 0, `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3.5-flash", "medium", 0, `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"gemini-3.1-pro-preview", "medium", 0, `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"gemini-3-pro-preview", "medium", 0, `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3-pro-preview", "xhigh", 0, `{"includeThoughts":true,"thinkingLevel":"high"}`},
		// Gemini 4 takes levels, never a budget.
		{"gemini-4-carbon", "medium", 0, `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"gemini-4-argon", "none", 0, `{"thinkingLevel":"low"}`},
		// An id that names its level is told no other.
		{"gemini-3.1-pro-high", "low", 0, `{"includeThoughts":true}`},
		{"gemini-3-pro-low", "none", 0, ""},
		// Gemini 2.5: budgets.
		{"gemini-2.5-flash", "none", 0, `{"thinkingBudget":0}`},
		{"gemini-2.5-pro", "none", 0, `{"thinkingBudget":128}`},
		{"gemini-2.5-flash", "low", 0, `{"includeThoughts":true,"thinkingBudget":1024}`},
		{"gemini-2.5-pro", "high", 0, `{"includeThoughts":true,"thinkingBudget":32768}`},
		{"gemini-2.5-flash", "max", 0, `{"includeThoughts":true,"thinkingBudget":24576}`},
		{"gemini-2.5-flash-lite", "low", 600, `{"includeThoughts":true,"thinkingBudget":512}`},
		{"gemini-2.5-flash", "medium", 4000, `{"includeThoughts":true,"thinkingBudget":2000}`},
		// Models that take neither.
		{"gemini-2.0-flash", "high", 0, ""},
		{"gemini-3.1-flash-image", "high", 0, ""},
		{"claude-sonnet-4-6", "high", 0, ""},
	} {
		got := ""
		if tc := geminiThinking(c.model, c.effort, c.max); tc != nil {
			got = asJSON(t, tc)
		}
		if got != c.want {
			t.Errorf("%s at %q: %s, want %s", c.model, c.effort, got, c.want)
		}
	}
}

// The effort reaches generationConfig from the chat request's
// reasoning_effort, through the Code Assist upstream.
func TestCodeAssistSendsThinkingConfig(t *testing.T) {
	srv, sent := scriptedServer(t, scripted{Status: 200, Body: `{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`})
	e := &Entry{ID: t.Name(), Name: "gemini", Type: KindGemini, Auth: AuthOAuth, BaseURL: srv.URL}
	up := &codeAssistUpstream{entry: e, ts: FixedToken(&Token{AccessToken: "at", ProjectID: "proj"})}
	for _, body := range []string{
		`{"model":"gemini-3.5-flash","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`,
	} {
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(body)}, discard); err != nil {
			t.Fatal(err)
		}
	}
	if got := asJSON(t, (*sent)[0]); !strings.Contains(got, `"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"medium"}`) {
		t.Errorf("with an effort: %s", got)
	}
	if got := asJSON(t, (*sent)[1]); strings.Contains(got, "thinkingConfig") {
		t.Errorf("without an effort: %s", got)
	}
}

// response_format: json_object is a mime type, json_schema the schema as
// given in responseJsonSchema; a request with tools gets neither.
func TestGeminiResponseFormat(t *testing.T) {
	build := func(extra string) string {
		chat, err := parseChat([]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		req, err := geminiRequest(chat, geminiOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return asJSON(t, req)
	}
	if got := build(`,"response_format":{"type":"json_object"}`); !strings.Contains(got, `"responseMimeType":"application/json"`) || strings.Contains(got, "Schema") {
		t.Errorf("json_object: %s", got)
	}
	schema := `{"type":"object","properties":{"b":{"type":"string","minLength":2},"a":{"anyOf":[{"type":"integer"},{"type":"null"}]}},"required":["b","a"],"additionalProperties":false}`
	got := build(`,"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":` + schema + `}}`)
	if !strings.Contains(got, `"responseJsonSchema":`+schema) || !strings.Contains(got, `"responseMimeType":"application/json"`) || strings.Contains(got, `"responseSchema"`) {
		t.Errorf("json_schema: %s", got)
	}
	tools := `,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]`
	if got := build(`,"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":` + schema + `}}` + tools); strings.Contains(got, "responseMimeType") || strings.Contains(got, "responseJsonSchema") {
		t.Errorf("with tools: %s", got)
	}
}

// Only an array keeps items, whichever shape the cleaning picked.
func TestCleanSchemaItemsOnlyOnArrays(t *testing.T) {
	for raw, want := range map[string]string{
		`{"anyOf":[{"type":"string"},{"type":"array"}],"items":{"type":"integer"}}`: `{"type":"string"}`,
		`{"type":["object","null"],"items":{"type":"integer"},"properties":{}}`:     `{"description":"(nullable)","properties":{},"type":"object"}`,
		`{"type":"array","items":[{"type":"integer"}]}`:                             `{"items":{"type":"integer"},"type":"array"}`,
		`{"type":"array"}`: `{"items":{"type":"string"},"type":"array"}`,
	} {
		if got := asJSON(t, cleanSchema([]byte(raw))); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
}

// A stream that ends normally with nothing in it is a failure worth another
// try, not an empty answer; thoughts alone are nothing.
func TestGeminiEmptyStopIsAnError(t *testing.T) {
	end := func(events ...string) error {
		st := &geminiStream{out: newChunks("m", discard)}
		for _, e := range events {
			if err := st.event([]byte(e)); err != nil {
				return err
			}
		}
		return st.end()
	}
	var se *node.StatusError
	for name, events := range map[string][]string{
		"empty":         {`{"response":{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9}}}`},
		"thoughts only": {`{"response":{"candidates":[{"content":{"parts":[{"text":"hmm","thought":true}]}}]}}`, `{"response":{"candidates":[{"content":{},"finishReason":"STOP"}]}}`},
		"other":         {`{"response":{"candidates":[{"content":{},"finishReason":"OTHER"}]}}`},
	} {
		if err := end(events...); !errors.As(err, &se) || se.Status != 502 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := end(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{}}}]},"finishReason":"STOP"}]}}`); err != nil {
		t.Errorf("a tool call alone: %v", err)
	}
	// Out of tokens while thinking is the length the buyer allowed.
	if err := end(`{"response":{"candidates":[{"content":{"parts":[{"text":"hmm","thought":true}]},"finishReason":"MAX_TOKENS"}]}}`); err != nil {
		t.Errorf("MAX_TOKENS: %v", err)
	}
}
