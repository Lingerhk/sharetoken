package portal

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AdminAllowConfig limits where the admin pages, the admin API and
// administrators' sign-ins may come from. With From empty there is no limit.
type AdminAllowConfig struct {
	// From lists IP addresses, CIDR prefixes and host names. A host name
	// stands for every A and AAAA address it resolves to.
	From []string
	// Refresh is how often host names are resolved again (default 5m). A
	// name that fails to resolve keeps the addresses it last had.
	Refresh time.Duration
	// Lookup resolves a host name (tests). nil: the system resolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

// ParseAdminAllow checks admin.allow_from: each entry must be an IP address,
// a CIDR prefix or a host name.
func ParseAdminAllow(from []string) error {
	_, _, err := parseAllow(from)
	return err
}

func parseAllow(from []string) (fixed []netip.Prefix, hosts []string, err error) {
	for _, e := range from {
		e = strings.TrimSpace(e)
		if p, err := netip.ParsePrefix(e); err == nil {
			fixed = append(fixed, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			fixed = append(fixed, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		if !validHostName(e) {
			return nil, nil, fmt.Errorf("admin.allow_from: %q is not an IP address, CIDR prefix or host name", e)
		}
		hosts = append(hosts, strings.ToLower(strings.TrimSuffix(e, ".")))
	}
	return fixed, hosts, nil
}

func validHostName(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// adminAllow is the parsed list. Host names' addresses are swapped in whole by
// refresh; requests read them without locking.
type adminAllow struct {
	fixed   []netip.Prefix
	hosts   []string
	refresh time.Duration
	lookup  func(ctx context.Context, host string) ([]netip.Addr, error)

	resolved atomic.Pointer[map[string][]netip.Addr] // host → addresses last resolved

	logMu  sync.Mutex
	logged map[string]time.Time // refusals logged per IP, so a scanner cannot flood the log
}

func newAdminAllow(cfg AdminAllowConfig) (*adminAllow, error) {
	if len(cfg.From) == 0 {
		return nil, nil
	}
	fixed, hosts, err := parseAllow(cfg.From)
	if err != nil {
		return nil, err
	}
	a := &adminAllow{fixed: fixed, hosts: hosts, refresh: cfg.Refresh, lookup: cfg.Lookup, logged: map[string]time.Time{}}
	if a.refresh <= 0 {
		a.refresh = 5 * time.Minute
	}
	if a.lookup == nil {
		a.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	empty := map[string][]netip.Addr{}
	a.resolved.Store(&empty)
	return a, nil
}

// resolve looks every host name up once. A failure keeps the name's previous
// addresses; the errors are returned for logging.
func (a *adminAllow) resolve(ctx context.Context) []error {
	if len(a.hosts) == 0 {
		return nil
	}
	old := *a.resolved.Load()
	next := make(map[string][]netip.Addr, len(a.hosts))
	var errs []error
	for _, h := range a.hosts {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		addrs, err := a.lookup(lctx, h)
		cancel()
		if err == nil && len(addrs) == 0 {
			err = fmt.Errorf("no addresses")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", h, err))
			next[h] = old[h]
			continue
		}
		for i := range addrs {
			addrs[i] = addrs[i].Unmap()
		}
		next[h] = addrs
	}
	a.resolved.Store(&next)
	return errs
}

func (a *adminAllow) allows(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range a.fixed {
		if p.Contains(addr) {
			return true
		}
	}
	for _, addrs := range *a.resolved.Load() {
		for _, x := range addrs {
			if x == addr {
				return true
			}
		}
	}
	return false
}

// shouldLog reports whether a refusal for ip is worth a log line: at most one
// per address per minute.
func (a *adminAllow) shouldLog(ip string, now time.Time) bool {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	if t, ok := a.logged[ip]; ok && now.Sub(t) < time.Minute {
		return false
	}
	if len(a.logged) >= 4096 {
		clear(a.logged)
	}
	a.logged[ip] = now
	return true
}

// runAdminAllow re-resolves the host names until ctx ends.
func (s *Server) runAdminAllow(ctx context.Context) {
	a := s.adminAllow
	if a == nil || len(a.hosts) == 0 {
		return
	}
	t := time.NewTicker(a.refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, err := range a.resolve(ctx) {
				s.log.Warn("admin.allow_from: lookup failed; keeping the previous addresses", "err", err)
			}
		}
	}
}

// adminAllowed reports whether r may reach the admin side (true when no
// list is configured). A refusal is logged.
func (s *Server) adminAllowed(r *http.Request) bool {
	a := s.adminAllow
	if a == nil {
		return true
	}
	ip := s.clientIP(r)
	if a.allows(ip) {
		return true
	}
	if a.shouldLog(ip, s.cfg.Now()) {
		s.log.Warn("admin access refused: source not in admin.allow_from", "ip", ip, "method", r.Method, "path", r.URL.Path)
	}
	return false
}

// isAdminPath: the admin pages and the admin API.
func isAdminPath(p string) bool {
	return p == "/admin" || strings.HasPrefix(p, "/admin/") ||
		p == "/api/v1/admin" || strings.HasPrefix(p, "/api/v1/admin/")
}

// guardAdmin answers every admin path from a source outside admin.allow_from
// with the mux's own 404, as if the admin side did not exist.
func (s *Server) guardAdmin(h http.Handler) http.Handler {
	if s.adminAllow == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAdminPath(r.URL.Path) && !s.adminAllowed(r) {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// adminSignInRefused stops an administrator's sign-in from outside
// admin.allow_from (after the mailed code, before the second factor), having
// written the response.
func (s *Server) adminSignInRefused(w http.ResponseWriter, r *http.Request, admin bool) bool {
	if !admin || s.adminAllowed(r) {
		return false
	}
	writeErr(w, http.StatusForbidden, "forbidden", "administrators cannot sign in from this network")
	return true
}
