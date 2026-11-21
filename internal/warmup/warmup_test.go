package warmup

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/provider"
)

var sgt = time.FixedZone("SGT", 8*3600)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, sgt) }

func TestDayStart(t *testing.T) {
	ts := []string{"06:00", "15:05"}
	if got, _ := dayStart(ts, at(20, 5, 0)); !got.Equal(at(19, 15, 5)) {
		t.Fatalf("before the first time: %v", got)
	}
	if got, _ := dayStart(ts, at(20, 6, 0)); !got.Equal(at(20, 6, 0)) {
		t.Fatalf("at the time: %v", got)
	}
	if got, _ := nextDayStart(ts, at(20, 16, 0)); !got.Equal(at(21, 6, 0)) {
		t.Fatalf("next after the last: %v", got)
	}
	if _, ok := dueSlot(ts, at(20, 7, 0)); !ok {
		t.Fatal("an hour late is still due")
	}
	if _, ok := dueSlot(ts, at(20, 7, 1)); ok {
		t.Fatal("over an hour late was due")
	}
	if !heldForDay(ts, 5*time.Hour, at(20, 2, 0)) || heldForDay(ts, 5*time.Hour, at(20, 0, 59)) {
		t.Fatal("heldForDay")
	}
	if _, ok := dayStart(nil, at(20, 6, 0)); ok {
		t.Fatal("no times had a start")
	}
}

const usageText = `You are currently using your subscription to power your Claude Code usage

Current session: 34% used · resets Nov 20 at 11:50am (Asia/Singapore)
Current week (all models): 32% used · resets Nov 26 at 8am (Asia/Singapore)
Current week (Fable): 62% used · resets Nov 26 at 8am (Asia/Singapore)

Last 24h · 999 requests · 6 sessions
`

func TestParseClaudeUsage(t *testing.T) {
	now := at(20, 9, 0)
	l, err := parseClaudeUsage(usageText, now)
	if err != nil || len(l.Windows) != 2 {
		t.Fatalf("got %+v %v", l, err)
	}
	sg, _ := time.LoadLocation("Asia/Singapore")
	if w := l.Windows[0]; w.Name != "5h" || w.Used != 34 || !w.ResetsAt.Equal(time.Date(2026, 11, 20, 11, 50, 0, 0, sg)) {
		t.Fatalf("session %+v", w)
	}
	if w := l.Windows[1]; w.Name != "7d" || w.Used != 32 || !w.ResetsAt.Equal(time.Date(2026, 11, 26, 8, 0, 0, 0, sg)) {
		t.Fatalf("week %+v", w)
	}
	idleText := "Current session: 0% used\nCurrent week (all models): 5% used · resets Nov 26 at 8am (Asia/Singapore)\n"
	l, err = parseClaudeUsage(idleText, now)
	if err != nil || !idle(l.Windows[0], now) || idle(l.Windows[1], now) {
		t.Fatalf("idle session %+v %v", l, err)
	}
	if _, err := parseClaudeUsage("Not logged in · Please run /login", now); err == nil || errors.Is(err, errUnknown) {
		t.Fatalf("a sign-in error: %v", err)
	}
	if _, err := parseClaudeUsage("something else", now); !errors.Is(err, errUnknown) {
		t.Fatalf("nothing read: %v", err)
	}
	// Linux and Windows print the date with a comma and no "at"
	sh, _ := time.LoadLocation("Asia/Shanghai")
	for text, want := range map[string]time.Time{
		"Nov 20, 10:30pm (Asia/Shanghai)": time.Date(2026, 11, 20, 22, 30, 0, 0, sh),
		"Nov 26, 8am (Asia/Shanghai)":     time.Date(2026, 11, 26, 8, 0, 0, 0, sh),
		"Nov 26, 22:00 (Asia/Shanghai)":   time.Date(2026, 11, 26, 22, 0, 0, 0, sh),
	} {
		if got, ok := parseReset(text, now); !ok || !got.Equal(want) {
			t.Fatalf("%q: got %v %v", text, got, ok)
		}
	}
	// a reset after New Year's Eve
	if got, ok := parseReset("Jan 2 at 3pm", at(30, 9, 0).AddDate(0, 1, 0)); !ok || got.Year() != 2027 {
		t.Fatalf("year: %v %v", got, ok)
	}
}

