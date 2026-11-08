package client

import (
	"slices"
	"testing"
	"time"

	"sharetoken/node"
	"sharetoken/p2p"
)

func rtt(addr string, ms float64) p2p.RelayRTT {
	return p2p.RelayRTT{Addr: addr, RTT: time.Duration(ms * float64(time.Millisecond))}
}

func TestChooseRelay(t *testing.T) {
	all := func(string) bool { return true }
	down := p2p.RelayRTT{Addr: "dead", Err: "connection refused"}
	cases := []struct {
		name    string
		res     []p2p.RelayRTT
		current string
		failed  bool
		okFn    func(string) bool
		avoid   string
		want    string
	}{
		{"fastest when nothing is in use", []p2p.RelayRTT{rtt("a", 3), rtt("b", 40), down}, "", false, all, "", "a"},
		{"skips relays the coordinator is not reachable through", []p2p.RelayRTT{rtt("a", 3), rtt("b", 40)}, "", false,
			func(s string) bool { return s == "b" }, "", "b"},
		{"falls back to any reachable relay", []p2p.RelayRTT{rtt("a", 3)}, "", false, func(string) bool { return false }, "", "a"},
		{"stays on current within the margin", []p2p.RelayRTT{rtt("a", 30), rtt("b", 33)}, "b", false, all, "", "b"},
		{"switches when clearly faster", []p2p.RelayRTT{rtt("a", 10), rtt("b", 40)}, "b", false, all, "", "a"},
		{"switches when current failed", []p2p.RelayRTT{rtt("a", 30), rtt("b", 31)}, "b", true, all, "", "a"},
		{"switches away from an unreachable current", []p2p.RelayRTT{rtt("a", 30), down}, "dead", false, all, "", "a"},
		{"none reachable", []p2p.RelayRTT{down}, "", false, all, "", ""},
		// relay-sg down, relay-cn fastest but just failed to carry coordinator traffic
		{"passes over an avoided relay", []p2p.RelayRTT{rtt("cn", 241), rtt("us", 255), down}, "cn", true, all, "cn", "us"},
		{"leaves an avoided current for a slower one", []p2p.RelayRTT{rtt("cn", 241), rtt("us", 255)}, "cn", false, all, "cn", "us"},
		{"avoided beats unknown coordinator status", []p2p.RelayRTT{rtt("cn", 241), rtt("us", 255)}, "", false,
			func(s string) bool { return s == "cn" }, "cn", "us"},
		{"an avoided relay beats none", []p2p.RelayRTT{rtt("cn", 241), down}, "cn", true, all, "cn", "cn"},
	}
	for _, c := range cases {
		got := chooseRelay(c.res, c.current, c.failed, c.okFn, func(a string) bool { return a == c.avoid })
		if (got == nil && c.want != "") || (got != nil && got.Addr != c.want) {
			t.Errorf("%s: got %v, want %q", c.name, got, c.want)
		}
	}
}

func TestLocalOrigin(t *testing.T) {
	for origin, want := range map[string]bool{
		"http://localhost:5173":     true,
		"http://127.0.0.1:9787":     true,
		"http://[::1]:3000":         true,
		"http://tauri.localhost":    true,
		"tauri://localhost":         true,
		"wails://wails":             true,
		"https://evil.example":      false,
		"http://192.168.1.10:8080":  false,
		"null":                      false,
		"http://localhost.evil.com": false,
	} {
		if got := localOrigin(origin); got != want {
			t.Errorf("localOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestConfigRoundTripAndValidation(t *testing.T) {
	home := t.TempDir()
	c, err := LoadConfig(home)
	if err != nil || c.HTTP != DefaultHTTP || !c.NAT || !c.Buy.Evidence || c.Share.Relays != 2 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c.Portal, c.Token = "http://portal/", "st_x"
	c.Share.Models = []node.ModelConfig{{ID: "m", UpstreamModel: "m:7b", PriceInMicros: 1, PriceOutMicros: 2}}
	c.Share.Enabled = true
	if err := SaveConfig(home, c); err != nil {
		t.Fatal(err)
	}
	back, err := LoadConfig(home)
	if err != nil || back.Portal != "http://portal" || back.Token != "st_x" || len(back.Share.Models) != 1 || !back.Share.Enabled {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	bad := ShareConfig{Enabled: true}
	if bad.Validate() == nil {
		t.Fatal("sharing with no model must be refused")
	}
	bad = ShareConfig{Models: []node.ModelConfig{{ID: "m", UpstreamModel: "x"}, {ID: "m", UpstreamModel: "y"}}}
	if bad.Validate() == nil {
		t.Fatal("duplicate ids must be refused")
	}
}

// Configs saved with the old IPv4-only default listen on IPv6 too; an
// explicit choice is kept.
func TestConfigListensOnIPv6ByDefault(t *testing.T) {
	c := Config{Listen: []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"}}
	c.fill()
	if !slices.Equal(c.Listen, DefaultListen()) || !slices.Contains(c.Listen, "/ip6/::/tcp/0") {
		t.Fatalf("old default not upgraded: %v", c.Listen)
	}
	c = Config{Listen: []string{"/ip4/0.0.0.0/tcp/4001"}}
	c.fill()
	if !slices.Equal(c.Listen, []string{"/ip4/0.0.0.0/tcp/4001"}) {
		t.Fatalf("explicit listen changed: %v", c.Listen)
	}
}

func TestPruneAvoid(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	down := p2p.RelayRTT{Addr: "sg", Err: "connection refused"}
	res := []p2p.RelayRTT{rtt("cn", 241), rtt("us", 255), down}

	avoid := map[string]time.Time{"cn": now.Add(time.Minute), "old": now.Add(-time.Second)}
	pruneAvoid(avoid, res, now)
	if _, ok := avoid["cn"]; !ok || len(avoid) != 1 {
		t.Fatalf("want only the unexpired cn entry, got %v", avoid)
	}
	// Every reachable relay avoided (the coordinator was restarting): start over.
	avoid["us"] = now.Add(time.Minute)
	avoid["sg"] = now.Add(time.Minute)
	pruneAvoid(avoid, res, now)
	if len(avoid) != 0 {
		t.Fatalf("want the list cleared, got %v", avoid)
	}
	// Nothing reachable: keep the list.
	avoid["cn"] = now.Add(time.Minute)
	pruneAvoid(avoid, []p2p.RelayRTT{down}, now)
	if len(avoid) != 1 {
		t.Fatalf("want the list kept, got %v", avoid)
	}
}
