package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"sharetoken/internal/mysqldb"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return &Store{DB: db}
}

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func signer() *DKIMSigner { return &DKIMSigner{Domain: "example.test", Selector: "s1", Key: testKey} }

// startInbound runs a receiving server on a free loopback port.
func startInbound(t *testing.T, st *Store, mutate func(*InboundConfig)) (port string, in *Inbound) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := SelfSignedTLS("mx.test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := InboundConfig{Store: st, Hostname: "mx.test", Mailboxes: []string{"support@localhost"}, TLS: tlsCfg,
		CheckSPF:  func(context.Context, net.IP, string, string) string { return "none" },
		CheckDKIM: func(context.Context, []byte) []DKIMResult { return nil }}
	if mutate != nil {
		mutate(&cfg)
	}
	in = NewInbound(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = in.Serve(ctx, l) }()
	t.Cleanup(func() { cancel(); <-done })
	_, port, _ = net.SplitHostPort(l.Addr().String())
	return port, in
}

func direct(port string) *Direct {
	return &Direct{EHLO: "mail.example.test", Port: port, AllowPrivate: true, DialTimeout: 2 * time.Second, CmdTimeout: 5 * time.Second}
}

func worker(st *Store, tr Transport) *Worker {
	return NewWorker(WorkerConfig{Store: st, Transport: tr, Signers: []*DKIMSigner{signer()}, SendTimeout: 10 * time.Second})
}

// deliverAll claims and delivers what is due.
func deliverAll(t *testing.T, st *Store, w *Worker) {
	t.Helper()
	msgs, err := st.claimDue(10, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := range msgs {
		w.deliver(context.Background(), &msgs[i])
	}
}

func outRow(t *testing.T, st *Store, id int64) *OutMail {
	t.Helper()
	m, err := st.OutMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDeliveryRoundTrip(t *testing.T) {
	st := newStore(t)
	port, _ := startInbound(t, st, nil)
	w := worker(st, direct(port))
	id, err := st.Enqueue(Message{Kind: KindNotice, From: "ShareToken <team@example.test>", To: "support@localhost", ReplyTo: "help@example.test",
		Subject: "你好，世界 — hello", Text: "第一行\r\n.以点开头的行\r\n第三行 — café\r\n"})
	if err != nil {
		t.Fatal(err)
	}
	deliverAll(t, st, w)
	m := outRow(t, st, id)
	if m.Status != StatusSent || m.MessageID == "" {
		t.Fatalf("not sent: %+v", m)
	}
	got, err := st.Inbox(InFilter{Status: InboxOpen})
	if err != nil || len(got) != 1 {
		t.Fatalf("inbox: %v %+v", err, got)
	}
	in := got[0]
	if !in.TLS || in.Subject != "你好，世界 — hello" || in.MailFrom != "team@example.test" || !strings.Contains(in.From, "team@example.test") || in.Helo != "mail.example.test" {
		t.Fatalf("stored wrong: %+v", in)
	}
	full, err := st.InMessage(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := Parse(full.Raw)
	if !strings.Contains(p.Text, ".以点开头的行") || !strings.Contains(p.Text, "café") {
		t.Fatalf("body mangled: %q", p.Text)
	}
	if ReplyTarget(full.Raw) != "help@example.test" {
		t.Fatalf("reply target %q", ReplyTarget(full.Raw))
	}
	// The signature verifies against the public key, after the trip through SMTP.
	rec, err := DKIMRecord(testKey)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := dkim.VerifyWithOptions(bytes.NewReader(full.Raw), &dkim.VerifyOptions{LookupTXT: func(domain string) ([]string, error) {
		if domain != "s1._domainkey.example.test" {
			return nil, fmt.Errorf("unexpected lookup %s", domain)
		}
		return []string{rec}, nil
	}})
	if err != nil || len(vs) != 1 || vs[0].Err != nil {
		t.Fatalf("DKIM: %v %+v", err, vs)
	}
}

func TestInboundRefusesOtherAddresses(t *testing.T) {
	st := newStore(t)
	port, in := startInbound(t, st, nil)
	w := worker(st, direct(port))
	// An open relay would take this; so would a catch-all.
	id, _ := st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "someone@localhost", Subject: "x", Text: "x"})
	deliverAll(t, st, w)
	m := outRow(t, st, id)
	if m.Status != StatusFailed || !strings.Contains(m.LastError, "550") {
		t.Fatalf("want a permanent failure: %+v", m)
	}
	if got, _ := st.Inbox(InFilter{}); len(got) != 0 {
		t.Fatalf("stored: %+v", got)
	}
	if in.Counters().Rejected == 0 {
		t.Fatal("rejection not counted")
	}
}

func TestInboundLimits(t *testing.T) {
	st := newStore(t)
	port, _ := startInbound(t, st, func(c *InboundConfig) { c.MaxMessageBytes = 2048; c.MaxPerIPHour = 1 })
	w := worker(st, direct(port))
	big, _ := st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "support@localhost", Subject: "big", Text: strings.Repeat("x", 8000)})
	deliverAll(t, st, w)
	if m := outRow(t, st, big); m.Status != StatusFailed || !strings.Contains(m.LastError, "552") {
		t.Fatalf("oversized: %+v", m)
	}
	// Small enough for the size limit once DKIM is accounted for only with a
	// larger limit; check the hourly limit on a server that allows the size.
	st2 := newStore(t)
	port2, _ := startInbound(t, st2, func(c *InboundConfig) { c.MaxPerIPHour = 1 })
	w2 := worker(st2, direct(port2))
	a, _ := st2.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "support@localhost", Subject: "1", Text: "1"})
	b, _ := st2.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "support@localhost", Subject: "2", Text: "2"})
	deliverAll(t, st2, w2)
	if m := outRow(t, st2, a); m.Status != StatusSent {
		t.Fatalf("first: %+v", m)
	}
	if m := outRow(t, st2, b); m.Status != StatusQueued || !strings.Contains(m.LastError, "450") {
		t.Fatalf("second should be retried: %+v", m)
	}
}

