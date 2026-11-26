package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"
)

// The shell's log and crash record, next to the client's (internal/diag: the
// same directory and names; the daemon turns a crash of the shell into a
// report, so this module needs nothing else of it).
const (
	guiLogName   = "gui.log"
	daemonStderr = "daemon.stderr"
	guiLogMax    = 10 << 20
	guiLogKeep   = 5
	logDirEnv    = "SHARETOKEN_LOG_DIR"
	crashDirName = "crash"
)

// logDir is where the client of home logs: $SHARETOKEN_LOG_DIR, else
// config.json's log.dir, else <home>/logs (internal/diag.Dir).
func logDir(home string) string {
	d := os.Getenv(logDirEnv)
	if d == "" {
		var c struct {
			Log struct {
				Dir string `json:"dir"`
			} `json:"log"`
		}
		if data, err := os.ReadFile(filepath.Join(home, "config.json")); err == nil && json.Unmarshal(data, &c) == nil {
			d = c.Log.Dir
		}
	}
	if d == "" {
		return filepath.Join(home, "logs")
	}
	if len(d) > 1 && d[0] == '~' && (d[1] == '/' || d[1] == '\\') {
		if h, err := os.UserHomeDir(); err == nil {
			d = filepath.Join(h, d[2:])
		}
	}
	if abs, err := filepath.Abs(d); err == nil {
		return abs
	}
	return d
}

// guiDiag is the shell's log file and crash file.
type guiDiag struct {
	log   *os.File
	crash string
}

// startGUILog makes dir/gui.log the shell's log (rotated at start once it is
// over 10 MiB: the shell writes little) and has the runtime record a crash in
// dir/crash. Started from the Finder or the Start menu, the shell's standard
// error goes nowhere, so without the file its lines would be lost.
func startGUILog(dir string) *guiDiag {
	g := &guiDiag{}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return g
	}
	p := filepath.Join(dir, guiLogName)
	if st, err := os.Stat(p); err == nil && st.Size() > guiLogMax {
		_ = os.Remove(p + "." + strconv.Itoa(guiLogKeep))
		for i := guiLogKeep - 1; i >= 1; i-- {
			_ = os.Rename(p+"."+strconv.Itoa(i), p+"."+strconv.Itoa(i+1))
		}
		_ = os.Rename(p, p+".1")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		g.log = f
		// Also standard error: a shell started from a terminal shows its lines.
		slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(f, os.Stderr), nil)))
	}
	cd := filepath.Join(dir, crashDirName)
	if os.MkdirAll(cd, 0o700) == nil {
		cp := filepath.Join(cd, sprintfCrash(time.Now().UTC().Format("20060102T150405Z"), os.Getpid()))
		if f, err := os.OpenFile(cp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			if debug.SetCrashOutput(f, debug.CrashOptions{}) == nil {
				g.crash = cp
			} else {
				_ = os.Remove(cp)
			}
			f.Close()
		}
	}
	return g
}

func sprintfCrash(ts string, pid int) string {
	return "crash-gui-" + ts + "-" + strconv.Itoa(pid) + ".txt"
}

// finish ends the crash record at a clean exit, removing the empty file.
func (g *guiDiag) finish() {
	if g == nil || g.crash == "" {
		return
	}
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	if st, err := os.Stat(g.crash); err == nil && st.Size() == 0 {
		_ = os.Remove(g.crash)
	}
	g.crash = ""
}
