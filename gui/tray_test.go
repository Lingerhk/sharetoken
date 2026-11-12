package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrayStatusForEachState(t *testing.T) {
	text := trayTexts["en"]
	for _, c := range []struct {
		st   trayState
		want string
	}{
		{trayState{Down: true}, text.ServiceDown},
		{trayState{State: stateLoggedOut, User: "last@example.com"}, text.SignedOut},
		{trayState{State: stateExpired}, text.Expired},
		{trayState{State: stateStarting, LoggedIn: true, User: "a@b.c"}, text.Connecting + " · a@b.c"},
		{trayState{State: stateRetrying, LoggedIn: true, User: "a@b.c"}, text.Retrying + " · a@b.c"},
		{trayState{State: stateRunning, LoggedIn: true, User: "a@b.c"}, text.Connected + " · a@b.c"},
		{trayState{State: "a-later-state"}, text.Connecting},
	} {
		if got := renderTray(text, c.st).Status; got != c.want {
			t.Errorf("%+v: got %q, want %q", c.st, got, c.want)
		}
	}
	m := renderTray(text, trayState{State: stateRunning})
	if !strings.Contains(m.Open, windowTitle()) || !strings.Contains(m.Quit, windowTitle()) || strings.Contains(m.Open+m.Quit, "{app}") {
		t.Fatalf("%+v", m)
	}
}

// The menu speaks every language the page does, and uses the page's words where
// they say the same thing.
func TestTrayTextsMatchPageLanguages(t *testing.T) {
	dir := filepath.Join("..", "internal", "client", "ui", "i18n", "client")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Skipf("no dictionaries here (a test binary copied to another machine): %v", err)
	}
	langs := map[string]bool{"zh-CN": true} // the source language has no dictionary
	for _, f := range files {
		lang := strings.TrimSuffix(filepath.Base(f), ".json")
		langs[lang] = true
		text, ok := trayTexts[lang]
		if !ok {
			t.Errorf("no tray text for %s", lang)
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var dict map[string]string
		if err := json.Unmarshal(data, &dict); err != nil {
			t.Fatal(err)
		}
		for key, got := range map[string]string{"已连接": text.Connected, "正在连接网络…": text.Connecting, "暂时连不上网络，正在重试": text.Retrying} {
			if dict[key] != got {
				t.Errorf("%s: %q is %q on the page, %q in the tray", lang, key, dict[key], got)
			}
		}
	}
	for lang, text := range trayTexts {
		if !langs[lang] {
			t.Errorf("tray text for %s, which the page does not have", lang)
		}
		for _, s := range []string{text.SignedOut, text.Expired, text.Connecting, text.Retrying, text.Connected, text.ServiceDown} {
			if s == "" || strings.Contains(s, "{app}") {
				t.Errorf("%s: %+v", lang, text)
			}
		}
		if !strings.Contains(text.Open, "{app}") || !strings.Contains(text.Quit, "{app}") {
			t.Errorf("%s: the app's name is missing: %+v", lang, text)
		}
		for _, s := range []string{text.UpdateAvailable, text.UpdateRequired, text.NotifyAvailable, text.NotifyRequired} {
			if !strings.Contains(s, "{version}") && s != text.NotifyRequired {
				t.Errorf("%s: no {version} in %q", lang, s)
			}
		}
		if strings.Contains(text.Relay, "{ms}") || !strings.Contains(text.Relay, "{name}") || !strings.Contains(text.RelayRTT, "{name}") || !strings.Contains(text.RelayRTT, "{ms}") ||
			!strings.Contains(text.Sharing, "{n}") || !strings.Contains(text.Balance, "{n}") {
			t.Errorf("%s: info lines: %+v", lang, text)
		}
		if !strings.Contains(text.NotifyAvailable+text.NotifyRequired, "{app}") || strings.Contains(text.UpdateAvailable+text.UpdateRequired, "{app}") {
			t.Errorf("%s: update texts: %+v", lang, text)
		}
	}
}

// The menu shows a new release until the user skips it; a required upgrade
// stays. The service being down hides it: the information is stale.
func TestTrayUpdateItem(t *testing.T) {
	text := trayTexts["en"]
	avail := trayUpdate{Available: true, Latest: "1.2.0", Key: "1.2.0"}
	for _, c := range []struct {
		st   trayState
		want string
	}{
		{trayState{State: stateRunning}, ""},
		{trayState{State: stateRunning, Update: avail}, "Update available: 1.2.0"},
		{trayState{State: stateLoggedOut, Update: avail}, "Update available: 1.2.0"},
		{trayState{State: stateRunning, Update: trayUpdate{Available: true, Latest: "1.2.0", Skipped: true}}, ""},
		{trayState{State: stateRunning, Update: trayUpdate{Available: true, Required: true, Latest: "1.2.0", Skipped: true}}, "Upgrade required: 1.2.0"},
		{trayState{Down: true, Update: avail}, ""},
	} {
		if got := renderTray(text, c.st).Update; got != c.want {
			t.Errorf("%+v: %q, want %q", c.st, got, c.want)
		}
	}
}

