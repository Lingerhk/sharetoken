// Package portal is sharetoken's user-management backend: a JSON HTTP API for
// sign-up and login, API tokens, device binding, credit balances and grants,
// job and token-usage history, and the relay registry.
//
// It shares one database with the coordinator (MySQL). The portal never talks to the
// coordinator: it writes accounts and credit grants, the coordinator writes
// jobs and settlements, and both move money only through the double-entry
// ledger inside a database transaction.
//
// Users' clients call the portal directly over HTTP(S); only the coordinator
// is hidden behind relays.
package portal

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sharetoken/accounts"
	"sharetoken/coordinator"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/version"
	"sharetoken/ledger"
	"sharetoken/mailer"
	"sharetoken/relaypass"
)

// Config configures the portal. Zero values get defaults.
type Config struct {
	DB                 *sql.DB
	OpenRegistration   bool          // anyone may sign up (else only admins create users)
	SignupCreditMicros uint64        // credit granted to each new user
	LoginTTL           time.Duration // lifetime of tokens issued at login (default 30 days)
	// PassKey signs relay passes (package relaypass); relays are configured
	// with its public key. nil: the portal issues no passes (open relays).
	PassKey ed25519.PrivateKey
	PassTTL time.Duration // lifetime of a relay pass (default 1h)
	// WebSessionTTL is the lifetime of a browser session (default 24h).
	WebSessionTTL time.Duration
	// SecureCookies marks session cookies Secure even on plain-HTTP
	// requests (the portal sits behind a TLS-terminating proxy).
	SecureCookies bool
	// TrustedProxies are reverse proxies on other machines whose X-Real-IP is
	// believed, as a proxy on loopback's always is.
	TrustedProxies []net.IP
	// MFAKey (32 bytes) seals administrators' TOTP secrets in the database.
	// nil: derived from PassKey. Without either, admins cannot log in, since
	// two-factor authentication is mandatory for them.
	MFAKey []byte
	// EmailVerification requires sign-ups to prove their email address with a
	// mailed code, and closes sign-up by plain username. It needs Mail.From.
	EmailVerification bool
	Mail              MailConfig
	// DownloadsDir holds the client packages offered at /downloads/ (downloads.go).
	// Empty: none.
	DownloadsDir string
	Now          func() time.Time
	Log          *slog.Logger
}

// Server is the portal.
type Server struct {
	cfg   Config
	db    *sql.DB
	store *accounts.Store
	mail  *mailer.Store
	log   *slog.Logger

	failMu sync.Mutex
	fails  map[string][]time.Time // login failures per username and per IP

	mfaKey     []byte
	mfaMu      sync.Mutex
	challenges map[string]*mfaChallenge // half-finished admin logins
}

// New prepares the portal on cfg.DB, creating any missing tables.
func New(cfg Config) (*Server, error) {
	if cfg.DB == nil {
		return nil, errors.New("portal: a database is required")
	}
	if cfg.LoginTTL == 0 {
		cfg.LoginTTL = 30 * 24 * time.Hour
	}
	if cfg.PassTTL == 0 {
		cfg.PassTTL = time.Hour
	}
	if cfg.WebSessionTTL == 0 {
		cfg.WebSessionTTL = 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default().With("component", "portal")
	}
	// Under heavy coordinator traffic the write lock can be held past the
	// lock wait timeout; a transient timeout at start must not take the portal
	// down (it stayed down once on the test network for exactly this).
	deadline := time.Now().Add(migrateRetryFor)
	for {
		err := coordinator.Migrate(cfg.DB)
		if err == nil {
			err = mailer.Migrate(cfg.DB)
		}
		if err == nil {
			break
		}
		if !mysqldb.IsBusy(err) || time.Now().After(deadline) {
			return nil, err
		}
		cfg.Log.Warn("database busy, retrying migration", "err", err)
		time.Sleep(migrateRetryEvery)
	}
	if cfg.EmailVerification && cfg.Mail.From == "" {
		return nil, errors.New("portal: email verification needs a mail sender (mail.from)")
	}
	key := cfg.MFAKey
	if key == nil && cfg.PassKey != nil {
		key = deriveMFAKey(cfg.PassKey)
	}
	if key != nil && len(key) != 32 {
		return nil, errors.New("portal: MFAKey must be 32 bytes")
	}
	return &Server{
		cfg: cfg, db: cfg.DB, log: cfg.Log,
		store:      &accounts.Store{DB: cfg.DB, Now: cfg.Now},
		mail:       &mailer.Store{DB: cfg.DB, Now: cfg.Now},
		fails:      make(map[string][]time.Time),
		mfaKey:     key,
		challenges: make(map[string]*mfaChallenge),
	}, nil
}

