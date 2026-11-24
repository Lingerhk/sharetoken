package diag

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Log file names in the log directory.
const (
	ClientLog   = "client.log"    // sharetoken daemon and sharetoken up
	GUILog      = "gui.log"       // the desktop app's shell
	StderrLog   = "daemon.stderr" // what a daemon writes before its log is open, and the runtime's own output
	CrashDir    = "crash"
	ReportsDir  = "reports"
	DirEnv      = "SHARETOKEN_LOG_DIR"
	defaultSize = 10 // MiB
	defaultKeep = 5
)

// Dir is the log directory: $SHARETOKEN_LOG_DIR, else flag, else the
// configuration's, else <home>/logs. It is read once at start: a change takes
// effect at the next start.
func Dir(home, flag, configured string) string {
	for _, d := range []string{os.Getenv(DirEnv), flag, configured} {
		if d != "" {
			if abs, err := filepath.Abs(expandHome(d)); err == nil {
				return abs
			}
			return d
		}
	}
	return filepath.Join(home, "logs")
}

func expandHome(p string) string {
	if p == "~" || len(p) > 1 && p[0] == '~' && (p[1] == '/' || p[1] == '\\') {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// LogOptions describe a log file.
type LogOptions struct {
	Dir      string
	File     string // ClientLog or GUILog
	Level    string // debug, info, warn, error ("": info)
	MaxSize  int    // MiB per file before it rotates (0: 10)
	MaxFiles int    // rotated files kept: file.1 … file.N (0: 5)
	Stderr   bool   // also write to standard error (a terminal)
}

// ParseLevel reads a log level; "" is info.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if s == "" {
		return slog.LevelInfo, nil
	}
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("log level %q: want debug, info, warn or error", s)
	}
	return l, nil
}

// Setup opens the log file of o and makes it the default for slog and for
// package log. It returns the file, which the caller closes at exit.
func Setup(o LogOptions) (*RotatingFile, error) {
	lvl, err := ParseLevel(o.Level)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, err
	}
	f, err := OpenRotating(filepath.Join(o.Dir, o.File), o.MaxSize, o.MaxFiles)
	if err != nil {
		return nil, err
	}
	var w io.Writer = f
	if o.Stderr {
		w = io.MultiWriter(f, os.Stderr)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})))
	return f, nil
}

// RotatingFile is a log file that is renamed to file.1 (file.1 to file.2, and
// so on) when it outgrows its size, keeping a few.
type RotatingFile struct {
	path string
	max  int64
	keep int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenRotating opens path for appending. maxMiB and keep of 0 mean 10 and 5.
func OpenRotating(path string, maxMiB, keep int) (*RotatingFile, error) {
	if maxMiB <= 0 {
		maxMiB = defaultSize
	}
	if keep <= 0 {
		keep = defaultKeep
	}
	r := &RotatingFile{path: path, max: int64(maxMiB) << 20, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Path is the file's name.
func (r *RotatingFile) Path() string { return r.path }

// Write appends p, rotating first if p would take the file past its size.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate shifts the files by one. On Windows a file another program has open
// cannot always be renamed; the file is then copied and emptied instead.
func (r *RotatingFile) rotate() {
	_ = r.f.Close()
	r.f = nil
	_ = os.Remove(r.path + "." + strconv.Itoa(r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(r.path+"."+strconv.Itoa(i), r.path+"."+strconv.Itoa(i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		if data, rerr := os.ReadFile(r.path); rerr == nil && os.WriteFile(r.path+".1", data, 0o600) == nil {
			_ = os.Truncate(r.path, 0)
		}
	}
	if err := r.open(); err != nil {
		// Nowhere to write: drop lines until the next rotation succeeds.
		r.f, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		r.size = 0
	}
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Tail returns up to n last lines of the file at path (and of path.1 when the
// file itself has fewer), reading at most the last 256 KiB of each.
func Tail(path string, n int) []string {
	lines := tailFile(path, n)
	if len(lines) < n {
		older := tailFile(path+".1", n-len(lines))
		lines = append(older, lines...)
	}
	return lines
}

func tailFile(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const window = 256 << 10
	seeked := false
	if st, err := f.Stat(); err == nil && st.Size() > window {
		if _, err := f.Seek(st.Size()-window, io.SeekStart); err != nil {
			return nil
		}
		seeked = true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if seeked { // the window may start mid-line
			seeked = false
			continue
		}
		out = append(out, sc.Text())
		if len(out) > n {
			out = out[1:]
		}
	}
	return out
}
