package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/tailnet"
)

// The tailnet (package tailnet) is a second way for a buyer and a seller to
// reach each other directly: a WireGuard tunnel over Tailscale's data plane,
// started through a DERP relay and moved to a direct path by its NAT
// traversal. It is off unless the user turns it on, and it needs both ends
// on: a sharing node then adds a /tailcat address to its offer, and a buyer
// dials that address next to the node's others. Nothing about the
// coordinator, the circuit relays or the protocols changes; a client without
// the tailnet skips the address.

// TailnetConfig is the tailnet section of the configuration.
type TailnetConfig struct {
	Enabled bool `json:"enabled"`
	// DERPMap names the DERP relays tunnels may start through: empty for
	// tailcat's public relays, the URL of a DERP map, or the path of a file
	// holding one (a network that runs its own). Buyer and seller need the
	// same relays: a buyer follows an address only to relays of its own map.
	DERPMap string `json:"derp_map,omitempty"`
	// WaitMillis is how long a job waits for the tunnel to a node it has no
	// direct connection to yet, before it goes through the relay as it would
	// without the tailnet (the tunnel then takes over for the next stream).
	// 0: do not wait.
	WaitMillis int `json:"wait_ms,omitempty"`
}

// MaxTailnetWaitMillis bounds TailnetConfig.WaitMillis: a job is not held
// longer for a path it does not need.
const MaxTailnetWaitMillis = 10_000

func (c TailnetConfig) validate() error {
	if c.WaitMillis < 0 || c.WaitMillis > MaxTailnetWaitMillis {
		return fmt.Errorf("tailnet.wait_ms must be between 0 and %d", MaxTailnetWaitMillis)
	}
	return nil
}

// TailnetStatus is the tailnet as the local API shows it.
type TailnetStatus struct {
	tailnet.Status
	WaitMillis int `json:"wait_ms,omitempty"`
}

// newTailnet makes the agent's tailnet transport and the libp2p option that
// installs it. The transport is always installed, on or off: turning it on
// later needs no new host. A DERP map that cannot be read leaves the tailnet
// off with a warning rather than keeping the client from starting.
func (a *Agent) newTailnet() libp2p.Option {
	cfg := tailnet.Config{Enabled: a.cfg.Tailnet.Enabled, DERPMap: a.cfg.Tailnet.DERPMap, Dir: a.home, Log: a.log}
	tn, opt, err := tailnet.New(cfg)
	if err != nil {
		a.warn("tailnet", "tailnet off: "+err.Error())
		cfg.Enabled, cfg.DERPMap = false, ""
		if tn, opt, err = tailnet.New(cfg); err != nil {
			return nil // cannot happen: the default source reads nothing
		}
	}
	a.tailnet = tn
	return opt
}

// tailnetHooks points the buyer at the tailnet.
func (a *Agent) tailnetHooks() {
	if a.tailnet == nil {
		return
	}
	a.buyer.Direct = func(ctx context.Context, node peer.ID, addrs []ma.Multiaddr, mayWait bool) {
		var wait time.Duration
		if mayWait {
			a.mu.Lock()
			wait = time.Duration(a.cfg.Tailnet.WaitMillis) * time.Millisecond
			a.mu.Unlock()
		}
		a.tailnet.Reach(ctx, a.h, node, addrs, wait)
	}
	a.buyer.ConnPath = func(c network.Conn) string {
		p, _ := a.tailnet.PathOf(c)
		return p
	}
}

// serveTailnet makes the sharing node reachable over the tailnet and
// announces the address; it tries again, ever more rarely, while the relays
// do not answer. It ends with the share (ctx).
func (a *Agent) serveTailnet(ctx context.Context, st *shareState) {
	backoff := 5 * time.Second
	for {
		if !a.tailnet.Enabled() {
			return
		}
		sctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		_, err := a.tailnet.Serve(sctx, a.h)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			a.mu.Lock()
			a.warn("tailnet", "")
			a.mu.Unlock()
			if st.node != nil {
				st.node.Announce()
			}
			return
		}
		a.mu.Lock()
		a.warn("tailnet", "not reachable over the tailnet (buyers use the relays): "+err.Error())
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 5*time.Minute)
	}
}

// tailnetAddr is the address a sharing node adds to its offer, "" while it
// has none.
func (a *Agent) tailnetAddr() string {
	if a.tailnet == nil || !a.tailnet.Enabled() {
		return ""
	}
	if m := a.tailnet.Addr(); m != nil {
		return m.String()
	}
	return ""
}

// tailnetSystemAddrs is where the host listens on a Tailscale network of
// the OS, for the offer of a sharing node with the tailnet on.
func (a *Agent) tailnetSystemAddrs() []string {
	if a.tailnet == nil || !a.tailnet.Enabled() {
		return nil
	}
	var out []string
	for _, m := range tailnet.SystemAddrs(a.h.Network().ListenAddresses()) {
		out = append(out, m.String())
	}
	return out
}

// TailnetStatus reports the tailnet.
func (a *Agent) TailnetStatus() TailnetStatus {
	a.mu.Lock()
	wait := a.cfg.Tailnet.WaitMillis
	a.mu.Unlock()
	if a.tailnet == nil {
		return TailnetStatus{WaitMillis: wait}
	}
	return TailnetStatus{Status: a.tailnet.Status(), WaitMillis: wait}
}

// SetTailnet applies new tailnet settings to the running client and saves
// them. Turning it off, or changing the relays, ends the tunnels at once:
// jobs on them resume over the nodes' other paths.
func (a *Agent) SetTailnet(tc TailnetConfig) error {
	if err := tc.validate(); err != nil {
		return err
	}
	if a.tailnet == nil {
		return fmt.Errorf("this client has no tailnet")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.cfg.Tailnet
	mapChanged := tc.DERPMap != old.DERPMap
	if mapChanged {
		if err := a.tailnet.SetDERPMap(tc.DERPMap); err != nil {
			return err
		}
	}
	if err := saveTailnet(a.home, tc); err != nil {
		return err
	}
	a.cfg.Tailnet = tc
	if !tc.Enabled || mapChanged {
		a.tailnet.SetEnabled(false) // before Reset: nothing may start meanwhile
		a.tailnet.Reset(a.h)
	}
	a.tailnet.SetEnabled(tc.Enabled)
	a.warn("tailnet", "")
	if st := a.share; st != nil {
		if tc.Enabled && (!old.Enabled || mapChanged) {
			go a.serveTailnet(st.ctx, st)
		} else if st.node != nil {
			st.node.Announce() // the offer loses, or keeps, the address
		}
	}
	return nil
}

// saveTailnet writes the tailnet settings to config.json, keeping its other
// fields.
func saveTailnet(home string, tc TailnetConfig) error {
	disk, err := LoadConfig(home)
	if err != nil {
		return err
	}
	disk.Tailnet = tc
	return SaveConfig(home, disk)
}

// ReloadTailnet takes up the tailnet settings of the config file (the CLI
// writes it and then asks for this).
func (a *Agent) ReloadTailnet() error {
	c, err := LoadConfig(a.home)
	if err != nil {
		return err
	}
	return a.SetTailnet(c.Tailnet)
}

func (a *Agent) handleSetTailnet(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	tc := a.cfg.Tailnet
	a.mu.Unlock()
	// Fields left out keep their value.
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&tc); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}
	if err := a.SetTailnet(tc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.TailnetStatus())
}