func TestParseCodexUsage(t *testing.T) {
	now := at(20, 9, 0)
	body := `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,
	  "primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_after_seconds":18000,"reset_at":0},
	  "secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_after_seconds":0,"reset_at":1800500000}}}`
	l, err := parseCodexUsage([]byte(body), now)
	if err != nil || len(l.Windows) != 2 {
		t.Fatalf("got %+v %v", l, err)
	}
	if !idle(l.Windows[0], now) || l.Windows[1].Name != "7d" || idle(l.Windows[1], now) {
		t.Fatalf("windows %+v", l.Windows)
	}
	l, _ = parseCodexUsage([]byte(`{"rate_limit":{"allowed":false,"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_after_seconds":60}}}`), now)
	if !l.Rejected || !usedUp(&l, now) {
		t.Fatalf("held account %+v", l)
	}
}

// fixture is a Service on a temporary home with a fake clock, provider
// warm-up and local runner.
type fixture struct {
	t      *testing.T
	s      *Service
	now    time.Time
	sent   []string
	fail   error
	usage  map[string]provider.UsageLimits
	lastOK map[string]time.Time
	cmds   []command
	usageT string // what /usage prints
	pid    string
}

func newFixture(t *testing.T, w *provider.WarmOptions) *fixture {
	f := &fixture{t: t, now: at(20, 6, 1), usage: map[string]provider.UsageLimits{}, lastOK: map[string]time.Time{}}
	home := t.TempDir()
	if err := provider.Update(home, func(file *provider.File) error {
		return file.Put(&provider.Entry{Name: "max", Type: provider.KindAnthropic, Auth: provider.AuthOAuth, Enabled: true,
			OAuth: &provider.Token{AccessToken: "a", RefreshToken: "r"}, Warmup: w})
	}); err != nil {
		t.Fatal(err)
	}
	pf, _ := provider.Load(home)
	f.pid = pf.Get("max").ID
	f.s = f.service(home)
	return f
}

func (f *fixture) service(home string) *Service {
	s := New(home)
	s.now = func() time.Time { return f.now }
	s.warm = func(ctx context.Context, home, name string) (provider.WarmResult, error) {
		f.sent = append(f.sent, name)
		return provider.WarmResult{Model: "claude-haiku-4-5-20251001"}, f.fail
	}
	s.usageOf = func(id string) (provider.UsageLimits, bool) { l, ok := f.usage[id]; return l, ok }
	s.lastOK = func(id string) time.Time { return f.lastOK[id] }
	s.find = func(name, set string) (string, error) {
		if name == AgentClaude {
			return "/opt/claude", nil
		}
		return "", errors.New("codex is not installed")
	}
	s.run = func(ctx context.Context, c command) ([]byte, []byte, error) {
		f.cmds = append(f.cmds, c)
		switch {
		case slices.Contains(c.args, "--version"):
			return []byte("2.1.329 (Claude Code)\n"), nil, nil
		case slices.Contains(c.args, "/usage"):
			return []byte(f.usageT), nil, nil
		}
		f.sent = append(f.sent, "local:"+c.path)
		return []byte(`{"type":"result","is_error":false,"result":"Hi!"}`), nil, f.fail
	}
	s.readCodex = func(ctx context.Context, now time.Time) (provider.UsageLimits, error) {
		return provider.UsageLimits{}, errUnknown
	}
	return s
}

func (f *fixture) look(want int) {
	f.t.Helper()
	before := len(f.sent)
	f.s.Look(context.Background())
	if got := len(f.sent) - before; got != want {
		f.t.Fatalf("at %s: %d warm-ups sent, want %d (%v)", f.now.Format("01-02 15:04"), got, want, f.sent)
	}
}

