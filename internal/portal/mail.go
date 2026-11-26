package portal

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	netmail "net/mail"
	"strconv"
	"strings"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
)

// MailConfig connects the portal to the mail module. The portal only writes to
// the outbox; cmd/mailserver delivers.
type MailConfig struct {
	From    string // sender of codes and notices, "ShareToken <team@sharetoken.app>"
	Support string // where users write to and admins answer from, "ShareToken Support <support@…>"
}

func (s *Server) mailEnabled() bool { return s.settings().MailFrom != "" }

// supportAddress is the bare address users are told to write to.
func (s *Server) supportAddress() string {
	if a, err := netmail.ParseAddress(s.settings().MailSupport); err == nil {
		return a.Address
	}
	return ""
}

// notify queues a notice to a user whose account name is an email address. A
// failure must not fail what triggered it: it is logged.
func (s *Server) notify(m mailer.Message) {
	if !s.mailEnabled() || !strings.Contains(m.To, "@") {
		return
	}
	m.From, m.ReplyTo = s.settings().MailFrom, s.supportAddress()
	if _, err := s.mail.Enqueue(m); err != nil {
		s.log.Warn("queueing a notice", "err", err, "user", m.To)
	}
}

// Notices one account makes another receive (a request to approve, an
// approval) are capped by who receives them and by who caused them: asking
// with many codes, or from many accounts, must not turn the portal into a way
// to flood a mailbox. What is over a cap is not mailed; the request itself
// still shows on the owner's page.
const (
	noticesPerRecipientHour = 6
	noticesPerRecipientDay  = 20
	noticesPerSenderHour    = 10
	noticesPerSenderDay     = 30
)

// notifyFrom is notify for a notice that account by caused to be sent to
// someone else.
func (s *Server) notifyFrom(by int64, m mailer.Message) {
	if !s.mailEnabled() || !strings.Contains(m.To, "@") {
		return
	}
	to, from := strings.ToLower(m.To), strconv.FormatInt(by, 10)
	if _, refused := s.reserve(
		failLimit{"mail:to:" + to, noticesPerRecipientHour}, failLimit{"mail:day:to:" + to, noticesPerRecipientDay},
		failLimit{"mail:by:" + from, noticesPerSenderHour}, failLimit{"mail:day:by:" + from, noticesPerSenderDay},
	); refused >= 0 {
		s.log.Warn("notice not mailed: over the limit", "by", by, "kind", m.Kind, "limit", refused)
		return
	}
	s.notify(m)
}

// ---- admin ----

func (s *Server) mailRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/admin/mail/status", s.admin(s.handleMailStatus))
	mux.Handle("GET /api/v1/admin/mail/inbox", s.admin(s.handleMailInbox))
	mux.Handle("GET /api/v1/admin/mail/inbox/{id}", s.admin(s.handleMailMessage))
	mux.Handle("GET /api/v1/admin/mail/inbox/{id}/raw", s.admin(s.handleMailRaw))
	mux.Handle("GET /api/v1/admin/mail/inbox/{id}/attachments/{n}", s.admin(s.handleMailAttachment))
	mux.Handle("POST /api/v1/admin/mail/inbox/read", s.admin(s.handleMailReadMany))
	mux.Handle("POST /api/v1/admin/mail/inbox/{id}/read", s.admin(s.handleMailRead))
	mux.Handle("POST /api/v1/admin/mail/inbox/{id}/status", s.admin(s.handleMailMove))
	mux.Handle("POST /api/v1/admin/mail/inbox/{id}/reply", s.admin(s.handleMailReply))
	mux.Handle("DELETE /api/v1/admin/mail/inbox/{id}", s.admin(s.handleMailDelete))
	mux.Handle("GET /api/v1/admin/mail/outbox", s.admin(s.handleMailOutbox))
	mux.Handle("GET /api/v1/admin/mail/outbox/{id}", s.admin(s.handleMailOutMessage))
	mux.Handle("POST /api/v1/admin/mail/outbox/{id}/retry", s.admin(s.handleMailRetry))
	mux.Handle("POST /api/v1/admin/mail/outbox/{id}/cancel", s.admin(s.handleMailCancel))
	mux.Handle("POST /api/v1/admin/mail/send", s.admin(s.handleMailSend))
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad id")
		return 0, false
	}
	return id, true
}

func (s *Server) mailErr(w http.ResponseWriter, err error) {
	if errors.Is(err, mailer.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "no such message, or it cannot do that any more")
		return
	}
	s.internal(w, err)
}

func (s *Server) handleMailStatus(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	o, err := s.mail.GetOverview()
	if err != nil {
		s.mailErr(w, err)
		return
	}
	st := s.settings()
	writeJSON(w, http.StatusOK, map[string]any{"overview": o, "from": st.MailFrom, "support": st.MailSupport,
		"registration_open": st.OpenRegistration})
}

