// Package diag is the client's own diagnostics: its log files, the record of
// a crash, and the crash reports it sends to the portal when the user lets it
// (docs: "Logs and crash reports").
//
//   - log.go: the log is written to a file of its own (logs/client.log),
//     rotated by size, whatever started the program.
//   - crash.go: a crash (an unrecovered panic, a fatal runtime error) is
//     written to logs/crash/ as well; the next start turns it into a report.
//   - safego.go: panics that something recovers (net/http's handlers, the
//     goroutines wrapped in Go) become reports at once.
//   - queue.go, upload.go: reports wait in logs/reports/ and are sent with the
//     user's login; nothing is sent while uploads are off, or logged out.
//   - region.go: uploads are on unless the user turns them off, except in the
//     European Economic Area, where they start off.
//
// Every report is redacted (internal/redact) before it is written to the
// queue, and so before the user can see it or it can be sent.
package diag

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"sharetoken/internal/redact"
)

// Kinds of report.
const (
	KindCrash = "crash" // the process died: an unrecovered panic or a fatal error
	KindPanic = "panic" // a panic that was recovered, the process lives on
)

// Processes that write reports.
const (
	ProcDaemon = "daemon" // sharetoken daemon, behind the desktop app
	ProcCLI    = "cli"    // sharetoken up
	ProcGUI    = "gui"    // the desktop app's shell
)

// Limits of one report. The portal enforces them too.
const (
	MaxMessage     = 1024
	MaxStack       = 32 << 10
	MaxContext     = 100 // lines
	MaxContextLine = 512
	MaxReport      = 64 << 10
	MaxFeatures    = 16
)

