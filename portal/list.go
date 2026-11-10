package portal

// Listings: one way to page, filter by date and export every table of the web
// pages. Query parameters:
//
//	page, page_size   a page, from 1; limit is another name for page_size
//	offset            instead of page
//	from, to          a date range in unix milliseconds; from inclusive, to exclusive
//	format=csv        the whole filtered listing as a CSV download
//
// The answer is {<key>: [...], total, page, page_size}.

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sharetoken/accounts"
	"sharetoken/coordinator"
	"sharetoken/ledger"
	"sharetoken/mailer"
)

const (
	defaultListSize = 50
	maxListSize     = 500
	// maxListOffset bounds a page's cost: reaching it reads every row before it.
	maxListOffset = 100_000
	// maxExportRows bounds an export, and so what the write timeout has to cover.
	maxExportRows = 50_000
	exportBatch   = 5000
	maxMillis     = 4_102_444_800_000 // 2100-01-01
)

// listParams is a parsed listing request.
type listParams struct {
	Page, Offset, Limit int
	From, To            int64 // unix nanos; 0 unbounded; To exclusive
	CSV                 bool
}

func (p listParams) to() int64 {
	if p.To <= 0 {
		return 1<<63 - 1
	}
	return p.To
}

// parseList reads the listing parameters, writing the error response itself.
func parseList(w http.ResponseWriter, r *http.Request) (listParams, bool) {
	q := r.URL.Query()
	p := listParams{Page: 1, Limit: defaultListSize, CSV: q.Get("format") == "csv"}
	num := func(key string) (int64, bool) {
		v := q.Get(key)
		if v == "" {
			return 0, true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "bad_request", "bad "+key)
			return 0, false
		}
		return n, true
	}
	for _, key := range []string{"limit", "page_size"} {
		n, ok := num(key)
		if !ok {
			return p, false
		}
		if n > 0 {
			p.Limit = int(min(n, maxListSize))
		}
	}
	page, ok := num("page")
	if !ok {
		return p, false
	}
	if page > maxListOffset {
		page = maxListOffset // past the limit below
	}
	if page > 1 {
		p.Page = int(page)
		p.Offset = (p.Page - 1) * p.Limit
	}
	offset, ok := num("offset")
	if !ok {
		return p, false
	}
	if offset > 0 {
		if offset > maxListOffset {
			offset = maxListOffset + 1
		}
		p.Offset = int(offset)
		p.Page = p.Offset/p.Limit + 1
	}
	if p.Offset > maxListOffset {
		writeErr(w, http.StatusBadRequest, "page_too_deep", "that page is too far in; narrow the date range")
		return p, false
	}
	for _, d := range []struct {
		key string
		out *int64
	}{{"from", &p.From}, {"to", &p.To}} {
		ms, ok := num(d.key)
		if !ok {
			return p, false
		}
		if ms > maxMillis {
			writeErr(w, http.StatusBadRequest, "bad_request", "bad "+d.key)
			return p, false
		}
		*d.out = ms * int64(time.Millisecond)
	}
	return p, true
}

// mailRange gives the date range in the unix seconds the mail tables keep, as
// [from, to): a to in the middle of a second keeps that second.
func (p listParams) mailRange() (from, to int64) {
	if p.From > 0 {
		from = p.From / int64(time.Second)
	}
	if p.To > 0 {
		to = (p.To + int64(time.Second) - 1) / int64(time.Second)
	}
	return from, to
}

// listing is one table: how to count it, read a window of it, and write it as CSV.
type listing[T any] struct {
	Key     string // the JSON field of the rows
	File    string // the download's name, also its audit action
	Count   func() (int64, error)
	Fetch   func(offset, limit int) ([]T, error)
	Columns []csvColumn[T]
	Extra   map[string]any // more fields of the JSON answer
}

// csvColumn is a column of an export. Trusted says Cell returns what the server
// formats itself (a number, a time); anything else may be text a user wrote.
type csvColumn[T any] struct {
	Title   string
	Cell    func(T) string
	Trusted bool
}

