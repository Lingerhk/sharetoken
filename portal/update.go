package portal

// What a client needs to know to offer its user an upgrade: the version policy,
// the release in the downloads directory, and the package of that release for
// the client's kind, build type, system and architecture.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"sharetoken/accounts"
	"sharetoken/internal/version"
)

// releaseRe names the file scripts/publish-packages.sh puts beside the packages.
var releaseRe = regexp.MustCompile(`^RELEASE-([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]+)?)\.json$`)

// Release is the published build: RELEASE-<version>.json. Before launch the
// version stays 1.0.0 while the code changes, so a client compares the commit
// and the build time as well.
type Release struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"` // RFC 3339, the same in every package of the release
	NotesURL  string `json:"notes_url,omitempty"`  // relative to the portal
}

// UpdateInfo is GET /api/v1/update.
type UpdateInfo struct {
	ClientVersions
	Release *Release `json:"release,omitempty"`
	// Package is the release's package for the client that asked, if published.
	Package *Package `json:"package,omitempty"`
}

// Query values of GET /api/v1/update.
var (
	updateKinds    = map[string]bool{"desktop": true, "cli": true}
	updateSystems  = map[string]bool{"macos": true, "linux": true, "windows": true}
	updateArchs    = map[string]bool{"amd64": true, "arm64": true}
	updateChannels = map[string]bool{"prod": true, "dev": true}
)

// currentRelease reads the newest release file in the downloads directory.
func (s *Server) currentRelease() *Release {
	if s.cfg.DownloadsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.cfg.DownloadsDir)
	if err != nil {
		return nil
	}
	var best *Release
	for _, e := range entries {
		m := releaseRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.cfg.DownloadsDir, e.Name()))
		var r Release
		if err != nil || json.Unmarshal(data, &r) != nil || r.Version != m[1] {
			s.log.Warn("unreadable release file", "file", e.Name(), "err", err)
			continue
		}
		if _, err := time.Parse(time.RFC3339, r.BuildTime); err != nil {
			r.BuildTime = ""
		}
		if best == nil || version.Compare(r.Version, best.Version) > 0 {
			best = &r
		}
	}
	if best != nil {
		best.NotesURL = "/changelog/#v" + version.Core(best.Version)
	}
	return best
}

// releasePackage finds the package of release v for one client, dev packages
// included: a dev client is offered the dev build of the release.
func (s *Server) releasePackage(v, kind, channel, osName, arch string) *Package {
	entries, err := os.ReadDir(s.cfg.DownloadsDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		m := packageRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() || m[3] != v || m[4] != osName || m[5] != arch {
			continue
		}
		if (m[1] != "") != (kind == "cli") || (m[2] != "") != (channel == "dev") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		return &Package{Kind: kind, OS: osName, Arch: arch, Version: v, File: e.Name(), URL: "/downloads/" + e.Name(), Size: info.Size(),
			SHA256: readChecksums(filepath.Join(s.cfg.DownloadsDir, "SHA256SUMS-"+v))[e.Name()]}
	}
	return nil
}

func (s *Server) updateInfo(kind, channel, osName, arch string) UpdateInfo {
	p, err := accounts.LoadVersionPolicy(s.db)
	if err != nil {
		s.log.Warn("reading the version policy", "err", err)
	}
	out := UpdateInfo{ClientVersions: ClientVersions{Min: p.MinClient, Recommended: p.RecommendedClient, Latest: p.LatestClient, DownloadURL: p.DownloadURL}}
	if out.DownloadURL == "" {
		out.DownloadURL = "/#download"
	}
	out.Release = s.currentRelease()
	if r := out.Release; r != nil {
		// A policy that names a newer version than the one published wins: the
		// client is sent to the download page for it.
		if out.Latest == "" || version.Compare(version.Core(r.Version), out.Latest) >= 0 {
			if !version.IsDev(r.Version) {
				out.Latest = r.Version
			}
			if updateKinds[kind] && updateChannels[channel] && updateSystems[osName] && updateArchs[arch] {
				out.Package = s.releasePackage(r.Version, kind, channel, osName, arch)
			}
		} else {
			out.Release = nil
		}
	}
	return out
}

// handleUpdate answers GET /api/v1/update?kind=desktop|cli&channel=prod|dev&os=…&arch=…
// A query the portal does not know gets no package, not an error: the client
// still learns about the release.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, s.updateInfo(q.Get("kind"), q.Get("channel"), q.Get("os"), q.Get("arch")))
}
