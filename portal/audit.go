package portal

// The administrators' audit log: who did what to which user or setting, and
// when. (Not GET /api/v1/admin/audits: those are the network's inference audits.)
//
// Recording lives in Server.admin, not in the handlers, so a new admin endpoint
// cannot forget it: every request that is not a read is logged, refused ones
// too, under the action auditActions gives its route. Handlers only add what the
// route does not say (cu.note), and never a password or a mail body. There is no
// endpoint that edits or deletes entries; only the retention setting removes them.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"sharetoken/accounts"
)

// auditActions names what each writing admin route does. "" leaves the route
// out of the log, for housekeeping nobody will ever ask about.
// TestEveryAdminRouteHasAnAuditAction keeps it complete.
var auditActions = map[string]string{
	"PUT /api/v1/admin/version-policy":                 "version_policy.set",
	"POST /api/v1/admin/users":                         "user.create",
	"POST /api/v1/admin/users/{name}/credit":           "user.credit",
	"POST /api/v1/admin/users/{name}/disable":          "user.disable",
	"POST /api/v1/admin/users/{name}/enable":           "user.enable",
	"POST /api/v1/admin/users/{name}/admin":            "user.set_admin",
	"POST /api/v1/admin/users/{name}/password":         "user.set_password",
	"POST /api/v1/admin/users/{name}/mfa/reset":        "user.mfa_reset",
	"DELETE /api/v1/admin/users/{name}/tokens/{id}":    "user.token_revoke",
	"POST /api/v1/admin/users/{name}/tokens/revoke":    "user.tokens_revoke_all",
	"DELETE /api/v1/admin/users/{name}/devices/{peer}": "user.device_unbind",
	"POST /api/v1/admin/relays":                        "relay.add",
	"PATCH /api/v1/admin/relays/{id}":                  "relay.update",
	"POST /api/v1/admin/relays/{id}/enable":            "relay.enable",
	"POST /api/v1/admin/relays/{id}/disable":           "relay.disable",
	"DELETE /api/v1/admin/relays/{id}":                 "relay.remove",
	"POST /api/v1/admin/disputes/{job}/resolve":        "dispute.resolve",
	"POST /api/v1/admin/mail/inbox/{id}/read":          "", // opening a message marks it read
	"POST /api/v1/admin/mail/inbox/read":               "mail.mark_read",
	"POST /api/v1/admin/mail/inbox/{id}/status":        "mail.move",
	"POST /api/v1/admin/mail/inbox/{id}/reply":         "mail.reply",
	"DELETE /api/v1/admin/mail/inbox/{id}":             "mail.delete",
	"POST /api/v1/admin/mail/outbox/{id}/retry":        "mail.retry",
	"POST /api/v1/admin/mail/outbox/{id}/cancel":       "mail.cancel",
	"POST /api/v1/admin/mail/send":                     "mail.send",
	"POST /api/v1/admin/mail/test":                     "mail.test",
	"POST /api/v1/admin/feedback/{id}/status":          "feedback.status",
	"PUT /api/v1/admin/settings":                       "settings.update",
	"POST /api/v1/admin/settings/apply-blocklist":      "settings.apply_blocklist",
	"POST /api/v1/admin/invites":                       "invite.create",
	"DELETE /api/v1/admin/invites/{id}":                "invite.disable",
}

// Actions recorded outside Server.admin.
const (
	actionAdminLogin    = "auth.admin_login"
	actionPasswordReset = "user.password_reset_by_mail"
	actionExport        = "export."
	auditPurgeEvery     = time.Hour
	auditPurgeBatch     = 5000
)

// auditNote is what a handler adds to its route's audit entry.
type auditNote struct {
	target string
	detail map[string]any
}

// note adds a detail to the audit entry of this request, if it has one.
func (cu ctxUser) note(key string, value any) {
	if cu.audit == nil {
		return
	}
	if cu.audit.detail == nil {
		cu.audit.detail = map[string]any{}
	}
	cu.audit.detail[key] = value
}

