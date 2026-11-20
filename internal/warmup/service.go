// Package warmup starts Claude and ChatGPT (Codex) usage windows when the
// user wants them to run. A subscription's 5-hour window, and its weekly
// one, start at the account's first request after they reset, not at the
// reset: one tiny request at 6:00 has the window that runs out at 11:00
// instead of 14:00, and the working day fits three windows instead of two.
//
// Two kinds of account are warmed up: the agents installed on this machine
// (Claude Code, Codex), through their own CLI, and a seller's subscription
// logins (providers.json), through the provider's own upstream, the way
// its jobs go. Neither involves the sharetoken network. Each has times of
// day, and may start its windows as soon as they reset. What the windows
// are is read without spending them: from the answers of the provider's
// requests, from Claude Code's /usage, from ChatGPT's usage endpoint.
// Modeled on magpie's daily warm-up.
package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/provider"
)

const (
	// lookEvery is how often the times and windows are looked at; the
	// wall clock, read each time, goes on while the machine sleeps.
	lookEvery = time.Minute
	// firstLook is how long after start the first look waits.
	firstLook = 30 * time.Second
	// maxFails is how many failed warm-ups one time of day gets, failRetry
	// apart.
	maxFails  = 3
	failRetry = 5 * time.Minute
	// readEvery is how often a local agent's windows are read while it
	// starts them on their reset (a read runs the agent's CLI).
	readEvery = 30 * time.Minute
	// readFresh is how old a read may be to decide a warm-up by.
	readFresh = 2 * time.Minute
	// recentJob: a provider that answered a request this recently has its
	// 5-hour window running, whatever was read before.
	recentJob = 10 * time.Minute
	// resetRetry is how long a window a warm-up left not started (as far
	// as is read) waits for the next, doubling up to resetRetryMax.
	resetRetry    = 15 * time.Minute
	resetRetryMax = 4 * time.Hour
	// binaryCheck is how long where an agent's program is is kept.
	binaryCheck = 10 * time.Minute
)

// Target kinds.
const (
	KindLocal    = "local"
	KindProvider = "provider"
)

// Results.
const (
	ResultWarmed  = "warmed"
	ResultRunning = "running" // the window was already running at the time of day
	ResultUsedUp  = "used_up" // a window is spent: a request would go past the plan
	ResultFailed  = "failed"
)

// Skip reasons: why nothing is sent automatically.
const (
	SkipPaused       = "paused"
	SkipDisabled     = "disabled"      // the provider is off
	SkipNotInstalled = "not_installed" // the agent's program isn't found
	SkipOff          = "off"           // no warm-up set
)

// TargetState is what the warm-up did and saw of one account.
type TargetState struct {
	Last    time.Time `json:"last,omitzero"`     // the last warm-up that was answered
	LastTry time.Time `json:"last_try,omitzero"` // the last one sent or decided
	Result  string    `json:"result,omitempty"`
	Error   string    `json:"error,omitempty"`
	// LastModel is the model the last warm-up asked.
	LastModel string `json:"last_model,omitempty"`
	// Slot is the last time of day handled, "2006-01-02 15:04".
	Slot     string    `json:"slot,omitempty"`
	FailSlot string    `json:"fail_slot,omitempty"`
	Fails    int       `json:"fails,omitempty"`
	NextTry  time.Time `json:"next_try,omitzero"`
	// ResetIdle counts warm-ups on reset that left the window not started
	// as far as was read; ResetRetry is when the next may go.
	ResetIdle  int       `json:"reset_idle,omitempty"`
	ResetRetry time.Time `json:"reset_retry,omitzero"`
	// Usage is the account's windows as last read.
	Usage     *provider.UsageLimits `json:"usage,omitempty"`
	ReadAt    time.Time             `json:"read_at,omitzero"`
	ReadError string                `json:"read_error,omitempty"`
}

// TargetStatus is one account the warm-up knows, as the API tells it.
type TargetStatus struct {
	ID      string   `json:"id"`   // local:claude, local:codex, provider:<provider id>
	Kind    string   `json:"kind"` // local or provider
	Agent   string   `json:"agent"`
	Name    string   `json:"name"` // the provider's name
	Account string   `json:"account,omitempty"`
	Enabled bool     `json:"enabled"` // a warm-up is set
	Times   []string `json:"times"`
	OnReset string   `json:"on_reset,omitempty"`
	Model   string   `json:"model,omitempty"`
	Skip    string   `json:"skip,omitempty"`
	// SkipError says more of Skip (where the program was looked for).
	SkipError string     `json:"skip_error,omitempty"`
	Binary    string     `json:"binary,omitempty"`
	Version   string     `json:"version,omitempty"`
	Next      *time.Time `json:"next,omitempty"` // the next time of day
	TargetState
}

