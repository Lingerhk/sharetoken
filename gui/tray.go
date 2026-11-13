package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// trayPollEvery is how often the menu's status line is brought up to date. The
// daemon is on this machine and the call only reads its state.
const trayPollEvery = 3 * time.Second

// trayInfoLines is how many lines the menu has under its status line.
const trayInfoLines = 3

// trayBalanceEvery is how often the balance in the menu is fetched: the daemon
// asks the portal for it.
const trayBalanceEvery = time.Minute

// trayMenu is what the menu shows. The status line is not clickable; Update,
// when not empty, is an item that opens the window, where the download is.
// Info is up to trayInfoLines lines under the status line (the relay in use
// and its latency, sharing, the balance), not clickable either. Tooltip is
// what hovering over the icon shows.
type trayMenu struct {
	Status, Update, Open, Quit string
	Info                       []string
	Tooltip                    string
}

// trayController keeps the native menu in line with the daemon and the language
// the page is shown in. The native side is the set function, so this part is the
// same on every platform and runs in plain tests.
type trayController struct {
	start func() error  // blocks until the daemon's address is settled
	addr  func() string // valid once start has returned
	set   func(trayMenu)
	every time.Duration
	wake  chan struct{}
	// notify, when set, shows a system notification and reports whether it did.
	notify func(title, body string) bool

	mu       sync.Mutex
	lang     string
	reported bool   // the page has said its language
	notified string // the update this run has tried to notify about

	// Only run touches these.
	balance   *int64 // micro-credits; nil while not known
	balanceAt time.Time
	balanceOf string // the user it belongs to
}

func newTrayController(start func() error, addr func() string, set func(trayMenu)) *trayController {
	return &trayController{start: start, addr: addr, set: set, every: trayPollEvery, wake: make(chan struct{}, 1), lang: "en"}
}

// setLang is called with the language of each page the window loads.
func (t *trayController) setLang(lang string) {
	if _, ok := trayTexts[lang]; !ok {
		return
	}
	t.mu.Lock()
	changed := t.lang != lang
	t.lang, t.reported = lang, true
	t.mu.Unlock()
	if changed {
		select {
		case t.wake <- struct{}{}:
		default:
		}
	}
}

func (t *trayController) currentLang() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lang
}

func (t *trayController) langReported() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reported
}

// run updates the menu until ctx ends.
func (t *trayController) run(ctx context.Context) {
	t.show(trayState{State: stateStarting})
	if t.start() != nil {
		t.show(trayState{Down: true})
	}
	tick := time.NewTicker(t.every)
	defer tick.Stop()
	for {
		st := fetchTrayState(ctx, t.addr())
		st.Balance = t.balanceFor(ctx, st)
		t.show(st)
		t.notifyUpdate(ctx, st.Update)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-t.wake:
		}
	}
}

func (t *trayController) show(st trayState) {
	t.mu.Lock()
	text := trayTexts[t.lang]
	t.mu.Unlock()
	t.set(renderTray(text, st))
}

// balanceFor is the logged-in user's balance, asked for once in a while. A
// failed request keeps the last one: the portal may be away for a moment.
func (t *trayController) balanceFor(ctx context.Context, st trayState) *int64 {
	if st.Down || !st.LoggedIn {
		t.balance, t.balanceAt = nil, time.Time{}
		return nil
	}
	if st.User != t.balanceOf {
		t.balance, t.balanceAt, t.balanceOf = nil, time.Time{}, st.User
	}
	if time.Since(t.balanceAt) < trayBalanceEvery {
		return t.balance
	}
	t.balanceAt = time.Now()
	var me struct {
		Balance struct {
			Available *int64 `json:"available_micros"`
		} `json:"balance"`
	}
	if trayGet(ctx, t.addr(), "/api/v1/account", &me) && me.Balance.Available != nil {
		t.balance = me.Balance.Available
	}
	return t.balance
}

