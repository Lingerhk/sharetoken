package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func watchAgent(t *testing.T, home, portalURL string) (*Agent, chan struct{}) {
	t.Helper()
	a := NewAgent(home, Config{HTTP: "127.0.0.1:0", Portal: portalURL, Token: "tok"})
	a.portal = NewPortalClient(portalURL, "tok", "")
	var cancel context.CancelFunc
	a.ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ended := make(chan struct{})
	a.OnLoginEnded = func() { close(ended) }
	a.LoginCheckEvery = 10 * time.Millisecond
	return a, ended
}

func TestRunningClientNoticesItsLoginEnded(t *testing.T) {
	var revoked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"revoked"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"devices":[]}`))
	}))
	defer srv.Close()
	a, ended := watchAgent(t, t.TempDir(), srv.URL)
	go a.watchLogin()
	select {
	case <-ended:
		t.Fatal("stopped while the login was valid")
	case <-time.After(100 * time.Millisecond):
	}
	revoked.Store(true)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("did not notice the revoked token")
	}
}

func TestUnreachablePortalDoesNotEndTheLogin(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	a, ended := watchAgent(t, t.TempDir(), url)
	go a.watchLogin()
	select {
	case <-ended:
		t.Fatal("an unreachable portal ended the login")
	case <-time.After(200 * time.Millisecond):
	}
}

// `sharetoken logout` after logging in again revokes the new token, not the one
// the running client holds: the client has to give up its own.
func TestLogoutCommandStopsAClientOnAnOlderToken(t *testing.T) {
	var loggedOut atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/logout" {
			loggedOut.Store(true)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	cfg := Config{Env: Env, Version: ConfigVersion, Portal: srv.URL, User: "a@example.com"}
	if err := SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	a, ended := watchAgent(t, home, srv.URL)
	a.LoginCheckEvery = time.Hour
	go a.watchLogin()
	a.loginKick <- struct{}{}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the logout command did not stop the client")
	}
	if !loggedOut.Load() {
		t.Fatal("the client's own token was not revoked")
	}
}

func TestLocalAPIAnswersOnlyToLoopbackNames(t *testing.T) {
	check := func(listen, host string, want int) {
		t.Helper()
		a := NewAgent(t.TempDir(), Config{HTTP: listen})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/login/check", nil)
		r.Host = host
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("listening on %s, Host %s: %d, want %d", listen, host, w.Code, want)
		}
	}
	check("127.0.0.1:9787", "127.0.0.1:9787", http.StatusAccepted)
	check("127.0.0.1:9787", "localhost:9787", http.StatusAccepted)
	check("127.0.0.1:9787", "[::1]:9787", http.StatusAccepted)
	check("127.0.0.1:9787", "evil.example", http.StatusForbidden)
	check("127.0.0.1:9787", "evil.example:9787", http.StatusForbidden)
	check("0.0.0.0:9787", "10.0.0.5:9787", http.StatusAccepted) // opened to the network on purpose
}
