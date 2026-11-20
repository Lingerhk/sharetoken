package relay

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"

	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/relaypass"
)

// A private relay reserves only for devices of the accounts it lists; other
// users of the same portal are told so. Anyone may still connect through it
// to a device that reserved.
func TestPrivateRelayAdmitsOnlyItsAccounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	portalPub, portalKey, _ := ed25519.GenerateKey(nil)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{{Name: "mine",
		PortalKeys: []ed25519.PublicKey{portalPub}, AllowAccounts: []string{"user:3", "user:7"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ri := peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}
	reserve := func(h host.Host) error { _, err := client.Reserve(ctx, h, ri); return err }
	pass := func(h host.Host, account string) error {
		return relaypass.Present(ctx, h, ri, relaypass.Issue(portalKey, h.ID().String(), account, relaypass.Client{}, time.Now(), time.Hour))
	}

	owner, _ := newHost(t)
	if err := pass(owner, "user:3"); err != nil {
		t.Fatalf("the owner's pass: %v", err)
	}
	if err := reserve(owner); err != nil {
		t.Fatalf("the owner's reservation: %v", err)
	}
	other, _ := newHost(t)
	if err := pass(other, "user:4"); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("another user's pass: want forbidden, got %v", err)
	}
	if err := reserve(other); err == nil {
		t.Fatal("another user reserved on a private relay")
	}
	st := r.Stats().Networks[0]
	if !st.Private || st.AllowedAccounts != 2 || st.PassesRefused != 1 || st.PassesAccepted != 1 || st.Reservations != 1 {
		t.Fatalf("stats: %+v", st)
	}

	// The other user, a buyer, reaches the owner's device through the relay.
	circuit := p2p.CircuitAddrs(ri)
	if err := other.Connect(ctx, peer.AddrInfo{ID: owner.ID(), Addrs: circuit}); err != nil {
		t.Fatalf("connecting through the private relay: %v", err)
	}
}

// A network learns a portal's new key while running, but never a key that
// another network answers for.
func TestRelayLearnsANewPortalKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	oldPub, _, _ := ed25519.GenerateKey(nil)
	newPub, newKey, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{
		{Name: "mine", PortalKeys: []ed25519.PublicKey{oldPub}, AllowAccounts: []string{"user:3"}},
		{Name: "other", PortalKeys: []ed25519.PublicKey{otherPub}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ri := peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}
	dev, _ := newHost(t)
	p := relaypass.Issue(newKey, dev.ID().String(), "user:3", relaypass.Client{}, time.Now(), time.Hour)
	if err := relaypass.Present(ctx, dev, ri, p); protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("a pass under an unknown key: %v", err)
	}
	if added, err := r.AddPortalKey("mine", newPub); err != nil || !added {
		t.Fatalf("adding the key: %v %v", added, err)
	}
	if added, err := r.AddPortalKey("mine", newPub); err != nil || added {
		t.Fatalf("adding it again: %v %v", added, err)
	}
	if _, err := r.AddPortalKey("mine", otherPub); err == nil {
		t.Fatal("took a key of another network")
	}
	if _, err := r.AddPortalKey("nobody", newPub); err == nil {
		t.Fatal("took a key for an unknown network")
	}
	if err := relaypass.Present(ctx, dev, ri, p); err != nil {
		t.Fatalf("a pass under the learned key: %v", err)
	}
}