// Report is one crash or panic, as the client queues it and sends it.
type Report struct {
	ID          string            `json:"id"` // random: a resent report is not counted twice
	Kind        string            `json:"kind"`
	Process     string            `json:"process"`
	Fingerprint string            `json:"fingerprint"`
	Version     string            `json:"version"`
	Env         string            `json:"env,omitempty"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	OSVersion   string            `json:"os_version,omitempty"`
	OccurredAt  int64             `json:"occurred_at"` // unix seconds
	Message     string            `json:"message"`
	Stack       string            `json:"stack,omitempty"`
	Context     []string          `json:"context,omitempty"`  // the log's last lines before it happened
	Features    map[string]string `json:"features,omitempty"` // which settings were on: names and states only
}

// NewID returns a fresh report ID.
func NewID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var (
	idRE      = regexp.MustCompile(`^[0-9a-f]{8,32}$`)
	fpRE      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	metaRE    = regexp.MustCompile(`^[A-Za-z0-9._+\-/ ()]*$`)
	featureRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
)

// ErrInvalid says a report breaks the rules Check enforces.
var ErrInvalid = errors.New("diag: invalid report")

// Check validates r's fields and enforces the size limits, cutting what is too
// long; what cannot be cut is an error.
func (r *Report) Check() error {
	switch {
	case !idRE.MatchString(r.ID):
		return errors.Join(ErrInvalid, errors.New("bad id"))
	case r.Kind != KindCrash && r.Kind != KindPanic:
		return errors.Join(ErrInvalid, errors.New("kind must be crash or panic"))
	case r.Process != ProcDaemon && r.Process != ProcCLI && r.Process != ProcGUI:
		return errors.Join(ErrInvalid, errors.New("bad process"))
	case !fpRE.MatchString(r.Fingerprint):
		return errors.Join(ErrInvalid, errors.New("bad fingerprint"))
	case r.OccurredAt <= 0:
		return errors.Join(ErrInvalid, errors.New("bad occurred_at"))
	}
	for _, f := range []*string{&r.Version, &r.Env, &r.OS, &r.Arch, &r.OSVersion} {
		*f = redact.Truncate(strings.TrimSpace(*f), 64)
		if !metaRE.MatchString(*f) {
			return errors.Join(ErrInvalid, errors.New("bad version, env, os or arch"))
		}
	}
	if len(r.Features) > MaxFeatures {
		return errors.Join(ErrInvalid, errors.New("too many features"))
	}
	for k, v := range r.Features {
		if !featureRE.MatchString(k) || !featureRE.MatchString(v) {
			return errors.Join(ErrInvalid, errors.New("bad feature"))
		}
	}
	r.Message = redact.Truncate(validText(r.Message), MaxMessage)
	r.Stack = redact.Truncate(validText(r.Stack), MaxStack)
	if len(r.Context) > MaxContext {
		r.Context = r.Context[len(r.Context)-MaxContext:]
	}
	for i, l := range r.Context {
		r.Context[i] = redact.Truncate(validText(l), MaxContextLine)
	}
	if r.Message == "" {
		return errors.Join(ErrInvalid, errors.New("empty message"))
	}
	return nil
}

func validText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// Redact rewrites every free-text field of r with rd.
func (r *Report) Redact(rd *redact.Redactor) {
	r.Message = rd.String(r.Message)
	r.Stack = rd.String(r.Stack)
	for i, l := range r.Context {
		r.Context[i] = rd.Line(l)
	}
}

// ---- fingerprints ----

// skipFrame is a frame that says where a panic surfaced, not where it came
// from: the runtime's, and this package's.
func skipFrame(fn string) bool {
	return strings.HasPrefix(fn, "runtime.") || strings.HasPrefix(fn, "runtime/") || strings.HasPrefix(fn, "internal/runtime/") || fn == "panic" ||
		strings.HasPrefix(fn, "sharetoken/internal/diag.Wrap") || strings.HasPrefix(fn, "sharetoken/internal/diag.Go") || strings.HasPrefix(fn, "created by ") ||
		strings.HasPrefix(fn, "net/http.(*conn).serve") || strings.HasPrefix(fn, "net/http.(*Server)")
}

var (
	offsetRE = regexp.MustCompile(` \+0x[0-9a-f]+$`)
	digitsRE = regexp.MustCompile(`0x[0-9a-fA-F]+|\d+`)
)

// Frame is one call in a Go traceback.
type Frame struct{ Func, At string }

// Frames reads the first goroutine of a Go traceback.
func Frames(stack string) []Frame {
	lines := strings.Split(stack, "\n")
	var out []Frame
	in := false
	for i := 0; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(l, "goroutine ") && strings.HasSuffix(l, ":") {
			if in {
				break // only the first goroutine
			}
			in = true
			continue
		}
		if !in || l == "" || strings.HasPrefix(l, "\t") {
			continue
		}
		name := l
		if strings.HasSuffix(name, ")") { // the arguments
			if j := strings.LastIndexByte(name, '('); j > 0 {
				name = name[:j]
			}
		}
		loc := ""
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\t") {
			loc = offsetRE.ReplaceAllString(strings.TrimSpace(lines[i+1]), "")
			if j := strings.LastIndexAny(loc, `/\`); j >= 0 {
				loc = loc[j+1:]
			}
		}
		out = append(out, Frame{name, loc})
	}
	return out
}

// TopFrame finds where a panic came from in a Go traceback: the call after the
// last panic frame (a panic that was recovered and raised again has two), or,
// without one (a fatal error), the first frame that is not the runtime's. It
// returns the function and file:line, or empty strings.
func TopFrame(stack string) (fn, at string) {
	frames := Frames(stack)
	start := 0
	for i, f := range frames {
		if f.Func == "panic" {
			start = i + 1
		}
	}
	for _, f := range frames[start:] {
		if !skipFrame(f.Func) {
			return f.Func, f.At
		}
	}
	return "", ""
}

// Fingerprint names a problem across reports: the frame it came from, or,
// without a traceback, the message with its numbers taken out.
func Fingerprint(message, stack string) string {
	key := ""
	if fn, at := TopFrame(stack); fn != "" {
		key = fn + "@" + at
	} else {
		m := strings.TrimSuffix(strings.TrimSpace(firstLine(message)), " [recovered, repanicked]")
		key = "msg:" + digitsRE.ReplaceAllString(m, "#")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
