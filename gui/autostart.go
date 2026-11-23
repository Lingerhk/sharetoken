package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Starting the app at login. The app keeps the sharetoken service running
// while it runs (closing the window leaves it in the tray), and Claude Code
// and Codex, once connected to the local endpoint, reach even their own
// vendor's models through it: without the app they stop working. So the page
// offers to start the app itself at login, with its window hidden
// (hiddenArg), and says what happens without it.
//
//	macOS    a LaunchAgent that opens the app (open -g), not kept alive: quitting stays quitting
//	Windows  a value under HKCU\Software\Microsoft\Windows\CurrentVersion\Run
//	Linux    an XDG autostart entry (~/.config/autostart)
//
// The page asks for it at autostartPath, which the shell answers itself: the
// daemon behind it would set up a service of its own instead (the CLI's
// sharetoken autostart), which the app's updates would not replace.

// hiddenArg starts the app with its window hidden, in the tray.
const hiddenArg = "--hidden"

// autostartPath is the daemon's path for the setting, answered here.
const autostartPath = "/api/v1/autostart"

// startHidden reports whether this start asked for the window hidden.
func startHidden() bool { return slices.Contains(os.Args[1:], hiddenArg) }

// trayAvailableSoon is trayAvailable, waiting for it a while when wait is
// set: at login the desktop's tray (GNOME's AppIndicator extension) may come
// up after the app, which would then show its window, having nowhere to
// hide.
func trayAvailableSoon(wait bool) bool {
	start := time.Now()
	for {
		if trayAvailable() {
			if waited := time.Since(start); waited > time.Second {
				log.Printf("sharetoken-gui: the tray came up after %s", waited.Round(time.Second))
			}
			return true
		}
		if !wait || time.Since(start) > trayWait {
			if wait {
				log.Printf("sharetoken-gui: no tray after %s: showing the window", trayWait)
			}
			return false
		}
		time.Sleep(time.Second)
	}
}

// trayWait is how long a start at login waits for the tray.
const trayWait = time.Minute

// autostartName is the name of the entry, by build type.
func autostartName() string {
	if Env == envProd {
		return "ShareToken"
	}
	return "ShareToken-" + Env
}

// appExecutable is this program, symlinks resolved.
func appExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}

// serveAutostart answers GET and PUT autostartPath.
func serveAutostart(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		var req struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			reply(http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "bad_request", "message": "malformed JSON"}})
			return
		}
		if err := setAppAutostart(req.On); err != nil {
			reply(http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "autostart", "message": err.Error()}})
			return
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	on, where := appAutostart()
	reply(http.StatusOK, map[string]any{"on": on, "where": where, "app": true, "tray": trayAvailable()})
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&"+"amp;", "<", "&"+"lt;", ">", "&"+"gt;", "\"", "&"+"quot;").Replace(s)
}
