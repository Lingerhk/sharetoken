// Package provider is the selling side's model sources that are not a model
// server on this machine: hosted APIs the user has an account with, reached
// with an API key or an OAuth login. Each configured provider becomes a
// node.Upstream that takes the OpenAI chat-completions request a buyer sent
// and answers with OpenAI stream chunks, whatever the provider itself speaks.
//
// They are kept in providers.json in the client's state directory, apart from
// config.json: the file holds API keys and OAuth tokens.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/secfile"
)

// FileName is the providers file in the client's state directory.
const FileName = "providers.json"

// FileVersion is the format of providers.json this build reads and writes.
const FileVersion = 1

// Ways to authenticate with a provider.
const (
	AuthAPIKey = "apikey"
	AuthOAuth  = "oauth"
)

// File is providers.json.
type File struct {
	Version   int      `json:"version"`
	Providers []*Entry `json:"providers"`
}

// Entry is one configured provider.
type Entry struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"` // the user's handle for it, unique in the file
	Type    string `json:"type"` // a Kind's ID
	Auth    string `json:"auth"` // AuthAPIKey or AuthOAuth
	Enabled bool   `json:"enabled"`
	// BaseURL overrides the kind's API address; a custom provider needs one.
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"` // secret
	// Headers are sent with every request (e.g. anthropic-workspace-id).
	Headers   map[string]string  `json:"headers,omitempty"`
	OAuth     *Token             `json:"oauth,omitempty"` // secret
	Transport *TransportOptions  `json:"transport,omitempty"`
	Anthropic *AnthropicOptions  `json:"anthropic,omitempty"`
	Identity  *AnthropicIdentity `json:"anthropic_identity,omitempty"` // secret, never in View
	// Models are what this provider sells: network model ID, the provider's
	// name for it, and prices.
	Models  []node.ModelConfig `json:"models"`
	AddedAt time.Time          `json:"added_at,omitzero"`
	// Warmup starts a subscription login's usage windows at times of day
	// (warm.go, internal/warmup).
	Warmup *WarmOptions `json:"warmup,omitempty"`
}

// Token is an OAuth login.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	Email        string    `json:"email,omitempty"`
	AccountUUID  string    `json:"account_uuid,omitempty"` // Anthropic user, not organization
	Scope        string    `json:"scope,omitempty"`
	LoginMode    string    `json:"login_mode,omitempty"`
	// AccountID is the account the token acts for, where requests must name
	// it (ChatGPT account, Anthropic organization).
	AccountID string `json:"account_id,omitempty"`
	// ProjectID is the Google Cloud project of a Gemini or Antigravity login.
	ProjectID string `json:"project_id,omitempty"`
	Plan      string `json:"plan,omitempty"`
	// Extra holds what only one kind needs.
	Extra map[string]string `json:"extra,omitempty"`
}

// Path is where home's providers file is.
func Path(home string) string { return filepath.Join(home, FileName) }

// Load reads home's providers file; a missing file is an empty one.
func Load(home string) (*File, error) {
	f := &File{Version: FileVersion}
	data, err := os.ReadFile(Path(home))
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(home), err)
	}
	if f.Version > FileVersion {
		return nil, fmt.Errorf("%s was written by a newer client (format %d, this one reads %d)", Path(home), f.Version, FileVersion)
	}
	for _, e := range f.Providers {
		if e == nil || e.Name == "" || e.Type == "" {
			return nil, fmt.Errorf("%s: a provider needs a name and a type", Path(home))
		}
	}
	return f, nil
}