// The lines under the status line: only while connected, and only what is known.
func TestTrayInfoLines(t *testing.T) {
	text := trayTexts["en"]
	bal := int64(12_000_000)
	for _, c := range []struct {
		st   trayState
		want string
	}{
		{trayState{State: stateRunning, LoggedIn: true}, ""},
		{trayState{State: stateRunning, LoggedIn: true, Net: &trayNet{Relay: "us"}}, "Relay: us"},
		{trayState{State: stateRunning, LoggedIn: true, Net: &trayNet{Relay: "us", RTT: 0.2}}, "Relay: us"},
		{trayState{State: stateRunning, LoggedIn: true, Net: &trayNet{Relay: "us", RTT: 87.4}, Balance: &bal}, "Relay: us · 87 ms|Available credit: 12"},
		{trayState{State: stateRetrying, LoggedIn: true, Net: &trayNet{Relay: "us", RTT: 87}, Balance: &bal}, "Available credit: 12"},
		{trayState{State: stateLoggedOut, Balance: &bal}, ""},
		{trayState{Down: true, LoggedIn: true, Net: &trayNet{Relay: "us"}, Balance: &bal}, ""},
	} {
		m := renderTray(text, c.st)
		if got := strings.Join(m.Info, "|"); got != c.want || len(m.Info) > trayInfoLines {
			t.Errorf("%+v: %q, want %q", c.st, got, c.want)
		}
	}
	for micros, want := range map[int64]string{0: "0", 5_000_000: "5", 4_990_000: "4.99", 1_500_000: "1.5", 1234: "0"} {
		if got := trayCredit(micros); got != want {
			t.Errorf("%d: %q, want %q", micros, got, want)
		}
	}
	// A relay the portal does not name shows the end of its address.
	var s trayStatus
	_ = json.Unmarshal([]byte(`{"relays":[{"addr":"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWabcdef","current":true,"rtt_ms":9}]}`), &s)
	if n := s.net(); n.Relay != "…abcdef" || n.RTT != 9 || n.Sharing {
		t.Fatalf("%+v", n)
	}
}

// One notification per offered build, recorded with the daemon; none for a
// skipped build, nor again for one the daemon says was notified.
func TestTrayNotifiesOncePerBuild(t *testing.T) {
	var mu sync.Mutex
	var recorded []string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Key string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		recorded = append(recorded, r.URL.Path+" "+req.Key)
		mu.Unlock()
	}))
	defer daemon.Close()
	var sent []string
	deliver := true
	tc := newTrayController(func() error { return nil }, func() string { return strings.TrimPrefix(daemon.URL, "http://") }, func(trayMenu) {})
	tc.notify = func(title, body string) bool {
		sent = append(sent, title+": "+body)
		return deliver
	}
	ctx := context.Background()
	u := trayUpdate{Available: true, Latest: "1.2.0", Key: "1.2.0+abc"}
	_ = json.Unmarshal([]byte(`{"en":{"summary":"A smaller window."},"zh-CN":{"summary":"窗口更小。"}}`), &u.Notes)
	tc.notifyUpdate(ctx, u)
	tc.notifyUpdate(ctx, u) // the next poll, before the daemon reports it
	tc.notifyUpdate(ctx, trayUpdate{Available: true, Latest: "1.3.0", Key: "1.3.0", Skipped: true})
	tc.notifyUpdate(ctx, trayUpdate{Available: true, Latest: "1.3.0", Key: "1.3.0", Notified: true})
	if len(sent) != 1 || !strings.HasSuffix(sent[0], "\nA smaller window.") || !strings.Contains(sent[0], "1.2.0") || !strings.HasPrefix(sent[0], windowTitle()+": ") || strings.Contains(sent[0], "{") {
		t.Fatalf("sent %q", sent)
	}
	mu.Lock()
	got := strings.Join(recorded, ",")
	mu.Unlock()
	if got != "/api/v1/update/notified 1.2.0+abc" {
		t.Fatalf("recorded %q", got)
	}
	// Not delivered (no permission): not recorded, and not retried in this run.
	deliver = false
	req := trayUpdate{Available: true, Required: true, Latest: "2.0.0", Key: "2.0.0", Skipped: true}
	tc.notifyUpdate(ctx, req)
	tc.notifyUpdate(ctx, req)
	if len(sent) != 2 || !strings.Contains(sent[1], "no longer supported") {
		t.Fatalf("sent %q", sent)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 1 {
		t.Fatalf("recorded %q", recorded)
	}
}

