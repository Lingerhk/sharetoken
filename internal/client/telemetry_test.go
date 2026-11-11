package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePostHog records what would have left the machine.
type fakePostHog struct {
	*httptest.Server
	mu     sync.Mutex
	raw    []string
	events []telemetryEvent
	keys   []string
	paths  []string
}

func newFakePostHog(t *testing.T) *fakePostHog {
	t.Helper()
	f := &fakePostHog{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var b struct {
			APIKey string           `json:"api_key"`
			Batch  []telemetryEvent `json:"batch"`
		}
		if err := json.Unmarshal(data, &b); err != nil {
			t.Errorf("bad batch: %s", data)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.raw, f.events = append(f.raw, string(data)), append(f.events, b.Batch...)
		f.keys, f.paths = append(f.keys, b.APIKey), append(f.paths, r.URL.Path)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePostHog) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, e.Event)
	}
	return out
}

func (f *fakePostHog) count(event string) int {
	n := 0
	for _, e := range f.names() {
		if e == event {
			n++
		}
	}
	return n
}

func (f *fakePostHog) waitFor(t *testing.T, event string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); f.count(event) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s; got %v", event, f.names())
		}
	}
}

func (e *daemonEnv) telemetryDaemon(home, host string) *testDaemon {
	e.t.Helper()
	d := NewDaemon(home)
	d.HTTP = "127.0.0.1:0"
	d.RetryMin, d.RetryMax = 50*time.Millisecond, 200*time.Millisecond
	d.TelemetryKey, d.TelemetryHost = "phc_test", host
	if err := d.Start(); err != nil {
		e.t.Fatal(err)
	}
	var once sync.Once
	td := &testDaemon{Daemon: d, t: e.t, base: "http://" + d.Addr()}
	td.closeOnce = func() { once.Do(d.Close) }
	e.t.Cleanup(td.closeOnce)
	return td
}

// The whole contract: what is sent, in which format, and what never is.
func TestTelemetryEventsCarryNothingPersonal(t *testing.T) {
	ph := newFakePostHog(t)
	e := newDaemonEnv(t, openPortal(), false)
	d := e.telemetryDaemon(t.TempDir(), ph.URL)
	ph.waitFor(t, eventDailyActive)
	ph.waitFor(t, eventHourlyActive)

	d.want(401, "bad_credentials", "POST", "/api/v1/login", map[string]string{"portal": e.url, "account": "victim@example.com", "password": "secret-wrong-pw"})
	ph.waitFor(t, eventSignInFailed)
	s := d.register(e.url, "alice@example.com")
	ph.waitFor(t, eventSignedIn)
	ph.waitFor(t, eventNetworkJoinFail) // this portal has no coordinator
	token := d.savedConfig().Token
	d.want(200, "", "POST", "/api/v1/logout", nil)
	ph.waitFor(t, eventSignedOut)

	ph.mu.Lock()
	defer ph.mu.Unlock()
	raw := strings.Join(ph.raw, "\n")
	for _, secret := range []string{"alice", "victim", "example.com", testPassword, "secret-wrong-pw", token, s.Peer, e.url, s.Device} {
		if secret != "" && strings.Contains(raw, secret) {
			t.Errorf("events contain %q:\n%s", secret, raw)
		}
	}
	for _, k := range ph.keys {
		if k != "phc_test" {
			t.Errorf("key %q", k)
		}
	}
	for _, p := range ph.paths {
		if p != "/batch/" {
			t.Errorf("path %q", p)
		}
	}
	id := ph.events[0].DistinctID
	if !strings.HasPrefix(id, "install:") || len(id) != len("install:")+32 {
		t.Errorf("distinct id %q", id)
	}
	for _, ev := range ph.events {
		p := ev.Properties
		if ev.DistinctID != id || p.SchemaVersion != 1 || p.OS == "" || p.Arch == "" || p.InsertID == "" || !p.GeoIPDisable || p.ProcessPersonProfil {
			t.Errorf("event %+v", ev)
		}
		if _, err := time.Parse(time.RFC3339, ev.Timestamp); err != nil {
			t.Errorf("timestamp %q", ev.Timestamp)
		}
	}
	// PostHog's special properties have exactly these names.
	if !strings.Contains(raw, `"$geoip_disable":true`) || !strings.Contains(raw, `"$process_person_profile":false`) || !strings.Contains(raw, `"$insert_id":`) {
		t.Errorf("property names: %s", raw)
	}
}

