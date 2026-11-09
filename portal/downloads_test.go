package portal

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sharetoken/internal/mysqldb"
)

func downloadsServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	dsn, err := mysqldb.TestDSN()
	if err != nil {
		t.Fatal(err)
	}
	db, err := mysqldb.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(Config{DB: db, DownloadsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func listDownloadsAPI(t *testing.T, url string) Downloads {
	t.Helper()
	resp, err := http.Get(url + "/api/v1/downloads")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var d Downloads
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDownloads(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const newest = "sharetoken-cli-prod-1.2.0-linux-amd64.tar.gz"
	write(newest, "new linux archive")
	write("sharetoken-cli-prod-1.1.9-linux-amd64.tar.gz", "older")
	write("sharetoken-cli-prod-1.2.0-windows-arm64.zip", "windows archive")
	write("sharetoken-prod-1.2.0-macos-arm64.zip", "desktop")
	write("SHA256SUMS-1.2.0", "abc123  "+newest+"\ndef456  sharetoken-cli-prod-1.2.0-windows-arm64.zip\n")
	write("secret.key", "never served")
	write("sharetoken-cli-prod-1.2.0-linux-amd64.tar.gz.tmp", "half uploaded")
	if err := os.Mkdir(filepath.Join(dir, "sharetoken-cli-prod-1.2.0-macos-arm64.tar.gz"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(dir), "outside-downloads")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	srv := downloadsServer(t, dir)
	d := listDownloadsAPI(t, srv.URL)
	if d.Env != "prod" || d.Version != "1.2.0" {
		t.Errorf("env %q version %q", d.Env, d.Version)
	}
	if len(d.Packages) != 3 {
		t.Fatalf("want 3 packages (newest per kind, system, architecture), got %+v", d.Packages)
	}
	byFile := map[string]Package{}
	for _, p := range d.Packages {
		byFile[p.File] = p
	}
	p, ok := byFile[newest]
	if !ok || p.Kind != "cli" || p.OS != "linux" || p.Arch != "amd64" || p.SHA256 != "abc123" || p.Size != int64(len("new linux archive")) || p.URL != "/downloads/"+newest {
		t.Errorf("linux package: %+v", p)
	}
	if p := byFile["sharetoken-prod-1.2.0-macos-arm64.zip"]; p.Kind != "desktop" || p.OS != "macos" {
		t.Errorf("desktop package: %+v", p)
	}

	get := func(path string, hdr ...string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if len(hdr) == 2 {
			req.Header.Set(hdr[0], hdr[1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}
	resp, body := get("/downloads/" + newest)
	if resp.StatusCode != 200 || body != "new linux archive" {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("content type %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, newest) {
		t.Errorf("content disposition %q", cd)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("a download lost the security headers")
	}
	// An interrupted download can resume.
	if resp, body := get("/downloads/"+newest, "Range", "bytes=4-8"); resp.StatusCode != 206 || body != "linux" {
		t.Errorf("range: %d %q", resp.StatusCode, body)
	}
	if resp, body := get("/downloads/SHA256SUMS-1.2.0"); resp.StatusCode != 200 || !strings.Contains(body, "abc123") {
		t.Errorf("checksums: %d %q", resp.StatusCode, body)
	}
	for _, path := range []string{
		"/downloads/secret.key",
		"/downloads/sharetoken-cli-prod-1.2.0-linux-amd64.tar.gz.tmp",
		"/downloads/sharetoken-cli-prod-1.2.0-macos-arm64.tar.gz", // a directory
		"/downloads/..%2foutside-downloads",
		"/downloads/%2e%2e%2foutside-downloads",
		"/downloads/SHA256SUMS-..%2f..%2fetc",
		"/downloads/",
	} {
		if resp, body := get(path); resp.StatusCode == 200 {
			t.Errorf("%s: served %q", path, body)
		}
	}
}

func TestDownloadsWithoutDirectory(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		srv := downloadsServer(t, dir)
		d := listDownloadsAPI(t, srv.URL)
		if d.Packages == nil || len(d.Packages) != 0 || d.Env != "" {
			t.Errorf("dir %q: %+v", dir, d)
		}
		resp, err := http.Get(srv.URL + "/downloads/sharetoken-cli-prod-1.2.0-linux-amd64.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("dir %q: status %d", dir, resp.StatusCode)
		}
	}
}

func TestDocsRoutes(t *testing.T) {
	srv := downloadsServer(t, "")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := noRedirect.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}
	if resp, _ := get("/docs"); resp.StatusCode != 301 || resp.Header.Get("Location") != "/docs/" {
		t.Errorf("/docs: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, path := range []string{"/docs/", "/docs/quickstart", "/docs/troubleshooting"} {
		resp, body := get(path)
		if resp.StatusCode != 200 || !strings.Contains(body, `id="doc"`) {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
		if resp.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: no CSP", path)
		}
	}
	// An unknown page is a real 404, with the guide's navigation.
	if resp, body := get("/docs/no-such-page"); resp.StatusCode != 404 || !strings.Contains(body, `id="doc"`) {
		t.Errorf("unknown page: %d", resp.StatusCode)
	}
	if resp, body := get("/static/docs/en/quickstart.md"); resp.StatusCode != 200 || !strings.Contains(body, "{{exe}}") {
		t.Errorf("markdown: %d", resp.StatusCode)
	}
	if resp, _ := get("/static/docs-nav.json"); resp.StatusCode != 200 {
		t.Errorf("nav: %d", resp.StatusCode)
	}
	for _, path := range []string{"/static/docs/en/nope.md", "/static/docs/..%2f..%2fweb.go/x", "/static/docs/en/..%2f..%2fdocs.html"} {
		if resp, body := get(path); resp.StatusCode == 200 && !strings.Contains(path, "docs.html") {
			t.Errorf("%s: served %q", path, body[:min(len(body), 60)])
		}
	}
}
