package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Transport hands one message to the next mail server.
type Transport interface {
	// Deliver returns the server that accepted the message. A *PermanentError
	// means retrying is pointless; any other error is retried.
	Deliver(ctx context.Context, from, to string, raw []byte) (remote string, err error)
	Name() string
}

// PermanentError is a final refusal.
type PermanentError struct {
	Remote string
	Err    error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Direct delivers to the recipient domain's MX hosts.
type Direct struct {
	EHLO string // host name to greet with; must be the name its IP answers to
	// Port and AllowPrivate exist for tests. Without AllowPrivate a recipient
	// domain whose MX points inside the network is refused: anyone who can type
	// an email address into a sign-up form picks these hosts.
	Port         string
	AllowPrivate bool
	Resolver     *net.Resolver
	DialTimeout  time.Duration
	CmdTimeout   time.Duration
}

// Name implements Transport.
func (d *Direct) Name() string { return "direct" }

func (d *Direct) resolver() *net.Resolver {
	if d.Resolver != nil {
		return d.Resolver
	}
	return net.DefaultResolver
}

// mxHosts lists the servers for a domain, best first: its MX records, else the
// domain itself (RFC 5321 §5.1).
func (d *Direct) mxHosts(ctx context.Context, domain string) ([]string, error) {
	mx, err := d.resolver().LookupMX(ctx, domain)
	var dnsErr *net.DNSError
	if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
		if len(mx) == 0 {
			return nil, fmt.Errorf("MX lookup for %s: %w", domain, err)
		}
	}
	if len(mx) == 0 {
		if _, err := d.resolver().LookupHost(ctx, domain); err != nil {
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				return nil, &PermanentError{Err: fmt.Errorf("domain %s does not exist", domain)}
			}
			return nil, fmt.Errorf("lookup %s: %w", domain, err)
		}
		return []string{domain}, nil
	}
	sort.SliceStable(mx, func(i, j int) bool { return mx[i].Pref < mx[j].Pref })
	var hosts []string
	for _, m := range mx {
		h := strings.TrimSuffix(m.Host, ".")
		if h == "" { // null MX, RFC 7505: the domain accepts no mail
			return nil, &PermanentError{Err: fmt.Errorf("domain %s accepts no mail", domain)}
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

func routable(ip net.IP) bool {
	return !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast())
}

// Deliver implements Transport.
func (d *Direct) Deliver(ctx context.Context, from, to string, raw []byte) (string, error) {
	at := strings.LastIndexByte(to, '@')
	if at < 0 {
		return "", &PermanentError{Err: fmt.Errorf("bad recipient %q", to)}
	}
	hosts, err := d.mxHosts(ctx, to[at+1:])
	if err != nil {
		return "", err
	}
	var last error
	for _, host := range hosts {
		// IPv4 only: SPF authorizes this network's IPv4 address.
		ips, err := d.resolver().LookupIP(ctx, "ip4", host)
		if err != nil {
			last = fmt.Errorf("lookup %s: %w", host, err)
			continue
		}
		for _, ip := range ips {
			if !d.AllowPrivate && !routable(ip) {
				last = &PermanentError{Remote: host, Err: fmt.Errorf("mail host %s is at non-public address %s", host, ip)}
				continue
			}
			err := d.send(ctx, host, ip.String(), from, to, raw)
			if err == nil {
				return host, nil
			}
			last = err
			var pe *PermanentError
			if errors.As(err, &pe) {
				return host, err // the server has spoken: a backup would say the same
			}
		}
	}
	if last == nil {
		last = errors.New("no mail host found")
	}
	return "", last
}

func (d *Direct) send(ctx context.Context, host, ip, from, to string, raw []byte) error {
	port := d.Port
	if port == "" {
		port = "25"
	}
	dt := d.DialTimeout
	if dt == 0 {
		dt = 30 * time.Second
	}
	conn, err := (&net.Dialer{Timeout: dt}).DialContext(ctx, "tcp4", net.JoinHostPort(ip, port))
	if err != nil {
		return fmt.Errorf("%s: %w", host, err)
	}
	c, err := newSMTPConn(conn, d.CmdTimeout)
	if err != nil {
		return classify(host, err)
	}
	defer c.Close()
	if err := c.hello(d.EHLO); err != nil {
		return classify(host, err)
	}
	if c.has("STARTTLS") {
		// Opportunistic, as between mail servers generally: MX hosts rarely
		// have certificates for the names that point to them, so the
		// certificate is not checked. It protects against listeners, and is
		// what lets recipients show the mail as encrypted.
		if err := c.startTLS(&tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}); err != nil { //nolint:gosec
			return fmt.Errorf("%s: STARTTLS: %w", host, err)
		}
		if err := c.hello(d.EHLO); err != nil {
			return classify(host, err)
		}
	}
	return classify(host, c.send(from, to, raw))
}

