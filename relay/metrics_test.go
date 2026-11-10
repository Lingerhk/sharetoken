package relay

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"sharetoken/internal/version"
	"sharetoken/p2p"
	"sharetoken/relaypass"
)

// scrape returns metric name + label value -> value.
func scrape(t *testing.T, r *Relay) map[string]float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(r.Collector())
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range families {
		for _, m := range f.Metric {
			out[metricKey(f.GetName(), m)] = m.GetGauge().GetValue() + m.GetCounter().GetValue()
		}
	}
	return out
}

func metricKey(name string, m *dto.Metric) string {
	var labels []string
	for _, l := range m.Label {
		labels = append(labels, l.GetValue())
	}
	if len(labels) == 0 {
		return name
	}
	return name + "{" + strings.Join(labels, ",") + "}"
}

// The metric the monitor cares about most follows the coordinator: a network
// with no coordinator reservation is unreachable through this relay.
func TestMetricsFollowCoordinatorReservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	portalPub, _, _ := ed25519.GenerateKey(nil)
	coord, coordKeys := newHost(t)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{{
		Name: "prod", PortalKeys: []ed25519.PublicKey{portalPub}, AllowPeers: []peer.ID{coordKeys.PeerID}, MaxReservations: 32}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for name, want := range map[string]float64{
		"sharetoken_relay_info{" + r.Host.ID().String() + "}":  1,
		"sharetoken_relay_max_reservations":                    4096,
		"sharetoken_relay_max_connections":                     8192,
		"sharetoken_relay_network_coordinators_expected{prod}": 1,
		"sharetoken_relay_network_coordinators_reserved{prod}": 0,
		"sharetoken_relay_network_max_reservations{prod}":      32,
	} {
		if got, ok := scrape(t, r)[name]; !ok || got != want {
			t.Errorf("%s = %v (present %v), want %v", name, got, ok, want)
		}
	}

	// The service starts asynchronously, once the host considers itself public.
	ri := peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}
	waitMetric := func(name string, want float64) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for scrape(t, r)[name] != want {
			if time.Now().After(deadline) {
				t.Fatalf("%s = %v, want %v", name, scrape(t, r)[name], want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitMetric("sharetoken_relay_service_enabled", 1)

	if _, err := client.Reserve(ctx, coord, ri); err != nil {
		t.Fatal(err)
	}
	waitMetric("sharetoken_relay_network_coordinators_reserved{prod}", 1)
	waitMetric("sharetoken_relay_network_reservations{prod}", 1)
	waitMetric("sharetoken_relay_reservations", 1)
	waitMetric("sharetoken_relay_peers", 1)

	// The coordinator going away must show up without waiting for the TTL.
	_ = coord.Close()
	waitMetric("sharetoken_relay_network_coordinators_reserved{prod}", 0)
}

// An open relay has no networks to report, and scrapes cleanly.
func TestMetricsOpenRelay(t *testing.T) {
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := scrape(t, r)
	if _, ok := m["sharetoken_relay_reservations"]; !ok {
		t.Fatalf("%v", m)
	}
	for k := range m {
		if strings.Contains(k, "network_") {
			t.Fatalf("open relay reports %s", k)
		}
	}
}

// Devices are counted by the build their pass names; one whose identify agent
// says otherwise is a mismatch. Connected peers are counted by agent.
func TestMetricsCountClientBuilds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	portalPub, portalKey, _ := ed25519.GenerateKey(nil)
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []Network{{Name: "prod", PortalKeys: []ed25519.PublicKey{portalPub}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ri := peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}
	agent, _ := version.ParseAgent(version.AgentVersion("client"))
	honest := relaypass.Client{Version: agent.Version, OS: agent.OS, Arch: agent.Arch}
	device := func(program string, c relaypass.Client) {
		k, _ := p2p.NewKeys()
		h, err := p2p.NewHost(k, p2p.HostConfig{Program: program, Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := relaypass.Present(ctx, h, ri, relaypass.Issue(portalKey, h.ID().String(), "user:"+k.ID()[len(k.ID())-4:], c, time.Now(), time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Reserve(ctx, h, ri); err != nil {
			t.Fatal(err)
		}
	}
	device("client", honest)
	device("client", honest)
	device("client", relaypass.Client{Version: "0.9.0", OS: "windows", Arch: "amd64"}) // the pass says Windows, the agent says otherwise
	device("", honest)                                                                 // no sharetoken agent at all

	// Identify finishes in the background; wait until every peer has an agent.
	deadline := time.Now().Add(5 * time.Second)
	for {
		m := scrape(t, r)
		if m["sharetoken_relay_agents{"+agent.Arch+","+agent.OS+",client,"+agent.Version+"}"] == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	m := scrape(t, r)
	// Labels come sorted by name: arch, network, os, version.
	for key, want := range map[string]float64{
		"sharetoken_relay_network_clients{" + agent.Arch + ",prod," + agent.OS + "," + agent.Version + "}": 3,
		"sharetoken_relay_network_clients{amd64,prod,windows,0.9.0}":                                       1,
		"sharetoken_relay_network_client_mismatches{prod}":                                                 2,
		"sharetoken_relay_agents{" + agent.Arch + "," + agent.OS + ",client," + agent.Version + "}":        3,
		"sharetoken_relay_agents{,,other,}":                                                                1,
	} {
		if m[key] != want {
			t.Errorf("%s = %v, want %v", key, m[key], want)
		}
	}
	if t.Failed() {
		t.Logf("metrics: %v", m)
	}
}
