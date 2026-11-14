package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// supervisor finds the daemon for this user's state directory, or starts it.
type supervisor struct {
	home   string
	addr   string
	key    string // the daemon's local API key, once it proved it holds it
	binary string // daemon executable; "" means look it up
	logTo  string
	// spare holds the addresses to use if addr belongs to another program. Only
	// the default address has them: an address the user chose is not replaced.
	spare []string

	mu      sync.Mutex
	cmd     *exec.Cmd // set when this process started the daemon
	stdin   io.Closer // closing it stops that daemon
	exited  chan struct{}
	started bool
}

func newSupervisor() *supervisor {
	home := homeDir()
	s := &supervisor{home: home, addr: daemonAddr(home), logTo: filepath.Join(home, "daemon.log")}
	if s.addr == current().defaultHTTP {
		s.spare = spareAddrs()
	}
	return s
}

// spareAddrs follows the default port. They are tried in order, so the local
// endpoint usually keeps its port from one start to the next, and tools set up
// with it keep working.
func spareAddrs() []string {
	var addrs []string
	for port := current().firstSpare; port <= current().lastSpare; port++ {
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

// runInfo is the file a running client records itself in (internal/client
// RunFile): where its local API is, and its key (internal/client/localauth.go).
type runInfo struct {
	API string `json:"api"`
	PID int    `json:"pid"`
	Key string `json:"key"`
}

// runningClient returns the client that is running on home now, from the file
// it records itself in. The file outlives a client that was killed, so the
// process has to be alive too. Only this user can write the file (the
// directory is 0700), so its key is the client's.
func runningClient(home string) (runInfo, bool) {
	var ri runInfo
	data, err := os.ReadFile(filepath.Join(home, "run.json"))
	if err != nil || json.Unmarshal(data, &ri) != nil || ri.API == "" || !processAlive(ri.PID) {
		return runInfo{}, false
	}
	return ri, true
}

func homeDir() string {
	if h := os.Getenv("SHARETOKEN_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, current().homeDir)
	}
	return current().homeDir
}

// daemonAddr is where the daemon of home listens: config.json's "http". A file
// of no or another build type does not count: the daemon deletes or refuses it.
func daemonAddr(home string) string {
	var c struct {
		Env  string `json:"env"`
		HTTP string `json:"http"`
	}
	if data, err := os.ReadFile(filepath.Join(home, "config.json")); err == nil && json.Unmarshal(data, &c) == nil && c.Env == Env && c.HTTP != "" {
		return c.HTTP
	}
	return current().defaultHTTP
}

// findBinary looks for the daemon next to this executable (inside the macOS
// app bundle, or in the same directory on Linux and Windows), then on $PATH.
func findBinary() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		if candidate, ok := daemonIn(filepath.Dir(exe)); ok {
			return candidate, nil
		}
	}
	p, err := exec.LookPath(daemonName())
	if err != nil {
		return "", fmt.Errorf("the %s program was not found next to this app or on PATH", daemonName())
	}
	return p, nil
}

// daemonIn returns the daemon executable of this build type in dir, if there is
// one. Windows has no executable bit, only the .exe name.
func daemonIn(dir string) (string, bool) {
	path := filepath.Join(dir, daemonName())
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || (filepath.Ext(path) != ".exe" && info.Mode()&0o111 == 0) {
		return "", false
	}
	return path, true
}

// probeState says who answers at addr.
type probeState int

const (
	probeNothing      probeState = iota // nobody listens
	probeDaemon                         // this user's sharetoken daemon of this build type: it proved it holds the key
	probeOther                          // something else: another program, `sharetoken up`, another user's client, or one that cannot prove the key
	probeOtherVersion                   // a sharetoken daemon of this build type, but another release
)

// probe says whether anyone listens at addr, and if so, whether it is this
// user's daemon with key.
func probe(ctx context.Context, addr, key string) probeState {
	st, _ := probeVersion(ctx, addr, key)
	return st
}

// probeVersion says who answers at addr and, for a daemon, which release it is.
// The app's pages come from the daemon, so a daemon of another release would
// show this app another release's pages.
//
// A daemon is this user's only if it proves it holds key (from run.json) by
// answering a fresh nonce with LocalProof (internal/client/localauth.go). The
// key itself is not sent: whatever holds the port may be another user's program.
func probeVersion(ctx context.Context, addr, key string) (probeState, string) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return probeNothing, ""
	}
	c.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	n := hex.EncodeToString(nonce)
	var h struct {
		Env     string `json:"env"`
		Version string `json:"version"`
		Proof   string `json:"proof"`
	}
	if !getJSON(ctx, "http://"+addr+"/api/v1/hello?nonce="+n, &h) || h.Env == "" {
		// A daemon of a release before the hello still says what it is here.
		var s struct {
			Env     string `json:"env"`
			State   string `json:"state"`
			Version string `json:"version"`
		}
		if getJSON(ctx, "http://"+addr+"/api/v1/session", &s) && s.State != "" && s.Env == Env && s.Version != version {
			return probeOtherVersion, s.Version
		}
		return probeOther, ""
	}
	if h.Env != Env {
		return probeOther, ""
	}
	if h.Version != version {
		return probeOtherVersion, h.Version
	}
	if key == "" || !hmac.Equal([]byte(h.Proof), []byte(localProof(key, addr, n))) {
		return probeOther, ""
	}
	return probeDaemon, h.Version
}

