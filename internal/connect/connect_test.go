package connect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setup(t *testing.T) (home, claudeDir, codexDir string) {
	t.Helper()
	root := t.TempDir()
	home, claudeDir, codexDir = filepath.Join(root, "st"), filepath.Join(root, "claude"), filepath.Join(root, "codex")
	for _, d := range []string{home, claudeDir, codexDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexDir)
	return
}

const userSettings = `{
  "theme": "dark",
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "old-token",
    "ANTHROPIC_CUSTOM_HEADERS": "X-Team: a",
    "FOO": "bar"
  },
  "modelPicker": {
    "options": [
      {"model": "claude-opus-5-5", "label": "Mine"}
    ]
  }
}
`

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v\n%s", p, err, data)
	}
	return m
}

func TestClaudeSubscriptionAndOff(t *testing.T) {
	home, dir, _ := setup(t)
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	o := Options{BaseURL: "http://127.0.0.1:9787", Key: "sk-st-k1", Models: []string{"gpt-5.6-sol", "deepseek-v4"}, Subscription: true}
	a, err := Claude(home, o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(a.Backup); string(b) != userSettings {
		t.Errorf("backup %q", b)
	}
	m := readJSON(t, p)
	env := m["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:9787" || env["ANTHROPIC_AUTH_TOKEN"] != nil || env["FOO"] != "bar" ||
		env["ANTHROPIC_CUSTOM_HEADERS"] != "X-Team: a\nX-Sharetoken-Key: sk-st-k1" || m["theme"] != "dark" {
		t.Errorf("env %v", m)
	}
	opts := m["modelPicker"].(map[string]any)["options"].([]any)
	if len(opts) != 3 || opts[1].(map[string]any)["model"] != "st-gpt-5.6-sol" {
		t.Errorf("picker %v", opts)
	}
	// Order kept: theme first.
	if data, _ := os.ReadFile(p); !strings.HasPrefix(strings.TrimSpace(string(data)), "{\n  \"theme\"") {
		t.Errorf("order lost:\n%s", data)
	}
	// Again with a new key and models: replaced, not added.
	o.Key, o.Models = "sk-st-k2", []string{"gpt-5.6-sol"}
	if err := Refresh(home, o.BaseURL, o.Key, o.Models); err != nil {
		t.Fatal(err)
	}
	m = readJSON(t, p)
	if h := m["env"].(map[string]any)["ANTHROPIC_CUSTOM_HEADERS"]; h != "X-Team: a\nX-Sharetoken-Key: sk-st-k2" {
		t.Errorf("headers after refresh %v", h)
	}
	if opts := m["modelPicker"].(map[string]any)["options"].([]any); len(opts) != 2 {
		t.Errorf("picker after refresh %v", opts)
	}
	// Switching to key mode and back off restores everything.
	o.Subscription = false
	if _, err := Claude(home, o); err != nil {
		t.Fatal(err)
	}
	m = readJSON(t, p)
	if m["env"].(map[string]any)["ANTHROPIC_AUTH_TOKEN"] != "sk-st-k2" || m["enableArtifact"] != false {
		t.Errorf("key mode %v", m)
	}
	if err := ClaudeOff(home); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != userSettings {
		t.Errorf("not restored to the byte:\n%s", data)
	}
	if st, _ := LoadState(home); st.Claude != nil {
		t.Error("state kept")
	}
}

func TestClaudeRefusesOtherGateway(t *testing.T) {
	home, dir, _ := setup(t)
	p := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(p, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://gw.example.com"}}`), 0o600)
	if _, err := Claude(home, Options{BaseURL: "http://127.0.0.1:9787", Subscription: true}); err == nil {
		t.Error("took over another gateway")
	}
	if _, err := Claude(home, Options{BaseURL: "http://127.0.0.1:9787", Subscription: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeOff(home); err != nil {
		t.Fatal(err)
	}
	if m := readJSON(t, p); m["env"].(map[string]any)["ANTHROPIC_BASE_URL"] != "https://gw.example.com" {
		t.Errorf("not restored %v", m)
	}
}

func TestClaudeNoFile(t *testing.T) {
	home, dir, _ := setup(t)
	if _, err := Claude(home, Options{BaseURL: "http://127.0.0.1:9787", Key: "sk-st-k", Subscription: true, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeOff(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Errorf("the file it made is left: %v", err)
	}
}

const userToml = `# my config
model = "gpt-6.2-astra"
model_provider = "openai"

[projects."/x"]
trust_level = "trusted"
`

func TestCodexSubscriptionUnlockAndOff(t *testing.T) {
	home, _, dir := setup(t)
	p := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(p, []byte(userToml), 0o600)
	o := Options{BaseURL: "http://127.0.0.1:9787", Key: "sk-st-k1", Subscription: true, Unlock: true}
	a, err := Codex(home, o)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	top, _ := splitTop(s)
	for _, want := range []string{`model_provider = "sharetoken"`, `chatgpt_base_url = "http://127.0.0.1:9787/k/sk-st-k1/backend-api/"`, `model = "gpt-6.2-astra"`} {
		if !strings.Contains(top, want) {
			t.Errorf("top lacks %s:\n%s", want, s)
		}
	}
	for _, want := range []string{"[model_providers.sharetoken]", "requires_openai_auth = true", `http_headers = { "X-Sharetoken-Key" = "sk-st-k1" }`, `trust_level = "trusted"`} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %s:\n%s", want, s)
		}
	}
	if b, _ := os.ReadFile(a.Backup); string(b) != userToml {
		t.Error("backup")
	}
	// Again (refresh) changes nothing; then the openai-provider variant.
	if _, err := Codex(home, o); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(p); string(again) != s {
		t.Errorf("not idempotent:\n%s\n---\n%s", s, again)
	}
	o.OpenAIProvider, o.Unlock = true, false
	if _, err := Codex(home, o); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	s = string(data)
	if !strings.Contains(s, `openai_base_url = "http://127.0.0.1:9787/k/sk-st-k1/v1"`) || !strings.Contains(s, `model_provider = "openai"`) ||
		strings.Contains(s, "chatgpt_base_url") || strings.Contains(s, "[model_providers.sharetoken]") {
		t.Errorf("openai provider:\n%s", s)
	}
	if err := CodexOff(home); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != userToml {
		t.Errorf("not restored:\n%q\n%q", data, userToml)
	}
}

func TestCodexKeyMode(t *testing.T) {
	home, _, dir := setup(t)
	if _, err := Codex(home, Options{BaseURL: "http://127.0.0.1:9787", Key: "sk-st-k"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if !strings.Contains(string(data), `experimental_bearer_token = "sk-st-k"`) || strings.Contains(string(data), "requires_openai_auth") {
		t.Errorf("key mode:\n%s", data)
	}
	if err := CodexOff(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Errorf("the file it made is left: %v", err)
	}
}

// The tools' files hold the client's API key once connected: they are the
// user's alone from then on, whatever their mode was.
func TestFilesWithKeyAreUserOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no file modes")
	}
	home, claudeDir, codexDir := setup(t)
	settings, config := filepath.Join(claudeDir, "settings.json"), filepath.Join(codexDir, "config.toml")
	for p, data := range map[string]string{settings: userSettings, config: userToml} {
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A temporary file left by an earlier run does not keep its mode either.
	if err := os.WriteFile(config+".sharetoken-tmp", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	o := Options{BaseURL: "http://127.0.0.1:9787", Key: "sk-st-k1", Subscription: true}
	if _, err := Claude(home, o); err != nil {
		t.Fatal(err)
	}
	if _, err := Codex(home, o); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{settings, config} {
		data, _ := os.ReadFile(p)
		if !strings.Contains(string(data), "sk-st-k1") {
			t.Fatalf("%s does not hold the key: the test checks nothing", p)
		}
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want 0600", p, fi.Mode().Perm(), err)
		}
	}
	// A refresh with another key keeps them so.
	if err := Refresh(home, o.BaseURL, "sk-st-k2", nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{settings, config} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s after a refresh: %v", p, fi.Mode().Perm())
		}
	}
}
