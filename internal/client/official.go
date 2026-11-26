package client

import (
	"log/slog"
	"net/http"

	"sharetoken/internal/official"
)

// gateway is the agent's official.Gateway, made on first use when the
// daemon gave it none.
func (a *Agent) gateway() *official.Gateway {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Official == nil {
		a.Official = newGateway(a.home, a.log)
	}
	return a.Official
}

// newGateway is the gateway for the vendor logins. Other builds than prod can
// be pointed at test vendors with $SHARETOKEN_OFFICIAL_ANTHROPIC and
// $SHARETOKEN_OFFICIAL_CHATGPT; a prod build ignores them, as it does
// $SHARETOKEN_PORTAL, so that the environment cannot send the user's Claude
// and ChatGPT logins and prompts elsewhere.
func newGateway(home string, log *slog.Logger) *official.Gateway {
	g := official.New(home, log)
	if Env != EnvProd {
		g.SetUpstreams(official.EnvUpstreams())
	}
	return g
}

// helloOK answers the probe Claude Code sends its base URL first
// (HEAD /api/hello).
func helloOK(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// toV1 serves the local /v1 endpoint: the agent's while it runs, else the
// vendor logins alone, with the network's requests told the client is not
// connected.
func (d *Daemon) toV1(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	api := d.agentAPI
	d.mu.Unlock()
	if api != nil {
		api.ServeHTTP(w, r)
		return
	}
	d.official.Wrap(http.HandlerFunc(d.toAgent), nil).ServeHTTP(w, r)
}