// serveList answers a listing request with a page or an export.
func serveList[T any](s *Server, w http.ResponseWriter, r *http.Request, cu ctxUser, p listParams, l listing[T]) {
	total, err := l.Count()
	if err != nil {
		s.internal(w, err)
		return
	}
	if p.CSV {
		exportList(s, w, r, cu, total, l)
		return
	}
	rows, err := l.Fetch(p.Offset, p.Limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	body := map[string]any{l.Key: rows, "total": total, "page": p.Page, "page_size": p.Limit}
	for k, v := range l.Extra {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
}

func exportList[T any](s *Server, w http.ResponseWriter, r *http.Request, cu ctxUser, total int64, l listing[T]) {
	st := s.settings()
	if !s.userAllowed(w, cu.user, st, "heavy", st.RateHeavyRPM) {
		return
	}
	if l.Columns == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "this listing cannot be exported")
		return
	}
	if total > maxExportRows {
		// Never a silently partial file: whoever opens it would take it for all of it.
		writeErr(w, http.StatusRequestEntityTooLarge, "export_too_large",
			fmt.Sprintf("%d rows match, an export is limited to %d; narrow the date range", total, maxExportRows))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
		// Bulk reads of other people's data are what an audit log is for.
		s.auditEvent(r, cu.user, actionExport+l.File, "", map[string]any{"rows": total, "query": r.URL.RawQuery})
	}
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.csv"`, l.File, s.cfg.Now().UTC().Format("20060102-150405")))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("\xef\xbb\xbf")) // a BOM: spreadsheets otherwise misread UTF-8
	out := csv.NewWriter(w)
	head := make([]string, len(l.Columns))
	for i, c := range l.Columns {
		head[i] = c.Title
	}
	_ = out.Write(head)
	for offset := 0; int64(offset) < total; offset += exportBatch {
		rows, err := l.Fetch(offset, exportBatch)
		if err != nil {
			// The status is gone; a cut-off body is all that is left to signal it with.
			s.log.Error("exporting a listing", "err", err, "file", l.File)
			panic(http.ErrAbortHandler)
		}
		for _, row := range rows {
			rec := make([]string, len(l.Columns))
			for i, c := range l.Columns {
				rec[i] = c.Cell(row)
				if !c.Trusted {
					rec[i] = csvText(rec[i])
				}
			}
			_ = out.Write(rec)
		}
		out.Flush()
		if len(rows) < exportBatch {
			break
		}
	}
	out.Flush()
}

