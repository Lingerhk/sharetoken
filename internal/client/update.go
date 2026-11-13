package client

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"sharetoken/internal/portal"
	"sharetoken/internal/secfile"
	"sharetoken/internal/version"
)

// userAgent says what this client is. The portal logs it; decisions use the
// version header.
func userAgent() string {
	return version.UserAgent("client") + " (" + Env + "; " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// Update tells the app and the CLI whether a newer client exists.
type Update struct {
	Available bool   `json:"available"`        // a newer release is recommended
	Required  bool   `json:"required"`         // this version is below the network's minimum
	Latest    string `json:"latest,omitempty"` // the newest release
	// DownloadURL is the download page; Package the file for this system, when
	// the portal publishes one. NotesURL says what changed. All absolute.
	DownloadURL string         `json:"download_url,omitempty"`
	Package     *UpdatePackage `json:"package,omitempty"`
	NotesURL    string         `json:"notes_url,omitempty"`
	// Notes is what the release says about itself, by language: a one-line
	// summary and a text in the release-notes markup (portal.ReleaseNotes).
	Notes map[string]portal.ReleaseNotes `json:"notes,omitempty"`
	// Key names the offered build, to skip it or to notify about it once.
	Key string `json:"key,omitempty"`
	// Skipped: the user chose to skip this build (never for a required one).
	// Notified: the desktop app has already shown a notification for it.
	Skipped   bool      `json:"skipped,omitempty"`
	Notified  bool      `json:"notified,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
}

// UpdatePackage is the file to download for this system.
type UpdatePackage struct {
	File   string `json:"file"`
	URL    string `json:"url"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// updateFor compares a build against what the portal reports; base is the
// portal's address, for its relative links. A build without a version number
// (not stamped) has nothing to compare.
func updateFor(cur version.Info, info portal.UpdateInfo, base string) Update {
	u := Update{Latest: info.Latest, DownloadURL: absURL(base, info.DownloadURL)}
	if r := info.Release; r != nil {
		u.Latest = r.Version
		u.NotesURL = absURL(base, r.NotesURL)
		u.Notes = r.Notes
		if p := info.Package; p != nil {
			u.Package = &UpdatePackage{File: p.File, URL: absURL(base, p.URL), Size: p.Size, SHA256: p.SHA256}
		}
	}
	if !version.Valid(cur.Version) {
		return u
	}
	core := version.Core(cur.Version)
	older := func(than string) bool {
		return than != "" && version.Valid(than) && version.Compare(core, version.Core(than)) < 0
	}
	u.Required = older(info.Min)
	u.Available = u.Required || older(info.Recommended) || older(info.Latest) || (info.Release != nil && older(info.Release.Version)) || rebuilt(cur, info)
	if u.Available {
		u.Key = u.Latest
		if r := info.Release; r != nil && r.Commit != "" {
			u.Key += "+" + r.Commit
		}
	}
	return u
}

// rebuilt reports whether the release is a newer build of this client's
// version: before launch the version stays 1.0.0 while the code changes. Only
// a published package for this client counts, so a dev desktop app is not told
// about a build it cannot get.
func rebuilt(cur version.Info, info portal.UpdateInfo) bool {
	r := info.Release
	if r == nil || info.Package == nil || r.Commit == "" || cur.Commit == "" || r.Commit == cur.Commit || !version.Valid(r.Version) {
		return false
	}
	if version.Compare(version.Core(r.Version), version.Core(cur.Version)) != 0 {
		return false
	}
	rt, err1 := time.Parse(time.RFC3339, r.BuildTime)
	ct, err2 := time.Parse(time.RFC3339, cur.BuildTime)
	return err1 == nil && err2 == nil && rt.After(ct)
}

// absURL resolves a link of the portal against its address. Only http(s)
// links are kept: the app opens them.
func absURL(base, ref string) string {
	if ref == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u, err := b.Parse(ref)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.String()
}

// platform names this system as the portal's packages do.
func platform() (osName, arch string) {
	osName = runtime.GOOS
	if osName == "darwin" {
		osName = "macos"
	}
	return osName, runtime.GOARCH
}

// Version asks the portal what it runs and which clients it wants.
func (p *PortalClient) Version(ctx context.Context) (*portal.VersionInfo, error) {
	var out portal.VersionInfo
	err := p.Do(ctx, http.MethodGet, "/api/v1/version", nil, &out)
	return &out, err
}

// UpdateInfo asks the portal about the current release for a client of kind
// desktop or cli, of this build type and system.
func (p *PortalClient) UpdateInfo(ctx context.Context, kind string) (*portal.UpdateInfo, error) {
	osName, arch := platform()
	q := url.Values{"kind": {kind}, "channel": {Env}, "os": {osName}, "arch": {arch}}
	var out portal.UpdateInfo
	err := p.Do(ctx, http.MethodGet, "/api/v1/update?"+q.Encode(), nil, &out)
	return &out, err
}

// CheckUpdate asks a portal once and compares this build.
func CheckUpdate(ctx context.Context, base, pin, kind string) (Update, error) {
	info, err := NewPortalClient(base, "", pin).UpdateInfo(ctx, kind)
	if err != nil {
		return Update{}, err
	}
	u := updateFor(version.Current(), *info, base)
	u.CheckedAt = time.Now().UTC()
	return u, nil
}

// updateCheckEvery is how often a running client looks for a new release.
const updateCheckEvery = time.Hour

// UpdateFile keeps what the user did about updates.
const UpdateFile = "update.json"

type updateState struct {
	Skipped  string `json:"skipped,omitempty"`  // key of the build the user skipped
	Notified string `json:"notified,omitempty"` // key of the last build the app notified about
}

// UpdateWatcher checks for a new release when started and then every hour.
// The daemon and `sharetoken up` each run one; the agent reports it in its status.
type UpdateWatcher struct {
	home   string
	kind   string                    // desktop or cli
	portal func() (base, pin string) // where to ask
	log    *slog.Logger

	mu     sync.Mutex
	cur    Update
	state  updateState
	logged string // key last written to the log
}

// NewUpdateWatcher prepares a watcher; portal says which portal to ask.
func NewUpdateWatcher(home, kind string, portal func() (base, pin string)) *UpdateWatcher {
	w := &UpdateWatcher{home: home, kind: kind, portal: portal, log: slog.Default().With("component", "update")}
	if data, err := os.ReadFile(filepath.Join(home, UpdateFile)); err == nil {
		_ = json.Unmarshal(data, &w.state)
	}
	return w
}

// Run checks until ctx ends.
func (w *UpdateWatcher) Run(ctx context.Context) {
	for {
		w.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateCheckEvery):
		}
	}
}

