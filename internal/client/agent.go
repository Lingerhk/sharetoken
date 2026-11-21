package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"

	"sharetoken/internal/attest"
	"sharetoken/internal/attest/hw"
	"sharetoken/internal/buyer"
	"sharetoken/internal/cli"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/portal"
	"sharetoken/internal/provider"
	"sharetoken/internal/relaypass"
	"sharetoken/internal/tailnet"
	"sharetoken/internal/warmup"
)

// Agent is a running client.
type Agent struct {
	// Timing of the background work; set before Start (zero: defaults).
	HealthEvery  time.Duration // check the path to the coordinator (10s)
	MeasureEvery time.Duration // re-measure relays (5m)
	// PrivateGrace is how long a sharing node waits for its private relays
	// before it falls back to the network's (30s).
	PrivateGrace time.Duration
	RefreshEvery time.Duration // refresh the relay list from the portal (5m)
	// OnLoginEnded, when set, is called once if the portal stops accepting this
	// agent's token, or the user logs out. The agent keeps running: stopping it
	// is up to the caller, and not from this call.
	OnLoginEnded    func()
	LoginCheckEvery time.Duration // how often to ask the portal (1m)
	// ErrandFirst is when the client first asks the coordinator for an errand
	// (a platform audit to carry, buyer.RunErrands); later the coordinator
	// says when (20s).
	ErrandFirst time.Duration
	// HostOptions are extra libp2p options for the agent's host (embedding,
	// tests); set before Start.
	HostOptions []libp2p.Option
	// ExternalAPI, when set, is the address of a local API server the embedder
	// runs and mounts Handler on (see Daemon): the agent opens no listener of
	// its own and leaves the run file to the embedder.
	ExternalAPI string
	// Updates, when set, is checking for new releases; the status reports it.
	Updates *UpdateWatcher
	// warm is the warm-up, run by an agent that serves its own API (an
	// embedder such as Daemon runs its own).
	warm *warmup.Service

	home string
	log  *slog.Logger

	mu       sync.Mutex
	cfg      Config
	keys     *p2p.Keys
	quote    *attest.Quote
	h        host.Host
	portal   *PortalClient
	netinfo  portal.NetworkInfo
	coord    peer.ID
	relays   []p2p.RelayRTT // last measurement of the network's relays, best first
	current  string         // relay used to reach the coordinator
	measured time.Time
	link     *p2p.LinkMonitor     // live pings of the current relay
	avoid    map[string]time.Time // relay -> until when not to choose it for coordinator traffic
	buyer    *buyer.Client
	proxy    *buyer.Proxy
	share    *shareState
	tailnet  *tailnet.Transport // the direct path over tunnels (tailnet.go); installed even while off
	tokens   *provider.Tokens   // OAuth logins of the providers
	warnings map[string]string

	// The user's own relays (private_relays.go), measured for the status.
	// Kept apart from relays: nothing that chooses the path to the
	// coordinator may see them.
	private     []p2p.RelayRTT
	privateDown time.Time // since when no private relay holds a reservation

	passMu sync.Mutex
	pass   *relaypass.Pass // admits this device's relay reservations

	ctx     context.Context
	cancel  context.CancelFunc
	srv     *http.Server
	addr    string
	key     string // the local API's key while it listens itself (localauth.go)
	started time.Time
	kick    chan struct{}

	loginKick chan struct{}
}