// localProof repeats internal/client.LocalProof, which this module cannot import.
func localProof(key, addr, nonce string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte("sharetoken-local-api-hello\n" + addr + "\n" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// getJSON reads a 200 answer to a GET of url into out.
func getJSON(ctx context.Context, url string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(out) == nil
}

// endpoint is where the daemon answers and the key its API wants.
type endpoint struct{ addr, key string }

// request makes a request of the daemon's local API, with its key.
func (e endpoint) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+e.addr+path, body)
	if err == nil && e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	return req, err
}

// endpoint is valid once start has returned without an error.
func (s *supervisor) endpoint() endpoint { return endpoint{s.addr, s.key} }

// otherVersionError explains a daemon left running by an earlier release. One
// this app starts ends with it, so this one was started by hand.
func otherVersionError(addr, running string) error {
	return fmt.Errorf("the sharetoken service at %s is release %s, and this app is release %s. Stop it (`sharetoken daemon`) and reopen this app", addr, running, version)
}

// start makes sure a daemon answers at the address. One that already runs is
// used and left alone; one this process starts ends with it (see stop).
func (s *supervisor) start(ctx context.Context) error {
	// A client on this home comes first, wherever it listens: a second one would
	// put the same device key on the network twice.
	if ri, ok := runningClient(s.home); ok {
		switch st, running := probeVersion(ctx, ri.API, ri.Key); st {
		case probeDaemon:
			s.addr, s.key = ri.API, ri.Key
			return nil
		case probeOtherVersion:
			return otherVersionError(ri.API, running)
		case probeOther:
			return fmt.Errorf("`sharetoken up` is running on this data directory (%s, at %s), or a program that is not this user's sharetoken service holds its port. Stop it and reopen this app", s.home, ri.API)
		}
	}
	// Without run.json there is no key to check a daemon against: whatever
	// answers at the address is not used.
	switch st, running := probeVersion(ctx, s.addr, ""); st {
	case probeOtherVersion:
		return otherVersionError(s.addr, running)
	case probeOther, probeDaemon:
		free, ok := firstFree(ctx, s.spare)
		if !ok {
			return fmt.Errorf("port %s is in use by another program. If it is `sharetoken up`, stop it and reopen this app", s.addr)
		}
		log.Printf("sharetoken-gui: port %s is in use by another program; using %s", s.addr, free)
		s.addr = free
	}
	bin := s.binary
	if bin == "" {
		var err error
		if bin, err = findBinary(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(s.logTo, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(bin, "-home", s.home, "daemon", "-http", s.addr, "-stop-on-stdin-eof")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = daemonProcAttr()
	// The daemon stops when this pipe closes: at stop, or if this process dies.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", bin, err)
	}
	exited := make(chan struct{})
	s.mu.Lock()
	s.cmd, s.stdin, s.exited, s.started = cmd, stdin, exited, true
	s.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	deadline := time.After(20 * time.Second)
	for {
		// The daemon records its key in run.json once it listens; only the one
		// this process started counts.
		if ri, ok := runningClient(s.home); ok && ri.PID == cmd.Process.Pid && ri.API == s.addr && probe(ctx, s.addr, ri.Key) == probeDaemon {
			s.key = ri.Key
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("the sharetoken service exited at start-up: %s (log: %s)", lastLine(s.logTo), s.logTo)
		case <-deadline:
			return fmt.Errorf("the sharetoken service did not answer within 20s (log: %s)", s.logTo)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// firstFree returns the first address that nobody listens on.
func firstFree(ctx context.Context, addrs []string) (string, bool) {
	for _, addr := range addrs {
		if probe(ctx, addr, "") == probeNothing {
			return addr, true
		}
	}
	return "", false
}

// stop ends a daemon this process started. It gets as long as it needs to let
// disputes in flight finish, a bit over the 30s it allows for them.
func (s *supervisor) stop() {
	s.mu.Lock()
	cmd, stdin, exited := s.cmd, s.stdin, s.exited
	s.cmd = nil
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = stdin.Close()
	select {
	case <-exited:
	case <-time.After(40 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
}

func lastLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "no log"
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return lines[len(lines)-1]
}
