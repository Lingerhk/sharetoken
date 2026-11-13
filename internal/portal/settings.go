package portal

// System settings: what operators change from the admin console without a
// restart. The YAML file only supplies the values of a database that has none
// yet; once an admin saves, the database wins (docs/configuration.md).
//
// Not here on purpose: how the mail server delivers (direct or smarthost). That
// stays in its own mail.yaml, because the internet-facing mail server has its
// own database account and reads nothing the portal writes except the outbox,
// and whoever edits this page should not be able to reroute every outgoing mail,
// sign-in codes included.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	netmail "net/mail"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
	"sharetoken/internal/mysqldb"
)

// Migrations is the schema of what only the portal uses, as component "portal".
var Migrations = []mysqldb.Migration{
	{Version: 1, Name: "settings, audit log, password resets, invite codes", SQL: `
CREATE TABLE IF NOT EXISTS portal_settings (
	name       VARCHAR(64) PRIMARY KEY,
	value      MEDIUMTEXT NOT NULL,
	updated_at BIGINT NOT NULL,
	updated_by VARCHAR(191) NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS admin_audit_log (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	at         BIGINT NOT NULL,
	actor_id   BIGINT NOT NULL DEFAULT 0,
	actor      VARCHAR(191) NOT NULL,
	action     VARCHAR(64) NOT NULL,
	target     VARCHAR(255) NOT NULL DEFAULT '',
	detail     TEXT NOT NULL,
	ip         VARCHAR(64) NOT NULL DEFAULT '',
	user_agent VARCHAR(255) NOT NULL DEFAULT '',
	status     INT NOT NULL DEFAULT 0,
	KEY audit_by_at (at),
	KEY audit_by_actor (actor, at),
	KEY audit_by_action (action, at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS password_resets (
	id          BIGINT PRIMARY KEY AUTO_INCREMENT,
	user_id     BIGINT NOT NULL,
	token_hash  VARBINARY(32) NOT NULL,
	ip          VARCHAR(64) NOT NULL DEFAULT '',
	created_at  BIGINT NOT NULL,
	expires_at  BIGINT NOT NULL,
	consumed_at BIGINT NOT NULL DEFAULT 0,
	UNIQUE KEY password_resets_by_hash (token_hash),
	KEY password_resets_by_user (user_id, created_at),
	KEY password_resets_by_time (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS invite_codes (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	code       VARCHAR(64) NOT NULL,
	note       VARCHAR(191) NOT NULL DEFAULT '',
	max_uses   BIGINT NOT NULL DEFAULT 1,
	used       BIGINT NOT NULL DEFAULT 0,
	expires_at BIGINT NOT NULL DEFAULT 0,
	disabled   BIGINT NOT NULL DEFAULT 0,
	created_by VARCHAR(191) NOT NULL DEFAULT '',
	created_at BIGINT NOT NULL,
	UNIQUE KEY invite_codes_by_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin
`},
	{Version: 2, Name: "relay metric history", SQL: `
CREATE TABLE IF NOT EXISTS relay_samples (
	relay   VARCHAR(191) NOT NULL,
	at      BIGINT NOT NULL,
	cpu     DOUBLE NOT NULL DEFAULT -1,
	rss     BIGINT NOT NULL DEFAULT 0,
	rx_bps  DOUBLE NOT NULL DEFAULT -1,
	tx_bps  DOUBLE NOT NULL DEFAULT -1,
	PRIMARY KEY (relay, at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`},
	{Version: 3, Name: "no passwords", SQL: `DROP TABLE IF EXISTS password_resets;`},
}