type shareState struct {
	cfg      ShareConfig
	models   []node.ModelConfig // everything sold: local models and the providers'
	node     *node.Service
	reserver *p2p.Reserver
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewAgent prepares an agent with the configuration in home.
func NewAgent(home string, cfg Config) *Agent {
	cfg.fill()
	return &Agent{home: home, cfg: cfg, tokens: &provider.Tokens{Home: home}, log: slog.Default().With("component", "client"), warnings: map[string]string{}, kick: make(chan struct{}, 1), loginKick: make(chan struct{}, 1),
		link: p2p.NewLinkMonitor(linkWindow), avoid: map[string]time.Time{}}
}

// Start joins the network and serves the local API; it returns once the
// agent is up. Close stops it.
func (a *Agent) Start(ctx context.Context) error {
	if a.cfg.Portal == "" || a.cfg.Token == "" {
		return errors.New("not logged in: run `sharetoken login` first")
	}
	if err := a.cfg.Share.Validate(); err != nil {
		return err
	}
	if a.HealthEvery == 0 {
		a.HealthEvery = 10 * time.Second
	}
	if a.MeasureEvery == 0 {
		a.MeasureEvery = 5 * time.Minute
	}
	if a.RefreshEvery == 0 {
		a.RefreshEvery = 5 * time.Minute
	}
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.started = time.Now()
	a.portal = NewPortalClient(a.cfg.Portal, a.cfg.Token, a.cfg.PortalPin)
	if InsecurePortalURL(a.cfg.Portal) {
		a.warn("portal-http", "the portal is plain HTTP: your token crosses the network unencrypted")
	}

	if err := a.loadKeys(); err != nil {
		return err
	}
	if err := a.refreshNetwork(a.ctx); err != nil {
		return err
	}
	if err := a.bind(a.ctx); err != nil {
		return err
	}
	hostOpts := slices.Clone(a.HostOptions)
	if opt := a.newTailnet(); opt != nil {
		hostOpts = append(hostOpts, opt)
	}
	h, err := p2p.NewHost(a.keys, p2p.HostConfig{Program: "client", Listen: a.cfg.Listen, NAT: a.cfg.NAT, Options: hostOpts})
	if err != nil {
		return err
	}
	a.h = h
	if err := a.setupBuyer(); err != nil {
		_ = h.Close()
		return err
	}
	a.tailnetHooks()
	if a.tailnet != nil {
		go a.tailnet.Run(a.ctx, h)
	}
	a.selectRelay(a.ctx, false)
	a.mu.Lock()
	if a.cfg.Share.Enabled {
		if err := a.startShareLocked(a.cfg.Share); err != nil {
			a.warn("share", err.Error())
		}
	}
	a.mu.Unlock()

	if a.ExternalAPI != "" {
		a.addr = a.ExternalAPI
	} else {
		ln, err := net.Listen("tcp", a.cfg.HTTP)
		if err != nil {
			a.Close()
			return fmt.Errorf("local API on %s: %w", a.cfg.HTTP, err)
		}
		a.addr = ln.Addr().String()
		a.key = newLocalKey()
		a.warm = warmup.New(a.home)
		go a.warm.Run(a.ctx)
		gate, err := newAPIKeyGate(a.home)
		if err != nil {
			ln.Close()
			a.Close()
			return err
		}
		a.srv = &http.Server{Handler: requireLocalKey(a.key, agentOpen, gate, a.Handler()), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.log.Error("local API stopped", "err", err)
			}
		}()
		a.writeRunFile()
	}
	if a.OnLoginEnded != nil {
		go a.watchLogin()
	}
	// Losing the last connection to the coordinator (its relay died) is
	// checked at once, not at the next health check.
	a.mu.Lock()
	coord := a.coord // fixed for the agent's life (a change needs a restart)
	a.mu.Unlock()
	a.h.Network().Notify(&network.NotifyBundle{DisconnectedF: func(n network.Network, c network.Conn) {
		if c.RemotePeer() == coord && n.Connectedness(coord) != network.Connected {
			a.Kick()
		}
	}})
	go a.maintain()
	go a.buyer.RunErrands(a.ctx, a.ErrandFirst, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.cfg.Buy.CarriesSpotChecks()
	})
	return nil
}

// RunFile records a running client's API address and key, for the CLI and the
// desktop shell (localauth.go).
const RunFile = "run.json"

// RunInfo is the content of RunFile.
type RunInfo struct {
	API string `json:"api"` // host:port of the local API
	PID int    `json:"pid"`
	Key string `json:"key"` // what the management API wants (localauth.go)
}

func (a *Agent) writeRunFile() { writeRunInfo(a.home, a.addr, a.key) }

// Key is the local API's key while the agent listens itself ("" otherwise).
func (a *Agent) Key() string { return a.key }

// Run starts the agent and blocks until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	a.Close()
	return nil
}

// Close stops sharing, the local API and the host.
func (a *Agent) Close() {
	if a.srv != nil {
		_ = os.Remove(filepath.Join(a.home, RunFile))
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.srv.Shutdown(sctx)
		cancel()
	}
	a.mu.Lock()
	a.stopShareLocked()
	b := a.buyer
	a.mu.Unlock()
	if b != nil {
		wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = b.WaitDisputes(wctx) // let disputes in flight reach the coordinator
		cancel()
	}
	if a.cancel != nil {
		a.cancel()
	}
	if a.h != nil {
		_ = a.h.Close()
	}
	if a.tailnet != nil {
		_ = a.tailnet.Close() // after the host, which closes its listener on the tunnel first
	}
}

// watchLogin notices a login that ended while the agent runs: the device key
// keeps working with the coordinator after its token is gone, so without this a
// client the user logged out of or revoked would go on spending and earning.
// An unreachable portal says nothing about the login and is ignored.
func (a *Agent) watchLogin() {
	every := a.LoginCheckEvery
	if every == 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ended := false
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		case <-a.loginKick:
			// `sharetoken logout` revokes the token in the file, which is not this
			// agent's if the user logged in again while it was running.
			if cfg, err := LoadConfig(a.home); err == nil && cfg.Token == "" {
				ended = true
			}
		}
		ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
		if ended {
			if err := a.portal.Logout(ctx); err != nil && !IsUnauthorized(err) {
				a.log.Warn("could not revoke this client's token", "err", err)
			}
		} else {
			ended = IsUnauthorized(a.portal.Do(ctx, http.MethodGet, "/api/v1/devices", nil, &struct{}{}))
		}
		cancel()
		if ended {
			a.log.Warn("this login has ended; stopping")
			a.OnLoginEnded()
			return
		}
	}
}

