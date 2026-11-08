package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain turns this test binary into the fake daemon when $FAKE_DAEMON is set.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_DAEMON"); mode != "" {
		runFakeDaemon(mode)
		return
	}
	os.Exit(m.Run())
}

// fakeDaemon returns an executable that stands in for `sharetoken daemon`: this
// test binary, which the supervisor's child finds in fake-daemon mode.
func fakeDaemon(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv("FAKE_DAEMON", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func runFakeDaemon(mode string) {
	if mode == "fail" {
		println("error: boom at start-up")
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}()
	addr := ""
	for i, a := range os.Args {
		if a == "-http" {
			addr = os.Args[i+1]
		}
	}
	http.HandleFunc("/api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Origin", r.Header.Get("Origin"))
		w.Header().Set("X-Seen-Host", r.Host)
		io.WriteString(w, `{"env":"`+Env+`","state":"logged_out","version":"`+version+`"}`)
	})
	println(http.ListenAndServe(addr, nil))
}

func freeAddr(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	return strings.TrimPrefix(srv.URL, "http://")
}

func testSupervisor(t *testing.T, addr, mode string) *supervisor {
	t.Helper()
	home := t.TempDir()
	return &supervisor{home: home, addr: addr, binary: fakeDaemon(t, mode), logTo: filepath.Join(home, "daemon.log")}
}

func TestStartsAndStopsDaemon(t *testing.T) {
	addr := freeAddr(t)
	s := testSupervisor(t, addr, "ok")
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.started || probe(context.Background(), addr) != probeDaemon {
		t.Fatal("daemon not running")
	}
	begin := time.Now()
	s.stop()
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("stop waited %v: the daemon did not stop when asked", d)
	}
	if got := probe(context.Background(), addr); got != probeNothing {
		t.Fatalf("after stop: %v", got)
	}
	s.stop() // idempotent
}

// A daemon that runs already (from an earlier window, or started by hand) is
// used and left running.
func TestAttachesToRunningDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":"`+Env+`","state":"running","version":"`+version+`"}`)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	s := testSupervisor(t, addr, "fail") // would fail if it were started
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.started {
		t.Fatal("started a second daemon")
	}
	s.stop()
	if probe(context.Background(), addr) != probeDaemon {
		t.Fatal("a daemon it did not start must keep running")
	}
}

func TestPortTakenByAnotherProgram(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	s := testSupervisor(t, strings.TrimPrefix(srv.URL, "http://"), "fail")
	err := s.start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("got %v", err)
	}
}

// The default port taken by something unrelated, such as a test client on
// another data directory, is no reason to refuse to start.
func TestDefaultPortTakenUsesSpare(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	spare := freeAddr(t)
	s := testSupervisor(t, strings.TrimPrefix(srv.URL, "http://"), "ok")
	s.spare = []string{strings.TrimPrefix(srv.URL, "http://"), spare} // the first is taken too
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	if s.addr != spare || probe(context.Background(), spare) != probeDaemon {
		t.Fatalf("addr %s, want the daemon at %s", s.addr, spare)
	}
}

func writeRunFile(t *testing.T, home, addr string, pid int) {
	t.Helper()
	data := fmt.Sprintf(`{"api":%q,"pid":%d}`, addr, pid)
	if err := os.WriteFile(filepath.Join(home, "run.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A daemon left running by another release is not used: its pages would not be
// this app's.
func TestRefusesDaemonOfAnotherRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":"`+Env+`","state":"running","version":"0.0.1"}`)
	}))
	defer srv.Close()
	s := testSupervisor(t, strings.TrimPrefix(srv.URL, "http://"), "fail")
	err := s.start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "0.0.1") || s.started {
		t.Fatalf("started %v, err %v", s.started, err)
	}
}

// A daemon that moved to a spare port is found again by the next window.
func TestAttachesToDaemonOnThisHomeAtAnyPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":"`+Env+`","state":"running","version":"`+version+`"}`)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	s := testSupervisor(t, freeAddr(t), "fail") // would fail if it were started
	writeRunFile(t, s.home, addr, os.Getpid())
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.started || s.addr != addr {
		t.Fatalf("started %v, addr %s", s.started, s.addr)
	}
}

// `up` on the same home holds the same device key: moving to a spare port would
// put that key on the network twice.
func TestUpOnThisHomeIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	s := testSupervisor(t, freeAddr(t), "ok")
	s.spare = []string{freeAddr(t)}
	writeRunFile(t, s.home, strings.TrimPrefix(srv.URL, "http://"), os.Getpid())
	err := s.start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "this data directory") || s.started {
		t.Fatalf("started %v, err %v", s.started, err)
	}
}

// A client that was killed leaves its file behind, and its port may have gone
// to another program by now.
func TestStaleRunFileIsIgnored(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	addr := freeAddr(t)
	s := testSupervisor(t, addr, "ok")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = []string{} // not in fake-daemon mode: it exits at once
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	writeRunFile(t, s.home, strings.TrimPrefix(srv.URL, "http://"), cmd.Process.Pid)
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	if !s.started || s.addr != addr {
		t.Fatalf("started %v, addr %s", s.started, s.addr)
	}
}

