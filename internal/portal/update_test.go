package portal

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mysqldb"
)

func getUpdate(t *testing.T, url, query string) UpdateInfo {
	t.Helper()
	resp, err := http.Get(url + "/api/v1/update?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var u UpdateInfo
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

// A client learns the published release and gets the package of its own kind,
// build type, system and architecture.
func TestUpdateInfo(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := downloadsServer(t, dir)

	// Nothing published, no policy: only the download page.
	u := getUpdate(t, srv.URL, "kind=cli&channel=prod&os=linux&arch=amd64")
	if u.Release != nil || u.Package != nil || u.Latest != "" || u.DownloadURL != "/#download" {
		t.Fatalf("empty: %+v", u)
	}

	for _, f := range []string{
		"sharetoken-1.0.0-macos-arm64.dmg", "sharetoken-1.0.0-windows-amd64.exe",
		"sharetoken-cli-1.0.0-linux-amd64.tar.gz", "sharetoken-cli-dev-1.0.0-linux-amd64.tar.gz",
		"sharetoken-cli-0.9.0-linux-arm64.tar.gz",
	} {
		write(f, "x")
	}
	write("SHA256SUMS-1.0.0", "abc  sharetoken-cli-1.0.0-linux-amd64.tar.gz\n")
	write("RELEASE-0.9.0.json", `{"version":"0.9.0","commit":"old","build_time":"2026-01-01T00:00:00Z"}`)
	write("RELEASE-1.0.0.json", `{"version":"1.0.0","commit":"abc1234","build_time":"2026-11-10T08:00:00Z"}`)

	u = getUpdate(t, srv.URL, "kind=cli&channel=prod&os=linux&arch=amd64")
	if u.Release == nil || u.Release.Version != "1.0.0" || u.Release.Commit != "abc1234" || u.Release.BuildTime != "2026-11-10T08:00:00Z" || u.Release.NotesURL != "/changelog/#v1.0.0" {
		t.Fatalf("release: %+v", u.Release)
	}
	if u.Latest != "1.0.0" || u.Package == nil || u.Package.File != "sharetoken-cli-1.0.0-linux-amd64.tar.gz" || u.Package.SHA256 != "abc" || u.Package.URL != "/downloads/sharetoken-cli-1.0.0-linux-amd64.tar.gz" {
		t.Fatalf("cli prod: %+v %+v", u.ClientVersions, u.Package)
	}
	for q, want := range map[string]string{
		"kind=cli&channel=dev&os=linux&arch=amd64":        "sharetoken-cli-dev-1.0.0-linux-amd64.tar.gz",
		"kind=desktop&channel=prod&os=macos&arch=arm64":   "sharetoken-1.0.0-macos-arm64.dmg",
		"kind=desktop&channel=prod&os=windows&arch=amd64": "sharetoken-1.0.0-windows-amd64.exe",
		"kind=desktop&channel=dev&os=macos&arch=arm64":    "", // dev desktop builds are not published
		"kind=cli&channel=prod&os=linux&arch=arm64":       "", // only an older version there
		"kind=cli&channel=prod&os=../../etc&arch=amd64":   "",
		"kind=installer&channel=prod&os=linux&arch=amd64": "",
	} {
		got := ""
		if p := getUpdate(t, srv.URL, q).Package; p != nil {
			got = p.File
		}
		if got != want {
			t.Errorf("%s: package %q, want %q", q, got, want)
		}
	}
}

// The policy's versions come first; a policy naming a version beyond the
// published one sends clients to the download page, with no package.
func TestUpdateInfoPolicy(t *testing.T) {
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "sharetoken-cli-1.0.0-linux-amd64.tar.gz"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "RELEASE-1.0.0.json"), []byte(`{"version":"1.0.0","commit":"abc","build_time":"2026-11-10T08:00:00Z"}`), 0o644)
	s, err := New(Config{DB: db, DownloadsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveVersionPolicy(db, accounts.VersionPolicy{MinClient: "0.9.0", RecommendedClient: "1.0.0", DownloadURL: "https://example.com/get"}); err != nil {
		t.Fatal(err)
	}
	u := s.updateInfo("cli", "prod", "linux", "amd64")
	if u.Min != "0.9.0" || u.Recommended != "1.0.0" || u.Latest != "1.0.0" || u.DownloadURL != "https://example.com/get" || u.Package == nil {
		t.Fatalf("%+v %+v", u.ClientVersions, u.Package)
	}
	if err := accounts.SaveVersionPolicy(db, accounts.VersionPolicy{LatestClient: "1.1.0"}); err != nil {
		t.Fatal(err)
	}
	u = s.updateInfo("cli", "prod", "linux", "amd64")
	if u.Latest != "1.1.0" || u.Release != nil || u.Package != nil {
		t.Fatalf("policy ahead: %+v %+v %+v", u.ClientVersions, u.Release, u.Package)
	}
}
