package diag

import (
	"fmt"
	"log"
	"log/slog"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Reporter makes reports for one process and queues them.
type Reporter struct {
	Process  string
	Version  string
	Env      string
	Queue    Queue
	Enabled  func() bool              // whether reports are made and sent; read at each report
	Self     func() string            // this device's peer ID, kept shortened; may be nil
	Features func() map[string]string // which settings are on; may be nil

	mu   sync.Mutex
	seen map[string]time.Time // fingerprint → last recovered panic recorded
}

// On reports whether the user lets reports be made.
func (r *Reporter) On() bool { return r != nil && (r.Enabled == nil || r.Enabled()) }

func (r *Reporter) base(kind, process string) Report {
	rep := Report{ID: NewID(), Kind: kind, Process: process, Version: r.Version, Env: r.Env,
		OS: runtime.GOOS, Arch: runtime.GOARCH, OSVersion: OSVersion(), OccurredAt: time.Now().Unix()}
	if r.Features != nil {
		rep.Features = r.Features()
	}
	return rep
}

// add redacts rep and queues it.
func (r *Reporter) add(rep Report) {
	self := ""
	if r.Self != nil {
		self = r.Self()
	}
	rep.Redact(redactorFor(self))
	rep.Fingerprint = Fingerprint(rep.Message, rep.Stack) // the same before and after redaction, but cheap to be sure
	if _, err := r.Queue.Add(rep); err != nil {
		slog.Warn("could not queue a crash report", "component", "diag", "err", err)
	}
}

// Panic records a panic: v is the recovered value, stack where it happened.
// The same panic is recorded at most once an hour by a process. kind is
// KindPanic, or KindCrash when the process is about to die of it.
func (r *Reporter) Panic(kind, where string, v any, stack []byte) {
	if !r.On() {
		return
	}
	msg := fmt.Sprintf("panic: %v", v)
	if err, ok := v.(error); ok {
		msg = "panic: " + err.Error()
	}
	if where != "" {
		msg += " (in " + where + ")"
	}
	fp := Fingerprint(msg, string(stack))
	r.mu.Lock()
	if r.seen == nil {
		r.seen = map[string]time.Time{}
	}
	if t, ok := r.seen[fp]; ok && time.Since(t) < time.Hour {
		r.mu.Unlock()
		return
	}
	r.seen[fp] = time.Now()
	r.mu.Unlock()
	rep := r.base(kind, r.Process)
	rep.Message, rep.Stack, rep.Fingerprint = msg, string(stack), fp
	rep.Context = Tail(r.logPath(), MaxContext)
	r.add(rep)
}

func (r *Reporter) logPath() string {
	if p := logPath.Load(); p != nil {
		return *p
	}
	return ""
}

var (
	current atomic.Pointer[Reporter]
	logPath atomic.Pointer[string]
)

// Install makes r the reporter of this process, and logFile the log whose last
// lines go with its reports.
func Install(r *Reporter, logFile string) {
	current.Store(r)
	logPath.Store(&logFile)
}

// Current is the installed reporter, or nil.
func Current() *Reporter { return current.Load() }

// Wrap returns fn with a guard: a panic in it is recorded at once, then goes
// on (the process dies, as it would have, and is started again). Wrapping the
// long-running goroutines gives their report the goroutine's name; the crash
// file alone would also catch it at the next start.
func Wrap(name string, fn func()) func() {
	return func() {
		defer func() {
			if v := recover(); v != nil {
				if r := Current(); r != nil {
					r.Panic(KindCrash, name, v, debug.Stack())
				}
				panic(v)
			}
		}()
		fn()
	}
}

// Go runs fn in a goroutine with Wrap's guard.
func Go(name string, fn func()) { go Wrap(name, fn)() }

// HTTPErrorLog is an http.Server's ErrorLog that turns a handler panic, which
// net/http recovers and only logs, into a report, and writes every line to
// the log at level error.
func HTTPErrorLog(component string) *log.Logger {
	return log.New(httpErrors{component}, "", 0)
}

type httpErrors struct{ component string }

func (h httpErrors) Write(p []byte) (int, error) {
	s := strings.TrimRight(string(p), "\n")
	first, rest, _ := strings.Cut(s, "\n")
	if strings.HasPrefix(first, "http: panic serving ") {
		msg := strings.TrimPrefix(first, "http: panic serving ")
		if i := strings.Index(msg, ": "); i >= 0 { // the remote address
			msg = msg[i+2:]
		}
		if r := Current(); r != nil {
			r.Panic(KindPanic, h.component, panicText(msg), []byte(rest))
		}
	}
	slog.Error(first, "component", h.component)
	return len(p), nil
}

// panicText keeps a message Panic formats as it is.
type panicText string

func (p panicText) String() string { return string(p) }
