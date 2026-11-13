package client

import (
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
// it, and sign-in codes only pass through on their way to the portal.
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

	home string
	log  *slog.Logger
	srv  *http.Server
	addr string
	key  string // the local API's key (localauth.go)

	authMu sync.Mutex // serializes what changes who is logged in
	bg     sync.WaitGroup

	mu       sync.Mutex
	cfg      Config
	state    string
	lastErr  string
	portal   *PortalClient // nil: logged out
	peer     string
	mfa      *pendingMFA // a sign-in waiting for its second factor
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
	d.key = newLocalKey()
	d.srv = &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := d.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.log.Error("local API stopped", "err", err)
		}
	}()
	writeRunInfo(d.home, d.addr, d.key)

	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
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
	if d.srv != nil {
		_ = os.Remove(filepath.Join(d.home, RunFile))
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = d.srv.Shutdown(sctx)
		cancel()
	}
}

// Addr is the local API's address (host:port).
func (d *Daemon) Addr() string { return d.addr }

// Key is what the local API wants in Authorization (localauth.go).
func (d *Daemon) Key() string { return d.key }

// portalAddr is the portal the user signed in to, or last used.
func (d *Daemon) portalAddr() (base, pin string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.Portal, d.cfg.PortalPin
}

// Session reports the current state.
func (d *Daemon) Session() Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Session{Env: Env, State: d.state, LoggedIn: d.portal != nil, Portal: d.cfg.Portal, User: d.cfg.User, API: "http://" + d.addr + "/v1", Error: d.lastErr, Version: version.Version, APIVersion: version.API}
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

// PortalProbe is what the sign-in page shows about a portal before any code
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
	PinRequired bool `json:"pin_required"`
	// RegistrationOpen: an address without an account gets one at its first
	// sign-in. InviteRequired: such a sign-in needs an invite code.
	RegistrationOpen bool   `json:"registration_open"`
	InviteRequired   bool   `json:"invite_required"`
	SignupCredit     uint64 `json:"signup_credit_micros"`
	SupportEmail     string `json:"support_email,omitempty"`
}

// apiError is a failure the API reports with a stable code the app can act on.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

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
			return nil, &apiError{http.StatusBadRequest, "insecure_portal", "refusing a plain-HTTP portal on another machine: your sign-in code would cross the network in clear"}
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
	if !info.CodeSignIn {
		return nil, &apiError{http.StatusBadGateway, "portal_outdated", "this portal still signs in with passwords; it needs an update"}
	}
	p.RegistrationOpen, p.InviteRequired, p.SignupCredit, p.SupportEmail = info.RegistrationOpen, info.InviteRequired, info.SignupCredit, info.SupportEmail
	return p, nil
}