func (f *fixture) state() TargetState { return f.s.state(KindProvider + ":" + f.pid) }

func running5h(resets time.Time, at time.Time) provider.UsageLimits {
	return provider.UsageLimits{At: at, Windows: []provider.UsageWindow{
		{Name: "5h", SpanSeconds: 5 * 3600, Used: 10, ResetsAt: resets},
		{Name: "7d", SpanSeconds: 7 * 86400, Used: 20, ResetsAt: resets.Add(72 * time.Hour)}}}
}

func TestProviderTimeOfDayOncePerDay(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}})
	f.look(1)
	if st := f.state(); st.Result != ResultWarmed || st.Slot != "2026-11-20 06:00" {
		t.Fatalf("state %+v", st)
	}
	f.now = at(20, 6, 30)
	f.look(0)
	// a restart does not send it again
	f.s = f.service(f.s.home)
	f.look(0)
	f.now = at(21, 6, 0)
	f.look(1)
	// a machine asleep through the time sends it on waking within the hour, not later
	f.now = at(22, 7, 30)
	f.look(0)
}

func TestProviderRunningWindowIsLeftAlone(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}})
	f.usage[f.pid] = running5h(at(20, 9, 0), at(20, 5, 0))
	f.look(0)
	if st := f.state(); st.Result != ResultRunning || st.Slot == "" {
		t.Fatalf("state %+v", st)
	}
}

func TestProviderWindowEndingSoonIsStartedAfterIt(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}})
	f.usage[f.pid] = running5h(at(20, 6, 30), at(20, 2, 0))
	f.look(0)
	if st := f.state(); st.Slot != "" {
		t.Fatalf("the time was spent on a window about to end: %+v", st)
	}
	f.now = at(20, 6, 31)
	f.look(1)
}

func TestProviderUsedUpOrRecentJob(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}})
	l := running5h(at(20, 9, 0), at(20, 5, 0))
	l.Windows[0].ResetsAt, l.Windows[0].Used = time.Time{}, 0
	l.Windows[1].Used = 100
	f.usage[f.pid] = l
	f.look(0)
	if st := f.state(); st.Result != ResultUsedUp {
		t.Fatalf("state %+v", st)
	}
	g := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}})
	g.lastOK[g.pid] = g.now.Add(-3 * time.Minute)
	g.look(0)
	if st := g.state(); st.Result != ResultRunning {
		t.Fatalf("state %+v", st)
	}
}

func TestProviderFailureRetries(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}})
	f.fail = errors.New("HTTP 500")
	f.look(1)
	f.now = f.now.Add(time.Minute)
	f.look(0)
	f.now = f.now.Add(failRetry)
	f.look(1)
	f.now = f.now.Add(failRetry)
	f.look(1)
	f.now = f.now.Add(failRetry)
	f.look(0) // three failures: given up for this time of day
	if st := f.state(); st.Result != ResultFailed || st.Error == "" || st.Fails != maxFails {
		t.Fatalf("state %+v", st)
	}
}

func TestProviderDisabledAndPaused(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}})
	if _, err := provider.SetEnabled(f.s.home, "max", false); err != nil {
		t.Fatal(err)
	}
	f.look(0)
	if _, err := provider.SetEnabled(f.s.home, "max", true); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(f.s.home, Settings{Paused: true}); err != nil {
		t.Fatal(err)
	}
	f.look(0)
	// warm up now works paused
	ts, err := f.s.RunNow(context.Background(), "provider:max")
	if err != nil || ts.Result != ResultWarmed || len(f.sent) != 1 {
		t.Fatalf("run now: %+v %v", ts, err)
	}
	if st := f.state(); st.Slot != "" {
		t.Fatal("warm up now used the time of day")
	}
}

