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
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"sharetoken/internal/node"
	"sharetoken/internal/secfile"
)

// Config is the client's settings file (config.json in the home directory).
// ConfigVersion is the format of config.json this build reads and writes.
const ConfigVersion = 1

type Config struct {
	Version    int    `json:"version"`               // format of this file (ConfigVersion)
	Env        string `json:"env"`                   // build type that wrote this file (see LoadConfig)
	Portal     string `json:"portal"`                // portal base URL
	PortalPin  string `json:"portal_pin,omitempty"`  // fingerprint of the portal's self-signed certificate
	User       string `json:"user,omitempty"`        // username of the logged-in user
	Token      string `json:"token,omitempty"`       // portal API token (secret)
	DeviceName string `json:"device_name,omitempty"` // shown in the portal's device list

	HTTP   string   `json:"http"`             // local API and OpenAI endpoint (keep it on loopback)
	Listen []string `json:"listen,omitempty"` // libp2p listen addresses
	NAT    bool     `json:"nat"`              // UPnP/NAT-PMP and hole punching
	Relays []string `json:"relays,omitempty"` // overrides the relays the portal lists
	// PrivateRelays are relays the user runs: a sharing node reserves on
	// them, nothing else uses them (private_relays.go).
	PrivateRelays []string `json:"private_relays,omitempty"`

	Buy   BuyConfig   `json:"buy"`
	Share ShareConfig `json:"share"`
	// Tailnet is the direct path over Tailscale's data plane (tailnet.go).
	Tailnet TailnetConfig `json:"tailnet"`
}

// BuyConfig tunes buying.
type BuyConfig struct {
	BudgetMicros uint64 `json:"budget_micros"`           // reserved per request; the unused part is refunded
	MaxPriceIn   uint64 `json:"max_price_in,omitempty"`  // per million prompt tokens (0: no limit)
	MaxPriceOut  uint64 `json:"max_price_out,omitempty"` // per million completion tokens
	Evidence     bool   `json:"evidence"`                // disclose a provably bad job's key for an automatic refund
	RequireTEE   bool   `json:"require_tee,omitempty"`
	// RequireNative buys only from nodes whose model server speaks the
	// request's API natively (Claude Code from Anthropic models, Codex from
	// OpenAI Responses), never converted. Off, such nodes are preferred and
	// others convert when none is free.
	RequireNative bool `json:"require_native,omitempty"`
	// LoopGuard stops paying for a model stuck in a loop: an answer whose
	// reasoning or text goes round in place is ended as a length stop, and
	// a request whose last eight turns are the same tool call with the same
	// result is answered locally and sent nowhere. Unset means on.
	LoopGuard *bool `json:"loop_guard,omitempty"`
	// SpotChecks lets the platform's audits of sellers travel through this
	// client (buyer.RunErrands): now and then it carries one short, sealed
	// job to a seller as if it were its own. It cannot read the job, pays
	// nothing, and the seller sees this device as it sees any buyer. Unset
	// means on.
	SpotChecks *bool  `json:"spot_checks,omitempty"`
	TEEPolicy  string `json:"tee_policy,omitempty"` // allow-list file of trusted node images
	TEEOnline  bool   `json:"tee_online,omitempty"`
	// QueueSeconds is how long a request waits for a free node when all are
	// busy (0: the default, 30; negative: fail at once).
	QueueSeconds int `json:"queue_seconds,omitempty"`
	// FirstByteSeconds / IdleSeconds: how long a job may wait for the node's
	// first frame / between frames before the node counts as stalled
	// (0: defaults 30 / 30). Nodes send a progress frame every 10 seconds
	// while the model is silent, so slow models need no higher values.
	FirstByteSeconds int `json:"first_byte_seconds,omitempty"`
	IdleSeconds      int `json:"idle_seconds,omitempty"`
}

