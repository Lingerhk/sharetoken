package accounts

import (
	"errors"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/p2p"
)

// An account binds a bounded number of devices: the next one is refused with
// a message that says what to do, a device already bound can still be
// renamed, and removing one makes room.
func TestDevicesPerUserAreCapped(t *testing.T) {
	s := testStore(t)
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	u, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bind := func(u *User, k *p2p.Keys, name string) error {
		ts := now.UnixNano()
		_, err := s.BindDevice(u, k.ID(), name, ts, SignBind(k.Signer, u.Username, k.ID(), ts))
		return err
	}
	var first *p2p.Keys
	for i := range MaxDevicesPerUser {
		k, err := p2p.NewKeys()
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = k
		}
		if err := bind(u, k, "device"); err != nil {
			t.Fatalf("device %d: %v", i+1, err)
		}
	}
	extra, _ := p2p.NewKeys()
	err = bind(u, extra, "one more")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "remove one") {
		t.Fatalf("device %d: want a refusal that says to remove one, got %v", MaxDevicesPerUser+1, err)
	}
	if err := bind(u, first, "renamed"); err != nil {
		t.Fatalf("renaming a bound device at the limit: %v", err)
	}
	if err := bind(other, extra, "bob's"); err != nil {
		t.Fatalf("another account: %v", err)
	}
	if err := s.UnbindDevice(u.ID, first.ID()); err != nil {
		t.Fatal(err)
	}
	again, _ := p2p.NewKeys()
	if err := bind(u, again, "after removing one"); err != nil {
		t.Fatalf("after removing a device: %v", err)
	}
}