// trust decides how to reach the portal before an address or code is sent to it and
// returns the pin to keep ("" when none is needed). The pin is either one the
// user confirmed after a probe or the one saved at an earlier login; a
// self-signed certificate with neither is refused.
func (d *Daemon) trust(ctx context.Context, base, pin string) (string, error) {
	if strings.HasPrefix(base, "http://") {
		if InsecurePortalURL(base) && !d.AllowHTTP {
			return "", &apiError{http.StatusBadRequest, "insecure_portal", "refusing a plain-HTTP portal on another machine: your sign-in code would cross the network in clear"}
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
	Portal string `json:"portal"`
	Pin    string `json:"pin"`
	Email  string `json:"email"`
	Code   string `json:"code"`        // the mailed sign-in code
	Invite string `json:"invite_code"` // a new account, on portals that require one
	Device string `json:"device"`      // name of this device (default: host name)
}

// pendingMFA is a sign-in whose mailed code checked out and that waits for
// the authenticator code (POST /api/v1/auth/mfa).
type pendingMFA struct {
	pc        *PortalClient
	base, pin string
	challenge string
	device    string
	expires   time.Time
}

// mfaWait is how long a sign-in waits for its second factor; the portal's
// challenge lasts five minutes.
const mfaWait = 5 * time.Minute

// portalFor checks a request's portal and how to trust it. Only an address
// crosses to the portal before the code, but it is checked like a sign-in:
// the user's email is not for a portal they have not confirmed.
func (d *Daemon) portalFor(ctx context.Context, req signInRequest) (pc *PortalClient, base, pin string, err error) {
	if strings.TrimSpace(req.Portal) == "" {
		req.Portal = PortalAddr()
	}
	if base, err = NormalizePortalURL(req.Portal); err != nil {
		return nil, "", "", &apiError{http.StatusBadRequest, "bad_portal", err.Error()}
	}
	if strings.TrimSpace(req.Email) == "" {
		return nil, "", "", &apiError{http.StatusBadRequest, "bad_request", "an email address is required"}
	}
	if pin, err = d.trust(ctx, base, req.Pin); err != nil {
		return nil, "", "", err
	}
	return NewPortalClient(base, "", pin), base, pin, nil
}

// requestCode asks the portal to mail a sign-in code.
func (d *Daemon) requestCode(ctx context.Context, req signInRequest) (*CodeSent, error) {
	pc, _, _, err := d.portalFor(ctx, req)
	if err != nil {
		return nil, err
	}
	return pc.RequestCode(ctx, strings.TrimSpace(req.Email), strings.TrimSpace(req.Invite))
}

// errLoggedIn refuses a second sign-in.
var errLoggedIn = &apiError{http.StatusConflict, "already_logged_in", "log out first"}

func (d *Daemon) loggedIn() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.portal != nil
}

// signIn redeems a mailed code (creating the account of a new address), binds
// this device and starts the agent. An account with two-factor
// authentication is left waiting for verifyMFA.
func (d *Daemon) signIn(ctx context.Context, req signInRequest) error {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	if d.loggedIn() {
		return errLoggedIn
	}
	if strings.TrimSpace(req.Code) == "" {
		return &apiError{http.StatusBadRequest, "bad_request", "the code from the email is required"}
	}
	pc, base, pin, err := d.portalFor(ctx, req)
	if err != nil {
		return err
	}
	device, err := d.deviceName(req.Device)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.mfa = nil
	d.mu.Unlock()
	res, err := pc.SignIn(ctx, strings.TrimSpace(req.Email), strings.TrimSpace(req.Code), strings.TrimSpace(req.Invite), "", "desktop on "+device)
	var pe *PortalError
	switch {
	case errors.As(err, &pe) && pe.Code == "mfa_required" && pe.Challenge != "":
		d.mu.Lock()
		d.mfa = &pendingMFA{pc: pc, base: base, pin: pin, challenge: pe.Challenge, device: device, expires: time.Now().Add(mfaWait)}
		d.mu.Unlock()
		return &apiError{http.StatusUnauthorized, "mfa_required", "enter the code from your authenticator app"}
	case errors.As(err, &pe) && pe.Code == "mfa_setup_required":
		return &apiError{http.StatusForbidden, "mfa_setup_required", "administrators must set up two-factor authentication: sign in on the web portal first"}
	case err != nil:
		return err
	}
	return d.finishSignIn(ctx, pc, base, pin, res, device)
}

// verifyMFA finishes the sign-in signIn left waiting for its second factor.
func (d *Daemon) verifyMFA(ctx context.Context, code string) error {
	d.authMu.Lock()
	defer d.authMu.Unlock()
	if d.loggedIn() {
		return errLoggedIn
	}
	d.mu.Lock()
	m := d.mfa
	d.mu.Unlock()
	if m == nil || time.Now().After(m.expires) {
		return &apiError{http.StatusUnauthorized, "challenge_expired", "the sign-in took too long; request a new code"}
	}
	if strings.TrimSpace(code) == "" {
		return &apiError{http.StatusBadRequest, "bad_request", "the code from your authenticator app is required"}
	}
	res, err := m.pc.CompleteMFA(ctx, m.challenge, strings.TrimSpace(code), "desktop on "+m.device)
	if err != nil {
		var pe *PortalError
		if errors.As(err, &pe) && pe.Code == "challenge_expired" {
			d.mu.Lock()
			d.mfa = nil
			d.mu.Unlock()
		}
		return err
	}
	d.mu.Lock()
	d.mfa = nil
	d.mu.Unlock()
	return d.finishSignIn(ctx, m.pc, m.base, m.pin, res, m.device)
}

// deviceName is the name this device is bound under.
func (d *Daemon) deviceName(asked string) (string, error) {
	if device := strings.TrimSpace(asked); device != "" {
		return device, nil
	}
	cfg, err := d.loadConfig()
	if err != nil {
		return "", err
	}
	if cfg.DeviceName != "" {
		return cfg.DeviceName, nil
	}
	host, _ := os.Hostname()
	return host, nil
}

// finishSignIn binds this device with a new token, saves the login and
// starts the agent. Caller holds authMu.
func (d *Daemon) finishSignIn(ctx context.Context, pc *PortalClient, base, pin string, res *AuthResult, device string) error {
	cfg, err := d.loadConfig()
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
	d.log.Info("signed in", "user", res.User.Username, "device", peer, "new_account", res.Created)
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
	mux.HandleFunc("GET /api/v1/hello", func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		if !validNonce(nonce) {
			writeErrorCode(w, http.StatusBadRequest, "bad_request", "nonce: 16 to 64 random bytes in hex")
			return
		}
		writeJSON(w, http.StatusOK, Hello{Env: Env, Version: version.Version, APIVersion: version.API, Proof: LocalProof(d.key, d.addr, nonce)})
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
	mux.HandleFunc("POST /api/v1/auth/code", jsonBody(func(w http.ResponseWriter, r *http.Request, req signInRequest) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		sent, err := d.requestCode(ctx, req)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sent)
	}))
	mux.HandleFunc("POST /api/v1/auth/verify", jsonBody(func(w http.ResponseWriter, r *http.Request, req signInRequest) {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := d.signIn(ctx, req); err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d.Session())
	}))
	mux.HandleFunc("POST /api/v1/auth/mfa", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		Code string `json:"code"`
	}) {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := d.verifyMFA(ctx, req.Code); err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d.Session())
	}))
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
	return guardHost(guardAPIOrigin(guardOrigin(requireLocalKey(d.key, daemonOpen, mux))))
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
