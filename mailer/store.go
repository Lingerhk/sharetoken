// Package mailer is sharetoken's email module: an outbox that any part of the
// platform writes to, a server (cmd/mailserver) that delivers it and receives
// mail for a few mailboxes, verification codes, and the stored inbox.
//
// Everything goes through the database. The portal never talks to the mail
// server: it inserts a row and the server's worker picks it up, so a mail
// server that is down delays mail but cannot fail a sign-up, and the
// internet-facing SMTP process holds no secrets of the portal. Its database
// account only needs the mail_outbox, mail_inbox and mail_status tables.
//
// Mail tables do not touch the ledger, so nothing here takes the database-wide
// write lock (mysqldb.Begin): every write is a single statement, and claims are
// compare-and-swap updates.
package mailer

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"sharetoken/internal/mysqldb"
)

// Schema creates the mail tables.
const Schema = `
CREATE TABLE IF NOT EXISTS mail_outbox (
	id              BIGINT PRIMARY KEY AUTO_INCREMENT,
	kind            VARCHAR(32) NOT NULL,
	has_secret      BIGINT NOT NULL DEFAULT 0,
	from_addr       VARCHAR(512) NOT NULL,
	to_addr         VARCHAR(320) NOT NULL,
	reply_to        VARCHAR(320) NOT NULL DEFAULT '',
	subject         VARCHAR(1024) NOT NULL,
	text_body       MEDIUMTEXT NOT NULL,
	html_body       MEDIUMTEXT NOT NULL,
	in_reply_to     VARCHAR(998) NOT NULL DEFAULT '',
	message_id      VARCHAR(255) NOT NULL DEFAULT '',
	created_by      VARCHAR(191) NOT NULL DEFAULT '',
	status          VARCHAR(16) NOT NULL,
	attempts        BIGINT NOT NULL DEFAULT 0,
	next_attempt_at BIGINT NOT NULL DEFAULT 0,
	expires_at      BIGINT NOT NULL DEFAULT 0,
	claimed_at      BIGINT NOT NULL DEFAULT 0,
	last_error      TEXT NOT NULL,
	remote          VARCHAR(255) NOT NULL DEFAULT '',
	created_at      BIGINT NOT NULL,
	finished_at     BIGINT NOT NULL DEFAULT 0,
	KEY mail_outbox_due (status, next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS mail_inbox (
	id          BIGINT PRIMARY KEY AUTO_INCREMENT,
	mailbox     VARCHAR(320) NOT NULL,
	mail_from   VARCHAR(512) NOT NULL DEFAULT '',
	from_hdr    VARCHAR(1024) NOT NULL DEFAULT '',
	subject     VARCHAR(1024) NOT NULL DEFAULT '',
	message_id  VARCHAR(998) NOT NULL DEFAULT '',
	remote_ip   VARCHAR(64) NOT NULL DEFAULT '',
	helo        VARCHAR(255) NOT NULL DEFAULT '',
	tls         BIGINT NOT NULL DEFAULT 0,
	spf         VARCHAR(32) NOT NULL DEFAULT '',
	dkim        VARCHAR(32) NOT NULL DEFAULT '',
	verified    BIGINT NOT NULL DEFAULT 0,
	is_bounce   BIGINT NOT NULL DEFAULT 0,
	size        BIGINT NOT NULL DEFAULT 0,
	raw         LONGBLOB NOT NULL,
	status      VARCHAR(16) NOT NULL DEFAULT 'inbox',
	read_at     BIGINT NOT NULL DEFAULT 0,
	replied_at  BIGINT NOT NULL DEFAULT 0,
	received_at BIGINT NOT NULL,
	KEY mail_inbox_by_status (status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS mail_codes (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	purpose    VARCHAR(32) NOT NULL,
	email      VARCHAR(191) NOT NULL,
	ip         VARCHAR(64) NOT NULL DEFAULT '',
	salt       VARBINARY(32) NOT NULL,
	code_hash  VARBINARY(64) NOT NULL,
	attempts   BIGINT NOT NULL DEFAULT 0,
	consumed   BIGINT NOT NULL DEFAULT 0,
	expires_at BIGINT NOT NULL,
	created_at BIGINT NOT NULL,
	KEY mail_codes_by_email (email, purpose, id),
	KEY mail_codes_by_ip (ip, created_at),
	KEY mail_codes_by_time (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS mail_status (
	name       VARCHAR(64) PRIMARY KEY,
	value      MEDIUMTEXT NOT NULL,
	updated_at BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Migrations is the schema of the mail tables, as component "mail".
var Migrations = []mysqldb.Migration{
	{Version: 1, Name: "initial schema", SQL: Schema},
}

// Migrate creates the mail tables. The portal runs it; the mail server's
// restricted database account cannot.
func Migrate(db *sql.DB) error { return mysqldb.Migrate(db, "mail", Migrations) }

// Outbox and inbox states.
const (
	StatusQueued    = "queued"
	StatusSending   = "sending"
	StatusSent      = "sent"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	InboxOpen     = "inbox"
	InboxArchived = "archived"
)

// Kinds of outgoing mail.
const (
	KindVerify  = "verify"
	KindNotice  = "notice"
	KindReply   = "reply"
	KindCompose = "compose"
	KindTest    = "test"
)

// ErrNotFound is returned for unknown or no longer applicable rows.
var ErrNotFound = errors.New("mailer: not found")

// Store reads and writes the mail tables.
type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// clean makes text safe for a utf8mb4 column of limited width.
func clean(s string, maxRunes int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	return s
}

// Message is an email to send.
type Message struct {
	Kind      string
	Sensitive bool
	From      string // "Name <a@b>"
	To        string
	ReplyTo   string
	Subject   string
	Text      string
	HTML      string
	InReplyTo string
	CreatedBy string
	ExpiresAt time.Time // zero: the server's retry window
}

// Enqueue stores a message for delivery and returns its id.
func (s *Store) Enqueue(m Message) (int64, error) {
	if m.From == "" || m.To == "" || m.Kind == "" {
		return 0, errors.New("mailer: message needs a kind, a sender and a recipient")
	}
	if strings.ContainsAny(m.To+m.From+m.ReplyTo+m.InReplyTo, "\r\n") {
		return 0, errors.New("mailer: line break in an address")
	}
	now := s.now().Unix()
	var exp int64
	if !m.ExpiresAt.IsZero() {
		exp = m.ExpiresAt.Unix()
	}
	res, err := s.DB.Exec(`INSERT INTO mail_outbox (kind, has_secret, from_addr, to_addr, reply_to, subject, text_body, html_body, in_reply_to,
		created_by, status, next_attempt_at, expires_at, last_error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		m.Kind, boolInt(m.Sensitive), clean(m.From, 500), clean(m.To, 320), clean(m.ReplyTo, 320), clean(m.Subject, 900), clean(m.Text, 1<<20), clean(m.HTML, 1<<20),
		clean(m.InReplyTo, 990), m.CreatedBy, StatusQueued, now, exp, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// OutMail is a row of the outbox.
type OutMail struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Sensitive  bool   `json:"sensitive"`
	From       string `json:"from"`
	To         string `json:"to"`
	ReplyTo    string `json:"reply_to,omitempty"`
	Subject    string `json:"subject"`
	Text       string `json:"text,omitempty"`
	HTML       string `json:"-"`
	InReplyTo  string `json:"in_reply_to,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	Status     string `json:"status"`
	Attempts   int64  `json:"attempts"`
	NextAt     int64  `json:"next_attempt_at"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	Remote     string `json:"remote,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

const outFields = `id, kind, has_secret, from_addr, to_addr, reply_to, subject, in_reply_to, message_id, created_by, status, attempts, next_attempt_at, expires_at, last_error, remote, created_at, finished_at`

func scanOut(row interface{ Scan(...any) error }, extra ...any) (*OutMail, error) {
	var m OutMail
	var sens int
	dest := []any{&m.ID, &m.Kind, &sens, &m.From, &m.To, &m.ReplyTo, &m.Subject, &m.InReplyTo, &m.MessageID, &m.CreatedBy, &m.Status,
		&m.Attempts, &m.NextAt, &m.ExpiresAt, &m.LastError, &m.Remote, &m.CreatedAt, &m.FinishedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	m.Sensitive = sens != 0
	return &m, nil
}

// OutFilter narrows Outbox. From and To bound created_at in unix seconds: From
// inclusive, To exclusive, 0 unbounded.
type OutFilter struct {
	Status, Kind, Q string
	From, To        int64
	Offset          int
	Limit           int
}

func (f OutFilter) where() (string, []any) {
	q := `1=1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.Q != "" {
		q += ` AND (LOWER(to_addr) LIKE ? ESCAPE '!' OR LOWER(subject) LIKE ? ESCAPE '!')`
		p := likePattern(f.Q)
		args = append(args, p, p)
	}
	if f.From > 0 {
		q += ` AND created_at >= ?`
		args = append(args, f.From)
	}
	if f.To > 0 {
		q += ` AND created_at < ?`
		args = append(args, f.To)
	}
	return q, args
}

