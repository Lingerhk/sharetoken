package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"sharetoken/internal/diag"
	"sharetoken/internal/version"
)

// Diag is a running client's log file and crash reporting (internal/diag).
type Diag struct {
	Dir      string // the log directory
	Home     string
	Process  string
	File     *diag.RotatingFile
	Reporter *diag.Reporter
	Queue    diag.Queue
	crash    *diag.CrashFile
}

// selfPeer is this device's peer ID once known: reports keep it, shortened,
// and hide every other peer's.
var selfPeer atomic.Pointer[string]

func setSelfPeer(id string) {
	if id != "" {
		selfPeer.Store(&id)
	}
}

func currentSelfPeer() string {
	if p := selfPeer.Load(); p != nil {
		return *p
	}
	return ""
}

// UploadReports says whether crash reports are made and sent: the user's
// choice, else the region's default.
func (c Config) UploadReports() bool {
	if c.Diagnostics.Upload != nil {
		return *c.Diagnostics.Upload
	}
	return diag.DefaultUpload()
}

// LogDir is where the client of home logs; flagDir is -log-dir.
func LogDir(home, flagDir string, cfg Config) string { return diag.Dir(home, flagDir, cfg.Log.Dir) }

// StartDiag opens the log file of a long-running client process (the
// daemon, sharetoken up), has the runtime record a crash next to it, and turns
// crashes left by earlier runs (this program's and the desktop app's) into
// reports. tee also writes the log to standard error (a terminal).
func StartDiag(home, process, flagDir string, cfg Config, tee bool) (*Diag, error) {
	dir := LogDir(home, flagDir, cfg)
	f, err := diag.Setup(diag.LogOptions{Dir: dir, File: diag.ClientLog, Level: cfg.Log.Level, MaxSize: cfg.Log.MaxSizeMB, MaxFiles: cfg.Log.MaxFiles, Stderr: tee})
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	d := &Diag{Dir: dir, Home: home, Process: process, File: f, Queue: diag.Queue{Dir: filepath.Join(dir, diag.ReportsDir)}}
	d.Reporter = &diag.Reporter{Process: process, Version: version.Version, Env: Env, Queue: d.Queue,
		Enabled:  func() bool { c, err := LoadConfig(home); return err == nil && c.UploadReports() },
		Self:     currentSelfPeer,
		Features: func() map[string]string { return reportFeatures(home) }}
	diag.Install(d.Reporter, f.Path())
	if process == diag.ProcDaemon {
		moveOldLogs(home, dir)
	}
	if n := (diag.Collector{Dir: dir, Reporter: d.Reporter}).Collect(); n > 0 {
		slog.Warn("found the record of an earlier crash", "component", "diag", "count", n, "reports", d.Reporter.On())
	}
	if d.crash, err = diag.StartCrashCapture(dir, process); err != nil {
		slog.Warn("crashes will not be recorded", "component", "diag", "err", err)
	}
	slog.Info("starting", "component", "diag", "version", version.Version, "process", process, "os", runtime.GOOS, "arch", runtime.GOARCH)
	return d, nil
}

// Close ends crash recording (a clean exit) and closes the log.
func (d *Diag) Close() {
	if d == nil {
		return
	}
	d.crash.Finish()
	_ = d.File.Close()
}

// moveOldLogs moves the logs of releases before 1.0.5, which the desktop app
// (daemon.log) and the macOS login item (~/Library/Logs) wrote and never
// rotated, into the log directory once.
func moveOldLogs(home, dir string) {
	old := []struct{ from, to string }{{filepath.Join(home, "daemon.log"), "daemon.log.old"}}
	if runtime.GOOS == "darwin" {
		if h, err := os.UserHomeDir(); err == nil {
			old = append(old, struct{ from, to string }{filepath.Join(h, "Library", "Logs", autostartName()+".log"), "launchd.log.old"})
		}
	}
	for _, o := range old {
		if _, err := os.Stat(o.from); err != nil {
			continue
		}
		if err := os.Rename(o.from, filepath.Join(dir, o.to)); err != nil {
			slog.Debug("could not move an old log", "component", "diag", "err", err)
		}
	}
}

// reportFeatures are the settings a report names, as on/off: which paths the
// client was taking. Nothing in them says who the user is.
func reportFeatures(home string) map[string]string {
	c, err := LoadConfig(home)
	if err != nil {
		return nil
	}
	onoff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	f := map[string]string{
		"logged_in":     onoff(c.Token != ""),
		"share":         onoff(c.Share.Enabled),
		"tailnet":       onoff(c.Tailnet.Enabled),
		"private_relay": onoff(len(c.PrivateRelays) > 0),
		"keep_official": onoff(c.Share.KeepOfficial),
		"tee":           c.Share.TEE,
	}
	if c.Tailnet.Enabled && c.Tailnet.Transport != "" {
		f["transport"] = c.Tailnet.Transport
	}
	for k, v := range f {
		if v == "" {
			delete(f, k)
		}
	}
	return f
}

