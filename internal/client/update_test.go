package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sharetoken/internal/accounts"
	"sharetoken/internal/portal"
	"sharetoken/internal/version"
)

func TestUpdateFor(t *testing.T) {
	c := portal.UpdateInfo{ClientVersions: portal.ClientVersions{Min: "1.0.0", Recommended: "1.1.0", Latest: "1.2.0", DownloadURL: "https://x/d"}}
	for v, want := range map[string][2]bool{ // available, required
		"0.9.0":         {true, true},
		"1.0.0":         {true, false},
		"1.1.0":         {true, false},
		"1.2.0":         {false, false},
		"1.3.0":         {false, false},
		"1.0.0-dev.abc": {true, false},
		"0.9.0-dev.abc": {true, true},
		"dev":           {false, false},
	} {
		u := updateFor(version.Info{Version: v}, c, "https://p")
		if u.Available != want[0] || u.Required != want[1] || u.Latest != "1.2.0" || u.DownloadURL != "https://x/d" {
			t.Errorf("%s: %+v, want %v", v, u, want)
		}
		if u.Available != (u.Key == "1.2.0") {
			t.Errorf("%s: key %q", v, u.Key)
		}
	}
	if u := updateFor(version.Info{Version: "1.0.0"}, portal.UpdateInfo{}, "https://p"); u.Available || u.Required {
		t.Errorf("no policy: %+v", u)
	}
}

// Before launch the version stays 1.0.0: a newer build of it is an update, when
// a package for this client is published.
func TestUpdateForRebuild(t *testing.T) {
	rel := &portal.Release{Version: "1.0.0", Commit: "new", BuildTime: "2026-11-10T08:00:00Z", NotesURL: "/changelog/#v1.0.0",
		Notes: map[string]portal.ReleaseNotes{"en": {Summary: "A smaller window", Body: "# New\nA smaller window"}}}
	pkg := &portal.Package{File: "sharetoken-cli-1.0.0-linux-amd64.tar.gz", URL: "/downloads/sharetoken-cli-1.0.0-linux-amd64.tar.gz", Size: 9, SHA256: "ab"}
	info := portal.UpdateInfo{ClientVersions: portal.ClientVersions{Latest: "1.0.0", DownloadURL: "/#download"}, Release: rel, Package: pkg}
	old := version.Info{Version: "1.0.0", Commit: "old", BuildTime: "2026-11-01T00:00:00Z"}
	u := updateFor(old, info, "https://portal.example/")
	if !u.Available || u.Required || u.Key != "1.0.0+new" || u.Latest != "1.0.0" || u.Notes["en"].Summary != "A smaller window" {
		t.Fatalf("%+v", u)
	}
	if u.Package == nil || u.Package.URL != "https://portal.example/downloads/sharetoken-cli-1.0.0-linux-amd64.tar.gz" || u.Package.SHA256 != "ab" ||
		u.NotesURL != "https://portal.example/changelog/#v1.0.0" || u.DownloadURL != "https://portal.example/#download" {
		t.Fatalf("links: %+v %+v", u, u.Package)
	}
	for name, cur := range map[string]version.Info{
		"same commit": {Version: "1.0.0", Commit: "new", BuildTime: "2026-11-01T00:00:00Z"},
		"built later": {Version: "1.0.0", Commit: "mine", BuildTime: "2026-11-11T00:00:00Z"},
		"no commit":   {Version: "1.0.0", BuildTime: "2026-11-01T00:00:00Z"},
		"no time":     {Version: "1.0.0", Commit: "old"},
		"newer":       {Version: "1.1.0", Commit: "old", BuildTime: "2026-11-01T00:00:00Z"},
		"not stamped": {Version: "dev"},
	} {
		if u := updateFor(cur, info, "https://p"); u.Available {
			t.Errorf("%s: %+v", name, u)
		}
	}
	// A dev build of the release, against the dev package.
	devInfo := info
	devInfo.Release = &portal.Release{Version: "1.0.0-dev.new", Commit: "new", BuildTime: rel.BuildTime}
	devInfo.Latest = ""
	if u := updateFor(version.Info{Version: "1.0.0-dev.old", Commit: "old", BuildTime: "2026-11-01T00:00:00Z"}, devInfo, "https://p"); !u.Available || u.Latest != "1.0.0-dev.new" {
		t.Fatalf("dev: %+v", u)
	}
	// Without a package for this client the rebuild is not offered.
	info.Package = nil
	if u := updateFor(old, info, "https://p"); u.Available {
		t.Fatalf("no package: %+v", u)
	}
	// Links only to the web.
	if absURL("https://p", "javascript:alert(1)") != "" || absURL("https://p", "file:///etc/passwd") != "" {
		t.Fatal("non-web link kept")
	}
}

