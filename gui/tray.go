package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"
)

// trayPollEvery is how often the menu's status line is brought up to date. The
// daemon is on this machine and the call only reads its state.
const trayPollEvery = 3 * time.Second

// trayMenu is what the menu shows. The status line is not clickable; Update,
// when not empty, is an item that opens the window, where the download is.
type trayMenu struct {
	Status, Update, Open, Quit string
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
	if !t.notify(windowTitle(), strings.ReplaceAll(text.fill(body), "{version}", u.Latest)) {
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
}

// trayUpdate is the session's word on a newer release (internal/client/update.go).
type trayUpdate struct {
	Available bool   `json:"available"`
	Required  bool   `json:"required"`
	Latest    string `json:"latest"`
	Key       string `json:"key"`
	Skipped   bool   `json:"skipped"`
	Notified  bool   `json:"notified"`
}

// shown: the user is to hear about it.
func (u trayUpdate) shown() bool { return u.Available && (u.Required || !u.Skipped) }

func fetchTrayState(ctx context.Context, addr string) trayState {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/v1/session", nil)
	if err != nil {
		return trayState{Down: true}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return trayState{Down: true}
	}
	defer resp.Body.Close()
	var st trayState
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&st) != nil || st.State == "" {
		return trayState{Down: true}
	}
	return st
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
// or a test on its own directory, are separate.
func singleInstanceID(home string) string {
	h := fnv.New64a()
	h.Write([]byte(home))
	return fmt.Sprintf("app.sharetoken.desktop.%s.%016x", Env, h.Sum64())
}
