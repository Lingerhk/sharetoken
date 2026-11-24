package client

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/diag"
)

func queuedReport(t *testing.T, q diag.Queue, fp string) {
	t.Helper()
	ok, err := q.Add(diag.Report{ID: diag.NewID(), Kind: diag.KindCrash, Process: diag.ProcDaemon, Fingerprint: fp, Version: "1.0.6", OS: "darwin", Arch: "arm64",
		OccurredAt: time.Now().Unix(), Message: "panic: test", Stack: "goroutine 1 [running]:\nmain.main()\n\tmain.go:1 +0x1\n"})
	if err != nil || !ok {
		t.Fatalf("queue: %v %v", ok, err)
	}
}

// Reports wait while nobody is logged in, go to the portal after the sign-in,
// and stop (the queue emptied) when the user turns them off.
func TestDaemonDiagnostics(t *testing.T) {
	t.Setenv("SHARETOKEN_DIAG_FIRST", "50ms")
	t.Setenv("SHARETOKEN_DIAG_EVERY", "100ms")
	t.Setenv("SHARETOKEN_OPEN_DRY_RUN", "1")
	t.Setenv(diag.DirEnv, "")
	t.Setenv(diag.RegionEnv, "CN")
	diag.ResetRegion()
	defer diag.ResetRegion()

	e := newDaemonEnv(t, openPortal(), false)
	home := t.TempDir()
	q := diag.Queue{Dir: filepath.Join(home, "logs", diag.ReportsDir)}
	queuedReport(t, q, "00000000000000a1")
	d := e.daemonIn(home)

	var st DiagStatus
	get := func() DiagStatus {
		t.Helper()
		var s DiagStatus
		if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/diagnostics", nil), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	st = get()
	if !st.Upload || st.Chosen || !st.DefaultUpload || st.Region.Country != "CN" || st.LogDir != filepath.Join(home, "logs") || st.LogFile != filepath.Join(home, "logs", diag.ClientLog) {
		t.Fatalf("status: %+v", st)
	}
	time.Sleep(400 * time.Millisecond)
	if st = get(); st.Pending != 1 || st.LastError != "" {
		t.Fatalf("logged out, the report must wait: %+v", st)
	}

	// What waits is what the page shows.
	var shown struct {
		Reports []diag.Report `json:"reports"`
	}
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/diagnostics/reports", nil), &shown); err != nil || len(shown.Reports) != 1 || shown.Reports[0].Fingerprint != "00000000000000a1" {
		t.Fatalf("reports: %+v %v", shown, err)
	}

	d.signIn(e, "crashy@example.com")
	deadline := time.Now().Add(10 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM client_reports r JOIN users u ON u.id = r.user_id WHERE u.username = 'crashy@example.com'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n != 1 {
		t.Fatal("the report did not reach the portal after the sign-in")
	}
	if st = get(); st.Pending != 0 || st.LastSent == 0 {
		t.Fatalf("after sending: %+v", st)
	}

	// Off: saved, the queue emptied, nothing more sent.
	d.want(400, "bad_request", "PUT", "/api/v1/diagnostics", map[string]any{})
	if err := json.Unmarshal(d.want(200, "", "PUT", "/api/v1/diagnostics", map[string]bool{"upload": false}), &st); err != nil || st.Upload || !st.Chosen {
		t.Fatalf("off: %+v %v", st, err)
	}
	queuedReport(t, q, "00000000000000a2")
	time.Sleep(400 * time.Millisecond)
	if st = get(); st.Pending != 0 {
		t.Fatalf("off, a report stayed queued: %+v", st)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM client_reports`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sent while off: %d %v", n, err)
	}
	cfg, err := LoadConfig(home)
	if err != nil || cfg.Diagnostics.Upload == nil || *cfg.Diagnostics.Upload {
		t.Fatalf("config: %+v %v", cfg.Diagnostics, err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ConfigFile))
	if !strings.Contains(string(data), `"diagnostics": {`) {
		t.Fatalf("config.json: %s", data)
	}
	d.want(200, "", "PUT", "/api/v1/diagnostics", map[string]bool{"upload": true})

	// The log directory opens (here: only checked).
	var opened map[string]string
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/logs/open", nil), &opened); err != nil || opened["dir"] != filepath.Join(home, "logs") {
		t.Fatalf("open: %v %v", opened, err)
	}
}

// In the European Economic Area reports start off; the user can still turn
// them on.
func TestDiagnosticsDefaultInEEA(t *testing.T) {
	t.Setenv(diag.RegionEnv, "DE")
	diag.ResetRegion()
	defer diag.ResetRegion()
	home := t.TempDir()
	cfg, _ := LoadConfig(home)
	if cfg.UploadReports() {
		t.Fatal("on by default in Germany")
	}
	if err := SetReportUpload(home, true, diag.Queue{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = LoadConfig(home); !cfg.UploadReports() {
		t.Fatal("the user's choice did not count")
	}
	t.Setenv(diag.RegionEnv, "JP")
	diag.ResetRegion()
	if cfg, _ = LoadConfig(t.TempDir()); !cfg.UploadReports() {
		t.Fatal("off by default in Japan")
	}
}

// StartDiag writes the log to the directory, records crashes there, and moves
// the logs older releases left in the state directory.
func TestStartDiag(t *testing.T) {
	t.Setenv(diag.DirEnv, "")
	home := t.TempDir()
	_ = os.WriteFile(filepath.Join(home, "daemon.log"), []byte("old\n"), 0o600)
	logs := filepath.Join(t.TempDir(), "elsewhere")
	cfg := DefaultConfig()
	cfg.Log.Dir = logs
	prev := slog.Default()
	dg, err := StartDiag(home, diag.ProcDaemon, "", cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { // the other tests log to stderr again, and report nothing
		dg.Close()
		slog.SetDefault(prev)
		diag.Install(nil, "")
	}()
	if dg.Dir != logs {
		t.Fatalf("dir %s", dg.Dir)
	}
	if data, err := os.ReadFile(filepath.Join(logs, "daemon.log.old")); err != nil || string(data) != "old\n" {
		t.Fatalf("old log: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, "daemon.log")); !os.IsNotExist(err) {
		t.Fatal("old log left in the state directory")
	}
	ents, _ := os.ReadDir(filepath.Join(logs, diag.CrashDir))
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "crash-daemon-") {
		t.Fatalf("crash file: %v", ents)
	}
	if lines := diag.Tail(filepath.Join(logs, diag.ClientLog), 5); len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "msg=starting") {
		t.Fatalf("log: %q", lines)
	}
	// -log-dir wins over the configuration, $SHARETOKEN_LOG_DIR over both.
	if d := LogDir(home, "/flag", cfg); d != "/flag" {
		t.Fatal(d)
	}
	t.Setenv(diag.DirEnv, "/env")
	if d := LogDir(home, "/flag", cfg); d != "/env" {
		t.Fatal(d)
	}
}