// Addr is the local API's address (host:port).
func (a *Agent) Addr() string { return a.addr }

// PeerID is this device's peer ID.
func (a *Agent) PeerID() string { return a.keys.ID() }

// Home is the client's state directory.
func (a *Agent) Home() string { return a.home }

// Host is the agent's libp2p host (tests).
func (a *Agent) Host() host.Host { return a.h }

func (a *Agent) warn(key, msg string) {
	if msg == "" {
		delete(a.warnings, key)
		return
	}
	if a.warnings[key] != msg {
		a.log.Warn(msg, "about", key)
	}
	a.warnings[key] = msg
}

func (a *Agent) loadKeys() error {
	tee := a.cfg.Share.TEE
	if !a.cfg.Share.Enabled || tee == "" || tee == "off" {
		if err := os.MkdirAll(a.home, 0o700); err != nil {
			return err
		}
		k, err := p2p.LoadOrCreateKeys(filepath.Join(a.home, KeyFile))
		a.keys = k
		return err
	}
	// In a confidential VM the keys exist only in guest memory (a key file
	// would be readable by whoever controls the disk), so the peer ID changes
	// every boot and the device is re-bound at start-up.
	k, err := p2p.NewKeys()
	if err != nil {
		return err
	}
	q, err := attestKeys(tee, k)
	if err != nil {
		return err
	}
	a.keys, a.quote = k, q
	return nil
}

func attestKeys(backend string, keys *p2p.Keys) (*attest.Quote, error) {
	att, err := hw.New(backend)
	if err != nil {
		return nil, err
	}
	q, err := att.Attest(attest.ReportData(keys.ReceiptKey(), keys.Box.Public()))
	if err != nil {
		return nil, err
	}
	claims, err := cli.TEEVerifier(false).Verify(q)
	if err != nil {
		return nil, fmt.Errorf("own quote does not verify: %w", err)
	}
	slog.Info("attested", "backend", q.Backend, "measurement", hex.EncodeToString(claims.Measurement), "debug", claims.Debug)
	return q, nil
}

// refreshNetwork fetches the coordinator and relay list from the portal,
// falling back to the last copy when the portal is unreachable.
func (a *Agent) refreshNetwork(ctx context.Context) error {
	cache := filepath.Join(a.home, NetworkFile)
	n, err := a.portal.Network(ctx)
	if err != nil {
		data, rerr := os.ReadFile(cache)
		if rerr != nil || json.Unmarshal(data, n) != nil || n.Coordinator == "" {
			return fmt.Errorf("cannot get the network from the portal: %w", err)
		}
		a.mu.Lock()
		a.warn("portal", "portal unreachable, using the cached relay list: "+err.Error())
		a.mu.Unlock()
	} else if data, err := json.Marshal(n); err == nil {
		_ = os.WriteFile(cache, data, 0o600)
		a.mu.Lock()
		a.warn("portal", "")
		a.mu.Unlock()
	}
	id, err := peer.Decode(n.Coordinator)
	if err != nil {
		return fmt.Errorf("portal returned a bad coordinator id: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.coord != "" && a.coord != id {
		a.log.Warn("the coordinator changed; restart the client to follow it", "old", a.coord, "new", id)
		return nil
	}
	a.coord, a.netinfo = id, *n
	return nil
}

// ErrLoginRejected means the portal no longer accepts the saved token.
var ErrLoginRejected = errors.New("the portal rejected the saved login (expired or revoked): run `sharetoken login` again")

// bind (re)binds this device's key to the logged-in user. Binding is
// idempotent; an expired login stops the agent.
func (a *Agent) bind(ctx context.Context) error {
	name := a.cfg.DeviceName
	if name == "" {
		name, _ = os.Hostname()
	}
	_, err := a.portal.BindDevice(ctx, a.keys, a.cfg.User, name)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case err == nil:
		a.warn("device", "")
	case IsUnauthorized(err):
		return ErrLoginRejected
	default:
		a.warn("device", "could not confirm the device binding: "+err.Error())
	}
	return nil
}