// The embedded key is for prod builds: nothing a test, a plain `go build` or a
// dev build runs may reach the real project, whatever its version. A prod build
// uses it.
func TestTelemetryBuiltinKeyOnlyInProdBuilds(t *testing.T) {
	defer func(e, k string) { Env, TelemetryKey = e, k }(Env, TelemetryKey)
	if Env != EnvDev || TelemetryKey != builtinTelemetryKey {
		t.Fatalf("defaults: %q %q", Env, TelemetryKey)
	}
	if newTelemetry(t.TempDir(), TelemetryKey, "https://example.com", nil) != nil {
		t.Fatal("a development build uses the real key")
	}
	if newTelemetry(t.TempDir(), "other", "https://example.com", nil) == nil {
		t.Fatal("another key must work in a development build (the UI test uses one)")
	}
	Env = EnvProd
	if newTelemetry(t.TempDir(), TelemetryKey, "https://example.com", nil) == nil {
		t.Fatal("a prod build ignores the built-in key")
	}
	if newTelemetry(t.TempDir(), "", "https://example.com", nil) != nil {
		t.Fatal("building with an empty key must turn statistics off")
	}
}

// A development build: no setting in the app, no file, not one request.
func TestTelemetryDevBuildSendsNothing(t *testing.T) {
	ph := newFakePostHog(t)
	e := newDaemonEnv(t, openPortal(), false)
	home := t.TempDir()
	d := NewDaemon(home)
	d.HTTP, d.TelemetryHost = "127.0.0.1:0", ph.URL
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	td := &testDaemon{Daemon: d, t: t, base: "http://" + d.Addr()}
	defer d.Close()
	if s := td.session(); s.Telemetry.Available || s.Telemetry.Enabled {
		t.Fatalf("session: %+v", s)
	}
	td.register(e.url, "a@example.com")
	td.want(200, "", "PUT", "/api/v1/telemetry", map[string]bool{"enabled": true})
	td.want(200, "", "POST", "/api/v1/logout", nil)
	time.Sleep(100 * time.Millisecond)
	if n := ph.names(); len(n) != 0 {
		t.Fatalf("sent %v", n)
	}
	if _, err := os.Stat(filepath.Join(home, TelemetryFile)); err == nil {
		t.Fatal("telemetry.json written by a development build")
	}
}

func TestTelemetryForbiddenByEnvironment(t *testing.T) {
	for _, env := range [][2]string{{"SHARETOKEN_TELEMETRY", "0"}, {"DO_NOT_TRACK", "1"}} {
		t.Run(env[0], func(t *testing.T) {
			ph := newFakePostHog(t)
			t.Setenv(env[0], env[1])
			e := newDaemonEnv(t, openPortal(), false)
			d := e.telemetryDaemon(t.TempDir(), ph.URL)
			if d.session().Telemetry.Available {
				t.Fatal("available")
			}
			d.register(e.url, "a@example.com")
			time.Sleep(100 * time.Millisecond)
			if n := ph.names(); len(n) != 0 {
				t.Fatalf("sent %v", n)
			}
		})
	}
}

// Opting out stops everything at once, survives a restart, and drops the ID.
func TestTelemetryOptOut(t *testing.T) {
	ph := newFakePostHog(t)
	e := newDaemonEnv(t, openPortal(), false)
	home := t.TempDir()
	d := e.telemetryDaemon(home, ph.URL)
	if st := d.session().Telemetry; !st.Available || !st.Enabled {
		t.Fatalf("state: %+v", st)
	}
	ph.waitFor(t, eventDailyActive)
	first := ph.events[0].DistinctID
	d.want(200, "", "PUT", "/api/v1/telemetry", map[string]bool{"enabled": false})
	if d.session().Telemetry.Enabled {
		t.Fatal("still enabled")
	}
	sent := len(ph.names())
	d.register(e.url, "a@example.com")
	d.want(200, "", "POST", "/api/v1/logout", nil)
	d.closeOnce()
	data, err := os.ReadFile(filepath.Join(home, TelemetryFile))
	if err != nil || strings.Contains(string(data), "install:") {
		t.Fatalf("file: %s %v", data, err)
	}
	if info, _ := os.Stat(filepath.Join(home, TelemetryFile)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	d2 := e.telemetryDaemon(home, ph.URL)
	d2.portalForTest = e.url
	if d2.session().Telemetry.Enabled {
		t.Fatal("the opt-out did not survive a restart")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(ph.names()); n != sent {
		t.Fatalf("%d events after opting out", n-sent)
	}
	d2.want(200, "", "PUT", "/api/v1/telemetry", map[string]bool{"enabled": true})
	ph.waitForAfter(t, sent, d2)
	if got := ph.lastID(); got == first {
		t.Fatal("opting in again kept the old ID")
	}
}

func (f *fakePostHog) waitForAfter(t *testing.T, n int, d *testDaemon) {
	t.Helper()
	d.want(401, "bad_credentials", "POST", "/api/v1/login", map[string]string{"portal": d.portalForTest, "account": "x@example.com", "password": "wrong password"})
	for deadline := time.Now().Add(5 * time.Second); len(f.names()) <= n; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no event after opting in again")
		}
	}
}

func (f *fakePostHog) lastID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[len(f.events)-1].DistinctID
}

