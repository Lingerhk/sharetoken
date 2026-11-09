package portal

// Client packages for download. They live in a directory on the server, not in
// the binary: the portal lists what is there, newest version per package kind,
// system and architecture, so publishing a release is copying files.

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sharetoken/internal/version"
)

// A slow download must not be cut off by the server's write timeout.
const downloadWriteTimeout = time.Hour

var (
	// The names scripts/build-cli.sh and scripts/build-gui.sh give their packages.
	packageRe  = regexp.MustCompile(`^sharetoken(-cli)?(-dev)?-([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]+)?)-(macos|linux|windows)-(amd64|arm64)\.(?:tar\.gz|zip|dmg|exe|deb)$`)
	checksumRe = regexp.MustCompile(`^SHA256SUMS-[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]+)?$`)
)

// Package is one downloadable archive.
type Package struct {
	Kind    string `json:"kind"` // cli or desktop
	OS      string `json:"os"`   // macos, linux or windows
	Arch    string `json:"arch"` // amd64 or arm64
	Version string `json:"version"`
	File    string `json:"file"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
}

// Downloads is the answer of GET /api/v1/downloads.
type Downloads struct {
	Version  string    `json:"version,omitempty"` // newest version
	Packages []Package `json:"packages"`
}

// listDownloads reads the directory. Every portal, the dev one too, offers the
// packages of the real network: dev packages are for testing, reachable by name
// but not listed. A missing directory is simply an empty list.
func (s *Server) listDownloads() Downloads {
	out := Downloads{Packages: []Package{}}
	if s.cfg.DownloadsDir == "" {
		return out
	}
	entries, err := os.ReadDir(s.cfg.DownloadsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.log.Warn("downloads directory", "err", err)
		}
		return out
	}
	newest := map[string]Package{}
	for _, e := range entries {
		m := packageRe.FindStringSubmatch(e.Name())
		if m == nil || m[2] != "" || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind := "desktop"
		if m[1] != "" {
			kind = "cli"
		}
		p := Package{Kind: kind, OS: m[4], Arch: m[5], Version: m[3], File: e.Name(), URL: "/downloads/" + e.Name(), Size: info.Size()}
		key := kind + "/" + p.OS + "/" + p.Arch
		if old, ok := newest[key]; !ok || version.Compare(p.Version, old.Version) > 0 {
			newest[key] = p
		}
		if out.Version == "" || version.Compare(p.Version, out.Version) > 0 {
			out.Version = p.Version
		}
	}
	sums := map[string]map[string]string{} // by version
	for _, p := range newest {
		if sums[p.Version] == nil {
			sums[p.Version] = readChecksums(filepath.Join(s.cfg.DownloadsDir, "SHA256SUMS-"+p.Version))
		}
		p.SHA256 = sums[p.Version][p.File]
		out.Packages = append(out.Packages, p)
	}
	sort.Slice(out.Packages, func(i, j int) bool { return out.Packages[i].File < out.Packages[j].File })
	return out
}

// readChecksums parses the output of shasum: "<hex>  <file>" lines.
func readChecksums(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 2 {
			out[strings.TrimPrefix(fields[1], "*")] = fields[0]
		}
	}
	return out
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.listDownloads())
}

// handleDownloadFile serves a package or a checksum file. The name must match
// what the build scripts produce, which rules out paths and everything else
// that may be in the directory.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if s.cfg.DownloadsDir == "" || (!packageRe.MatchString(name) && !checksumRe.MatchString(name)) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.cfg.DownloadsDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadWriteTimeout))
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, info.ModTime(), f)
}