// csvText keeps a spreadsheet from running text as a formula: usernames, memos
// and mail subjects are written by strangers, and an admin opens the file.
func csvText(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func csvTime(nanos int64) string {
	if nanos == 0 {
		return ""
	}
	return time.Unix(0, nanos).UTC().Format(time.RFC3339)
}

func csvSeconds(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func csvInt(n int64) string { return strconv.FormatInt(n, 10) }

// csvCredit writes micro-credits as an exact decimal number of credits.
func csvCredit(micros int64) string {
	sign := ""
	u := uint64(micros)
	if micros < 0 {
		sign, u = "-", uint64(-micros)
	}
	return fmt.Sprintf("%s%d.%06d", sign, u/1_000_000, u%1_000_000)
}

// ---- the listings ----

// ledgerListing reads an account's journal as the account holder sees it.
func (s *Server) ledgerListing(w http.ResponseWriter, r *http.Request, cu ctxUser, userID int64, file string) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	acct := ledger.UserAccount(userID)
	lo, hi, err := ledger.SeqRange(s.db, p.From, p.To)
	if err != nil {
		s.internal(w, err)
		return
	}
	if before := queryInt(r, "before", 0); before > 0 {
		hi = min(hi, before)
	}
	serveList(s, w, r, cu, p, listing[LedgerLine]{
		Key: "entries", File: file, Extra: map[string]any{"account": acct},
		Count: func() (int64, error) { return ledger.CountEntries(s.db, acct, lo, hi) },
		Fetch: func(offset, limit int) ([]LedgerLine, error) {
			lines := []LedgerLine{}
			err := s.ledgerRead(func(_ *sql.Tx, led *ledger.Tx) error {
				es, err := led.AccountEntries(acct, lo, hi, limit, offset)
				for _, e := range es {
					amt := e.Amount
					if e.Debit == acct {
						amt = -amt
					}
					lines = append(lines, LedgerLine{Seq: e.Seq, At: e.At, JobID: e.JobID, Amount: amt, Memo: e.Memo})
				}
				return err
			})
			return lines, err
		},
		Columns: []csvColumn[LedgerLine]{
			{"seq", func(l LedgerLine) string { return csvInt(l.Seq) }, true},
			{"time", func(l LedgerLine) string { return csvTime(l.At) }, true},
			{"amount_credit", func(l LedgerLine) string { return csvCredit(l.Amount) }, true},
			{"memo", func(l LedgerLine) string { return l.Memo }, false},
			{"job_id", func(l LedgerLine) string { return l.JobID }, false},
		},
	})
}

// jobsListing reads an account's jobs as the account holder sees them.
func (s *Server) jobsListing(w http.ResponseWriter, r *http.Request, cu ctxUser, userID int64, file string) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	role := r.URL.Query().Get("role")
	if role != "" && role != coordinator.RoleBuy && role != coordinator.RoleSell {
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown role")
		return
	}
	acct := ledger.UserAccount(userID)
	f := coordinator.JobQuery{Role: role, From: p.From, To: p.To}
	if before := queryInt(r, "before", 0); before > 0 && (f.To == 0 || before < f.To) {
		f.To = before
	}
	serveList(s, w, r, cu, p, listing[coordinator.JobInfo]{
		Key: "jobs", File: file,
		Count: func() (int64, error) { return coordinator.CountAccountJobs(s.db, acct, f) },
		Fetch: func(offset, limit int) ([]coordinator.JobInfo, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return coordinator.AccountJobs(s.db, acct, q)
		},
		Columns: []csvColumn[coordinator.JobInfo]{
			{"time", func(j coordinator.JobInfo) string { return csvTime(j.CreatedAt) }, true},
			{"job_id", func(j coordinator.JobInfo) string { return j.JobID }, false},
			{"role", func(j coordinator.JobInfo) string { return j.Role }, true},
			{"model", func(j coordinator.JobInfo) string { return j.Model }, false},
			{"status", func(j coordinator.JobInfo) string { return string(j.Status) }, true},
			{"prompt_tokens", func(j coordinator.JobInfo) string { return csvInt(int64(j.PromptTokens)) }, true},
			{"completion_tokens", func(j coordinator.JobInfo) string { return csvInt(int64(j.CompletionTokens)) }, true},
			{"cost_credit", func(j coordinator.JobInfo) string { return csvCredit(int64(j.CostMicros)) }, true},
			{"fee_credit", func(j coordinator.JobInfo) string { return csvCredit(int64(j.FeeMicros)) }, true},
			{"earned_credit", func(j coordinator.JobInfo) string { return csvCredit(int64(j.EarnedMicros)) }, true},
			{"finished", func(j coordinator.JobInfo) string { return csvTime(j.DoneAt) }, true},
		},
	})
}

func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	f := accounts.UserQuery{Q: r.URL.Query().Get("q"), From: p.From, To: p.To}
	serveList(s, w, r, cu, p, listing[accounts.UserRow]{
		Key: "users", File: "users",
		Count: func() (int64, error) { return s.store.UserCount(f) },
		Fetch: func(offset, limit int) ([]accounts.UserRow, error) { return s.store.SearchUsers(f, limit, offset) },
		Columns: []csvColumn[accounts.UserRow]{
			{"id", func(u accounts.UserRow) string { return csvInt(u.ID) }, true},
			{"username", func(u accounts.UserRow) string { return u.Username }, false},
			{"is_admin", func(u accounts.UserRow) string { return strconv.FormatBool(u.Admin) }, true},
			{"disabled", func(u accounts.UserRow) string { return strconv.FormatBool(u.Disabled) }, true},
			{"available_credit", func(u accounts.UserRow) string { return csvCredit(u.AvailableMicros) }, true},
			{"held_credit", func(u accounts.UserRow) string { return csvCredit(u.HeldMicros) }, true},
			{"devices", func(u accounts.UserRow) string { return csvInt(u.Devices) }, true},
			{"tokens", func(u accounts.UserRow) string { return csvInt(u.Tokens) }, true},
			{"created", func(u accounts.UserRow) string { return csvTime(u.CreatedAt) }, true},
		},
	})
}

