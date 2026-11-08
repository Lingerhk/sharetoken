package mailer

import (
	"context"
	"crypto"
	"log/slog"
	"net"
	"sync"
	"time"
)

// ServerConfig ties the worker and the SMTP server of cmd/mailserver together.
type ServerConfig struct {
	Store    *Store
	Worker   *Worker
	Inbound  *Inbound // nil: send only
	Listener net.Listener
	Hostname string
	Domain   string
	Selector string
	Key      crypto.Signer
	Delivery string
	Log      *slog.Logger
	// How often to report and re-check DNS. Zero values get defaults.
	HeartbeatEvery time.Duration
	DNSEvery       time.Duration
}

// Run serves until ctx ends, and returns the SMTP server's fatal error, if any.
func Run(ctx context.Context, c ServerConfig) error {
	if c.HeartbeatEvery <= 0 {
		c.HeartbeatEvery = 30 * time.Second
	}
	if c.DNSEvery <= 0 {
		c.DNSEvery = 10 * time.Minute
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.Worker.Run(ctx) }()
	go func() { defer wg.Done(); c.heartbeat(ctx) }()
	var err error
	if c.Inbound != nil {
		err = c.Inbound.Serve(ctx, c.Listener)
		if err != nil {
			cancel()
		}
	} else {
		<-ctx.Done()
	}
	wg.Wait()
	return err
}

func (c *ServerConfig) heartbeat(ctx context.Context) {
	st := ServerStatus{Hostname: c.Hostname, Domain: c.Domain, Delivery: c.Delivery, DKIMSelector: c.Selector, StartedAt: time.Now().Unix()}
	if c.Inbound != nil {
		st.Mailboxes, st.SMTPListen = c.Inbound.cfg.Mailboxes, c.Listener.Addr().String()
	}
	var lastDNS time.Time
	tick := time.NewTicker(c.HeartbeatEvery)
	defer tick.Stop()
	for {
		if time.Since(lastDNS) >= c.DNSEvery && c.Key != nil {
			lastDNS = time.Now()
			dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			st.DNS = CheckDNS(dctx, nil, DNSExpect{Domain: c.Domain, Hostname: c.Hostname, Selector: c.Selector, Key: c.Key})
			cancel()
			st.DNSCheckedAt = lastDNS.Unix()
		}
		if c.Inbound != nil {
			st.Counters = c.Inbound.Counters()
		}
		if err := c.Store.writeStatus(st); err != nil {
			c.Log.Warn("reporting status", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
