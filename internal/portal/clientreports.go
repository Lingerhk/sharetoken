package portal

// Crash reports from the desktop client and the CLI (internal/diag): a
// client that crashed, or recovered from a panic, sends what happened,
// redacted, with the user's login. One row stands for one problem
// (fingerprint) of one user, version and process, and counts how often it
// was reported; the admin console lists them by problem.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/diag"
	"sharetoken/internal/redact"
)

// clientReportsSchema is portal migration 4.
const clientReportsSchema = `
CREATE TABLE IF NOT EXISTS client_reports (
	id             BIGINT PRIMARY KEY AUTO_INCREMENT,
	user_id        BIGINT NOT NULL,
	fingerprint    CHAR(16) NOT NULL,
	version        VARCHAR(64) NOT NULL,
	process        VARCHAR(16) NOT NULL,
	kind           VARCHAR(16) NOT NULL,
	env            VARCHAR(16) NOT NULL DEFAULT '',
	os             VARCHAR(16) NOT NULL DEFAULT '',
	arch           VARCHAR(16) NOT NULL DEFAULT '',
	os_version     VARCHAR(64) NOT NULL DEFAULT '',
	message        TEXT NOT NULL,
	stack          MEDIUMTEXT NOT NULL,
	context        MEDIUMTEXT NOT NULL,
	features       TEXT NOT NULL,
	count          BIGINT NOT NULL DEFAULT 1,
	last_report_id VARCHAR(32) NOT NULL,
	occurred_at    BIGINT NOT NULL,
	first_seen     BIGINT NOT NULL,
	last_seen      BIGINT NOT NULL,
	status         VARCHAR(16) NOT NULL DEFAULT 'new',
	note           VARCHAR(1000) NOT NULL DEFAULT '',
	handled_by     VARCHAR(191) NOT NULL DEFAULT '',
	UNIQUE KEY client_reports_one (user_id, fingerprint, version, process),
	KEY client_reports_by_fp (fingerprint, last_seen),
	KEY client_reports_by_seen (last_seen)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Limits and retention.
const (
	ClientReportsPerUserHour = 20
	ClientReportsPerIPHour   = 60
	ClientReportRetention    = 90 * 24 * time.Hour
	maxClientReportBody      = 160 << 10
)

// Report states, set by an admin for a whole problem (fingerprint).
const (
	ReportNew   = "new"
	ReportKnown = "known"
	ReportFixed = "fixed"
)

// hourWindow counts events per key over the last hour.
type hourWindow struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// take records an event for key if fewer than limit happened in the last
// hour, and reports whether it did.
func (h *hourWindow) take(key string, limit int, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits == nil || len(h.hits) > 100_000 {
		h.hits = map[string][]time.Time{}
	}
	ts := h.hits[key]
	for len(ts) > 0 && !ts[0].After(now.Add(-time.Hour)) {
		ts = ts[1:]
	}
	if len(ts) >= limit {
		h.hits[key] = ts
		return false
	}
	h.hits[key] = append(ts, now)
	return true
}

func (s *Server) clientReportRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/client-reports", s.clientOnly(s.auth(s.handleClientReport)))
	mux.Handle("GET /api/v1/admin/client-reports", s.admin(s.handleAdminClientReports))
	mux.Handle("GET /api/v1/admin/client-reports/{fp}", s.admin(s.handleAdminClientReport))
	mux.Handle("POST /api/v1/admin/client-reports/{fp}/status", s.admin(s.handleClientReportStatus))
}

func (s *Server) handleClientReport(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var rep diag.Report
	if !readJSON(w, r, &rep) {
		return
	}
	// The client redacted it; this is the second pass, on everything stored.
	rep.Redact(&redact.Redactor{})
	rep.Fingerprint = strings.ToLower(rep.Fingerprint)
	if err := rep.Check(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	now := s.cfg.Now()
	if !s.reportsByIP.take(s.rateIP(r), ClientReportsPerIPHour, now) || !s.reportsByUser.take(cu.user.Username, ClientReportsPerUserHour, now) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many crash reports; try again later")
		return
	}
	ctxJSON, _ := json.Marshal(rep.Context)
	featJSON, _ := json.Marshal(rep.Features)
	if rep.Features == nil {
		featJSON = []byte("{}")
	}
	at := now.UnixNano()
	// A report resent with the same id (its answer was lost) does not count again.
	// A problem marked fixed that comes back is open again.
	_, err := s.db.Exec(`INSERT INTO client_reports (user_id, fingerprint, version, process, kind, env, os, arch, os_version, message, stack, context, features,
			count, last_report_id, occurred_at, first_seen, last_seen, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, 'new') AS n
		ON DUPLICATE KEY UPDATE
			count = client_reports.count + IF(client_reports.last_report_id = n.last_report_id, 0, 1),
			kind = IF(n.kind = 'crash', 'crash', client_reports.kind),
			last_seen = n.last_seen, last_report_id = n.last_report_id, os_version = n.os_version, features = n.features,
			status = IF(client_reports.status = 'fixed', 'new', client_reports.status)`,
		cu.user.ID, rep.Fingerprint, rep.Version, rep.Process, rep.Kind, rep.Env, rep.OS, rep.Arch, rep.OSVersion, rep.Message, rep.Stack, string(ctxJSON), string(featJSON),
		rep.ID, rep.OccurredAt, at, at)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.log.Info("client crash report", "user", cu.user.Username, "kind", rep.Kind, "process", rep.Process, "version", rep.Version, "fingerprint", rep.Fingerprint)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// ClientProblem is one fingerprint across users and versions.
type ClientProblem struct {
	Fingerprint string   `json:"fingerprint"`
	Kind        string   `json:"kind"` // crash if any report of it was one
	Process     string   `json:"process"`
	Message     string   `json:"message"`
	Count       int64    `json:"count"`
	Users       int64    `json:"users"`
	Versions    []string `json:"versions"`
	OSes        []string `json:"oses"`
	FirstSeen   int64    `json:"first_seen"` // unix nanos
	LastSeen    int64    `json:"last_seen"`
	Status      string   `json:"status"` // new while any row is new, else known, else fixed
	Note        string   `json:"note,omitempty"`
	HandledBy   string   `json:"handled_by,omitempty"`
}

// ClientReportRow is one user's reports of a problem on one version.
type ClientReportRow struct {
	Username   string            `json:"username"`
	Version    string            `json:"version"`
	Process    string            `json:"process"`
	Kind       string            `json:"kind"`
	Env        string            `json:"env,omitempty"`
	OS         string            `json:"os"`
	Arch       string            `json:"arch"`
	OSVersion  string            `json:"os_version,omitempty"`
	Count      int64             `json:"count"`
	OccurredAt int64             `json:"occurred_at"` // unix seconds, the client's clock
	FirstSeen  int64             `json:"first_seen"`
	LastSeen   int64             `json:"last_seen"`
	Features   map[string]string `json:"features,omitempty"`
}

// ClientProblemDetail is a problem with its rows and its latest report's text.
type ClientProblemDetail struct {
	ClientProblem
	Rows    []ClientReportRow `json:"rows"`
	Stack   string            `json:"stack"`
	Context []string          `json:"context"`
}

type problemQuery struct {
	status, q string
	from, to  int64
}

func (p problemQuery) having() (string, []any) {
	to := p.to
	if to <= 0 {
		to = 1<<63 - 1
	}
	where, args := `WHERE r.last_seen >= ? AND r.last_seen < ?`, []any{p.from, to}
	if p.q != "" {
		where += ` AND (r.fingerprint = ? OR r.message LIKE ? OR r.version = ?)`
		args = append(args, p.q, "%"+likeEscape(p.q)+"%", p.q)
	}
	having := ""
	switch p.status {
	case ReportNew:
		having = ` HAVING SUM(r.status = 'new') > 0`
	case ReportKnown:
		having = ` HAVING SUM(r.status = 'new') = 0 AND SUM(r.status = 'known') > 0`
	case ReportFixed:
		having = ` HAVING SUM(r.status <> 'fixed') = 0`
	}
	return where + ` GROUP BY r.fingerprint` + having, args
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

const problemColumns = `r.fingerprint, IF(SUM(r.kind = 'crash') > 0, 'crash', 'panic'), ANY_VALUE(r.process), ANY_VALUE(r.message), SUM(r.count), COUNT(DISTINCT r.user_id),
	GROUP_CONCAT(DISTINCT r.version ORDER BY r.version SEPARATOR ','), GROUP_CONCAT(DISTINCT r.os ORDER BY r.os SEPARATOR ','), MIN(r.first_seen), MAX(r.last_seen),
	IF(SUM(r.status = 'new') > 0, 'new', IF(SUM(r.status = 'known') > 0, 'known', 'fixed')), MAX(r.note), MAX(r.handled_by)`

func scanProblem(sc interface{ Scan(...any) error }) (ClientProblem, error) {
	var p ClientProblem
	var versions, oses string
	err := sc.Scan(&p.Fingerprint, &p.Kind, &p.Process, &p.Message, &p.Count, &p.Users, &versions, &oses, &p.FirstSeen, &p.LastSeen, &p.Status, &p.Note, &p.HandledBy)
	p.Versions, p.OSes = splitList(versions), splitList(oses)
	return p, err
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func (s *Server) countProblems(q problemQuery) (int64, error) {
	tail, args := q.having()
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT r.fingerprint FROM client_reports r `+tail+`) t`, args...).Scan(&n)
	return n, err
}