func classify(host string, err error) error {
	if err == nil {
		return nil
	}
	var re *replyError
	if errors.As(err, &re) && re.Code >= 500 {
		return &PermanentError{Remote: host, Err: fmt.Errorf("%s: %w", host, err)}
	}
	return fmt.Errorf("%s: %w", host, err)
}

// Smarthost hands everything to one authenticating relay, for networks whose
// address receivers do not accept mail from directly.
type Smarthost struct {
	Addr     string // host:port
	Username string
	Password string
	EHLO     string
}

// Name implements Transport.
func (s *Smarthost) Name() string { return "smarthost " + s.Addr }

// Deliver implements Transport.
func (s *Smarthost) Deliver(ctx context.Context, from, to string, raw []byte) (string, error) {
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return "", err
	}
	conn, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return "", err
	}
	// Certificates are checked here, and TLS is required: credentials cross this link.
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	implicit := strings.HasSuffix(s.Addr, ":465")
	if implicit {
		conn = tls.Client(conn, cfg)
	}
	c, err := newSMTPConn(conn, 0)
	if err != nil {
		return "", fmt.Errorf("%s: %w", s.Addr, err)
	}
	defer c.Close()
	if err := c.hello(s.EHLO); err != nil {
		return "", classify(s.Addr, err)
	}
	if !implicit {
		if !c.has("STARTTLS") {
			return "", fmt.Errorf("%s does not offer STARTTLS", s.Addr)
		}
		if err := c.startTLS(cfg); err != nil {
			return "", fmt.Errorf("%s: STARTTLS: %w", s.Addr, err)
		}
		if err := c.hello(s.EHLO); err != nil {
			return "", classify(s.Addr, err)
		}
	}
	if s.Username != "" {
		if err := c.authPlain(s.Username, s.Password); err != nil {
			return "", fmt.Errorf("%s: auth: %w", s.Addr, err) // retried: fixing the credentials should not lose mail
		}
	}
	return s.Addr, classify(s.Addr, c.send(from, to, raw))
}

// ---- worker ----

// WorkerConfig configures Worker.
type WorkerConfig struct {
	Store       *Store
	Transport   Transport
	Signers     []*DKIMSigner // chosen by the sender's domain
	Workers     int           // parallel deliveries (default 4)
	PollEvery   time.Duration // default 2s
	RetryFor    time.Duration // give up after this (default 48h)
	KeepFor     time.Duration // finished mail is kept this long (default 90 days)
	SendTimeout time.Duration // one delivery attempt (default 3m)
	Log         *slog.Logger
}

// Worker delivers the outbox.
type Worker struct {
	cfg WorkerConfig
}

// NewWorker prepares a Worker.
func NewWorker(cfg WorkerConfig) *Worker {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 2 * time.Second
	}
	if cfg.RetryFor <= 0 {
		cfg.RetryFor = 48 * time.Hour
	}
	if cfg.KeepFor <= 0 {
		cfg.KeepFor = 90 * 24 * time.Hour
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = 3 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Worker{cfg: cfg}
}

// backoff is the wait before attempt n+1.
func backoff(attempts int64) time.Duration {
	steps := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}
	if attempts < 1 {
		attempts = 1
	}
	if int(attempts) > len(steps) {
		return 2 * time.Hour
	}
	return steps[attempts-1]
}