// Status is the warm-up as the API tells it.
type Status struct {
	Paused   bool           `json:"paused"`
	Zone     string         `json:"zone"` // the zone times of day are in
	Settings Settings       `json:"settings"`
	Targets  []TargetStatus `json:"targets"`
}

type target struct {
	TargetStatus
	providerID string
	coolUntil  time.Time // the provider rests after a 429
}

type binary struct {
	path, err, version, set string
	at                      time.Time
}

// Service runs the warm-ups of one state directory.
type Service struct {
	home string
	log  *slog.Logger

	// for tests
	now       func() time.Time
	run       runner
	readCodex func(ctx context.Context, now time.Time) (provider.UsageLimits, error)
	warm      func(ctx context.Context, home, name string) (provider.WarmResult, error)
	usageOf   func(id string) (provider.UsageLimits, bool)
	lastOK    func(id string) time.Time
	find      func(name, set string) (string, error)

	runMu sync.Mutex // one look or warm-up at a time

	mu     sync.Mutex
	states map[string]*TargetState // nil until read
	bins   map[string]binary
	kick   chan struct{}
}

// New prepares the warm-up of home; Run runs it.
func New(home string) *Service {
	return &Service{home: home, log: slog.Default().With("component", "warmup"), now: time.Now, run: runCommand,
		readCodex: readCodex, warm: provider.Warm, usageOf: provider.UsageOf, lastOK: provider.LastSuccess, find: findBinary,
		bins: map[string]binary{}, kick: make(chan struct{}, 1)}
}

// Kick has the warm-up look again soon, after a change of settings.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run looks at the warm-ups every minute until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTimer(firstLook)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
		s.Look(ctx)
		t.Reset(lookEvery)
	}
}

// Look does what is due now.
func (s *Service) Look(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	set, err := LoadSettings(s.home)
	if err != nil {
		s.log.Warn("warm-up settings", "err", err)
		return
	}
	ts := s.targets(ctx, set)
	s.prune(ts)
	for i := range ts {
		if ctx.Err() != nil {
			return
		}
		s.consider(ctx, &ts[i])
	}
}

// targets are the accounts there are to warm up, with their settings.
func (s *Service) targets(ctx context.Context, set Settings) []target {
	now := s.now()
	var out []target
	for _, a := range []struct {
		agent string
		l     Local
	}{{AgentClaude, set.Claude}, {AgentCodex, set.Codex}} {
		t := target{TargetStatus: TargetStatus{ID: KindLocal + ":" + a.agent, Kind: KindLocal, Agent: a.agent,
			Enabled: a.l.Enabled && (len(a.l.Times) > 0 || a.l.OnReset != ""), Times: a.l.Times, OnReset: a.l.OnReset, Model: a.l.Model}}
		b := s.binary(ctx, a.agent, a.l.Binary, now)
		t.Binary, t.Version = b.path, b.version
		switch {
		case b.path == "":
			t.Skip, t.SkipError = SkipNotInstalled, b.err
		case !t.Enabled:
			t.Skip = SkipOff
		}
		out = append(out, t)
	}
	if f, err := provider.Load(s.home); err == nil {
		for _, e := range f.Providers {
			if !e.WarmCapable() {
				continue
			}
			v := e.View()
			agent := AgentClaude
			if e.Type == provider.KindOpenAI {
				agent = AgentCodex
			}
			t := target{TargetStatus: TargetStatus{ID: KindProvider + ":" + e.ID, Kind: KindProvider, Agent: agent, Name: e.Name, Account: v.Account, Times: []string{}},
				providerID: e.ID, coolUntil: v.Runtime.RetryAt}
			if w := e.Warmup; w != nil {
				t.Times, t.OnReset, t.Model = validTimes(w.Times), w.OnReset, w.Model
				t.Enabled = len(t.Times) > 0 || t.OnReset != ""
			}
			switch {
			case !t.Enabled:
				t.Skip = SkipOff
			case !e.Enabled:
				t.Skip = SkipDisabled
			}
			out = append(out, t)
		}
	} else {
		s.log.Warn("providers file", "err", err)
	}
	for i := range out {
		if out[i].Times == nil {
			out[i].Times = []string{}
		}
		if set.Paused && out[i].Skip == "" {
			out[i].Skip = SkipPaused
		}
		if next, ok := nextDayStart(out[i].Times, now); ok && out[i].Enabled {
			out[i].Next = &next
		}
	}
	return out
}