// setTarget names the entry's target where the path does not (a new user).
func (cu ctxUser) setTarget(target string) {
	if cu.audit != nil {
		cu.audit.target = target
	}
}

// statusWriter remembers the status a handler answered with, and the start of
// the body of an error answer, so the log can say why a request was refused.
type statusWriter struct {
	http.ResponseWriter
	status  int
	errBody []byte
}

const auditErrBody = 2048

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && len(w.errBody) < auditErrBody {
		w.errBody = append(w.errBody, b[:min(len(b), auditErrBody-len(w.errBody))]...)
	}
	return w.ResponseWriter.Write(b)
}

// refusal is why an error answer refused the request: its error code and
// message, or "" when the body is not one of ours.
func (w *statusWriter) refusal() string {
	var b ErrorBody
	if w.status < 400 || json.Unmarshal(w.errBody, &b) != nil || b.Error.Code == "" {
		return ""
	}
	if b.Error.Message == "" {
		return b.Error.Code
	}
	return b.Error.Code + ": " + b.Error.Message
}

// audited runs h and writes the audit entry of a writing admin request.
func (s *Server) audited(w http.ResponseWriter, r *http.Request, cu ctxUser, h authedHandler) {
	action, known := auditActions[r.Pattern]
	if !known {
		// Still logged: a route missing from the table must not go unrecorded.
		action = strings.ToLower(r.Pattern)
		s.log.Error("admin route without an audit action", "route", r.Pattern)
	}
	if action == "" {
		h(w, r, cu)
		return
	}
	cu.audit = &auditNote{}
	rec := &statusWriter{ResponseWriter: w}
	h(rec, r, cu)
	// Everything the path names is part of what was done: the first is the
	// target, the rest (the token of a user) go with the details.
	target := cu.audit.target
	for _, name := range []string{"name", "job", "peer", "id"} {
		v := r.PathValue(name)
		switch {
		case v == "":
		case target == "":
			target = v
		default:
			cu.note(name, v)
		}
	}
	if why := rec.refusal(); why != "" {
		cu.note("error", cut(why, 300))
	}
	s.writeAudit(r, cu.user.ID, cu.user.Username, action, target, cu.audit.detail, rec.status)
}

// auditEvent records something that happened outside an admin route.
func (s *Server) auditEvent(r *http.Request, u *accounts.User, action, target string, detail map[string]any) {
	s.writeAudit(r, u.ID, u.Username, action, target, detail, http.StatusOK)
}

// AuditCLI records what an operator did with the portal command on the server.
func (s *Server) AuditCLI(action, target string, detail map[string]any) {
	s.writeAudit(nil, 0, "cli", action, target, detail, http.StatusOK)
}

func cut(v string, maxRunes int) string {
	v = strings.ToValidUTF8(v, "�")
	if utf8.RuneCountInString(v) > maxRunes {
		v = string([]rune(v)[:maxRunes])
	}
	return v
}

// writeAudit appends an entry. What it records has already happened and cannot
// be undone from here, so a failure is logged at error level and not returned.
func (s *Server) writeAudit(r *http.Request, actorID int64, actor, action, target string, detail map[string]any, status int) {
	// The request itself: method and path, so an entry says exactly which call it was.
	if r != nil {
		if detail == nil {
			detail = map[string]any{}
		}
		detail["method"] = r.Method
		detail["path"] = cut(r.URL.Path, 200)
	}
	raw := []byte("{}")
	if len(detail) > 0 {
		var err error
		if raw, err = json.Marshal(detail); err != nil {
			raw = []byte("{}")
		}
	}
	var ip, ua string
	if r != nil {
		ip, ua = s.clientIP(r), r.UserAgent()
	}
	now := s.cfg.Now()
	ctx, cancel := dbContext()
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO admin_audit_log (at, actor_id, actor, action, target, detail, ip, user_agent, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		now.UnixNano(), actorID, cut(actor, 191), cut(action, 64), cut(target, 255), cut(string(raw), 8000), cut(ip, 64), cut(ua, 255), status); err != nil {
		s.log.Error("writing an audit entry", "err", err, "actor", actor, "action", action, "target", target)
		return
	}
	s.purgeAudit(now)
}