func TestSingleInstancePerDataDirectory(t *testing.T) {
	a, b := singleInstanceID("/home/a/.sharetoken"), singleInstanceID("/home/b/.sharetoken")
	if a == b || a != singleInstanceID("/home/a/.sharetoken") {
		t.Fatalf("%s %s", a, b)
	}
	// It names a lock file, a notification and a Windows window class.
	if strings.ContainsAny(a, `/\: `) || len(a) > 200 || !strings.Contains(a, Env) {
		t.Fatal(a)
	}
}

// menuRecorder is the native side of the tray in these tests.
type menuRecorder struct {
	mu   sync.Mutex
	last trayMenu
	seen chan trayMenu
}

func (r *menuRecorder) set(m trayMenu) {
	r.mu.Lock()
	r.last = m
	r.mu.Unlock()
	select {
	case r.seen <- m:
	default:
	}
}

func (r *menuRecorder) latest() trayMenu {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *menuRecorder) waitFor(t *testing.T, status string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-r.seen:
			if m.Status == status {
				return
			}
		case <-deadline:
			t.Fatalf("status never became %q; last %+v", status, r.latest())
		}
	}
}

func TestTrayFollowsDaemonAndLanguage(t *testing.T) {
	var body atomic.Value
	body.Store(`{"state":"logged_out"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/session":
			io.WriteString(w, body.Load().(string))
		case "/api/v1/status":
			io.WriteString(w, `{"coordinator":{"via":"/x/relay-b","link":{"samples":3,"avg_ms":41.6}},"relays":[{"addr":"/x/relay-a","name":"relay-a","rtt_ms":12},{"addr":"/x/relay-b","name":"relay-b","rtt_ms":300,"current":true}],"share":{"running":true,"serving":[{},{}]}}`)
		case "/api/v1/account":
			io.WriteString(w, `{"balance":{"available_micros":4990000}}`)
		default:
			t.Errorf("asked for %s", r.URL.Path)
		}
	}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	rec := &menuRecorder{seen: make(chan trayMenu, 64)}
	release := make(chan struct{})
	tc := newTrayController(func() error { <-release; return nil }, func() string { return addr }, rec.set)
	tc.every = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tc.run(ctx); close(done) }()

	// Until the daemon's start has finished, the menu already shows.
	en, zh := trayTexts["en"], trayTexts["zh-CN"]
	rec.waitFor(t, en.Connecting)
	close(release)
	rec.waitFor(t, en.SignedOut)

	body.Store(`{"state":"running","logged_in":true,"user":"a@b.c"}`)
	rec.waitFor(t, en.Connected+" · a@b.c")
	// The relay in use with its live latency, what is shared, the balance.
	if m := rec.latest(); strings.Join(m.Info, "|") != "Relay: relay-b · 42 ms|Sharing · models on sale: 2|Available credit: 4.99" || m.Tooltip != windowTitle()+" · Relay: relay-b · 42 ms" {
		t.Fatalf("%+v", m)
	}

	tc.setLang("zh-CN")
	rec.waitFor(t, zh.Connected+" · a@b.c")
	if last := rec.latest(); !strings.HasPrefix(last.Quit, "退出") {
		t.Fatalf("%+v", last)
	}
	tc.setLang("") // the start-up error page has no language
	tc.setLang("not-a-language")
	rec.waitFor(t, zh.Connected+" · a@b.c")

	// The daemon dies while the window is closed.
	srv.Close()
	rec.waitFor(t, zh.ServiceDown)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestTrayWhenDaemonCouldNotStart(t *testing.T) {
	rec := &menuRecorder{seen: make(chan trayMenu, 64)}
	tc := newTrayController(func() error { return errors.New("port in use") }, func() string { return "127.0.0.1:1" }, rec.set)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tc.run(ctx)
	rec.waitFor(t, trayTexts["en"].ServiceDown)
}

func TestProxyTakesLanguageReportItself(t *testing.T) {
	var got string
	p := newProxy(&supervisor{addr: "127.0.0.1:1"}, func() error {
		t.Error("a language report must not wait for the daemon")
		return nil
	}, nil, func(l string) { got = l })
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", shellLangPath+"?l=pt-BR", nil))
	if w.Code != http.StatusNoContent || got != "pt-BR" {
		t.Fatalf("%d %q", w.Code, got)
	}
}

// The window's first request can come before the daemon's address is settled.
func TestProxyWaitsForStartAndUsesSettledAddress(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "from-daemon") }))
	defer daemon.Close()
	sup := &supervisor{addr: "127.0.0.1:1"}
	release := make(chan struct{})
	p := newProxy(sup, func() error { <-release; return nil }, nil, nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { p.ServeHTTP(w, httptest.NewRequest("GET", "/", nil)); close(done) }()
	select {
	case <-done:
		t.Fatal("answered before the daemon started")
	case <-time.After(50 * time.Millisecond):
	}
	sup.addr = strings.TrimPrefix(daemon.URL, "http://") // moved to a spare port
	close(release)
	<-done
	if w.Body.String() != "from-daemon" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