func validTimes(ts []string) []string {
	n, err := provider.NormalizeTimes(ts)
	if err != nil {
		return []string{}
	}
	return n
}

// binary is where agent's program is, looked for again every binaryCheck.
func (s *Service) binary(ctx context.Context, agent, set string, now time.Time) binary {
	s.mu.Lock()
	b, ok := s.bins[agent]
	s.mu.Unlock()
	if ok && b.set == set && now.Sub(b.at) < binaryCheck {
		return b
	}
	b = binary{set: set, at: now}
	if p, err := s.find(agent, set); err != nil {
		b.err = err.Error()
	} else {
		b.path = p
		b.version = version(ctx, s.run, p)
	}
	s.mu.Lock()
	s.bins[agent] = b
	s.mu.Unlock()
	return b
}

// consider does what is due for t now: a time of day come, a window reset.
func (s *Service) consider(ctx context.Context, t *target) {
	now := s.now()
	st := s.state(t.ID)
	if t.Kind == KindProvider {
		s.providerUsage(t, &st)
	}
	if t.Skip != "" {
		s.put(t.ID, st)
		return
	}
	slot, slotOK := dueSlot(t.Times, now)
	key := ""
	daily := false
	if slotOK {
		key = slotKey(slot)
		daily = st.Slot != key && !(st.FailSlot == key && st.Fails >= maxFails) && !now.Before(st.NextTry)
	}
	onReset := t.OnReset != "" && !now.Before(st.ResetRetry)
	if t.Kind == KindLocal && (daily && now.Sub(st.ReadAt) > readFresh || t.OnReset != "" && s.readDue(st, now)) {
		s.read(ctx, t, &st, now)
	}
	recent := t.Kind == KindProvider && now.Sub(s.lastOK(t.providerID)) < recentJob
	exhausted := usedUp(st.Usage, now)
	resetDue := false
	if onReset && !recent && !exhausted {
		var running bool
		resetDue, running = resetWanted(t.Times, t.OnReset, st, now)
		if running {
			st.ResetIdle, st.ResetRetry = 0, time.Time{}
		}
	}
	if daily {
		running, resets := false, time.Time{}
		if w, ok := window(st.Usage, true); ok && !idle(w, now) {
			running, resets = true, asOf(w, now).ResetsAt
		}
		switch {
		case exhausted:
			st.Slot, st.Result, st.Error, st.LastTry = key, ResultUsedUp, "", now
			daily = false
		case recent:
			st.Slot, st.Result, st.Error, st.LastTry = key, ResultRunning, "", now
			daily = false
		case running && !resets.IsZero() && resets.Before(slot.Add(dailySlack)):
			// it runs out within the time's slack: started then, as wanted
			daily = false
		case running:
			st.Slot, st.Result, st.Error, st.LastTry = key, ResultRunning, "", now
			daily = false
		}
	}
	if (daily || resetDue) && !now.Before(t.coolUntil) {
		s.put(t.ID, st)
		s.send(ctx, t, &st, now, daily, key, resetDue)
	}
	s.put(t.ID, st)
}

// readDue says whether a local agent starting its windows on reset reads
// them now: every readEvery, and as soon as one was to reset.
func (s *Service) readDue(st TargetState, now time.Time) bool {
	if now.Sub(st.ReadAt) >= readEvery {
		return true
	}
	if st.Usage == nil {
		return false
	}
	for _, w := range st.Usage.Windows {
		if !w.ResetsAt.IsZero() && w.ResetsAt.After(st.ReadAt) && !now.Before(w.ResetsAt) {
			return true
		}
	}
	return false
}