func (a *Agent) setupBuyer() error {
	b := buyer.New(a.h, a.keys, peer.AddrInfo{ID: a.coord})
	b.Evidence = a.cfg.Buy.Evidence
	b.OnCoordinatorUnreachable = a.Kick
	if v := a.cfg.Buy.FirstByteSeconds; v > 0 {
		b.FirstByteTimeout = time.Duration(v) * time.Second
	}
	if v := a.cfg.Buy.IdleSeconds; v > 0 {
		b.IdleTimeout = time.Duration(v) * time.Second
	}
	switch q := a.cfg.Buy.QueueSeconds; {
	case q < 0:
		b.QueueWait = 0
	case q > 0:
		b.QueueWait = time.Duration(q) * time.Second
	}
	if a.cfg.Buy.TEEPolicy != "" {
		p, err := attest.LoadPolicy(a.cfg.Buy.TEEPolicy)
		if err != nil {
			return err
		}
		b.Attestation = &buyer.Attestation{Verifier: cli.TEEVerifier(a.cfg.Buy.TEEOnline), Policy: p}
	} else if a.cfg.Buy.RequireTEE {
		return errors.New("buy.require_tee needs buy.tee_policy: the list of node images you trust")
	}
	a.buyer = b
	a.proxy = &buyer.Proxy{Client: b, BudgetMicros: a.cfg.Buy.BudgetMicros, MaxPriceIn: a.cfg.Buy.MaxPriceIn,
		MaxPriceOut: a.cfg.Buy.MaxPriceOut, RequireTEE: a.cfg.Buy.RequireTEE, RequireNative: a.cfg.Buy.RequireNative,
		NoLoopGuard: a.cfg.Buy.LoopGuard != nil && !*a.cfg.Buy.LoopGuard}
	return nil
}

// relayAddrs is the candidate relay list: the configured override, else the
// portal's.
func (a *Agent) relayAddrs() []string {
	if len(a.cfg.Relays) > 0 {
		return slices.Clone(a.cfg.Relays)
	}
	out := make([]string, 0, len(a.netinfo.Relays))
	for _, r := range a.netinfo.Relays {
		out = append(out, r.Addr)
	}
	return out
}

func (a *Agent) coordinatorOK(addr string) bool {
	for _, r := range a.netinfo.Relays {
		if r.Addr == addr {
			return r.OK
		}
	}
	return len(a.cfg.Relays) > 0 // overrides carry no status: assume yes
}

