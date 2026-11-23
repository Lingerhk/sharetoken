// Package connect points Claude Code and Codex at the client's local
// endpoint by editing their own configuration files, keeping a backup of
// each file it changes and the values it replaced, so that Off puts them
// back.
//
// With Subscription the tool stays signed in to its vendor (claude.ai or
// ChatGPT) and gets the network's models next to its plan's: the local
// endpoint routes each request by its model (internal/official), and the
// client's API key travels in X-Sharetoken-Key. Without it the tool uses
// the network only, with the API key as its credential.
package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StateFile records, in the client's home, what was changed.
const StateFile = "connect.json"

// BackupDir holds, in the client's home, copies of the files as they were
// before each change.
const BackupDir = "backups"

// Options say how a tool is connected.
type Options struct {
	// BaseURL is the local endpoint without /v1 (http://127.0.0.1:9787).
	BaseURL string `json:"-"`
	// Key is the client's API key ("" when none is required).
	Key string `json:"-"`
	// Models are the network's models, bare names (Claude Code's menu).
	Models []string `json:"-"`

	// Subscription keeps the tool's own vendor login.
	Subscription bool `json:"subscription"`
	// OpenAIProvider (Codex): Codex's built-in OpenAI provider points here
	// (openai_base_url), so its earlier conversations stay listed.
	OpenAIProvider bool `json:"openai_provider,omitempty"`
	// Unlock (Codex): the ChatGPT backend's requests come here as well
	// (chatgpt_base_url), for the Codex Desktop unlock.
	Unlock bool `json:"unlock,omitempty"`
	// Force takes over a base URL that points elsewhere.
	Force bool `json:"-"`
}

// Applied is one tool's connection.
type Applied struct {
	Path   string `json:"path"`
	Backup string `json:"backup,omitempty"`
	// Created: there was no file before the first change; -off removes
	// what is left of it when that is nothing.
	Created bool      `json:"created,omitempty"`
	Options Options   `json:"options"`
	At      time.Time `json:"at"`
	// Original holds what the managed settings were before the first
	// change: a JSON value, or absent for none (Claude Code), or the whole
	// line, or "" for none (Codex).
	Original map[string]*json.RawMessage `json:"original,omitempty"`
	Lines    map[string]string           `json:"lines,omitempty"`
}

// State is StateFile's content.
type State struct {
	Claude *Applied `json:"claude,omitempty"`
	Codex  *Applied `json:"codex,omitempty"`
}

// LoadState reads home's StateFile.
func LoadState(home string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(home, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

func saveState(home string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(home, StateFile), append(data, '\n'), 0o600)
}

// backup copies path into home's BackupDir; "" when there is no file.
func backup(home, path, name string) (string, error) {
	src, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer src.Close()
	dir := filepath.Join(home, BackupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405.000")
	var dst string
	var out *os.File
	for i := 0; ; i++ {
		dst = filepath.Join(dir, fmt.Sprintf("%s.%s", name, stamp))
		if i > 0 {
			dst += fmt.Sprintf("-%d", i)
		}
		out, err = os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 100 {
			return "", err
		}
	}
	pruneBackups(dir, name, dst)
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return "", err
	}
	return dst, out.Close()
}

// keepBackups is how many backups of a file are kept; the first one, the
// file as it was before the first change, is kept as well.
const keepBackups = 20

func pruneBackups(dir, name, newest string) {
	all, _ := filepath.Glob(filepath.Join(dir, name+".*"))
	if len(all) <= keepBackups+1 {
		return
	}
	sort.Strings(all)
	for _, p := range all[1 : len(all)-keepBackups] {
		if p != newest {
			_ = os.Remove(p)
		}
	}
}

// removeIfEmpty removes a file the client created once nothing of the
// user's is in it; true when it did.
func removeIfEmpty(path string, data []byte) bool {
	s := strings.TrimSpace(string(data))
	if s != "" && s != "{}" {
		return false
	}
	return os.Remove(path) == nil
}

// writeAtomic replaces path with data, keeping its mode when it exists.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".sharetoken-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// localURL reports whether u points at this machine.
func localURL(u string) bool {
	for _, p := range []string{"http://127.0.0.1", "http://localhost", "http://[::1]", "https://localhost", "https://127.0.0.1"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// Refresh applies the saved connections again with the endpoint's current
// address, key and models; a file is written only when it changes. The
// daemon calls it as these change (another port, a rotated key, new
// models).
func Refresh(home, baseURL, key string, models []string) error {
	s, err := LoadState(home)
	if err != nil {
		return err
	}
	var errs []error
	if a := s.Claude; a != nil {
		o := a.Options
		o.BaseURL, o.Key, o.Models, o.Force = baseURL, key, models, true
		if models == nil {
			o.Models = a.listed()
		}
		if _, err := Claude(home, o); err != nil {
			errs = append(errs, fmt.Errorf("Claude Code: %w", err))
		}
	}
	if a := s.Codex; a != nil {
		o := a.Options
		o.BaseURL, o.Key, o.Force = baseURL, key, true
		if _, err := Codex(home, o); err != nil {
			errs = append(errs, fmt.Errorf("Codex: %w", err))
		}
	}
	return errors.Join(errs...)
}