// Run delivers until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	st := w.cfg.Store
	_ = st.requeueStuck(0) // a stuck row from before this start belongs to nobody
	sem := make(chan struct{}, w.cfg.Workers)
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(w.cfg.PollEvery)
	defer tick.Stop()
	lastHousekeep := time.Now()
	for {
		free := w.cfg.Workers - len(sem)
		if free > 0 {
			msgs, err := st.claimDue(free)
			if err != nil {
				w.cfg.Log.Warn("reading the outbox", "err", err)
			}
			for i := range msgs {
				m := msgs[i]
				sem <- struct{}{}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					w.deliver(ctx, &m)
				}()
			}
		}
		if time.Since(lastHousekeep) > time.Hour {
			lastHousekeep = time.Now()
			if err := st.purge(w.cfg.KeepFor); err != nil {
				w.cfg.Log.Warn("purging the outbox", "err", err)
			}
			if err := st.requeueStuck(30 * time.Minute); err != nil {
				w.cfg.Log.Warn("requeueing the outbox", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (w *Worker) signer(from string) *DKIMSigner {
	dom := from[strings.LastIndexByte(from, '@')+1:]
	for _, s := range w.cfg.Signers {
		if strings.EqualFold(s.Domain, dom) {
			return s
		}
	}
	return nil
}

func (w *Worker) deliver(ctx context.Context, m *OutMail) {
	st := w.cfg.Store
	now := st.now()
	messageID := m.MessageID
	if messageID == "" {
		dom := "localhost"
		if len(w.cfg.Signers) > 0 {
			dom = w.cfg.Signers[0].Domain
		}
		var err error
		if messageID, err = newMessageID(dom); err != nil {
			w.cfg.Log.Error("message id", "err", err)
			return
		}
	}
	log := w.cfg.Log.With("mail", m.ID, "kind", m.Kind, "to", m.To, "attempt", m.Attempts)
	fail := func(remote, why string) {
		if err := st.markFailed(m, messageID, remote, why); err != nil {
			log.Error("recording a failure", "err", err)
		}
		log.Warn("mail failed", "err", why)
	}
	deadline := time.Unix(m.CreatedAt, 0).Add(w.cfg.RetryFor)
	if m.ExpiresAt > 0 {
		deadline = time.Unix(m.ExpiresAt, 0)
	}
	if now.After(deadline) {
		fail("", "expired before it could be delivered: "+m.LastError)
		return
	}
	from, raw, err := Compose(m, messageID, now)
	if err != nil {
		fail("", err.Error())
		return
	}
	if s := w.signer(from); s != nil {
		if raw, err = s.Sign(raw); err != nil {
			fail("", "DKIM: "+err.Error())
			return
		}
	} else {
		// Unsigned mail from this domain fails DMARC at the receiver.
		fail("", fmt.Errorf("%w for the domain of %s", errNoSigner, from).Error())
		return
	}
	sctx, cancel := context.WithTimeout(ctx, w.cfg.SendTimeout)
	defer cancel()
	remote, err := w.cfg.Transport.Deliver(sctx, from, bareAddress(m.To), raw)
	switch {
	case err == nil:
		if err := st.markSent(m, messageID, remote); err != nil {
			log.Error("recording a delivery", "err", err)
		}
		log.Info("mail sent", "remote", remote)
	case errors.As(err, new(*PermanentError)):
		fail(remote, err.Error())
	default:
		if ctx.Err() != nil { // shutting down: not the message's fault
			_ = st.markRetry(m, now, messageID, m.LastError)
			return
		}
		next := now.Add(backoff(m.Attempts))
		if next.After(deadline) {
			fail(remote, err.Error())
			return
		}
		if err2 := st.markRetry(m, next, messageID, err.Error()); err2 != nil {
			log.Error("recording a retry", "err", err2)
		}
		log.Info("mail will be retried", "err", err, "next", next.Format(time.RFC3339))
	}
}

func bareAddress(a string) string {
	if i := strings.LastIndexByte(a, '<'); i >= 0 {
		a = strings.TrimSuffix(a[i+1:], ">")
	}
	return strings.TrimSpace(a)
}

// ReadSecretFile reads a one-line secret.
func ReadSecretFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
