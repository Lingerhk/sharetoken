package mailer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-smtp"
	"golang.org/x/net/publicsuffix"
)

// InboundConfig configures the receiving SMTP server.
type InboundConfig struct {
	Store     *Store
	Listen    string
	Hostname  string
	Mailboxes []string // full local addresses accepted, lowercase; nothing else is
	TLS       *tls.Config
	// Limits. Zero values get defaults.
	MaxMessageBytes int64         // default 10 MiB
	MaxConns        int           // concurrent, default 20
	MaxConnsPerIP   int           // concurrent, default 4
	MaxPerIPHour    int           // accepted messages per source address, default 60
	MaxInboxBytes   int64         // stop accepting (temporarily) above this, default 2 GiB
	Timeout         time.Duration // per read and write, default 2m
	// CheckSPF and CheckDKIM are replaced in tests.
	CheckSPF  func(ctx context.Context, ip net.IP, helo, sender string) string
	CheckDKIM func(ctx context.Context, raw []byte) []DKIMResult
	Log       *slog.Logger
}

// DKIMResult is one verified signature.
type DKIMResult struct {
	Domain string
	OK     bool
}

// Inbound is the receiving SMTP server. It never relays: mail is accepted only
// for the configured addresses and only ever stored. It sends no automatic
// replies, so forged senders cannot turn it into a source of backscatter.
type Inbound struct {
	cfg InboundConfig
	srv *smtp.Server

	mu    sync.Mutex
	conns map[string]int
	hour  map[string][]time.Time
	open  int

	boxes      map[string]bool
	sizeAt     time.Time
	sizeCached int64

	connections, accepted, rejected atomic.Int64
}

// NewInbound prepares the server.
func NewInbound(cfg InboundConfig) *Inbound {
	if cfg.MaxMessageBytes <= 0 {
		cfg.MaxMessageBytes = 10 << 20
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 20
	}
	if cfg.MaxConnsPerIP <= 0 {
		cfg.MaxConnsPerIP = 4
	}
	if cfg.MaxPerIPHour <= 0 {
		cfg.MaxPerIPHour = 60
	}
	if cfg.MaxInboxBytes <= 0 {
		cfg.MaxInboxBytes = 2 << 30
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.CheckSPF == nil {
		cfg.CheckSPF = checkSPF
	}
	if cfg.CheckDKIM == nil {
		cfg.CheckDKIM = checkDKIM
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	in := &Inbound{cfg: cfg, conns: map[string]int{}, hour: map[string][]time.Time{}, boxes: map[string]bool{}}
	for _, b := range cfg.Mailboxes {
		in.boxes[strings.ToLower(b)] = true
	}
	s := smtp.NewServer(smtp.BackendFunc(in.newSession))
	s.Addr, s.Domain, s.TLSConfig = cfg.Listen, cfg.Hostname, cfg.TLS
	s.MaxMessageBytes, s.MaxRecipients = cfg.MaxMessageBytes, 10
	s.ReadTimeout, s.WriteTimeout = cfg.Timeout, cfg.Timeout
	s.ErrorLog = discardLogger{}
	in.srv = s
	return in
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}
func (discardLogger) Println(...any)        {}

// Serve accepts mail on l until ctx ends.
func (in *Inbound) Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = in.srv.Shutdown(sctx)
		_ = in.srv.Close()
	}()
	err := in.srv.Serve(l)
	if ctx.Err() != nil || errors.Is(err, smtp.ErrServerClosed) {
		return nil
	}
	return err
}

// Counters reports activity since the start.
func (in *Inbound) Counters() SrvCounters {
	return SrvCounters{Connections: in.connections.Load(), Accepted: in.accepted.Load(), Rejected: in.rejected.Load()}
}

var (
	errBusy     = &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 3, 2}, Message: "Too many connections, try later"}
	errRate     = &smtp.SMTPError{Code: 450, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Too much mail from your address, try later"}
	errNoBox    = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such mailbox here"}
	errFull     = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "Mail storage is full, try later"}
	errInternal = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Temporary problem, try later"}
)