func TestSpareAddrsOnlyForDefaultAddress(t *testing.T) {
	t.Setenv("SHARETOKEN_HOME", t.TempDir())
	if s := newSupervisor(); s.addr != current().defaultHTTP || len(s.spare) == 0 {
		t.Fatalf("default: %s %v", s.addr, s.spare)
	}
	os.WriteFile(filepath.Join(homeDir(), "config.json"), []byte(`{"env":"`+Env+`","http":"127.0.0.1:1234"}`), 0o600)
	if s := newSupervisor(); s.addr != "127.0.0.1:1234" || s.spare != nil {
		t.Fatalf("chosen: %s %v", s.addr, s.spare)
	}
}

func TestDaemonFailsAtStart(t *testing.T) {
	s := testSupervisor(t, freeAddr(t), "fail")
	err := s.start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom at start-up") {
		t.Fatalf("got %v", err)
	}
}

func TestWindowFitsScreen(t *testing.T) {
	for _, c := range []struct{ sw, sh, w, h int }{
		{2560, 1440, windowWidth, windowHeight},
		{1280, 720, windowWidth, 720 - screenReserve},
		{1024, 768, 1024, 768 - screenReserve},
		{800, 480, 800, minWindowHeight},
	} {
		if w, h := fittedSize(c.sw, c.sh); w != c.w || h != c.h {
			t.Errorf("screen %dx%d: got %dx%d, want %dx%d", c.sw, c.sh, w, h, c.w, c.h)
		}
	}
}

func TestDaemonFoundByPlatformName(t *testing.T) {
	dir := t.TempDir()
	if _, ok := daemonIn(dir); ok {
		t.Fatal("found a daemon in an empty directory")
	}
	path := filepath.Join(dir, daemonName())
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := daemonIn(dir); !ok || got != path {
		t.Fatalf("got %q %v", got, ok)
	}
}

func TestDaemonBinaryMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := findBinary(); err == nil {
		t.Fatal("found a binary")
	}
}

func TestDaemonAddrFromConfig(t *testing.T) {
	home := t.TempDir()
	if got := daemonAddr(home); got != current().defaultHTTP {
		t.Fatal(got)
	}
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"env":"`+Env+`","http":"127.0.0.1:1234"}`), 0o600)
	if got := daemonAddr(home); got != "127.0.0.1:1234" {
		t.Fatal(got)
	}
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`not json`), 0o600)
	if got := daemonAddr(home); got != current().defaultHTTP {
		t.Fatal(got)
	}
}

func TestProxyForwards(t *testing.T) {
	var got struct {
		origin, host, body, ct, method string
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.origin, got.host, got.body, got.ct, got.method = r.Header.Get("Origin"), r.Host, string(b), r.Header.Get("Content-Type"), r.Method
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.WriteHeader(201)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer daemon.Close()
	addr := strings.TrimPrefix(daemon.URL, "http://")
	p := newProxy(&supervisor{addr: addr}, nil, nil)
	req := httptest.NewRequest("POST", "wails://wails/api/v1/login?x=1", strings.NewReader(`{"a":1}`))
	req.Header.Set("Origin", "wails://wails")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 201 || w.Body.String() != `{"ok":true}` || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("reply: %d %s %v", w.Code, w.Body, w.Header())
	}
	if got.origin != "" || got.host != addr || got.body != `{"a":1}` || got.ct != "application/json" || got.method != "POST" {
		t.Fatalf("forwarded: %+v", got)
	}
}

func TestProxyDaemonDown(t *testing.T) {
	p := newProxy(&supervisor{addr: "127.0.0.1:1"}, nil, nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/session", nil))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "daemon_down") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestProxyShowsStartError(t *testing.T) {
	p := newProxy(&supervisor{addr: "127.0.0.1:1"}, errString("port <in use>"), nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "port &lt;in use&gt;") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// A daemon of the other build type on the port is someone else's: the shell must
// not show a dev window on production's account, or the reverse.
func TestOtherBuildTypesDaemonIsNotUsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":"prod","state":"logged_out"}`)
	}))
	defer srv.Close()
	if got := probe(context.Background(), strings.TrimPrefix(srv.URL, "http://")); got != probeOther {
		t.Fatalf("probe = %v, want probeOther", got)
	}
}

func TestBuildTypesDoNotShareAnything(t *testing.T) {
	dev, prod := profiles[envDev], profiles[envProd]
	if dev.homeDir == prod.homeDir || dev.defaultHTTP == prod.defaultHTTP || dev.firstSpare <= prod.lastSpare {
		t.Fatalf("%+v %+v", dev, prod)
	}
	if !strings.HasPrefix(daemonName(), "sharetoken-dev") {
		t.Fatalf("daemon name %q", daemonName())
	}
}

// A config of no or another build type does not choose the address.
func TestDaemonAddrIgnoresOtherConfigs(t *testing.T) {
	home := t.TempDir()
	for _, c := range []string{`{"http":"127.0.0.1:1234"}`, `{"env":"prod","http":"127.0.0.1:1234"}`} {
		os.WriteFile(filepath.Join(home, "config.json"), []byte(c), 0o600)
		if got := daemonAddr(home); got != current().defaultHTTP {
			t.Fatalf("%s: %q", c, got)
		}
	}
}
