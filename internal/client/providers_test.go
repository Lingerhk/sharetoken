package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/provider"
)

func TestProviderConfigurationAPI(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	if err := SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	if err := provider.Update(home, func(f *provider.File) error {
		return f.Put(&provider.Entry{Name: "claude", Type: provider.KindAnthropic, Auth: provider.AuthOAuth, OAuth: &provider.Token{AccessToken: "secret-token", AccountUUID: "user-uuid", RefreshToken: "refresh-secret"}, Transport: &provider.TransportOptions{ProxyMode: "custom", ProxyURL: "http://user:proxy-secret@proxy.test:8080"}})
	}); err != nil {
		t.Fatal(err)
	}
	a := NewAgent(home, cfg)
	mux := http.NewServeMux()
	a.providerRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call("PATCH", "/api/v1/providers/claude", `{"transport":{"token_tls_profile":"chrome"},"anthropic":{"profile":"claude-code","sampling":"strict"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	f, _ := provider.Load(home)
	e := f.Get("claude")
	if e.Transport.ProxyURL != "http://user:proxy-secret@proxy.test:8080" || e.Transport.TokenTLSProfile != "chrome" || e.Anthropic.Profile != "claude-code" {
		t.Fatal("partial options erased connection settings")
	}
	w = call("POST", "/api/v1/providers/oauth/start", `{"type":"anthropic","name":"claude","mode":"inference"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var login provider.Login
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(login.URL, "scope=user%3Ainference") {
		t.Fatal("mode not passed to login")
	}
	w = call("GET", "/api/v1/providers", "")
	for _, secret := range []string{"secret-token", "refresh-secret", "proxy-secret", "session_key", "device_id"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("view leaked %s", secret)
		}
	}
	if !strings.Contains(w.Body.String(), "proxy.test:8080") {
		t.Fatal("missing sanitized proxy address")
	}
	w = call("PATCH", "/api/v1/providers/claude", `{"transport":{"proxy_mode":"direct"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("direct reset: %s", w.Body.String())
	}
	f, _ = provider.Load(home)
	if f.Get("claude").Transport.ProxyURL != "" {
		t.Fatal("direct mode retained proxy password")
	}
}
