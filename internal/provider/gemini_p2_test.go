package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// A signature rides in the call's id: short ones inline, long ones as a
// key into the provider's store; the mark always comes off on the way back,
// and a signature that was cut short or is not kept reads as none.
func TestSignedIDRoundTrip(t *testing.T) {
	store := signatureStore(t.Name())
	short := base64.StdEncoding.EncodeToString([]byte("sig-1"))
	long := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("thinking ", 400)))
	keptID := signedID("call_1", long, store)
	if len(keptID) > len("call_1")+len("__tsk16__")+16 || !strings.Contains(keptID, "__tsk16__") {
		t.Fatalf("a long signature's id: %s", keptID)
	}
	for _, c := range []struct {
		name, id     string
		store        *sigStore
		wantID, want string
	}{
		{"inline", signedID("call_1", short, nil), nil, "call_1", short},
		{"not base64", signedID("call_1", "plain text!", nil), nil, "call_1", "plain text!"},
		{"kept", keptID, store, "call_1", long},
		{"kept, another seller", keptID, signatureStore(t.Name() + "-other"), "call_1", ""},
		{"kept, no store", keptID, nil, "call_1", ""},
		{"cut short", signedID("call_1", short, nil)[:len("call_1")+12], nil, "call_1", ""},
		{"no id: one is made", signedID("", short, nil), nil, "", short},
		{"no signature", signedID("call_1", "", store), store, "call_1", ""},
		{"placeholder", signedID("call_1", skipThoughtSignature, store), store, "call_1", ""},
		{"plain id", "toolu_01__tsx", nil, "toolu_01__tsx", ""},
	} {
		own, sig := unsignedID(c.id, c.store)
		if sig != c.want || (c.wantID != "" && own != c.wantID) || strings.Contains(own, sigMark+"k") {
			t.Errorf("%s: %q → %q %q", c.name, c.id, own, sig)
		}
	}
	// A long signature with no store goes inline.
	if id := signedID("c", long, nil); !strings.Contains(id, "__ts") || strings.Contains(id, "__tsk") {
		t.Errorf("no store: %s", id)
	}
}

// The store forgets the least recently used signature past its bounds.
func TestSigStoreBounded(t *testing.T) {
	old := sigStoreMax
	sigStoreMax = 2
	t.Cleanup(func() { sigStoreMax = old })
	s := signatureStore(t.Name())
	a, b := s.keep("sig-a"), s.keep("sig-b")
	_ = s.get(a) // a is used again: b goes first
	c := s.keep("sig-c")
	if s.get(a) != "sig-a" || s.get(b) != "" || s.get(c) != "sig-c" {
		t.Fatalf("kept %q %q %q", s.get(a), s.get(b), s.get(c))
	}
	if s.keep("sig-c") != c || s.order.Len() != 2 {
		t.Fatal("the same signature kept twice")
	}
}

// codeAssistTurn sends a chat body through a Gemini CLI upstream and
// returns the tool calls it answered.
func codeAssistTurn(t *testing.T, up *codeAssistUpstream, body string) []protocol.ToolCall {
	t.Helper()
	_, _, _, calls, _ := collect(t, func(emit func([]byte) error) error {
		return up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(body)}, emit)
	})
	return calls
}

func geminiUpstream(t *testing.T, id, url string) *codeAssistUpstream {
	e := &Entry{ID: id, Name: "gemini", Type: KindGemini, Auth: AuthOAuth, BaseURL: url}
	return &codeAssistUpstream{entry: e, ts: FixedToken(&Token{AccessToken: "at", ProjectID: "proj"})}
}