// selectRelay measures every relay and routes coordinator traffic through
// the best one. It keeps the current relay unless it failed or another is
// clearly faster, so that small jitter does not cause flapping.
func (a *Agent) selectRelay(ctx context.Context, currentFailed bool) {
	res := a.measureRelays(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	avoided := func(addr string) bool { _, ok := a.avoid[addr]; return ok }
	a.useRelayLocked(chooseRelay(res, a.current, currentFailed, a.coordinatorOK, avoided), currentFailed)
}

// measureRelays measures every candidate relay and records the result.
func (a *Agent) measureRelays(ctx context.Context) []p2p.RelayRTT {
	a.mu.Lock()
	addrs := a.relayAddrs()
	a.mu.Unlock()
	res := p2p.MeasureRelays(ctx, a.h, addrs, 3)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.relays, a.measured = res, time.Now()
	pruneAvoid(a.avoid, res, time.Now())
	a.mu.Unlock()
	a.measurePrivate(ctx)
	a.mu.Lock()
	return res
}

// failover moves coordinator traffic off failed, a relay the coordinator was
// just found unreachable through. It tries the other reachable relays in
// order of preference and moves to the first the coordinator answers
// through; only then is failed avoided for a while (a relay that is up but
// cannot carry the traffic, like a congested international link, often
// measures fastest again at once). If the coordinator answers through none,
// the coordinator itself is down (restarting): no relay is to blame, so the
// client stays on the best one, by RTT, to be ready when it is back.
func (a *Agent) failover(ctx context.Context, failed string) {
	res := a.measureRelays(ctx)
	a.mu.Lock()
	avoided := func(addr string) bool { _, ok := a.avoid[addr]; return ok }
	var cands []p2p.RelayRTT
	for _, r := range res {
		if r.OK() && r.Addr != failed {
			cands = append(cands, r)
		}
	}
	rank := func(r p2p.RelayRTT) int {
		switch av, ok := avoided(r.Addr), a.coordinatorOK(r.Addr); {
		case !av && ok:
			return 0
		case !av:
			return 1
		case ok:
			return 2
		}
		return 3
	}
	sort.SliceStable(cands, func(i, j int) bool { return rank(cands[i]) < rank(cands[j]) })
	a.mu.Unlock()

	for i := range cands {
		if !a.probeCoordinator(ctx, cands[i]) {
			continue
		}
		a.mu.Lock()
		if failed != "" {
			a.avoid[failed] = time.Now().Add(relayAvoidFor)
		}
		a.useRelayLocked(&cands[i], true)
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(cands) > 0 {
		a.warn("coordinator", "coordinator unreachable through every relay (restarting?); waiting on the best relay")
	}
	a.useRelayLocked(chooseRelay(res, a.current, true, a.coordinatorOK, avoided), true)
}

// probeCoordinator reports whether the coordinator can be reached through
// relay r alone. It leaves the connection open when it can.
func (a *Agent) probeCoordinator(ctx context.Context, r p2p.RelayRTT) bool {
	ps := a.h.Peerstore()
	ps.ClearAddrs(a.coord)
	ps.AddAddrs(a.coord, p2p.CircuitAddrs(r.Info()), peerstore.TempAddrTTL)
	if sw, ok := a.h.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(a.coord)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return a.h.Connect(network.WithAllowLimitedConn(cctx, "sharetoken-ctrl"), peer.AddrInfo{ID: a.coord}) == nil
}

// useRelayLocked routes coordinator traffic through best (nil: none is
// reachable). When currentFailed, connections through other relays are
// closed at once; otherwise requests in flight on the old path may finish.
func (a *Agent) useRelayLocked(best *p2p.RelayRTT, currentFailed bool) {
	// The peerstore may hold a probe's address: always restore the chosen one.
	ps := a.h.Peerstore()
	if best == nil {
		if a.current != "" {
			if ai, err := p2p.ParseRelay(a.current); err == nil {
				ps.ClearAddrs(a.coord)
				ps.AddAddrs(a.coord, p2p.CircuitAddrs(ai), peerstore.PermanentAddrTTL)
			}
		}
		a.warn("relay", "no relay is reachable; retrying")
		return
	}
	a.warn("relay", "")
	ps.ClearAddrs(a.coord)
	ps.AddAddrs(a.coord, p2p.CircuitAddrs(best.Info()), peerstore.PermanentAddrTTL)
	if best.Addr != a.current {
		a.log.Info("using relay", "relay", best.Addr, "rtt", best.RTT.Round(100*time.Microsecond))
		if a.current != "" {
			grace := relaySwitchGrace
			if currentFailed {
				grace = 0 // the old path is dead; keep only the new relay's connection
			}
			go a.closeOldCoordinatorConns(best.Info().ID, grace)
		}
		a.current = best.Addr
		a.link.Reset(best.Addr)
	}
	if a.share != nil {
		_ = a.share.reserver.SetRelays(a.shareRelaysLocked(a.share.cfg))
	}
}

// pruneAvoid drops expired entries from avoid, and clears it when every
// reachable relay is in it: then the coordinator itself is the likely problem
// (restarting), not the relays, and the client should choose by RTT again.
func pruneAvoid(avoid map[string]time.Time, res []p2p.RelayRTT, now time.Time) {
	for r, until := range avoid {
		if !now.Before(until) {
			delete(avoid, r)
		}
	}
	reachable, avoided := 0, 0
	for _, r := range res {
		if r.OK() {
			reachable++
			if _, ok := avoid[r.Addr]; ok {
				avoided++
			}
		}
	}
	if reachable > 0 && avoided == reachable {
		clear(avoid)
	}
}

// chooseRelay picks the relay for coordinator traffic from a measurement
// (best first): the fastest reachable relay, preferring relays that are not
// avoided (they recently failed to carry coordinator traffic) and then those
// the coordinator is known to be reachable through. It keeps current unless
// current failed or the best is faster by more than max(5ms, 20%), so jitter
// causes no flapping.
func chooseRelay(res []p2p.RelayRTT, current string, currentFailed bool, coordinatorOK, avoided func(string) bool) *p2p.RelayRTT {
	var best *p2p.RelayRTT
	tiers := []func(string) bool{
		func(a string) bool { return !avoided(a) && coordinatorOK(a) },
		func(a string) bool { return !avoided(a) },
		coordinatorOK,
		func(string) bool { return true }, // a bad relay beats none
	}
	for _, ok := range tiers {
		for i := range res {
			if res[i].OK() && ok(res[i].Addr) {
				best = &res[i]
				break
			}
		}
		if best != nil {
			break
		}
	}
	if best == nil || current == "" || current == best.Addr || currentFailed {
		return best
	}
	for i := range res {
		if r := &res[i]; r.Addr == current && r.OK() && !avoided(current) {
			if best.RTT+max(5*time.Millisecond, r.RTT/5) >= r.RTT {
				return r // not clearly better: stay
			}
			break
		}
	}
	return best
}

// shareRelaysLocked picks the relays to hold reservations on: the user's
// private relays, and of the network's the best reachable ones, the
// coordinator's relay first, unless a private relay does the job
// (officialWantedLocked).
func (a *Agent) shareRelaysLocked(sc ShareConfig) []string {
	private := a.privateRelaysLocked()
	out := slices.Clone(private)
	var rsv *p2p.Reserver
	if a.share != nil {
		rsv = a.share.reserver
	}
	if !a.officialWantedLocked(sc, private, rsv) {
		return out
	}
	if a.current != "" {
		out = append(out, a.current)
	}
	for _, r := range a.relays {
		if len(out)-len(private) >= sc.Relays {
			break
		}
		if r.OK() && !slices.Contains(out, r.Addr) {
			out = append(out, r.Addr)
		}
	}
	return out
}

// CoordinatorReachable dials the coordinator through the current relay.
func (a *Agent) CoordinatorReachable(ctx context.Context) bool {
	if a.h.Network().Connectedness(a.coord) == network.Connected {
		return true
	}
	// After a few failed dials (coordinator restarting, relay down) libp2p
	// backs off from the peer for up to minutes and Connect fails without
	// dialing. This check runs only every HealthEvery, so clearing the backoff
	// costs nothing and lets the client reconnect as soon as the coordinator
	// is back.
	if sw, ok := a.h.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(a.coord)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return a.h.Connect(network.WithAllowLimitedConn(cctx, "sharetoken-ctrl"), peer.AddrInfo{ID: a.coord}) == nil
}

// Kick asks for a health check now, e.g. because a call to the coordinator
// just failed. It never blocks; checks run at most every kickMinInterval.
func (a *Agent) Kick() {
	select {
	case a.kick <- struct{}{}:
	default: // one is already pending
	}
}

// kickMinInterval spaces out kicked health checks: while the coordinator is
// down every request fails and kicks, and each check may refresh the portal
// and re-measure every relay.
const kickMinInterval = 2 * time.Second

func (a *Agent) maintain() {
	health := time.NewTicker(a.HealthEvery)
	defer health.Stop()
	lastRefresh := time.Now()
	var lastCheck time.Time
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-health.C:
		case <-a.kick:
			if d := kickMinInterval - time.Since(lastCheck); d > 0 {
				select {
				case <-a.ctx.Done():
					return
				case <-time.After(d):
				}
			}
		}
		lastCheck = time.Now()
		a.syncShareRelays()
		if time.Since(lastRefresh) > a.RefreshEvery {
			lastRefresh = time.Now()
			if err := a.refreshNetwork(a.ctx); err != nil {
				a.log.Warn("refreshing the network failed", "err", err)
			}
			if err := a.bind(a.ctx); err != nil {
				a.mu.Lock()
				a.warn("login", err.Error())
				a.mu.Unlock()
			}
		}
		a.mu.Lock()
		stale := time.Since(a.measured) > a.MeasureEvery || a.current == ""
		current := a.current
		a.mu.Unlock()
		a.pingCurrent(current)
		reachable := a.CoordinatorReachable(a.ctx)
		lossy := current != "" && a.link.Lossy()
		if !reachable || lossy {
			a.mu.Lock()
			if !reachable {
				a.warn("coordinator", "coordinator unreachable through "+a.current+"; switching relay")
			} else {
				a.warn("coordinator", "relay "+a.current+" is losing pings; switching relay")
			}
			if lossy && reachable {
				// The relay still carries traffic but drops it (a congested
				// international link); it often measures fastest again right
				// away, so keep off it for a while.
				a.avoid[current] = time.Now().Add(relayAvoidFor)
				a.link.Reset(current) // judge the next relay (or this one again) afresh
			}
			a.mu.Unlock()
			if err := a.refreshNetwork(a.ctx); err == nil { // the portal knows which relays work
				lastRefresh = time.Now()
			}
			if !reachable {
				a.failover(a.ctx, current)
			} else {
				// Move off gracefully: requests in flight finish on the old path.
				a.selectRelay(a.ctx, false)
			}
			continue
		}
		a.mu.Lock()
		a.warn("coordinator", "")
		a.mu.Unlock()
		if stale {
			a.selectRelay(a.ctx, false)
		}
	}
}

