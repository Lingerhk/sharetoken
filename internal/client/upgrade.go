package client

// One-click updates: the release key, fetching and checking a package, and how
// this copy of the program was installed. The desktop app's shell (gui/update.go)
// and `sharetoken upgrade` install what is fetched here; docs/versioning.md
// "一键升级" has the whole picture.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ReleaseKey is the public half of the release key (scripts/release/sign-packages.go).
// A package is installed only if the key signed its SHA-256. gui/updatekey.go
// holds the same key; a test keeps them equal.
const ReleaseKey = "LOGeF+4zN0c420kfo3JRwLIHC6QT/gKO0pvCL0kPCrY="

// releaseKey is ReleaseKey decoded; tests replace it.
var releaseKey = func() ed25519.PublicKey {
	k, err := base64.StdEncoding.DecodeString(ReleaseKey)
	if err != nil || len(k) != ed25519.PublicKeySize {
		panic("bad release key")
	}
	return k
}()

// maxPackageSize bounds a download: the largest package is well under it.
const maxPackageSize = 512 << 20

// ErrBadPackage: a package did not match its checksum or its signature.
var ErrBadPackage = errors.New("the package failed its check")

// checkDigest says whether digest is the package's checksum and the release key signed it.
func checkDigest(digest []byte, sha256hex, signature string) error {
	want, err := hex.DecodeString(sha256hex)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("%w: no checksum", ErrBadPackage)
	}
	if subtle.ConstantTimeCompare(digest, want) != 1 {
		return fmt.Errorf("%w: checksum mismatch", ErrBadPackage)
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: no signature", ErrBadPackage)
	}
	if !ed25519.Verify(releaseKey, digest, sig) {
		return fmt.Errorf("%w: the signature is not the release key's", ErrBadPackage)
	}
	return nil
}

// VerifyPackage checks a file against its checksum and signature.
func VerifyPackage(path, sha256hex, signature string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	return checkDigest(h.Sum(nil), sha256hex, signature)
}

// UpdateDir is where packages are fetched to, in the state directory.
const UpdateDir = "update"

// DownloadPackage fetches pkg into dir and checks it; the file is kept only if
// it passes. Other files in dir are removed first: only one package is kept.
// progress, if set, is called as bytes arrive.
func DownloadPackage(ctx context.Context, hc *http.Client, pkg *UpdatePackage, dir string, progress func(written, total int64)) (string, error) {
	if pkg == nil || pkg.URL == "" || pkg.File == "" || filepath.Base(pkg.File) != pkg.File || strings.HasPrefix(pkg.File, ".") {
		return "", errors.New("no package to fetch")
	}
	if pkg.SHA256 == "" || pkg.Signature == "" {
		return "", fmt.Errorf("%w: the release did not sign %s", ErrBadPackage, pkg.File)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkg.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: %s", pkg.URL, resp.Status)
	}
	total := resp.ContentLength
	if total <= 0 {
		total = pkg.Size
	}
	if total > maxPackageSize {
		return "", fmt.Errorf("%s is too large (%d bytes)", pkg.File, total)
	}
	part := filepath.Join(dir, pkg.File+".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	var written int64
	buf := make([]byte, 256<<10)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if written += int64(n); written > maxPackageSize {
				f.Close()
				os.Remove(part)
				return "", fmt.Errorf("%s is too large", pkg.File)
			}
			h.Write(buf[:n])
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(part)
				return "", err
			}
			if progress != nil && time.Since(last) > 200*time.Millisecond {
				progress(written, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(part)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return "", err
	}
	if progress != nil {
		progress(written, total)
	}
	if err := checkDigest(h.Sum(nil), pkg.SHA256, pkg.Signature); err != nil {
		os.Remove(part)
		return "", err
	}
	final := filepath.Join(dir, pkg.File)
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return "", err
	}
	return final, nil
}

// Installation says how this copy of the program was installed, which decides
// how it is updated.
type Installation struct {
	// Kind is one of
	//   macos-app    inside a macOS .app bundle (Path: the bundle)
	//   windows-app  from the Windows installer, next to the desktop app (Path: the directory)
	//   linux-deb    from the .deb package (Path: the package name)
	//   cli          anything else: the command-line archive (Path: the program)
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// UpdateKind is the kind of package the portal offers this installation.
func (i Installation) UpdateKind() string {
	if i.Kind == "cli" {
		return "cli"
	}
	return "desktop"
}

// DetectInstallation looks at where this program runs from.
func DetectInstallation() Installation {
	exe, err := os.Executable()
	if err != nil {
		return Installation{Kind: "cli"}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return detectInstallation(runtime.GOOS, exe, "/var/lib/dpkg/info")
}

func detectInstallation(goos, exe, dpkgInfo string) Installation {
	switch goos {
	case "darwin":
		if i := strings.Index(exe, ".app/Contents/MacOS/"); i > 0 {
			return Installation{Kind: "macos-app", Path: exe[:i+len(".app")]}
		}
	case "windows":
		dir := filepath.Dir(exe)
		gui := strings.Replace(filepath.Base(exe), "sharetoken", "sharetoken-gui", 1)
		if _, err := os.Stat(filepath.Join(dir, gui)); err == nil && strings.HasPrefix(strings.ToLower(filepath.Base(exe)), "sharetoken") {
			return Installation{Kind: "windows-app", Path: dir}
		}
	case "linux":
		name := filepath.Base(exe)
		if filepath.Dir(exe) == "/usr/bin" {
			if data, err := os.ReadFile(filepath.Join(dpkgInfo, name+".list")); err == nil {
				for _, l := range strings.Split(string(data), "\n") {
					if l == exe {
						return Installation{Kind: "linux-deb", Path: name}
					}
				}
			}
		}
	}
	return Installation{Kind: "cli", Path: exe}
}
