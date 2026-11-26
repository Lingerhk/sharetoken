package client

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"sharetoken/internal/official"
)

// The local API's key. Loopback TCP is open to every process and every user of
// the machine, so the Origin and Host checks only keep web pages out. Callers
// of the management API (/api/...) also prove that they can read this user's
// state directory: each client makes a key at start-up, writes it to RunFile
// (mode 0600 in a 0700 directory) and wants it in every request as
//
//	Authorization: Bearer <key>
//
// The desktop shell, the CLI and scripts read it from there. The key changes at
// each start, so one that leaked (a log, a closed window) ends with that run.
//
// The other way round, the desktop shell checks that the daemon it talks to is
// this user's before it shows its pages or sends a sign-in code there: a
// program of another user may hold the port. GET /api/v1/hello?nonce=… answers
// without the key, with an HMAC of the nonce under it (LocalProof), which only
// the client that wrote RunFile can give; the shell never sends the key to an
// address that has not proved it.

// localKeyBytes is the key's length before hex encoding.
const localKeyBytes = 32

// newLocalKey returns a fresh key for the local API.
func newLocalKey() string {
	b := make([]byte, localKeyBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported systems
	}
	return hex.EncodeToString(b)
}

// LocalProof is what a client answers to the hello at addr (its listener's
// host:port, as in RunFile) with key: HMAC-SHA256 under the key, of the
// address and the caller's nonce. Binding the address stops a program at
// another port from passing the nonce on to the real client and back.
func LocalProof(key, addr, nonce string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte("sharetoken-local-api-hello\n" + addr + "\n" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// validNonce: 16 to 64 bytes in hex.
func validNonce(n string) bool {
	if len(n) < 32 || len(n) > 128 {
		return false
	}
	_, err := hex.DecodeString(n)
	return err == nil
}

// Hello is the answer of GET /api/v1/hello.
type Hello struct {
	Env        string `json:"env"`
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
	Proof      string `json:"proof"`
}

// requireLocalKey lets through requests with the key, and without it only
// those that open allows. The OpenAI endpoint is the API key's instead
// (apikey.go). The path is judged cleaned, as the mux routes it.
func requireLocalKey(key string, open func(method, cleanPath string) bool, api *apiKeyGate, h http.Handler) http.Handler {
	want := []byte("Bearer " + key)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		if rest, key, ok := pathKey(p); ok {
			// The key in the path (/k/<key>/v1/…): for a tool whose
			// headers cannot be set, Codex on its built-in OpenAI provider.
			// The API key buys on /v1 and no more: behind it the
			// management API would be open to whoever holds it, and with
			// `apikey off`, when any path key matches, to everybody.
			ctx := r.Context()
			switch {
			case openOpenAI(rest):
				if !api.matches(key) {
					writeErrorCode(w, http.StatusUnauthorized, "invalid_api_key", "the API key in the path is not this client's")
					return
				}
			case official.IsBackendPath(rest):
				// Codex Desktop's ChatGPT requests under the unlock
				// (chatgpt_base_url). They are open without a key too
				// (openVendor); the key itself, also when /v1 wants none,
				// shows that the caller reads this user's files
				// (official.HasKey).
				if api.is(key) {
					ctx = official.WithKey(ctx)
				}
			default:
				writeErrorCode(w, http.StatusNotFound, "not_found", "the API key in the path is for /v1 only")
				return
			}
			r2 := r.Clone(ctx)
			r2.URL.Path, r2.URL.RawPath = rest, ""
			h.ServeHTTP(w, r2)
			return
		}
		if openOpenAI(p) {
			if ok, why := api.allow(r); !ok {
				if official.Classify(r.Header) == official.CredNone {
					writeErrorCode(w, http.StatusUnauthorized, "invalid_api_key", why)
					return
				}
				// The vendor's own login, without the client's key: the
				// vendor's models only (official.KeyMissing).
				r = r.WithContext(official.WithoutKey(r.Context()))
			}
			h.ServeHTTP(w, r)
			return
		}
		if !open(r.Method, p) && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeErrorCode(w, http.StatusUnauthorized, "local_auth",
				"the local API wants the key from "+RunFile+" in this user's sharetoken directory (Authorization: Bearer <key>)")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// openOpenAI: the OpenAI-compatible endpoint, which wants the API key instead
// of this one (apikey.go); the Origin check keeps web pages out of it.
func openOpenAI(p string) bool { return p == "/v1" || strings.HasPrefix(p, "/v1/") }

// pathKey splits a path that carries the API key, /k/<key>/rest.
func pathKey(p string) (rest, key string, ok bool) {
	after, found := strings.CutPrefix(p, "/k/")
	if !found {
		return "", "", false
	}
	key, rest, _ = strings.Cut(after, "/")
	return "/" + rest, key, key != ""
}

// openVendor: what the tools' vendor logins reach without the key (the
// requests carry their own credentials): the ChatGPT backend passthrough
// (official.ServeBackend) and Claude Code's probe.
func openVendor(p string) bool { return official.IsBackendPath(p) || p == "/api/hello" }

// agentOpen is what `sharetoken up` serves without the key.
func agentOpen(_, p string) bool { return openOpenAI(p) || openVendor(p) }

// daemonOpen is what the daemon serves without the key: also the app's page
// and its files, which hold nothing of the user's, and the hello.
func daemonOpen(method, p string) bool {
	if openOpenAI(p) || openVendor(p) {
		return true
	}
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	return p == "/" || strings.HasPrefix(p, "/ui/") || p == "/api/v1/hello"
}

// writeRunInfo records a client listening at addr with key in home/RunFile.
func writeRunInfo(home, addr, key string) {
	data, _ := json.Marshal(RunInfo{API: addr, PID: os.Getpid(), Key: key})
	_ = os.WriteFile(filepath.Join(home, RunFile), data, 0o600)
}

// LocalAPI returns where the client running with home answers, and its key:
// what it recorded at start-up, else the configured address and no key.
func LocalAPI(home string, cfg Config) (addr, key string) {
	var ri RunInfo
	if data, err := os.ReadFile(filepath.Join(home, RunFile)); err == nil && json.Unmarshal(data, &ri) == nil && ri.API != "" {
		return ri.API, ri.Key
	}
	return cfg.HTTP, ""
}