// relayAvoidFor is how long a relay that failed to carry coordinator traffic
// is passed over when others are reachable.
const relayAvoidFor = 5 * time.Minute

// relaySwitchGrace is how long the old coordinator connection is kept after
// switching to a better relay.
const relaySwitchGrace = 30 * time.Second

// closeOldCoordinatorConns closes, after grace, the coordinator connections
// that do not run through relay keep; new streams then dial the new relay.
func (a *Agent) closeOldCoordinatorConns(keep peer.ID, grace time.Duration) {
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(grace):
	}
	for _, c := range a.h.Network().ConnsToPeer(a.coord) {
		if !strings.Contains(c.RemoteMultiaddr().String(), "/p2p/"+keep.String()+"/p2p-circuit") {
			_ = c.Close()
		}
	}
}

// linkWindow is how many health-check pings of the current relay are kept
// (5 minutes at the default 10 s interval).
const linkWindow = 30

// pingCurrent pings the relay carrying coordinator traffic once, for the
// live latency, jitter and loss shown in the status.
func (a *Agent) pingCurrent(relay string) {
	if relay == "" {
		return
	}
	ai, err := p2p.ParseRelay(relay)
	if err != nil {
		return
	}
	rtt, err := p2p.PingRelay(a.ctx, a.h, ai, min(5*time.Second, max(a.HealthEvery, time.Second)))
	if a.ctx.Err() != nil {
		return
	}
	a.link.Add(relay, rtt, err, time.Now())
}

// Remeasure re-measures the relays now.
func (a *Agent) Remeasure(ctx context.Context) []p2p.RelayRTT {
	a.selectRelay(ctx, false)
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.relays)
}

// ---- sharing ----

