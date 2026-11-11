package accounts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"sharetoken/internal/version"
)

// VersionPolicy says which client versions the network serves. It lives in the
// shared database, so the portal and the coordinator apply the same one and an
// operator changes it without a deployment. A database without one serves every
// client (development, tests).
type VersionPolicy struct {
	MinClient         string `json:"min_client,omitempty"`         // older clients are refused new business
	RecommendedClient string `json:"recommended_client,omitempty"` // older clients are told to upgrade
	LatestClient      string `json:"latest_client,omitempty"`
	DownloadURL       string `json:"download_url,omitempty"`
	AllowDev          bool   `json:"allow_dev"` // serve builds that are not releases
}

const versionPolicyKey = "version_policy"

// Validate checks the versions are versions and in order.
func (p VersionPolicy) Validate() error {
	vs := []string{p.MinClient, p.RecommendedClient, p.LatestClient}
	prev := ""
	for _, v := range vs {
		if v == "" {
			continue
		}
		if !version.Valid(v) || version.IsDev(v) {
			return fmt.Errorf("%q is not a release version (1.2.3)", v)
		}
		if prev != "" && version.Compare(prev, v) > 0 {
			return errors.New("want min_client <= recommended_client <= latest_client")
		}
		prev = v
	}
	return nil
}

// Refuses returns why a client of version v may not start new business, or "".
func (p VersionPolicy) Refuses(v string) string {
	if p.MinClient == "" && p.AllowDev {
		return ""
	}
	switch {
	case v == "":
		return "this client does not say its version; install the current one"
	case version.IsDev(v):
		if !p.AllowDev {
			return "development builds (" + v + ") are not served here; install a release"
		}
		// A build not stamped at all has no number to compare.
		if !version.Valid(v) {
			return ""
		}
	}
	if p.MinClient != "" && version.Compare(version.Core(v), p.MinClient) < 0 {
		return "client " + v + " is too old, this network needs " + p.MinClient + " or newer; upgrade to continue"
	}
	return ""
}

// LoadVersionPolicy reads the policy.
func LoadVersionPolicy(db *sql.DB) (VersionPolicy, error) {
	p := VersionPolicy{AllowDev: true}
	var raw string
	err := db.QueryRow("SELECT value FROM meta WHERE `key` = ?", versionPolicyKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p = VersionPolicy{}
	return p, json.Unmarshal([]byte(raw), &p)
}

// SaveVersionPolicy validates and stores the policy.
func SaveVersionPolicy(db *sql.DB, p VersionPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO meta (`key`, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)", versionPolicyKey, string(raw))
	return err
}

// PolicyCache reads the policy at most once per TTL: the coordinator checks it
// on every announce and ticket. If a read fails it keeps the last good policy.
type PolicyCache struct {
	DB  *sql.DB
	TTL time.Duration

	mu     sync.Mutex
	policy VersionPolicy
	read   time.Time
	loaded bool
}

// Get returns the current policy.
func (c *PolicyCache) Get(now time.Time) VersionPolicy {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && now.Sub(c.read) < c.TTL {
		return c.policy
	}
	p, err := LoadVersionPolicy(c.DB)
	if err != nil {
		if !c.loaded {
			return VersionPolicy{AllowDev: true}
		}
		return c.policy
	}
	c.policy, c.read, c.loaded = p, now, true
	return p
}

// SetDeviceVersion records the version a device runs.
func (s *Store) SetDeviceVersion(peer, v string) error {
	if v == "" || len(v) > 64 {
		return nil
	}
	_, err := s.DB.Exec(`UPDATE devices SET client_version = ? WHERE peer = ? AND client_version <> ?`, v, peer, v)
	return err
}

// SetDevicePlatform records the operating system a device runs on (a GOOS
// name sent by the client); anything else is ignored.
func (s *Store) SetDevicePlatform(peer, os string) error {
	if !validPlatform(os) {
		return nil
	}
	_, err := s.DB.Exec(`UPDATE devices SET client_os = ? WHERE peer = ? AND client_os <> ?`, os, peer, os)
	return err
}

func validPlatform(os string) bool {
	switch os {
	case "windows", "linux", "darwin", "freebsd":
		return true
	}
	return false
}

// SetDeviceVersionTx is SetDeviceVersion inside a transaction.
func SetDeviceVersionTx(tx *sql.Tx, peer, v string) error {
	if v == "" || len(v) > 64 {
		return nil
	}
	_, err := tx.Exec(`UPDATE devices SET client_version = ? WHERE peer = ? AND client_version <> ?`, v, peer, v)
	return err
}

// VersionCount is how many devices run one version, and on which systems.
type VersionCount struct {
	Version string `json:"version"`
	Devices int64  `json:"devices"`
	Windows int64  `json:"windows"`
	Linux   int64  `json:"linux"`
	MacOS   int64  `json:"macos"`
	Other   int64  `json:"other"` // another system, or one not reported yet
}

// DeviceVersions counts devices per client version and system, most used first.
func (s *Store) DeviceVersions() ([]VersionCount, error) {
	rows, err := s.DB.Query(`SELECT client_version, COUNT(*),
		COALESCE(SUM(client_os = 'windows'), 0), COALESCE(SUM(client_os = 'linux'), 0), COALESCE(SUM(client_os = 'darwin'), 0)
		FROM devices GROUP BY client_version ORDER BY COUNT(*) DESC, client_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionCount{}
	for rows.Next() {
		var c VersionCount
		if err := rows.Scan(&c.Version, &c.Devices, &c.Windows, &c.Linux, &c.MacOS); err != nil {
			return nil, err
		}
		c.Other = c.Devices - c.Windows - c.Linux - c.MacOS
		out = append(out, c)
	}
	return out, rows.Err()
}