func (s *Server) handleMailMessage(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := s.mail.InMessage(id)
	if err != nil {
		s.mailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": m, "parsed": mailer.Parse(m.Raw), "reply_to": mailer.ReplyTarget(m.Raw)})
}

// download sends bytes a stranger wrote: always as a download of an opaque type
// (the Content-Type they claimed is not repeated), never rendered on this origin.
func download(w http.ResponseWriter, name string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleMailRaw(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := s.mail.InMessage(id)
	if err != nil {
		s.mailErr(w, err)
		return
	}
	download(w, fmt.Sprintf("message-%d.eml", id), m.Raw)
}

func (s *Server) handleMailAttachment(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad attachment number")
		return
	}
	m, err := s.mail.InMessage(id)
	if err != nil {
		s.mailErr(w, err)
		return
	}
	a, data, err := mailer.ReadAttachment(m.Raw, n)
	if err != nil {
		s.mailErr(w, err)
		return
	}
	name := a.Filename
	if name == "" {
		name = fmt.Sprintf("attachment-%d", n)
	}
	download(w, name, data)
}

func (s *Server) handleMailRead(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Read bool `json:"read"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.mail.SetRead(id, req.Read); err != nil {
		s.mailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// maxMailBatch bounds one batch request: a page of the inbox is at most 100.
const maxMailBatch = 500

// handleMailReadMany is POST /api/v1/admin/mail/inbox/read: {ids, read} marks a
// selection of messages at once.
func (s *Server) handleMailReadMany(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		IDs  []int64 `json:"ids"`
		Read bool    `json:"read"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > maxMailBatch {
		writeErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("ids must list 1 to %d messages", maxMailBatch))
		return
	}
	cu.note("count", len(req.IDs))
	cu.note("read", req.Read)
	n, err := s.mail.SetReadMany(req.IDs, req.Read)
	if err != nil {
		s.mailErr(w, err)
		return
	}
	cu.note("found", n)
	writeJSON(w, http.StatusOK, map[string]int64{"updated": n})
}

func (s *Server) handleMailMove(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Status != mailer.InboxOpen && req.Status != mailer.InboxArchived {
		writeErr(w, http.StatusBadRequest, "bad_request", "status must be inbox or archived")
		return
	}
	cu.note("status", req.Status)
	if err := s.mail.SetInboxStatus(id, req.Status); err != nil {
		s.mailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMailDelete(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.mail.DeleteInbound(id); err != nil {
		s.mailErr(w, err)
		return
	}
	s.log.Info("received mail deleted", "id", id, "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMailReply(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.mailConfigured(w) {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "the reply is empty")
		return
	}
	m, err := s.mail.InMessage(id)
	if err != nil {
		s.mailErr(w, err)
		return
	}
	to := accounts.NormalizeEmail(mailer.ReplyTarget(m.Raw))
	if m.IsBounce || accounts.CheckEmail(to) != nil {
		writeErr(w, http.StatusBadRequest, "no_reply_address", "this message has no address to answer to")
		return
	}
	subject := m.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	cu.note("to", to)
	out, err := s.mail.Enqueue(mailer.Message{Kind: mailer.KindReply, From: s.settings().MailSupport, To: to, Subject: subject, Text: req.Text, InReplyTo: m.MessageID, CreatedBy: cu.user.Username})
	if err != nil {
		s.mailErr(w, err)
		return
	}
	if err := s.mail.MarkReplied(id); err != nil {
		s.log.Warn("marking mail replied", "err", err, "id", id)
	}
	s.log.Info("reply queued", "id", id, "outbox", out, "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]int64{"outbox_id": out})
}

func (s *Server) mailConfigured(w http.ResponseWriter) bool {
	if st := s.settings(); st.MailFrom == "" || st.MailSupport == "" {
		writeErr(w, http.StatusConflict, "mail_not_configured", "no mail sender or support address is set; see the settings page")
		return false
	}
	return true
}

// maskSensitive hides a code still waiting in the queue: it opens the account,
// and administrators do not get to read it.
func maskSensitive(m *mailer.OutMail) {
	if m.Sensitive {
		m.Text, m.Subject = "（内容含验证码，不显示）", "（含验证码，不显示）"
	}
}

func (s *Server) handleMailOutMessage(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := s.mail.OutMessage(id)
	if err != nil {
		s.mailErr(w, err)
		return
	}
	maskSensitive(m)
	writeJSON(w, http.StatusOK, map[string]any{"message": m})
}

func (s *Server) handleMailRetry(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.mail.Retry(id); err != nil {
		s.mailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMailCancel(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.mail.Cancel(id); err != nil {
		s.mailErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMailSend(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	if !s.mailConfigured(w) {
		return
	}
	var req struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
		From    string `json:"from"` // "team" or "support" (default)
	}
	if !readJSON(w, r, &req) {
		return
	}
	to := accounts.NormalizeEmail(req.To)
	if err := accounts.CheckEmail(to); err != nil {
		s.accountErr(w, err)
		return
	}
	if strings.TrimSpace(req.Subject) == "" || strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "subject and text are required")
		return
	}
	st := s.settings()
	from := st.MailSupport
	if req.From == "team" {
		from = st.MailFrom
	}
	cu.note("to", to)
	cu.note("subject", req.Subject)
	id, err := s.mail.Enqueue(mailer.Message{Kind: mailer.KindCompose, From: from, To: to, ReplyTo: s.supportAddress(), Subject: req.Subject, Text: req.Text, CreatedBy: cu.user.Username})
	if err != nil {
		s.mailErr(w, err)
		return
	}
	s.log.Info("mail composed", "outbox", id, "to", to, "by", cu.user.Username)
	writeJSON(w, http.StatusOK, map[string]int64{"outbox_id": id})
}
