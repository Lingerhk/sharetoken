package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"sharetoken/internal/p2p"
)

// A portal that refuses the device signature (this computer's clock is off)
// is reported as a clock to check, and the warning goes once binding works.
func TestAgentBindClockWarning(t *testing.T) {
	var skewed atomic.Bool
	skewed.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if skewed.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"bad_signature","message":"accounts: device signature is too old or from the future"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	a := NewAgent(home, Config{Portal: srv.URL, User: "u@example.com", Token: "t"})
	keys, err := p2p.LoadOrCreateKeys(filepath.Join(home, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	a.keys, a.portal = keys, NewPortalClient(srv.URL, "t", "")
	if err := a.bind(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.warnings["device"] != ClockWarning || a.bindFail.IsZero() {
		t.Fatalf("warning %q, failed at %v", a.warnings["device"], a.bindFail)
	}
	skewed.Store(false)
	if err := a.bind(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w, ok := a.warnings["device"]; ok || !a.bindFail.IsZero() {
		t.Fatalf("after the clock is right: warning %q, failed at %v", w, a.bindFail)
	}
}