// CountOutbox counts what Outbox pages through.
func (s *Store) CountOutbox(f OutFilter) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM mail_outbox WHERE `+where, args...).Scan(&n)
	return n, err
}

// Outbox lists outgoing mail, newest first.
func (s *Store) Outbox(f OutFilter) ([]OutMail, error) {
	where, args := f.where()
	rows, err := s.DB.Query(`SELECT `+outFields+` FROM mail_outbox WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, pageSize(f.Limit), f.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutMail{}
	for rows.Next() {
		m, err := scanOut(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// maxPageSize bounds one read: a page, or a batch of an export.
const maxPageSize = 5000

func pageSize(n int) int {
	if n <= 0 || n > maxPageSize {
		return 50
	}
	return n
}

func likePattern(q string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return "%" + r.Replace(strings.ToLower(strings.TrimSpace(q))) + "%"
}

// OutMessage reads one outgoing message with its body.
func (s *Store) OutMessage(id int64) (*OutMail, error) {
	var text, html string
	m, err := scanOut(s.DB.QueryRow(`SELECT `+outFields+`, text_body, html_body FROM mail_outbox WHERE id = ?`, id), &text, &html)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Text, m.HTML = text, html
	return m, nil
}

// Retry queues a failed or cancelled message again. Sensitive messages cannot
// be retried: their body is gone, and the user asks for a new code anyway.
func (s *Store) Retry(id int64) error {
	now := s.now().Unix()
	// A new retry window, or a message older than the old one would expire at once.
	res, err := s.DB.Exec(`UPDATE mail_outbox SET status = ?, next_attempt_at = ?, expires_at = ?, attempts = 0, last_error = '', finished_at = 0
		WHERE id = ? AND has_secret = 0 AND status IN (?, ?)`, StatusQueued, now, now+int64((48*time.Hour)/time.Second), id, StatusFailed, StatusCancelled)
	return mustAffect(res, err)
}

// Cancel stops a message that is still waiting to be sent.
func (s *Store) Cancel(id int64) error {
	res, err := s.DB.Exec(`UPDATE mail_outbox SET status = ?, finished_at = ? WHERE id = ? AND status = ?`, StatusCancelled, s.now().Unix(), id, StatusQueued)
	return mustAffect(res, err)
}

func mustAffect(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// claimDue marks up to limit due messages as being sent and returns them. Two
// servers on one database never take the same row.
func (s *Store) claimDue(limit int) ([]OutMail, error) {
	now := s.now().Unix()
	rows, err := s.DB.Query(`SELECT id FROM mail_outbox WHERE status = ? AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`, StatusQueued, now, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var out []OutMail
	for _, id := range ids {
		res, err := s.DB.Exec(`UPDATE mail_outbox SET status = ?, claimed_at = ?, attempts = attempts + 1 WHERE id = ? AND status = ?`, StatusSending, now, id, StatusQueued)
		if err != nil {
			return out, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		m, err := s.OutMessage(id)
		if err != nil {
			return out, err
		}
		out = append(out, *m)
	}
	return out, nil
}

// requeueStuck returns messages a crashed server left in "sending".
func (s *Store) requeueStuck(olderThan time.Duration) error {
	now := s.now()
	_, err := s.DB.Exec(`UPDATE mail_outbox SET status = ?, next_attempt_at = ? WHERE status = ? AND claimed_at < ?`,
		StatusQueued, now.Unix(), StatusSending, now.Add(-olderThan).Unix())
	return err
}

// wipedBody is what a finished sensitive message keeps of its subject and body:
// the code in them has no business staying in the database or in backups.
const wipedBody = "[removed after sending]"

func (s *Store) markSent(m *OutMail, messageID, remote string) error {
	return s.finish(m, StatusSent, messageID, remote, "")
}

func (s *Store) markFailed(m *OutMail, messageID, remote, errText string) error {
	return s.finish(m, StatusFailed, messageID, remote, errText)
}

func (s *Store) finish(m *OutMail, status, messageID, remote, errText string) error {
	q := `UPDATE mail_outbox SET status = ?, message_id = ?, remote = ?, last_error = ?, finished_at = ?`
	args := []any{status, messageID, clean(remote, 250), clean(errText, 2000), s.now().Unix()}
	if m.Sensitive {
		q += `, subject = ?, text_body = ?, html_body = ''`
		args = append(args, wipedBody, wipedBody)
	}
	_, err := s.DB.Exec(q+` WHERE id = ?`, append(args, m.ID)...)
	return err
}

func (s *Store) markRetry(m *OutMail, next time.Time, messageID, errText string) error {
	_, err := s.DB.Exec(`UPDATE mail_outbox SET status = ?, next_attempt_at = ?, message_id = ?, last_error = ? WHERE id = ?`,
		StatusQueued, next.Unix(), messageID, clean(errText, 2000), m.ID)
	return err
}

// purge removes finished outgoing mail past its retention.
func (s *Store) purge(keep time.Duration) error {
	_, err := s.DB.Exec(`DELETE FROM mail_outbox WHERE status IN (?, ?, ?) AND finished_at > 0 AND finished_at < ?`,
		StatusSent, StatusFailed, StatusCancelled, s.now().Add(-keep).Unix())
	return err
}

// ---- inbox ----

// InMail is a received message. Raw is only filled by InMessage.
type InMail struct {
	ID         int64  `json:"id"`
	Mailbox    string `json:"mailbox"`
	MailFrom   string `json:"mail_from"`
	From       string `json:"from"`
	Subject    string `json:"subject"`
	MessageID  string `json:"message_id,omitempty"`
	RemoteIP   string `json:"remote_ip"`
	Helo       string `json:"helo"`
	TLS        bool   `json:"tls"`
	SPF        string `json:"spf"`
	DKIM       string `json:"dkim"`
	Verified   bool   `json:"verified"`
	IsBounce   bool   `json:"is_bounce"`
	Size       int64  `json:"size"`
	Status     string `json:"status"`
	ReadAt     int64  `json:"read_at"`
	RepliedAt  int64  `json:"replied_at"`
	ReceivedAt int64  `json:"received_at"`
	Raw        []byte `json:"-"`
}

const inFields = `id, mailbox, mail_from, from_hdr, subject, message_id, remote_ip, helo, tls, spf, dkim, verified, is_bounce, size, status, read_at, replied_at, received_at`

func scanIn(row interface{ Scan(...any) error }, extra ...any) (*InMail, error) {
	var m InMail
	var t, v, b int
	dest := []any{&m.ID, &m.Mailbox, &m.MailFrom, &m.From, &m.Subject, &m.MessageID, &m.RemoteIP, &m.Helo, &t, &m.SPF, &m.DKIM, &v, &b, &m.Size, &m.Status, &m.ReadAt, &m.RepliedAt, &m.ReceivedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	m.TLS, m.Verified, m.IsBounce = t != 0, v != 0, b != 0
	return &m, nil
}

// storeInbound saves one received message.
func (s *Store) storeInbound(m *InMail) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO mail_inbox (mailbox, mail_from, from_hdr, subject, message_id, remote_ip, helo, tls, spf, dkim, verified, is_bounce, size, raw, status, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		clean(m.Mailbox, 320), clean(m.MailFrom, 500), clean(m.From, 1000), clean(m.Subject, 1000), clean(m.MessageID, 990), clean(m.RemoteIP, 64), clean(m.Helo, 250),
		boolInt(m.TLS), m.SPF, m.DKIM, boolInt(m.Verified), boolInt(m.IsBounce), int64(len(m.Raw)), m.Raw, InboxOpen, s.now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// inboxBytes is the stored size of the inbox, bounded by the cap on it.
func (s *Store) inboxBytes() (int64, error) {
	var n sql.NullInt64
	err := s.DB.QueryRow(`SELECT SUM(size) FROM mail_inbox`).Scan(&n)
	return n.Int64, err
}

// InFilter narrows Inbox. From and To bound received_at in unix seconds: From
// inclusive, To exclusive, 0 unbounded.
type InFilter struct {
	Mailbox, Status, Q string
	UnreadOnly         bool
	IncludeBounces     bool
	OnlyBounces        bool
	From, To           int64
	Offset             int
	Limit              int
}

func (f InFilter) where() (string, []any) {
	q := `1=1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Mailbox != "" {
		q += ` AND mailbox = ?`
		args = append(args, f.Mailbox)
	}
	if f.UnreadOnly {
		q += ` AND read_at = 0`
	}
	switch {
	case f.OnlyBounces:
		q += ` AND is_bounce = 1`
	case !f.IncludeBounces:
		q += ` AND is_bounce = 0`
	}
	if f.Q != "" {
		q += ` AND (LOWER(from_hdr) LIKE ? ESCAPE '!' OR LOWER(subject) LIKE ? ESCAPE '!')`
		p := likePattern(f.Q)
		args = append(args, p, p)
	}
	if f.From > 0 {
		q += ` AND received_at >= ?`
		args = append(args, f.From)
	}
	if f.To > 0 {
		q += ` AND received_at < ?`
		args = append(args, f.To)
	}
	return q, args
}

// CountInbox counts what Inbox pages through.
func (s *Store) CountInbox(f InFilter) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM mail_inbox WHERE `+where, args...).Scan(&n)
	return n, err
}

// Inbox lists received mail, newest first.
func (s *Store) Inbox(f InFilter) ([]InMail, error) {
	where, args := f.where()
	rows, err := s.DB.Query(`SELECT `+inFields+` FROM mail_inbox WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, pageSize(f.Limit), f.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InMail{}
	for rows.Next() {
		m, err := scanIn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// InMessage reads one received message with its raw bytes.
func (s *Store) InMessage(id int64) (*InMail, error) {
	var raw []byte
	m, err := scanIn(s.DB.QueryRow(`SELECT `+inFields+`, raw FROM mail_inbox WHERE id = ?`, id), &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Raw = raw
	return m, nil
}

// SetRead marks a message read or unread.
func (s *Store) SetRead(id int64, read bool) error {
	var at int64
	if read {
		at = s.now().Unix()
	}
	res, err := s.DB.Exec(`UPDATE mail_inbox SET read_at = ? WHERE id = ?`, at, id)
	return mustAffect(res, err)
}

// MarkReplied records that an administrator answered a message.
func (s *Store) MarkReplied(id int64) error {
	res, err := s.DB.Exec(`UPDATE mail_inbox SET replied_at = ? WHERE id = ?`, s.now().Unix(), id)
	return mustAffect(res, err)
}

// SetInboxStatus moves a message between the inbox and the archive.
func (s *Store) SetInboxStatus(id int64, status string) error {
	if status != InboxOpen && status != InboxArchived {
		return fmt.Errorf("mailer: unknown status %q", status)
	}
	res, err := s.DB.Exec(`UPDATE mail_inbox SET status = ? WHERE id = ?`, status, id)
	return mustAffect(res, err)
}

// DeleteInbound removes a received message for good.
func (s *Store) DeleteInbound(id int64) error {
	res, err := s.DB.Exec(`DELETE FROM mail_inbox WHERE id = ?`, id)
	return mustAffect(res, err)
}

// ---- status ----

// ServerStatus is what a running mail server reports about itself.
type ServerStatus struct {
	Version      string      `json:"version,omitempty"`
	Hostname     string      `json:"hostname"`
	Domain       string      `json:"domain"`
	SMTPListen   string      `json:"smtp_listen"`
	Mailboxes    []string    `json:"mailboxes"`
	Delivery     string      `json:"delivery"` // direct, or smarthost host:port
	DKIMSelector string      `json:"dkim_selector"`
	StartedAt    int64       `json:"started_at"`
	DNS          []DNSCheck  `json:"dns,omitempty"`
	DNSCheckedAt int64       `json:"dns_checked_at,omitempty"`
	Counters     SrvCounters `json:"counters"`
}

// SrvCounters counts SMTP activity since the server started.
type SrvCounters struct {
	Connections int64 `json:"connections"`
	Accepted    int64 `json:"accepted"`
	Rejected    int64 `json:"rejected"`
}

// DNSCheck is the result of checking one DNS record the server depends on.
type DNSCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func (s *Store) writeStatus(st ServerStatus) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	res, err := s.DB.Exec(`UPDATE mail_status SET value = ?, updated_at = ? WHERE name = 'server'`, string(b), now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.DB.Exec(`INSERT INTO mail_status (name, value, updated_at) VALUES ('server', ?, ?)`, string(b), now)
	return err
}

// ServerVersion reads the version the mail server last reported.
func (s *Store) ServerVersion() (string, error) {
	var value string
	if err := s.DB.QueryRow(`SELECT value FROM mail_status WHERE name = 'server'`).Scan(&value); err != nil {
		return "", err
	}
	var st ServerStatus
	if err := json.Unmarshal([]byte(value), &st); err != nil {
		return "", err
	}
	return st.Version, nil
}

// Overview is the mail section's dashboard.
type Overview struct {
	Online       bool             `json:"online"` // the server reported recently
	LastSeen     int64            `json:"last_seen,omitempty"`
	Server       *ServerStatus    `json:"server,omitempty"`
	Outbox       map[string]int64 `json:"outbox"`
	Unread       int64            `json:"unread"`
	Inbox        int64            `json:"inbox"`
	Archived     int64            `json:"archived"`
	Bounces      int64            `json:"bounces"`
	InboxBytes   int64            `json:"inbox_bytes"`
	CodesLastDay int64            `json:"codes_last_day"`
}

// heartbeatWindow is how old the last report may be for a server to count as up.
const heartbeatWindow = 90 * time.Second

// GetOverview gathers the dashboard.
func (s *Store) GetOverview() (*Overview, error) {
	now := s.now()
	o := &Overview{Outbox: map[string]int64{}}
	rows, err := s.DB.Query(`SELECT status, COUNT(*) FROM mail_outbox GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		o.Outbox[st] = n
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var bytes sql.NullInt64
	err = s.DB.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status = 'inbox' AND is_bounce = 0 AND read_at = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'inbox' AND is_bounce = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'archived' AND is_bounce = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(is_bounce), 0), SUM(size) FROM mail_inbox`).Scan(&o.Unread, &o.Inbox, &o.Archived, &o.Bounces, &bytes)
	if err != nil {
		return nil, err
	}
	o.InboxBytes = bytes.Int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM mail_codes WHERE created_at >= ?`, now.Add(-24*time.Hour).Unix()).Scan(&o.CodesLastDay); err != nil {
		return nil, err
	}
	var value string
	var at int64
	err = s.DB.QueryRow(`SELECT value, updated_at FROM mail_status WHERE name = 'server'`).Scan(&value, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		o.LastSeen = at
		o.Online = now.Sub(time.Unix(at, 0)) < heartbeatWindow
		var st ServerStatus
		if json.Unmarshal([]byte(value), &st) == nil {
			o.Server = &st
		}
	}
	return o, nil
}