func TestProviderOnReset(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{OnReset: provider.WarmOnResetAll})
	f.now = at(20, 13, 0)
	// never read: sent once to start (and learn) the windows
	f.look(1)
	f.usage[f.pid] = running5h(at(20, 18, 0), at(20, 13, 0))
	f.now = at(20, 14, 0)
	f.look(0)
	// the 5-hour window reset at 18:00 and nobody used it since
	f.now = at(20, 18, 2)
	f.look(1)
	f.now = at(20, 18, 3)
	f.look(0) // still read as before: waits resetRetry
	f.usage[f.pid] = running5h(at(20, 23, 2), at(20, 18, 2))
	f.now = at(20, 18, 20)
	f.look(0)
}

func TestProviderOnResetWaitsForTimeOfDay(t *testing.T) {
	f := newFixture(t, &provider.WarmOptions{Times: []string{"06:00"}, OnReset: provider.WarmOnResetAll})
	f.usage[f.pid] = running5h(at(20, 2, 0), at(19, 21, 0))
	f.now = at(20, 2, 5)
	f.look(0) // started now it would run past 6:00
	f.now = at(20, 6, 0)
	f.look(1)
	if st := f.state(); st.Slot != "2026-11-20 06:00" {
		t.Fatalf("state %+v", st)
	}
}

func TestLocalClaude(t *testing.T) {
	f := newFixture(t, nil)
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-pass")
	if err := SaveSettings(f.s.home, Settings{Claude: Local{Enabled: true, Times: []string{"6:00"}}}); err != nil {
		t.Fatal(err)
	}
	f.usageT = "Current session: 0% used\nCurrent week (all models): 5% used · resets Nov 26 at 8am (Asia/Singapore)\n"
	f.look(1)
	var send command
	for _, c := range f.cmds {
		if !slices.Contains(c.args, "--version") && !slices.Contains(c.args, "/usage") {
			send = c
		}
	}
	if send.stdin != "hi" || !slices.Contains(send.args, "--no-session-persistence") || !slices.Contains(send.args, "haiku") {
		t.Fatalf("command %+v", send)
	}
	i := slices.Index(send.args, "--tools")
	if i < 0 || send.args[i+1] != "" {
		t.Fatalf("tools not off: %v", send.args)
	}
	for _, kv := range send.env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Fatal("the API key reached the agent")
		}
	}
	st := f.s.state("local:claude")
	if st.Result != ResultWarmed || st.Usage == nil {
		t.Fatalf("state %+v", st)
	}
	// a running session is left alone the next day
	f.usageT = "Current session: 12% used · resets Nov 21 at 9am (Asia/Singapore)\n"
	f.now = at(21, 6, 0)
	f.look(0)
	status, err := f.s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var codex TargetStatus
	for _, ts := range status.Targets {
		if ts.ID == "local:codex" {
			codex = ts
		}
	}
	if codex.Skip != SkipNotInstalled || codex.SkipError == "" {
		t.Fatalf("codex %+v", codex)
	}
}

func TestClaudeResult(t *testing.T) {
	if r, ok := claudeResult([]byte("Warning: x\n{\"type\":\"result\",\"is_error\":true,\"result\":\"Not logged in\"}\n")); !ok || !r.IsError {
		t.Fatalf("got %+v %v", r, ok)
	}
	if !isShim("/Users/a/.cmuxterm/cmux-cli-shims/X/claude") || isShim("/opt/homebrew/bin/claude") {
		t.Fatal("isShim")
	}
}

func TestSettingsNormalize(t *testing.T) {
	s := Settings{Claude: Local{Times: []string{"9:00", "6:00"}, OnReset: "off"}}
	if err := s.Normalize(); err != nil || strings.Join(s.Claude.Times, ",") != "06:00,09:00" || s.Claude.OnReset != "" {
		t.Fatalf("got %+v %v", s, err)
	}
	if err := (&Settings{Codex: Local{Model: "gpt x"}}).Normalize(); err == nil {
		t.Fatal("a model with a space was taken")
	}
}