// newReportUploader sends this client's reports with the login portal gives;
// portal returns nil while nobody is logged in.
func NewReportUploader(home string, q diag.Queue, portal func() *PortalClient) *diag.Uploader {
	return &diag.Uploader{
		Queue:   q,
		Enabled: func() bool { c, err := LoadConfig(home); return err == nil && c.UploadReports() },
		Send: func(ctx context.Context, r diag.Report) error {
			pc := portal()
			if pc == nil {
				return diag.ErrNotLoggedIn
			}
			err := pc.Do(ctx, http.MethodPost, "/api/v1/client-reports", r, nil)
			var pe *PortalError
			if errors.As(err, &pe) {
				switch pe.Status {
				case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
					return diag.Rejected{Err: err}
				case http.StatusUnauthorized:
					return diag.ErrNotLoggedIn
				}
			}
			return err
		},
		First: envDuration("SHARETOKEN_DIAG_FIRST"),
		Every: envDuration("SHARETOKEN_DIAG_EVERY"),
	}
}

// envDuration reads a duration from the environment, for tests; 0 if unset.
func envDuration(k string) time.Duration {
	d, _ := time.ParseDuration(os.Getenv(k))
	return d
}

// ---- the daemon's API ----

// DiagStatus is what the settings page shows about logs and reports.
type DiagStatus struct {
	Upload        bool        `json:"upload"`
	Chosen        bool        `json:"chosen"`         // the user set Upload; else it is the default
	DefaultUpload bool        `json:"default_upload"` // off in the European Economic Area
	Region        diag.Region `json:"region"`
	Pending       int         `json:"pending"`
	LastSent      int64       `json:"last_sent,omitempty"`
	LastError     string      `json:"last_error,omitempty"`
	LogDir        string      `json:"log_dir"`
	LogFile       string      `json:"log_file"`
}

func (d *Daemon) logDir() string {
	if d.Diag != nil {
		return d.Diag.Dir
	}
	c, _ := LoadConfig(d.home)
	return LogDir(d.home, "", c)
}

func (d *Daemon) reportQueue() diag.Queue {
	return diag.Queue{Dir: filepath.Join(d.logDir(), diag.ReportsDir)}
}

func (d *Daemon) diagStatus() (DiagStatus, error) {
	c, err := LoadConfig(d.home)
	if err != nil {
		return DiagStatus{}, err
	}
	reps, _ := d.reportQueue().List()
	dir := d.logDir()
	st := DiagStatus{Upload: c.UploadReports(), Chosen: c.Diagnostics.Upload != nil, DefaultUpload: diag.DefaultUpload(), Region: diag.SystemRegion(),
		Pending: len(reps), LogDir: dir, LogFile: filepath.Join(dir, diag.ClientLog)}
	if d.reports != nil {
		st.LastSent, st.LastError = d.reports.Status()
	}
	return st, nil
}

// SetReportUpload saves the user's choice about crash reports in home's
// configuration. Turning them off empties the queue: nothing that was waiting
// is sent.
func SetReportUpload(home string, on bool, q diag.Queue) error {
	c, err := LoadConfig(home)
	if err != nil {
		return err
	}
	c.Diagnostics.Upload = &on
	if err := SaveConfig(home, c); err != nil {
		return err
	}
	if !on {
		return q.Clear()
	}
	return nil
}

// testCrashRoutes, set in builds with the diagtest tag (diag_testcrash.go),
// adds endpoints that crash the daemon on purpose.
var testCrashRoutes func(*http.ServeMux)

func (d *Daemon) diagRoutes(mux *http.ServeMux) {
	if testCrashRoutes != nil {
		testCrashRoutes(mux)
	}
	mux.HandleFunc("GET /api/v1/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		st, err := d.diagStatus()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("PUT /api/v1/diagnostics", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		Upload *bool `json:"upload"`
	}) {
		if req.Upload == nil {
			writeErrorCode(w, http.StatusBadRequest, "bad_request", "upload must be true or false")
			return
		}
		if err := SetReportUpload(d.home, *req.Upload, d.reportQueue()); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		d.log.Info("crash reports", "state", map[bool]string{true: "on", false: "off"}[*req.Upload])
		if *req.Upload && d.reports != nil {
			d.reports.Kick()
		}
		st, err := d.diagStatus()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	}))
	// What would be sent: the queue as it is, already redacted.
	mux.HandleFunc("GET /api/v1/diagnostics/reports", func(w http.ResponseWriter, r *http.Request) {
		reps, err := d.reportQueue().List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"reports": reps})
	})
	mux.HandleFunc("POST /api/v1/logs/open", func(w http.ResponseWriter, r *http.Request) {
		dir := d.logDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := openFolder(dir); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"dir": dir})
	})
}

// openFolder shows dir in the system's file manager. $SHARETOKEN_OPEN_DRY_RUN
// (tests) only checks it.
func openFolder(dir string) error {
	if os.Getenv("SHARETOKEN_OPEN_DRY_RUN") != "" {
		_, err := os.Stat(dir)
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // explorer exits 1 even when it opened the folder
	return nil
}
