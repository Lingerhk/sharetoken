package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/client"
	"sharetoken/portal"
	"sharetoken/protocol"
)

// daemonUser drives a desktop client the way the app does: only through the
// daemon's HTTP API.
type daemonUser struct {
	t    *testing.T
	d    *client.Daemon
	base string
	home string
}

func (n *network) startDaemon(home string) *daemonUser {
	n.t.Helper()
	d := client.NewDaemon(home)
	d.HTTP = "127.0.0.1:0"
	nat := false
	d.NAT, d.Listen = &nat, []string{"/ip4/127.0.0.1/tcp/0"}
	d.RetryMin, d.RetryMax = 50*time.Millisecond, 500*time.Millisecond
	d.OnAgent = func(a *client.Agent) {
		a.HealthEvery, a.MeasureEvery = 200*time.Millisecond, time.Hour
		a.HostOptions = []libp2p.Option{libp2p.ConnectionGater(onlyFamily{ma.P_IP4})}
	}
	if err := d.Start(); err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(d.Close)
	return &daemonUser{t: n.t, d: d, base: "http://" + d.Addr(), home: home}
}

func (u *daemonUser) call(method, path string, body any, out any) int {
	u.t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, u.base+path, rd)
	if err != nil {
		u.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			u.t.Fatalf("%s: %s", path, data)
		}
	}
	return resp.StatusCode
}

func (u *daemonUser) waitRunning() client.Session {
	u.t.Helper()
	var s client.Session
	waitFor(u.t, 30*time.Second, "the agent joining the network", func() bool {
		s = client.Session{}
		u.call("GET", "/api/v1/session", nil, &s)
		return s.State == client.StateRunning
	})
	return s
}