func (s *Server) listProblems(q problemQuery, offset, limit int) ([]ClientProblem, error) {
	tail, args := q.having()
	rows, err := s.db.Query(`SELECT `+problemColumns+` FROM client_reports r `+tail+` ORDER BY MAX(r.last_seen) DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientProblem{}
	for rows.Next() {
		p, err := scanProblem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Server) handleAdminClientReports(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	q := problemQuery{status: r.URL.Query().Get("status"), q: strings.TrimSpace(r.URL.Query().Get("q")), from: p.From, to: p.To}
	waiting, err := s.countProblems(problemQuery{status: ReportNew})
	if err != nil {
		s.internal(w, err)
		return
	}
	serveList(s, w, r, cu, p, listing[ClientProblem]{
		Key: "problems", File: "client-reports", Extra: map[string]any{"new": waiting},
		Count: func() (int64, error) { return s.countProblems(q) },
		Fetch: func(offset, limit int) ([]ClientProblem, error) { return s.listProblems(q, offset, limit) },
		Columns: []csvColumn[ClientProblem]{
			{"last_seen", func(c ClientProblem) string { return csvTime(c.LastSeen) }, true},
			{"first_seen", func(c ClientProblem) string { return csvTime(c.FirstSeen) }, true},
			{"fingerprint", func(c ClientProblem) string { return c.Fingerprint }, true},
			{"kind", func(c ClientProblem) string { return c.Kind }, true},
			{"process", func(c ClientProblem) string { return c.Process }, true},
			{"message", func(c ClientProblem) string { return c.Message }, false},
			{"count", func(c ClientProblem) string { return csvInt(c.Count) }, true},
			{"users", func(c ClientProblem) string { return csvInt(c.Users) }, true},
			{"versions", func(c ClientProblem) string { return strings.Join(c.Versions, " ") }, false},
			{"oses", func(c ClientProblem) string { return strings.Join(c.OSes, " ") }, false},
			{"status", func(c ClientProblem) string { return c.Status }, true},
			{"note", func(c ClientProblem) string { return c.Note }, false},
		},
	})
}

var fpParamRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (s *Server) handleAdminClientReport(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	fp := r.PathValue("fp")
	if !fpParamRE.MatchString(fp) {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad fingerprint")
		return
	}
	d, err := s.clientProblem(fp)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not_found", "no such problem")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) clientProblem(fp string) (*ClientProblemDetail, error) {
	p, err := scanProblem(s.db.QueryRow(`SELECT `+problemColumns+` FROM client_reports r WHERE r.fingerprint = ? GROUP BY r.fingerprint`, fp))
	if err != nil {
		return nil, err
	}
	d := &ClientProblemDetail{ClientProblem: p, Rows: []ClientReportRow{}, Context: []string{}}
	rows, err := s.db.Query(`SELECT u.username, r.version, r.process, r.kind, r.env, r.os, r.arch, r.os_version, r.count, r.occurred_at, r.first_seen, r.last_seen, r.features
		FROM client_reports r JOIN users u ON u.id = r.user_id WHERE r.fingerprint = ? ORDER BY r.last_seen DESC LIMIT 200`, fp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row ClientReportRow
		var feat string
		if err := rows.Scan(&row.Username, &row.Version, &row.Process, &row.Kind, &row.Env, &row.OS, &row.Arch, &row.OSVersion, &row.Count, &row.OccurredAt, &row.FirstSeen, &row.LastSeen, &feat); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(feat), &row.Features)
		d.Rows = append(d.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var ctxJSON string
	err = s.db.QueryRow(`SELECT stack, context FROM client_reports WHERE fingerprint = ? ORDER BY last_seen DESC LIMIT 1`, fp).Scan(&d.Stack, &ctxJSON)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(ctxJSON), &d.Context)
	return d, nil
}

func (s *Server) handleClientReportStatus(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	fp := r.PathValue("fp")
	if !fpParamRE.MatchString(fp) {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad fingerprint")
		return
	}
	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Status != ReportNew && req.Status != ReportKnown && req.Status != ReportFixed {
		writeErr(w, http.StatusBadRequest, "bad_request", "status must be new, known or fixed")
		return
	}
	req.Note = redact.Truncate(strings.TrimSpace(req.Note), 1000)
	cu.note("status", req.Status)
	by := cu.user.Username
	if req.Status == ReportNew {
		by = ""
	}
	res, err := s.db.Exec(`UPDATE client_reports SET status = ?, note = ?, handled_by = ? WHERE fingerprint = ?`, req.Status, req.Note, by, fp)
	if err != nil {
		s.internal(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if s.db.QueryRow(`SELECT 1 FROM client_reports WHERE fingerprint = ? LIMIT 1`, fp).Scan(&one) != nil {
			writeErr(w, http.StatusNotFound, "not_found", "no such problem")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pruneClientReports deletes the rows nobody reported for ClientReportRetention.
func (s *Server) pruneClientReports() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM client_reports WHERE last_seen < ?`, s.cfg.Now().Add(-ClientReportRetention).UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Server) runReportPrune(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		if n, err := s.pruneClientReports(); err != nil {
			s.log.Warn("pruning old crash reports failed", "err", err)
		} else if n > 0 {
			s.log.Info("pruned old crash reports", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// DashCrashes is the dashboard's crash summary: rows reported in the last 7
// days, by version.
type DashCrashes struct {
	Problems  int64              `json:"problems"` // distinct fingerprints
	New       int64              `json:"new"`
	Reports   int64              `json:"reports"`
	Users     int64              `json:"users"`
	ByVersion []DashCrashVersion `json:"by_version"`
}

type DashCrashVersion struct {
	Version string `json:"version"`
	Reports int64  `json:"reports"`
	Users   int64  `json:"users"`
}

func (s *Server) dashCrashes() (DashCrashes, error) {
	since := s.cfg.Now().Add(-7 * 24 * time.Hour).UnixNano()
	var d DashCrashes
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT fingerprint), COUNT(DISTINCT IF(status = 'new', fingerprint, NULL)), COALESCE(SUM(count), 0), COUNT(DISTINCT user_id)
		FROM client_reports WHERE last_seen >= ?`, since).Scan(&d.Problems, &d.New, &d.Reports, &d.Users)
	if err != nil {
		return d, err
	}
	rows, err := s.db.Query(`SELECT version, SUM(count), COUNT(DISTINCT user_id) FROM client_reports WHERE last_seen >= ? GROUP BY version ORDER BY SUM(count) DESC LIMIT 10`, since)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	d.ByVersion = []DashCrashVersion{}
	for rows.Next() {
		var v DashCrashVersion
		if err := rows.Scan(&v.Version, &v.Reports, &v.Users); err != nil {
			return d, err
		}
		d.ByVersion = append(d.ByVersion, v)
	}
	return d, rows.Err()
}
