package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"sharetoken/internal/portal"
	"sharetoken/internal/version"
)

// testReleaseKey makes the release key one the test holds, and returns a signer.
func testReleaseKey(t *testing.T) func(data []byte) (sha, sig string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := releaseKey
	releaseKey = pub
	t.Cleanup(func() { releaseKey = old })
	return func(data []byte) (string, string) {
		d := sha256.Sum256(data)
		return hex.EncodeToString(d[:]), base64.StdEncoding.EncodeToString(ed25519.Sign(priv, d[:]))
	}
}

func TestReleaseKeyDecodes(t *testing.T) {
	if len(releaseKey) != ed25519.PublicKeySize {
		t.Fatal("release key")
	}
}

func TestVerifyPackage(t *testing.T) {
	sign := testReleaseKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "p.deb")
	data := []byte("the package")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sha, sig := sign(data)
	if err := VerifyPackage(path, sha, sig); err != nil {
		t.Fatalf("good package: %v", err)
	}
	otherSha, otherSig := sign([]byte("another package"))
	_, foreign, _ := ed25519.GenerateKey(rand.Reader)
	d := sha256.Sum256(data)
	for name, c := range map[string][2]string{
		"wrong checksum":            {otherSha, sig},
		"signature of another file": {sha, otherSig},
		"another key":               {sha, base64.StdEncoding.EncodeToString(ed25519.Sign(foreign, d[:]))},
		"no signature":              {sha, ""},
		"no checksum":               {"", sig},
	} {
		if err := VerifyPackage(path, c[0], c[1]); !errors.Is(err, ErrBadPackage) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDownloadPackage(t *testing.T) {
	sign := testReleaseKey(t)
	data := []byte("installer bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tampered" {
			w.Write([]byte("installer bytez"))
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	sha, sig := sign(data)
	dir := filepath.Join(t.TempDir(), UpdateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "old.exe"), []byte("old"), 0o600)
	var seen int64
	path, err := DownloadPackage(context.Background(), srv.Client(), &UpdatePackage{File: "s.exe", URL: srv.URL + "/s.exe", SHA256: sha, Signature: sig}, dir, func(w, _ int64) { seen = w })
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(data) || seen != int64(len(data)) {
		t.Fatalf("got %q, progress %d", got, seen)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.exe")); err == nil {
		t.Error("an older package was kept")
	}
	// Bytes that do not match: nothing is left.
	if _, err := DownloadPackage(context.Background(), srv.Client(), &UpdatePackage{File: "t.exe", URL: srv.URL + "/tampered", SHA256: sha, Signature: sig}, dir, nil); !errors.Is(err, ErrBadPackage) {
		t.Fatalf("tampered: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
	// An unsigned package is not fetched; nor a name that is a path.
	for _, p := range []*UpdatePackage{
		{File: "s.exe", URL: srv.URL + "/s.exe", SHA256: sha},
		{File: "../s.exe", URL: srv.URL + "/s.exe", SHA256: sha, Signature: sig},
	} {
		if _, err := DownloadPackage(context.Background(), srv.Client(), p, dir, nil); err == nil {
			t.Errorf("%+v: fetched", p)
		}
	}
}

func TestDetectInstallation(t *testing.T) {
	dir := t.TempDir()
	win := filepath.Join(dir, "win")
	_ = os.MkdirAll(win, 0o700)
	_ = os.WriteFile(filepath.Join(win, "sharetoken-gui-dev.exe"), nil, 0o600)
	dpkg := filepath.Join(dir, "dpkg")
	_ = os.MkdirAll(dpkg, 0o700)
	_ = os.WriteFile(filepath.Join(dpkg, "sharetoken.list"), []byte("/.\n/usr\n/usr/bin\n/usr/bin/sharetoken\n/usr/bin/sharetoken-gui\n"), 0o600)
	for _, c := range []struct {
		goos, exe string
		want      Installation
	}{
		{"darwin", "/Applications/ShareToken DEV.app/Contents/MacOS/sharetoken-dev", Installation{"macos-app", "/Applications/ShareToken DEV.app"}},
		{"darwin", "/usr/local/bin/sharetoken", Installation{"cli", "/usr/local/bin/sharetoken"}},
		{"windows", filepath.Join(win, "sharetoken-dev.exe"), Installation{"windows-app", win}},
		{"windows", filepath.Join(dir, "sharetoken.exe"), Installation{"cli", filepath.Join(dir, "sharetoken.exe")}},
		{"linux", "/usr/bin/sharetoken", Installation{"linux-deb", "sharetoken"}},
		{"linux", "/usr/bin/sharetoken-dev", Installation{"cli", "/usr/bin/sharetoken-dev"}},
		{"linux", "/home/u/bin/sharetoken", Installation{"cli", "/home/u/bin/sharetoken"}},
	} {
		if (c.goos == "windows") != (runtime.GOOS == "windows") {
			continue // paths are the host's: Windows cases on Windows, the others elsewhere
		}
		if got := detectInstallation(c.goos, c.exe, dpkg); got != c.want {
			t.Errorf("%s %s: %+v, want %+v", c.goos, c.exe, got, c.want)
		}
	}
	if (Installation{Kind: "cli"}).UpdateKind() != "cli" || (Installation{Kind: "linux-deb"}).UpdateKind() != "desktop" {
		t.Error("update kind")
	}
}

// The daemon fetches the offered build's file in the background and reports it.
func TestWatcherDownload(t *testing.T) {
	sign := testReleaseKey(t)
	data := []byte("app.zip bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer srv.Close()
	sha, sig := sign(data)
	home := t.TempDir()
	w := NewUpdateWatcher(home, "desktop", func() (string, string) { return srv.URL, "" })
	if err := w.StartDownload("1.0.0+abc"); !errors.Is(err, errNotOffered) {
		t.Fatalf("nothing offered: %v", err)
	}
	w.cur = Update{Available: true, Key: "1.0.0+abc", Latest: "1.0.0"}
	if err := w.StartDownload("1.0.0+abc"); !errors.Is(err, errNoInstall) {
		t.Fatalf("no install file: %v", err)
	}
	w.cur.Install = &UpdatePackage{File: "s.app.zip", URL: srv.URL + "/s.app.zip", Size: int64(len(data)), SHA256: sha, Signature: sig}
	if err := w.StartDownload("1.0.0+other"); !errors.Is(err, errNotOffered) {
		t.Fatalf("another key: %v", err)
	}
	if err := w.StartDownload("1.0.0+abc"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		u := w.Current()
		if d := u.Download; d != nil && d.State == "ready" {
			if d.Path != filepath.Join(home, UpdateDir, "s.app.zip") || d.Written != int64(len(data)) {
				t.Fatalf("%+v", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not ready: %+v", u.Download)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A bad signature ends in failed, with nothing kept.
	w.cur.Key = "1.0.0+def"
	w.cur.Install.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := w.StartDownload("1.0.0+def"); err != nil {
		t.Fatal(err)
	}
	for {
		if d := w.Current().Download; d != nil && d.State == "failed" {
			break
		}
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatalf("not failed: %+v", w.Current().Download)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(home, UpdateDir, "s.app.zip")); err == nil {
		t.Error("a file that failed its check was kept")
	}
}

// A rebuild offered only as a file to install (the macOS app's zip) counts too,
// and the file comes with its signature.
func TestUpdateForInstallOnly(t *testing.T) {
	rel := &portal.Release{Version: "1.0.0", Commit: "new", BuildTime: "2026-11-10T08:00:00Z"}
	inst := &portal.Package{File: "sharetoken-1.0.0-macos-arm64.app.zip", URL: "/downloads/sharetoken-1.0.0-macos-arm64.app.zip", SHA256: "ab", Signature: "c2ln"}
	u := updateFor(version.Info{Version: "1.0.0", Commit: "old", BuildTime: "2026-11-01T00:00:00Z"}, portal.UpdateInfo{Release: rel, Install: inst}, "https://p.example")
	if !u.Available || u.Install == nil || u.Install.URL != "https://p.example/downloads/sharetoken-1.0.0-macos-arm64.app.zip" || u.Install.Signature != "c2ln" {
		t.Fatalf("%+v %+v", u, u.Install)
	}
}
