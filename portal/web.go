package portal

// The web front end: static pages served from the binary, browser sessions
// in an HttpOnly cookie, and the endpoints the pages need beyond the CLI
// API.
//
// Security model
//
//   - A browser session is an ordinary API token whose secret lives only in
//     an HttpOnly, SameSite=Strict (and, over TLS, Secure) cookie: page
//     scripts never see it, so a script injection cannot exfiltrate it.
//   - A cookie is only honoured on requests that carry the custom header
//     X-Sharetoken-Web (a cross-site page cannot add it without a CORS
//     preflight, which the portal never grants) and whose Origin, when the
//     browser sends one, is the portal itself. That blocks cross-site
//     request forgery, including login CSRF.
//   - Bearer tokens keep working as before; a cookie is never used as a
//     fallback for them.
//   - Every response carries a strict Content-Security-Policy (same-origin
//     scripts and styles only, no inline code, no framing), nosniff and a
//     no-referrer policy. The pages are plain HTML/JS with no third-party
//     code, and they build the DOM with textContent, never with HTML strings.
//   - Sign-ups are rate-limited per IP like failed logins.

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sharetoken/accounts"
	"sharetoken/coordinator"
	"sharetoken/ledger"
)

//go:embed web
var webFiles embed.FS

// Names and headers of the browser session.
const (
	SessionCookie = "st_session"
	WebHeader     = "X-Sharetoken-Web" // every cookie-authenticated request must carry it
	webTokenName  = "web session"
)

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.TLS != nil {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

// Info is what the pages need before anyone signs in.
type Info struct {
	RegistrationOpen bool   `json:"registration_open"`
	SignupCredit     uint64 `json:"signup_credit_micros"`
	SessionHours     int64  `json:"session_hours"`
	PassesEnabled    bool   `json:"passes_enabled"`
	// EmailVerification: sign-ups first ask POST /api/v1/register/code.
	EmailVerification bool   `json:"email_verification"`
	SupportEmail      string `json:"support_email,omitempty"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Info{RegistrationOpen: s.cfg.OpenRegistration, SignupCredit: s.cfg.SignupCreditMicros,
		SessionHours: int64(s.cfg.WebSessionTTL / time.Hour), PassesEnabled: s.cfg.PassKey != nil,
		EmailVerification: s.cfg.EmailVerification, SupportEmail: s.supportAddress()})
}

// ---- static pages ----

func (s *Server) servePages(mux *http.ServeMux) {
	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, sub, name)
		}
	}
	mux.HandleFunc("GET /{$}", page("index.html")) // the public landing page
	mux.HandleFunc("GET /app/{$}", page("app.html"))
	for _, from := range []string{"GET /app", "GET /login"} {
		mux.HandleFunc(from, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/app/", http.StatusFound)
		})
	}
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /admin/{$}", page("admin.html"))
	// Translations; a wildcard matches one path segment, so these stay under i18n/.
	mux.HandleFunc("GET /static/i18n/{bundle}/{file}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, sub, "i18n/"+r.PathValue("bundle")+"/"+r.PathValue("file"))
	})
	mux.HandleFunc("GET /static/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if strings.HasSuffix(name, ".html") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, sub, name)
	})
}

// ---- browser sessions ----

// sameOrigin reports whether a request is one of the portal's own pages
// talking to it: it carries WebHeader, and any Origin / Sec-Fetch-Site the
// browser attached point back at the portal.
func sameOrigin(r *http.Request) bool {
	if r.Header.Get(WebHeader) != "1" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "https" && u.Scheme != "http") {
			return false
		}
		if (u.Scheme == "https") != (r.TLS != nil) && !s3Proxied(r) {
			return false
		}
	}
	return true
}

// s3Proxied reports whether a TLS-terminating proxy sits in front of the
// portal (it then says so in X-Forwarded-Proto).
func s3Proxied(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// webOnly rejects requests that do not come from the portal's own pages.
func webOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "cross_site", "this endpoint is for the portal's own pages")
			return
		}
		h(w, r)
	}
}

// credential extracts the bearer token or, for the portal's own pages, the
// session cookie.
func (s *Server) credential(w http.ResponseWriter, r *http.Request) (string, bool) {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(tok), true
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return "", false
	}
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "cross_site", "session cookies are only accepted from the portal's own pages")
		return "", false
	}
	return c.Value, true
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	c := &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.cfg.SecureCookies || r.TLS != nil || s3Proxied(r)}
	if ttl > 0 {
		c.MaxAge = int(ttl / time.Second)
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *accounts.User, status int, mfa bool) {
	plain, _, err := s.store.IssueSessionToken(u.ID, webTokenName, s.cfg.WebSessionTTL, mfa)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	s.setSession(w, r, plain, s.cfg.WebSessionTTL)
	writeJSON(w, status, map[string]any{"user": u})
}

func (s *Server) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	u, mfa, ok := s.authenticate(w, r, c)
	if !ok {
		return
	}
	s.startSession(w, r, u, http.StatusOK, mfa)
}

func (s *Server) handleWebRegister(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	u, ok := s.register(w, r, c)
	if !ok {
		return
	}
	s.startSession(w, r, u, http.StatusCreated, false)
}

func (s *Server) handleWebLogout(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	_ = s.store.RevokeToken(cu.user.ID, cu.tokenID)
	s.setSession(w, r, "", 0)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- user endpoints beyond the CLI API ----

func (s *Server) handleRenameDevice(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.store.RenameDevice(cu.user.ID, r.PathValue("peer"), req.Name); err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- admin endpoints beyond the CLI API ----

func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	q := r.URL.Query().Get("q")
	us, err := s.store.SearchUsers(q, queryLimit(r), int(max(queryInt(r, "offset", 0), 0)))
	if err != nil {
		s.internal(w, err)
		return
	}
	total, err := s.store.UserCount(q)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": us, "total": total})
}

func (s *Server) handleSetAdmin(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	var req struct {
		Admin bool `json:"is_admin"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if u.ID == cu.user.ID && !req.Admin {
		writeErr(w, http.StatusBadRequest, "bad_request", "you cannot revoke your own admin rights")
		return
	}
	if err := s.store.SetAdmin(u.ID, req.Admin); err != nil {
		s.accountErr(w, err)
		return
	}
	s.log.Info("admin rights changed", "user", u.Username, "admin", req.Admin, "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminPassword(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	var req struct {
		New          string `json:"new_password"`
		RevokeTokens bool   `json:"revoke_tokens"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.store.SetPassword(u.ID, req.New); err != nil {
		s.accountErr(w, err)
		return
	}
	var revoked int64
	if req.RevokeTokens {
		var err error
		if revoked, err = s.store.RevokeAllTokens(u.ID); err != nil {
			s.internal(w, err)
			return
		}
	}
	s.log.Info("password reset by admin", "user", u.Username, "by", cu.user.Username, "tokens_revoked", revoked)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens_revoked": revoked})
}

func (s *Server) handleAdminTokens(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	ts, err := s.store.Tokens(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": ts})
}

func (s *Server) handleAdminRevokeToken(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad token id")
		return
	}
	if err := s.store.RevokeToken(u.ID, id); err != nil {
		s.accountErr(w, err)
		return
	}
	s.log.Info("token revoked by admin", "user", u.Username, "token", id, "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminRevokeAll(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	n, err := s.store.RevokeAllTokens(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.log.Info("all tokens revoked by admin", "user", u.Username, "count", n, "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens_revoked": n})
}

func (s *Server) handleAdminUnbind(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	if err := s.store.UnbindDevice(u.ID, r.PathValue("peer")); err != nil {
		s.accountErr(w, err)
		return
	}
	s.log.Info("device unbound by admin", "user", u.Username, "peer", r.PathValue("peer"), "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminUserLedger(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	s.writeLedger(w, r, u.ID)
}

func (s *Server) handleAdminUserJobs(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	jobs, err := coordinator.AccountJobs(s.db, ledger.UserAccount(u.ID), r.URL.Query().Get("role"), queryInt(r, "before", 0), queryLimit(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleUpdateRelay(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad relay id")
		return
	}
	var req struct{ Name, Region string }
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.store.UpdateRelay(id, req.Name, req.Region); err != nil {
		s.accountErr(w, err)
		return
	}
	s.log.Info("relay updated", "id", id, "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminJobs(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	q := r.URL.Query()
	status := q.Get("status")
	switch coordinator.Status(status) {
	case "", coordinator.StatusHeld, coordinator.StatusPending, coordinator.StatusDisputed, coordinator.StatusFinal, coordinator.StatusReleased, coordinator.StatusRefunded:
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown status")
		return
	}
	acct := q.Get("account")
	if name := q.Get("user"); name != "" {
		u, err := s.store.UserByName(name)
		if err != nil {
			s.accountErr(w, err)
			return
		}
		acct = ledger.UserAccount(u.ID)
	}
	jobs, err := coordinator.QueryJobs(s.db, status, acct, queryInt(r, "before", 0), queryLimit(r))
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	ns, err := coordinator.QueryNodes(s.db)
	if err != nil {
		s.internal(w, err)
		return
	}
	// Tell operators which user each node belongs to.
	type nodeRow struct {
		coordinator.NodeStats
		User string `json:"user,omitempty"`
	}
	out := make([]nodeRow, 0, len(ns))
	for _, n := range ns {
		row := nodeRow{NodeStats: n}
		_ = s.db.QueryRow(`SELECT u.username FROM devices d JOIN users u ON u.id = d.user_id WHERE d.peer = ?`, n.Node).Scan(&row.User)
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (s *Server) handleAudits(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	as, err := coordinator.QueryAudits(s.db, queryLimit(r))
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audits": as})
}

func (s *Server) handleDisputes(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	ds, err := coordinator.QueryDisputes(s.db, queryLimit(r))
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disputes": ds})
}

func (s *Server) handleResolveDispute(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Decision string `json:"decision"` // pay | refund
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Decision != "pay" && req.Decision != "refund" {
		writeErr(w, http.StatusBadRequest, "bad_request", "decision must be pay or refund")
		return
	}
	job := r.PathValue("job")
	err := coordinator.ResolveDispute(s.db, job, req.Decision == "pay", s.cfg.Now())
	switch {
	case err == nil:
		s.log.Info("dispute resolved", "job", job, "decision", req.Decision, "by", cu.user.Username)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, coordinator.ErrNotDisputed):
		writeErr(w, http.StatusConflict, "not_disputed", err.Error())
	case strings.Contains(err.Error(), "unknown job"):
		writeErr(w, http.StatusNotFound, "not_found", "unknown job")
	default:
		s.internal(w, err)
	}
}

// JournalLine is one ledger entry for operators.
type JournalLine struct {
	ledger.Entry
	FromUser string `json:"from_user,omitempty"`
	ToUser   string `json:"to_user,omitempty"`
}

func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	before, limit := queryInt(r, "before", 0), queryLimit(r)
	if before <= 0 {
		before = 1<<63 - 1
	}
	const cols = `seq, at, job_id, debit, credit, amount, memo`
	query, args := `SELECT `+cols+` FROM ledger_entries WHERE seq < ? ORDER BY seq DESC LIMIT ?`, []any{before, limit}
	if acct := r.URL.Query().Get("account"); acct != "" {
		// Two index-ordered scans merged (an OR of the two columns would sort
		// the account's whole history); UNION ALL is exact as debit != credit.
		const side = `SELECT ` + cols + ` FROM ledger_entries WHERE %s = ? AND seq < ? ORDER BY seq DESC LIMIT ?`
		query = `SELECT * FROM (` + fmt.Sprintf(side, "debit") + `) AS d UNION ALL SELECT * FROM (` + fmt.Sprintf(side, "credit") + `) AS c ORDER BY seq DESC LIMIT ?`
		args = []any{acct, before, limit, acct, before, limit, limit}
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		s.internal(w, err)
		return
	}
	defer rows.Close()
	lines := []JournalLine{}
	for rows.Next() {
		var l JournalLine
		if err := rows.Scan(&l.Seq, &l.At, &l.JobID, &l.Debit, &l.Credit, &l.Amount, &l.Memo); err != nil {
			s.internal(w, err)
			return
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, err)
		return
	}
	rows.Close() // single-connection pool: see handleAccounts
	names := map[string]string{}
	for i := range lines {
		lines[i].FromUser = s.accountUser(names, lines[i].Debit)
		lines[i].ToUser = s.accountUser(names, lines[i].Credit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": lines})
}

// accountUser names the user behind a user:<id> account, caching lookups.
func (s *Server) accountUser(cache map[string]string, acct string) string {
	if !strings.HasPrefix(acct, "user:") {
		return ""
	}
	if n, ok := cache[acct]; ok {
		return n
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(acct, "user:"), 10, 64)
	var name string
	if err == nil {
		if u, err := s.store.User(id); err == nil {
			name = u.Username
		}
	}
	cache[acct] = name
	return name
}

// AccountRow is one ledger account with its balance, for operators.
type AccountRow struct {
	Account string `json:"account"`
	User    string `json:"user,omitempty"`
	Balance int64  `json:"balance_micros"`
	Held    int64  `json:"held_micros"`
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	rows, err := s.db.Query(`SELECT a.account, a.balance, COALESCE((SELECT SUM(amount) FROM ledger_holds h WHERE h.owner = a.account AND h.open = 1), 0)
		FROM ledger_accounts a ORDER BY a.account`)
	if err != nil {
		s.internal(w, err)
		return
	}
	defer rows.Close()
	out := []AccountRow{}
	for rows.Next() {
		var a AccountRow
		if err := rows.Scan(&a.Account, &a.Balance, &a.Held); err != nil {
			s.internal(w, err)
			return
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, err)
		return
	}
	rows.Close() // the pool has one connection: finish reading before looking names up
	names := map[string]string{}
	for i := range out {
		out[i].User = s.accountUser(names, out[i].Account)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

// overviewExtras fills the counters the dashboard shows beyond the books.
func (s *Server) overviewExtras(o *Overview) error {
	counts, err := coordinator.JobCounts(s.db)
	if err != nil {
		return err
	}
	o.Jobs = map[string]int64{}
	for st, n := range counts {
		o.Jobs[string(st)] = n
	}
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(enabled), 0), COALESCE(SUM(enabled AND coordinator_ok), 0) FROM relays`).
		Scan(&o.Relays, &o.RelaysEnabled, &o.RelaysOK); err != nil {
		return err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE disabled = 1`).Scan(&o.DisabledUsers); err != nil {
		return err
	}
	var raw sql.NullString
	if err := s.db.QueryRow("SELECT value FROM meta WHERE `key` = 'fee_bps'").Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	o.FeeBps, _ = strconv.ParseInt(raw.String, 10, 64)
	return nil
}
