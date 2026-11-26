package diag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a program that crashes: $DIAG_CRASH says how,
// $DIAG_DIR where its log is.
func TestMain(m *testing.M) {
	if how := os.Getenv("DIAG_CRASH"); how != "" {
		crashNow(how, os.Getenv("DIAG_DIR"))
		return
	}
	os.Exit(m.Run())
}

//go:noinline
func explode(n int) int {
	var s []int
	return s[n] // index out of range
}

func crashNow(how, dir string) {
	f, err := Setup(LogOptions{Dir: dir, File: ClientLog})
	if err != nil {
		panic(err)
	}
	defer f.Close()
	r := &Reporter{Process: ProcDaemon, Version: "1.0.5", Env: "prod", Queue: Queue{Dir: filepath.Join(dir, ReportsDir)}}
	Install(r, f.Path())
	if _, err := StartCrashCapture(dir, ProcDaemon); err != nil {
		panic(err)
	}
	slog.Info("joined the network", "component", "daemon", "user", "erin@example.com")
	slog.Warn("cannot join the network yet", "component", "daemon", "err", "dial tcp 203.0.113.5:443: refused")
	switch how {
	case "panic":
		done := make(chan struct{})
		go func() { defer close(done); explode(3) }()
		<-done
	case "wrap":
		done := make(chan struct{})
		Go("worker", func() { defer close(done); explode(4) })
		<-done
	case "fatal":
		m := map[int]int{}
		for i := range 8 {
			go func() {
				for j := 0; ; j++ {
					m[j%100+i] = j
				}
			}()
		}
		time.Sleep(10 * time.Second)
	}
	time.Sleep(time.Second)
}

func runCrash(t *testing.T, how, dir string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "DIAG_CRASH="+how, "DIAG_DIR="+dir)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("%s: the program did not crash: %v\n%s", how, err, out)
	}
}

func collector(dir string, on bool) Collector {
	return Collector{Dir: dir, Reporter: &Reporter{Process: ProcDaemon, Version: "1.0.5", Env: "prod", Queue: Queue{Dir: filepath.Join(dir, ReportsDir)},
		Enabled: func() bool { return on }}}
}

func TestCrashBecomesAReport(t *testing.T) {
	dir := t.TempDir()
	runCrash(t, "panic", dir)
	ents, _ := os.ReadDir(filepath.Join(dir, CrashDir))
	if len(ents) != 1 {
		t.Fatalf("crash files: %v", ents)
	}
	c := collector(dir, true)
	if n := c.Collect(); n != 1 {
		t.Fatalf("collected %d", n)
	}
	reps, err := c.Reporter.Queue.List()
	if err != nil || len(reps) != 1 {
		t.Fatalf("queue: %v %v", reps, err)
	}
	r := reps[0]
	if r.Kind != KindCrash || r.Process != ProcDaemon || !strings.HasPrefix(r.Message, "panic: runtime error: index out of range [3]") {
		t.Fatalf("report: %+v", r)
	}
	if fn, _ := TopFrame(r.Stack); fn != "sharetoken/internal/diag.explode" {
		t.Fatalf("top frame %q in\n%s", fn, r.Stack)
	}
	all := strings.Join(r.Context, "\n")
	if !strings.Contains(all, "joined the network") || strings.Contains(all, "erin@example.com") || strings.Contains(all, "203.0.113.5") {
		t.Fatalf("context:\n%s", all)
	}
	if r.Fingerprint != Fingerprint(r.Message, r.Stack) || r.Version != "1.0.5" || r.OS == "" {
		t.Fatalf("report: %+v", r)
	}
	// The file moved to crash/old, and is not collected again.
	if old, _ := os.ReadDir(filepath.Join(dir, CrashDir, "old")); len(old) != 1 {
		t.Fatalf("old: %v", old)
	}
	if n := c.Collect(); n != 0 {
		t.Fatalf("collected again: %d", n)
	}

	// A second crash at the same place is the same problem.
	runCrash(t, "panic", dir)
	c.Collect()
	reps, _ = c.Reporter.Queue.List()
	if len(reps) != 1 {
		t.Fatalf("the same problem queued twice: %d", len(reps))
	}
}