// purgeAudit removes entries past the retention setting, at most once an hour.
func (s *Server) purgeAudit(now time.Time) {
	days := s.settings().AuditRetentionDays
	s.auditPurgeMu.Lock()
	due := days > 0 && now.Sub(s.auditPurged) >= auditPurgeEvery
	if due {
		s.auditPurged = now
	}
	s.auditPurgeMu.Unlock()
	if !due {
		return
	}
	ctx, cancel := dbContext()
	defer cancel()
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).UnixNano()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin_audit_log WHERE at < ? ORDER BY at LIMIT ?`, cutoff, auditPurgeBatch); err != nil {
		s.log.Warn("purging the audit log", "err", err)
	}
}

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	ID        int64           `json:"id"`
	At        int64           `json:"at"` // unix nanos
	ActorID   int64           `json:"actor_id,omitempty"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Target    string          `json:"target,omitempty"`
	Detail    json.RawMessage `json:"detail"`
	IP        string          `json:"ip,omitempty"`
	UserAgent string          `json:"user_agent,omitempty"`
	Status    int             `json:"status"`
}

// handleAuditLog is GET /api/v1/admin/oplog: the log, newest first.
func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	where, args := `at >= ? AND at < ?`, []any{p.From, p.to()}
	if a := strings.ToLower(strings.TrimSpace(q.Get("actor"))); a != "" {
		where, args = where+` AND actor = ?`, append(args, a)
	}
	if a := strings.TrimSpace(q.Get("action")); a != "" {
		where, args = where+` AND action = ?`, append(args, a)
	}
	if t := strings.TrimSpace(q.Get("target")); t != "" {
		// A prefix of what is at most a short name: this does not need a pattern search.
		where, args = where+` AND target LIKE ? ESCAPE '!'`, append(args, likePrefix(t))
	}
	serveList(s, w, r, cu, p, listing[AuditEntry]{
		Key: "entries", File: "audit-log",
		Count: func() (int64, error) {
			var n int64
			err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_audit_log WHERE `+where, args...).Scan(&n)
			return n, err
		},
		Fetch: func(offset, limit int) ([]AuditEntry, error) {
			rows, err := s.db.Query(`SELECT id, at, actor_id, actor, action, target, detail, ip, user_agent, status FROM admin_audit_log WHERE `+where+` ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`,
				append(append([]any{}, args...), limit, offset)...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			out := []AuditEntry{}
			for rows.Next() {
				var e AuditEntry
				var detail string
				if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.Actor, &e.Action, &e.Target, &detail, &e.IP, &e.UserAgent, &e.Status); err != nil {
					return nil, err
				}
				e.Detail = json.RawMessage(detail)
				out = append(out, e)
			}
			return out, rows.Err()
		},
		Columns: []csvColumn[AuditEntry]{
			{"time", func(e AuditEntry) string { return csvTime(e.At) }, true},
			{"actor", func(e AuditEntry) string { return e.Actor }, false},
			{"action", func(e AuditEntry) string { return e.Action }, false},
			{"target", func(e AuditEntry) string { return e.Target }, false},
			{"status", func(e AuditEntry) string { return csvInt(int64(e.Status)) }, true},
			{"detail", func(e AuditEntry) string { return string(e.Detail) }, false},
			{"ip", func(e AuditEntry) string { return e.IP }, false},
			{"user_agent", func(e AuditEntry) string { return e.UserAgent }, false},
		},
	})
}

func likePrefix(q string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(q) + "%"
}
