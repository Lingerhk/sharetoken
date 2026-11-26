package diag

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/redact"
)

// keepOld is how many processed crash files stay in crash/old for the user.
const keepOld = 5

// CrashFile is where this process's crash, if it has one, is written: the Go
// runtime writes a fatal panic or error there as well as to standard error.
type CrashFile struct{ path string }

// StartCrashCapture opens crash/crash-<process>-<time>-<pid>.txt in the log
// directory and has the runtime write a crash there. The file stays empty
// until something crashes; Finish removes an empty one at a clean exit.
func StartCrashCapture(dir, process string) (*CrashFile, error) {
	cd := filepath.Join(dir, CrashDir)
	if err := os.MkdirAll(cd, 0o700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("crash-%s-%s-%d.txt", process, time.Now().UTC().Format("20060102T150405Z"), os.Getpid())
	p := filepath.Join(cd, name)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close() // the runtime keeps its own copy of the descriptor
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		_ = os.Remove(p)
		return nil, err
	}
	return &CrashFile{path: p}, nil
}

// Path is the crash file's name.
func (c *CrashFile) Path() string { return c.path }

// Finish stops writing crashes to the file and removes it if it is empty.
func (c *CrashFile) Finish() {
	if c == nil {
		return
	}
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	if st, err := os.Stat(c.path); err == nil && st.Size() == 0 {
		_ = os.Remove(c.path)
	}
}

var crashNameRE = regexp.MustCompile(`^crash-(daemon|cli|gui)-(\d{8}T\d{6}Z)-(\d+)\.txt$`)

// Collector turns the crash files that earlier runs left into reports.
type Collector struct {
	Dir      string // the log directory
	Reporter *Reporter
}

// logFor is the log a process writes, whose last lines go with its crash.
func logFor(process string) string {
	if process == ProcGUI {
		return GUILog
	}
	return ClientLog
}

// Collect handles every crash file of another process: one with a crash in it
// becomes a report (when the reporter is on) and moves to crash/old, an empty
// one of a process that is gone is removed. It returns the crashes found.
func (c Collector) Collect() int {
	cd := filepath.Join(c.Dir, CrashDir)
	ents, err := os.ReadDir(cd)
	if err != nil {
		return 0
	}
	found := 0
	for _, e := range ents {
		m := crashNameRE.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		pid, _ := strconv.Atoi(m[3])
		if pid == os.Getpid() {
			continue
		}
		p := filepath.Join(cd, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() == 0 {
			if !pidAlive(pid) {
				_ = os.Remove(p)
			}
			continue
		}
		// A process that is still alive is still writing its crash.
		if pidAlive(pid) && time.Since(info.ModTime()) < 30*time.Second {
			continue
		}
		found++
		if c.Reporter != nil && c.Reporter.On() {
			if r, ok := c.report(p, m[1], info.ModTime()); ok {
				c.Reporter.add(r)
			}
		}
		c.keep(p, e.Name())
	}
	return found
}

func (c Collector) report(path, process string, at time.Time) (Report, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Report{}, false
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 512<<10))
	text := strings.ToValidUTF8(string(data), "�")
	msg, stack := splitCrash(text)
	if strings.HasPrefix(msg, "goroutine ") || msg == "" {
		// A fatal error's first line ("fatal error: concurrent map writes")
		// goes only to standard error, which a daemon the app started writes
		// to daemon.stderr; else the runtime frame that threw names it.
		stack, msg = text, fatalLine(filepath.Join(c.Dir, StderrLog), at)
		if msg == "" {
			msg = "fatal error"
			if fs := Frames(stack); len(fs) > 0 {
				msg += ": " + fs[0].Func
			}
		}
	}
	r := c.Reporter.base(KindCrash, process)
	r.OccurredAt = at.Unix()
	r.Message, r.Stack = msg, stack
	r.Fingerprint = Fingerprint(msg, stack)
	r.Context = linesBefore(Tail(filepath.Join(c.Dir, logFor(process)), MaxContext*2), at.Add(2*time.Second), MaxContext)
	return r, true
}

// splitCrash takes the message (panic: …, fatal error: …) from the start of a
// crash's output; the rest is the traceback.
func splitCrash(text string) (msg, stack string) {
	text = strings.TrimLeft(text, "\r\n")
	i := strings.Index(text, "\ngoroutine ")
	if i < 0 {
		i = strings.Index(text, "\n\n")
	}
	if i < 0 {
		return strings.TrimSpace(text), ""
	}
	return strings.TrimSpace(text[:i]), strings.TrimLeft(text[i:], "\r\n")
}

// fatalLine finds the "fatal error: …" line a crash at t wrote to standard
// error, when that went to the file at path.
func fatalLine(path string, t time.Time) string {
	st, err := os.Stat(path)
	if err != nil || st.ModTime().Sub(t).Abs() > 10*time.Second {
		return ""
	}
	lines := Tail(path, 400)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "fatal error: ") {
			return lines[i]
		}
	}
	return ""
}

// linesBefore keeps the lines logged at or before t (the crashed run's, not
// the lines of the run that collects it), at most n.
func linesBefore(lines []string, t time.Time, n int) []string {
	cut := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if at, ok := lineTime(lines[i]); ok {
			if at.After(t) {
				cut = i
				continue
			}
			break
		}
	}
	lines = lines[:cut]
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return slices.Clone(lines)
}

// lineTime reads the time of one of slog's text lines.
func lineTime(l string) (time.Time, bool) {
	v, ok := strings.CutPrefix(l, "time=")
	if !ok {
		return time.Time{}, false
	}
	if i := strings.IndexByte(v, ' '); i >= 0 {
		v = v[:i]
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	return t, err == nil
}

// keep moves a processed crash file to crash/old, keeping the newest few.
func (c Collector) keep(path, name string) {
	old := filepath.Join(c.Dir, CrashDir, "old")
	if err := os.MkdirAll(old, 0o700); err != nil || os.Rename(path, filepath.Join(old, name)) != nil {
		_ = os.Remove(path)
		return
	}
	ents, err := os.ReadDir(old)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if crashNameRE.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	// Names sort by process, then time: order by the time part.
	slices.SortFunc(names, func(a, b string) int {
		return strings.Compare(crashNameRE.FindStringSubmatch(a)[2], crashNameRE.FindStringSubmatch(b)[2])
	})
	for len(names) > keepOld {
		_ = os.Remove(filepath.Join(old, names[0]))
		names = names[1:]
	}
}

// redactorFor is the Redactor for this machine and user.
func redactorFor(self string) *redact.Redactor {
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	home, _ := os.UserHomeDir()
	return redact.New(home, user, host, self)
}

// RedactorFor is the Redactor the reports of this machine use.
func RedactorFor(self string) *redact.Redactor { return redactorFor(self) }
