package client

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/secfile"
)

// The OpenAI-compatible endpoint's API key. Loopback is open to every program
// and user of the machine, and /v1 spends the user's credits, so by default it
// wants a key, as an OpenAI API does: Authorization: Bearer <key>, or x-api-key.
// Unlike the management API's key (localauth.go), which changes at each start
// and which the CLI and the app read again for each request, this one stays the
// same until the user makes a new one: tools keep it in their settings.
//
// The key and whether it is required live in APIKeyFile (mode 0600), apart from
// config.json, which the client rewrites. A running client reads the file again
// whenever it changes, so `sharetoken apikey on|off|rotate` needs no restart.

// APIKeyFile holds the endpoint's key and whether it is required.
const APIKeyFile = "apikey.json"

// apiKeyPrefix starts every key, so that a key in a settings file says what it is.
const apiKeyPrefix = "sk-st-"

// APIKeySettings is the content of APIKeyFile.
type APIKeySettings struct {
	Key      string `json:"key"`
	Required bool   `json:"required"`
}

func newAPIKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return apiKeyPrefix + hex.EncodeToString(b)
}

func validAPIKey(k string) bool {
	h, ok := strings.CutPrefix(k, apiKeyPrefix)
	if !ok || len(h) < 32 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// LoadAPIKey reads home's APIKeyFile. A home without one gets a new key, and
// the key is required.
func LoadAPIKey(home string) (APIKeySettings, error) {
	path := filepath.Join(home, APIKeyFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s := APIKeySettings{Key: newAPIKey(), Required: true}
		return s, SaveAPIKey(home, s)
	}
	if err != nil {
		return APIKeySettings{}, err
	}
	var s APIKeySettings
	if err := json.Unmarshal(data, &s); err != nil || !validAPIKey(s.Key) {
		return APIKeySettings{}, fmt.Errorf("%s is damaged: delete it to make a new key", path)
	}
	return s, nil
}

// SaveAPIKey writes home's APIKeyFile.
func SaveAPIKey(home string, s APIKeySettings) error {
	if !validAPIKey(s.Key) {
		return errors.New("not an API key of this client")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return secfile.WriteFile(filepath.Join(home, APIKeyFile), append(data, '\n'))
}

// SetAPIKeyRequired turns the key on or off for home.
func SetAPIKeyRequired(home string, on bool) (APIKeySettings, error) {
	s, err := LoadAPIKey(home)
	if err != nil {
		return s, err
	}
	s.Required = on
	return s, SaveAPIKey(home, s)
}

// RotateAPIKey gives home a new key; the old one stops working.
func RotateAPIKey(home string) (APIKeySettings, error) {
	s, err := LoadAPIKey(home)
	if err != nil {
		s = APIKeySettings{Required: true} // a damaged file is replaced
	}
	s.Key = newAPIKey()
	return s, SaveAPIKey(home, s)
}

// apiKeyGate checks the endpoint's requests against home's APIKeyFile, read
// again when it changes on disk. If the file cannot be read, nothing gets in.
type apiKeyGate struct {
	home string

	mu   sync.Mutex
	mod  time.Time
	size int64
	s    APIKeySettings
	err  error
}

func newAPIKeyGate(home string) (*apiKeyGate, error) {
	g := &apiKeyGate{home: home}
	_, err := g.current()
	return g, err
}

func (g *apiKeyGate) current() (APIKeySettings, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	info, err := os.Stat(filepath.Join(g.home, APIKeyFile))
	if err == nil && g.err == nil && info.ModTime().Equal(g.mod) && info.Size() == g.size {
		return g.s, nil
	}
	g.s, g.err = LoadAPIKey(g.home)
	if info, err := os.Stat(filepath.Join(g.home, APIKeyFile)); err == nil {
		g.mod, g.size = info.ModTime(), info.Size()
	}
	return g.s, g.err
}

// presentedAPIKey is the key a request carries, the OpenAI way or the
// Anthropic way.
func presentedAPIKey(r *http.Request) string {
	if k, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(k)
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// allow reports whether r may use the endpoint, and if not, why.
func (g *apiKeyGate) allow(r *http.Request) (bool, string) {
	s, err := g.current()
	if err != nil {
		return false, err.Error()
	}
	if !s.Required {
		return true, ""
	}
	if subtle.ConstantTimeCompare([]byte(presentedAPIKey(r)), []byte(s.Key)) == 1 {
		return true, ""
	}
	return false, "this endpoint wants the client's API key (Authorization: Bearer <key>): `sharetoken apikey` shows it, and the desktop app shows it next to the endpoint"
}

// apiKeyView is what the management API says about the key.
type apiKeyView struct {
	Required bool   `json:"required"`
	Key      string `json:"key"`
}

// apiKeyRoutes serves the key's settings on the management API, which only
// the holder of the local key reaches (localauth.go):
//
//	GET  /api/v1/apikey          {"required": …, "key": …}
//	PUT  /api/v1/apikey          {"required": bool}
//	POST /api/v1/apikey/rotate   a new key
func apiKeyRoutes(mux *http.ServeMux, home string) {
	reply := func(w http.ResponseWriter, s APIKeySettings, err error) {
		if err != nil {
			writeErrorCode(w, http.StatusInternalServerError, "apikey", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, apiKeyView{Required: s.Required, Key: s.Key})
	}
	mux.HandleFunc("GET /api/v1/apikey", func(w http.ResponseWriter, r *http.Request) {
		s, err := LoadAPIKey(home)
		reply(w, s, err)
	})
	mux.HandleFunc("PUT /api/v1/apikey", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		Required *bool `json:"required"`
	}) {
		if req.Required == nil {
			writeErrorCode(w, http.StatusBadRequest, "bad_request", `send {"required": true|false}`)
			return
		}
		s, err := SetAPIKeyRequired(home, *req.Required)
		reply(w, s, err)
	}))
	mux.HandleFunc("POST /api/v1/apikey/rotate", func(w http.ResponseWriter, r *http.Request) {
		s, err := RotateAPIKey(home)
		reply(w, s, err)
	})
}

// APIKey is the endpoint's key of the agent's home ("" if it cannot be read).
func (a *Agent) APIKey() string { s, _ := LoadAPIKey(a.home); return s.Key }

// APIKey is the endpoint's key of the daemon's home ("" if it cannot be read).
func (d *Daemon) APIKey() string { s, _ := LoadAPIKey(d.home); return s.Key }
