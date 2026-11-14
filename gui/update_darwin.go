package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"golang.org/x/sys/unix"
)

// appBundle is the .app this program runs from.
func appBundle() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return "", false
	}
	return exe[:i+len(".app")], true
}

func platformCanUpdate() (bool, string) {
	bundle, ok := appBundle()
	if !ok {
		return false, "this program does not run from an app bundle"
	}
	// An app opened from the disk image, or one macOS runs from a random
	// place because it was never moved (App Translocation), cannot replace itself.
	if strings.HasPrefix(bundle, "/Volumes/") || strings.Contains(bundle, "/AppTranslocation/") {
		return false, "move the app to the Applications folder first"
	}
	if unix.Access(filepath.Dir(bundle), unix.W_OK) != nil {
		return false, "this user cannot write " + filepath.Dir(bundle)
	}
	return true, ""
}

// fileProvider gives Wails' updater the package the daemon fetched: Wails
// checks it again (digest and signature against the release key), unpacks the
// zip, and swaps the bundle once this process has left.
type fileProvider struct {
	mu   sync.Mutex
	rel  *updater.Release
	path string
}

func (p *fileProvider) Name() string { return "sharetoken" }

func (p *fileProvider) Check(context.Context, updater.CheckRequest) (*updater.Release, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rel == nil {
		return nil, nil
	}
	r := *p.rel
	return &r, nil
}

func (p *fileProvider) Download(ctx context.Context, _ *updater.Release, dst io.Writer, onProgress func(written, total int64)) error {
	p.mu.Lock()
	path := p.path
	p.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	n, err := io.Copy(dst, f)
	onProgress(n, info.Size())
	return err
}

var (
	macProvider = &fileProvider{}
	macInit     sync.Once
	macInitErr  error
)

func platformInstall(ctx context.Context, u *shellUpdater, o *offer) error {
	app := u.app()
	if app == nil {
		return errors.New("the app is not running")
	}
	digest, err := hex.DecodeString(o.Install.SHA256)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(o.Install.Signature)
	if err != nil {
		return err
	}
	macProvider.mu.Lock()
	macProvider.rel = &updater.Release{
		Version:      o.Latest,
		Artifact:     updater.Artifact{Filename: o.Install.File, Size: o.Install.Size, Platform: "darwin"},
		Verification: &updater.Verification{DigestAlgo: "sha256", Digest: digest, SignatureAlgo: "ed25519", Signature: sig},
	}
	macProvider.path = o.Download.Path
	macProvider.mu.Unlock()
	macInit.Do(func() {
		macInitErr = app.Updater.Init(updater.Config{
			CurrentVersion: version,
			Providers:      []updater.Provider{macProvider},
			PublicKey:      releaseKeyBytes(),
			Window:         updater.WindowNone,
		})
	})
	if macInitErr != nil {
		return macInitErr
	}
	rel, err := app.Updater.Check(ctx)
	if err != nil {
		return err
	}
	if rel == nil {
		return errors.New("no update to install")
	}
	if err := app.Updater.DownloadAndInstall(ctx); err != nil {
		return err
	}
	u.set(updateStatus{State: "restarting"})
	// The daemon ends its jobs before the helper waits for this process to leave.
	u.sup.stop()
	// The helper takes this process's environment and passes it on to the copy
	// it starts, the new one or, if the swap fails, this one again.
	setAfterUpdateEnv()
	return app.Updater.Restart(ctx)
}

// cleanStaging removes what Wails' updater leaves beside the bundle when an
// update fails after its download (wails-update-* directories), once it is
// surely over.
func cleanStaging() {
	bundle, ok := appBundle()
	if !ok {
		return
	}
	dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(bundle), "wails-update-*"))
	for _, d := range dirs {
		if info, err := os.Stat(d); err == nil && info.IsDir() && time.Since(info.ModTime()) > 10*time.Minute {
			_ = os.RemoveAll(d)
		}
	}
	if info, err := os.Stat(bundle + ".bak"); err == nil && time.Since(info.ModTime()) > 10*time.Minute {
		_ = os.RemoveAll(bundle + ".bak")
	}
}