// Every request to the portal says what is asking, and refusals keep their code.
func TestPortalRequestsCarryVersion(t *testing.T) {
	var ver, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ver, ua = r.Header.Get(portal.ClientVersionHeader), r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"upgrade_required","message":"too old"}}`))
	}))
	defer srv.Close()
	err := NewPortalClient(srv.URL, "", "").Do(context.Background(), http.MethodGet, "/api/v1/me", nil, nil)
	pe, ok := err.(*PortalError)
	if !ok || pe.Code != "upgrade_required" || pe.Status != http.StatusUpgradeRequired {
		t.Fatalf("%v", err)
	}
	if ver != version.Version || !strings.HasPrefix(ua, "sharetoken-client/"+version.Version) {
		t.Fatalf("version %q, user agent %q", ver, ua)
	}
}

// A configuration of an unknown format is not guessed at.
func TestConfigVersion(t *testing.T) {
	home := t.TempDir()
	c := DefaultConfig()
	c.User = "alice"
	if err := SaveConfig(home, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(home)
	if err != nil || got.User != "alice" || got.Version != ConfigVersion {
		t.Fatalf("%+v %v", got, err)
	}
	writeConfig := func(s string) {
		if err := osWriteConfig(home, s); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(`{"version":99,"env":"dev","user":"alice"}`)
	if _, err := LoadConfig(home); err == nil || !strings.Contains(err.Error(), "newer client") {
		t.Fatalf("newer format: %v", err)
	}
	writeConfig(`{"env":"dev","user":"alice"}`)
	got, err = LoadConfig(home)
	if err != nil || got.User != "" {
		t.Fatalf("unversioned format is signed out: %+v %v", got, err)
	}
}

func osWriteConfig(home, s string) error {
	return os.WriteFile(filepath.Join(home, ConfigFile), []byte(s), 0o600)
}

// stampVersion makes this test binary a build of v.
func stampVersion(t *testing.T, v, commit, built string) {
	ov, oc, ob := version.Version, version.Commit, version.BuildTime
	version.Version, version.Commit, version.BuildTime = v, commit, built
	t.Cleanup(func() { version.Version, version.Commit, version.BuildTime = ov, oc, ob })
}

// The session says what the daemon is and what the portal recommends; the
// user can skip a recommended build, which is remembered, but not a required one.
func TestSessionReportsVersionAndUpdate(t *testing.T) {
	stampVersion(t, "1.0.0", "old", "2026-11-01T00:00:00Z")
	e := newDaemonEnv(t, openPortal(), false)
	t.Setenv("SHARETOKEN_PORTAL", e.url)
	db := e.portal.Store().DB
	if err := accounts.SaveVersionPolicy(db, accounts.VersionPolicy{LatestClient: "99.0.0", DownloadURL: "/d", AllowDev: true}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	d := e.daemonIn(home)
	d.updates.Check(context.Background())
	s := d.Session()
	if s.Version != version.Version || s.APIVersion != version.API {
		t.Fatalf("%+v", s)
	}
	u := s.Update
	if !u.Available || u.Required || u.Latest != "99.0.0" || u.DownloadURL != e.url+"/d" || u.Key != "99.0.0" || u.Skipped || u.Notified || u.CheckedAt.IsZero() {
		t.Fatalf("%+v", u)
	}

	if st, body := d.call("POST", "/api/v1/update/skip", map[string]string{"key": "98.0.0"}); st != http.StatusConflict {
		t.Fatalf("skipping a build not on offer: %d %s", st, body)
	}
	for _, path := range []string{"/api/v1/update/skip", "/api/v1/update/notified"} {
		if st, body := d.call("POST", path, map[string]string{"key": "99.0.0"}); st != http.StatusOK {
			t.Fatalf("%s: %d %s", path, st, body)
		}
	}
	if u := d.Session().Update; !u.Skipped || !u.Notified {
		t.Fatalf("after skip: %+v", u)
	}
	// Remembered across restarts.
	w := NewUpdateWatcher(home, "desktop", func() (string, string) { return e.url, "" })
	w.Check(context.Background())
	if u := w.Current(); !u.Skipped || !u.Notified {
		t.Fatalf("restart: %+v", u)
	}
	// A required upgrade shows although skipped, and cannot be skipped.
	if err := accounts.SaveVersionPolicy(db, accounts.VersionPolicy{MinClient: "99.0.0", LatestClient: "99.0.0", AllowDev: true}); err != nil {
		t.Fatal(err)
	}
	w.Check(context.Background())
	if u := w.Current(); !u.Required || u.Skipped {
		t.Fatalf("required: %+v", u)
	}
	if err := w.Skip("99.0.0"); err == nil {
		t.Fatal("skipped a required upgrade")
	}
}