func TestCrashWithReportsOff(t *testing.T) {
	dir := t.TempDir()
	runCrash(t, "panic", dir)
	c := collector(dir, false)
	if n := c.Collect(); n != 1 {
		t.Fatalf("collected %d", n)
	}
	if reps, _ := c.Reporter.Queue.List(); len(reps) != 0 {
		t.Fatalf("queued with reports off: %v", reps)
	}
	// The record stays for the user.
	if old, _ := os.ReadDir(filepath.Join(dir, CrashDir, "old")); len(old) != 1 {
		t.Fatalf("old: %v", old)
	}
}

func TestWrappedGoroutineReportsOnce(t *testing.T) {
	dir := t.TempDir()
	runCrash(t, "wrap", dir)
	q := Queue{Dir: filepath.Join(dir, ReportsDir)}
	reps, _ := q.List()
	if len(reps) != 1 || !strings.Contains(reps[0].Message, "(in worker)") || reps[0].Kind != KindCrash {
		t.Fatalf("written before dying: %+v", reps)
	}
	c := collector(dir, true)
	if n := c.Collect(); n != 1 {
		t.Fatalf("collected %d", n)
	}
	if reps, _ = q.List(); len(reps) != 1 {
		t.Fatalf("the crash file added a second report of the same panic: %d", len(reps))
	}
}

func TestFatalError(t *testing.T) {
	dir := t.TempDir()
	runCrash(t, "fatal", dir)
	c := collector(dir, true)
	c.Collect()
	reps, _ := c.Reporter.Queue.List()
	if len(reps) != 1 || !strings.HasPrefix(reps[0].Message, "fatal error: ") || !strings.Contains(reps[0].Stack, "crashNow") {
		t.Fatalf("fatal: %+v", reps)
	}
}

func TestEmptyCrashFiles(t *testing.T) {
	dir := t.TempDir()
	cd := filepath.Join(dir, CrashDir)
	_ = os.MkdirAll(cd, 0o700)
	dead := filepath.Join(cd, "crash-gui-20261124T000000Z-999999.txt") // no such process
	alive := filepath.Join(cd, fmt.Sprintf("crash-gui-20261124T000000Z-%d.txt", os.Getppid()))
	for _, p := range []string{dead, alive} {
		_ = os.WriteFile(p, nil, 0o600)
	}
	collector(dir, true).Collect()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("the empty file of a finished process stayed")
	}
	if _, err := os.Stat(alive); err != nil {
		t.Fatal("the empty file of a running process was removed")
	}
}

func TestCrashCaptureCleanExit(t *testing.T) {
	dir := t.TempDir()
	c, err := StartCrashCapture(dir, ProcCLI)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path()); err != nil {
		t.Fatal(err)
	}
	c.Finish()
	if _, err := os.Stat(c.Path()); !os.IsNotExist(err) {
		t.Fatal("an empty crash file stayed after a clean exit")
	}
}

func TestHTTPHandlerPanic(t *testing.T) {
	dir := t.TempDir()
	r := &Reporter{Process: ProcDaemon, Version: "1.0.5", Queue: Queue{Dir: dir}}
	Install(r, "")
	defer Install(nil, "")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { explode(5) }))
	srv.Config.ErrorLog = HTTPErrorLog("test")
	srv.Start()
	defer srv.Close()
	for range 3 {
		if resp, err := http.Get(srv.URL); err == nil {
			resp.Body.Close()
		}
	}
	reps, _ := r.Queue.List()
	if len(reps) != 1 || reps[0].Kind != KindPanic || !strings.Contains(reps[0].Message, "index out of range [5]") {
		t.Fatalf("reports: %+v", reps)
	}
	if fn, _ := TopFrame(reps[0].Stack); fn != "sharetoken/internal/diag.explode" {
		t.Fatalf("top frame %q:\n%s", fn, reps[0].Stack)
	}
}

