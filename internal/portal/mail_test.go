package portal

import (
	"database/sql"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"sharetoken/internal/mailer"
)

func newMailPortal(t *testing.T) (*Server, *api, string) {
	t.Helper()
	s, a := newPortal(t, Config{OpenRegistration: true, EmailVerification: true, SignupCreditMicros: 1000,
		Mail: MailConfig{From: "ShareToken <team@sharetoken.test>", Support: "ShareToken Support <support@sharetoken.test>"}})
	root, err := s.CreateUser("root", "correct horse", true)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.Store().IssueSessionToken(root.ID, "test", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, tok
}

// mailedCode reads the code back from the outbox, as the recipient would.
func mailedCode(t *testing.T, s *Server, to string) string {
	t.Helper()
	ms, err := s.mail.Outbox(mailer.OutFilter{Kind: mailer.KindVerify, Q: to, Limit: 1})
	if err != nil || len(ms) == 0 {
		t.Fatalf("no code for %s: %v", to, err)
	}
	return strings.TrimPrefix(ms[0].Subject, "Your ShareToken verification code: ")
}

func TestRegisterNeedsMailedCode(t *testing.T) {
	s, a, _ := newMailPortal(t)
	var info Info
	a.call("GET", "/api/v1/info", "", nil, 200, &info)
	if !info.EmailVerification || info.SupportEmail != "support@sharetoken.test" {
		t.Fatalf("%+v", info)
	}
	const email = "alice@example.com"
	reg := func(body map[string]string, want int) {
		t.Helper()
		a.call("POST", "/api/v1/register", "", body, want, nil)
	}

	// There is no way around the code.
	reg(map[string]string{"username": "alice", "password": "correct horse"}, 400)
	reg(map[string]string{"email": email, "password": "correct horse"}, 400)
	reg(map[string]string{"username": email, "password": "correct horse", "code": "123456"}, 400)

	var sent map[string]any
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": " Alice@Example.com "}, 200, &sent)
	if sent["cooldown_seconds"] == nil {
		t.Fatalf("%+v", sent)
	}
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": email}, 429, nil)
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": "not-an-email"}, 400, nil)

	code := mailedCode(t, s, email)
	if len(code) != 6 {
		t.Fatalf("code %q", code)
	}
	// A code is for one address; a short password does not use it up.
	reg(map[string]string{"email": "bob@example.com", "password": "correct horse", "code": code}, 400)
	reg(map[string]string{"email": email, "password": "short", "code": code}, 400)

	var res AuthResponse
	a.call("POST", "/api/v1/register", "", map[string]string{"email": "ALICE@example.com", "password": "correct horse", "code": code, "token_name": "t"}, 201, &res)
	if res.User == nil || res.User.Username != email || res.Token == "" {
		t.Fatalf("%+v", res)
	}
	var bal Balance
	if bal, _ = s.Balance(res.User.ID); bal.AvailableMicros != 1000 {
		t.Fatalf("sign-up credit: %+v", bal)
	}
	reg(map[string]string{"email": email, "password": "correct horse", "code": code}, 400) // used
}

// An address that already has an account is told so, and no mail goes out.
func TestRegisterCodeForTakenAddress(t *testing.T) {
	s, a, _ := newMailPortal(t)
	if _, err := s.CreateUser("taken@example.com", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	var e ErrorBody
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": "Taken@Example.com"}, 409, &e)
	if e.Error.Code != "exists" {
		t.Fatalf("%+v", e)
	}
	if ms, _ := s.mail.Outbox(mailer.OutFilter{Q: "taken@example.com"}); len(ms) != 0 {
		t.Fatalf("mail went out: %+v", ms)
	}
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": "free@example.com"}, 200, nil)
	// Checking many addresses from one IP is throttled.
	for i := 1; i < maxFails; i++ {
		a.call("POST", "/api/v1/register/code", "", map[string]string{"email": "taken@example.com"}, 409, nil)
	}
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": "taken@example.com"}, 429, nil)
}

func TestRegisterCodeWrongGuessesThrottled(t *testing.T) {
	s, a, _ := newMailPortal(t)
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": "a@example.com"}, 200, nil)
	code := mailedCode(t, s, "a@example.com")
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for i := 0; i < maxFails; i++ {
		a.call("POST", "/api/v1/register", "", map[string]string{"email": "a@example.com", "password": "correct horse", "code": wrong}, 400, nil)
	}
	a.call("POST", "/api/v1/register", "", map[string]string{"email": "a@example.com", "password": "correct horse", "code": code}, 429, nil)
}

func TestRegisterCodeClosedAndUnconfigured(t *testing.T) {
	_, closed := newPortal(t, Config{EmailVerification: true, Mail: MailConfig{From: "team@sharetoken.test"}})
	closed.call("POST", "/api/v1/register/code", "", map[string]string{"email": "a@example.com"}, 403, nil)
	_, plain := newPortal(t, Config{OpenRegistration: true})
	plain.call("POST", "/api/v1/register/code", "", map[string]string{"email": "a@example.com"}, 404, nil)
	// Without verification sign-up works as before.
	plain.call("POST", "/api/v1/register", "", map[string]string{"username": "alice", "password": "correct horse"}, 201, nil)
	if _, err := New(Config{DB: mustDB(t), EmailVerification: true}); err == nil {
		t.Fatal("verification without a sender was accepted")
	}
}

func mustDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRegisterCodeRefusesOtherSites(t *testing.T) {
	_, a, _ := newMailPortal(t)
	req, _ := http.NewRequest("POST", a.url+"/api/v1/register/code", strings.NewReader(`{"email":"a@example.com"}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func seedInbound(t *testing.T, s *Server, raw string) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO mail_inbox (mailbox, mail_from, from_hdr, subject, message_id, size, raw, status, received_at, verified, spf, dkim, remote_ip, helo)
		VALUES ('support@sharetoken.test', 'user@example.com', 'User <user@example.com>', 'Help', '<m1@example.com>', ?, ?, 'inbox', 1000, 0, 'none', 'none', '1.2.3.4', 'mx.example.com')`, len(raw), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const feedback = "From: User <user@example.com>\r\nReply-To: Real <real@example.com>\r\nSubject: Help\r\nMessage-ID: <m1@example.com>\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: text/html\r\n\r\n<p>my feedback</p><script>steal()</script>\r\n--B\r\nContent-Type: text/html\r\nContent-Disposition: attachment; filename=\"x.html\"\r\n\r\n<script>steal()</script>\r\n--B--\r\n"

func TestAdminMail(t *testing.T) {
	s, a, root := newMailPortal(t)
	id := seedInbound(t, s, feedback)
	base := "/api/v1/admin/mail/inbox/" + strconv.FormatInt(id, 10)

	// Users and anonymous callers see nothing.
	u, _ := s.CreateUser("user1", "correct horse", false)
	userTok, _, _ := s.Store().IssueSessionToken(u.ID, "t", 0, false)
	a.call("GET", "/api/v1/admin/mail/inbox", userTok, nil, 403, nil)
	a.call("GET", base, "", nil, 401, nil)
	a.call("POST", "/api/v1/admin/mail/send", userTok, map[string]string{"to": "a@b.co", "subject": "s", "text": "t"}, 403, nil)

	var st struct {
		Overview mailer.Overview `json:"overview"`
	}
	a.call("GET", "/api/v1/admin/mail/status", root, nil, 200, &st)
	if st.Overview.Unread != 1 || st.Overview.Online {
		t.Fatalf("%+v", st.Overview)
	}

	var list struct {
		Messages []mailer.InMail `json:"messages"`
	}
	a.call("GET", "/api/v1/admin/mail/inbox", root, nil, 200, &list)
	if len(list.Messages) != 1 || list.Messages[0].ID != id {
		t.Fatalf("%+v", list)
	}
	a.call("GET", "/api/v1/admin/mail/inbox?q=nothing-like-this", root, nil, 200, &list)
	if len(list.Messages) != 0 {
		t.Fatalf("search: %+v", list)
	}

	var d struct {
		Message mailer.InMail `json:"message"`
		Parsed  mailer.Parsed `json:"parsed"`
		ReplyTo string        `json:"reply_to"`
	}
	a.call("GET", base, root, nil, 200, &d)
	if d.ReplyTo != "real@example.com" || !strings.Contains(d.Parsed.Text, "my feedback") || strings.Contains(d.Parsed.Text, "steal") || len(d.Parsed.Attachments) != 1 {
		t.Fatalf("%+v", d)
	}
	a.call("POST", base+"/read", root, map[string]bool{"read": true}, 200, nil)

	// A selection is marked at once; unknown ids are skipped, empty or huge batches refused.
	var batch struct {
		Updated int64 `json:"updated"`
	}
	a.call("POST", "/api/v1/admin/mail/inbox/read", root, map[string]any{"ids": []int64{id, 99999}, "read": false}, 200, &batch)
	if m, _ := s.mail.InMessage(id); batch.Updated != 1 || m.ReadAt != 0 {
		t.Fatalf("batch unread: %+v %+v", batch, m)
	}
	a.call("POST", "/api/v1/admin/mail/inbox/read", root, map[string]any{"ids": []int64{id}, "read": true}, 200, &batch)
	if m, _ := s.mail.InMessage(id); batch.Updated != 1 || m.ReadAt == 0 {
		t.Fatalf("batch read: %+v %+v", batch, m)
	}
	a.call("POST", "/api/v1/admin/mail/inbox/read", root, map[string]any{"ids": []int64{}, "read": true}, 400, nil)
	a.call("POST", "/api/v1/admin/mail/inbox/read", root, map[string]any{"ids": make([]int64, maxMailBatch+1), "read": true}, 400, nil)
	a.call("POST", "/api/v1/admin/mail/inbox/read", userTok, map[string]any{"ids": []int64{id}, "read": true}, 403, nil)

	// A file from a stranger is an opaque download, whatever it claims to be.
	req, _ := http.NewRequest("GET", a.url+base+"/attachments/0", nil)
	req.Header.Set("Authorization", "Bearer "+root)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(string(body), "steal") {
		t.Fatalf("%d %v %q", resp.StatusCode, resp.Header, body)
	}
	a.call("GET", base+"/attachments/9", root, nil, 404, nil)

	// Reply: to Reply-To, from the support address, threaded.
	a.call("POST", base+"/reply", root, map[string]string{"text": " "}, 400, nil)
	var r struct {
		OutboxID int64 `json:"outbox_id"`
	}
	a.call("POST", base+"/reply", root, map[string]string{"text": "thanks"}, 200, &r)
	out, err := s.mail.OutMessage(r.OutboxID)
	if err != nil {
		t.Fatal(err)
	}
	if out.To != "real@example.com" || !strings.Contains(out.From, "support@sharetoken.test") || out.Subject != "Re: Help" || out.InReplyTo != "<m1@example.com>" || out.CreatedBy != "root" {
		t.Fatalf("%+v", out)
	}
	m, _ := s.mail.InMessage(id)
	if m.RepliedAt == 0 {
		t.Fatal("not marked replied")
	}

	// Archive, restore, delete.
	a.call("POST", base+"/status", root, map[string]string{"status": "nonsense"}, 400, nil)
	a.call("POST", base+"/status", root, map[string]string{"status": "archived"}, 200, nil)
	a.call("GET", "/api/v1/admin/mail/inbox", root, nil, 200, &list)
	if len(list.Messages) != 0 {
		t.Fatalf("archived mail in the inbox: %+v", list)
	}
	a.call("GET", "/api/v1/admin/mail/inbox?status=archived", root, nil, 200, &list)
	if len(list.Messages) != 1 {
		t.Fatalf("%+v", list)
	}
	a.call("DELETE", base, root, nil, 200, nil)
	a.call("GET", base, root, nil, 404, nil)

	// Compose, cancel, retry.
	a.call("POST", "/api/v1/admin/mail/send", root, map[string]string{"to": "bad", "subject": "s", "text": "t"}, 400, nil)
	a.call("POST", "/api/v1/admin/mail/send", root, map[string]string{"to": "a@example.com", "subject": "", "text": "t"}, 400, nil)
	a.call("POST", "/api/v1/admin/mail/send", root, map[string]string{"to": "a@example.com", "subject": "s", "text": "t", "from": "team"}, 200, &r)
	outBase := "/api/v1/admin/mail/outbox/" + strconv.FormatInt(r.OutboxID, 10)
	a.call("POST", outBase+"/retry", root, nil, 404, nil) // still queued
	a.call("POST", outBase+"/cancel", root, nil, 200, nil)
	a.call("POST", outBase+"/cancel", root, nil, 404, nil)
	a.call("POST", outBase+"/retry", root, nil, 200, nil)
	var ol struct {
		Messages []mailer.OutMail `json:"messages"`
	}
	a.call("GET", "/api/v1/admin/mail/outbox?status=queued&kind=compose", root, nil, 200, &ol)
	if len(ol.Messages) != 1 || !strings.Contains(ol.Messages[0].From, "team@") {
		t.Fatalf("%+v", ol)
	}
}

// Administrators do not get to read verification codes.
func TestAdminCannotReadCodes(t *testing.T) {
	s, a, root := newMailPortal(t)
	a.call("POST", "/api/v1/register/code", "", map[string]string{"email": "a@example.com"}, 200, nil)
	code := mailedCode(t, s, "a@example.com")
	var ol struct {
		Messages []mailer.OutMail `json:"messages"`
	}
	req, _ := http.NewRequest("GET", a.url+"/api/v1/admin/mail/outbox", nil)
	req.Header.Set("Authorization", "Bearer "+root)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), code) {
		t.Fatalf("the outbox list shows the code: %s", raw)
	}
	a.call("GET", "/api/v1/admin/mail/outbox", root, nil, 200, &ol)
	req, _ = http.NewRequest("GET", a.url+"/api/v1/admin/mail/outbox/"+strconv.FormatInt(ol.Messages[0].ID, 10), nil)
	req.Header.Set("Authorization", "Bearer "+root)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(string(raw), code) {
		t.Fatalf("%d the message shows the code: %s", resp.StatusCode, raw)
	}
}

func TestPasswordChangeNotifies(t *testing.T) {
	s, a, _ := newMailPortal(t)
	u, _ := s.CreateUser("n@example.com", "correct horse", false)
	tok, _, _ := s.Store().IssueSessionToken(u.ID, "t", 0, false)
	a.call("POST", "/api/v1/password", tok, map[string]any{"old_password": "correct horse", "new_password": "battery staple"}, 200, nil)
	ms, _ := s.mail.Outbox(mailer.OutFilter{Kind: mailer.KindNotice, Q: "n@example.com"})
	if len(ms) != 1 {
		t.Fatalf("%+v", ms)
	}
}