// ShareConfig tunes sharing models. Upstream and Models are the model server
// on this machine; hosted APIs the user sells through are kept apart, with
// their keys, in the providers file (package provider).
type ShareConfig struct {
	Enabled        bool               `json:"enabled"`
	Upstream       string             `json:"upstream"`                   // OpenAI-compatible base URL of the local model server
	UpstreamKeyEnv string             `json:"upstream_key_env,omitempty"` // env var holding its API key
	Concurrency    int                `json:"concurrency"`
	Models         []node.ModelConfig `json:"models"`
	Relays         int                `json:"relays"` // how many of the network's relays to hold reservations on
	// KeepOfficial keeps those reservations while a private relay works too;
	// otherwise the network's relays are only the fallback.
	KeepOfficial bool   `json:"keep_official,omitempty"`
	TEE          string `json:"tee,omitempty"` // off, auto, sev-snp, tdx
}

// Defaults.
const (
	DefaultUpstream = "http://127.0.0.1:11434/v1"
	// DefaultBudgetMicros is 2 credits: coding agents send prompts of tens of
	// thousands of tokens. Only what a request uses is charged.
	DefaultBudgetMicros = 2_000_000
	// oldDefaultBudgetMicros was the default before; a configuration that
	// still has it gets the new one.
	oldDefaultBudgetMicros = 200_000
	// The range the settings page accepts: 0.01 to 1000 credits.
	MinBudgetMicros      = 10_000
	MaxBudgetMicros      = 1_000_000_000
	DefaultPriceInMicros = 100_000
	DefaultPriceOut      = 200_000
)

// PortalAddr is the portal a new login goes to: the build type's. Other builds
// than prod can be pointed at a test network with $SHARETOKEN_PORTAL; a prod
// build ignores it, so that the environment cannot send sign-ins elsewhere.
func PortalAddr() string {
	if v := strings.TrimSpace(os.Getenv("SHARETOKEN_PORTAL")); v != "" && Env != EnvProd {
		return v
	}
	return BuiltInPortal()
}

// DefaultConfig returns a fresh configuration.
func DefaultConfig() Config {
	return Config{
		Env:    Env,
		HTTP:   DefaultHTTP(),
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

// CarriesSpotChecks reports whether the client carries the platform's audits.
func (b BuyConfig) CarriesSpotChecks() bool { return b.SpotChecks == nil || *b.SpotChecks }

func (c *Config) fill() {
	d := DefaultConfig()
	if c.HTTP == "" {
		c.HTTP = d.HTTP
	}
	if len(c.Listen) == 0 {
		c.Listen = d.Listen
	}
	if c.Buy.BudgetMicros == 0 || c.Buy.BudgetMicros == oldDefaultBudgetMicros {
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

// Files in the home directory.
const (
	ConfigFile  = "config.json"
	KeyFile     = "device.key"
	NetworkFile = "network.json" // last network info from the portal, used when it is unreachable
)

// LoadConfig reads home/config.json, returning defaults if it does not exist.
//
// A file without a build type predates them and is not supported: it is deleted
// with the device identity, and the user signs in again. A file of the other
// build type is refused and left alone: this client was pointed at the wrong
// directory, and those are that environment's credentials.
func LoadConfig(home string) (Config, error) {
	c := DefaultConfig()
	data, err := os.ReadFile(filepath.Join(home, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	var file Config
	if err := json.Unmarshal(data, &file); err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Join(home, ConfigFile), err)
	}
	switch {
	case file.Version > ConfigVersion:
		return c, fmt.Errorf("%s was written by a newer client (format %d, this one reads %d): install the current client", filepath.Join(home, ConfigFile), file.Version, ConfigVersion)
	case file.Version < ConfigVersion:
		slog.Warn("deleting a configuration of an unsupported format; sign in again", "home", home, "format", file.Version)
		return DefaultConfig(), removeOldState(home)
	case file.Env != Env:
		return c, fmt.Errorf("%s belongs to a %s client, and this is a %s client: use another directory", filepath.Join(home, ConfigFile), file.Env, Env)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Join(home, ConfigFile), err)
	}
	c.fill()
	return c, nil
}

// removeOldState deletes the state of a format this client does not read.
func removeOldState(home string) error {
	var errs []error
	for _, f := range []string{ConfigFile, KeyFile, NetworkFile} {
		if err := os.Remove(filepath.Join(home, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SaveConfig writes home/config.json (mode 0600: it holds the API token).
func SaveConfig(home string, c Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	c.Version, c.Env = ConfigVersion, Env
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(home, ConfigFile+".tmp")
	if err := secfile.WriteFile(tmp, append(data, '\n')); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, ConfigFile))
}
