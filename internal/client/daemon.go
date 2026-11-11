package client

import (
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/cli"
	"sharetoken/internal/p2p"
	"sharetoken/internal/portal"
	"sharetoken/internal/version"
)

// Daemon is the service behind the desktop app. Unlike a bare Agent, it runs
// with nobody logged in: it serves the app's pages and the API that signs a
// user up or in, and runs an Agent for as long as a user is logged in. The
// portal token stays inside the daemon and config.json; the API never returns
// it, and passwords only pass through on their way to the portal.
type Daemon struct {
	// Settings; set before Start.
	HTTP      string   // local API address (default: the configuration's); must be loopback
	Listen    []string // overrides the configuration's libp2p listen addresses
	NAT       *bool    // overrides the configuration's NAT setting
	AllowHTTP bool     // accept a plain-HTTP portal on another machine (test networks only)
	// RetryMin and RetryMax bound the wait between attempts to join the
	// network (zero: 2s and 1m).
	RetryMin, RetryMax time.Duration
	// OnAgent, if set, adjusts each agent before it starts (embedding, tests).
	OnAgent func(*Agent)
	// TelemetryKey and TelemetryHost replace the build's settings (tests).
	TelemetryKey, TelemetryHost string

	telemetry *Telemetry

	home string
	log  *slog.Logger
	srv  *http.Server
	addr string

	authMu sync.Mutex // serializes what changes who is logged in
	bg     sync.WaitGroup

	mu       sync.Mutex
	cfg      Config
	state    string
	lastErr  string
	portal   *PortalClient // nil: logged out
	peer     string
	agent    *Agent
	agentAPI http.Handler
	cancel   context.CancelFunc // ends the current run
	done     chan struct{}      // closed when the current start-up attempts end
	updates  *UpdateWatcher

	stopUpdates context.CancelFunc
}

// Session states.
const (
	StateLoggedOut = "logged_out"
	StateExpired   = "expired"  // logged out because the portal rejected the token
	StateStarting  = "starting" // logged in, joining the network
	StateRetrying  = "retrying" // logged in, the last attempt to join failed (see error)
	StateRunning   = "running"
)

// Session is what the app shows first: who is logged in, and the agent's state.
type Session struct {
	Env      string `json:"env"` // build type; the desktop shell only uses a daemon of its own
	State    string `json:"state"`
	LoggedIn bool   `json:"logged_in"`
	// Portal and User are the last ones used, also when logged out, to
	// pre-fill the login form.
	Portal string `json:"portal,omitempty"`
	User   string `json:"user,omitempty"`
	Device string `json:"device,omitempty"`
	Peer   string `json:"peer,omitempty"`
	API    string `json:"api"`             // OpenAI base URL
	Error  string `json:"error,omitempty"` // why the state is retrying or expired
	// Telemetry is the usage-statistics setting.
	Telemetry TelemetryState `json:"telemetry"`
	// Version and APIVersion say what this daemon is: the desktop shell only
	// uses a daemon of its own release.
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
	Update     Update `json:"update"`
}

// NewDaemon prepares a daemon with its state in home.
func NewDaemon(home string) *Daemon {
	d := &Daemon{home: home, log: slog.Default().With("component", "daemon"), state: StateLoggedOut}
	d.updates = NewUpdateWatcher(home, "desktop", d.portalAddr)
	return d
}

func (d *Daemon) loadConfig() (Config, error) {
	cfg, err := LoadConfig(d.home)
	if err != nil {
		return cfg, err
	}
	if d.Listen != nil {
		cfg.Listen = d.Listen
	}
	if d.NAT != nil {
		cfg.NAT = *d.NAT
	}
	return cfg, nil
}