// resetWanted says whether one of the windows warmed on reset (the weekly
// one; with "all" the 5-hour one too) isn't running and wants starting
// now, and whether they all run. A 5-hour window that, started now, would
// still run at the next time of day waits for that time; so does the
// weekly one while the 5-hour one isn't running, as the one request starts
// both. A window never read is started once a whole window after the last
// warm-up.
func resetWanted(times []string, which string, st TargetState, now time.Time) (due, running bool) {
	if !known(st.Usage) {
		span := 7 * 24 * time.Hour
		if which == provider.WarmOnResetAll {
			span = 5 * time.Hour
		}
		return st.Last.IsZero() || now.Sub(st.Last) >= span, false
	}
	running = true
	kinds := []bool{false}
	if which == provider.WarmOnResetAll {
		kinds = []bool{true, false}
	}
	for _, wantShort := range kinds {
		w, ok := window(st.Usage, wantShort)
		if !ok || !idle(w, now) {
			continue
		}
		running = false
		if wantShort && heldForDay(times, w.Span(), now) {
			continue
		}
		if !wantShort {
			if f, ok := window(st.Usage, true); ok && idle(f, now) && heldForDay(times, f.Span(), now) {
				continue
			}
		}
		due = true
	}
	return due, running
}

// send sends t's warm-up and keeps what came of it.
func (s *Service) send(ctx context.Context, t *target, st *TargetState, now time.Time, daily bool, key string, onReset bool) {
	model, err := s.warmOne(ctx, t)
	st.LastTry = now
	if err == nil {
		st.Last, st.Result, st.Error, st.LastModel = now, ResultWarmed, "", model
		if daily {
			st.Slot, st.FailSlot, st.Fails, st.NextTry = key, "", 0, time.Time{}
		}
		if onReset {
			st.ResetRetry = now.Add(min(resetRetry<<min(st.ResetIdle, 8), resetRetryMax))
			st.ResetIdle++
		}
		s.log.Info("warmed up", "target", t.ID, "name", t.Name, "model", model)
		s.after(ctx, t, st)
		return
	}
	st.Result, st.Error = ResultFailed, clip(err.Error())
	if daily {
		if st.FailSlot != key {
			st.FailSlot, st.Fails = key, 0
		}
		st.Fails++
		st.NextTry = now.Add(failRetry)
	}
	if onReset {
		st.ResetRetry = now.Add(resetRetry)
	}
	s.log.Warn("warm-up failed", "target", t.ID, "name", t.Name, "err", err)
}

// warmOne sends t one tiny request; model is the one asked, when known.
func (s *Service) warmOne(ctx context.Context, t *target) (string, error) {
	if t.Kind == KindProvider {
		r, err := s.warm(ctx, s.home, t.Name)
		return r.Model, err
	}
	if t.Binary == "" {
		return "", errors.New(cmpOr(t.SkipError, t.Agent+" is not installed"))
	}
	model := t.Model
	if model == "" && t.Agent == AgentClaude {
		model = "haiku"
	}
	return model, sendLocal(ctx, s.run, t.Agent, t.Binary, t.Model)
}

// after reads what the warm-up made of the windows.
func (s *Service) after(ctx context.Context, t *target, st *TargetState) {
	if t.Kind == KindProvider {
		s.providerUsage(t, st)
		return
	}
	s.read(ctx, t, st, s.now())
}

// providerUsage takes what the provider's answers said of its windows,
// when newer than what was kept.
func (s *Service) providerUsage(t *target, st *TargetState) {
	if l, ok := s.usageOf(t.providerID); ok && (st.Usage == nil || l.At.After(st.Usage.At)) {
		st.Usage, st.ReadAt, st.ReadError = &l, l.At, ""
	}
}

// read reads a local agent's windows.
func (s *Service) read(ctx context.Context, t *target, st *TargetState, now time.Time) {
	var (
		l   provider.UsageLimits
		err error
	)
	switch {
	case t.Agent == AgentClaude && t.Binary != "":
		l, err = readClaude(ctx, s.run, t.Binary, now)
	case t.Agent == AgentCodex:
		l, err = s.readCodex(ctx, now)
	default:
		err = errUnknown
	}
	st.ReadAt = now
	if err != nil {
		st.ReadError = clip(err.Error())
		if errors.Is(err, errUnknown) {
			st.ReadError = ""
		}
		return
	}
	st.Usage, st.ReadError = &l, ""
}