// Store exposes the account store (admin commands).
func (s *Server) Store() *accounts.Store { return s.store }

// ---- operations shared by the HTTP API and the admin CLI ----

// CreateUser registers a user and grants the sign-up credit.
func (s *Server) CreateUser(username, password string, admin bool) (*accounts.User, error) {
	u, err := s.store.CreateUser(username, password, admin)
	if err != nil {
		return nil, err
	}
	if s.cfg.SignupCreditMicros > 0 {
		if err := s.AdjustCredit(u.ID, int64(min(s.cfg.SignupCreditMicros, math.MaxInt64)), "sign-up credit"); err != nil {
			return u, err
		}
	}
	return u, nil
}

// AdjustCredit adds micros (> 0) to a user's account from outside the
// network, or removes -micros (< 0) from its available balance.
func (s *Server) AdjustCredit(userID, micros int64, memo string) error {
	if micros == 0 {
		return ledger.ErrInvalidAmount
	}
	if memo = strings.TrimSpace(memo); memo == "" {
		memo = "admin adjustment"
	}
	if len(memo) > 128 {
		memo = memo[:128]
	}
	if _, err := s.store.User(userID); err != nil {
		return err
	}
	return s.ledgerTx(func(_ *sql.Tx, led *ledger.Tx) error {
		acct := ledger.UserAccount(userID)
		if micros > 0 {
			return led.Deposit(acct, micros, memo)
		}
		return led.Transfer(acct, ledger.External, -micros, memo)
	})
}

// Balance is an account's available and held credit.
type Balance struct {
	Account         string `json:"account"`
	AvailableMicros int64  `json:"available_micros"`
	HeldMicros      int64  `json:"held_micros"`
}

// Balance reads a user's balance.
func (s *Server) Balance(userID int64) (Balance, error) {
	b := Balance{Account: ledger.UserAccount(userID)}
	err := s.ledgerRead(func(_ *sql.Tx, led *ledger.Tx) (err error) {
		if b.AvailableMicros, err = led.Balance(b.Account); err != nil {
			return err
		}
		b.HeldMicros, err = led.Held(b.Account)
		return err
	})
	return b, err
}

// Overview is the platform's state for operators.
type Overview struct {
	Users           int64  `json:"users"`
	Devices         int64  `json:"devices"`
	Circulating     int64  `json:"circulating_micros"` // credits issued into the network (-External)
	PlatformFees    int64  `json:"platform_fees_micros"`
	OpenHolds       int64  `json:"open_holds_micros"`
	BooksBalance    bool   `json:"books_balance"`
	BooksError      string `json:"books_error,omitempty"`
	CoordinatorPeer string `json:"coordinator,omitempty"`
	// Dashboard counters.
	DisabledUsers int64            `json:"disabled_users"`
	Jobs          map[string]int64 `json:"jobs"` // by status
	Relays        int64            `json:"relays"`
	RelaysEnabled int64            `json:"relays_enabled"`
	RelaysOK      int64            `json:"relays_ok"`
	FeeBps        int64            `json:"fee_bps"`
}

// Overview summarizes the platform and replays the ledger journal.
func (s *Server) Overview() (Overview, error) {
	var o Overview
	err := s.ledgerRead(func(tx *sql.Tx, led *ledger.Tx) error {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&o.Users); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&o.Devices); err != nil {
			return err
		}
		ext, err := led.Balance(ledger.External)
		if err != nil {
			return err
		}
		o.Circulating = -ext
		if o.PlatformFees, err = led.Balance(ledger.PlatformFees); err != nil {
			return err
		}
		var holds sql.NullInt64
		if err := tx.QueryRow(`SELECT SUM(amount) FROM ledger_holds WHERE open = 1`).Scan(&holds); err != nil {
			return err
		}
		o.OpenHolds = holds.Int64
		if err := led.Reconcile(); err != nil {
			o.BooksError = err.Error()
		} else {
			o.BooksBalance = true
		}
		return nil
	})
	if err != nil {
		return o, err
	}
	if o.CoordinatorPeer, err = coordinator.CoordinatorID(s.db); err != nil {
		return o, err
	}
	return o, s.overviewExtras(&o)
}