func (in *Inbound) newSession(c *smtp.Conn) (smtp.Session, error) {
	in.connections.Add(1)
	ip, _, _ := net.SplitHostPort(c.Conn().RemoteAddr().String())
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.open >= in.cfg.MaxConns || in.conns[ip] >= in.cfg.MaxConnsPerIP {
		in.rejected.Add(1)
		return nil, errBusy
	}
	in.open++
	in.conns[ip]++
	return &session{in: in, conn: c, ip: ip}, nil
}

func (in *Inbound) release(ip string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.open--
	if in.conns[ip]--; in.conns[ip] <= 0 {
		delete(in.conns, ip)
	}
}

// allowMessage counts one accepted message from ip against the hourly limit.
func (in *Inbound) allowMessage(ip string) bool {
	now := time.Now()
	cut := now.Add(-time.Hour)
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.hour) > 50_000 {
		clear(in.hour)
	}
	ts := in.hour[ip]
	for len(ts) > 0 && ts[0].Before(cut) {
		ts = ts[1:]
	}
	if len(ts) >= in.cfg.MaxPerIPHour {
		in.hour[ip] = ts
		return false
	}
	in.hour[ip] = append(ts, now)
	return true
}

// storageFull checks the cap, reading the total at most once a minute.
func (in *Inbound) storageFull() bool {
	in.mu.Lock()
	fresh := time.Since(in.sizeAt) < time.Minute
	size := in.sizeCached
	in.mu.Unlock()
	if !fresh {
		n, err := in.cfg.Store.inboxBytes()
		if err != nil {
			in.cfg.Log.Warn("reading the inbox size", "err", err)
			return false
		}
		size = n
		in.mu.Lock()
		in.sizeAt, in.sizeCached = time.Now(), n
		in.mu.Unlock()
	}
	return size >= in.cfg.MaxInboxBytes
}

type session struct {
	in   *Inbound
	conn *smtp.Conn
	ip   string
	from string
	null bool
	rcpt []string
	done bool
}

func (s *session) Reset() { s.from, s.null, s.rcpt = "", false, nil }