func (s *Server) handleAdminJobs(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
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
	f := coordinator.JobQuery{Status: status, Acct: acct, From: p.From, To: p.To}
	if before := queryInt(r, "before", 0); before > 0 && (f.To == 0 || before < f.To) {
		f.To = before
	}
	serveList(s, w, r, cu, p, listing[coordinator.AdminJob]{
		Key: "jobs", File: "jobs",
		Count: func() (int64, error) { return coordinator.CountJobs(s.db, f) },
		Fetch: func(offset, limit int) ([]coordinator.AdminJob, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return coordinator.QueryJobs(s.db, q)
		},
		Columns: []csvColumn[coordinator.AdminJob]{
			{"time", func(j coordinator.AdminJob) string { return csvTime(j.CreatedAt) }, true},
			{"job_id", func(j coordinator.AdminJob) string { return j.JobID }, false},
			{"model", func(j coordinator.AdminJob) string { return j.Model }, false},
			{"status", func(j coordinator.AdminJob) string { return string(j.Status) }, true},
			{"buyer", func(j coordinator.AdminJob) string { return j.Buyer }, false},
			{"buyer_account", func(j coordinator.AdminJob) string { return j.BuyerAcct }, false},
			{"node", func(j coordinator.AdminJob) string { return j.Node }, false},
			{"node_account", func(j coordinator.AdminJob) string { return j.NodeAcct }, false},
			{"prompt_tokens", func(j coordinator.AdminJob) string { return csvInt(int64(j.PromptTokens)) }, true},
			{"completion_tokens", func(j coordinator.AdminJob) string { return csvInt(int64(j.CompletionTokens)) }, true},
			{"cost_credit", func(j coordinator.AdminJob) string { return csvCredit(int64(j.CostMicros)) }, true},
			{"fee_credit", func(j coordinator.AdminJob) string { return csvCredit(int64(j.FeeMicros)) }, true},
			{"dispute_outcome", func(j coordinator.AdminJob) string { return j.DisputeOutcome }, false},
			{"finished", func(j coordinator.AdminJob) string { return csvTime(j.DoneAt) }, true},
		},
	})
}

func (s *Server) handleAudits(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	f := coordinator.RangeQuery{From: p.From, To: p.To}
	serveList(s, w, r, cu, p, listing[coordinator.AuditRecord]{
		Key: "audits", File: "inference-audits",
		Count: func() (int64, error) { return coordinator.CountAudits(s.db, f) },
		Fetch: func(offset, limit int) ([]coordinator.AuditRecord, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return coordinator.QueryAuditsPage(s.db, q)
		},
		Columns: []csvColumn[coordinator.AuditRecord]{
			{"time", func(a coordinator.AuditRecord) string { return csvTime(a.At) }, true},
			{"job_id", func(a coordinator.AuditRecord) string { return a.JobID }, false},
			{"node", func(a coordinator.AuditRecord) string { return a.Node }, false},
			{"model", func(a coordinator.AuditRecord) string { return a.Model }, false},
			{"verdict", func(a coordinator.AuditRecord) string { return string(a.Verdict) }, false},
			{"similarity", func(a coordinator.AuditRecord) string { return strconv.FormatFloat(a.Similarity, 'f', 4, 64) }, true},
			{"detail", func(a coordinator.AuditRecord) string { return a.Detail }, false},
		},
	})
}