func (s *Server) ledgerTx(fn func(tx *sql.Tx, led *ledger.Tx) error) error {
	tx, err := mysqldb.Begin(s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx, ledger.On(tx, s.cfg.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

// ledgerRead runs fn in a read-only (deferred) transaction. Unlike ledgerTx
// it never takes the database's write lock, so the coordinator's writes can
// not make it wait or fail: under load the write lock is held most of the
// time and an immediate transaction starves. fn must not write.
func (s *Server) ledgerRead(fn func(tx *sql.Tx, led *ledger.Tx) error) error {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx, ledger.On(tx, s.cfg.Now()))
}

// NetworkInfo is what a client needs to join the network.
type NetworkInfo struct {
	Coordinator string        `json:"coordinator"` // peer ID; reach it through any relay
	Relays      []RelayPublic `json:"relays"`
	PassKey     string        `json:"pass_key,omitempty"` // public key relays verify passes with
}

// RelayPublic is a relay as clients see it.
type RelayPublic struct {
	Addr   string `json:"addr"`
	Name   string `json:"name,omitempty"`
	Region string `json:"region,omitempty"`
	OK     bool   `json:"ok"`               // the coordinator is reachable through it
	RTT    int64  `json:"rtt_ms,omitempty"` // coordinator → relay round trip; 0 when not measured yet
}

// Network returns the coordinator and the enabled relays, working ones first.
func (s *Server) Network() (NetworkInfo, error) {
	id, err := coordinator.CoordinatorID(s.db)
	if err != nil {
		return NetworkInfo{}, err
	}
	rs, err := s.store.Relays(true)
	if err != nil {
		return NetworkInfo{}, err
	}
	out := NetworkInfo{Coordinator: id, Relays: []RelayPublic{}}
	if s.cfg.PassKey != nil {
		out.PassKey = relaypass.FormatKey(s.cfg.PassKey.Public().(ed25519.PublicKey))
	}
	for _, r := range rs {
		out.Relays = append(out.Relays, RelayPublic{Addr: r.Addr, Name: r.Name, Region: r.Region, OK: r.CoordinatorOK, RTT: r.RTTMillis})
	}
	// Working relays first, the fastest of them at the top.
	sort.SliceStable(out.Relays, func(i, j int) bool {
		a, b := out.Relays[i], out.Relays[j]
		if a.OK != b.OK {
			return a.OK
		}
		if a.OK && (a.RTT > 0) != (b.RTT > 0) {
			return a.RTT > 0
		}
		return a.OK && a.RTT < b.RTT
	})
	return out, nil
}

// ---- HTTP ----

// Handler returns the portal's HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.servePages(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/v1/info", s.handleInfo)
	mux.HandleFunc("GET /api/v1/network", s.handleNetwork)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/downloads", s.handleDownloads)
	mux.HandleFunc("GET /api/v1/update", s.handleUpdate)
	mux.HandleFunc("GET /downloads/{file}", s.handleDownloadFile)
	mux.Handle("POST /api/v1/register", s.clientOnly(http.HandlerFunc(s.handleRegister)))
	mux.HandleFunc("POST /api/v1/register/code", s.handleRegisterCode) // the web sign-up uses it too; the sign-up itself is what is guarded
	mux.Handle("POST /api/v1/login", s.clientOnly(http.HandlerFunc(s.handleLogin)))

	// Browser sessions (cookie): only for the portal's own pages.
	mux.HandleFunc("POST /api/v1/web/login", webOnly(s.handleWebLogin))
	mux.HandleFunc("POST /api/v1/web/register", webOnly(s.handleWebRegister))
	mux.HandleFunc("POST /api/v1/web/mfa", webOnly(s.handleWebMFA))
	mux.HandleFunc("POST /api/v1/web/mfa/setup", webOnly(s.handleWebMFASetup))
	mux.HandleFunc("POST /api/v1/web/mfa/confirm", webOnly(s.handleWebMFAConfirm))
	mux.Handle("POST /api/v1/mfa", s.clientOnly(http.HandlerFunc(s.handleMFA)))
	mux.Handle("POST /api/v1/web/logout", s.auth(s.handleWebLogout))

	mux.Handle("POST /api/v1/logout", s.auth(s.handleLogout))
	mux.Handle("GET /api/v1/me", s.auth(s.handleMe))
	mux.Handle("POST /api/v1/password", s.auth(s.handlePassword))
	mux.Handle("GET /api/v1/tokens", s.auth(s.handleTokens))
	mux.Handle("POST /api/v1/tokens", s.auth(s.handleNewToken))
	mux.Handle("DELETE /api/v1/tokens/{id}", s.auth(s.handleRevokeToken))
	mux.Handle("GET /api/v1/devices", s.auth(s.handleDevices))
	mux.Handle("POST /api/v1/devices", s.clientOnly(s.auth(s.handleBindDevice)))
	mux.Handle("DELETE /api/v1/devices/{peer}", s.auth(s.handleUnbindDevice))
	mux.Handle("PATCH /api/v1/devices/{peer}", s.auth(s.handleRenameDevice))
	mux.Handle("GET /api/v1/ledger", s.auth(s.handleLedger))
	mux.Handle("POST /api/v1/relay-pass", s.auth(s.handleRelayPass))
	mux.Handle("GET /api/v1/jobs", s.auth(s.handleJobs))
	mux.Handle("GET /api/v1/usage", s.auth(s.handleUsage))

	mux.Handle("GET /api/v1/admin/versions", s.admin(s.handleAdminVersions))
	mux.Handle("PUT /api/v1/admin/version-policy", s.admin(s.handleSetVersionPolicy))
	mux.Handle("GET /api/v1/admin/overview", s.admin(s.handleOverview))
	mux.Handle("GET /api/v1/admin/users", s.admin(s.handleUsers))
	mux.Handle("GET /api/v1/admin/users/search", s.admin(s.handleSearchUsers))
	mux.Handle("POST /api/v1/admin/users", s.admin(s.handleAdminCreateUser))
	mux.Handle("GET /api/v1/admin/users/{name}", s.admin(s.handleUser))
	mux.Handle("POST /api/v1/admin/users/{name}/credit", s.admin(s.handleCredit))
	mux.Handle("POST /api/v1/admin/users/{name}/disable", s.admin(s.handleDisable(true)))
	mux.Handle("POST /api/v1/admin/users/{name}/enable", s.admin(s.handleDisable(false)))
	mux.Handle("POST /api/v1/admin/users/{name}/admin", s.admin(s.handleSetAdmin))
	mux.Handle("POST /api/v1/admin/users/{name}/password", s.admin(s.handleAdminPassword))
	mux.Handle("POST /api/v1/admin/users/{name}/mfa/reset", s.admin(s.handleAdminResetMFA))
	mux.Handle("GET /api/v1/admin/users/{name}/tokens", s.admin(s.handleAdminTokens))
	mux.Handle("DELETE /api/v1/admin/users/{name}/tokens/{id}", s.admin(s.handleAdminRevokeToken))
	mux.Handle("POST /api/v1/admin/users/{name}/tokens/revoke", s.admin(s.handleAdminRevokeAll))
	mux.Handle("DELETE /api/v1/admin/users/{name}/devices/{peer}", s.admin(s.handleAdminUnbind))
	mux.Handle("GET /api/v1/admin/users/{name}/ledger", s.admin(s.handleAdminUserLedger))
	mux.Handle("GET /api/v1/admin/users/{name}/jobs", s.admin(s.handleAdminUserJobs))
	mux.Handle("GET /api/v1/admin/relays", s.admin(s.handleRelays))
	mux.Handle("POST /api/v1/admin/relays", s.admin(s.handleAddRelay))
	mux.Handle("PATCH /api/v1/admin/relays/{id}", s.admin(s.handleUpdateRelay))
	mux.Handle("POST /api/v1/admin/relays/{id}/enable", s.admin(s.handleRelayEnabled(true)))
	mux.Handle("POST /api/v1/admin/relays/{id}/disable", s.admin(s.handleRelayEnabled(false)))
	mux.Handle("DELETE /api/v1/admin/relays/{id}", s.admin(s.handleRemoveRelay))
	mux.Handle("GET /api/v1/admin/jobs", s.admin(s.handleAdminJobs))
	mux.Handle("GET /api/v1/admin/nodes", s.admin(s.handleNodes))
	mux.Handle("GET /api/v1/admin/audits", s.admin(s.handleAudits))
	mux.Handle("GET /api/v1/admin/disputes", s.admin(s.handleDisputes))
	mux.Handle("POST /api/v1/admin/disputes/{job}/resolve", s.admin(s.handleResolveDispute))
	mux.Handle("GET /api/v1/admin/ledger", s.admin(s.handleJournal))
	mux.Handle("GET /api/v1/admin/accounts", s.admin(s.handleAccounts))
	s.mailRoutes(mux)
	s.feedbackRoutes(mux)
	return withLimits(mux)
}

const maxBody = 64 << 10

// How long New keeps retrying the schema migration while another process
// holds the database's write lock.
const (
	migrateRetryFor   = 2 * time.Minute
	migrateRetryEvery = 2 * time.Second
)

func withLimits(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set(ServerVersionHeader, version.Version)
		securityHeaders(w, r)
		h.ServeHTTP(w, r)
	})
}