// An unreadable settings file may hold an opt-out, so it means off.
func TestTelemetryCorruptFileMeansOff(t *testing.T) {
	ph := newFakePostHog(t)
	e := newDaemonEnv(t, openPortal(), false)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, TelemetryFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := e.telemetryDaemon(home, ph.URL)
	if d.session().Telemetry.Enabled {
		t.Fatal("enabled")
	}
	time.Sleep(50 * time.Millisecond)
	if n := ph.names(); len(n) != 0 {
		t.Fatalf("sent %v", n)
	}
}

// Active events go out once per UTC day and hour, also across restarts.
func TestTelemetryActiveOncePerPeriod(t *testing.T) {
	ph := newFakePostHog(t)
	home := t.TempDir()
	now := time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)
	mk := func() *Telemetry {
		tm := newTelemetry(home, "k", ph.URL, func() (string, bool) { return StateRunning, true })
		tm.now = func() time.Time { return now }
		return tm
	}
	tm := mk()
	active := func(tm *Telemetry) {
		tm.markActive()
		tm.flush(t.Context())
	}
	active(tm)
	active(tm)
	check := func(day, hour int) {
		t.Helper()
		if d, h := ph.count(eventDailyActive), ph.count(eventHourlyActive); d != day || h != hour {
			t.Fatalf("daily %d hourly %d, want %d %d", d, h, day, hour)
		}
	}
	check(1, 1)
	active(mk()) // a restart in the same hour
	check(1, 1)
	now = now.Add(time.Hour)
	active(mk())
	check(1, 2)
	now = now.Add(24 * time.Hour)
	active(mk())
	check(2, 3)
	ph.mu.Lock()
	defer ph.mu.Unlock()
	p := ph.events[0].Properties
	if p.State != StateRunning || p.Sharing == nil || !*p.Sharing {
		t.Fatalf("props: %+v", p)
	}
}

// A dead or hanging collector costs the user nothing, and the login still works.
func TestTelemetryFailureIsHarmless(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer hang.Close()
	defer close(release)
	e := newDaemonEnv(t, openPortal(), false)
	for name, host := range map[string]string{"down": "http://127.0.0.1:1", "hanging": hang.URL} {
		t.Run(name, func(t *testing.T) {
			d := e.telemetryDaemon(t.TempDir(), host)
			began := time.Now()
			d.register(e.url, name+"@example.com")
			if took := time.Since(began); took > 2*time.Second {
				t.Fatalf("sign-up took %v", took)
			}
			began = time.Now()
			d.closeOnce()
			if took := time.Since(began); took > telemetrySendLimit+3*time.Second {
				t.Fatalf("closing took %v", took)
			}
		})
	}
}

// Closing is bounded even when a send is already stuck on the collector.
func TestTelemetryCloseDoesNotWaitForStuckSend(t *testing.T) {
	release := make(chan struct{})
	reached := make(chan struct{}, 8)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-release
	}))
	defer hang.Close()
	defer close(release)
	tm := newTelemetry(t.TempDir(), "k", hang.URL, func() (string, bool) { return "", false })
	tm.start()
	tm.signedIn("login")
	select {
	case <-reached:
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was sent")
	}
	began := time.Now()
	tm.Close()
	if took := time.Since(began); took > telemetryCloseLimit+time.Second {
		t.Fatalf("closing took %v", took)
	}
}

func TestTelemetryQueueIsBounded(t *testing.T) {
	tm := newTelemetry(t.TempDir(), "k", "http://127.0.0.1:1", func() (string, bool) { return "", false })
	for range telemetryQueueMax * 2 {
		tm.signedIn("login")
	}
	if n := len(tm.queue); n != telemetryQueueMax {
		t.Fatalf("queue %d", n)
	}
}

func TestTelemetryEndpoint(t *testing.T) {
	for host, want := range map[string]string{
		"https://us.i.posthog.com":     "https://us.i.posthog.com/batch/",
		"https://ph.corp.example.com/": "https://ph.corp.example.com/batch/",
		"http://127.0.0.1:8000":        "http://127.0.0.1:8000/batch/",
		"http://ph.corp.example.com":   "",
		"":                             "",
		"ftp://x":                      "",
	} {
		if got := telemetryEndpoint(host); got != want {
			t.Errorf("%q: %q, want %q", host, got, want)
		}
	}
	if newTelemetry(t.TempDir(), "k", "http://ph.corp.example.com", nil) != nil {
		t.Error("plain HTTP collector accepted")
	}
}

func TestTelemetryCodesAreAllowListed(t *testing.T) {
	tm := newTelemetry(t.TempDir(), "k", "http://127.0.0.1:1", nil)
	tm.signInFailed("login", "bad_credentials")
	tm.signInFailed("login", "user alice@example.com not found")
	tm.signInFailed("login", "mfa_required")
	if len(tm.queue) != 2 || tm.queue[0].Properties.Reason != "bad_credentials" || tm.queue[1].Properties.Reason != "other" {
		t.Fatalf("queue: %+v", tm.queue)
	}
}
