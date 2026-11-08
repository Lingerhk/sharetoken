package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/cli"
)

// builtinTelemetryKey is sharetoken's PostHog project API key. It is embedded on
// purpose: a project key can only write events, and ships in every app anyway.
const builtinTelemetryKey = "phc_sjtwNCcgiAkXb3c8sMPh3pSiCDUdoAT8RNQSf6GnT2au"

// Build-time settings, with -ldflags "-X sharetoken/client.Name=value".
var (
	TelemetryKey  = builtinTelemetryKey        // an empty value builds an app that sends nothing
	TelemetryHost = "https://us.i.posthog.com" // PostHog Cloud (the project is in the US region), or a self-hosted instance
	Version       = "dev"                      // set by scripts/build-gui.sh; says which code, not which environment (see Env)
)

// TelemetryFile holds the opt-out and the anonymous install ID. It is separate
// from config.json, which a running agent rewrites from its own copy.
const TelemetryFile = "telemetry.json"

// Events. Each is sent with telemetryProps and nothing else.
const (
	eventDailyActive     = "sharetoken_daily_active"
	eventHourlyActive    = "sharetoken_hourly_active"
	eventSignedIn        = "sharetoken_signed_in"
	eventSignInFailed    = "sharetoken_sign_in_failed"
	eventSignedOut       = "sharetoken_signed_out"
	eventNetworkJoined   = "sharetoken_network_joined"
	eventNetworkJoinFail = "sharetoken_network_join_failed"
)