type ctxUser struct {
	user    *accounts.User
	tokenID int64
	mfa     bool // the token was issued after two-factor verification
}

type authedHandler func(w http.ResponseWriter, r *http.Request, cu ctxUser)

func (s *Server) auth(h authedHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := s.credential(w, r)
		if !ok {
			return
		}
		u, id, mfa, err := s.store.UserByToken(tok)
		if err != nil {
			if errors.Is(err, accounts.ErrBadToken) || errors.Is(err, accounts.ErrDisabled) || errors.Is(err, accounts.ErrNotFound) {
				writeErr(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			s.internal(w, err)
			return
		}
		h(w, r, ctxUser{user: u, tokenID: id, mfa: mfa})
	})
}

func (s *Server) admin(h authedHandler) http.Handler {
	return s.auth(func(w http.ResponseWriter, r *http.Request, cu ctxUser) {
		if !cu.user.Admin {
			writeErr(w, http.StatusForbidden, "forbidden", "admin only")
			return
		}
		if !cu.mfa {
			// A token from before the promotion (or before two-factor
			// authentication existed): the admin must log in again.
			writeErr(w, http.StatusForbidden, "mfa_required", "admin access needs a login with two-factor authentication")
			return
		}
		h(w, r, cu)
	})
}

func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	n, err := s.Network()
	if err != nil {
		s.internal(w, err)
		return
	}
	if n.Coordinator == "" {
		writeErr(w, http.StatusServiceUnavailable, "not_ready", "the coordinator has not started yet")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type credentials struct {
	Email     string `json:"email"` // sign-up with an email address: it becomes the account name
	Username  string `json:"username"`
	Password  string `json:"password"`
	TokenName string `json:"token_name"`
	TOTP      string `json:"totp"` // admins: the current two-factor code (API login in one step)
	Code      string `json:"code"` // sign-up: the emailed verification code
}

type AuthResponse struct {
	User  *accounts.User `json:"user"`
	Token string         `json:"token"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	u, ok := s.register(w, r, c)
	if !ok {
		return
	}
	s.issueLoginToken(w, u, c.TokenName, http.StatusCreated, false)
}

// register creates the account of a sign-up, writing the error response
// itself. Sign-ups are rate-limited per IP.
func (s *Server) register(w http.ResponseWriter, r *http.Request, c credentials) (*accounts.User, bool) {
	if !s.cfg.OpenRegistration {
		writeErr(w, http.StatusForbidden, "registration_closed", "registration is closed; ask an administrator for an account")
		return nil, false
	}
	ip := s.clientIP(r)
	if s.throttled("reg:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many sign-ups from this address; try again in a few minutes")
		return nil, false
	}
	name := c.Username
	if c.Email != "" {
		// Unlike username, this field never falls back to a plain name.
		name = accounts.NormalizeEmail(c.Email)
		if err := accounts.CheckEmail(name); err != nil {
			s.accountErr(w, err)
			return nil, false
		}
	}
	if s.cfg.EmailVerification {
		// With verification on, a plain username would be a way around it.
		name = accounts.NormalizeEmail(name)
		if err := accounts.CheckEmail(name); err != nil {
			writeErr(w, http.StatusBadRequest, "email_required", "sign-up needs an email address")
			return nil, false
		}
		if err := accounts.CheckPassword(c.Password); err != nil {
			s.accountErr(w, err) // before the code: a short password must not use it up
			return nil, false
		}
		if !s.checkRegisterCode(w, ip, name, c.Code) {
			return nil, false
		}
	}
	u, err := s.CreateUser(name, c.Password, false)
	if err != nil {
		s.accountErr(w, err)
		return nil, false
	}
	s.recordFailure("reg:" + ip)
	s.log.Info("user registered", "user", u.Username, "id", u.ID, "ip", ip)
	return u, true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	u, mfa, ok := s.authenticate(w, r, c)
	if !ok {
		return
	}
	s.issueLoginToken(w, u, c.TokenName, http.StatusOK, mfa)
}

// authenticate checks a login, writing the error response itself.
// authenticate checks a login, writing the error response itself. For an
// administrator it also runs the second factor (see mfa.go); mfa reports
// whether one was verified, which the issued token remembers.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, c credentials) (u *accounts.User, mfa, ok bool) {
	ip := s.clientIP(r)
	name := strings.ToLower(strings.TrimSpace(c.Username))
	if s.throttled("u:"+name) || s.throttled("ip:"+ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many failed logins; try again in a few minutes")
		return nil, false, false
	}
	u, err := s.store.Authenticate(name, c.Password)
	if err != nil {
		if errors.Is(err, accounts.ErrBadCredentials) || errors.Is(err, accounts.ErrDisabled) {
			s.recordFailure("u:"+name, "ip:"+ip)
			writeErr(w, http.StatusUnauthorized, "bad_credentials", err.Error())
			return nil, false, false
		}
		s.internal(w, err)
		return nil, false, false
	}
	proceed, mfa := s.secondFactor(w, r, u, mfaCodeField(c))
	if !proceed {
		return nil, false, false
	}
	return u, mfa, true
}

func (s *Server) issueLoginToken(w http.ResponseWriter, u *accounts.User, name string, status int, mfa bool) {
	if name = strings.TrimSpace(name); name == "" {
		name = "login"
	}
	plain, _, err := s.store.IssueSessionToken(u.ID, name, s.cfg.LoginTTL, mfa)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, status, AuthResponse{User: u, Token: plain})
}

const (
	failWindow = 5 * time.Minute
	maxFails   = 8
)

func (s *Server) throttled(key string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	cut := s.cfg.Now().Add(-failWindow)
	fs := s.fails[key]
	for len(fs) > 0 && fs[0].Before(cut) {
		fs = fs[1:]
	}
	if len(fs) == 0 {
		delete(s.fails, key)
	} else {
		s.fails[key] = fs
	}
	return len(fs) >= maxFails
}

func (s *Server) recordFailure(keys ...string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if len(s.fails) > 100_000 {
		clear(s.fails)
	}
	for _, k := range keys {
		s.fails[k] = append(s.fails[k], s.cfg.Now())
	}
}

// clientIP is the address failures and rate limits are keyed on. Behind a
// local reverse proxy every connection comes from loopback, so there (and only
// there) the proxy's X-Real-IP is believed; a remote peer cannot set it.
func (s *Server) clientIP(r *http.Request) string { return clientIP(r, s.cfg.TrustedProxies) }

func clientIP(r *http.Request, trusted []net.IP) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || slices.ContainsFunc(trusted, ip.Equal)) {
		if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); real != nil {
			return real.String()
		}
	}
	return host
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	if err := s.store.RevokeToken(cu.user.ID, cu.tokenID); err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Me is the signed-in user's summary.
type Me struct {
	User    *accounts.User    `json:"user"`
	Balance Balance           `json:"balance"`
	Usage   coordinator.Usage `json:"usage"`
	Devices []accounts.Device `json:"devices"`
}

func (s *Server) me(u *accounts.User) (*Me, error) {
	b, err := s.Balance(u.ID)
	if err != nil {
		return nil, err
	}
	usage, err := coordinator.AccountUsage(s.db, b.Account)
	if err != nil {
		return nil, err
	}
	ds, err := s.store.Devices(u.ID)
	if err != nil {
		return nil, err
	}
	return &Me{User: u, Balance: b, Usage: usage, Devices: ds}, nil
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	m, err := s.me(cu.user)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Old          string `json:"old_password"`
		New          string `json:"new_password"`
		RevokeOthers bool   `json:"revoke_others"` // sign every other token / session out
	}
	if !readJSON(w, r, &req) {
		return
	}
	// A wrong current password counts like a failed login, so a stolen
	// session cannot brute-force it.
	if s.throttled("u:" + cu.user.Username) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many failed attempts; try again in a few minutes")
		return
	}
	if _, err := s.store.Authenticate(cu.user.Username, req.Old); err != nil {
		s.recordFailure("u:" + cu.user.Username)
		writeErr(w, http.StatusUnauthorized, "bad_credentials", "the current password is wrong")
		return
	}
	if err := s.store.SetPassword(cu.user.ID, req.New); err != nil {
		s.accountErr(w, err)
		return
	}
	s.notify(cu.user.Username, "ShareToken: your password was changed", "The password of your ShareToken account was just changed.\r\nIf this wasn't you, contact "+s.supportAddress()+" immediately.\r\n")
	var revoked int64
	if req.RevokeOthers {
		ts, err := s.store.Tokens(cu.user.ID)
		if err != nil {
			s.internal(w, err)
			return
		}
		for _, t := range ts {
			if t.ID != cu.tokenID {
				if err := s.store.RevokeToken(cu.user.ID, t.ID); err == nil {
					revoked++
				}
			}
		}
	}
	s.log.Info("password changed", "user", cu.user.Username, "tokens_revoked", revoked)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens_revoked": revoked})
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	ts, err := s.store.Tokens(cu.user.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": ts, "current": cu.tokenID})
}

func (s *Server) handleNewToken(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Name     string `json:"name"`
		TTLHours int64  `json:"ttl_hours"` // 0: never expires
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.TTLHours < 0 || req.TTLHours > 24*3650 {
		writeErr(w, http.StatusBadRequest, "bad_request", "ttl_hours must be 0-87600")
		return
	}
	plain, t, err := s.store.IssueSessionToken(cu.user.ID, req.Name, time.Duration(req.TTLHours)*time.Hour, cu.mfa)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": plain, "info": t})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad token id")
		return
	}
	if err := s.store.RevokeToken(cu.user.ID, id); err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	ds, err := s.store.Devices(cu.user.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": ds})
}

// BindRequest binds a device: the device signs accounts.BindMessage.
type BindRequest struct {
	Peer     string `json:"peer"`
	Name     string `json:"name"`
	UnixNano int64  `json:"ts"`
	Sig      []byte `json:"sig"`
}

func (s *Server) handleBindDevice(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req BindRequest
	if !readJSON(w, r, &req) {
		return
	}
	d, err := s.store.BindDevice(cu.user, req.Peer, req.Name, req.UnixNano, req.Sig)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	s.log.Info("device bound", "user", cu.user.Username, "peer", d.Peer, "name", d.Name)
	if err := s.store.SetDeviceVersion(d.Peer, r.Header.Get(ClientVersionHeader)); err != nil {
		s.log.Warn("recording a device's version", "err", err)
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleUnbindDevice(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	if err := s.store.UnbindDevice(cu.user.ID, r.PathValue("peer")); err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// LedgerLine is one journal entry from the user's point of view.
type LedgerLine struct {
	Seq    int64  `json:"seq"`
	At     int64  `json:"at"`
	JobID  string `json:"job_id,omitempty"`
	Amount int64  `json:"amount_micros"` // positive: into the account
	Memo   string `json:"memo"`
}

func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	s.writeLedger(w, r, cu.user.ID)
}

// writeLedger answers with a user's journal, newest first.
func (s *Server) writeLedger(w http.ResponseWriter, r *http.Request, userID int64) {
	before, limit := queryInt(r, "before", 0), queryLimit(r)
	acct := ledger.UserAccount(userID)
	var lines []LedgerLine
	err := s.ledgerRead(func(_ *sql.Tx, led *ledger.Tx) error {
		es, err := led.AccountEntries(acct, before, limit)
		for _, e := range es {
			amt := e.Amount
			if e.Debit == acct {
				amt = -amt
			}
			lines = append(lines, LedgerLine{Seq: e.Seq, At: e.At, JobID: e.JobID, Amount: amt, Memo: e.Memo})
		}
		return err
	})
	if err != nil {
		s.internal(w, err)
		return
	}
	if lines == nil {
		lines = []LedgerLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": acct, "entries": lines})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	role := r.URL.Query().Get("role")
	jobs, err := coordinator.AccountJobs(s.db, ledger.UserAccount(cu.user.ID), role, queryInt(r, "before", 0), queryLimit(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u, err := coordinator.AccountUsage(s.db, ledger.UserAccount(cu.user.ID))
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// IssuePass signs a relay pass for peer if it is a device of the active user u.
func (s *Server) IssuePass(u *accounts.User, peer string) (*relaypass.Pass, error) {
	if s.cfg.PassKey == nil {
		return nil, errNoPasses
	}
	acct, uid, err := accounts.DeviceAccount(s.db, peer)
	if err != nil {
		return nil, err
	}
	if uid != u.ID {
		return nil, accounts.ErrBoundElsewhere
	}
	return relaypass.Issue(s.cfg.PassKey, peer, acct, s.cfg.Now(), s.cfg.PassTTL), nil
}

var errNoPasses = errors.New("portal: relay passes are not enabled")

func (s *Server) handleRelayPass(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Peer string `json:"peer"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, err := s.IssuePass(cu.user, req.Peer)
	switch {
	case err == nil:
		// Devices that only buy never announce an offer: passes are where
		// their version shows up, once an hour.
		if err := s.store.SetDeviceVersion(req.Peer, r.Header.Get(ClientVersionHeader)); err != nil {
			s.log.Warn("recording a device's version", "err", err)
		}
		writeJSON(w, http.StatusOK, p)
	case errors.Is(err, errNoPasses):
		writeErr(w, http.StatusNotFound, "passes_disabled", err.Error())
	case errors.Is(err, accounts.ErrUnknownDevice), errors.Is(err, accounts.ErrBoundElsewhere):
		writeErr(w, http.StatusForbidden, "not_your_device", "this peer is not a device of your account; bind it first")
	default:
		s.accountErr(w, err)
	}
}

// ---- admin ----

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	o, err := s.Overview()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	us, err := s.store.Users(queryLimit(r), int(queryInt(r, "offset", 0)))
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": us})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Admin    bool   `json:"is_admin"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	u, err := s.CreateUser(req.Username, req.Password, req.Admin)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	s.log.Info("user created by admin", "user", u.Username, "admin", cu.user.Username)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) userByPath(w http.ResponseWriter, r *http.Request) *accounts.User {
	u, err := s.store.UserByName(r.PathValue("name"))
	if err != nil {
		s.accountErr(w, err)
		return nil
	}
	return u
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	m, err := s.me(u)
	if err != nil {
		s.internal(w, err)
		return
	}
	ts, err := s.store.Tokens(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*Me
		Tokens []accounts.Token `json:"tokens"`
	}{m, ts})
}