func (a *Agent) startShareLocked(sc ShareConfig) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	if !sc.Enabled {
		return nil
	}
	if u, err := url.Parse(sc.Upstream); err != nil || u.Scheme == "" {
		return fmt.Errorf("bad upstream URL %q", sc.Upstream)
	} else if u.Scheme == "http" && !cli.IsLocalURL(sc.Upstream) {
		a.warn("upstream", "prompts travel unencrypted to "+sc.Upstream+"; use https or keep the model server local")
	}
	models, err := a.shareModelsLocked(sc)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	st := &shareState{cfg: sc, models: models, ctx: ctx, cancel: cancel}
	st.reserver = p2p.NewReserver(a.h, func() {
		if st.node != nil {
			st.node.Announce() // advertise the new relay addresses at once
		}
	})
	st.reserver.Prepare = a.presentPass
	_ = st.reserver.SetRelays(a.shareRelaysLocked(sc))
	go st.reserver.Run(ctx)
	h := a.h
	svc, err := node.NewService(ctx, h, a.keys, node.Config{
		Coordinator:              peer.AddrInfo{ID: a.coord},
		Models:                   models,
		MaxConcurrency:           sc.Concurrency,
		Quote:                    a.quote,
		OnCoordinatorUnreachable: a.Kick,
		Addrs: func() []string {
			out := p2p.AddrStrings(h)
			for _, c := range st.reserver.CircuitAddrs() {
				out = append(out, c.String())
			}
			if tn := a.tailnetAddr(); tn != "" {
				out = append(out, tn)
			}
			for _, sys := range a.tailnetSystemAddrs() {
				if !slices.Contains(out, sys) {
					out = append(out, sys)
				}
			}
			return out
		},
	})
	if err != nil {
		cancel()
		return err
	}
	st.node = svc
	a.share = st
	if a.tailnet != nil && a.tailnet.Enabled() {
		go a.serveTailnet(ctx, st)
	}
	a.warn("share", "")
	for _, m := range models {
		a.log.Info("sharing", "model", m.ID, "upstream", m.UpstreamModel, "in", m.PriceInMicros, "out", m.PriceOutMicros)
	}
	return nil
}

// localSource names the model server on this machine among the sources of
// models; the others are named after their provider.
const localSource = "local"

// shareModelsLocked collects what this node sells: the models of the local
// model server and those of every provider that is on. A provider that cannot
// be used is left out with a warning; the rest is sold all the same.
func (a *Agent) shareModelsLocked(sc ShareConfig) ([]node.ModelConfig, error) {
	for key := range a.warnings {
		if strings.HasPrefix(key, "source:") {
			delete(a.warnings, key)
		}
	}
	var models []node.ModelConfig
	seen := map[string]string{}
	add := func(source string, up node.Upstream, ms []node.ModelConfig) {
		wu := &watchedUpstream{a: a, source: source, up: up}
		up = wu
		if _, ok := wu.up.(node.Compactor); ok {
			up = watchedCompactor{wu}
		}
		for _, m := range ms {
			if other, dup := seen[m.ID]; dup {
				a.warn("source:"+source+":"+m.ID, fmt.Sprintf("%s: model ID %s is already sold from %s; not sold twice", source, m.ID, other))
				continue
			}
			seen[m.ID] = source
			m.Upstream = up
			models = append(models, m)
		}
	}
	if len(sc.Models) > 0 {
		if u, err := url.Parse(sc.Upstream); err != nil || u.Scheme == "" {
			return nil, fmt.Errorf("bad upstream URL %q", sc.Upstream)
		} else if u.Scheme == "http" && !cli.IsLocalURL(sc.Upstream) {
			a.warn("upstream", "prompts travel unencrypted to "+sc.Upstream+"; use https or keep the model server local")
		}
		var apiKey string
		if sc.UpstreamKeyEnv != "" {
			apiKey = os.Getenv(sc.UpstreamKeyEnv)
		}
		add(localSource, &node.OpenAICompat{BaseURL: sc.Upstream, APIKey: apiKey}, sc.Models)
	}
	file, err := provider.LoadReady(a.home)
	if err != nil {
		a.warn("source:providers", "providers are not sold: "+err.Error())
		file = &provider.File{}
	}
	for _, e := range file.Providers {
		if !e.Selling() {
			continue
		}
		up, err := e.Upstream(a.tokens.For(e.Name))
		if err != nil {
			a.warn("source:"+e.Name, fmt.Sprintf("provider %s is not sold: %v", e.Name, err))
			continue
		}
		add(e.Name, up, e.Models)
	}
	if len(models) == 0 {
		return nil, errors.New("sharing is on but there is no model to share (`sharetoken share add <model>`, or `sharetoken provider` for a hosted API)")
	}
	return models, nil
}

// watchedUpstream tells the user when a source of models refuses its
// credentials or runs out of balance: every job fails until that is fixed,
// and each failure counts against this node.
type watchedUpstream struct {
	a      *Agent
	source string
	up     node.Upstream
}

func (w *watchedUpstream) ReadyAt() time.Time {
	if up, ok := w.up.(interface{ ReadyAt() time.Time }); ok {
		return up.ReadyAt()
	}
	return time.Time{}
}

func (w *watchedUpstream) APIs() []string { return w.up.APIs() }

func (w *watchedUpstream) Stream(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	return w.watch(w.up.Stream(ctx, r, emit))
}