// Save writes the file (mode 0600: it holds API keys and tokens).
func (f *File) Save(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f.Version = FileVersion
	if f.Providers == nil {
		f.Providers = []*Entry{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(home) + ".tmp"
	if err := secfile.WriteFile(tmp, append(data, '\n')); err != nil {
		return err
	}
	return os.Rename(tmp, Path(home))
}

// Update reads the file, lets fn change it and writes it back. Everything
// that changes the file goes through here, so that a token refreshed by the
// running client and a setting changed by a command do not undo each other.
func Update(home string, fn func(*File) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlock, err := lockProviderFile(ctx, home, "store")
	if err != nil {
		return err
	}
	defer unlock()
	f, err := Load(home)
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	for _, e := range f.Providers {
		e.ensureIdentity()
	}
	return f.Save(home)
}

// LoadReady persists missing identities once, including for providers written
// by older clients. Read-only display calls may continue to use Load.
func LoadReady(home string) (*File, error) {
	f, err := Load(home)
	if err != nil {
		return nil, err
	}
	for _, e := range f.Providers {
		if e.ID == "" || (e.Type == KindAnthropic && e.Auth == AuthOAuth && e.Identity == nil) {
			if err := Update(home, func(*File) error { return nil }); err != nil {
				return nil, err
			}
			return Load(home)
		}
	}
	return f, nil
}

// Get finds a provider by name.
func (f *File) Get(name string) *Entry {
	for _, e := range f.Providers {
		if strings.EqualFold(e.Name, name) {
			return e
		}
	}
	return nil
}

// ErrNotFound is returned for a provider name that is not configured.
var ErrNotFound = errors.New("no such provider")

// Find is Get with an error for a missing provider.
func (f *File) Find(name string) (*Entry, error) {
	if e := f.Get(name); e != nil {
		return e, nil
	}
	return nil, fmt.Errorf("%w: %q (`sharetoken provider` lists them)", ErrNotFound, name)
}

// Remove deletes a provider by name.
func (f *File) Remove(name string) bool {
	for i, e := range f.Providers {
		if strings.EqualFold(e.Name, name) {
			f.Providers = append(f.Providers[:i], f.Providers[i+1:]...)
			return true
		}
	}
	return false
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// Put adds e, or replaces the provider of the same name, keeping the models
// and the on/off state of the one it replaces when the kind is the same: a
// new key or a new login does not change what is sold.
func (f *File) Put(e *Entry) error {
	e.Name = strings.ToLower(e.Name)
	if !nameRe.MatchString(e.Name) {
		return fmt.Errorf("bad provider name %q: use lower-case letters, digits, '.', '_' and '-'", e.Name)
	}
	if err := e.Validate(); err != nil {
		return err
	}
	for _, old := range f.Providers {
		if !strings.EqualFold(old.Name, e.Name) && e.Type == KindAnthropic && e.OAuth != nil && old.Type == KindAnthropic && old.OAuth != nil {
			if (e.OAuth.AccountUUID != "" && e.OAuth.AccountUUID == old.OAuth.AccountUUID) || (e.OAuth.RefreshToken != "" && e.OAuth.RefreshToken == old.OAuth.RefreshToken) {
				return fmt.Errorf("this Anthropic account is already configured as %s", old.Name)
			}
		}
	}
	for i, old := range f.Providers {
		if strings.EqualFold(old.Name, e.Name) {
			if old.Type == e.Type {
				e.Models, e.AddedAt = old.Models, old.AddedAt
				e.ID = old.ID
				if e.Transport == nil {
					e.Transport = old.Transport
				}
				if e.Warmup == nil {
					e.Warmup = old.Warmup // a new login keeps the account's warm-up
				}
				if e.Anthropic == nil && e.Auth == old.Auth {
					e.Anthropic = old.Anthropic
				}
				if sameAnthropicAccount(old.OAuth, e.OAuth) {
					e.Identity = old.Identity
				}
				if len(e.Headers) == 0 {
					e.Headers = old.Headers
				}
			}
			if err := e.Validate(); err != nil {
				return err
			}
			e.ensureIdentity()
			f.Providers[i] = e
			return nil
		}
	}
	e.ensureIdentity()
	f.Providers = append(f.Providers, e)
	return nil
}

// Validate checks one entry.
func (e *Entry) Validate() error {
	k, ok := LookupKind(e.Type)
	if !ok {
		return fmt.Errorf("unknown provider type %q", e.Type)
	}
	if e.Transport != nil {
		if err := e.Transport.validate(); err != nil {
			return err
		}
	}
	if e.Anthropic != nil {
		if e.Type != KindAnthropic {
			return fmt.Errorf("Anthropic options require an Anthropic provider")
		}
		if err := e.Anthropic.validate(e.Auth); err != nil {
			return err
		}
	}
	switch e.Auth {
	case AuthAPIKey:
		if !k.APIKey {
			return fmt.Errorf("%s has no API-key access: use `sharetoken provider login %s`", k.Name, k.ID)
		}
		if e.APIKey == "" && !k.KeyOptional {
			return fmt.Errorf("%s needs an API key", k.Name)
		}
	case AuthOAuth:
		if !k.OAuth {
			return fmt.Errorf("%s has no OAuth login: use `sharetoken provider add %s`", k.Name, k.ID)
		}
		if e.OAuth == nil || e.OAuth.AccessToken == "" && e.OAuth.RefreshToken == "" {
			return fmt.Errorf("%s is not logged in", k.Name)
		}
	default:
		return fmt.Errorf("auth must be %s or %s, not %q", AuthAPIKey, AuthOAuth, e.Auth)
	}
	if e.URL() == "" {
		return fmt.Errorf("%s needs the base URL of its API", k.Name)
	}
	if err := CheckSecretURL(e.URL()); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range e.Models {
		if m.ID == "" || m.UpstreamModel == "" {
			return fmt.Errorf("provider %s: model %+v needs an id and an upstream model", e.Name, m)
		}
		if seen[m.ID] {
			return fmt.Errorf("provider %s: duplicate model id %q", e.Name, m.ID)
		}
		seen[m.ID] = true
	}
	return nil
}

// URL is the API address requests go to.
func (e *Entry) URL() string {
	if e.BaseURL != "" {
		return strings.TrimRight(e.BaseURL, "/")
	}
	k, _ := LookupKind(e.Type)
	if e.Auth == AuthOAuth {
		return k.OAuthURL
	}
	return k.BaseURL
}

// Selling reports whether the provider is on and has models to sell.
func (e *Entry) Selling() bool { return e.Enabled && len(e.Models) > 0 }

// View is an entry without its secrets, for display and the local API.
type View struct {
	Name            string             `json:"name"`
	Type            string             `json:"type"`
	TypeName        string             `json:"type_name"`
	Auth            string             `json:"auth"`
	Enabled         bool               `json:"enabled"`
	BaseURL         string             `json:"base_url"`
	Key             string             `json:"key,omitempty"`     // masked
	Account         string             `json:"account,omitempty"` // OAuth: who is logged in
	Plan            string             `json:"plan,omitempty"`
	Expires         *time.Time         `json:"token_expires,omitempty"` // OAuth access token; it is renewed by itself
	Headers         []string           `json:"headers,omitempty"`       // names only
	Models          []node.ModelConfig `json:"models"`
	ProxyMode       string             `json:"proxy_mode,omitempty"`
	ProxyAddress    string             `json:"proxy_address,omitempty"`
	Anthropic       *AnthropicOptions  `json:"anthropic,omitempty"`
	LoginMode       string             `json:"login_mode,omitempty"`
	IdentityReady   bool               `json:"identity_ready,omitempty"`
	Runtime         RuntimeStatus      `json:"runtime"`
	CanRefresh      bool               `json:"can_refresh,omitempty"`
	TLSProfile      string             `json:"tls_profile,omitempty"`
	TokenTLSProfile string             `json:"token_tls_profile,omitempty"`
	// WarmCapable: a subscription login with usage windows; Warmup is its
	// warm-up, Usage what the last answers said of its windows.
	WarmCapable bool         `json:"warm_capable,omitempty"`
	Warmup      *WarmOptions `json:"warmup,omitempty"`
	Usage       *UsageLimits `json:"usage,omitempty"`
}

// View hides the secrets of e.
func (e *Entry) View() View {
	k, _ := LookupKind(e.Type)
	v := View{Name: e.Name, Type: e.Type, TypeName: k.Name, Auth: e.Auth, Enabled: e.Enabled, BaseURL: e.URL(), Models: e.Models}
	v.Anthropic = e.Anthropic
	v.Runtime = providerStatus(e.ID)
	if e.WarmCapable() {
		v.WarmCapable, v.Warmup = true, e.Warmup
		if l, ok := UsageOf(e.ID); ok {
			v.Usage = &l
		}
	}
	if e.Transport != nil {
		v.ProxyMode, v.ProxyAddress = e.Transport.ProxyMode, e.Transport.summary()
		v.TLSProfile, v.TokenTLSProfile = e.Transport.TLSProfile, e.Transport.TokenTLSProfile
	}
	if v.Models == nil {
		v.Models = []node.ModelConfig{}
	}
	if e.APIKey != "" {
		v.Key = MaskKey(e.APIKey)
	}
	if t := e.OAuth; t != nil {
		v.CanRefresh = t.RefreshToken != ""
		v.LoginMode = t.LoginMode
		v.IdentityReady = t.AccountUUID != "" && e.Identity != nil
		v.Account, v.Plan = t.Email, t.Plan
		if v.Account == "" {
			v.Account = t.AccountID
		}
		if !t.ExpiresAt.IsZero() {
			v.Expires = &t.ExpiresAt
		}
	}
	for h := range e.Headers {
		v.Headers = append(v.Headers, h)
	}
	return v
}

// MaskKey shows enough of a secret to tell two keys apart, and no more.
func MaskKey(key string) string {
	if len(key) < 12 {
		return "…"
	}
	return key[:3] + "…" + key[len(key)-4:]
}
