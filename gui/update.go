package main

// One-click updates. The daemon fetches the offered build's package and checks
// it (internal/client/upgrade.go); the shell installs it, since installing ends
// this process:
//
//	macOS    Wails' updater (pkg/updater) swaps the .app bundle for the one in
//	         the package's zip and starts it again (update_darwin.go)
//	Windows  the installer runs silently, and starts the app again (/RELAUNCH)
//	Linux    pkexec dpkg -i installs the .deb; a small shell script starts the
//	         app again once this one has left (update_linux.go)
//
// The page asks for it at /__shell/update (POST, {key}) and follows it there
// (GET). Before installing, the shell stops the daemon it started, which lets
// jobs in flight finish, and checks the package itself against the release key.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// releaseKey repeats internal/client.ReleaseKey, which this module cannot
// import; TestReleaseKeyMatches keeps them equal.
const releaseKey = "LOGeF+4zN0c420kfo3JRwLIHC6QT/gKO0pvCL0kPCrY="

// shellUpdatePath is where the page starts and follows an update; it never reaches the daemon.
const shellUpdatePath = "/__shell/update"

// offer is what the daemon says about the offered build (internal/client.Update).
type offer struct {
	Available bool   `json:"available"`
	Key       string `json:"key"`
	Latest    string `json:"latest"`
	Install   *struct {
		File      string `json:"file"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256"`
		Signature string `json:"signature"`
	} `json:"install"`
	Download *struct {
		State   string `json:"state"`
		Written int64  `json:"written"`
		Total   int64  `json:"total"`
		Path    string `json:"path"`
		Error   string `json:"error"`
	} `json:"download"`
}

// updateStatus is what GET /__shell/update answers.
type updateStatus struct {
	State   string `json:"state"` // idle, downloading, installing, restarting, failed
	Written int64  `json:"written"`
	Total   int64  `json:"total"`
	Error   string `json:"error,omitempty"`
}

type shellUpdater struct {
	sup  *supervisor
	app  func() *application.App
	quit func()

	mu      sync.Mutex
	st      updateStatus
	running bool
}

func newShellUpdater(sup *supervisor, app func() *application.App, quit func()) *shellUpdater {
	return &shellUpdater{sup: sup, app: app, quit: quit, st: updateStatus{State: "idle"}}
}

// supported says whether this copy can install an update by itself; why says
// what stands in the way.
func (u *shellUpdater) supported() (ok bool, why string) {
	return platformCanUpdate()
}

func (u *shellUpdater) status() updateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func (u *shellUpdater) set(st updateStatus) {
	u.mu.Lock()
	u.st = st
	u.mu.Unlock()
}

var errUpdateRunning = errors.New("an update is already being installed")

// start installs the build named key, in the background.
func (u *shellUpdater) start(key string) error {
	if ok, why := u.supported(); !ok {
		return errors.New(why)
	}
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return errUpdateRunning
	}
	u.running = true
	u.st = updateStatus{State: "downloading"}
	u.mu.Unlock()
	go func() {
		err := u.run(context.Background(), key)
		u.mu.Lock()
		defer u.mu.Unlock()
		u.running = false
		if err != nil {
			log.Printf("sharetoken-gui: update: %v", err)
			u.st = updateStatus{State: "failed", Error: err.Error()}
		}
	}()
	return nil
}

// run fetches the build through the daemon and installs it. It returns only on
// failure, or once the app is on its way out.
func (u *shellUpdater) run(ctx context.Context, key string) error {
	o, err := u.fetch(ctx, key)
	if err != nil {
		return err
	}
	if err := verifyPackage(o.Download.Path, o.Install.SHA256, o.Install.Signature); err != nil {
		return err
	}
	u.set(updateStatus{State: "installing"})
	return platformInstall(ctx, u, o)
}