// Settings is everything the settings page edits.
type Settings struct {
	// Mail: what the portal writes into the outbox.
	MailFrom    string `json:"mail_from"`
	MailSupport string `json:"mail_support"`

	// Registration.
	OpenRegistration bool     `json:"open_registration"`  // an address without an account gets one at its first sign-in
	EmailDomainAllow []string `json:"email_domain_allow"` // example.com, *.edu.cn; empty allows any
	EmailBlock       []string `json:"email_block"`        // exact addresses
	InviteRequired   bool     `json:"invite_required"`

	// New users.
	SignupCreditMicros uint64 `json:"signup_credit_micros"`

	// Security.
	WebSessionHours int `json:"web_session_hours"` // 0: the configuration's value
	LoginTokenHours int `json:"login_token_hours"` // 0: the configuration's value

	// Interface.
	DefaultPageSize int `json:"default_page_size"`

	// API rate limits, requests per minute; 0 is off.
	RateUserRPM     int  `json:"rate_user_rpm"`   // per account, any signed-in endpoint
	RateHeavyRPM    int  `json:"rate_heavy_rpm"`  // per account: CSV exports, the overview's ledger replay
	RatePublicRPM   int  `json:"rate_public_rpm"` // per IP: endpoints that need no account
	RateAdminExempt bool `json:"rate_admin_exempt"`

	// Audit.
	AuditRetentionDays int `json:"audit_retention_days"` // 0 keeps everything
}

const (
	settingsKey      = "settings"
	settingsCacheTTL = 5 * time.Second
	defaultPageSize  = 20
	maxDomainRules   = 500
	maxBlockedEmails = 5000
	maxSettingsBody  = 512 << 10
)

var pageSizes = []int{10, 20, 50, 100}

// defaultSettings are the values of a database nobody has saved settings into.
func (s *Server) defaultSettings() Settings {
	hours := func(d time.Duration) int {
		if d%time.Hour != 0 {
			return 0
		}
		return int(d / time.Hour)
	}
	return Settings{
		MailFrom: s.cfg.Mail.From, MailSupport: s.cfg.Mail.Support,
		OpenRegistration: s.cfg.OpenRegistration,
		EmailDomainAllow: []string{}, EmailBlock: []string{},
		SignupCreditMicros: s.cfg.SignupCreditMicros,
		WebSessionHours:    hours(s.cfg.WebSessionTTL), LoginTokenHours: hours(s.cfg.LoginTTL),
		DefaultPageSize: defaultPageSize, RateAdminExempt: true, AuditRetentionDays: 365,
	}
}

// settingsCache keeps a read per request away from the database. A save here
// replaces it at once; a save by another process shows within settingsCacheTTL.
type settingsCache struct {
	mu   sync.Mutex
	val  *Settings
	read time.Time
}

// settings returns the settings in force. Their slices are shared: read only.
func (s *Server) settings() Settings {
	now := s.cfg.Now()
	s.setCache.mu.Lock()
	defer s.setCache.mu.Unlock()
	if s.setCache.val != nil && now.Sub(s.setCache.read) < settingsCacheTTL && !now.Before(s.setCache.read) {
		return *s.setCache.val
	}
	st, err := s.loadSettings()
	if err != nil {
		s.log.Warn("reading the settings", "err", err)
		if s.setCache.val != nil {
			return *s.setCache.val // the last good ones
		}
		return s.defaultSettings()
	}
	s.setCache.val, s.setCache.read = &st, now
	return st
}

