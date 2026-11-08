package main

import (
	"context"
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

// runningClient returns the address of a client that is running on home now,
// from the file it records itself in. The file outlives a client that was
// killed, so the process has to be alive too.
func runningClient(home string) (string, bool) {
	var ri struct {
		API string `json:"api"`
		PID int    `json:"pid"`
	}
	data, err := os.ReadFile(filepath.Join(home, "run.json"))
	if err != nil || json.Unmarshal(data, &ri) != nil || ri.API == "" || !processAlive(ri.PID) {
		return "", false
	}
	return ri.API, true
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
	probeDaemon                         // a sharetoken daemon of this build type
	probeOther                          // something else, a client without the app's API, or the other build type's daemon
	probeOtherVersion                   // a sharetoken daemon of this build type, but another release
)

func probe(ctx context.Context, addr string) probeState {
	st, _ := probeVersion(ctx, addr)
	return st
}

// probeVersion says who answers at addr and, for a daemon, which release it is.
// The app's pages come from the daemon, so a daemon of another release would
// show this app another release's pages.
func probeVersion(ctx context.Context, addr string) (probeState, string) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return probeNothing, ""
	}
	c.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/v1/session", nil)
	if err != nil {
		return probeOther, ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeOther, ""
	}
	defer resp.Body.Close()
	var s struct {
		Env     string `json:"env"`
		State   string `json:"state"`
		Version string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&s) != nil || s.State == "" || s.Env != Env {
		return probeOther, ""
	}
	if s.Version != version {
		return probeOtherVersion, s.Version
	}
	return probeDaemon, s.Version
}

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
	if addr, ok := runningClient(s.home); ok {
		switch st, running := probeVersion(ctx, addr); st {
		case probeDaemon:
			s.addr = addr
			return nil
		case probeOtherVersion:
			return otherVersionError(addr, running)
		case probeOther:
			return fmt.Errorf("`sharetoken up` is running on this data directory (%s, at %s). Stop it and reopen this app", s.home, addr)
		}
	}
	switch st, running := probeVersion(ctx, s.addr); st {
	case probeDaemon:
		return nil
	case probeOtherVersion:
		return otherVersionError(s.addr, running)
	case probeOther:
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
		if probe(ctx, s.addr) == probeDaemon {
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
		if probe(ctx, addr) == probeNothing {
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