// watchedCompactor is a watchedUpstream whose model server compacts natively.
type watchedCompactor struct{ *watchedUpstream }

func (w watchedCompactor) Compact(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	return w.watch(w.up.(node.Compactor).Compact(ctx, r, emit))
}

// watch tells the user about err when it says the credentials are refused,
// the balance is gone or the provider limits the rate.
func (w *watchedUpstream) watch(err error) error {
	msg := ""
	var se *node.StatusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			msg = fmt.Sprintf("%s refused its credentials (HTTP %d): jobs fail until they are replaced", w.source, se.Status)
		case http.StatusPaymentRequired:
			msg = w.source + " is out of balance (HTTP 402): jobs fail until the account is topped up"
		case http.StatusTooManyRequests:
			msg = w.source + " is temporarily rate limited (HTTP 429)"
		}
	}
	if err != nil && msg == "" {
		return err // other failures say nothing about the credentials
	}
	w.a.mu.Lock()
	w.a.warn("source:"+w.source+":credentials", msg)
	if se != nil && se.Status == http.StatusTooManyRequests && w.a.share != nil {
		w.a.share.node.Announce()
	}
	w.a.mu.Unlock()
	return err
}

// renewPassBefore fetches a new relay pass when the current one has less
// than this left (passes last an hour by default).
const renewPassBefore = 15 * time.Minute

// relayPass returns a valid relay pass from the portal, renewing it ahead of
// expiry. A nil pass with a nil error means the portal issues none (open
// relays).
func (a *Agent) relayPass(ctx context.Context) (*relaypass.Pass, error) {
	a.passMu.Lock()
	defer a.passMu.Unlock()
	if a.pass != nil && time.Until(a.pass.Expires()) > renewPassBefore {
		return a.pass, nil
	}
	p, err := a.portal.RelayPass(ctx, a.keys.ID())
	switch {
	case err == nil:
		a.pass = p
		return p, nil
	case isCode(err, "passes_disabled"):
		return nil, nil
	case a.pass != nil && time.Now().Before(a.pass.Expires()):
		return a.pass, nil // portal unreachable: keep using the pass until it expires
	}
	return nil, err
}

// presentPass shows the relay this device's pass before a reservation. It
// never fails the attempt: an open relay needs no pass, and a relay that
// requires one refuses the reservation itself.
func (a *Agent) presentPass(ctx context.Context, relay peer.AddrInfo) error {
	p, err := a.relayPass(ctx)
	if err != nil {
		a.mu.Lock()
		a.warn("relay-pass", "cannot get a relay pass from the portal: "+err.Error())
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	a.warn("relay-pass", "")
	a.mu.Unlock()
	if p == nil {
		return nil
	}
	err = relaypass.Present(ctx, a.h, relay, p)
	if err != nil {
		a.log.Debug("relay did not take the pass", "relay", relay.ID, "err", err)
	}
	a.privateRefused(relay.ID, err)
	return nil
}

func isCode(err error, code string) bool {
	pe, ok := err.(*PortalError)
	return ok && pe.Code == code
}

func (a *Agent) stopShareLocked() {
	if a.share == nil {
		return
	}
	a.share.node.Close()
	_ = a.share.reserver.SetRelays(nil)
	a.share.cancel()
	a.share = nil
	if a.tailnet != nil {
		a.tailnet.StopServing(a.h) // buyers' tunnels to other nodes stay
	}
}

// SetShare applies and saves new sharing settings.
func (a *Agent) SetShare(sc ShareConfig) error {
	if sc.Upstream == "" {
		sc.Upstream = DefaultUpstream
	}
	if sc.Concurrency <= 0 {
		sc.Concurrency = 2
	}
	if sc.Relays <= 0 {
		sc.Relays = 2
	}
	if sc.TEE == "" {
		sc.TEE = "off"
	}
	if err := sc.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if sc.Enabled && sc.TEE != "off" && a.quote == nil {
		return errors.New("TEE sharing must be configured before start-up (restart the client)")
	}
	a.stopShareLocked()
	a.cfg.Share = sc
	if err := a.startShareLocked(sc); err != nil {
		a.warn("share", err.Error())
		return err
	}
	return a.saveLocked()
}

// ReloadShare re-reads the sharing settings from the config file.
func (a *Agent) ReloadShare() error {
	c, err := LoadConfig(a.home)
	if err != nil {
		return err
	}
	if clean, err := cleanPrivateRelays(c.PrivateRelays); err != nil {
		return err
	} else {
		a.mu.Lock()
		a.cfg.PrivateRelays = clean
		a.mu.Unlock()
	}
	return a.SetShare(c.Share)
}

func (a *Agent) saveLocked() error {
	// Keep fields other tools may have changed (e.g. a new login token).
	disk, err := LoadConfig(a.home)
	if err != nil {
		return err
	}
	disk.Share = a.cfg.Share
	disk.PrivateRelays = a.cfg.PrivateRelays
	return SaveConfig(a.home, disk)
}