func (s *Server) loadSettings() (Settings, error) {
	st := s.defaultSettings()
	var raw string
	err := s.db.QueryRow(`SELECT value FROM portal_settings WHERE name = ?`, settingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	// Over the defaults: a field added later keeps its default until saved.
	return st, json.Unmarshal([]byte(raw), &st)
}

func (s *Server) saveSettings(st Settings, by string) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO portal_settings (name, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = VALUES(updated_at), updated_by = VALUES(updated_by)`,
		settingsKey, string(raw), s.cfg.Now().UnixNano(), by)
	if err != nil {
		return err
	}
	s.setCache.mu.Lock()
	s.setCache.val, s.setCache.read = &st, s.cfg.Now()
	s.setCache.mu.Unlock()
	return nil
}

// ---- validation ----

var domainRuleRE = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// settingsError is a value an admin has to change.
type settingsError struct {
	field string
	msg   string
}

func (e *settingsError) Error() string { return e.field + ": " + e.msg }

// normalize cleans st and checks it; the first problem is returned.
func (st *Settings) normalize() error {
	bad := func(field, format string, a ...any) error { return &settingsError{field, fmt.Sprintf(format, a...)} }
	st.MailFrom = strings.TrimSpace(st.MailFrom)
	st.MailSupport = strings.TrimSpace(st.MailSupport)
	for field, a := range map[string]string{"mail_from": st.MailFrom, "mail_support": st.MailSupport} {
		if a == "" {
			continue
		}
		if _, err := netmail.ParseAddress(a); err != nil || strings.ContainsAny(a, "\r\n") {
			return bad(field, "must be an address like ShareToken <team@example.com>")
		}
	}
	rules := make([]string, 0, len(st.EmailDomainAllow))
	for _, d := range st.EmailDomainAllow {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || slices.Contains(rules, d) {
			continue
		}
		if !domainRuleRE.MatchString(d) || len(d) > 253 {
			return bad("email_domain_allow", "%q is not a domain or *.domain", d)
		}
		rules = append(rules, d)
	}
	if len(rules) > maxDomainRules {
		return bad("email_domain_allow", "at most %d rules", maxDomainRules)
	}
	st.EmailDomainAllow = rules
	seen := make(map[string]bool, len(st.EmailBlock))
	blocked := make([]string, 0, len(st.EmailBlock))
	for _, e := range st.EmailBlock {
		e = accounts.NormalizeEmail(e)
		if e == "" || seen[e] {
			continue
		}
		if err := accounts.CheckEmail(e); err != nil {
			return bad("email_block", "%q is not an email address", e)
		}
		seen[e] = true
		blocked = append(blocked, e)
	}
	if len(blocked) > maxBlockedEmails {
		return bad("email_block", "at most %d addresses", maxBlockedEmails)
	}
	st.EmailBlock = blocked
	if st.SignupCreditMicros > 1_000_000*1_000_000 {
		return bad("signup_credit_micros", "must be at most 1,000,000 credit")
	}
	if st.WebSessionHours < 0 || st.WebSessionHours > 24*90 {
		return bad("web_session_hours", "must be 1-2160")
	}
	if st.LoginTokenHours < 0 || st.LoginTokenHours > 24*3650 {
		return bad("login_token_hours", "must be 1-87600")
	}
	if !slices.Contains(pageSizes, st.DefaultPageSize) {
		return bad("default_page_size", "must be one of %v", pageSizes)
	}
	// A floor under each limit: one page load makes several requests, and a
	// limit below it would lock everybody out of the page that changes it.
	for _, r := range []struct {
		field    string
		v, floor int
	}{{"rate_user_rpm", st.RateUserRPM, 60}, {"rate_heavy_rpm", st.RateHeavyRPM, 1}, {"rate_public_rpm", st.RatePublicRPM, 30}} {
		if r.v != 0 && (r.v < r.floor || r.v > 1_000_000) {
			return bad(r.field, "must be 0 (off) or %d-1000000", r.floor)
		}
	}
	if d := st.AuditRetentionDays; d != 0 && (d < 7 || d > 36500) {
		return bad("audit_retention_days", "must be 0 (keep everything) or 7-36500")
	}
	return nil
}

// domainAllowed reports whether rules let an address of domain sign up. A rule
// *.edu.cn matches any subdomain of edu.cn, and not edu.cn itself.
func domainAllowed(rules []string, domain string) bool {
	if len(rules) == 0 {
		return true
	}
	for _, r := range rules {
		if suffix, wild := strings.CutPrefix(r, "*."); wild {
			if strings.HasSuffix(domain, "."+suffix) {
				return true
			}
		} else if domain == r {
			return true
		}
	}
	return false
}

// checkSignupRules applies the registration rules to name and, writing the error
// response itself, refuses a sign-up they exclude. The invite code is only
// looked at here: it is used up when the account is created.
func (s *Server) checkSignupRules(w http.ResponseWriter, ip, name, invite string, st Settings) bool {
	email := accounts.NormalizeEmail(name)
	if _, domain, ok := strings.Cut(email, "@"); ok {
		if slices.Contains(st.EmailBlock, email) || !domainAllowed(st.EmailDomainAllow, domain) {
			writeErr(w, http.StatusForbidden, "email_not_allowed", "this email address may not sign up here")
			return false
		}
	}
	if !st.InviteRequired {
		return true
	}
	if strings.TrimSpace(invite) == "" {
		writeErr(w, http.StatusForbidden, "invite_required", "a new account needs an invite code; if this app has no field for it, sign up on the web portal")
		return false
	}
	if s.throttled("invite:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong invite codes from this address; try again in a few minutes")
		return false
	}
	ok, err := s.inviteUsable(invite)
	if err != nil {
		s.internal(w, err)
		return false
	}
	if !ok {
		s.recordFailure("invite:" + ip)
		writeErr(w, http.StatusForbidden, "bad_invite", "this invite code is wrong, used up or expired")
		return false
	}
	return true
}

// ---- rate limits ----

// rateLimiter counts requests per key in fixed one-minute windows. It is per
// process, like the login throttle; there is one portal.
type rateLimiter struct {
	mu     sync.Mutex
	minute int64
	counts map[string]int
}

func (l *rateLimiter) allow(key string, rpm int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if m := now.Unix() / 60; m != l.minute || l.counts == nil {
		l.minute, l.counts = m, make(map[string]int)
	}
	l.counts[key]++
	return l.counts[key] <= rpm
}

func (s *Server) writeRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(60-s.cfg.Now().Unix()%60)))
	writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again in a minute")
}

// userAllowed applies a per-account limit, writing the refusal itself.
func (s *Server) userAllowed(w http.ResponseWriter, u *accounts.User, st Settings, class string, rpm int) bool {
	if rpm == 0 || (u.Admin && st.RateAdminExempt) || s.limiter.allow(class+":"+strconv.FormatInt(u.ID, 10), rpm, s.cfg.Now()) {
		return true
	}
	s.writeRateLimited(w)
	return false
}

// public applies the per-IP limit to an endpoint that needs no account.
func (s *Server) public(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rpm := s.settings().RatePublicRPM; rpm > 0 && !s.limiter.allow("ip:"+s.clientIP(r), rpm, s.cfg.Now()) {
			s.writeRateLimited(w)
			return
		}
		h(w, r)
	}
}

// ---- endpoints ----

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/admin/settings", s.admin(s.handleGetSettings))
	mux.Handle("PUT /api/v1/admin/settings", s.admin(s.handlePutSettings))
	mux.Handle("POST /api/v1/admin/settings/apply-blocklist", s.admin(s.handleApplyBlocklist))
	mux.Handle("POST /api/v1/admin/mail/test", s.admin(s.handleMailTest))
	mux.Handle("GET /api/v1/admin/invites", s.admin(s.handleInvites))
	mux.Handle("POST /api/v1/admin/invites", s.admin(s.handleNewInvite))
	mux.Handle("DELETE /api/v1/admin/invites/{id}", s.admin(s.handleDisableInvite))
}

// settingsView is the settings page's data: the settings, and what depends on
// things it cannot change.
type settingsView struct {
	Settings  Settings         `json:"settings"`
	Mail      *mailer.Overview `json:"mail,omitempty"`
	UpdatedAt int64            `json:"updated_at,omitempty"`
	UpdatedBy string           `json:"updated_by,omitempty"`
}

func (s *Server) settingsResponse() (*settingsView, error) {
	st := s.settings()
	v := &settingsView{Settings: st}
	if st.EmailDomainAllow == nil {
		v.Settings.EmailDomainAllow = []string{}
	}
	if st.EmailBlock == nil {
		v.Settings.EmailBlock = []string{}
	}
	if err := s.db.QueryRow(`SELECT updated_at, updated_by FROM portal_settings WHERE name = ?`, settingsKey).Scan(&v.UpdatedAt, &v.UpdatedBy); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if o, err := s.mail.GetOverview(); err == nil {
		v.Mail = o
	} else {
		s.log.Warn("reading the mail status", "err", err)
	}
	return v, nil
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	v, err := s.settingsResponse()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	st, err := s.loadSettings()
	if err != nil {
		s.internal(w, err)
		return
	}
	before := st
	// Over the current values: a field the request leaves out keeps its value.
	if !readJSON(w, r, &st) {
		return
	}
	if err := st.normalize(); err != nil {
		var bad *settingsError
		if errors.As(err, &bad) {
			var b struct {
				ErrorBody
				Field string `json:"field"`
			}
			b.Error.Code, b.Error.Message, b.Field = "bad_setting", err.Error(), bad.field
			writeJSON(w, http.StatusBadRequest, b)
			return
		}
		s.internal(w, err)
		return
	}
	if err := s.saveSettings(st, cu.user.Username); err != nil {
		s.internal(w, err)
		return
	}
	cu.note("changed", settingsDiff(before, st))
	s.log.Info("settings changed", "by", cu.user.Username)
	v, err := s.settingsResponse()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// settingsDiff says which fields changed, by their JSON names: what an audit
// entry records. Long values only say that they changed.
func settingsDiff(a, b Settings) map[string]any {
	out := map[string]any{}
	ta, va, vb := reflect.TypeOf(a), reflect.ValueOf(a), reflect.ValueOf(b)
	for i := range ta.NumField() {
		x, y := va.Field(i).Interface(), vb.Field(i).Interface()
		if reflect.DeepEqual(x, y) {
			continue
		}
		name, _, _ := strings.Cut(ta.Field(i).Tag.Get("json"), ",")
		switch {
		case ta.Field(i).Type.Kind() == reflect.Slice:
			xs, ys := x.([]string), y.([]string)
			var added, removed int
			for _, e := range ys {
				if !slices.Contains(xs, e) {
					added++
				}
			}
			for _, e := range xs {
				if !slices.Contains(ys, e) {
					removed++
				}
			}
			out[name] = fmt.Sprintf("+%d -%d", added, removed)
		default:
			out[name] = map[string]any{"from": x, "to": y}
		}
	}
	return out
}

// handleApplyBlocklist disables the accounts the email blocklist names. dry_run
// only says who they are. Administrators are never disabled this way: the list
// is bulk-edited, and a typo must not lock the console.
func (s *Server) handleApplyBlocklist(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	matched := []string{}
	admins := []string{}
	var ids []int64
	for _, e := range s.settings().EmailBlock {
		u, err := s.store.UserByName(e)
		switch {
		case errors.Is(err, accounts.ErrNotFound):
			continue
		case err != nil:
			s.internal(w, err)
			return
		case u.Admin:
			admins = append(admins, u.Username)
		case !u.Disabled:
			matched = append(matched, u.Username)
			ids = append(ids, u.ID)
		}
	}
	if !req.DryRun {
		for _, id := range ids {
			if err := s.store.SetDisabled(id, true); err != nil {
				s.accountErr(w, err)
				return
			}
		}
	}
	cu.note("dry_run", req.DryRun)
	cu.note("disabled", len(matched))
	writeJSON(w, http.StatusOK, map[string]any{"matched": matched, "skipped_admins": admins, "disabled": !req.DryRun})
}

// handleMailTest queues a test mail; the page follows its fate in the outbox.
func (s *Server) handleMailTest(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		To string `json:"to"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	st := s.settings()
	if st.MailFrom == "" {
		writeErr(w, http.StatusConflict, "mail_not_configured", "no mail sender is set")
		return
	}
	to := accounts.NormalizeEmail(req.To)
	if err := accounts.CheckEmail(to); err != nil {
		s.accountErr(w, err)
		return
	}
	id, err := s.mail.Enqueue(mailer.Message{Kind: mailer.KindTest, From: st.MailFrom, To: to, ReplyTo: s.supportAddress(),
		Subject: "ShareToken test email", CreatedBy: cu.user.Username,
		Text: "This is a test email from the settings page of the ShareToken admin console, sent by " + cu.user.Username + ".\r\nIf you can read it, mail delivery works. Check whether it landed in the inbox or in the spam folder.\r\n"})
	if err != nil {
		s.mailErr(w, err)
		return
	}
	cu.note("to", to)
	writeJSON(w, http.StatusOK, map[string]int64{"outbox_id": id})
}

// ---- invite codes ----

// Invite is a sign-up invite code.
type Invite struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Note      string `json:"note,omitempty"`
	MaxUses   int64  `json:"max_uses"` // 0: any number
	Used      int64  `json:"used"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // unix nanos
	Disabled  bool   `json:"disabled"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// newOpaqueCode makes an invite code: n bytes of randomness, base32.
func newOpaqueCode(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func normalizeInvite(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

const inviteLive = `disabled = 0 AND (max_uses = 0 OR used < max_uses) AND (expires_at = 0 OR expires_at > ?)`

func (s *Server) inviteUsable(code string) (bool, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM invite_codes WHERE code = ? AND `+inviteLive, normalizeInvite(code), s.cfg.Now().UnixNano()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// useInvite takes one use of a code. It reports false when none is left: another
// sign-up may have taken the last one since inviteUsable.
func (s *Server) useInvite(code string) (bool, error) {
	res, err := s.db.Exec(`UPDATE invite_codes SET used = used + 1 WHERE code = ? AND `+inviteLive, normalizeInvite(code), s.cfg.Now().UnixNano())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Server) returnInvite(code string) {
	if _, err := s.db.Exec(`UPDATE invite_codes SET used = used - 1 WHERE code = ? AND used > 0`, normalizeInvite(code)); err != nil {
		s.log.Warn("returning an invite code", "err", err)
	}
}

func (s *Server) handleInvites(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	rows, err := s.db.Query(`SELECT id, code, note, max_uses, used, expires_at, disabled, created_by, created_at FROM invite_codes ORDER BY id DESC LIMIT 500`)
	if err != nil {
		s.internal(w, err)
		return
	}
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		var i Invite
		var disabled int
		if err := rows.Scan(&i.ID, &i.Code, &i.Note, &i.MaxUses, &i.Used, &i.ExpiresAt, &disabled, &i.CreatedBy, &i.CreatedAt); err != nil {
			s.internal(w, err)
			return
		}
		i.Disabled = disabled != 0
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
}

func (s *Server) handleNewInvite(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Note     string `json:"note"`
		MaxUses  int64  `json:"max_uses"`
		TTLHours int64  `json:"ttl_hours"` // 0: never expires
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(req.Note) > 128 || req.MaxUses < 0 || req.MaxUses > 1_000_000 || req.TTLHours < 0 || req.TTLHours > 24*3650 {
		writeErr(w, http.StatusBadRequest, "bad_request", "note up to 128 characters, max_uses 0-1000000, ttl_hours 0-87600")
		return
	}
	code, err := newOpaqueCode(10)
	if err != nil {
		s.internal(w, err)
		return
	}
	now := s.cfg.Now()
	var exp int64
	if req.TTLHours > 0 {
		exp = now.Add(time.Duration(req.TTLHours) * time.Hour).UnixNano()
	}
	res, err := s.db.Exec(`INSERT INTO invite_codes (code, note, max_uses, expires_at, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		code, req.Note, req.MaxUses, exp, cu.user.Username, now.UnixNano())
	if err != nil {
		s.internal(w, err)
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		s.internal(w, err)
		return
	}
	cu.setTarget(strconv.FormatInt(id, 10))
	cu.note("max_uses", req.MaxUses)
	cu.note("note", req.Note)
	writeJSON(w, http.StatusCreated, Invite{ID: id, Code: code, Note: req.Note, MaxUses: req.MaxUses, ExpiresAt: exp, CreatedBy: cu.user.Username, CreatedAt: now.UnixNano()})
}

func (s *Server) handleDisableInvite(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := s.db.Exec(`UPDATE invite_codes SET disabled = 1 WHERE id = ?`, id)
	if err != nil {
		s.internal(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "not_found", "no such invite code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// dbContext bounds a write nothing should wait long for.
func dbContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
