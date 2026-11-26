package warmup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sharetoken/internal/provider"
)

// SettingsFile holds the warm-up's settings for the agents on this machine
// (each seller provider's are kept with it in providers.json). It is a file
// of its own so that the CLI can change it while the client runs, which
// reads it again at each look.
const SettingsFile = "warmup.json"

// StateFile holds what the warm-up did and saw, so that a restart sends
// nothing twice.
const StateFile = "warmup-state.json"

// Settings are the warm-up's settings.
type Settings struct {
	// Paused stops every automatic warm-up, the providers' too; Warm up now
	// still works.
	Paused bool  `json:"paused,omitempty"`
	Claude Local `json:"claude"`
	Codex  Local `json:"codex"`
}

// Local is the warm-up of an agent installed on this machine.
type Local struct {
	Enabled bool `json:"enabled"`
	// Times are local times of day to start the 5-hour window at.
	Times []string `json:"times,omitempty"`
	// OnReset: "" no, "week" start the weekly window as it resets, "all"
	// the 5-hour one too.
	OnReset string `json:"on_reset,omitempty"`
	// Model to ask; empty: Haiku for Claude, Codex's default for Codex.
	Model string `json:"model,omitempty"`
	// Binary is the agent's program; empty: found by itself.
	Binary string `json:"binary,omitempty"`
}

// Normalize checks s and writes its times of day as "15:04".
func (s *Settings) Normalize() error {
	for agent, l := range map[string]*Local{AgentClaude: &s.Claude, AgentCodex: &s.Codex} {
		ts, err := provider.NormalizeTimes(l.Times)
		if err != nil {
			return err
		}
		l.Times = ts
		switch l.OnReset {
		case "", provider.WarmOnResetWeek, provider.WarmOnResetAll:
		case "off":
			l.OnReset = ""
		default:
			return fmt.Errorf("on_reset must be off, week or all, not %q", l.OnReset)
		}
		l.Model, l.Binary = strings.TrimSpace(l.Model), strings.TrimSpace(l.Binary)
		if strings.ContainsAny(l.Model, " \t\n\"'") {
			return fmt.Errorf("model %q is not a model name", l.Model)
		}
		if l.Binary != "" {
			if err := checkBinaryPath(agent, l.Binary); err != nil {
				return err
			}
		}
	}
	return nil
}

// LoadSettings reads home's warm-up settings; a missing file is all off.
func LoadSettings(home string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(filepath.Join(home, SettingsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s: %w", SettingsFile, err)
	}
	return s, nil
}

// SaveSettings checks s and writes it.
func SaveSettings(home string, s Settings) error {
	if err := s.Normalize(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(home, SettingsFile), append(data, '\n'))
}

// writeFile replaces path at once, readable by the user only.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