func TestStorageCapDefersMail(t *testing.T) {
	st := newStore(t)
	if _, err := st.storeInbound(&InMail{Mailbox: "support@localhost", Raw: bytes.Repeat([]byte("x"), 2000)}); err != nil {
		t.Fatal(err)
	}
	port, _ := startInbound(t, st, func(c *InboundConfig) { c.MaxInboxBytes = 1000 })
	w := worker(st, direct(port))
	id, _ := st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "support@localhost", Subject: "x", Text: "x"})
	deliverAll(t, st, w)
	if m := outRow(t, st, id); m.Status != StatusQueued || !strings.Contains(m.LastError, "452") {
		t.Fatalf("want a temporary refusal: %+v", m)
	}
}

func TestRetriesAndExpiry(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	st.Now = func() time.Time { return now }
	l, _ := net.Listen("tcp4", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close() // nobody home
	w := worker(st, direct(port))
	id, _ := st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "user@localhost", Subject: "x", Text: "x"})
	deliverAll(t, st, w)
	m := outRow(t, st, id)
	if m.Status != StatusQueued || m.NextAt != now.Add(time.Minute).Unix() || m.LastError == "" {
		t.Fatalf("want a retry in a minute: %+v", m)
	}
	if msgs, _ := st.claimDue(10, 10); len(msgs) != 0 {
		t.Fatal("claimed before it was due")
	}
	// Past the window it fails, keeping the reason.
	now = now.Add(49 * time.Hour)
	deliverAll(t, st, w)
	m = outRow(t, st, id)
	if m.Status != StatusFailed || !strings.Contains(m.LastError, "expired") {
		t.Fatalf("want expiry: %+v", m)
	}
	// An administrator can give it a new window.
	if err := st.Retry(id); err != nil {
		t.Fatal(err)
	}
	if m = outRow(t, st, id); m.Status != StatusQueued || m.Attempts != 0 {
		t.Fatalf("retry: %+v", m)
	}
	deliverAll(t, st, w)
	if m = outRow(t, st, id); m.Status != StatusQueued {
		t.Fatalf("a retried message expired at once: %+v", m)
	}
}

func TestCrashedDeliveryIsRequeued(t *testing.T) {
	st := newStore(t)
	id, _ := st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "user@localhost", Subject: "x", Text: "x"})
	if msgs, err := st.claimDue(5, 5); err != nil || len(msgs) != 1 {
		t.Fatalf("claim: %v %d", err, len(msgs))
	}
	if msgs, _ := st.claimDue(5, 5); len(msgs) != 0 {
		t.Fatal("claimed twice")
	}
	st.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := st.requeueStuck(30 * time.Minute); err != nil {
		t.Fatal(err)
	}
	if m := outRow(t, st, id); m.Status != StatusQueued {
		t.Fatalf("still %s", m.Status)
	}
}

func TestPrivateMailHostsRefused(t *testing.T) {
	d := &Direct{EHLO: "mail.example.test", DialTimeout: time.Second}
	_, err := d.Deliver(context.Background(), "a@example.test", "user@localhost", []byte("x\r\n"))
	var pe *PermanentError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("want refusal of a loopback mail host, got %v", err)
	}
}