// Start opens the local API and, if a login is saved, starts joining the
// network; it does not wait for that.
func (d *Daemon) Start() error {
	cfg, err := d.loadConfig()
	if err != nil {
		return err
	}
	if d.HTTP == "" {
		d.HTTP = cfg.HTTP
	}
	host, _, err := net.SplitHostPort(d.HTTP)
	if err != nil {
		return fmt.Errorf("local API address %q: %w", d.HTTP, err)
	}
	if !cli.IsLoopback(host) {
		return fmt.Errorf("local API address %q is not on loopback: the daemon handles your login without authenticating its callers", d.HTTP)
	}
	if err := os.MkdirAll(d.home, 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", d.HTTP)
	if err != nil {
		return fmt.Errorf("local API on %s: %w", d.HTTP, err)
	}
	d.addr = ln.Addr().String()
	d.srv = &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := d.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.log.Error("local API stopped", "err", err)
		}
	}()
	data, _ := json.Marshal(RunInfo{API: d.addr, PID: os.Getpid()})
	_ = os.WriteFile(filepath.Join(d.home, RunFile), data, 0o600)

	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
	d.telemetry = newTelemetry(d.home, cmp.Or(d.TelemetryKey, TelemetryKey), cmp.Or(d.TelemetryHost, TelemetryHost), d.telemetryStatus)
	d.telemetry.start()
	uctx, stopUpdates := context.WithCancel(context.Background())
	d.stopUpdates = stopUpdates
	d.bg.Go(func() { d.updates.Run(uctx) })
	if cfg.Portal != "" && cfg.Token != "" {
		if keys, err := p2p.LoadOrCreateKeys(filepath.Join(d.home, KeyFile)); err == nil {
			d.mu.Lock()
			d.peer = keys.ID()
			d.mu.Unlock()
		}
		d.startRun(cfg)
	}
	return nil
}

// Close stops the agent and the local API.
func (d *Daemon) Close() {
	d.authMu.Lock()
	d.stopRun()
	d.authMu.Unlock()
	if d.stopUpdates != nil {
		d.stopUpdates()
	}
	d.bg.Wait()
	d.telemetry.Close()
	if d.srv != nil {
		_ = os.Remove(filepath.Join(d.home, RunFile))
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = d.srv.Shutdown(sctx)
		cancel()
	}
}

// Addr is the local API's address (host:port).
func (d *Daemon) Addr() string { return d.addr }

// portalAddr is the portal the user signed in to, or last used.
func (d *Daemon) portalAddr() (base, pin string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.Portal, d.cfg.PortalPin
}

func (d *Daemon) telemetryStatus() (string, bool) {
	d.mu.Lock()
	state, a := d.state, d.agent
	d.mu.Unlock()
	return state, a != nil && a.Status().Share.Running
}

// Session reports the current state.
func (d *Daemon) Session() Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Session{Env: Env, State: d.state, LoggedIn: d.portal != nil, Portal: d.cfg.Portal, User: d.cfg.User, API: "http://" + d.addr + "/v1", Error: d.lastErr, Telemetry: d.telemetry.State(), Version: version.Version, APIVersion: version.API}
	s.Update = d.updates.Current()
	if !s.LoggedIn || s.Portal == "" {
		s.Portal = PortalAddr() // the sign-in page does not ask for it
	}
	if s.LoggedIn {
		s.Device, s.Peer = d.cfg.DeviceName, d.peer
	}
	return s
}

// startRun begins joining the network as cfg's user.
func (d *Daemon) startRun(cfg Config) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d.mu.Lock()
	d.cfg = cfg
	d.state, d.lastErr = StateStarting, ""
	d.portal = NewPortalClient(cfg.Portal, cfg.Token, cfg.PortalPin)
	d.cancel, d.done = cancel, done
	d.mu.Unlock()
	go d.joinNetwork(ctx, done, cfg)
}

// joinNetwork starts an agent, retrying until it runs: the portal, the relays
// or the coordinator being away must not keep the user from their account.
func (d *Daemon) joinNetwork(ctx context.Context, done chan struct{}, cfg Config) {
	defer close(done)
	wait := cmpDuration(d.RetryMin, 2*time.Second)
	limit := cmpDuration(d.RetryMax, time.Minute)
	began, reported := time.Now(), false
	for {
		a := NewAgent(d.home, cfg)
		a.ExternalAPI = d.addr
		a.Updates = d.updates
		a.OnLoginEnded = func() { go d.expire(cfg.Token, "the portal rejected the login (expired or revoked)") }
		if d.OnAgent != nil {
			d.OnAgent(a)
		}
		err := a.Start(ctx)
		if err == nil {
			d.mu.Lock()
			if ctx.Err() != nil {
				d.mu.Unlock()
				a.Close()
				return
			}
			d.agent, d.agentAPI = a, a.Handler()
			d.state, d.lastErr, d.peer = StateRunning, "", a.PeerID()
			d.mu.Unlock()
			d.log.Info("joined the network", "user", cfg.User)
			d.telemetry.networkJoined(time.Since(began))
			return
		}
		a.Close()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrLoginRejected) {
			// expire waits for this goroutine, so it cannot run on it.
			d.bg.Add(1)
			go func() {
				defer d.bg.Done()
				d.expire(cfg.Token, err.Error())
			}()
			return
		}
		d.log.Warn("cannot join the network yet", "err", err, "retry_in", wait)
		if !reported { // once per login: the retries would only repeat it
			reported = true
			d.telemetry.networkJoinFailed()
		}
		d.mu.Lock()
		d.state, d.lastErr = StateRetrying, err.Error()
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > limit {
			wait = limit
		}
	}
}