func (s *Server) handleDisputes(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	f := coordinator.RangeQuery{From: p.From, To: p.To}
	serveList(s, w, r, cu, p, listing[coordinator.DisputeInfo]{
		Key: "disputes", File: "disputes",
		Count: func() (int64, error) { return coordinator.CountDisputes(s.db, f) },
		Fetch: func(offset, limit int) ([]coordinator.DisputeInfo, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return coordinator.QueryDisputesPage(s.db, q)
		},
		Columns: []csvColumn[coordinator.DisputeInfo]{
			{"time", func(d coordinator.DisputeInfo) string { return csvTime(d.CreatedAt) }, true},
			{"job_id", func(d coordinator.DisputeInfo) string { return d.JobID }, false},
			{"model", func(d coordinator.DisputeInfo) string { return d.Model }, false},
			{"status", func(d coordinator.DisputeInfo) string { return string(d.Status) }, true},
			{"buyer_account", func(d coordinator.DisputeInfo) string { return d.BuyerAcct }, false},
			{"node_account", func(d coordinator.DisputeInfo) string { return d.NodeAcct }, false},
			{"cost_credit", func(d coordinator.DisputeInfo) string { return csvCredit(int64(d.Cost)) }, true},
			{"reason", func(d coordinator.DisputeInfo) string { return d.Reason }, false},
			{"outcome", func(d coordinator.DisputeInfo) string { return d.Outcome }, false},
			{"detail", func(d coordinator.DisputeInfo) string { return d.Detail }, false},
		},
	})
}

// handleJournal is the whole journal, or one account's, for operators.
func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	lo, hi, err := ledger.SeqRange(s.db, p.From, p.To)
	if err != nil {
		s.internal(w, err)
		return
	}
	if before := queryInt(r, "before", 0); before > 0 {
		hi = min(hi, before)
	}
	acct := r.URL.Query().Get("account")
	serveList(s, w, r, cu, p, listing[JournalLine]{
		Key: "entries", File: "journal",
		Count: func() (int64, error) { return ledger.CountEntries(s.db, acct, lo, hi) },
		Fetch: func(offset, limit int) ([]JournalLine, error) {
			query, args := `SELECT seq, at, job_id, debit, credit, amount, memo FROM ledger_entries WHERE seq >= ? AND seq < ? ORDER BY seq DESC LIMIT ? OFFSET ?`, []any{lo, hi, limit, offset}
			if acct != "" {
				query, args = ledger.AccountEntriesSQL, ledger.AccountEntriesArgs(acct, lo, hi, limit, offset)
			}
			rows, err := s.db.Query(query, args...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			lines := []JournalLine{}
			for rows.Next() {
				var l JournalLine
				if err := rows.Scan(&l.Seq, &l.At, &l.JobID, &l.Debit, &l.Credit, &l.Amount, &l.Memo); err != nil {
					return nil, err
				}
				lines = append(lines, l)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			rows.Close() // the pool may have one connection: see handleAccounts
			names := map[string]string{}
			for i := range lines {
				lines[i].FromUser = s.accountUser(names, lines[i].Debit)
				lines[i].ToUser = s.accountUser(names, lines[i].Credit)
			}
			return lines, nil
		},
		Columns: []csvColumn[JournalLine]{
			{"seq", func(l JournalLine) string { return csvInt(l.Seq) }, true},
			{"time", func(l JournalLine) string { return csvTime(l.At) }, true},
			{"from", func(l JournalLine) string { return l.Debit }, false},
			{"from_user", func(l JournalLine) string { return l.FromUser }, false},
			{"to", func(l JournalLine) string { return l.Credit }, false},
			{"to_user", func(l JournalLine) string { return l.ToUser }, false},
			{"amount_credit", func(l JournalLine) string { return csvCredit(l.Amount) }, true},
			{"memo", func(l JournalLine) string { return l.Memo }, false},
			{"job_id", func(l JournalLine) string { return l.JobID }, false},
		},
	})
}

func (s *Server) handleMailInbox(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := mailer.InFilter{Mailbox: q.Get("mailbox"), Q: q.Get("q"), UnreadOnly: q.Get("unread") == "1", OnlyBounces: q.Get("bounces") == "only"}
	f.From, f.To = p.mailRange()
	switch st := q.Get("status"); st {
	case "", mailer.InboxOpen:
		f.Status = mailer.InboxOpen
	case mailer.InboxArchived:
		f.Status = st
	case "all":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "bad status")
		return
	}
	serveList(s, w, r, cu, p, listing[mailer.InMail]{
		Key: "messages", File: "mail-inbox",
		Count: func() (int64, error) { return s.mail.CountInbox(f) },
		Fetch: func(offset, limit int) ([]mailer.InMail, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return s.mail.Inbox(q)
		},
		Columns: []csvColumn[mailer.InMail]{
			{"received", func(m mailer.InMail) string { return csvSeconds(m.ReceivedAt) }, true},
			{"mailbox", func(m mailer.InMail) string { return m.Mailbox }, false},
			{"from", func(m mailer.InMail) string { return m.From }, false},
			{"subject", func(m mailer.InMail) string { return m.Subject }, false},
			{"verified", func(m mailer.InMail) string { return strconv.FormatBool(m.Verified) }, true},
			{"bounce", func(m mailer.InMail) string { return strconv.FormatBool(m.IsBounce) }, true},
			{"size", func(m mailer.InMail) string { return csvInt(m.Size) }, true},
		},
	})
}