func TestUnsignedMailIsNotSent(t *testing.T) {
	st := newStore(t)
	port, _ := startInbound(t, st, nil)
	w := worker(st, direct(port))
	id, _ := st.Enqueue(Message{Kind: KindNotice, From: "team@other.test", To: "support@localhost", Subject: "x", Text: "x"})
	deliverAll(t, st, w)
	if m := outRow(t, st, id); m.Status != StatusFailed || !strings.Contains(m.LastError, "DKIM") {
		t.Fatalf("%+v", m)
	}
}

func TestComposeHeaderInjection(t *testing.T) {
	m := &OutMail{Kind: KindReply, From: "team@example.test", To: "user@example.test", Subject: "hi\r\nBcc: victim@example.test\r\n\r\nowned", Text: "body", InReplyTo: "<a@b>\r\nX-Evil: 1"}
	_, raw, err := Compose(m, "<id@example.test>", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "X-Evil:") {
			t.Fatalf("header injected:\n%s", head)
		}
	}
	if _, err := (&Store{}).Enqueue(Message{Kind: "x", From: "a@b.c", To: "a@b.c\r\nBcc: x@y.z"}); err == nil {
		t.Fatal("line break in an address accepted")
	}
}

func TestParse(t *testing.T) {
	t.Run("html is reduced to text", func(t *testing.T) {
		raw := "From: a@b.test\r\nSubject: x\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><head><title>T</title><style>p{}</style></head><body><p>hello</p><script>alert('xss')</script><img src=x onerror=alert(1)><p>world</p></body></html>"
		p := Parse([]byte(raw))
		if !p.FromHTML || !strings.Contains(p.Text, "hello") || !strings.Contains(p.Text, "world") {
			t.Fatalf("%+v", p)
		}
		for _, bad := range []string{"alert", "<", "onerror", "p{}"} {
			if strings.Contains(p.Text, bad) {
				t.Fatalf("%q survived: %q", bad, p.Text)
			}
		}
	})
	t.Run("gbk from a Chinese provider", func(t *testing.T) {
		// "你好" in GBK, in the subject and the body.
		gbk := []byte{0xC4, 0xE3, 0xBA, 0xC3}
		raw := "From: =?GBK?B?" + base64.StdEncoding.EncodeToString(gbk) + "?= <a@qq.test>\r\nSubject: =?GBK?B?" + base64.StdEncoding.EncodeToString(gbk) + "?=\r\nContent-Type: text/plain; charset=GBK\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(gbk) + "\r\n"
		p := Parse([]byte(raw))
		if p.Subject != "你好" || strings.TrimSpace(p.Text) != "你好" || !strings.Contains(p.From, "你好") {
			t.Fatalf("%+v", p)
		}
		if s := summarize([]byte(raw)); s.Subject != "你好" {
			t.Fatalf("summary %+v", s)
		}
	})
	t.Run("attachments", func(t *testing.T) {
		raw := "From: a@b.test\r\nSubject: x\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--B\r\nContent-Type: text/html; name=evil.html\r\nContent-Disposition: attachment; filename=\"evil.html\"\r\n\r\n<script>1</script>\r\n--B\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=\"a.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw==\r\n--B--\r\n"
		p := Parse([]byte(raw))
		if len(p.Attachments) != 2 || p.Attachments[0].Filename != "evil.html" || p.Attachments[1].Filename != "a.png" || strings.Contains(p.Text, "script") {
			t.Fatalf("%+v", p)
		}
		a, data, err := ReadAttachment([]byte(raw), 1)
		if err != nil || a.Filename != "a.png" || !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}) {
			t.Fatalf("%v %+v %q", err, a, data)
		}
		if _, _, err := ReadAttachment([]byte(raw), 2); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("garbage does not panic and invalid utf8 is cleaned", func(t *testing.T) {
		for _, raw := range []string{"", "\x00\xff\xfe", "not a mail", "Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nbroken", "Subject: \xff\xfe\r\n\r\n\xff\xfe\x00body"} {
			p := Parse([]byte(raw))
			if strings.Contains(p.Text, "\x00") {
				t.Fatalf("NUL in %q", p.Text)
			}
			_ = ReplyTarget([]byte(raw))
			_ = summarize([]byte(raw))
		}
	})
}