// historyWith is the next turn of a conversation whose assistant made
// calls (their ids as the buyer got them), each answered.
func historyWith(calls []protocol.ToolCall) string {
	var tc, results []any
	for _, c := range calls {
		tc = append(tc, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Function.Name, "arguments": c.Function.Arguments}})
		results = append(results, map[string]any{"role": "tool", "tool_call_id": c.ID, "content": "result of " + c.Function.Name})
	}
	msgs := append([]any{
		map[string]any{"role": "user", "content": "look"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": tc},
	}, results...)
	b, _ := json.Marshal(map[string]any{"model": "gemini-3.5-flash", "messages": msgs,
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": json.RawMessage(lookupParams)}}}})
	return string(b)
}

const lookupParams = `{"type":"object","properties":{"q":{"type":"string"}}}`

// The signature Gemini gave a call comes back on that call in the next
// turn, from the id the buyer was given; the later call of the same step
// goes without one. Another seller, which does not keep the signature,
// sends the placeholder instead; both send the calls' own ids.
func TestCodeAssistThoughtSignatureRoundTrip(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("reasoning state ", 200)))
	answer := fmt.Sprintf(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"a"},"id":"fc-1"},"thoughtSignature":%q},{"functionCall":{"name":"lookup","args":{"q":"b"},"id":"fc-2"}}]},"finishReason":"STOP"}]}}`, sig)
	srv, sent := scriptedServer(t, scripted{Status: 200, Body: answer},
		scripted{Status: 200, Body: `{"response":{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}}`})
	up := geminiUpstream(t, t.Name(), srv.URL)
	calls := codeAssistTurn(t, up, `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"look"}]}`)
	if len(calls) != 2 || !strings.HasPrefix(calls[0].ID, "fc-1__tsk16__") || calls[1].ID != "fc-2" {
		t.Fatalf("calls %+v", calls)
	}
	next := historyWith(calls)
	codeAssistTurn(t, up, next)
	got := asJSON(t, (*sent)[1])
	wantAll(t, got,
		`{"functionCall":{"args":{"q":"a"},"id":"fc-1","name":"lookup"},"thoughtSignature":"`+sig+`"}`,
		`{"functionCall":{"args":{"q":"b"},"id":"fc-2","name":"lookup"}}`,
		`"functionResponse":{"id":"fc-1","name":"lookup"`)
	if strings.Contains(got, skipThoughtSignature) || strings.Contains(got, "__ts") {
		t.Errorf("second turn: %s", got)
	}

	other := geminiUpstream(t, t.Name()+"-other", srv.URL)
	codeAssistTurn(t, other, next)
	got = asJSON(t, (*sent)[2])
	wantAll(t, got,
		`{"functionCall":{"args":{"q":"a"},"id":"fc-1","name":"lookup"},"thoughtSignature":"skip_thought_signature_validator"}`,
		`{"functionCall":{"args":{"q":"b"},"id":"fc-2","name":"lookup"},"thoughtSignature":"skip_thought_signature_validator"}`,
		`"functionResponse":{"id":"fc-1","name":"lookup"`)
	if strings.Contains(got, "__ts") || strings.Contains(got, sig) {
		t.Errorf("another seller: %s", got)
	}
}

// A signature the backend refuses is dropped: the request goes once more
// with placeholders.
func TestCodeAssistRetriesRefusedSignature(t *testing.T) {
	srv, sent := scriptedServer(t,
		scripted{Status: 400, Body: `{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}`},
		scripted{Status: 200, Body: `{"response":{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}}`})
	up := geminiUpstream(t, t.Name(), srv.URL)
	id := signedID("fc-1", "c2ln", nil) // a short signature, inline
	text, _, _, _, _ := collect(t, func(emit func([]byte) error) error {
		return up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(historyWith([]protocol.ToolCall{{ID: id, Type: "function", Function: protocol.ToolCallFunction{Name: "lookup", Arguments: `{"q":"a"}`}}}))}, emit)
	})
	if text != "done" || len(*sent) != 2 {
		t.Fatalf("%q after %d requests", text, len(*sent))
	}
	if first, second := asJSON(t, (*sent)[0]), asJSON(t, (*sent)[1]); !strings.Contains(first, `"thoughtSignature":"c2ln"`) || !strings.Contains(second, `"thoughtSignature":"skip_thought_signature_validator"`) {
		t.Errorf("first %s\nsecond %s", first, second)
	}
	// Without a signature of the model's there is nothing to drop: the
	// refusal is the answer.
	srv, sent = scriptedServer(t, scripted{Status: 400, Body: `{"error":{"code":400,"message":"Invalid thought signature."}}`})
	up = geminiUpstream(t, t.Name()+"-2", srv.URL)
	err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(historyWith([]protocol.ToolCall{{ID: "fc-1", Type: "function", Function: protocol.ToolCallFunction{Name: "lookup", Arguments: `{}`}}}))}, discard)
	if err == nil || len(*sent) != 1 {
		t.Errorf("%v after %d requests", err, len(*sent))
	}
}

// parametersJsonSchema takes JSON Schema whole: parameters that fit go as
// they came; the root alone is made an object.
func TestGeminiToolSchema(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"type":"object","properties":{"b":{"anyOf":[{"type":"string"},{"type":"integer"}]},"a":{"type":"string","minLength":1}}}`,
			`{"type":"object","properties":{"b":{"anyOf":[{"type":"string"},{"type":"integer"}]},"a":{"type":"string","minLength":1}}}`},
		{`{"properties":{"q":{"type":"string"}}}`, `{"properties":{"q":{"type":"string"}},"type":"object"}`},
		{`{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"number"}}}]}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"number"}},"type":"object"}`},
		{`{}`, `{"properties":{},"type":"object"}`},
		{`true`, `{"type":"object","properties":{}}`},
	} {
		if got := string(geminiToolSchema(json.RawMessage(c.in))); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// The Gemini CLI's backend gets parametersJsonSchema with the unions kept;
// Antigravity the Schema object.
func TestCodeAssistToolSchemaPerBackend(t *testing.T) {
	params := `{"type":"object","properties":{"v":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}}}`
	body := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"set","parameters":` + params + `}}]}`
	srv, sent := scriptedServer(t, scripted{Status: 200, Body: `{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`})
	gemini := geminiUpstream(t, t.Name(), srv.URL)
	codeAssistTurn(t, gemini, body)
	if got := asJSON(t, (*sent)[0]); !strings.Contains(got, `"parametersJsonSchema":{"properties":{"v":{"anyOf":[{"type":"string"},{"items":{"type":"string"},"type":"array"}]}},"type":"object"}`) || strings.Contains(got, `"parameters":`) {
		t.Errorf("gemini: %s", got)
	}
	ag := &codeAssistUpstream{entry: gemini.entry, ts: gemini.ts, antigravity: true}
	codeAssistTurn(t, ag, body)
	if got := asJSON(t, (*sent)[1]); strings.Contains(got, "parametersJsonSchema") || !strings.Contains(got, `"parameters":{"properties":{"v":{"type":"string"}},"type":"object"}`) {
		t.Errorf("antigravity: %s", got)
	}
}

// The Schema object needs a type on every node: one with items is an array.
func TestCleanSchemaInfersArray(t *testing.T) {
	got := asJSON(t, cleanSchema([]byte(`{"type":"object","properties":{"tags":{"items":{"type":"string"}},"x":{}}}`)))
	if got != `{"properties":{"tags":{"items":{"type":"string"},"type":"array"},"x":{"type":"string"}},"type":"object"}` {
		t.Errorf("%s", got)
	}
}

// Args that come as JSON text in pieces make one call, joined by the call's
// id or, without one, while they read as one call's; an object after them
// is the call's snapshot. Calls with object args are each a call, with or
// without an id.
func TestGeminiStreamArgPieces(t *testing.T) {
	type call struct{ id, name, args string }
	part := func(name, id string, args any) string {
		fc := map[string]any{"name": name, "args": args}
		if id != "" {
			fc["id"] = id
		}
		b, _ := json.Marshal(map[string]any{"functionCall": fc})
		return string(b)
	}
	for _, c := range []struct {
		name  string
		parts []string
		want  []call
	}{
		{"pieces by id", []string{part("f", "1", `{"q":`), part("f", "1", `"dog"}`)}, []call{{"1", "f", `{"q":"dog"}`}}},
		{"pieces without id", []string{part("f", "", `{"q":`), part("f", "", `"dog"}`)}, []call{{"", "f", `{"q":"dog"}`}}},
		{"cumulative text", []string{part("f", "", `{"q":`), part("f", "", `{"q":"dog"}`)}, []call{{"", "f", `{"q":"dog"}`}}},
		{"snapshot replaces pieces", []string{part("f", "", `{"q": `), part("f", "", `"dog" }`), part("f", "", map[string]any{"q": "dog"})}, []call{{"", "f", `{"q":"dog"}`}}},
		{"snapshot completes pieces", []string{part("f", "1", `{"q":"d`), part("f", "1", map[string]any{"q": "dog"})}, []call{{"1", "f", `{"q":"dog"}`}}},
		{"a whole text then another call", []string{part("f", "", `{"q":"a"}`), part("f", "", `{"q":"b"}`)}, []call{{"", "f", `{"q":"a"}`}, {"", "f", `{"q":"b"}`}}},
		{"two ids", []string{part("f", "1", `{"q":"a"}`), part("f", "2", `{"q":`), part("f", "2", `"b"}`)}, []call{{"1", "f", `{"q":"a"}`}, {"2", "f", `{"q":"b"}`}}},
		{"another name", []string{part("f", "", `{"q":`), part("g", "", map[string]any{})}, []call{{"", "f", `{"q":`}, {"", "g", `{}`}}},
		{"parallel objects without id", []string{part("f", "", map[string]any{"q": "a"}), part("f", "", map[string]any{"q": "a"})}, []call{{"", "f", `{"q":"a"}`}, {"", "f", `{"q":"a"}`}}},
	} {
		events := []string{fmt.Sprintf(`{"response":{"candidates":[{"content":{"parts":[%s]}}]}}`, strings.Join(c.parts[:len(c.parts)-1], ","))}
		events = append(events, fmt.Sprintf(`{"response":{"candidates":[{"content":{"parts":[%s]},"finishReason":"STOP"}]}}`, c.parts[len(c.parts)-1]))
		if len(c.parts) == 1 {
			events = events[1:]
		}
		_, _, finish, calls, _ := collect(t, func(emit func([]byte) error) error {
			st := &geminiStream{out: newChunks("m", emit)}
			for _, e := range events {
				if err := st.event([]byte(e)); err != nil {
					return err
				}
			}
			return st.end()
		})
		if finish != "tool_calls" || len(calls) != len(c.want) {
			t.Errorf("%s: %s %+v", c.name, finish, calls)
			continue
		}
		for i, w := range c.want {
			got := calls[i]
			if got.Function.Name != w.name || got.Function.Arguments != w.args || (w.id != "" && got.ID != w.id) || got.ID == "" {
				t.Errorf("%s: call %d %+v, want %+v", c.name, i, got, w)
			}
		}
	}
}

// A tier without a project of the backend's gets the account's project
// from Cloud Resource Manager (Code Assist's own first), and a tier that
// bills a named project is signed up with that one; when projects cannot
// be listed (403) the seller is asked, as before.
func TestFindProjectFromResourceManager(t *testing.T) {
	projects := `{"projects":[{"projectId":"deleted-1","lifecycleState":"DELETE_REQUESTED"},{"projectId":"my-app","name":"My App","lifecycleState":"ACTIVE"},{"projectId":"cloud-ai-companion-xyz","lifecycleState":"ACTIVE"}]}`
	app, sent := googleBackendWith(t, `{"currentTier":{"id":"standard-tier"}}`, "", projects)
	tok := &Token{AccessToken: "at"}
	if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "cloud-ai-companion-xyz" || tok.Plan != "standard-tier" {
		t.Fatalf("%v %+v", err, tok)
	}
	if len(sent["onboardUser"]) != 0 || sent["projects"][0]["auth"] != "Bearer at" {
		t.Errorf("sent %v", sent)
	}

	app, _ = googleBackendWith(t, `{"currentTier":{"id":"standard-tier"}}`, "", `{"projects":[{"projectId":"p-1","lifecycleState":"ACTIVE"},{"projectId":"p-2","lifecycleState":"ACTIVE"}]}`)
	tok = &Token{AccessToken: "at"}
	if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "p-1" {
		t.Fatalf("first active: %v %+v", err, tok)
	}

	app, sent = googleBackendWith(t, `{"allowedTiers":[{"id":"standard-tier","isDefault":true,"userDefinedCloudaicompanionProject":true}]}`,
		`{"done":true,"response":{}}`, projects)
	tok = &Token{AccessToken: "at"}
	if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "cloud-ai-companion-xyz" || tok.Plan != "standard-tier" {
		t.Fatalf("user-defined tier: %v %+v", err, tok)
	}
	if got := asJSON(t, sent["onboardUser"][0]); !strings.Contains(got, `"cloudaicompanionProject":"cloud-ai-companion-xyz"`) || !strings.Contains(got, `"tierId":"standard-tier"`) {
		t.Errorf("onboardUser: %s", got)
	}

	// Refused (403): the seller is asked for a project.
	app, sent = googleBackend(t, `{"currentTier":{"id":"standard-tier"}}`, "")
	if err := app.findProject(context.Background(), &Token{AccessToken: "at"}); err == nil || !strings.Contains(err.Error(), "-project") || len(sent["projects"]) != 1 {
		t.Errorf("403: %v", err)
	}
}

// A new account on a tier other than free is signed up with the project the
// seller gave; the free tier is signed up without, and the seller's project
// is the one used either way.
func TestFindProjectOnboardsWithGivenProject(t *testing.T) {
	for _, c := range []struct {
		tier        string
		withProject bool
	}{{"standard-tier", true}, {"free-tier", false}} {
		app, sent := googleBackend(t, `{"allowedTiers":[{"id":"`+c.tier+`","isDefault":true}]}`, `{"done":true,"response":{"cloudaicompanionProject":{"id":"managed"}}}`)
		tok := &Token{AccessToken: "at", ProjectID: "mine"}
		if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "mine" || tok.Plan != c.tier {
			t.Fatalf("%s: %v %+v", c.tier, err, tok)
		}
		got := asJSON(t, sent["onboardUser"][0])
		if strings.Contains(got, `"cloudaicompanionProject":"mine"`) != c.withProject || !strings.Contains(got, `"duetProject":"mine"`) {
			t.Errorf("%s: onboardUser %s", c.tier, got)
		}
	}
	// Onboarding that fails keeps the seller's project.
	app, _ := googleBackend(t, `{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`, "")
	tok := &Token{AccessToken: "at", ProjectID: "mine"}
	if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "mine" {
		t.Fatalf("%v %+v", err, tok)
	}
}

// A renewed token has its plan read again at most every planRecheck: an
// upgraded account is served as one, and a plan that cannot be read stays.
func TestRecheckPlan(t *testing.T) {
	app, sent := googleBackend(t, `{"currentTier":{"id":"free-tier"},"paidTier":{"id":"g1-ultra-tier"}}`, "")
	old := &Token{Plan: "free-tier", Extra: map[string]string{planCheckedKey: timeNow().Add(-10 * time.Minute).UTC().Format(time.RFC3339)}}
	nt := &Token{AccessToken: "new"}
	app.recheckPlan(context.Background(), old, nt)
	if nt.Plan != "" || len(sent["loadCodeAssist"]) != 0 {
		t.Fatalf("checked again within the hour: %+v", nt)
	}
	old.Extra[planCheckedKey] = timeNow().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	app.recheckPlan(context.Background(), old, nt)
	if nt.Plan != "g1-ultra-tier" || nt.Extra[planCheckedKey] == "" || len(sent["loadCodeAssist"]) != 1 {
		t.Fatalf("not checked: %+v", nt)
	}
	if got := asJSON(t, sent["loadCodeAssist"][0]); strings.Contains(got, "cloudaicompanionProject") {
		t.Errorf("loadCodeAssist: %s", got)
	}
	if merged := mergeToken(old, nt); merged.Plan != "g1-ultra-tier" || merged.Extra[planCheckedKey] != nt.Extra[planCheckedKey] {
		t.Errorf("merged %+v", merged)
	}
	// A token never checked is checked; an error keeps the plan.
	app.hosts = []string{"http://127.0.0.1:1"}
	nt = &Token{AccessToken: "new"}
	app.recheckPlan(context.Background(), &Token{Plan: "free-tier"}, nt)
	if nt.Plan != "" || nt.Extra[planCheckedKey] == "" {
		t.Errorf("unreadable: %+v", nt)
	}
	if merged := mergeToken(&Token{Plan: "free-tier"}, nt); merged.Plan != "free-tier" {
		t.Errorf("merged %+v", merged)
	}
}
