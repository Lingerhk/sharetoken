package main

import (
	"context"
	"fmt"
	"io"
	"net"
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
	addr, home := "", ""
	for i, a := range os.Args {
		switch a {
		case "-http":
			addr = os.Args[i+1]
		case "-home":
			home = os.Args[i+1]
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		println(err.Error())
		os.Exit(1)
	}
	key := "fake-daemon-key"
	data := fmt.Sprintf(`{"api":%q,"pid":%d,"key":%q}`, addr, os.Getpid(), key)
	if err := os.WriteFile(filepath.Join(home, "run.json"), []byte(data), 0o600); err != nil {
		println(err.Error())
		os.Exit(1)
	}
	println(http.Serve(ln, fakeDaemonAPI(addr, key, version)).Error())
}

// fakeDaemonAPI answers like a daemon of release ver at addr with key: the
// hello without the key, the session only with it.
func fakeDaemonAPI(addr, key, ver string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"env":%q,"version":%q,"proof":%q}`, Env, ver, localProof(key, addr, r.URL.Query().Get("nonce")))
	})
	mux.HandleFunc("/api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Seen-Origin", r.Header.Get("Origin"))
		w.Header().Set("X-Seen-Host", r.Host)
		io.WriteString(w, `{"env":"`+Env+`","state":"logged_out","version":"`+ver+`"}`)
	})
	return mux
}

// serveFakeDaemon runs fakeDaemonAPI on a port of its own and returns its address.
func serveFakeDaemon(t *testing.T, key string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := &http.Server{Handler: fakeDaemonAPI(addr, key, version)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return addr
}

// keyOf is the key the daemon on home recorded.
func keyOf(home string) string {
	ri, _ := runningClient(home)
	return ri.Key
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
	if !s.started || s.key != "fake-daemon-key" || probe(context.Background(), addr, s.key) != probeDaemon {
		t.Fatal("daemon not running")
	}
	begin := time.Now()
	s.stop()
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("stop waited %v: the daemon did not stop when asked", d)
	}
	if got := probe(context.Background(), addr, ""); got != probeNothing {
		t.Fatalf("after stop: %v", got)
	}
	s.stop() // idempotent
}

// A daemon that runs already (from an earlier window, or started by hand) is
// used and left running.
func TestAttachesToRunningDaemon(t *testing.T) {
	addr := serveFakeDaemon(t, "k1")
	s := testSupervisor(t, addr, "fail") // would fail if it were started
	writeRunFile(t, s.home, addr, os.Getpid(), "k1")
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.started || s.key != "k1" {
		t.Fatalf("started %v, key %q", s.started, s.key)
	}
	s.stop()
	if probe(context.Background(), addr, "k1") != probeDaemon {
		t.Fatal("a daemon it did not start must keep running")
	}
}

// What holds the port of this user's daemon but cannot prove its key (another
// user's program, waiting for the port) gets neither the window nor the key.
func TestRefusesDaemonWithoutTheKey(t *testing.T) {
	addr := serveFakeDaemon(t, "someone-else")
	s := testSupervisor(t, freeAddr(t), "fail")
	writeRunFile(t, s.home, addr, os.Getpid(), "mine")
	err := s.start(context.Background())
	if err == nil || s.started || s.key != "" || !strings.Contains(err.Error(), "not this user's") {
		t.Fatalf("started %v, key %q, err %v", s.started, s.key, err)
	}
}

// Without run.json there is no key to check against: a daemon-like answer at
// the configured address is passed over for a spare port, as any program.
func TestDaemonWithoutRunFileIsNotUsed(t *testing.T) {
	taken := serveFakeDaemon(t, "k1")
	spare := freeAddr(t)
	s := testSupervisor(t, taken, "ok")
	s.spare = []string{spare}
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	if !s.started || s.addr != spare || probe(context.Background(), spare, keyOf(s.home)) != probeDaemon {
		t.Fatalf("started %v, addr %s", s.started, s.addr)
	}
}

// The key goes to the daemon with each request of the window, but not to the
// OpenAI endpoint, whose Authorization is the page's.
func TestProxySendsKey(t *testing.T) {
	var auth []string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.URL.Path+" "+r.Header.Get("Authorization"))
	}))
	defer daemon.Close()
	p := newProxy(&supervisor{addr: strings.TrimPrefix(daemon.URL, "http://"), key: "k1"}, started, nil, nil)
	for _, path := range []string{"/api/v1/session", "/", "/v1/models"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer from-page")
		p.ServeHTTP(httptest.NewRecorder(), req)
	}
	want := []string{"/api/v1/session Bearer k1", "/ Bearer k1", "/v1/models Bearer from-page"}
	if strings.Join(auth, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", auth)
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
	if s.addr != spare || probe(context.Background(), spare, s.key) != probeDaemon {
		t.Fatalf("addr %s, want the daemon at %s", s.addr, spare)
	}
}

func writeRunFile(t *testing.T, home, addr string, pid int, key string) {
	t.Helper()
	data := fmt.Sprintf(`{"api":%q,"pid":%d,"key":%q}`, addr, pid, key)
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
	addr := serveFakeDaemon(t, "k2")
	s := testSupervisor(t, freeAddr(t), "fail") // would fail if it were started
	writeRunFile(t, s.home, addr, os.Getpid(), "k2")
	if err := s.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.started || s.addr != addr || s.key != "k2" {
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
	writeRunFile(t, s.home, strings.TrimPrefix(srv.URL, "http://"), os.Getpid(), "k")
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
	writeRunFile(t, s.home, strings.TrimPrefix(srv.URL, "http://"), cmd.Process.Pid, "k")
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
		{1280, 720, windowWidth, windowHeight},
		{1024, 640, windowWidth, 640 - screenReserve},
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
	p := newProxy(&supervisor{addr: addr}, started, nil, nil)
	req := httptest.NewRequest("POST", "wails://wails/api/v1/auth/verify?x=1", strings.NewReader(`{"a":1}`))
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
	p := newProxy(&supervisor{addr: "127.0.0.1:1"}, started, nil, nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/session", nil))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "daemon_down") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestProxyShowsStartError(t *testing.T) {
	p := newProxy(&supervisor{addr: "127.0.0.1:1"}, func() error { return errString("port <in use>") }, nil, nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "port &lt;in use&gt;") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func started() error { return nil }

type errString string

func (e errString) Error() string { return string(e) }

// A daemon of the other build type on the port is someone else's: the shell must
// not show a dev window on production's account, or the reverse.
func TestOtherBuildTypesDaemonIsNotUsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":"prod","state":"logged_out"}`)
	}))
	defer srv.Close()
	if got := probe(context.Background(), strings.TrimPrefix(srv.URL, "http://"), ""); got != probeOther {
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