// Mail bodies and headers come from strangers and go into a utf8mb4 column.
func TestInboundStoresBadEncodings(t *testing.T) {
	st := newStore(t)
	raw := []byte("From: \xff\xfe <a@b.test>\r\nSubject: \xff\x00bad\r\n\r\nbody\r\n")
	sum := summarize(raw)
	id, err := st.storeInbound(&InMail{Mailbox: "support@localhost", From: sum.From, Subject: sum.Subject, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.InMessage(id)
	if err != nil || !bytes.Equal(m.Raw, raw) || strings.Contains(m.Subject, "\x00") {
		t.Fatalf("%v %+v", err, m)
	}
}

// The domain checked against SPF and DKIM is that of the address a reader
// sees, whatever the display name says, and there has to be exactly one.
func TestFromDomainIsTheAddressShown(t *testing.T) {
	for _, c := range []struct{ name, headers, want string }{
		{"plain", "From: alice@victim.example\r\n", "victim.example"},
		{"named", "From: Alice <alice@Victim.Example>\r\n", "victim.example"},
		{"a domain in the display name", "From: \"@evil.example>\" <admin@victim.example>\r\n", "victim.example"},
		{"an address in the display name", "From: \"x@evil.example\" <admin@victim.example>\r\n", "victim.example"},
		{"two mailboxes", "From: a@evil.example, admin@victim.example\r\n", ""},
		{"two From fields", "From: a@evil.example\r\nFrom: admin@victim.example\r\n", ""},
		{"none", "Subject: x\r\n", ""},
		{"unreadable", "From: <<>>\r\n", ""},
	} {
		sum := summarize([]byte(c.headers + "Subject: s\r\n\r\nbody\r\n"))
		if sum.FromDomain != c.want {
			t.Errorf("%s: domain %q, want %q (shown as %q)", c.name, sum.FromDomain, c.want, sum.From)
		}
	}
	// What the old code read from the shown text.
	sum := summarize([]byte("From: \"@evil.example>\" <admin@victim.example>\r\n\r\nx\r\n"))
	if v, _ := authVerdict(sum.FromDomain, "evil.example", "pass", []DKIMResult{{"evil.example", true}}); v {
		t.Fatal("mail from evil.example was shown as verified mail of victim.example")
	}
}

func TestAuthVerdict(t *testing.T) {
	for _, c := range []struct {
		name, from, env, spfRes string
		sigs                    []DKIMResult
		want                    bool
		dkim                    string
	}{
		{"aligned spf", "qq.com", "qq.com", "pass", nil, true, "none"},
		{"aligned subdomain spf", "qq.com", "mail.qq.com", "pass", nil, true, "none"},
		{"spf for another domain is not enough", "bank.com", "evil.com", "pass", nil, false, "none"},
		{"aligned dkim", "gmail.com", "bounce.other.net", "fail", []DKIMResult{{"gmail.com", true}}, true, "pass"},
		{"dkim for another domain", "bank.com", "evil.com", "fail", []DKIMResult{{"evil.com", true}}, false, "pass"},
		{"failed dkim", "gmail.com", "x.net", "none", []DKIMResult{{"gmail.com", false}}, false, "fail"},
		{"forged", "bank.com", "bank.com", "fail", nil, false, "none"},
		{"no from", "", "", "pass", nil, false, "none"},
	} {
		got, d := authVerdict(c.from, c.env, c.spfRes, c.sigs)
		if got != c.want || d != c.dkim {
			t.Errorf("%s: %v %s", c.name, got, d)
		}
	}
}

var mailedCodeRE = regexp.MustCompile(`(?m)^ {4}(\d{6})\r?$`)

// lastCode reads the newest code mail, as its recipient would.
func lastCode(t *testing.T, st *Store) string {
	t.Helper()
	ms, err := st.Outbox(OutFilter{Kind: KindVerify, Limit: 1})
	if err != nil || len(ms) == 0 {
		t.Fatalf("%v %v", err, ms)
	}
	m := outRow(t, st, ms[0].ID)
	if !m.Sensitive || m.ExpiresAt == 0 {
		t.Fatalf("code mail must be sensitive and expire: %+v", m)
	}
	got := mailedCodeRE.FindStringSubmatch(m.Text)
	if got == nil {
		t.Fatalf("no code in %q", m.Text)
	}
	if strings.Contains(m.Subject, got[1]) || !strings.Contains(m.HTML, got[1]) {
		t.Fatalf("the code belongs in the body only: %q", m.Subject)
	}
	return got[1]
}

func otherCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestCodes(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	st.Now = func() time.Time { return now }
	const email = "user@example.test"
	req := CodeRequest{Purpose: PurposeLogin, Email: email, IP: "1.2.3.4", From: "team@example.test"}
	ask := func(r CodeRequest) (nonce, code string) {
		t.Helper()
		nonce, err := st.RequestCode(r)
		if err != nil || nonce == "" {
			t.Fatalf("%q %v", nonce, err)
		}
		return nonce, lastCode(t, st)
	}
	nonce, code := ask(req)
	if _, err := st.RequestCode(req); !errors.Is(err, ErrCodeCooldown) {
		t.Fatalf("cooldown: %v", err)
	}
	if err := st.VerifyCode(PurposeLogin, "other@example.test", nonce, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("code of another address: %v", err)
	}
	if err := st.VerifyCode("reset", email, nonce, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("code for another purpose: %v", err)
	}
	// The code alone is worth nothing: it goes with the nonce of its request.
	for _, n := range []string{"", "AAAAAAAAAAAAAAAAAAAAAA"} {
		for range 2 * maxCodeAttempts {
			if err := st.VerifyCode(PurposeLogin, email, n, code); !errors.Is(err, ErrBadCode) {
				t.Fatalf("code without its nonce (%q): %v", n, err)
			}
		}
	}
	// ... and none of that counted against the code.
	if err := st.VerifyCode(PurposeLogin, email, nonce, " "+code[:3]+" "+code[3:]+" "); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if err := st.VerifyCode(PurposeLogin, email, nonce, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("second use: %v", err)
	}

	// Guessing with the nonce burns the code: even the right one stops working.
	now = now.Add(2 * time.Minute)
	nonce, code = ask(req)
	for range maxCodeAttempts {
		if err := st.VerifyCode(PurposeLogin, email, nonce, otherCode(code)); !errors.Is(err, ErrBadCode) {
			t.Fatal(err)
		}
	}
	if err := st.VerifyCode(PurposeLogin, email, nonce, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("code survived %d wrong guesses: %v", maxCodeAttempts, err)
	}

	// Expiry.
	now = now.Add(2 * time.Minute)
	nonce, code = ask(req)
	now = now.Add(CodeTTL + time.Second)
	if err := st.VerifyCode(PurposeLogin, email, nonce, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired code: %v", err)
	}

	// A second request leaves the first one's code alone: each works with its
	// own nonce, and burning one does not touch the other.
	other := CodeRequest{Purpose: PurposeLogin, Email: "n@example.test", IP: "5.6.7.8", From: "team@example.test"}
	now = now.Add(2 * time.Minute)
	n1, c1 := ask(other)
	now = now.Add(2 * time.Minute)
	n2, c2 := ask(other)
	if err := st.VerifyCode(PurposeLogin, other.Email, n2, c1); c1 != c2 && !errors.Is(err, ErrBadCode) {
		t.Fatalf("a code with another request's nonce: %v", err)
	}
	for range maxCodeAttempts {
		_ = st.VerifyCode(PurposeLogin, other.Email, n2, otherCode(c2))
	}
	if err := st.VerifyCode(PurposeLogin, other.Email, n2, c2); !errors.Is(err, ErrBadCode) {
		t.Fatalf("the second code survived its guesses: %v", err)
	}
	if err := st.VerifyCode(PurposeLogin, other.Email, n1, c1); err != nil {
		t.Fatalf("the first code was hurt by the second request: %v", err)
	}
}

// An operator's code needs no nonce, cannot be used up by wrong guesses, and
// leaves mailed codes alone.
func TestOperatorCode(t *testing.T) {
	st := newStore(t)
	const email = "user@example.test"
	nonce, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: email, IP: "1.2.3.4", From: "team@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	mailed := lastCode(t, st)
	old, _, err := st.IssueCode(PurposeLogin, email)
	if err != nil || len(old) != 12 {
		t.Fatalf("%q %v", old, err)
	}
	code, _, err := st.IssueCode(PurposeLogin, email)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		if err := st.VerifyCode(PurposeLogin, email, "", "000000000000"); !errors.Is(err, ErrBadCode) {
			t.Fatal(err)
		}
	}
	if err := st.VerifyCode(PurposeLogin, email, "", old); !errors.Is(err, ErrBadCode) {
		t.Fatalf("a replaced operator code: %v", err)
	}
	if err := st.VerifyCode(PurposeLogin, "other@example.test", "", code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("another address: %v", err)
	}
	// Typed into a form that asked for a mailed code first: the nonce is there, and does not matter.
	if err := st.VerifyCode(PurposeLogin, email, nonce, code[:4]+"-"+code[4:8]+" "+code[8:]); err != nil {
		t.Fatalf("operator code: %v", err)
	}
	if err := st.VerifyCode(PurposeLogin, email, "", code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("second use: %v", err)
	}
	if err := st.VerifyCode(PurposeLogin, email, nonce, mailed); err != nil {
		t.Fatalf("the mailed code after an operator code: %v", err)
	}
}

func TestCodeLimits(t *testing.T) {
	st := newStore(t)
	now := time.Now()
	st.Now = func() time.Time { return now }
	for i := range maxCodesPerEmail {
		if _, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: "a@example.test", IP: fmt.Sprintf("10.0.0.%d", i), From: "team@example.test"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(CodeCooldown + time.Second)
	}
	if _, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: "a@example.test", IP: "10.0.1.1", From: "team@example.test"}); !errors.Is(err, ErrCodeLimit) {
		t.Fatalf("per address: %v", err)
	}
	for i := range MaxCodesPerIP {
		if _, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: fmt.Sprintf("u%d@example.test", i), IP: "9.9.9.9", From: "team@example.test"}); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: "z@example.test", IP: "9.9.9.9", From: "team@example.test"}); !errors.Is(err, ErrCodeLimit) {
		t.Fatalf("per IP: %v", err)
	}
	var queued int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM mail_outbox`).Scan(&queued); err != nil || queued != maxCodesPerEmail+MaxCodesPerIP {
		t.Fatalf("%d mails queued, want one per code: %v", queued, err)
	}
}

// The budget of addresses without an account can be used up by anyone; the
// one of addresses with an account is another, and still serves them then.
func TestCodeBudgets(t *testing.T) {
	st := newStore(t)
	now := time.Now().Unix()
	fill := func(known, n int) {
		t.Helper()
		for i := range n {
			if _, err := st.DB.Exec(`INSERT INTO mail_codes (purpose, email, ip, salt, code_hash, known, expires_at, created_at) VALUES ('login', ?, ?, 'x', 'x', ?, ?, ?)`,
				fmt.Sprintf("f%d-%d@example.test", known, i), fmt.Sprintf("ip-%d-%d", known, i), known, now+600, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	req := func(email string, known bool) error {
		_, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: email, IP: "1.2.3.4", Known: known, From: "team@example.test"})
		return err
	}
	fill(0, MaxNewCodesPerHour)
	if err := req("new@example.test", false); !errors.Is(err, ErrNewCodeBudget) || !errors.Is(err, ErrCodeLimit) {
		t.Fatalf("new addresses past their budget: %v", err)
	}
	if err := req("member@example.test", true); err != nil {
		t.Fatalf("an address with an account while new ones are used up: %v", err)
	}
	fill(1, MaxKnownCodesPerHour-1)
	if err := req("member2@example.test", true); !errors.Is(err, ErrKnownCodeBudget) {
		t.Fatalf("known addresses past their budget: %v", err)
	}
	// An operator's codes count against nothing.
	for range 3 {
		if _, _, err := st.IssueCode(PurposeLogin, "member@example.test"); err != nil {
			t.Fatal(err)
		}
	}
}

// Requests sent together cannot all pass a limit: counting and storing are
// one transaction.
func TestCodeLimitsHoldInParallel(t *testing.T) {
	st := newStore(t)
	const parallel = 3 * MaxCodesPerIP
	run := func(req func(i int) CodeRequest) (ok int) {
		t.Helper()
		var wg sync.WaitGroup
		var passed atomic.Int64
		for i := range parallel {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := st.RequestCode(req(i))
				switch {
				case err == nil:
					passed.Add(1)
				case !errors.Is(err, ErrCodeLimit) && !errors.Is(err, ErrCodeCooldown):
					t.Errorf("request %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		return int(passed.Load())
	}
	if n := run(func(i int) CodeRequest {
		return CodeRequest{Purpose: PurposeLogin, Email: fmt.Sprintf("p%d@example.test", i), IP: "7.7.7.7", From: "team@example.test"}
	}); n != MaxCodesPerIP {
		t.Fatalf("%d codes for one address of the network in parallel, want %d", n, MaxCodesPerIP)
	}
	// One mailbox, many addresses: the cooldown lets a single one through.
	if n := run(func(i int) CodeRequest {
		return CodeRequest{Purpose: PurposeLogin, Email: "one@example.test", IP: fmt.Sprintf("8.8.%d.1", i), From: "team@example.test"}
	}); n != 1 {
		t.Fatalf("%d codes to one mailbox in parallel, want 1", n)
	}
}

// One code is tested maxCodeAttempts times at most, however the guesses come.
func TestCodeGuessesHoldInParallel(t *testing.T) {
	st := newStore(t)
	const email = "user@example.test"
	nonce, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: email, IP: "1.2.3.4", From: "team@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	code := lastCode(t, st)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.VerifyCode(PurposeLogin, email, nonce, otherCode(code)); !errors.Is(err, ErrBadCode) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var attempts int
	if err := st.DB.QueryRow(`SELECT attempts FROM mail_codes WHERE email = ?`, email).Scan(&attempts); err != nil || attempts != maxCodeAttempts {
		t.Fatalf("%d guesses counted, want %d: %v", attempts, maxCodeAttempts, err)
	}
	if err := st.VerifyCode(PurposeLogin, email, nonce, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("the code survived: %v", err)
	}
}

// A disabled account's request is counted like any other and mails a notice
// without a code.
func TestNoCodeRequest(t *testing.T) {
	st := newStore(t)
	req := CodeRequest{Purpose: PurposeLogin, Email: "off@example.test", IP: "1.2.3.4", Known: true, NoCode: true, From: "team@example.test", ReplyTo: "support@example.test"}
	nonce, err := st.RequestCode(req)
	if err != nil || nonce == "" {
		t.Fatalf("%q %v", nonce, err)
	}
	if _, err := st.RequestCode(req); !errors.Is(err, ErrCodeCooldown) {
		t.Fatalf("cooldown: %v", err)
	}
	ms, _ := st.Outbox(OutFilter{})
	if len(ms) != 1 || ms[0].Kind != KindNotice || ms[0].Sensitive {
		t.Fatalf("%+v", ms)
	}
	if m := outRow(t, st, ms[0].ID); mailedCodeRE.MatchString(m.Text) || !strings.Contains(m.Text, "disabled") || !strings.Contains(m.Text, "support@example.test") {
		t.Fatalf("%q", m.Text)
	}
}

func TestSensitiveBodyIsWiped(t *testing.T) {
	st := newStore(t)
	port, _ := startInbound(t, st, nil)
	w := worker(st, direct(port))
	if _, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: "support@localhost", IP: "1.1.1.1", From: "ShareToken <team@example.test>"}); err != nil {
		t.Fatal(err)
	}
	deliverAll(t, st, w)
	ms, _ := st.Outbox(OutFilter{})
	if len(ms) != 1 || ms[0].Status != StatusSent {
		t.Fatalf("%+v", ms)
	}
	m := outRow(t, st, ms[0].ID)
	if m.Text != wipedBody || m.HTML != "" || m.Subject != wipedBody {
		t.Fatalf("code kept: %q %q %q", m.Subject, m.Text, m.HTML)
	}
	if err := st.Retry(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sensitive retry: %v", err)
	}
}

// Cancelling a code mail removes the code like sending it does; other mail
// keeps its text.
func TestCancelWipesSensitiveBody(t *testing.T) {
	st := newStore(t)
	if _, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: "user@example.test", IP: "1.1.1.1", From: "team@example.test"}); err != nil {
		t.Fatal(err)
	}
	notice, _ := st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "user@example.test", Subject: "hello", Text: "kept"})
	ms, _ := st.Outbox(OutFilter{Kind: KindVerify})
	for _, id := range []int64{ms[0].ID, notice} {
		if err := st.Cancel(id); err != nil {
			t.Fatal(err)
		}
	}
	if m := outRow(t, st, ms[0].ID); m.Status != StatusCancelled || m.Text != wipedBody || m.HTML != "" || m.Subject != wipedBody {
		t.Fatalf("a cancelled code mail kept its code: %+v", m)
	}
	if m := outRow(t, st, notice); m.Status != StatusCancelled || m.Text != "kept" || m.Subject != "hello" {
		t.Fatalf("a cancelled notice: %+v", m)
	}
}

// Sign-in codes are claimed before other mail, however much of that waits,
// and other mail never takes more than its share of the deliveries.
func TestCodesAreClaimedFirst(t *testing.T) {
	st := newStore(t)
	for i := range 6 {
		if _, err := st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: fmt.Sprintf("n%d@example.test", i), Subject: "x", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RequestCode(CodeRequest{Purpose: PurposeLogin, Email: "user@example.test", IP: "1.1.1.1", From: "team@example.test"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.claimDue(3, 3)
	if err != nil || len(msgs) != 3 || msgs[0].Kind != KindVerify || msgs[1].Kind != KindNotice {
		t.Fatalf("%v %+v", err, msgs)
	}
	if msgs, _ := st.claimDue(4, 1); len(msgs) != 1 {
		t.Fatalf("%d notices claimed past their share", len(msgs))
	}
	if msgs, _ := st.claimDue(4, 0); len(msgs) != 0 {
		t.Fatalf("%d notices claimed into the place kept for codes", len(msgs))
	}
}

func TestDKIMKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dkim.key")
	if err := GenerateDKIMKey(path); err != nil {
		t.Fatal(err)
	}
	if err := GenerateDKIMKey(path); err == nil {
		t.Fatal("overwrote an existing key")
	}
	k, err := LoadDKIMKey(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := DKIMRecord(k)
	if err != nil || !strings.HasPrefix(rec, "v=DKIM1; k=rsa; p=") {
		t.Fatalf("%v %q", err, rec)
	}
}

// A sender that carries on after being refused, or vanishes mid-dialogue, must
// not wedge the server or use up its connection slots.
func TestInboundSurvivesMisbehavingClients(t *testing.T) {
	st := newStore(t)
	port, _ := startInbound(t, st, func(c *InboundConfig) { c.MaxConnsPerIP = 2; c.Timeout = 3 * time.Second })
	addr := net.JoinHostPort("127.0.0.1", port)
	session := func(script string, vanish bool) error {
		c, err := net.DialTimeout("tcp4", addr, time.Second)
		if err != nil {
			return err
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(8 * time.Second))
		if _, err := c.Write([]byte(script)); err != nil {
			return err
		}
		if vanish {
			return nil
		}
		// Read to the server's close; the deadline turns a hang into a failure.
		_, err = bytes.NewBuffer(nil).ReadFrom(c)
		return err
	}
	const refused = "EHLO x\r\nMAIL FROM:<a@b.test>\r\nRCPT TO:<root@localhost>\r\nDATA\r\nSubject: x\r\n\r\nx\r\n.\r\nQUIT\r\n"
	for i := range 60 {
		if err := session(refused, i%3 == 0); err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	// The slots came back: real mail still gets through.
	w := worker(st, direct(port))
	var id int64
	var m *OutMail
	for i := range 20 {
		id, _ = st.Enqueue(Message{Kind: KindNotice, From: "team@example.test", To: "support@localhost", Subject: fmt.Sprint(i), Text: "x"})
		deliverAll(t, st, w)
		if m = outRow(t, st, id); m.Status == StatusSent {
			return
		}
		time.Sleep(200 * time.Millisecond) // vanished clients are still being noticed
	}
	t.Fatalf("mail not accepted after misbehaving clients: %+v", m)
}

func TestMFAChangedMail(t *testing.T) {
	when := time.Date(2026, 11, 10, 13, 5, 0, 0, time.FixedZone("x", 8*3600))
	m := MFAChangedMail("ann@example.test", true, when, "203.0.113.7", "support@example.test")
	for _, want := range []string{"ann@example.test", "2026-11-10 05:05 UTC", "203.0.113.7", "support@example.test", "turned on"} {
		if !strings.Contains(m.Text, want) || !strings.Contains(m.HTML, want) {
			t.Fatalf("%q missing:\n%s\n%s", want, m.Text, m.HTML)
		}
	}
	if m.Kind != KindNotice || m.Sensitive || !strings.Contains(m.Subject, "is on") {
		t.Fatalf("%+v", m)
	}
	// The address is the user's own input: escaped in the HTML.
	m = MFAChangedMail("<b>@example.test", false, when, "", "")
	if strings.Contains(m.HTML, "<b>@") || strings.Contains(m.Text, "IP address") || !strings.Contains(m.Text, "turned off") {
		t.Fatalf("%s\n%s", m.Text, m.HTML)
	}
}

// A message built to be expensive to take apart is shown as it arrived.
func TestMessagesOfTooManyPartsAreNotTakenApart(t *testing.T) {
	var nested strings.Builder
	nested.WriteString("From: a@b.example\r\nSubject: deep\r\nMIME-Version: 1.0\r\n")
	const depth = 3000
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&nested, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	nested.WriteString("Content-Type: text/plain\r\n\r\nhello\r\n")
	start := time.Now()
	p := Parse([]byte(nested.String()))
	if !p.Malformed || len(p.Attachments) != 0 || !strings.Contains(p.Text, "multipart/mixed") {
		t.Fatalf("a message nested %d deep was taken apart: malformed=%v text=%.60q", depth, p.Malformed, p.Text)
	}
	if _, _, err := ReadAttachment([]byte(nested.String()), 0); err == nil {
		t.Fatal("an attachment was read from it")
	}

	var many strings.Builder
	many.WriteString("From: a@b.example\r\nSubject: many\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n")
	for i := 0; i < 5000; i++ {
		many.WriteString("--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=a\r\n\r\nz\r\n")
	}
	many.WriteString("--x--\r\n")
	p = Parse([]byte(many.String()))
	if !p.Malformed || len(p.Attachments) > maxParts {
		t.Fatalf("5000 parts: malformed=%v attachments=%d", p.Malformed, len(p.Attachments))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	// Ordinary mail is unaffected.
	ok := "From: a@b.example\r\nSubject: s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain\r\n\r\nhello\r\n--x\r\nContent-Type: text/html\r\n\r\n<p>hello</p>\r\n--x--\r\n"
	if p := Parse([]byte(ok)); p.Malformed || strings.TrimSpace(p.Text) != "hello" {
		t.Fatalf("ordinary mail: %+v", p)
	}
}