// fetch asks the daemon to fetch and check the package, and waits until it has.
func (u *shellUpdater) fetch(ctx context.Context, key string) (*offer, error) {
	ep := u.sup.endpoint()
	body, _ := json.Marshal(map[string]string{"key": key})
	var o offer
	if err := daemonCall(ctx, ep, http.MethodPost, "/api/v1/update/download", body, &o); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(35 * time.Minute)
	for {
		var s struct {
			Update offer `json:"update"`
		}
		if err := daemonCall(ctx, ep, http.MethodGet, "/api/v1/session", nil, &s); err != nil {
			return nil, err
		}
		o = s.Update
		if !o.Available || o.Key != key || o.Install == nil {
			return nil, errors.New("this build is no longer the one on offer")
		}
		if d := o.Download; d != nil {
			switch d.State {
			case "ready":
				return &o, nil
			case "failed":
				return nil, errors.New(d.Error)
			case "downloading":
				u.set(updateStatus{State: "downloading", Written: d.Written, Total: d.Total})
			}
		}
		if time.Now().After(deadline) {
			return nil, errors.New("fetching the update took too long")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// daemonCall makes one call of the daemon's local API and reads its JSON answer.
func daemonCall(ctx context.Context, ep endpoint, method, path string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := ep.request(ctx, method, path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return errors.New(e.Error.Message)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return json.Unmarshal(data, out)
}

func releaseKeyBytes() ed25519.PublicKey {
	k, _ := base64.StdEncoding.DecodeString(releaseKey)
	return k
}

// verifyPackage checks the fetched file once more, here: the daemon checked it,
// but this is the program that runs it.
func verifyPackage(path, sha256hex, signature string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	d := h.Sum(nil)
	want, err := hex.DecodeString(sha256hex)
	if err != nil || subtle.ConstantTimeCompare(d, want) != 1 {
		return errors.New("the update package does not match its checksum")
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || !ed25519.Verify(releaseKeyBytes(), d, sig) {
		return errors.New("the update package is not signed with the release key")
	}
	return nil
}

// afterUpdateEnv is the environment of the app started after an update: a
// self-test of the update says with $SHARETOKEN_GUI_SELFTEST_AFTER what the new
// copy does; otherwise it is not a self-test.
func afterUpdateEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !hasPrefix(kv, "SHARETOKEN_GUI_SELFTEST=") {
			env = append(env, kv)
		}
	}
	if v := os.Getenv("SHARETOKEN_GUI_SELFTEST_AFTER"); v != "" {
		env = append(env, "SHARETOKEN_GUI_SELFTEST="+v)
	}
	return env
}

// setAfterUpdateEnv makes this process's environment afterUpdateEnv's, for a
// restart that passes it on by itself (Wails' updater on macOS).
func setAfterUpdateEnv() {
	if v := os.Getenv("SHARETOKEN_GUI_SELFTEST_AFTER"); v != "" {
		os.Setenv("SHARETOKEN_GUI_SELFTEST", v)
	} else {
		os.Unsetenv("SHARETOKEN_GUI_SELFTEST")
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// ServeHTTP answers /__shell/update.
func (u *shellUpdater) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(u.status())
	case http.MethodPost:
		var req struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Key == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, `{"error":{"code":"bad_request","message":"key is required"}}`)
			return
		}
		if err := u.start(req.Key); err != nil {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "update_refused", "message": err.Error()}})
			return
		}
		_ = json.NewEncoder(w).Encode(u.status())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// runUpdateCheck is the update self-test ($SHARETOKEN_GUI_SELFTEST=update): it
// asks the daemon for an update and installs it as the page's button does. It
// returns once the app is restarting into the new copy.
func runUpdateCheck(start func() error, sup *supervisor, u *shellUpdater, can bool, why string) error {
	if !can {
		return errors.New("this copy cannot update itself: " + why)
	}
	if err := start(); err != nil {
		return err
	}
	var o offer
	if err := daemonCall(context.Background(), sup.endpoint(), http.MethodPost, "/api/v1/update/check", nil, &o); err != nil {
		return err
	}
	if !o.Available || o.Install == nil {
		return fmt.Errorf("no update with a package to install is on offer (available=%v)", o.Available)
	}
	fmt.Println("selftest: installing", o.Latest, o.Key, o.Install.File)
	if err := u.start(o.Key); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		switch st := u.status(); st.State {
		case "failed":
			return errors.New(st.Error)
		case "restarting":
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("the update did not finish within 10 minutes")
}
