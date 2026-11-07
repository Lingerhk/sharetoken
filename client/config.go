// Package client is the sharetoken user client: one process that is both a
// buyer and (optionally) a seller, under one user account.
//
// It logs in to the portal, binds this device's key to the user, picks the
// relay with the best round-trip time and reaches the coordinator through it,
// serves a local OpenAI-compatible endpoint for buying, and, when sharing is
// on, serves a local model to the network through relay reservations.
//
// Everything a GUI needs is exposed on the same loopback HTTP server under
// /api (status, account, models, relays, sharing settings), next to the
// OpenAI endpoint under /v1.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sharetoken/node"
)

// Config is the client's settings file (config.json in the home directory).
type Config struct {
	Portal     string `json:"portal"`                // portal base URL
	PortalPin  string `json:"portal_pin,omitempty"`  // fingerprint of the portal's self-signed certificate
	User       string `json:"user,omitempty"`        // username of the logged-in user
	Token      string `json:"token,omitempty"`       // portal API token (secret)
	DeviceName string `json:"device_name,omitempty"` // shown in the portal's device list

	HTTP   string   `json:"http"`             // local API and OpenAI endpoint (keep it on loopback)
	Listen []string `json:"listen,omitempty"` // libp2p listen addresses
	NAT    bool     `json:"nat"`              // UPnP/NAT-PMP and hole punching
	Relays []string `json:"relays,omitempty"` // overrides the relays the portal lists

	Buy   BuyConfig   `json:"buy"`
	Share ShareConfig `json:"share"`
}

// BuyConfig tunes buying.
type BuyConfig struct {
	BudgetMicros uint64 `json:"budget_micros"`           // reserved per request; the unused part is refunded
	MaxPriceIn   uint64 `json:"max_price_in,omitempty"`  // per million prompt tokens (0: no limit)
	MaxPriceOut  uint64 `json:"max_price_out,omitempty"` // per million completion tokens
	Evidence     bool   `json:"evidence"`                // disclose a provably bad job's key for an automatic refund
	RequireTEE   bool   `json:"require_tee,omitempty"`
	TEEPolicy    string `json:"tee_policy,omitempty"` // allow-list file of trusted node images
	TEEOnline    bool   `json:"tee_online,omitempty"`
	// QueueSeconds is how long a request waits for a free node when all are
	// busy (0: the default, 30; negative: fail at once).
	QueueSeconds int `json:"queue_seconds,omitempty"`
	// FirstByteSeconds / IdleSeconds: how long a job may wait for the node's
	// first frame / between frames before the node counts as stalled
	// (0: defaults 120 / 60; raise them for slow models with long prompts).
	FirstByteSeconds int `json:"first_byte_seconds,omitempty"`
	IdleSeconds      int `json:"idle_seconds,omitempty"`
}

// ShareConfig tunes sharing a local model.
type ShareConfig struct {
	Enabled        bool               `json:"enabled"`
	Upstream       string             `json:"upstream"`                   // OpenAI-compatible base URL of the local model server
	UpstreamKeyEnv string             `json:"upstream_key_env,omitempty"` // env var holding its API key
	Concurrency    int                `json:"concurrency"`
	Models         []node.ModelConfig `json:"models"`
	Relays         int                `json:"relays"`        // how many relays to hold reservations on
	TEE            string             `json:"tee,omitempty"` // off, auto, sev-snp, tdx
}

// Defaults.
const (
	DefaultHTTP          = "127.0.0.1:9787"
	DefaultUpstream      = "http://127.0.0.1:11434/v1"
	DefaultBudgetMicros  = 200_000
	DefaultPriceInMicros = 100_000
	DefaultPriceOut      = 200_000
)

// DefaultConfig returns a fresh configuration.
func DefaultConfig() Config {
	return Config{
		HTTP:   DefaultHTTP,
		Listen: DefaultListen(),
		NAT:    true,
		Buy:    BuyConfig{BudgetMicros: DefaultBudgetMicros, Evidence: true},
		Share:  ShareConfig{Upstream: DefaultUpstream, Concurrency: 2, Relays: 2},
	}
}

// DefaultListen listens on every interface over TCP and QUIC, IPv4 and
// IPv6. On a machine without IPv6 the IPv6 listeners fail and are skipped.
// Listening on IPv6 lets buyers reach a seller directly when both have IPv6,
// which usually needs no NAT traversal, instead of through a relay.
func DefaultListen() []string {
	return []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1", "/ip6/::/tcp/0", "/ip6/::/udp/0/quic-v1"}
}

var oldDefaultListen = []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"}

func (c *Config) fill() {
	d := DefaultConfig()
	if c.HTTP == "" {
		c.HTTP = d.HTTP
	}
	if len(c.Listen) == 0 || slices.Equal(c.Listen, oldDefaultListen) {
		c.Listen = d.Listen // configs saved before IPv6 was on by default
	}
	if c.Buy.BudgetMicros == 0 {
		c.Buy.BudgetMicros = d.Buy.BudgetMicros
	}
	if c.Share.Upstream == "" {
		c.Share.Upstream = d.Share.Upstream
	}
	if c.Share.Concurrency <= 0 {
		c.Share.Concurrency = d.Share.Concurrency
	}
	if c.Share.Relays <= 0 {
		c.Share.Relays = d.Share.Relays
	}
	if c.Share.TEE == "" {
		c.Share.TEE = "off"
	}
	c.Portal = strings.TrimRight(c.Portal, "/")
}

// Validate checks a share configuration.
func (s *ShareConfig) Validate() error {
	if s.Enabled && len(s.Models) == 0 {
		return errors.New("sharing is on but no model is configured (sharetoken share add <model>)")
	}
	seen := map[string]bool{}
	for _, m := range s.Models {
		if m.ID == "" || m.UpstreamModel == "" {
			return fmt.Errorf("model %+v needs an id and an upstream model", m)
		}
		if seen[m.ID] {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		seen[m.ID] = true
	}
	switch s.TEE {
	case "", "off", "auto", "sev-snp", "tdx":
	default:
		return fmt.Errorf("tee must be off, auto, sev-snp or tdx, not %q", s.TEE)
	}
	return nil
}

// Home is the client's state directory: $SHARETOKEN_HOME, else ~/.sharetoken.
func Home() string {
	if h := os.Getenv("SHARETOKEN_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".sharetoken")
	}
	return ".sharetoken"
}

// Files in the home directory.
const (
	ConfigFile  = "config.json"
	KeyFile     = "device.key"
	NetworkFile = "network.json" // last network info from the portal, used when it is unreachable
)

// LoadConfig reads home/config.json, returning defaults if it does not exist.
func LoadConfig(home string) (Config, error) {
	c := DefaultConfig()
	data, err := os.ReadFile(filepath.Join(home, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Join(home, ConfigFile), err)
	}
	c.fill()
	return c, nil
}

// SaveConfig writes home/config.json (mode 0600: it holds the API token).
func SaveConfig(home string, c Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(home, ConfigFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, ConfigFile))
}
