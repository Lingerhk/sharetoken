package relay

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"

	"sharetoken/p2p"
	"sharetoken/protocol"
	"sharetoken/relaypass"
)

func newHost(t *testing.T) (host.Host, *p2p.Keys) {
	t.Helper()
	k, _ := p2p.NewKeys()
	h, err := p2p.NewHost(k, p2p.HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, k
}

// A relay with a portal key reserves only for devices that present a valid
// pass (or are allow-listed), and at most MaxDevicesPerAccount per account.
func TestRelayAdmitsOnlyPassHolders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	portalPub, portalKey, _ := ed25519.GenerateKey(nil)
	_, rogueKey, _ := ed25519.GenerateKey(nil)
	coord, coordKeys := newHost(t)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, PortalKeys: []ed25519.PublicKey{portalPub},
		AllowPeers: []peer.ID{coordKeys.PeerID}, MaxDevicesPerAccount: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Open() {
		t.Fatal("relay with a portal key must not be open")
	}
	ri := peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}
	reserve := func(h host.Host) error { _, err := client.Reserve(ctx, h, ri); return err }
	now := time.Now()

	// No pass: refused. The allow-listed coordinator needs none.
	stranger, _ := newHost(t)
	if err := reserve(stranger); err == nil {
		t.Fatal("reserved without a pass")
	}
	if err := reserve(coord); err != nil {
		t.Fatalf("allow-listed coordinator: %v", err)
	}

	// Passes the relay must refuse.
	for name, pass := range map[string]*relaypass.Pass{
		"another portal's": relaypass.Issue(rogueKey, stranger.ID().String(), "user:9", now, time.Hour),
		"another peer's":   relaypass.Issue(portalKey, coord.ID().String(), "user:9", now, time.Hour),
		"an expired":       relaypass.Issue(portalKey, stranger.ID().String(), "user:9", now.Add(-2*time.Hour), time.Hour),
	} {
		if err := relaypass.Present(ctx, stranger, ri, pass); protocol.CodeOf(err) != protocol.CodeUnauthorized {
			t.Fatalf("%s pass: want unauthorized, got %v", name, err)
		}
	}
	if err := reserve(stranger); err == nil {
		t.Fatal("reserved after presenting only bad passes")
	}

	// Valid passes: two devices of user:1 may reserve, a third may not;
	// another account is unaffected.
	devices := make([]host.Host, 3)
	for i := range devices {
		devices[i], _ = newHost(t)
		if err := relaypass.Present(ctx, devices[i], ri, relaypass.Issue(portalKey, devices[i].ID().String(), "user:1", now, time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := reserve(devices[0]); err != nil {
		t.Fatalf("first device: %v", err)
	}
	if err := reserve(devices[1]); err != nil {
		t.Fatalf("second device: %v", err)
	}
	if err := reserve(devices[2]); err == nil {
		t.Fatal("third device of the same account reserved")
	}
	other, _ := newHost(t)
	if err := relaypass.Present(ctx, other, ri, relaypass.Issue(portalKey, other.ID().String(), "user:2", now, time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := reserve(other); err != nil {
		t.Fatalf("other account: %v", err)
	}
	// A device that leaves frees its account's slot.
	_ = devices[0].Close()
	time.Sleep(200 * time.Millisecond)
	if err := reserve(devices[2]); err != nil {
		t.Fatalf("after a device left: %v", err)
	}
	if st := r.Stats(); st.PassesAccepted != 4 || st.PassesRejected != 3 {
		t.Fatalf("stats: %+v", st)
	}
}

// Without portal keys the relay is open, as before.
func TestRelayWithoutKeysIsOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h, _ := newHost(t)
	if _, err := client.Reserve(ctx, h, peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
}
