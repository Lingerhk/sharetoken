package main

import (
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
	// The sample carries placeholders for the keys.
	f.Access.PortalKeys, f.Access.AllowPeers = nil, nil
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
	f.Access.PortalKeys = []string{"bogus"}
	f.Limits.ReservationTTL = config.D("1s")
	_, err = relayConfig(f)
	if err == nil || !strings.Contains(err.Error(), "portal_keys") || !strings.Contains(err.Error(), "reservation_ttl") {
		t.Fatalf("want both errors, got %v", err)
	}
}