func cmpDuration(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// stopRun stops the agent or the attempts to start one. Caller holds authMu.
func (d *Daemon) stopRun() {
	d.mu.Lock()
	a, cancel, done := d.agent, d.cancel, d.done
	d.agent, d.agentAPI, d.cancel, d.done = nil, nil, nil, nil
	d.mu.Unlock()
	if a != nil {
		a.Close() // before the cancel: disputes in flight may still finish
	}
	if cancel != nil {
		cancel()
		<-done
	}
}

// expire ends a login the portal no longer accepts. token is the login that
// was rejected: a reply that arrives after the user logged in anew changes
// nothing.
func (d *Daemon) expire(token, reason string) {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	d.mu.Lock()
	current := d.cfg.Token
	d.mu.Unlock()
	if token == "" || token != current {
		return
	}
	d.log.Warn("login rejected by the portal", "reason", reason)
	d.stopRun()
	if err := d.clearLogin(StateExpired, reason); err != nil {
		d.log.Error("forgetting the login", "err", err)
	}
	d.telemetry.signedOut("expired")
}

// clearLogin stops using the token and removes it from disk, keeping every
// other setting. Caller holds authMu.
func (d *Daemon) clearLogin(state, reason string) error {
	cfg, err := LoadConfig(d.home) // an agent may have saved sharing settings
	if err == nil {
		cfg.Token = ""
		err = SaveConfig(d.home, cfg)
	} else {
		cfg = d.cfg
		cfg.Token = ""
	}
	d.mu.Lock()
	d.cfg = cfg
	d.state, d.lastErr, d.portal, d.peer = state, reason, nil, ""
	d.mu.Unlock()
	return err
}

// ---- trusting the portal ----

// NormalizePortalURL cleans up what a user typed as the portal's address; a
// missing scheme means https.
func NormalizePortalURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("portal address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("not a valid portal address: %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("the portal address must be https://…, not %q", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the portal address must not contain credentials or a query: %q", raw)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"), nil
}

// PortalProbe is what the login page shows about a portal before any password
// is sent to it.
type PortalProbe struct {
	Portal string `json:"portal"` // the normalized address
	HTTPS  bool   `json:"https"`
	// Trusted: the certificate chains to a system root for this host name.
	Trusted     bool       `json:"trusted"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	Subject     string     `json:"subject,omitempty"`
	NotAfter    *time.Time `json:"not_after,omitempty"`
	// Known: the certificate is the one pinned at an earlier login.
	Known bool `json:"known"`
	// PinChanged: a different certificate was pinned for this portal.
	PinChanged bool `json:"pin_changed"`
	// PinRequired: the user must confirm Fingerprint before logging in.
	PinRequired      bool   `json:"pin_required"`
	RegistrationOpen bool   `json:"registration_open"`
	SignupCredit     uint64 `json:"signup_credit_micros"`
	// EmailVerification: registering needs a code from POST /api/v1/register/code.
	EmailVerification bool   `json:"email_verification"`
	SupportEmail      string `json:"support_email,omitempty"`
}

// apiError is a failure the API reports with a stable code the app can act on.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

func errorCode(err error) string {
	var ae *apiError
	var pe *PortalError
	switch {
	case errors.As(err, &ae):
		return ae.Code
	case errors.As(err, &pe):
		return pe.Code
	}
	return "portal_unreachable"
}

func writeAPIError(w http.ResponseWriter, err error) {
	var ae *apiError
	var pe *PortalError
	switch {
	case errors.As(err, &ae):
		writeErrorCode(w, ae.Status, ae.Code, ae.Message)
	case errors.As(err, &pe):
		writeErrorCode(w, pe.Status, pe.Code, pe.Message)
	default:
		writeErrorCode(w, http.StatusBadGateway, "portal_unreachable", "cannot reach the portal: "+err.Error())
	}
}

func (d *Daemon) savedPin(portalURL string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfg.Portal == portalURL {
		return d.cfg.PortalPin
	}
	return ""
}

func (d *Daemon) probe(ctx context.Context, raw string) (*PortalProbe, error) {
	if strings.TrimSpace(raw) == "" {
		raw = PortalAddr()
	}
	base, err := NormalizePortalURL(raw)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, "bad_portal", err.Error()}
	}
	p := &PortalProbe{Portal: base, HTTPS: strings.HasPrefix(base, "https://")}
	pin := ""
	if !p.HTTPS {
		if InsecurePortalURL(base) && !d.AllowHTTP {
			return nil, &apiError{http.StatusBadRequest, "insecure_portal", "refusing a plain-HTTP portal on another machine: your password would cross the network in clear"}
		}
	} else {
		cert, err := ProbePortal(ctx, base)
		if err != nil {
			return nil, &apiError{http.StatusBadGateway, "portal_unreachable", "cannot reach the portal: " + err.Error()}
		}
		p.Trusted, p.Fingerprint, p.Subject, p.NotAfter = cert.Trusted, cert.Fingerprint, cert.Subject, &cert.NotAfter
		saved := d.savedPin(base)
		p.Known = saved != "" && strings.EqualFold(saved, cert.Fingerprint)
		p.PinChanged = saved != "" && !p.Known
		p.PinRequired = p.PinChanged || (!cert.Trusted && !p.Known)
		if !cert.Trusted {
			pin = cert.Fingerprint // only reads public data; the user has not accepted it yet
		}
	}
	info, err := NewPortalClient(base, "", pin).Info(ctx)
	if err != nil {
		var pe *PortalError
		if errors.As(err, &pe) {
			return nil, &apiError{http.StatusBadGateway, "not_a_portal", "this address did not answer like a sharetoken portal"}
		}
		return nil, &apiError{http.StatusBadGateway, "portal_unreachable", "cannot reach the portal: " + err.Error()}
	}
	p.RegistrationOpen, p.SignupCredit = info.RegistrationOpen, info.SignupCredit
	p.EmailVerification, p.SupportEmail = info.EmailVerification, info.SupportEmail
	return p, nil
}

// trust decides how to reach the portal before a password is sent to it and
// returns the pin to keep ("" when none is needed). The pin is either one the
// user confirmed after a probe or the one saved at an earlier login; a
// self-signed certificate with neither is refused.
func (d *Daemon) trust(ctx context.Context, base, pin string) (string, error) {
	if strings.HasPrefix(base, "http://") {
		if InsecurePortalURL(base) && !d.AllowHTTP {
			return "", &apiError{http.StatusBadRequest, "insecure_portal", "refusing a plain-HTTP portal on another machine: your password would cross the network in clear"}
		}
		return "", nil
	}
	cert, err := ProbePortal(ctx, base)
	if err != nil {
		return "", &apiError{http.StatusBadGateway, "portal_unreachable", "cannot reach the portal: " + err.Error()}
	}
	saved := d.savedPin(base)
	if pin == "" {
		pin = saved
	}
	switch {
	case pin != "":
		if !strings.EqualFold(pin, cert.Fingerprint) {
			return "", &apiError{http.StatusBadGateway, "pin_mismatch", fmt.Sprintf("the portal's certificate %s does not match %s: someone may be intercepting the connection, or the operator replaced the certificate", cert.Fingerprint, pin)}
		}
		if cert.Trusted {
			return "", nil
		}
		return cert.Fingerprint, nil
	case cert.Trusted:
		return "", nil
	}
	return "", &apiError{http.StatusBadRequest, "pin_required", "confirm the portal's self-signed certificate " + cert.Fingerprint}
}

// ---- signing in ----

type signInRequest struct {
	Portal   string `json:"portal"`
	Pin      string `json:"pin"`
	Email    string `json:"email"`   // register
	Code     string `json:"code"`    // register: the emailed verification code
	Account  string `json:"account"` // login: email address or username
	Password string `json:"password"`
	TOTP     string `json:"totp"`   // login of an administrator
	Device   string `json:"device"` // name of this device (default: host name)
}

// requestCode asks the portal to mail a sign-up code. Only an address crosses
// to the portal, but it is checked like a sign-in: the user's email is not for
// a portal they have not confirmed.
func (d *Daemon) requestCode(ctx context.Context, req signInRequest) (*CodeSent, error) {
	if strings.TrimSpace(req.Portal) == "" {
		req.Portal = PortalAddr()
	}
	base, err := NormalizePortalURL(req.Portal)
	if err != nil {
		return nil, &apiError{http.StatusBadRequest, "bad_portal", err.Error()}
	}
	if strings.TrimSpace(req.Email) == "" {
		return nil, &apiError{http.StatusBadRequest, "bad_request", "an email address is required"}
	}
	pin, err := d.trust(ctx, base, req.Pin)
	if err != nil {
		return nil, err
	}
	return NewPortalClient(base, "", pin).RequestRegisterCode(ctx, req.Email)
}

// signIn registers or logs in, binds this device and starts the agent.
func (d *Daemon) signIn(ctx context.Context, req signInRequest, register bool) error {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	d.mu.Lock()
	loggedIn := d.portal != nil
	d.mu.Unlock()
	if loggedIn {
		return &apiError{http.StatusConflict, "already_logged_in", "log out first"}
	}
	if strings.TrimSpace(req.Portal) == "" {
		req.Portal = PortalAddr()
	}
	base, err := NormalizePortalURL(req.Portal)
	if err != nil {
		return &apiError{http.StatusBadRequest, "bad_portal", err.Error()}
	}
	if req.Password == "" {
		return &apiError{http.StatusBadRequest, "bad_request", "a password is required"}
	}
	pin, err := d.trust(ctx, base, req.Pin)
	if err != nil {
		return err
	}
	cfg, err := d.loadConfig()
	if err != nil {
		return err
	}
	device := strings.TrimSpace(req.Device)
	if device == "" {
		device = cfg.DeviceName
	}
	if device == "" {
		device, _ = os.Hostname()
	}
	pc := NewPortalClient(base, "", pin)
	var res *AuthResult
	if register {
		if strings.TrimSpace(req.Email) == "" {
			return &apiError{http.StatusBadRequest, "bad_request", "an email address is required"}
		}
		res, err = pc.RegisterEmail(ctx, req.Email, req.Password, req.Code, "desktop on "+device)
	} else {
		if strings.TrimSpace(req.Account) == "" {
			return &apiError{http.StatusBadRequest, "bad_request", "an email address or username is required"}
		}
		res, err = pc.LoginCode(ctx, req.Account, req.Password, strings.TrimSpace(req.TOTP), "desktop on "+device)
	}
	if err != nil {
		return err
	}
	pc.Token = res.Token
	peer, err := d.bindDevice(ctx, pc, res.User.Username, device)
	if err != nil {
		// Do not leave a token behind that nothing holds on to.
		lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = pc.Logout(lctx)
		var pe *PortalError
		if errors.As(err, &pe) {
			return &apiError{pe.Status, "bind_failed", "signed in, but binding this device failed: " + pe.Message}
		}
		return &apiError{http.StatusBadGateway, "bind_failed", "signed in, but binding this device failed: " + err.Error()}
	}
	cfg.Portal, cfg.PortalPin, cfg.User, cfg.Token, cfg.DeviceName = base, pin, res.User.Username, res.Token, device
	if err := SaveConfig(d.home, cfg); err != nil {
		return err
	}
	d.mu.Lock()
	d.peer = peer
	d.mu.Unlock()
	d.log.Info("signed in", "user", res.User.Username, "device", peer)
	d.startRun(cfg)
	return nil
}

// bindDevice binds this machine's device key to the user. A key bound to
// another account (someone else used this machine) is retired for a new one:
// a device belongs to one account, and money belongs to accounts, not keys.
func (d *Daemon) bindDevice(ctx context.Context, pc *PortalClient, username, device string) (string, error) {
	path := filepath.Join(d.home, KeyFile)
	keys, err := p2p.LoadOrCreateKeys(path)
	if err != nil {
		return "", err
	}
	_, err = pc.BindDevice(ctx, keys, username, device)
	var pe *PortalError
	if errors.As(err, &pe) && pe.Code == "bound_elsewhere" {
		retired := fmt.Sprintf("%s.retired-%d", path, time.Now().UnixNano())
		d.log.Info("this device key belongs to another account; using a new one", "old", keys.ID(), "kept_as", retired)
		if err := os.Rename(path, retired); err != nil {
			return "", err
		}
		if keys, err = p2p.LoadOrCreateKeys(path); err != nil {
			return "", err
		}
		_, err = pc.BindDevice(ctx, keys, username, device)
	}
	if err != nil {
		return "", err
	}
	return keys.ID(), nil
}

// logout stops the agent, revokes the token (best effort: without the portal
// it just stays unused until it expires) and forgets it.
func (d *Daemon) logout(ctx context.Context) error {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	d.mu.Lock()
	pc := d.portal
	d.mu.Unlock()
	if pc == nil {
		return nil
	}
	d.stopRun()
	if err := pc.Logout(ctx); err != nil && !IsUnauthorized(err) {
		d.log.Warn("could not revoke the token at the portal", "err", err)
	}
	d.telemetry.signedOut("user")
	return d.clearLogin(StateLoggedOut, "")
}

// ---- HTTP ----

//go:embed all:ui
var uiFiles embed.FS

// Handler serves the app and the API. Everything an Agent serves is available
// while one runs.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	ui, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err)
	}
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Security-Policy", uiCSP)
			if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.ServeFileFS(w, r, ui, name)
		}
	}
	mux.HandleFunc("GET /{$}", page("index.html"))
	for _, f := range []string{"app.js", "app.css", "i18n.js", "i18n.css", "favicon.svg"} {
		mux.HandleFunc("GET /ui/"+f, page(f))
	}
	mux.HandleFunc("GET /ui/i18n/client/{file}", func(w http.ResponseWriter, r *http.Request) {
		page("i18n/client/"+r.PathValue("file"))(w, r)
	})
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, d.Session()) })
	mux.HandleFunc("POST /api/v1/portal/probe", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		Portal string `json:"portal"`
	}) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		p, err := d.probe(ctx, req.Portal)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
	signIn := func(register bool) http.HandlerFunc {
		return jsonBody(func(w http.ResponseWriter, r *http.Request, req signInRequest) {
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			method, status := "login", http.StatusOK
			if register {
				method, status = "register", http.StatusCreated
			}
			if err := d.signIn(ctx, req, register); err != nil {
				d.telemetry.signInFailed(method, errorCode(err))
				writeAPIError(w, err)
				return
			}
			d.telemetry.signedIn(method)
			writeJSON(w, status, d.Session())
		})
	}
	mux.HandleFunc("POST /api/v1/register/code", jsonBody(func(w http.ResponseWriter, r *http.Request, req signInRequest) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		sent, err := d.requestCode(ctx, req)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sent)
	}))
	mux.HandleFunc("POST /api/v1/register", signIn(true))
	mux.HandleFunc("POST /api/v1/login", signIn(false))
	mux.HandleFunc("POST /api/v1/logout", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := d.logout(ctx); err != nil {
			writeErrorCode(w, http.StatusInternalServerError, "logout_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, d.Session())
	})
	mux.HandleFunc("POST /api/v1/update/check", func(w http.ResponseWriter, r *http.Request) {
		d.updates.Check(r.Context())
		writeJSON(w, http.StatusOK, d.updates.Current())
	})
	updateAction := func(do func(string) error) http.HandlerFunc {
		return jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
			Key string `json:"key"`
		}) {
			if err := do(req.Key); errors.Is(err, errNotOffered) {
				writeErrorCode(w, http.StatusConflict, "not_offered", err.Error())
				return
			} else if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, d.updates.Current())
		})
	}
	mux.HandleFunc("POST /api/v1/update/skip", updateAction(func(k string) error { return d.updates.Skip(k) }))
	mux.HandleFunc("POST /api/v1/update/notified", updateAction(func(k string) error { return d.updates.MarkNotified(k) }))
	mux.HandleFunc("GET /api/v1/telemetry", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, d.telemetry.State()) })
	mux.HandleFunc("PUT /api/v1/telemetry", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		Enabled bool `json:"enabled"`
	}) {
		if err := d.telemetry.SetEnabled(req.Enabled); err != nil {
			writeErrorCode(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, d.telemetry.State())
	}))
	// The account pages work without the agent: the portal may be up while
	// the relays or the coordinator are not.
	mux.HandleFunc("POST /api/v1/feedback", jsonBody(func(w http.ResponseWriter, r *http.Request, req portal.FeedbackRequest) {
		d.mu.Lock()
		pc := d.portal
		d.mu.Unlock()
		if pc == nil {
			writeErrorCode(w, http.StatusUnauthorized, "not_logged_in", "not logged in")
			return
		}
		// The daemon says where this comes from; the page sends only the text.
		req.Source, req.AppVersion, req.OS = "client", version.Version, runtime.GOOS+"/"+runtime.GOARCH
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		var out map[string]int64
		if err := pc.Do(ctx, http.MethodPost, "/api/v1/feedback", req, &out); err != nil {
			if IsUnauthorized(err) {
				d.expire(pc.Token, "the portal rejected the login (expired or revoked)")
			}
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}))
	mux.HandleFunc("GET /api/v1/feedback/mine", d.fromPortal("/api/v1/feedback"))
	mux.HandleFunc("GET /api/v1/account", d.fromPortal("/api/v1/me"))
	mux.HandleFunc("GET /api/v1/jobs", d.fromPortal("/api/v1/jobs"))
	mux.HandleFunc("GET /api/v1/ledger", d.fromPortal("/api/v1/ledger"))
	mux.HandleFunc("/api/v1/", d.toAgent)
	mux.HandleFunc("/v1/", d.toAgent)
	return guardHost(guardAPIOrigin(guardOrigin(mux)))
}

// guardAPIOrigin keeps the management API, which handles logins, to the app:
// its own page, CLI tools, and the desktop shell (which sends no Origin). Local
// web apps and browser extensions, which guardOrigin lets use the OpenAI
// endpoint, stay out.
func guardAPIOrigin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && strings.HasPrefix(r.URL.Path, "/api/v1/") && o != "http://"+r.Host {
			writeErrorCode(w, http.StatusForbidden, "bad_origin", "the management API is only for the sharetoken app")
			return
		}
		h.ServeHTTP(w, r)
	})
}

const uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// guardHost rejects requests addressed to anything but this machine: with DNS
// rebinding a web page can make a name of its own resolve to 127.0.0.1, and the
// browser then counts this server as that page's origin.
func guardHost(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hn, _, err := net.SplitHostPort(host); err == nil {
			host = hn
		}
		host = strings.Trim(host, "[]")
		if host != "localhost" && !cli.IsLoopback(host) {
			writeErrorCode(w, http.StatusForbidden, "bad_host", "this API answers only to a loopback address")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// jsonBody decodes a small JSON request; requiring the content type keeps
// out what a web form could post.
func jsonBody[T any](h func(http.ResponseWriter, *http.Request, T)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if ct != "application/json" {
			writeErrorCode(w, http.StatusUnsupportedMediaType, "bad_request", "send application/json")
			return
		}
		var req T
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			writeErrorCode(w, http.StatusBadRequest, "bad_request", "malformed JSON")
			return
		}
		h(w, r, req)
	}
}

func (d *Daemon) fromPortal(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		pc := d.portal
		d.mu.Unlock()
		if pc == nil {
			writeErrorCode(w, http.StatusUnauthorized, "not_logged_in", "not logged in")
			return
		}
		if err := forwardPortalGET(w, r, pc, path); IsUnauthorized(err) {
			d.expire(pc.Token, "the portal rejected the login (expired or revoked)")
		}
	}
}

func (d *Daemon) toAgent(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	api, loggedIn, msg := d.agentAPI, d.portal != nil, d.lastErr
	d.mu.Unlock()
	switch {
	case api != nil:
		api.ServeHTTP(w, r)
	case !loggedIn:
		writeErrorCode(w, http.StatusUnauthorized, "not_logged_in", "not logged in")
	default:
		if msg == "" {
			msg = "still joining the network"
		}
		writeErrorCode(w, http.StatusServiceUnavailable, "not_connected", msg)
	}
}
