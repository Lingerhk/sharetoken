package portal

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"sharetoken/internal/p2p"
)

const sampleMetrics = `# HELP process_cpu_seconds_total Total user and system CPU time spent in seconds.
process_cpu_seconds_total 249.13
process_max_fds 65536
process_open_fds 14
process_resident_memory_bytes 2.1049344e+07
process_start_time_seconds 1.79417947802e+09
sharetoken_relay_build_info{version="1.0.0"} 1
sharetoken_relay_circuits 2
sharetoken_relay_circuits_denied_total 159
sharetoken_relay_connections 6
sharetoken_relay_info{peer_id="12D3KooWLCSbc4QX8etk5kRoQeBWQdsX1tuVcAdA81KEL3QthP9H"} 1
sharetoken_relay_max_connections 8192
sharetoken_relay_max_reservations 4096
sharetoken_relay_network_coordinators_expected{network="dev"} 1
sharetoken_relay_network_coordinators_expected{network="prod"} 1
sharetoken_relay_network_coordinators_reserved{network="dev"} 0
sharetoken_relay_network_coordinators_reserved{network="prod"} 1
sharetoken_relay_network_max_reservations{network="dev"} 32
sharetoken_relay_network_reservations{network="dev"} 1
sharetoken_relay_network_client_mismatches{network="prod"} 1
sharetoken_relay_network_clients{arch="arm64",network="prod",os="darwin",version="1.0.0"} 2
sharetoken_relay_network_clients{arch="amd64",network="prod",os="windows",version="1.0.0"} 5
sharetoken_relay_agents{arch="amd64",os="windows",program="client",version="1.0.0"} 6
sharetoken_relay_agents{arch="",os="",program="other",version=""} 1
sharetoken_relay_peers 6
sharetoken_relay_relayed_bytes_total 1.26499907e+08
sharetoken_relay_reservations 2
sharetoken_relay_reservations_denied_total 3259
sharetoken_relay_service_enabled 1
`

func TestParseRelayMetrics(t *testing.T) {
	m, err := parseRelayMetrics([]byte(sampleMetrics))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.0.0" || !m.Service || m.Peers != 6 || m.MaxConns != 8192 || m.Relayed != 126499907 || m.RSSBytes != 21049344 || m.ResvDenied != 3259 {
		t.Fatalf("parsed: %+v", m)
	}
	if m.StartedAt/1e9 != 1794179478 {
		t.Fatalf("started at %d", m.StartedAt)
	}
	if len(m.Networks) != 2 || m.Networks[0].Network != "dev" || m.Networks[0].Reserved != 0 || m.Networks[0].MaxReservations != 32 || m.Networks[1].Reserved != 1 {
		t.Fatalf("networks: %+v", m.Networks)
	}
	if p := m.Networks[1]; p.Mismatches != 1 || len(p.Clients) != 2 || p.Clients[0] != (RelayClientCount{Version: "1.0.0", OS: "windows", Arch: "amd64", Count: 5}) {
		t.Fatalf("prod clients: %+v", p)
	}
	if len(m.Networks[0].Clients) != 0 || m.Networks[0].Clients == nil {
		t.Fatalf("dev clients must be an empty list: %#v", m.Networks[0].Clients)
	}
	if len(m.Agents) != 2 || m.Agents[0].Program != "client" || m.Agents[0].Count != 6 || m.Agents[1].Program != "other" {
		t.Fatalf("agents: %+v", m.Agents)
	}
	if _, err := parseRelayMetrics([]byte("go_goroutines 7\n")); err == nil {
		t.Fatal("output without relay metrics was accepted")
	}
}

// fakeRelaySSH is an SSH server that answers any exec with body, for clientKey only.
func fakeRelaySSH(t *testing.T, clientKey ssh.PublicKey, body string) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	_, hpriv, _ := ed25519.GenerateKey(rand.Reader)
	hs, err := ssh.NewSignerFromKey(hpriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) != string(clientKey.Marshal()) {
			return nil, os.ErrPermission
		}
		return nil, nil
	}}
	// Real servers offer several kinds of host key and prefer one that is not pinned.
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	es, err := ssh.NewSignerFromKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AddHostKey(es)
	cfg.AddHostKey(hs)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, err := nc.Accept()
					if err != nil {
						return
					}
					for r := range creqs {
						if r.Type != "exec" {
							r.Reply(false, nil)
							continue
						}
						r.Reply(true, nil)
						ch.Write([]byte(body))
						ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						ch.Close()
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), hs.PublicKey()
}

func TestDashboardShowsRelayMetricsReadOverSSH(t *testing.T) {
	dir := t.TempDir()
	_, cpriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(cpriv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "id")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	cpub, _ := ssh.NewPublicKey(cpriv.Public())
	good, goodHost := fakeRelaySSH(t, cpub, sampleMetrics)
	impostor, _ := fakeRelaySSH(t, cpub, sampleMetrics) // a host key nobody pinned
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{good, impostor}, goodHost)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := newAdminFixture(t, Config{RelayMetrics: RelayMetricsConfig{KeyFile: keyFile, KnownHostsFile: known, Timeout: 5 * time.Second,
		Targets: []RelayMetricsTarget{{Relay: "r1", SSH: "probe@" + good}, {Relay: "r2", SSH: "probe@" + impostor}}}})
	for _, name := range []string{"r1", "r2"} {
		k, _ := p2p.NewKeys()
		f.ok("POST", "/api/v1/admin/relays", map[string]string{"addr": "/ip4/1.2.3.4/tcp/1/p2p/" + k.ID(), "name": name}, nil)
	}
	for _, tg := range f.s.relayMetrics.cfg.Targets {
		f.s.relayMetrics.scrapeOne(context.Background(), tg)
	}

	var d Dashboard
	f.ok("GET", "/api/v1/admin/dashboard", nil, &d)
	if !d.MetricsOn || len(d.Relays) != 2 || d.Users.Total < 1 || len(d.Daily) != dashDays {
		t.Fatalf("dashboard: %+v", d)
	}
	byName := map[string]DashRelay{}
	for _, r := range d.Relays {
		byName[r.Name] = r
	}
	r1, r2 := byName["r1"].Metrics, byName["r2"].Metrics
	if r1 == nil || r1.Error != "" || r1.Peers != 6 || r1.Version != "1.0.0" || r1.OKAt == 0 {
		t.Fatalf("r1 metrics: %+v", r1)
	}
	if r2 == nil || r2.OKAt != 0 || !strings.Contains(r2.Error, "key") {
		t.Fatalf("an unpinned host key was not refused: %+v", r2)
	}

	// Non-admins never see it.
	st, _ := f.call("GET", "/api/v1/admin/dashboard", "", nil)
	if st != 401 {
		t.Fatalf("anonymous dashboard: %d", st)
	}
}

func TestDashboardWithoutRelayMetrics(t *testing.T) {
	f := newAdminFixture(t, Config{})
	var d Dashboard
	f.ok("GET", "/api/v1/admin/dashboard", nil, &d)
	if d.MetricsOn || d.Users.Total != 1 || d.Users.Admins != 1 || d.Users.New24h != 1 || d.Daily[dashDays-1].Users != 1 {
		t.Fatalf("dashboard: %+v", d)
	}
}
