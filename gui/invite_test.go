package main

import (
	"errors"
	"testing"
)

var errStop = errors.New("stop")

func TestInviteArg(t *testing.T) {
	for args, want := range map[string]string{
		"":                                      "",
		"--flag":                                "",
		"sharetoken://p/7kQm3xV9pT2b":           "sharetoken://p/7kQm3xV9pT2b",
		"SHARETOKEN://s/7kQm3xV9pT2b?h=x":       "SHARETOKEN://s/7kQm3xV9pT2b?h=x",
		"https://sharetoken.app/p#7kQm3xV9pT2b": "",
		"sharetoken:":                           "",
	} {
		if got := inviteArg([]string{"/path/sharetoken-gui", args}); got != want {
			t.Errorf("%q: %q, want %q", args, got, want)
		}
	}
}

func TestInviteForwarderOncePerClick(t *testing.T) {
	sent := 0
	f := &inviteForwarder{start: func() error { sent++; return errStop }, ep: func() endpoint { return endpoint{} }}
	f.forward("sharetoken://p/7kQm3xV9pT2b")
	f.forward("sharetoken://p/7kQm3xV9pT2b")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last != "sharetoken://p/7kQm3xV9pT2b" {
		t.Fatalf("last: %q", f.last)
	}
}