func (s *Server) handleMailOutbox(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := mailer.OutFilter{Status: q.Get("status"), Kind: q.Get("kind"), Q: q.Get("q")}
	f.From, f.To = p.mailRange()
	serveList(s, w, r, cu, p, listing[mailer.OutMail]{
		Key: "messages", File: "mail-outbox",
		Count: func() (int64, error) { return s.mail.CountOutbox(f) },
		Fetch: func(offset, limit int) ([]mailer.OutMail, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			msgs, err := s.mail.Outbox(q)
			for i := range msgs {
				maskSensitive(&msgs[i])
			}
			return msgs, err
		},
		Columns: []csvColumn[mailer.OutMail]{
			{"created", func(m mailer.OutMail) string { return csvSeconds(m.CreatedAt) }, true},
			{"to", func(m mailer.OutMail) string { return m.To }, false},
			{"subject", func(m mailer.OutMail) string { return m.Subject }, false},
			{"kind", func(m mailer.OutMail) string { return m.Kind }, true},
			{"status", func(m mailer.OutMail) string { return m.Status }, true},
			{"attempts", func(m mailer.OutMail) string { return csvInt(m.Attempts) }, true},
			{"last_error", func(m mailer.OutMail) string { return m.LastError }, false},
			{"created_by", func(m mailer.OutMail) string { return m.CreatedBy }, false},
		},
	})
}

func feedbackColumns() []csvColumn[accounts.Feedback] {
	return []csvColumn[accounts.Feedback]{
		{"time", func(f accounts.Feedback) string { return csvTime(f.CreatedAt) }, true},
		{"user", func(f accounts.Feedback) string { return f.Username }, false},
		{"category", func(f accounts.Feedback) string { return f.Category }, false},
		{"message", func(f accounts.Feedback) string { return f.Message }, false},
		{"source", func(f accounts.Feedback) string { return f.Source }, false},
		{"app_version", func(f accounts.Feedback) string { return f.AppVersion }, false},
		{"os", func(f accounts.Feedback) string { return f.OS }, false},
		{"lang", func(f accounts.Feedback) string { return f.Lang }, false},
		{"status", func(f accounts.Feedback) string { return f.Status }, false},
		{"handled_by", func(f accounts.Feedback) string { return f.HandledBy }, false},
	}
}

func (s *Server) handleMyFeedback(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	f := accounts.FeedbackQuery{UserID: cu.user.ID, From: p.From, To: p.To}
	serveList(s, w, r, cu, p, listing[accounts.Feedback]{
		Key: "feedback", File: "feedback",
		Count: func() (int64, error) { return s.store.CountFeedback(f) },
		Fetch: func(offset, limit int) ([]accounts.Feedback, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			fs, err := s.store.ListFeedback(q)
			for i := range fs {
				fs[i].HandledBy = "" // who handled it is internal
			}
			return fs, err
		},
	})
}

func (s *Server) handleAdminFeedback(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	waiting, err := s.store.NewFeedbackCount()
	if err != nil {
		s.internal(w, err)
		return
	}
	f := accounts.FeedbackQuery{Status: r.URL.Query().Get("status"), From: p.From, To: p.To}
	serveList(s, w, r, cu, p, listing[accounts.Feedback]{
		Key: "feedback", File: "feedback", Extra: map[string]any{"new": waiting}, Columns: feedbackColumns(),
		Count: func() (int64, error) { return s.store.CountFeedback(f) },
		Fetch: func(offset, limit int) ([]accounts.Feedback, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return s.store.ListFeedback(q)
		},
	})
}
