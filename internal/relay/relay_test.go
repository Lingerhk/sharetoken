package relay

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"

	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/relaypass"
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
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{{Name: "prod", PortalKeys: []ed25519.PublicKey{portalPub},
		AllowPeers: []peer.ID{coordKeys.PeerID}, MaxDevicesPerAccount: 2}}})
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
		"another portal's": relaypass.Issue(rogueKey, stranger.ID().String(), "user:9", relaypass.Client{}, now, time.Hour),
		"another peer's":   relaypass.Issue(portalKey, coord.ID().String(), "user:9", relaypass.Client{}, now, time.Hour),
		"an expired":       relaypass.Issue(portalKey, stranger.ID().String(), "user:9", relaypass.Client{}, now.Add(-2*time.Hour), time.Hour),
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
		if err := relaypass.Present(ctx, devices[i], ri, relaypass.Issue(portalKey, devices[i].ID().String(), "user:1", relaypass.Client{}, now, time.Hour)); err != nil {
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
	if err := relaypass.Present(ctx, other, ri, relaypass.Issue(portalKey, other.ID().String(), "user:2", relaypass.Client{}, now, time.Hour)); err != nil {
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

// Two networks on one relay: the same account name in each is two accounts, a
// network's cap binds only that network, and each coordinator gets in.
func TestRelayKeepsNetworksApart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prodPub, prodKey, _ := ed25519.GenerateKey(nil)
	devPub, devKey, _ := ed25519.GenerateKey(nil)
	prodCoord, prodCoordKeys := newHost(t)
	devCoord, devCoordKeys := newHost(t)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{
		{Name: "prod", PortalKeys: []ed25519.PublicKey{prodPub}, AllowPeers: []peer.ID{prodCoordKeys.PeerID}, MaxDevicesPerAccount: 1},
		{Name: "dev", PortalKeys: []ed25519.PublicKey{devPub}, AllowPeers: []peer.ID{devCoordKeys.PeerID}, MaxDevicesPerAccount: 1, MaxReservations: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ri := peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}
	now := time.Now()
	reserve := func(h host.Host) error { _, err := client.Reserve(ctx, h, ri); return err }
	device := func(key ed25519.PrivateKey, account string) host.Host {
		h, _ := newHost(t)
		if err := relaypass.Present(ctx, h, ri, relaypass.Issue(key, h.ID().String(), account, relaypass.Client{}, now, time.Hour)); err != nil {
			t.Fatal(err)
		}
		return h
	}
	stats := func(name string) NetworkStats {
		for _, n := range r.Stats().Networks {
			if n.Name == name {
				return n
			}
		}
		t.Fatalf("no stats for %s", name)
		return NetworkStats{}
	}

	if err := reserve(prodCoord); err != nil {
		t.Fatalf("prod coordinator: %v", err)
	}
	if err := reserve(devCoord); err != nil {
		t.Fatalf("dev coordinator: %v", err)
	}

	// user:3 of dev holds its one device slot; user:3 of prod is someone else.
	if err := reserve(device(devKey, "user:3")); err != nil {
		t.Fatalf("dev user:3: %v", err)
	}
	if err := reserve(device(prodKey, "user:3")); err != nil {
		t.Fatalf("prod user:3 blocked by dev user:3: %v", err)
	}
	// Within one network the limit still holds.
	if err := reserve(device(devKey, "user:3")); err == nil {
		t.Fatal("second device of dev user:3 reserved")
	}
	if err := reserve(device(prodKey, "user:3")); err == nil {
		t.Fatal("second device of prod user:3 reserved")
	}

	// dev's cap is 2 and its coordinator does not count: one more, then none.
	if err := reserve(device(devKey, "user:4")); err != nil {
		t.Fatalf("dev reservation within the cap: %v", err)
	}
	if err := reserve(device(devKey, "user:5")); err == nil {
		t.Fatal("dev reserved over its cap")
	}
	// prod is unaffected by dev being full.
	if err := reserve(device(prodKey, "user:5")); err != nil {
		t.Fatalf("prod blocked by dev's cap: %v", err)
	}

	if s := stats("dev"); s.Reservations != 3 || s.ReservationsDenied != 2 || s.PassesAccepted != 4 {
		t.Fatalf("dev stats: %+v", s)
	}
	if s := stats("prod"); s.Reservations != 3 || s.ReservationsDenied != 1 || s.PassesAccepted != 3 {
		t.Fatalf("prod stats: %+v", s)
	}
}

// A pass signed by no listed network is refused, whichever network comes first.
func TestRelayRefusesUnknownPortal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, _, _ := ed25519.GenerateKey(nil)
	b, _, _ := ed25519.GenerateKey(nil)
	_, rogue, _ := ed25519.GenerateKey(nil)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{
		{Name: "a", PortalKeys: []ed25519.PublicKey{a}}, {Name: "b", PortalKeys: []ed25519.PublicKey{b}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h, _ := newHost(t)
	ri := peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}
	err = relaypass.Present(ctx, h, ri, relaypass.Issue(rogue, h.ID().String(), "user:1", relaypass.Client{}, time.Now(), time.Hour))
	if protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("want unauthorized, got %v", err)
	}
	if st := r.Stats(); st.PassesRejected != 1 || st.PassesAccepted != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestNewRefusesAmbiguousNetworks(t *testing.T) {
	k, _, _ := ed25519.GenerateKey(nil)
	rk, _ := p2p.NewKeys()
	_, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{
		{Name: "a", PortalKeys: []ed25519.PublicKey{k}}, {Name: "b", PortalKeys: []ed25519.PublicKey{k}}}})
	if err == nil {
		t.Fatal("two networks with one portal key were accepted")
	}
}

// Without networks the relay is open, as before.
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

// A pass frame is a few hundred bytes; a peer that announces a large one is
// cut off before it can send it.
func TestRelayRefusesOversizedPassFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	portalPub, _, _ := ed25519.GenerateKey(nil)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{{Name: "prod", PortalKeys: []ed25519.PublicKey{portalPub}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dev, _ := newHost(t)
	if err := dev.Connect(ctx, peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := dev.NewStream(ctx, r.Host.ID(), lproto.ID(relaypass.Protocol))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	// A 1 MiB frame: the relay must reset the stream without reading it.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 1<<20)
	if _, err := s.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.Recv(s); err == nil {
		t.Fatal("the relay answered an oversized pass frame")
	}
}
