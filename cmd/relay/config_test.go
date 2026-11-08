package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sharetoken/internal/config"
)

func TestExampleConfigLoads(t *testing.T) {
	f := defaults()
	if err := config.Load("../../configs/relay.example.yaml", &f); err != nil {
		t.Fatal(err)
	}
	if f.Limits.CircuitBuffer != 16<<10 || f.Limits.ReservationTTL != config.D("1h") || len(f.Network.Listen) != 4 {
		t.Fatalf("%+v", f)
	}
	if len(f.Access.Networks) != 2 || f.Access.Networks[1].MaxReservations != 32 {
		t.Fatalf("networks: %+v", f.Access.Networks)
	}
	// The sample carries placeholders for the keys.
	f.Access.Networks = nil
	if _, err := relayConfig(f); err != nil {
		t.Fatal(err)
	}
}

func TestFlagsOverrideFile(t *testing.T) {
	f, _, err := load([]string{"-config", "../../configs/relay.example.yaml", "-listen", "/ip4/0.0.0.0/tcp/8091",
		"-announce", "/dns4/relay-cn.sharetoken.app/tcp/8091", "-max-circuits", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.Network.Listen, ",") != "/ip4/0.0.0.0/tcp/8091" || f.Limits.MaxCircuits != 7 || f.Limits.MaxReservations != 4096 {
		t.Fatalf("%+v", f)
	}
	if !strings.HasSuffix(f.Identity.KeyFile, "configs/relay.key") {
		t.Fatalf("key file not resolved against the config dir: %s", f.Identity.KeyFile)
	}
	f.Access.Networks = []NetworkFile{{Name: "prod", PortalKeys: []string{"bogus"}}}
	f.Limits.ReservationTTL = config.D("1s")
	_, err = relayConfig(f)
	if err == nil || !strings.Contains(err.Error(), "portal_keys") || !strings.Contains(err.Error(), "reservation_ttl") {
		t.Fatalf("want both errors, got %v", err)
	}
}

// Old-style access settings fail loudly rather than start an open relay.
func TestOldAccessFormatIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.yaml")
	if err := os.WriteFile(path, []byte("version: 1\naccess:\n  portal_keys: [ed25519:00]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := defaults()
	if err := config.Load(path, &f); err == nil || !strings.Contains(err.Error(), "portal_keys") {
		t.Fatalf("want an unknown-field error, got %v", err)
	}
}

func TestNetworksMustBeDistinct(t *testing.T) {
	const key = "ed25519:0c8928fec72a6b1502eb83e8104cfdc56d740c54d040e18a7eed05edaaeb8dc3"
	const coord = "12D3KooWFAqCYkUS2Kt2KRWQ1mWF82Em4mRbB5nD4V7Fr5PmWfUh"
	const key2 = "ed25519:1c8928fec72a6b1502eb83e8104cfdc56d740c54d040e18a7eed05edaaeb8dc3"
	for name, c := range map[string]struct {
		nets []NetworkFile
		want string
	}{
		"no name":     {[]NetworkFile{{PortalKeys: []string{key}}}, "no name"},
		"same name":   {[]NetworkFile{{Name: "a", PortalKeys: []string{key}}, {Name: "a", PortalKeys: []string{key2}}}, "twice"},
		"no key":      {[]NetworkFile{{Name: "a"}}, "no portal key"},
		"shared key":  {[]NetworkFile{{Name: "a", PortalKeys: []string{key}}, {Name: "b", PortalKeys: []string{key}}}, "share a portal key"},
		"shared peer": {[]NetworkFile{{Name: "a", PortalKeys: []string{key}, AllowPeers: []string{coord}}, {Name: "b", PortalKeys: []string{key2}, AllowPeers: []string{coord}}}, "share the allowed peer"},
	} {
		f := defaults()
		f.Access.Networks = c.nets
		if _, err := relayConfig(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}
