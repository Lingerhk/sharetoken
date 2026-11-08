package client

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"sharetoken/node"
	"sharetoken/p2p"
)

// Status is the agent's state, for the CLI and a GUI.
type Status struct {
	Peer        string            `json:"peer"`
	User        string            `json:"user"`
	Portal      string            `json:"portal"`
	Started     time.Time         `json:"started"`
	API         string            `json:"api"` // OpenAI base URL
	Coordinator CoordinatorStatus `json:"coordinator"`
	Relays      []RelayView       `json:"relays"`
	MeasuredAt  time.Time         `json:"measured_at,omitzero"`
	Buy         BuyConfig         `json:"buy"`
	Share       ShareStatus       `json:"share"`
	Warnings    []string          `json:"warnings,omitempty"`
}

// CoordinatorStatus is the path to the coordinator.
type CoordinatorStatus struct {
	Peer      string `json:"peer"`
	Connected bool   `json:"connected"`
	Relayed   bool   `json:"relayed"` // always true when connected: the coordinator accepts nothing direct
	Via       string `json:"via"`     // the relay in use
	// Link is the live quality of the path to Via, from a ping at every
	// health check (last 30 pings); nil until the first ping.
	Link *p2p.LinkQuality `json:"link,omitempty"`
}

// RelayView is one relay with its measurement.
type RelayView struct {
	Addr          string  `json:"addr"`
	Name          string  `json:"name,omitempty"`
	Region        string  `json:"region,omitempty"`
	RTTMillis     float64 `json:"rtt_ms,omitempty"`
	Error         string  `json:"error,omitempty"`
	CoordinatorOK bool    `json:"coordinator_ok"`
	Current       bool    `json:"current"`  // carries this client's coordinator traffic
	Reserved      bool    `json:"reserved"` // holds a reservation for sharing
}

// ShareStatus is the sharing side.
type ShareStatus struct {
	ShareConfig
	Running      bool              `json:"running"`
	Stats        *node.Stats       `json:"stats,omitempty"`
	Reservations []p2p.RelayStatus `json:"reservations,omitempty"`
}

// Status reports the agent's state.
func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := Status{
		Peer: a.keys.ID(), User: a.cfg.User, Portal: a.cfg.Portal, Started: a.started,
		API: "http://" + a.addr + "/v1", MeasuredAt: a.measured, Buy: a.cfg.Buy,
		Coordinator: CoordinatorStatus{Peer: a.coord.String(), Via: a.current, Link: a.link.Quality()},
		Share:       ShareStatus{ShareConfig: a.cfg.Share},
	}
	for _, c := range a.h.Network().ConnsToPeer(a.coord) {
		st.Coordinator.Connected = true
		st.Coordinator.Relayed = p2p.IsRelayed(c)
	}
	reserved := map[string]bool{}
	if a.share != nil {
		st.Share.Running = true
		s := a.share.node.Stats()
		st.Share.Stats = &s
		st.Share.Reservations = a.share.reserver.Status()
		for _, r := range st.Share.Reservations {
			reserved[r.Addr] = r.Reserved
		}
	}
	names := map[string][2]string{}
	for _, r := range a.netinfo.Relays {
		names[r.Addr] = [2]string{r.Name, r.Region}
	}
	for _, r := range a.relays {
		v := RelayView{Addr: r.Addr, Name: names[r.Addr][0], Region: names[r.Addr][1], Error: r.Err,
			CoordinatorOK: a.coordinatorOK(r.Addr), Current: r.Addr == a.current, Reserved: reserved[r.Addr]}
		if r.OK() {
			v.RTTMillis = float64(r.RTT.Microseconds()) / 1000
		}
		st.Relays = append(st.Relays, v)
	}
	for _, w := range a.warnings {
		st.Warnings = append(st.Warnings, w)
	}
	slices.Sort(st.Warnings)
	return st
}

// Handler serves the local API (/api/...) and the OpenAI-compatible
// endpoint (/v1/...).
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", a.proxy.Handler())
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.Status()) })
	mux.HandleFunc("GET /api/models", a.handleModels)
	mux.HandleFunc("GET /api/account", a.portalPassthrough("/api/v1/me"))
	mux.HandleFunc("GET /api/jobs", a.portalPassthrough("/api/v1/jobs"))
	mux.HandleFunc("GET /api/ledger", a.portalPassthrough("/api/v1/ledger"))
	mux.HandleFunc("POST /api/relays/measure", func(w http.ResponseWriter, r *http.Request) {
		a.Remeasure(r.Context())
		writeJSON(w, http.StatusOK, a.Status().Relays)
	})
	mux.HandleFunc("GET /api/share", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.Status().Share) })
	mux.HandleFunc("PUT /api/share", a.handleSetShare)
	mux.HandleFunc("POST /api/share/start", a.handleToggleShare(true))
	mux.HandleFunc("POST /api/share/stop", a.handleToggleShare(false))
	mux.HandleFunc("POST /api/share/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := a.ReloadShare(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.Status().Share)
	})
	mux.HandleFunc("GET /api/share/detect", a.handleDetect)
	return guardOrigin(mux)
}

// guardOrigin rejects requests that a web page in the user's browser sent:
// any page could otherwise spend the user's credits through this loopback
// server. Requests without an Origin (CLI tools, SDKs) and from local or app
// origins (a desktop GUI) pass.
func guardOrigin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !localOrigin(o) {
			writeError(w, http.StatusForbidden, "cross-origin requests from web pages are not allowed")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || origin == "null" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return u.Scheme != "" // app schemes (tauri://, wails://, app://)
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *Agent) handleModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ms, err := a.buyer.Models(network.WithAllowLimitedConn(ctx, "sharetoken-ctrl"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": ms})
}

func (a *Agent) portalPassthrough(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { _ = forwardPortalGET(w, r, a.portal, path) }
}

// forwardPortalGET answers r with the portal's reply to a GET of path, and
// returns the error it reported, if any.
func forwardPortalGET(w http.ResponseWriter, r *http.Request, pc *PortalClient, path string) error {
	target := path
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	var raw json.RawMessage
	if err := pc.Do(r.Context(), http.MethodGet, target, nil, &raw); err != nil {
		status, code := http.StatusBadGateway, "portal_unreachable"
		if pe, ok := err.(*PortalError); ok {
			status, code = pe.Status, pe.Code
		}
		writeErrorCode(w, status, code, err.Error())
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
	return nil
}

func (a *Agent) handleSetShare(w http.ResponseWriter, r *http.Request) {
	var sc ShareConfig
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&sc); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}
	if err := a.SetShare(sc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.Status().Share)
}

func (a *Agent) handleToggleShare(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		sc := a.cfg.Share
		a.mu.Unlock()
		sc.Enabled = on
		if err := a.SetShare(sc); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.Status().Share)
	}
}

func (a *Agent) handleDetect(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	upstream := a.cfg.Share.Upstream
	a.mu.Unlock()
	if u := r.URL.Query().Get("upstream"); u != "" {
		upstream = u
	}
	ms, err := DetectModels(r.Context(), upstream)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstream": upstream, "models": ms})
}

// DetectModels lists the models a local OpenAI-compatible server offers.
func DetectModels(ctx context.Context, upstream string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(upstream, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