// notifyUpdate shows one notification per offered build: the daemon records it,
// so a restart does not repeat it. A required upgrade is notified even if the
// user skipped that build.
func (t *trayController) notifyUpdate(ctx context.Context, u trayUpdate) {
	if t.notify == nil || !u.shown() || u.Notified || u.Key == "" {
		return
	}
	t.mu.Lock()
	tried := t.notified == u.Key
	t.notified = u.Key
	text := trayTexts[t.lang]
	t.mu.Unlock()
	if tried {
		return
	}
	body := text.NotifyAvailable
	if u.Required {
		body = text.NotifyRequired
	}
	body = strings.ReplaceAll(text.fill(body), "{version}", u.Latest)
	if s := u.summary(t.currentLang()); s != "" && !u.Required {
		body += "\n" + s
	}
	if !t.notify(windowTitle(), body) {
		return
	}
	data, _ := json.Marshal(map[string]string{"key": u.Key})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+t.addr()+"/api/v1/update/notified", bytes.NewReader(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

// The daemon's session states (internal/client/daemon.go).
const (
	stateLoggedOut = "logged_out"
	stateExpired   = "expired"
	stateStarting  = "starting"
	stateRetrying  = "retrying"
	stateRunning   = "running"
)

// trayState is the part of the daemon's session the menu shows. Down: the daemon
// does not answer.
type trayState struct {
	Down     bool       `json:"-"`
	State    string     `json:"state"`
	LoggedIn bool       `json:"logged_in"`
	User     string     `json:"user"`
	Update   trayUpdate `json:"update"`

	// From the agent's status, once it runs.
	Net     *trayNet `json:"-"`
	Balance *int64   `json:"-"` // micro-credits
}

// trayNet is the part of the agent's status the menu shows: the relay that
// carries this client's traffic, how fast it answers, and what is shared.
type trayNet struct {
	Relay   string  // its name; empty when none is in use
	RTT     float64 // milliseconds; 0 when not measured
	Sharing bool
	Models  int // on sale
}

// trayStatus is the agent's status (internal/client/api.go), as far as needed.
type trayStatus struct {
	Coordinator struct {
		Via  string `json:"via"`
		Link *struct {
			Samples int     `json:"samples"`
			Avg     float64 `json:"avg_ms"`
		} `json:"link"`
	} `json:"coordinator"`
	Relays []struct {
		Addr    string  `json:"addr"`
		Name    string  `json:"name"`
		RTT     float64 `json:"rtt_ms"`
		Current bool    `json:"current"`
	} `json:"relays"`
	Share struct {
		Running bool              `json:"running"`
		Serving []json.RawMessage `json:"serving"`
	} `json:"share"`
}

func (s trayStatus) net() *trayNet {
	n := &trayNet{Sharing: s.Share.Running, Models: len(s.Share.Serving)}
	for _, r := range s.Relays {
		if !r.Current {
			continue
		}
		n.Relay, n.RTT = r.Name, r.RTT
		if n.Relay == "" { // a relay the portal does not name: the end of its address
			n.Relay = r.Addr
			if len(n.Relay) > 8 {
				n.Relay = "…" + n.Relay[len(n.Relay)-6:]
			}
		}
	}
	// The live pings of the relay in use say more than the last survey of all.
	if l := s.Coordinator.Link; l != nil && l.Samples > 0 && l.Avg > 0 && n.Relay != "" {
		n.RTT = l.Avg
	}
	return n
}

// trayUpdate is the session's word on a newer release (internal/client/update.go).
type trayUpdate struct {
	Available bool   `json:"available"`
	Required  bool   `json:"required"`
	Latest    string `json:"latest"`
	Key       string `json:"key"`
	Skipped   bool   `json:"skipped"`
	Notified  bool   `json:"notified"`
	Notes     map[string]struct {
		Summary string `json:"summary"`
	} `json:"notes"`
}

// summary is the release's one line about itself, in lang or else in English.
func (u trayUpdate) summary(lang string) string {
	if n, ok := u.Notes[lang]; ok && n.Summary != "" {
		return n.Summary
	}
	return u.Notes["en"].Summary
}

// shown: the user is to hear about it.
func (u trayUpdate) shown() bool { return u.Available && (u.Required || !u.Skipped) }

// trayGet reads one answer of the daemon's local API into out.
func trayGet(ctx context.Context, addr, path string, out any) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(out) == nil
}

func fetchTrayState(ctx context.Context, addr string) trayState {
	var st trayState
	if !trayGet(ctx, addr, "/api/v1/session", &st) || st.State == "" {
		return trayState{Down: true}
	}
	if st.State == stateRunning {
		var status trayStatus
		if trayGet(ctx, addr, "/api/v1/status", &status) {
			st.Net = status.net()
		}
	}
	return st
}

// trayCredit writes micro-credits as credits, to two places.
func trayCredit(micros int64) string {
	s := strconv.FormatFloat(float64(micros)/1e6, 'f', 2, 64)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

func renderTray(text trayText, st trayState) trayMenu {
	m := trayMenu{Open: text.fill(text.Open), Quit: text.fill(text.Quit)}
	switch {
	case st.Down:
		m.Status = text.ServiceDown
	case st.State == stateLoggedOut:
		m.Status = text.SignedOut
	case st.State == stateExpired:
		m.Status = text.Expired
	case st.State == stateRetrying:
		m.Status = text.Retrying
	case st.State == stateRunning:
		m.Status = text.Connected
	default:
		m.Status = text.Connecting
	}
	if st.LoggedIn && st.User != "" {
		m.Status += " · " + st.User
	}
	m.Tooltip = windowTitle()
	if n := st.Net; n != nil && !st.Down && st.State == stateRunning {
		if n.Relay != "" {
			line := strings.ReplaceAll(text.Relay, "{name}", n.Relay)
			if ms := int(n.RTT + 0.5); ms > 0 {
				line = strings.ReplaceAll(strings.ReplaceAll(text.RelayRTT, "{name}", n.Relay), "{ms}", strconv.Itoa(ms))
			}
			m.Info = append(m.Info, line)
			m.Tooltip += " · " + line
		}
		if n.Sharing {
			m.Info = append(m.Info, strings.ReplaceAll(text.Sharing, "{n}", strconv.Itoa(n.Models)))
		}
	}
	if st.Balance != nil && !st.Down && st.LoggedIn {
		m.Info = append(m.Info, strings.ReplaceAll(text.Balance, "{n}", trayCredit(*st.Balance)))
	}
	if !st.Down && st.Update.shown() {
		item := text.UpdateAvailable
		if st.Update.Required {
			item = text.UpdateRequired
		}
		m.Update = strings.ReplaceAll(item, "{version}", st.Update.Latest)
	}
	return m
}

// singleInstanceID names this app on one data directory: a second start on the
// same directory only brings the running window back, because a second client on
// it would put the same device key on the network twice. A dev and a prod app,
// or a test on its own directory, are separate. On Linux it is a D-Bus name,
// whose elements may not start with a digit: the hash gets a letter in front.
func singleInstanceID(home string) string {
	h := fnv.New64a()
	h.Write([]byte(home))
	return fmt.Sprintf("app.sharetoken.desktop.%s.h%016x", Env, h.Sum64())
}