func (s *session) Logout() error {
	if !s.done {
		s.done = true
		s.in.release(s.ip)
	}
	return nil
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if s.in.storageFull() {
		return errFull
	}
	s.from, s.null = from, from == ""
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	to = strings.ToLower(strings.TrimSpace(to))
	if !s.in.boxes[to] {
		s.in.rejected.Add(1)
		return errNoBox
	}
	for _, r := range s.rcpt {
		if r == to {
			return nil
		}
	}
	s.rcpt = append(s.rcpt, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, s.in.cfg.MaxMessageBytes+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > s.in.cfg.MaxMessageBytes {
		s.in.rejected.Add(1)
		return smtp.ErrDataTooLarge
	}
	if !s.in.allowMessage(s.ip) {
		s.in.rejected.Add(1)
		return errRate
	}
	helo := s.conn.Hostname()
	_, usedTLS := s.conn.TLSConnectionState()
	sum := summarize(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spfRes := s.in.cfg.CheckSPF(ctx, net.ParseIP(s.ip), helo, s.from)
	dkimRes := s.in.cfg.CheckDKIM(ctx, raw)
	fromDomain := addressDomain(sum.From)
	verified, dkimText := authVerdict(fromDomain, addressDomain(s.from), spfRes, dkimRes)

	for _, box := range s.rcpt {
		id, err := s.in.cfg.Store.storeInbound(&InMail{
			Mailbox: box, MailFrom: s.from, From: sum.From, Subject: sum.Subject, MessageID: sum.MessageID,
			RemoteIP: s.ip, Helo: helo, TLS: usedTLS, SPF: spfRes, DKIM: dkimText, Verified: verified, IsBounce: s.null, Raw: raw,
		})
		if err != nil {
			s.in.cfg.Log.Error("storing received mail", "err", err)
			return errInternal // the sender keeps it and retries
		}
		s.in.accepted.Add(1)
		s.in.cfg.Log.Info("mail received", "id", id, "mailbox", box, "ip", s.ip, "spf", spfRes, "dkim", dkimText, "verified", verified, "bytes", len(raw))
	}
	return nil
}

// authVerdict says whether the From domain is backed by SPF or DKIM, in the
// sense of relaxed DMARC alignment.
func authVerdict(fromDomain, envelopeDomain, spfRes string, sigs []DKIMResult) (verified bool, dkimText string) {
	dkimText = "none"
	for _, r := range sigs {
		if !r.OK {
			if dkimText == "none" {
				dkimText = "fail"
			}
			continue
		}
		dkimText = "pass"
		if aligned(fromDomain, r.Domain) {
			verified = true
		}
	}
	if spfRes == string(spf.Pass) && aligned(fromDomain, envelopeDomain) {
		verified = true
	}
	return verified, dkimText
}

func aligned(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	oa, err1 := publicsuffix.EffectiveTLDPlusOne(a)
	ob, err2 := publicsuffix.EffectiveTLDPlusOne(b)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(a, b)
	}
	return strings.EqualFold(oa, ob)
}

// addressDomain takes the domain of "Name <a@b>" or "a@b"; for a list, the first.
func addressDomain(a string) string {
	if i := strings.IndexByte(a, '>'); i >= 0 {
		a = a[:i]
	}
	if i := strings.LastIndexByte(a, '<'); i >= 0 {
		a = a[i+1:]
	}
	if i := strings.IndexByte(a, ','); i >= 0 {
		a = a[:i]
	}
	if i := strings.LastIndexByte(a, '@'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(a[i+1:]))
	}
	return ""
}

func checkSPF(ctx context.Context, ip net.IP, helo, sender string) string {
	if ip == nil {
		return string(spf.None)
	}
	if sender == "" {
		sender = "postmaster@" + helo // RFC 7208 §2.4: null sender checks HELO
	}
	res, _ := spf.CheckHostWithSender(ip, helo, sender, spf.WithContext(ctx))
	return string(res)
}

func checkDKIM(ctx context.Context, raw []byte) []DKIMResult {
	type result struct {
		v []*dkim.Verification
	}
	ch := make(chan result, 1)
	go func() { // dkim.Verify takes no context, and only needs DNS
		v, _ := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{MaxVerifications: 5})
		ch <- result{v}
	}()
	select {
	case r := <-ch:
		out := make([]DKIMResult, 0, len(r.v))
		for _, v := range r.v {
			out = append(out, DKIMResult{Domain: v.Domain, OK: v.Err == nil})
		}
		return out
	case <-ctx.Done():
		return nil
	}
}

// ---- TLS ----

// CertLoader serves a certificate from files and notices renewals.
type CertLoader struct {
	Cert, Key string

	mu      sync.Mutex
	checked time.Time
	modTime time.Time
	cur     *tls.Certificate
}

// Get implements tls.Config.GetCertificate.
func (c *CertLoader) Get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && time.Since(c.checked) < time.Minute {
		return c.cur, nil
	}
	c.checked = time.Now()
	st, err := os.Stat(c.Cert)
	if err != nil {
		if c.cur != nil {
			return c.cur, nil
		}
		return nil, err
	}
	if c.cur != nil && st.ModTime().Equal(c.modTime) {
		return c.cur, nil
	}
	pair, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		if c.cur != nil { // mid-renewal: key and certificate do not match yet
			return c.cur, nil
		}
		return nil, err
	}
	c.cur, c.modTime = &pair, st.ModTime()
	return c.cur, nil
}

// SelfSignedTLS makes a throwaway certificate. Sending servers use STARTTLS
// opportunistically and do not check certificates, so this still encrypts.
func SelfSignedTLS(hostname string) (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(5, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: hostname}}, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("self-signed certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, nil
}