func (s *Server) handleCredit(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	var req struct {
		Micros int64  `json:"micros"` // negative deducts
		Memo   string `json:"memo"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.AdjustCredit(u.ID, req.Micros, req.Memo); err != nil {
		s.accountErr(w, err)
		return
	}
	s.log.Info("credit adjusted", "user", u.Username, "micros", req.Micros, "by", cu.user.Username, "memo", req.Memo)
	b, err := s.Balance(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleDisable(disabled bool) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, cu ctxUser) {
		u := s.userByPath(w, r)
		if u == nil {
			return
		}
		if u.ID == cu.user.ID {
			writeErr(w, http.StatusBadRequest, "bad_request", "you cannot disable yourself")
			return
		}
		if err := s.store.SetDisabled(u.ID, disabled); err != nil {
			s.accountErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handleRelays(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	rs, err := s.store.Relays(false)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": rs})
}

func (s *Server) handleAddRelay(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	var req struct{ Addr, Name, Region string }
	if !readJSON(w, r, &req) {
		return
	}
	rl, err := s.store.AddRelay(req.Addr, req.Name, req.Region)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rl)
}

func (s *Server) handleRelayEnabled(enabled bool) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, _ ctxUser) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err == nil {
			err = s.store.SetRelayEnabled(id, enabled)
		}
		if err != nil {
			s.accountErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handleRemoveRelay(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		err = s.store.RemoveRelay(id)
	}
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- helpers ----

func queryInt(r *http.Request, key string, def int64) int64 {
	v, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil {
		return def
	}
	return v
}

func queryLimit(r *http.Request) int {
	return int(min(max(queryInt(r, "limit", 50), 1), 500))
}

// readJSON decodes a request body. Unknown fields are ignored, so a client newer
// than this portal can add an optional field without being refused.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body: "+err.Error())
		return false
	}
	return true
}

// ErrorBody is the shape of every error response.
type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var b ErrorBody
	b.Error.Code, b.Error.Message = code, msg
	writeJSON(w, status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	if mysqldb.IsBusy(err) {
		// Another process held the write lock for the whole busy timeout.
		// Nothing is wrong with the request: tell the client to retry.
		s.log.Warn("database busy", "err", err)
		w.Header().Set("Retry-After", "2")
		writeErr(w, http.StatusServiceUnavailable, "busy", "the database is busy, retry shortly")
		return
	}
	s.log.Error("request failed", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

// accountErr maps accounts and ledger errors to HTTP statuses.
func (s *Server) accountErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounts.ErrInvalid), errors.Is(err, ledger.ErrInvalidAmount):
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, accounts.ErrExists):
		writeErr(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, accounts.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, accounts.ErrBoundElsewhere):
		writeErr(w, http.StatusConflict, "bound_elsewhere", err.Error())
	case errors.Is(err, accounts.ErrBadSignature), errors.Is(err, accounts.ErrStale):
		writeErr(w, http.StatusBadRequest, "bad_signature", err.Error())
	case errors.Is(err, ledger.ErrInsufficientFunds):
		writeErr(w, http.StatusConflict, "insufficient_funds", err.Error())
	default:
		s.internal(w, fmt.Errorf("%w", err))
	}
}
