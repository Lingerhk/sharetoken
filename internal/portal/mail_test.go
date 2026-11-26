package portal

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
)

func newMailPortal(t *testing.T) (*Server, *api, string) {
	t.Helper()
	s, a := newPortal(t, Config{OpenRegistration: true, SignupCreditMicros: 1000,
		Mail: MailConfig{From: "ShareToken <team@sharetoken.test>", Support: "ShareToken Support <support@sharetoken.test>"}})
	root, err := s.store.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.Store().IssueSessionToken(root.ID, "test", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, tok
}

var mailedCodeRE = regexp.MustCompile(`(?m)^ {4}(\d{6})\r?$`)

// mailedCode reads the code back from the outbox, as the recipient would.
func mailedCode(t *testing.T, s *Server, to string) string {
	t.Helper()
	ms, err := s.mail.Outbox(mailer.OutFilter{Kind: mailer.KindVerify, Q: to, Limit: 1})
	if err != nil || len(ms) == 0 {
		t.Fatalf("no code for %s: %v", to, err)
	}
	m, err := s.mail.OutMessage(ms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	code := mailedCodeRE.FindStringSubmatch(m.Text)
	if code == nil || strings.Contains(m.Subject, code[1]) {
		t.Fatalf("code mail for %s: %q %q", to, m.Subject, m.Text)
	}
	return code[1]
}

// testNonces holds, per portal and address, the nonce of the code last asked
// for through the test helpers (api.call, web.do). They send it along when a
// test redeems a code without saying which nonce, as a client does that asked
// for the code itself; a test about nonces names its own, "" included.
var testNonces sync.Map

func noteNonce(url, path string, body any, raw []byte) {
	if !strings.HasSuffix(path, "/auth/code") {
		return
	}
	var sent struct {
		Nonce string `json:"nonce"`
	}
	if json.Unmarshal(raw, &sent) != nil || sent.Nonce == "" {
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	data, _ := json.Marshal(body)
	_ = json.Unmarshal(data, &req)
	testNonces.Store(url+"\n"+accounts.NormalizeEmail(req.Email), sent.Nonce)
}

func withNonce(url, path string, body any) any {
	if !strings.HasSuffix(path, "/auth/verify") || body == nil {
		return body
	}
	data, _ := json.Marshal(body)
	var req map[string]any
	if json.Unmarshal(data, &req) != nil {
		return body
	}
	if _, named := req["nonce"]; named {
		return body
	}
	email, _ := req["email"].(string)
	if n, ok := testNonces.Load(url + "\n" + accounts.NormalizeEmail(email)); ok {
		req["nonce"] = n
	}
	return req
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
	u, _ := s.store.CreateUser("user1", false)
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
	a.call("POST", "/api/v1/auth/code", "", map[string]string{"email": "a@example.com"}, 200, nil)
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

// Notices one account causes another to get are capped per recipient and per
// account that caused them; the requests themselves are still filed.
func TestNoticeLimits(t *testing.T) {
	s, a, _ := newMailPortal(t)
	notices := func(to string) int64 {
		t.Helper()
		n, err := s.mail.CountOutbox(mailer.OutFilter{Kind: mailer.KindNotice, Q: to})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	_, owner := testUser(t, s, "owner@example.com")
	var code accounts.PairingCode
	a.call("POST", "/api/v1/pairing/codes", owner, map[string]any{"approval": true}, 201, &code)
	for i := range noticesPerRecipientHour + 3 {
		_, tok := testUser(t, s, "asker"+strconv.Itoa(i)+"@example.com")
		a.call("POST", "/api/v1/pairing/redeem", tok, map[string]string{"code": code.Code}, 202, nil)
	}
	if n := notices("owner@example.com"); n != noticesPerRecipientHour {
		t.Fatalf("%d notices to one owner in an hour, want %d", n, noticesPerRecipientHour)
	}
	var v PairingView
	if a.call("GET", "/api/v1/pairing", owner, nil, 200, &v); len(v.Requests) != noticesPerRecipientHour+3 {
		t.Fatalf("%d requests filed, want %d", len(v.Requests), noticesPerRecipientHour+3)
	}
	// One account, many recipients.
	for i := range noticesPerSenderHour + 5 {
		s.notifyFrom(9000, mailer.Message{Kind: mailer.KindNotice, To: "many" + strconv.Itoa(i) + "@example.org", Subject: "x", Text: "x"})
	}
	if n := notices("@example.org"); n != noticesPerSenderHour {
		t.Fatalf("%d notices caused by one account in an hour, want %d", n, noticesPerSenderHour)
	}
	// A notice about one's own account is not one of these.
	for range noticesPerRecipientHour + 2 {
		s.notify(mailer.Message{Kind: mailer.KindNotice, To: "self@example.net", Subject: "x", Text: "x"})
	}
	if n := notices("self@example.net"); n != noticesPerRecipientHour+2 {
		t.Fatalf("%d of %d notices about one's own account", n, noticesPerRecipientHour+2)
	}
}