// RunNow sends target id's warm-up at once, whatever its settings: id is
// local:claude, local:codex (or claude, codex), or provider:<id or name>.
func (s *Service) RunNow(ctx context.Context, id string) (TargetStatus, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	set, err := LoadSettings(s.home)
	if err != nil {
		return TargetStatus{}, err
	}
	ts := s.targets(ctx, set)
	i := slices.IndexFunc(ts, func(t target) bool { return t.matches(id) })
	if i < 0 {
		return TargetStatus{}, provider.ErrNotFound
	}
	t := &ts[i]
	now := s.now()
	st := s.state(t.ID)
	model, err := s.warmOne(ctx, t)
	st.LastTry = now
	if err != nil {
		st.Result, st.Error = ResultFailed, clip(err.Error())
	} else {
		st.Last, st.Result, st.Error, st.LastModel = now, ResultWarmed, "", model
		s.after(ctx, t, &st)
	}
	s.put(t.ID, st)
	t.TargetState = st
	if err != nil {
		return t.TargetStatus, err
	}
	return t.TargetStatus, nil
}

func (t target) matches(id string) bool {
	id = strings.TrimSpace(id)
	switch {
	case strings.EqualFold(id, t.ID):
		return true
	case t.Kind == KindLocal:
		return strings.EqualFold(id, t.Agent)
	default:
		name, ok := strings.CutPrefix(id, KindProvider+":")
		if !ok {
			name = id
		}
		return strings.EqualFold(name, t.Name) || name == t.providerID
	}
}

// Status is every account there is to warm up, its settings and what was
// done.
func (s *Service) Status(ctx context.Context) (Status, error) {
	set, err := LoadSettings(s.home)
	if err != nil {
		return Status{}, err
	}
	now := s.now()
	out := Status{Paused: set.Paused, Zone: zoneName(now), Settings: set, Targets: []TargetStatus{}}
	for _, t := range s.targets(ctx, set) {
		st := s.state(t.ID)
		if t.Kind == KindProvider {
			s.providerUsage(&t, &st)
		}
		t.TargetState = st
		out.Targets = append(out.Targets, t.TargetStatus)
	}
	return out, nil
}

// ---- state ----

func (s *Service) statePath() string { return filepath.Join(s.home, StateFile) }

func (s *Service) loadLocked() {
	if s.states != nil {
		return
	}
	s.states = map[string]*TargetState{}
	if data, err := os.ReadFile(s.statePath()); err == nil {
		if err := json.Unmarshal(data, &s.states); err != nil {
			s.log.Warn("warm-up state unreadable; starting afresh", "err", err)
			s.states = map[string]*TargetState{}
		}
	}
}

func (s *Service) state(id string) TargetState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if st := s.states[id]; st != nil {
		c := *st
		return c
	}
	return TargetState{}
}

// put keeps st as id's state and writes the file when it changed.
func (s *Service) put(id string, st TargetState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if old := s.states[id]; old != nil && sameState(*old, st) {
		return
	}
	c := st
	s.states[id] = &c
	s.saveLocked()
}

// prune forgets the providers no longer there.
func (s *Service) prune(ts []target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	n := len(s.states)
	maps.DeleteFunc(s.states, func(id string, _ *TargetState) bool {
		return strings.HasPrefix(id, KindProvider+":") && !slices.ContainsFunc(ts, func(t target) bool { return t.ID == id })
	})
	if len(s.states) != n {
		s.saveLocked()
	}
}

func (s *Service) saveLocked() {
	data, err := json.MarshalIndent(s.states, "", "  ")
	if err == nil {
		err = writeFile(s.statePath(), append(data, '\n'))
	}
	if err != nil {
		s.log.Warn("warm-up state not saved", "err", err)
	}
}

func sameState(a, b TargetState) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// zoneName names the machine's time zone as people know it: Asia/Shanghai
// (UTC+08:00), where the system says which.
func zoneName(now time.Time) string {
	offset := "UTC" + now.Format("-07:00")
	name := strings.TrimPrefix(os.Getenv("TZ"), ":")
	if name == "" {
		if p, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
			if _, after, ok := strings.Cut(filepath.ToSlash(p), "zoneinfo/"); ok {
				name = after
			}
		}
	}
	if name == "" || name == "Local" {
		return offset
	}
	return name + " (" + offset + ")"
}
