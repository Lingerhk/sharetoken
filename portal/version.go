package portal

import (
	"net/http"

	"sharetoken/accounts"
	"sharetoken/coordinator"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/version"
	"sharetoken/mailer"
	"sharetoken/protocol"
	"sharetoken/relaypass"
)

// HTTP headers that carry versions.
const (
	ClientVersionHeader = "X-ST-Client-Version" // the desktop client and CLI, on every request
	ServerVersionHeader = "X-ST-Server-Version" // the portal, on every response
)

// ClientVersions tells a client where it stands.
type ClientVersions struct {
	Min         string `json:"min,omitempty"`
	Recommended string `json:"recommended,omitempty"`
	Latest      string `json:"latest,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

// VersionInfo is GET /api/v1/version: what this portal runs and speaks.
type VersionInfo struct {
	Server    version.Info   `json:"server"`
	API       int            `json:"api"`
	Protocols Protocols      `json:"protocols"`
	Schema    map[string]int `json:"schema"`
	Client    ClientVersions `json:"client"`
}

// Protocols lists the versions of the network's wire protocols.
type Protocols struct {
	Ctrl      string   `json:"ctrl"`
	Infer     string   `json:"infer"`
	InferJob  []uint32 `json:"infer_versions"`
	RelayPass string   `json:"relay_pass"`
}

func (s *Server) versionInfo() VersionInfo {
	p, err := accounts.LoadVersionPolicy(s.db)
	if err != nil {
		s.log.Warn("reading the version policy", "err", err)
	}
	return VersionInfo{
		Server: version.Current(), API: version.API,
		Protocols: Protocols{Ctrl: protocol.CtrlProtocol, Infer: protocol.InferProtocol, InferJob: protocol.SupportedInferVersions, RelayPass: relaypass.Protocol},
		Schema:    map[string]int{"core": mysqldb.Latest(coordinator.Migrations), "mail": mysqldb.Latest(mailer.Migrations), "portal": mysqldb.Latest(Migrations)},
		Client:    ClientVersions{Min: p.MinClient, Recommended: p.RecommendedClient, Latest: p.LatestClient, DownloadURL: p.DownloadURL},
	}
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.versionInfo())
}

// clientOnly guards what starts new business from a client: signing in and
// binding a device. A client the policy does not serve gets 426. What it already
// has keeps working: its account pages, passes for its relays, so that it can
// still settle and dispute. The web pages have their own endpoints.
func (s *Server) clientOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := accounts.LoadVersionPolicy(s.db)
		if err != nil {
			s.internal(w, err)
			return
		}
		if why := p.Refuses(r.Header.Get(ClientVersionHeader)); why != "" {
			writeErr(w, http.StatusUpgradeRequired, protocol.CodeUpgradeRequired, why)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// VersionsReport is the operator's view of what runs on the network.
type VersionsReport struct {
	VersionInfo
	Coordinator string                  `json:"coordinator,omitempty"`
	Mail        string                  `json:"mail,omitempty"`
	Relays      []accounts.Relay        `json:"relays"`
	Devices     []accounts.VersionCount `json:"devices"`
	Policy      accounts.VersionPolicy  `json:"policy"`
	Release     *Release                `json:"release,omitempty"` // what the downloads directory offers
}

// Versions builds the operator's report.
func (s *Server) Versions() (*VersionsReport, error) {
	rep := &VersionsReport{VersionInfo: s.versionInfo()}
	var err error
	if rep.Policy, err = accounts.LoadVersionPolicy(s.db); err != nil {
		return nil, err
	}
	if rep.Relays, err = s.store.Relays(false); err != nil {
		return nil, err
	}
	if rep.Devices, err = s.store.DeviceVersions(); err != nil {
		return nil, err
	}
	rep.Release = s.currentRelease()
	rep.Coordinator, _ = coordinator.RunningVersion(s.db)
	if ov, err := (&mailer.Store{DB: s.db}).ServerVersion(); err == nil {
		rep.Mail = ov
	}
	return rep, nil
}

func (s *Server) handleAdminVersions(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	rep, err := s.Versions()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleSetVersionPolicy(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var p accounts.VersionPolicy
	if !readJSON(w, r, &p) {
		return
	}
	if err := accounts.SaveVersionPolicy(s.db, p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	cu.note("min_client", p.MinClient)
	cu.note("recommended_client", p.RecommendedClient)
	cu.note("latest_client", p.LatestClient)
	cu.note("allow_dev", p.AllowDev)
	s.log.Info("version policy changed", "by", cu.user.Username, "min_client", p.MinClient, "latest", p.LatestClient, "allow_dev", p.AllowDev)
	writeJSON(w, http.StatusOK, p)
}
