package client

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// Private relays are relays the user runs (cmd/relay, `relay init`) and lists
// in the configuration. The network's own relays, which the portal lists,
// carry everything else:
//
//   - Traffic with the coordinator never uses a private relay: the
//     coordinator holds no reservation there, and whoever runs the relay
//     would otherwise see this device talk to it. Private relays are kept in
//     their own list (Agent.private) that the relay choice never reads.
//   - A sharing node reserves on its private relays and, while one of them
//     holds a reservation, on no other: buyers then reach it through the
//     user's relay only (or directly). The network's relays are the fallback
//     while no private relay works, unless share.keep_official keeps them.
//
// Buying needs nothing: a buyer dials whatever addresses the node's offer has.

// defaultPrivateGrace is how long a node waits for its private relays, at
// start and after losing them, before it reserves on the network's relays
// instead (Agent.PrivateGrace).
const defaultPrivateGrace = 30 * time.Second

// privateRelaysLocked returns the configured private relays that parse and
// are not the network's own.
func (a *Agent) privateRelaysLocked() []string {
	var out []string
	seen := map[peer.ID]bool{}
	for _, r := range a.netinfo.Relays {
		if ai, err := p2p.ParseRelay(r.Addr); err == nil {
			seen[ai.ID] = true
		}
	}
	for _, addr := range a.cfg.PrivateRelays {
		ai, err := p2p.ParseRelay(addr)
		if err != nil || seen[ai.ID] {
			continue
		}
		seen[ai.ID] = true
		out = append(out, addr)
	}
	return out
}

// officialWantedLocked reports whether the node should hold reservations on
// the network's relays: always without private relays, else only when asked
// to keep them or while no private relay has worked for privateGrace.
func (a *Agent) officialWantedLocked(sc ShareConfig, private []string, rsv *p2p.Reserver) bool {
	if len(private) == 0 || sc.KeepOfficial {
		a.privateDown = time.Time{}
		return true
	}
	if rsv != nil {
		for _, st := range rsv.Status() {
			if st.Reserved && slices.Contains(private, st.Addr) {
				a.privateDown = time.Time{}
				return false
			}
		}
	}
	if a.privateDown.IsZero() {
		a.privateDown = time.Now()
	}
	return time.Since(a.privateDown) > cmp.Or(a.PrivateGrace, defaultPrivateGrace)
}

// syncShareRelays re-decides which relays the node reserves on; the answer
// changes as private relays come and go.
func (a *Agent) syncShareRelays() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.share == nil || len(a.cfg.PrivateRelays) == 0 {
		return
	}
	want := a.shareRelaysLocked(a.share.cfg)
	var have []string
	for _, st := range a.share.reserver.Status() {
		have = append(have, st.Addr)
	}
	slices.Sort(want)
	if !slices.Equal(want, have) {
		_ = a.share.reserver.SetRelays(want)
	}
}

// privateRefused records whether a private relay refused this device's pass
// because its account is not on the relay's list.
func (a *Agent) privateRefused(relay peer.ID, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := "relay-private:" + relay.String()
	if protocol.CodeOf(err) == protocol.CodeForbidden {
		a.warn(key, fmt.Sprintf("private relay %s does not list your account: its owner adds the account shown by `sharetoken account` to allow_accounts", shortPeer(relay)))
		return
	}
	a.warn(key, "")
}

func shortPeer(id peer.ID) string {
	s := id.String()
	if len(s) > 12 {
		return s[:4] + "…" + s[len(s)-4:]
	}
	return s
}

// PrivateRelays returns the configured private relays.
func (a *Agent) PrivateRelays() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.cfg.PrivateRelays)
}