func TestFingerprint(t *testing.T) {
	var stack []byte
	func() {
		defer func() { recover(); stack = debug.Stack() }()
		explode(1)
	}()
	fn, at := TopFrame(string(stack))
	if fn != "sharetoken/internal/diag.explode" || !strings.HasPrefix(at, "diag_test.go:") {
		t.Fatalf("top frame %q %q", fn, at)
	}
	// Without a traceback: the message, numbers aside.
	if Fingerprint("panic: x 12", "") != Fingerprint("panic: x 345 [recovered, repanicked]", "") || Fingerprint("panic: x", "") == Fingerprint("panic: y", "") {
		t.Fatal("message fingerprints")
	}
	if len(Fingerprint("a", "")) != 16 {
		t.Fatal("length")
	}
}

func TestQueue(t *testing.T) {
	q := Queue{Dir: t.TempDir()}
	base := Report{Kind: KindCrash, Process: ProcGUI, Version: "1.0.5", OS: "linux", Arch: "amd64", Message: "panic: x", OccurredAt: 1_000_000}
	add := func(fp string, at int64) bool {
		r := base
		r.ID, r.Fingerprint, r.OccurredAt = NewID(), fp, at
		ok, err := q.Add(r)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !add("0000000000000001", 1_000_000) || add("0000000000000001", 1_000_100) || !add("0000000000000001", 1_000_000+SameWindow) || !add("0000000000000002", 1_000_000) {
		t.Fatal("dedup")
	}
	reps, _ := q.List()
	if len(reps) != 3 {
		t.Fatalf("listed %d", len(reps))
	}
	if err := q.Remove(reps[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := q.Remove("../../etc"); err == nil {
		t.Fatal("a path was accepted as an id")
	}
	for i := range MaxQueued + 5 {
		add(fmt.Sprintf("%016x", 100+i), int64(i+1))
	}
	if reps, _ = q.List(); len(reps) != MaxQueued {
		t.Fatalf("not trimmed: %d", len(reps))
	}
	// A damaged file is dropped.
	_ = os.WriteFile(filepath.Join(q.Dir, "000000000000.json"), []byte("{"), 0o600)
	if reps2, _ := q.List(); len(reps2) != MaxQueued {
		t.Fatalf("damaged file listed: %d", len(reps2))
	}
	if err := q.Clear(); err != nil {
		t.Fatal(err)
	}
	if reps, _ = q.List(); len(reps) != 0 {
		t.Fatal("not cleared")
	}
	bad := base
	bad.ID, bad.Fingerprint, bad.Kind = NewID(), "0000000000000003", "other"
	if _, err := q.Add(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad kind: %v", err)
	}
}

func TestCheckCuts(t *testing.T) {
	r := Report{ID: NewID(), Kind: KindPanic, Process: ProcCLI, Fingerprint: "00000000000000aa", OccurredAt: 1, Message: strings.Repeat("m", 5000),
		Stack: strings.Repeat("s", MaxStack+100), Version: "1.0.5"}
	for range MaxContext + 20 {
		r.Context = append(r.Context, strings.Repeat("c", MaxContextLine+10))
	}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	if len(r.Message) > MaxMessage+3 || len(r.Stack) > MaxStack+3 || len(r.Context) != MaxContext || len(r.Context[0]) > MaxContextLine+3 {
		t.Fatalf("not cut: %d %d %d %d", len(r.Message), len(r.Stack), len(r.Context), len(r.Context[0]))
	}
}

func TestUploader(t *testing.T) {
	q := Queue{Dir: t.TempDir()}
	for i := range 3 {
		_, err := q.Add(Report{ID: NewID(), Kind: KindCrash, Process: ProcDaemon, Fingerprint: fmt.Sprintf("%016x", i), Version: "1", OS: "x", Arch: "y", Message: "panic", OccurredAt: int64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	var sent []string
	results := []error{nil, Rejected{errors.New("too big")}, errors.New("portal away")}
	on := true
	u := &Uploader{Queue: q, Enabled: func() bool { return on }, Send: func(_ context.Context, r Report) error {
		err := results[len(sent)]
		sent = append(sent, r.Fingerprint)
		return err
	}}
	if n := u.Flush(context.Background()); n != 1 {
		t.Fatalf("sent %d", n)
	}
	reps, _ := q.List()
	if len(reps) != 1 || reps[0].Fingerprint != "0000000000000002" {
		t.Fatalf("left: %+v", reps)
	}
	if last, msg := u.Status(); last == 0 || msg != "portal away" {
		t.Fatalf("status %d %q", last, msg)
	}
	// Logged out: waits quietly.
	results = append(results, ErrNotLoggedIn)
	u.Flush(context.Background())
	if _, msg := u.Status(); msg != "" {
		t.Fatalf("logged out is not an error: %q", msg)
	}
	// Off: nothing is sent, and the queue is emptied.
	on = false
	before := len(sent)
	u.Flush(context.Background())
	if reps, _ = q.List(); len(reps) != 0 || len(sent) != before {
		t.Fatalf("off: %d left, %d sent", len(reps), len(sent)-before)
	}
}

func TestUploaderRunAndKick(t *testing.T) {
	q := Queue{Dir: t.TempDir()}
	got := make(chan string, 4)
	u := &Uploader{Queue: q, First: time.Hour, Every: time.Hour, Send: func(_ context.Context, r Report) error { got <- r.ID; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)
	id := NewID()
	_, _ = q.Add(Report{ID: id, Kind: KindCrash, Process: ProcDaemon, Fingerprint: "00000000000000ff", Version: "1", OS: "x", Arch: "y", Message: "panic", OccurredAt: 1})
	u.Kick()
	select {
	case g := <-got:
		if g != id {
			t.Fatal(g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a kick did not send")
	}
}

func TestRotatingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "client.log")
	f, err := OpenRotating(p, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 1023) + "\n"
	for i := range 3500 { // 3.5 MiB: three rotations
		if _, err := fmt.Fprintf(f, "%05d%s", i, line[5:]); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	for _, name := range []string{"client.log", "client.log.1", "client.log.2"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Size() > 1<<20 {
			t.Fatalf("%s: %v %v", name, st, err)
		}
	}
	if _, err := os.Stat(p + ".3"); !os.IsNotExist(err) {
		t.Fatal("more files kept than asked")
	}
	tail := Tail(p, 5)
	if len(tail) != 5 || !strings.HasPrefix(tail[4], "03499") {
		t.Fatalf("tail: %d %q", len(tail), tail[len(tail)-1][:8])
	}
	// Fewer lines in the file than asked: the rotated one fills in.
	_ = os.WriteFile(p, []byte("only\n"), 0o600)
	if tail = Tail(p, 3); len(tail) != 3 || tail[2] != "only" {
		t.Fatalf("tail across rotation: %q", tail)
	}
}

func TestDir(t *testing.T) {
	abs := func(p string) string { a, _ := filepath.Abs(p); return a } // a drive letter on Windows
	t.Setenv(DirEnv, "")
	home := t.TempDir()
	if d := Dir(home, "", ""); d != filepath.Join(home, "logs") {
		t.Fatal(d)
	}
	if d := Dir(home, "", "/var/tmp/stlogs"); d != abs("/var/tmp/stlogs") {
		t.Fatal(d)
	}
	if d := Dir(home, "/flag", "/var/tmp/stlogs"); d != abs("/flag") {
		t.Fatal(d)
	}
	t.Setenv(DirEnv, "/env")
	if d := Dir(home, "/flag", "/cfg"); d != abs("/env") {
		t.Fatal(d)
	}
	t.Setenv(DirEnv, "")
	if h, _ := os.UserHomeDir(); Dir(home, "", "~/x") != filepath.Join(h, "x") {
		t.Fatal("~ not expanded")
	}
}

func TestRegion(t *testing.T) {
	for in, want := range map[string]string{"de_DE.UTF-8": "DE", "en-GB": "GB", "zh_Hans_CN": "CN", "sr_RS@latin": "RS", "C.UTF-8": "", "en": "", "": "", "fr-FR": "FR"} {
		if got := countryOfLocale(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if z := zoneOfLink("/var/db/timezone/zoneinfo/Europe/Berlin"); z != "Europe/Berlin" || !eeaZones[z] {
		t.Fatal(z)
	}
	defer ResetRegion()
	for c, eu := range map[string]bool{"DE": true, "no": true, "CN": false, "US": false, "GB": false, "CH": false} {
		t.Setenv(RegionEnv, c)
		ResetRegion()
		if SystemRegion().EEA != eu || DefaultUpload() == eu {
			t.Errorf("%s: %+v", c, SystemRegion())
		}
	}
}

// Started by the desktop app, a daemon's standard error goes to
// daemon.stderr: the fatal error's own line is found there.
func TestFatalErrorWithStderr(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.Create(filepath.Join(dir, StderrLog))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "DIAG_CRASH=fatal", "DIAG_DIR="+dir)
	cmd.Stdout, cmd.Stderr = f, f
	_ = cmd.Run()
	f.Close()
	c := collector(dir, true)
	c.Collect()
	reps, _ := c.Reporter.Queue.List()
	if len(reps) != 1 || !strings.HasPrefix(reps[0].Message, "fatal error: concurrent map") {
		t.Fatalf("fatal: %+v", reps)
	}
}

// A reader holding the log open (sharetoken logs -f; on Windows that blocks
// a rename) does not stop the rotation or the writing.
func TestRotateWhileOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "client.log")
	f, err := OpenRotating(p, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.Write([]byte("first\n"))
	rd, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	chunk := []byte(strings.Repeat("y", 1023) + "\n")
	for range 1100 {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = f.Write([]byte("last\n"))
	if st, err := os.Stat(p); err != nil || st.Size() > 1<<20 {
		t.Fatalf("not rotated: %v %v", st, err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("no rotated file")
	}
	if tail := Tail(p, 1); len(tail) != 1 || tail[0] != "last" {
		t.Fatalf("tail %q", tail)
	}
}

// The desktop shell writes crash files the daemon reports: process gui, with
// the lines of gui.log written before the crash.
func TestShellCrashFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, CrashDir), 0o700)
	crashAt := time.Now().Add(-time.Minute)
	_ = os.WriteFile(filepath.Join(dir, GUILog), []byte(
		"time="+crashAt.Add(-time.Second).Format(time.RFC3339Nano)+" level=INFO msg=\"sharetoken-gui: the tray came up\"\n"+
			"time="+crashAt.Add(time.Minute).Format(time.RFC3339Nano)+" level=INFO msg=\"after the crash\"\n"), 0o600)
	p := filepath.Join(dir, CrashDir, "crash-gui-20261124T000000Z-999998.txt")
	_ = os.WriteFile(p, []byte("panic: tray: nil menu\n\ngoroutine 1 [running]:\nmain.(*tray).update(0x0)\n\tsharetoken/gui/tray.go:88 +0x1c\nmain.main()\n\tsharetoken/gui/main.go:120 +0x40\n"), 0o600)
	_ = os.Chtimes(p, crashAt, crashAt)
	c := collector(dir, true)
	c.Collect()
	reps, _ := c.Reporter.Queue.List()
	if len(reps) != 1 {
		t.Fatalf("reports: %+v", reps)
	}
	r := reps[0]
	if r.Process != ProcGUI || r.Message != "panic: tray: nil menu" || r.OccurredAt != crashAt.Unix() {
		t.Fatalf("report: %+v", r)
	}
	if fn, at := TopFrame(r.Stack); fn != "main.(*tray).update" || at != "tray.go:88" {
		t.Fatalf("top frame %q %q", fn, at)
	}
	if len(r.Context) != 1 || !strings.Contains(r.Context[0], "the tray came up") {
		t.Fatalf("context: %q", r.Context)
	}
}
