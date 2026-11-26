package portal

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	// What the release says about itself, by language; a file of another
	// version, an oversized one and one that is not a notes file are left out.
	write("RELEASE-1.0.0.en.md", "# A smaller window\r\n\r\nThe app is **compact** now.\n\n## Changes\n- Relay latency in the menu\n")
	write("RELEASE-1.0.0.zh-CN.md", "# 更小的窗口\n- 菜单里显示中继延迟\n")
	write("RELEASE-0.9.0.en.md", "old")
	write("RELEASE-1.0.0.de.md", strings.Repeat("x", maxNotesBytes+1))
	write("RELEASE-1.0.0.notes.md", "not a language")

	u = getUpdate(t, srv.URL, "kind=cli&channel=prod&os=linux&arch=amd64")
	if n := u.Release.Notes; len(n) != 2 || n["en"].Summary != "The app is compact now." || !strings.HasPrefix(n["en"].Body, "# A smaller window\n\nThe app") ||
		n["zh-CN"].Summary != "" || n["zh-CN"].Body != "# 更小的窗口\n- 菜单里显示中继延迟" {
		t.Fatalf("notes: %+v", n)
	}
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

// A client is offered a file to install by itself only when the release has
// signed it: the installer, the .app in a zip on macOS, the CLI archive.
func TestUpdateInstall(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := downloadsServer(t, dir)
	files := []string{
		"sharetoken-1.0.0-macos-arm64.dmg", "sharetoken-1.0.0-macos-arm64.app.zip",
		"sharetoken-dev-1.0.0-macos-arm64.dmg", "sharetoken-dev-1.0.0-macos-arm64.app.zip",
		"sharetoken-1.0.0-windows-amd64.exe", "sharetoken-1.0.0-linux-arm64.deb",
		"sharetoken-cli-1.0.0-linux-amd64.tar.gz", "sharetoken-1.0.0-macos-amd64.dmg", "sharetoken-1.0.0-macos-amd64.app.zip",
	}
	sums, sigs := "", ""
	for _, f := range files {
		write(f, "x")
		sums += "aa  " + f + "\n"
		if f != "sharetoken-1.0.0-macos-amd64.app.zip" { // published unsigned
			sigs += "c2ln  " + f + "\n"
		}
	}
	write("SHA256SUMS-1.0.0", sums)
	write("SIGNATURES-1.0.0", sigs)
	write("RELEASE-1.0.0.json", `{"version":"1.0.0","commit":"abc1234","build_time":"2026-11-10T08:00:00Z"}`)
	for q, want := range map[string]string{
		"kind=desktop&channel=prod&os=macos&arch=arm64":   "sharetoken-1.0.0-macos-arm64.app.zip",
		"kind=desktop&channel=dev&os=macos&arch=arm64":    "sharetoken-dev-1.0.0-macos-arm64.app.zip",
		"kind=desktop&channel=prod&os=macos&arch=amd64":   "",
		"kind=desktop&channel=prod&os=windows&arch=amd64": "sharetoken-1.0.0-windows-amd64.exe",
		"kind=desktop&channel=prod&os=linux&arch=arm64":   "sharetoken-1.0.0-linux-arm64.deb",
		"kind=cli&channel=prod&os=linux&arch=amd64":       "sharetoken-cli-1.0.0-linux-amd64.tar.gz",
		"kind=cli&channel=prod&os=macos&arch=arm64":       "",
	} {
		u := getUpdate(t, srv.URL, q)
		got := ""
		if p := u.Install; p != nil {
			got = p.File
			if p.Signature != "c2ln" || p.SHA256 != "aa" || p.URL != "/downloads/"+p.File {
				t.Errorf("%s: install %+v", q, p)
			}
		}
		if got != want {
			t.Errorf("%s: install %q, want %q", q, got, want)
		}
	}
	// The zip and the signatures are served; the zip is not listed.
	for _, f := range []string{"sharetoken-1.0.0-macos-arm64.app.zip", "SIGNATURES-1.0.0"} {
		resp, err := http.Get(srv.URL + "/downloads/" + f)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d", f, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/api/v1/downloads")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var d Downloads
	_ = json.NewDecoder(resp.Body).Decode(&d)
	for _, p := range d.Packages {
		if strings.HasSuffix(p.File, ".app.zip") {
			t.Errorf("listed %s", p.File)
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
	if err := accounts.SaveVersionPolicy(db, accounts.VersionPolicy{MinClient: "0.9.0", RecommendedClient: "1.0.0", DownloadURL: "/get"}); err != nil {
		t.Fatal(err)
	}
	u := s.updateInfo("cli", "prod", "linux", "amd64")
	if u.Min != "0.9.0" || u.Recommended != "1.0.0" || u.Latest != "1.0.0" || u.DownloadURL != "/get" || u.Package == nil {
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