// SetPrivateRelays replaces the private relays, saves them, and applies
// them to a running node.
func (a *Agent) SetPrivateRelays(ctx context.Context, addrs []string) error {
	clean, err := cleanPrivateRelays(addrs)
	if err != nil {
		return err
	}
	a.mu.Lock()
	for _, addr := range clean {
		ai, _ := p2p.ParseRelay(addr)
		if ai.ID == a.coord || ai.ID.String() == a.keys.ID() {
			a.mu.Unlock()
			return fmt.Errorf("%s is not a relay", ai.ID)
		}
		for _, r := range a.netinfo.Relays {
			if o, err := p2p.ParseRelay(r.Addr); err == nil && o.ID == ai.ID {
				a.mu.Unlock()
				return fmt.Errorf("%s is one of the network's relays already", cmpName(r.Name, ai.ID.String()))
			}
		}
	}
	for k := range a.warnings {
		if len(k) > len("relay-private:") && k[:len("relay-private:")] == "relay-private:" {
			delete(a.warnings, k)
		}
	}
	a.cfg.PrivateRelays = clean
	a.privateDown = time.Time{}
	err = a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	a.measurePrivate(ctx)
	a.syncShareRelaysNow()
	return nil
}

// syncShareRelaysNow applies a changed list at once, also when it is empty.
func (a *Agent) syncShareRelaysNow() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.share != nil {
		_ = a.share.reserver.SetRelays(a.shareRelaysLocked(a.share.cfg))
	}
}

func cmpName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// maxPrivateRelays bounds the list: each is measured and reserved on.
const maxPrivateRelays = 8

// cleanPrivateRelays checks a list of private relays and drops repeats.
func cleanPrivateRelays(addrs []string) ([]string, error) {
	var out []string
	seen := map[peer.ID]bool{}
	for _, addr := range addrs {
		ai, err := p2p.ParseRelay(addr)
		if err != nil {
			return nil, fmt.Errorf("private relay %q: %w", addr, err)
		}
		if seen[ai.ID] {
			continue
		}
		seen[ai.ID] = true
		out = append(out, p2p.RelayAddr(ai))
	}
	if len(out) > maxPrivateRelays {
		return nil, fmt.Errorf("at most %d private relays", maxPrivateRelays)
	}
	return out, nil
}

// measurePrivate measures the private relays, for the status only.
func (a *Agent) measurePrivate(ctx context.Context) {
	a.mu.Lock()
	addrs := a.privateRelaysLocked()
	a.mu.Unlock()
	var res []p2p.RelayRTT
	if len(addrs) > 0 {
		res = p2p.MeasureRelays(ctx, a.h, addrs, 3)
	}
	a.mu.Lock()
	a.private = res
	a.mu.Unlock()
}

func (a *Agent) privateRelayRoutes(mux *http.ServeMux) {
	type body struct {
		Addr string `json:"addr"`
	}
	read := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		if !jsonContent(r) {
			writeErrorCode(w, http.StatusUnsupportedMediaType, "bad_request", "send application/json")
			return "", false
		}
		var b body
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil || b.Addr == "" {
			writeError(w, http.StatusBadRequest, "want {\"addr\": \"<relay address or peer ID>\"}")
			return "", false
		}
		return b.Addr, true
	}
	done := func(w http.ResponseWriter, err error) {
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.Status().Relays)
	}
	mux.HandleFunc("POST /api/v1/relays/private", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := read(w, r)
		if !ok {
			return
		}
		ai, err := p2p.ParseRelay(addr)
		if err != nil {
			done(w, err)
			return
		}
		// The same relay again replaces its address.
		list := slices.DeleteFunc(a.PrivateRelays(), func(o string) bool {
			oi, err := p2p.ParseRelay(o)
			return err == nil && oi.ID == ai.ID
		})
		done(w, a.SetPrivateRelays(r.Context(), append(list, addr)))
	})
	mux.HandleFunc("DELETE /api/v1/relays/private", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := read(w, r)
		if !ok {
			return
		}
		cur := a.PrivateRelays()
		list := slices.DeleteFunc(slices.Clone(cur), func(o string) bool {
			if o == addr {
				return true
			}
			oi, err := p2p.ParseRelay(o)
			if err != nil {
				return false
			}
			if ai, err := p2p.ParseRelay(addr); err == nil && ai.ID == oi.ID {
				return true
			}
			return oi.ID.String() == addr
		})
		if len(list) == len(cur) {
			done(w, errors.New("no such private relay"))
			return
		}
		done(w, a.SetPrivateRelays(r.Context(), list))
	})
}
