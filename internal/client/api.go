package client

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sharetoken/internal/version"
	"slices"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"sharetoken/internal/cli"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/provider"
)

// Status is the agent's state, for the CLI and a GUI.
type Status struct {
	Version     string            `json:"version"`
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
	Tailnet     *TailnetStatus    `json:"tailnet,omitempty"` // the direct path over tunnels, when it is on
	Warnings    []string          `json:"warnings,omitempty"`
	Update      *Update           `json:"update,omitempty"` // a newer release, when there is one
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
	// Private marks a relay of the user's own (config private_relays): only
	// sharing uses it, never the coordinator.
	Private bool `json:"private,omitempty"`
}

// ShareStatus is the sharing side.
type ShareStatus struct {
	ShareConfig
	Providers []provider.View `json:"providers"` // hosted APIs, without their keys
	// Serving is what the running node sells: the local models and those of
	// the providers that are on.
	Serving      []node.ModelConfig `json:"serving"`
	Running      bool               `json:"running"`
	Stats        *node.Stats        `json:"stats,omitempty"`
	Reservations []p2p.RelayStatus  `json:"reservations,omitempty"`
}

// Status reports the agent's state.
func (a *Agent) Status() Status {
	views, _ := provider.Views(a.home)
	var tn *TailnetStatus
	if a.tailnet != nil && a.tailnet.Enabled() {
		ts := a.TailnetStatus()
		tn = &ts
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := Status{
		Peer: a.keys.ID(), User: a.cfg.User, Portal: a.cfg.Portal, Started: a.started,
		Version: version.Version, API: "http://" + a.addr + "/v1", MeasuredAt: a.measured, Buy: a.cfg.Buy,
		Coordinator: CoordinatorStatus{Peer: a.coord.String(), Via: a.current, Link: a.link.Quality()},
		Share:       ShareStatus{ShareConfig: a.cfg.Share, Providers: views},
		Tailnet:     tn,
	}
	for _, c := range a.h.Network().ConnsToPeer(a.coord) {
		st.Coordinator.Connected = true
		st.Coordinator.Relayed = p2p.IsRelayed(c)
	}
	reserved := map[string]bool{}
	if a.share != nil {
		st.Share.Running = true
		st.Share.Serving = a.share.models
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
	resErr := map[string]string{}
	for _, r := range st.Share.Reservations {
		resErr[r.Addr] = r.Err
	}
	for _, r := range a.private {
		v := RelayView{Addr: r.Addr, Name: "private", Private: true, Reserved: reserved[r.Addr], Error: cmp.Or(r.Err, resErr[r.Addr])}
		if r.OK() {
			v.RTTMillis = float64(r.RTT.Microseconds()) / 1000
		}
		st.Relays = append(st.Relays, v)
	}
	for _, w := range a.warnings {
		st.Warnings = append(st.Warnings, w)
	}
	slices.Sort(st.Warnings)
	if a.Updates != nil {
		if u := a.Updates.Current(); u.Available {
			st.Update = &u
		}
	}
	return st
}

// Handler serves the local API (/api/...) and the OpenAI-compatible
// endpoint (/v1/...).
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", a.proxy.Handler())
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.Status()) })
	mux.HandleFunc("GET /api/v1/models", a.handleModels)
	mux.HandleFunc("GET /api/v1/account", a.portalPassthrough("/api/v1/me"))
	mux.HandleFunc("GET /api/v1/jobs", a.portalPassthrough("/api/v1/jobs"))
	mux.HandleFunc("GET /api/v1/ledger", a.portalPassthrough("/api/v1/ledger"))
	mux.HandleFunc("POST /api/v1/relays/measure", func(w http.ResponseWriter, r *http.Request) {
		a.Remeasure(r.Context())
		writeJSON(w, http.StatusOK, a.Status().Relays)
	})
	mux.HandleFunc("GET /api/v1/share", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.Status().Share) })
	mux.HandleFunc("PUT /api/v1/share", a.handleSetShare)
	mux.HandleFunc("GET /api/v1/tailnet", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.TailnetStatus()) })
	mux.HandleFunc("PUT /api/v1/tailnet", a.handleSetTailnet)
	mux.HandleFunc("POST /api/v1/tailnet/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := a.ReloadTailnet(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.TailnetStatus())
	})
	mux.HandleFunc("GET /api/v1/buy", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, a.BuySettings()) })
	mux.HandleFunc("PUT /api/v1/buy", a.handleSetBuy)
	mux.HandleFunc("POST /api/v1/share/start", a.handleToggleShare(true))
	mux.HandleFunc("POST /api/v1/share/stop", a.handleToggleShare(false))
	mux.HandleFunc("POST /api/v1/share/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := a.ReloadShare(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.Status().Share)
	})
	mux.HandleFunc("GET /api/v1/share/detect", a.handleDetect)
	a.privateRelayRoutes(mux)
	a.providerRoutes(mux)
	a.pairingRoutes(mux)
	a.sharingRoutes(mux)
	apiKeyRoutes(mux, a.home)
	if a.warm != nil {
		a.warm.Routes(mux)
	}
	mux.HandleFunc("POST /api/v1/login/check", func(w http.ResponseWriter, r *http.Request) {
		select {
		case a.loginKick <- struct{}{}:
		default:
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	})
	h := guardOrigin(mux)
	// DNS rebinding. An address the user opened to the network on purpose is
	// reached under other names, and is exposed to more than web pages anyway.
	if host, _, err := net.SplitHostPort(a.cfg.HTTP); err == nil && cli.IsLoopback(host) {
		h = guardHost(h)
	}
	return h
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
	ms, err := a.buyer.ModelsNow(network.WithAllowLimitedConn(ctx, "sharetoken-ctrl"))
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

// BuySettingsView is what the settings page changes of the buying side.
type BuySettingsView struct {
	BudgetMicros  uint64 `json:"budget_micros"`
	RequireNative bool   `json:"require_native"`
}

// buyView is the settings page's view of the buying settings.
func buyView(b BuyConfig) BuySettingsView {
	return BuySettingsView{BudgetMicros: b.BudgetMicros, RequireNative: b.RequireNative}
}

// apply writes the view into b.
func (v BuySettingsView) apply(b *BuyConfig) {
	b.BudgetMicros, b.RequireNative = v.BudgetMicros, v.RequireNative
}

// BuySettings reports the buying settings the settings page changes.
func (a *Agent) BuySettings() BuySettingsView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return buyView(a.cfg.Buy)
}

// SetBuySettings applies them to the running endpoint at once and saves them.
func (a *Agent) SetBuySettings(v BuySettingsView) error {
	if err := v.validate(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := saveBuySettings(a.home, v); err != nil {
		return err
	}
	v.apply(&a.cfg.Buy)
	if a.proxy != nil {
		a.proxy.SetBuy(v.BudgetMicros, v.RequireNative)
	}
	return nil
}

func (v BuySettingsView) validate() error {
	if v.BudgetMicros < MinBudgetMicros || v.BudgetMicros > MaxBudgetMicros {
		return fmt.Errorf("the budget must be between %d and %d micro-credits", MinBudgetMicros, MaxBudgetMicros)
	}
	return nil
}

// saveBuySettings writes them to config.json, keeping its other fields.
func saveBuySettings(home string, v BuySettingsView) error {
	if err := v.validate(); err != nil {
		return err
	}
	disk, err := LoadConfig(home)
	if err != nil {
		return err
	}
	v.apply(&disk.Buy)
	return SaveConfig(home, disk)
}

func (a *Agent) handleSetBuy(w http.ResponseWriter, r *http.Request) {
	var v BuySettingsView
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}
	if err := a.SetBuySettings(v); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.BuySettings())
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
