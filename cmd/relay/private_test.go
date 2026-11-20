package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sharetoken/internal/config"
	"sharetoken/internal/relaypass"
)

func portalStub(t *testing.T, key string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/network" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"coordinator": "x", "relays": []any{}, "pass_key": key})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// `relay init` writes a file the relay loads: private to the accounts named,
// with the portal's key and no coordinator.
func TestInitWritesAPrivateRelayConfig(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	srv := portalStub(t, relaypass.FormatKey(pub))
	path := filepath.Join(t.TempDir(), "relay.yaml")
	args := []string{"-portal", srv.URL, "-account", "user:3,user:7", "-account", "user:9",
		"-announce", "/dns4/relay.example.com/tcp/4001", "-out", path}
	if err := initPrivate(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	f := defaults()
	if err := config.Load(path, &f); err != nil {
		t.Fatal(err)
	}
	cfg, err := relayConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Networks[0]
	if len(cfg.Networks) != 1 || !n.Private() || strings.Join(n.AllowAccounts, " ") != "user:3 user:7 user:9" ||
		len(n.AllowPeers) != 0 || len(n.PortalKeys) != 1 || !n.PortalKeys[0].Equal(pub) {
		t.Fatalf("network: %+v", n)
	}
	if f.Access.Networks[0].PortalURL != srv.URL || len(cfg.Announce) != 1 || cfg.MaxReservations != 64 {
		t.Fatalf("file: %+v", f)
	}
	if err := initPrivate(args, &bytes.Buffer{}); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if b, _ := os.ReadFile(path); !bytes.Contains(b, []byte("sharetoken relays add")) {
		t.Fatal("the file does not say how to use the relay")
	}

	// Without an account the relay would serve everyone.
	if err := initPrivate([]string{"-portal", srv.URL}, &bytes.Buffer{}); err == nil {
		t.Fatal("wrote a private relay without accounts")
	}
}

// The key is read over https only (plain http just from this machine), and a
// portal without passes cannot have a private relay.
func TestFetchPassKey(t *testing.T) {
	if _, err := fetchPassKey(context.Background(), "http://portal.example.com"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http to another host: %v", err)
	}
	if _, err := fetchPassKey(context.Background(), portalStub(t, "").URL); err == nil {
		t.Fatal("took a portal without a pass key")
	}
	if _, err := fetchPassKey(context.Background(), portalStub(t, "ed25519:zz").URL); err == nil {
		t.Fatal("took a malformed key")
	}
}

func TestPrivateNetworkSettingsAreChecked(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	f := defaults()
	f.Access.Networks = []NetworkFile{{Name: "mine", PortalKeys: []string{relaypass.FormatKey(pub)}, AllowAccounts: []string{"user 3"}}}
	if _, err := relayConfig(f); err == nil || !strings.Contains(err.Error(), "allow_accounts") {
		t.Fatalf("a malformed account: %v", err)
	}
	f.Access.Networks[0].AllowAccounts = []string{"user:3"}
	f.Access.Networks[0].PortalURL = "http://portal.example.com"
	if _, err := relayConfig(f); err == nil || !strings.Contains(err.Error(), "portal_url") {
		t.Fatalf("a plain-http portal_url: %v", err)
	}
}