// telemetryProps is the complete contract of what leaves this machine. It is a
// concrete struct of fixed values rather than a map, so that no caller can add
// an email address, a URL, an error message or anything from a prompt. Never
// add a field holding free text.
type telemetryProps struct {
	SchemaVersion int    `json:"schema_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	AppVersion    string `json:"app_version"`
	Method        string `json:"method,omitempty"`   // login, register
	Reason        string `json:"reason,omitempty"`   // user, expired, or an allow-listed error code
	State         string `json:"state,omitempty"`    // a session state
	Sharing       *bool  `json:"sharing,omitempty"`  // sharing a model right now
	Duration      string `json:"duration,omitempty"` // a duration bucket
	InsertID      string `json:"$insert_id"`         // lets PostHog drop duplicates
	// No location lookup and no person profile for this ID.
	GeoIPDisable        bool `json:"$geoip_disable"`
	ProcessPersonProfil bool `json:"$process_person_profile"`
}

type telemetryEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties telemetryProps `json:"properties"`
	Timestamp  string         `json:"timestamp"`
}

// telemetryCodes are the sign-in errors reported by name; others are "other",
// because these codes come from the portal.
var telemetryCodes = map[string]bool{
	"bad_portal": true, "insecure_portal": true, "portal_unreachable": true, "not_a_portal": true,
	"pin_required": true, "pin_mismatch": true, "bad_request": true, "exists": true,
	"registration_closed": true, "bad_credentials": true, "throttled": true, "bad_code": true, "email_required": true, "code_cooldown": true,
	"mfa_setup_required": true, "already_logged_in": true, "bind_failed": true,
}

// TelemetryState is what the app shows about usage statistics.
type TelemetryState struct {
	Available bool `json:"available"` // this build has a key, and the environment does not forbid it
	Enabled   bool `json:"enabled"`
}

type telemetryFile struct {
	Disabled  bool   `json:"disabled,omitempty"`
	InstallID string `json:"install_id,omitempty"`
	LastDay   string `json:"last_day,omitempty"`
	LastHour  string `json:"last_hour,omitempty"`
}

// Telemetry sends anonymous usage events, best effort: it never blocks the
// caller, and an event it cannot deliver is dropped.
type Telemetry struct {
	key, endpoint string
	path          string
	http          *http.Client
	log           *slog.Logger
	now           func() time.Time
	activeEvery   time.Duration
	status        func() (state string, sharing bool)

	mu    sync.Mutex
	st    telemetryFile
	queue []telemetryEvent

	wake, stop, done chan struct{}
	ctx              context.Context // ends every send; Close cancels it when its time is up
	cancel           context.CancelFunc
}

const (
	telemetryQueueMax  = 256
	telemetryBatchMax  = 50
	telemetrySendLimit = 5 * time.Second
	// telemetryCloseLimit is how long closing may wait for events to go out.
	telemetryCloseLimit = 2 * time.Second
)

// telemetryForbidden reports whether the environment turns statistics off:
// $SHARETOKEN_TELEMETRY=0, or the common $DO_NOT_TRACK=1.
func telemetryForbidden() bool {
	return os.Getenv("SHARETOKEN_TELEMETRY") == "0" || os.Getenv("DO_NOT_TRACK") == "1"
}

// telemetryEndpoint is where events go, or "" if host is not acceptable: events
// are anonymous but still leave the machine, so only over TLS or to loopback.
func telemetryEndpoint(host string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(host), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && cli.IsLoopback(u.Hostname()))) {
		return ""
	}
	return u.String() + "/batch/"
}

// newTelemetry returns nil (on which every method does nothing) when this build
// or environment has no statistics at all.
func newTelemetry(home, key, host string, status func() (string, bool)) *Telemetry {
	endpoint := telemetryEndpoint(host)
	// Only prod builds report to the real project.
	if key == builtinTelemetryKey && Env != EnvProd {
		return nil
	}
	if strings.TrimSpace(key) == "" || endpoint == "" || telemetryForbidden() {
		return nil
	}
	t := &Telemetry{key: strings.TrimSpace(key), endpoint: endpoint, path: filepath.Join(home, TelemetryFile),
		http: &http.Client{Timeout: telemetrySendLimit}, log: slog.Default().With("component", "telemetry"),
		now: time.Now, activeEvery: 10 * time.Minute, status: status,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	data, err := os.ReadFile(t.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil || json.Unmarshal(data, &t.st) != nil:
		// It may hold an opt-out we cannot read: stay off.
		t.log.Warn("cannot read the telemetry settings; staying off")
		t.st = telemetryFile{Disabled: true}
	}
	return t
}

func (t *Telemetry) start() {
	if t != nil {
		go t.run()
	}
}

// State reports the setting; a nil Telemetry is simply unavailable.
func (t *Telemetry) State() TelemetryState {
	if t == nil {
		return TelemetryState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return TelemetryState{Available: true, Enabled: !t.st.Disabled}
}

// SetEnabled turns statistics on or off. Turning them off drops what is queued
// and forgets the install ID, so turning them on again starts a new one.
func (t *Telemetry) SetEnabled(on bool) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if on == !t.st.Disabled {
		return nil
	}
	t.st = telemetryFile{Disabled: !on}
	t.queue = nil
	return t.saveLocked()
}

func (t *Telemetry) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(t.st)
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// capture queues an event. The caller gives only the fields that vary.
func (t *Telemetry) capture(event string, p telemetryProps) {
	if t == nil {
		return
	}
	t.mu.Lock()
	ok := t.captureLocked(event, p)
	t.mu.Unlock()
	if ok {
		select {
		case t.wake <- struct{}{}:
		default:
		}
	}
}

func (t *Telemetry) captureLocked(event string, p telemetryProps) bool {
	if t.st.Disabled || len(t.queue) >= telemetryQueueMax {
		return false
	}
	if t.st.InstallID == "" {
		// Random, and not derived from the account or the device key, so it
		// cannot be linked to either.
		t.st.InstallID = "install:" + randomHex(16)
		if err := t.saveLocked(); err != nil {
			t.log.Debug("saving the install ID", "err", err)
		}
	}
	p.SchemaVersion, p.OS, p.Arch, p.AppVersion = 1, runtime.GOOS, runtime.GOARCH, Version
	p.InsertID, p.GeoIPDisable, p.ProcessPersonProfil = randomHex(16), true, false
	t.queue = append(t.queue, telemetryEvent{Event: event, DistinctID: t.st.InstallID, Properties: p, Timestamp: t.now().UTC().Format(time.RFC3339)})
	return true
}

func (t *Telemetry) signedIn(method string) { t.capture(eventSignedIn, telemetryProps{Method: method}) }

func (t *Telemetry) signInFailed(method, code string) {
	if code == "mfa_required" {
		return // a step of a login, not a failure
	}
	if !telemetryCodes[code] {
		code = "other"
	}
	t.capture(eventSignInFailed, telemetryProps{Method: method, Reason: code})
}

func (t *Telemetry) signedOut(reason string) {
	t.capture(eventSignedOut, telemetryProps{Reason: reason})
}

func (t *Telemetry) networkJoined(took time.Duration) {
	t.capture(eventNetworkJoined, telemetryProps{Duration: durationBucket(took)})
}

func (t *Telemetry) networkJoinFailed() { t.capture(eventNetworkJoinFail, telemetryProps{}) }

func durationBucket(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "under_5s"
	case d < 30*time.Second:
		return "5s_30s"
	case d < 2*time.Minute:
		return "30s_2m"
	}
	return "over_2m"
}

// markActive sends the daily and hourly active events, at most once per UTC day
// and hour for this install, across restarts.
func (t *Telemetry) markActive() {
	state, sharing := t.status()
	now := t.now().UTC()
	day, hour := now.Format("2006-01-02"), now.Format("2006-01-02T15")
	t.mu.Lock()
	sent := false
	if t.st.LastDay != day && t.captureLocked(eventDailyActive, telemetryProps{State: state, Sharing: &sharing}) {
		t.st.LastDay, sent = day, true
	}
	if t.st.LastHour != hour && t.captureLocked(eventHourlyActive, telemetryProps{State: state, Sharing: &sharing}) {
		t.st.LastHour, sent = hour, true
	}
	if sent {
		if err := t.saveLocked(); err != nil {
			t.log.Debug("saving the telemetry state", "err", err)
		}
	}
	t.mu.Unlock()
}

func (t *Telemetry) run() {
	defer close(t.done)
	active := time.NewTicker(t.activeEvery)
	defer active.Stop()
	t.markActive()
	t.flush(t.ctx)
	for {
		select {
		case <-t.stop:
			return
		case <-active.C:
			t.markActive()
		case <-t.wake:
		}
		t.flush(t.ctx)
	}
}

// flush sends what is queued, dropping it if that fails: no retries, and no
// event is worth keeping or slowing anything down for.
func (t *Telemetry) flush(ctx context.Context) {
	for {
		t.mu.Lock()
		n := min(len(t.queue), telemetryBatchMax)
		batch := t.queue[:n:n]
		t.queue = t.queue[n:]
		t.mu.Unlock()
		if n == 0 {
			return
		}
		body, err := json.Marshal(struct {
			APIKey string           `json:"api_key"`
			Batch  []telemetryEvent `json:"batch"`
		}{t.key, batch})
		if err != nil {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := t.http.Do(req)
		if err != nil {
			t.log.Debug("events dropped", "n", n, "err", err)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
	}
}

// Close sends what is left, waiting at most telemetryCloseLimit in all: that
// covers a send already under way, which may be stuck on a hanging collector.
func (t *Telemetry) Close() {
	if t == nil {
		return
	}
	limit := time.AfterFunc(telemetryCloseLimit, t.cancel)
	defer limit.Stop()
	defer t.cancel()
	close(t.stop)
	<-t.done
	t.flush(t.ctx)
}