// Check asks the portal now. A failure keeps the last answer.
func (w *UpdateWatcher) Check(ctx context.Context) {
	base, pin := w.portal()
	if base == "" {
		base = PortalAddr()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u, err := CheckUpdate(ctx, base, pin, w.kind)
	if err != nil {
		w.log.Debug("checking for a new release", "err", err)
		return
	}
	w.mu.Lock()
	w.cur = u
	logIt := u.Available && u.Key != w.logged
	if logIt {
		w.logged = u.Key
	}
	w.mu.Unlock()
	if logIt {
		where := u.DownloadURL
		if u.Package != nil {
			where = u.Package.URL
		}
		if u.Required {
			w.log.Warn("this client is older than the network's minimum version: it can settle jobs it has, but not start new ones; upgrade to "+u.Latest, "current", version.Version, "download", where)
		} else {
			w.log.Warn("a new release of sharetoken is available: "+u.Latest, "current", version.Version, "download", where, "notes", u.NotesURL)
		}
	}
}

// Current is the last answer, with what the user did about it.
func (w *UpdateWatcher) Current() Update {
	w.mu.Lock()
	defer w.mu.Unlock()
	u := w.cur
	if u.Available {
		u.Skipped = !u.Required && u.Key == w.state.Skipped
		u.Notified = u.Key == w.state.Notified
	}
	return u
}

// errNotOffered: the key is not the build on offer (the page is out of date).
var errNotOffered = errors.New("this build is no longer the one on offer")

// Skip stops offering the build named key. A required upgrade cannot be skipped.
func (w *UpdateWatcher) Skip(key string) error {
	return w.record(key, func(s *updateState) { s.Skipped = key }, true)
}

// MarkNotified records that the app has notified about the build named key.
func (w *UpdateWatcher) MarkNotified(key string) error {
	return w.record(key, func(s *updateState) { s.Notified = key }, false)
}

func (w *UpdateWatcher) record(key string, set func(*updateState), skip bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.cur.Available || key != w.cur.Key || (skip && w.cur.Required) {
		return errNotOffered
	}
	next := w.state
	set(&next)
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := secfile.WriteFile(filepath.Join(w.home, UpdateFile), data); err != nil {
		return err
	}
	w.state = next
	return nil
}
