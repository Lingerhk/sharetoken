package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// Encrypted reasoning another account made is refused once per conversation:
// the next turn leaves it out from the start, while the reasoning this
// account made after the move still goes along.
func TestRefusedSealsRemembered(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		if strings.Contains(string(b), "FOREIGN") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"The encrypted content for item rs_1 could not be verified.","code":"invalid_encrypted_content"}}`)
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n")
	}))
	defer srv.Close()
	up := &codexUpstream{base: srv.URL, ts: FixedToken(&Token{AccessToken: "a"})}
	user := `{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`
	turn1 := `{"model":"m","prompt_cache_key":"conv","input":[{"type":"reasoning","encrypted_content":"FOREIGN-1"},` + user + `]}`
	req := func(body string) *node.Request {
		return &node.Request{API: protocol.APIResponses, Body: []byte(body)}
	}
	if err := up.Stream(context.Background(), req(turn1), discard); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("first turn: %d requests", len(sent))
	}

	sent = nil
	turn2 := `{"model":"m","prompt_cache_key":"conv","input":[{"type":"reasoning","encrypted_content":"FOREIGN-1"},` + user +
		`,{"type":"reasoning","encrypted_content":"OWN-2"},` + user + `]}`
	if err := up.Stream(context.Background(), req(turn2), discard); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || strings.Contains(sent[0], "FOREIGN-1") || !strings.Contains(sent[0], "OWN-2") {
		t.Fatalf("second turn: %v", sent)
	}

	// Another conversation, or another model, learns nothing from it.
	sent = nil
	other := strings.Replace(turn1, `"conv"`, `"other"`, 1)
	if err := up.Stream(context.Background(), req(other), discard); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("other conversation: %d requests", len(sent))
	}
}

func TestRefusedSealsBounds(t *testing.T) {
	var s refusedSeals
	body := func(enc string) []byte {
		return []byte(`{"input":[{"type":"reasoning","encrypted_content":"` + enc + `"},{"type":"message","role":"user","content":"x"}]}`)
	}
	s.note("k", body("A"), []byte(`{"input":[{"type":"message","role":"user","content":"x"}]}`))
	if out, ok := s.without("k", body("A")); !ok || strings.Contains(string(out), `"A"`) {
		t.Fatalf("not dropped: %s", out)
	}
	if _, ok := s.without("k", body("B")); ok {
		t.Fatal("dropped an item never refused")
	}
	// Converted reasoning is never remembered: every seller reads it.
	s.note("c", body("sharetoken-reasoning-v1:eA=="), []byte(`{"input":[]}`))
	if _, ok := s.m["c"]; ok {
		t.Fatal("remembered a converted item")
	}
	// Past sealsKeep, it is forgotten.
	defer func(f func() time.Time) { timeNow = f }(timeNow)
	later := time.Now().Add(sealsKeep + time.Minute)
	timeNow = func() time.Time { return later }
	if _, ok := s.without("k", body("A")); ok {
		t.Fatal("kept past sealsKeep")
	}
	for i := 0; i < sealsMostKeys+10; i++ {
		s.note(fmt.Sprint(i), body("A"), []byte(`{"input":[]}`))
	}
	if len(s.m) > sealsMostKeys {
		t.Fatalf("%d conversations kept", len(s.m))
	}
	var nilSeals *refusedSeals
	if _, ok := nilSeals.without("k", body("A")); ok {
		t.Fatal("a nil memory dropped something")
	}
}

// The backend's x-codex-turn-state of a turn's first reply goes back with
// the turn's later requests to the same account, and not with another turn's.
func TestCodexTurnStateKept(t *testing.T) {
	var got []string
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		got = append(got, r.Header.Get("x-codex-turn-state"))
		n++
		w.Header().Set("x-codex-turn-state", fmt.Sprintf("state-%d", n))
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n")
	}))
	defer srv.Close()
	up := &codexUpstream{base: srv.URL, ts: FixedToken(&Token{AccessToken: "a", AccountID: "acct"})}
	send := func(turn string) {
		t.Helper()
		h := protocol.RequestHeader{"session_id": "s1"}
		if turn != "" {
			h["x-codex-turn-id"] = turn
		}
		r := &node.Request{API: protocol.APIResponses, Header: h, Body: []byte(`{"model":"m","input":"hi"}`)}
		if err := up.Stream(context.Background(), r, discard); err != nil {
			t.Fatal(err)
		}
	}
	send("t1")
	send("t1")
	send("t2")
	send("t1")
	send("")
	want := []string{"", "state-1", "", "state-2", ""}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("turn states sent %q, want %q", got, want)
	}
}

func TestCodexTurnStatesBounded(t *testing.T) {
	var c codexTurnStates
	for i := 0; i < codexTurnStateMost+5; i++ {
		c.put(fmt.Sprint(i), "s")
	}
	if len(c.m) > codexTurnStateMost {
		t.Fatalf("%d kept", len(c.m))
	}
	c.put("k", "v")
	defer func(f func() time.Time) { timeNow = f }(timeNow)
	later := time.Now().Add(codexTurnStateFor + time.Minute)
	timeNow = func() time.Time { return later }
	if c.get("k") != "" {
		t.Fatal("kept past codexTurnStateFor")
	}
	if codexTurnKey("a", "s", "") != "" {
		t.Fatal("a key for a request without a turn")
	}
}