// The desktop client's whole life over its API, on the real topology: sign up
// by email, join through a relay, see the network, buy from another user,
// watch the balance move, restart, log out, log in.
func TestDaemonFullFlow(t *testing.T) {
	n := newNetwork(t, 2)
	upstream := mockUpstream(t, "mock-7b", false)
	seller := n.user("seller", shareMock(upstream.URL, 100_000, 200_000))

	home := t.TempDir()
	u := n.startDaemon(home)

	var s client.Session
	if st := u.call("GET", "/api/v1/session", nil, &s); st != 200 || s.State != client.StateLoggedOut {
		t.Fatalf("logged out: %d %+v", st, s)
	}
	var probe client.PortalProbe
	if st := u.call("POST", "/api/v1/portal/probe", map[string]string{"portal": n.url}, &probe); st != 200 || !probe.RegistrationOpen || probe.SignupCredit != 5_000_000 {
		t.Fatalf("probe: %d %+v", st, probe)
	}
	if st := u.call("POST", "/api/v1/register", map[string]string{"portal": n.url, "email": "Buyer@Example.com", "password": "correct horse", "device": "buyer-mac"}, &s); st != 201 {
		t.Fatalf("register: %d", st)
	}
	if s.User != "buyer@example.com" {
		t.Fatalf("session: %+v", s)
	}
	s = u.waitRunning()

	var st client.Status
	if code := u.call("GET", "/api/v1/status", nil, &st); code != 200 {
		t.Fatalf("status: %d", code)
	}
	if st.User != "buyer@example.com" || st.API != u.base+"/v1" || len(st.Relays) != 2 || st.Peer != s.Peer {
		t.Fatalf("status: %+v", st)
	}
	waitFor(t, 20*time.Second, "a path to the coordinator", func() bool {
		u.call("GET", "/api/v1/status", nil, &st)
		return st.Coordinator.Connected && st.Coordinator.Via != ""
	})

	var models struct {
		Models []protocol.ModelSummary `json:"models"`
	}
	waitFor(t, 20*time.Second, "the seller's model on the list", func() bool {
		return u.call("GET", "/api/v1/models", nil, &models) == 200 && len(models.Models) == 1
	})
	if models.Models[0].Model != model || models.Models[0].Nodes != 1 || models.Models[0].MinPriceIn != 100_000 {
		t.Fatalf("models: %+v", models)
	}

	var me portal.Me
	if code := u.call("GET", "/api/v1/account", nil, &me); code != 200 || me.Balance.AvailableMicros != 5_000_000 || len(me.Devices) != 1 || me.Devices[0].Name != "buyer-mac" {
		t.Fatalf("account: %d %+v", code, me)
	}

	// Buy through the daemon's OpenAI endpoint.
	resp, err := http.Post(u.base+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Sharetoken-Verified") != "true" {
		t.Fatalf("chat: %d %s", resp.StatusCode, data)
	}
	waitFor(t, 20*time.Second, "the job on the bill and in the job list", func() bool {
		var jobs struct {
			Jobs []struct {
				Role   string `json:"role"`
				Model  string `json:"model"`
				Status string `json:"status"`
			} `json:"jobs"`
		}
		u.call("GET", "/api/v1/jobs?limit=5", nil, &jobs)
		u.call("GET", "/api/v1/account", nil, &me)
		return len(jobs.Jobs) == 1 && jobs.Jobs[0].Role == "buy" && jobs.Jobs[0].Model == model && jobs.Jobs[0].Status == "final" &&
			me.Balance.AvailableMicros < 5_000_000 && me.Usage.Bought.Jobs == 1
	})
	if got := n.me(seller).Balance.AvailableMicros; got <= 5_000_000 {
		t.Fatalf("the seller earned nothing: %d", got)
	}

	// Measuring relays and the sharing settings go to the agent.
	var relays []client.RelayView
	if code := u.call("POST", "/api/v1/relays/measure", nil, &relays); code != 200 || len(relays) != 2 {
		t.Fatalf("measure: %d %+v", code, relays)
	}
	if code := u.call("GET", "/api/v1/share", nil, nil); code != 200 {
		t.Fatalf("share: %d", code)
	}

	// The CLI finds the daemon through the run file.
	if addr := client.APIAddr(home, client.Config{}); addr != u.d.Addr() {
		t.Fatalf("run file: %s, want %s", addr, u.d.Addr())
	}

	// A restart resumes the session, same device.
	peer := s.Peer
	u.d.Close()
	u = n.startDaemon(home)
	if s = u.waitRunning(); s.User != "buyer@example.com" || s.Peer != peer {
		t.Fatalf("after restart: %+v", s)
	}

	// Logging out stops buying.
	if code := u.call("POST", "/api/v1/logout", struct{}{}, &s); code != 200 || s.LoggedIn {
		t.Fatalf("logout: %d %+v", code, s)
	}
	resp, err = http.Post(u.base+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("chat after logout: %d", resp.StatusCode)
	}
	if code := u.call("GET", "/api/v1/status", nil, nil); code != 401 {
		t.Fatalf("status after logout: %d", code)
	}

	// Logging in again brings it all back, on the same device.
	if code := u.call("POST", "/api/v1/login", map[string]string{"portal": n.url, "account": "buyer@example.com", "password": "correct horse"}, &s); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if s = u.waitRunning(); s.Peer != peer {
		t.Fatalf("after login: %+v", s)
	}
	waitFor(t, 20*time.Second, "buying again", func() bool {
		resp, err := http.Post(u.base+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
}

// A login made while the network is down starts working once it is up.
func TestDaemonJoinsWhenNetworkReturns(t *testing.T) {
	n := newNetwork(t, 1)
	front := newSwitchablePortal(t, n)
	u := n.startDaemon(t.TempDir())
	front.networkDown.Store(true)
	var s client.Session
	if code := u.call("POST", "/api/v1/register", map[string]string{"portal": front.url, "email": "late@example.com", "password": "correct horse"}, &s); code != 201 {
		t.Fatalf("register: %d", code)
	}
	waitFor(t, 10*time.Second, "the first failed attempt", func() bool {
		u.call("GET", "/api/v1/session", nil, &s)
		return s.State == client.StateRetrying && s.Error != ""
	})
	if code := u.call("GET", "/api/v1/account", nil, nil); code != 200 {
		t.Fatalf("account with the network down: %d", code)
	}
	if code := u.call("GET", "/api/v1/status", nil, nil); code != 503 {
		t.Fatalf("status with the network down: %d", code)
	}
	front.networkDown.Store(false)
	s = u.waitRunning()
	if s.Error != "" {
		t.Fatalf("session: %+v", s)
	}
}

// switchablePortal fronts the portal and can pretend the coordinator is away.
type switchablePortal struct {
	url         string
	networkDown atomic.Bool
}

func newSwitchablePortal(t *testing.T, n *network) *switchablePortal {
	t.Helper()
	p := &switchablePortal{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/network" && p.networkDown.Load() {
			http.Error(w, `{"error":{"code":"not_ready","message":"the coordinator has not started yet"}}`, http.StatusServiceUnavailable)
			return
		}
		n.portal.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	p.url = srv.URL
	return p
}
