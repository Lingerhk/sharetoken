package warmup

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The program set for an agent is run as the user: only an absolute path to
// an executable file named as the agent is taken, when it is set and when it
// is used.
func TestSetBinaryIsTheAgentsProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("paths and modes of Unix")
	}
	dir := t.TempDir()
	file := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := file("bin/claude", 0o755)
	if p, err := findBinary(AgentClaude, good); err != nil || p != good {
		t.Fatalf("findBinary(%s) = %q, %v", good, p, err)
	}
	t.Chdir(filepath.Join(dir, "bin"))
	for name, set := range map[string]string{
		"another program":   file("bin/evil", 0o755),
		"another agent's":   file("bin/codex", 0o755),
		"a system program":  "/bin/sh",
		"a relative path":   "claude",
		"a relative path 2": "./claude",
		"not executable":    file("plain/claude", 0o644),
		"a directory":       filepath.Dir(file("claude/x", 0o755)),
		"missing":           filepath.Join(dir, "none", "claude"),
	} {
		if p, err := findBinary(AgentClaude, set); err == nil {
			t.Errorf("%s: findBinary(%q) = %q", name, set, p)
		}
	}
	// Saving refuses what could never be used, and says so at once.
	home := t.TempDir()
	for _, set := range []string{"/bin/sh", "claude", "./claude", filepath.Join(dir, "bin", "codex")} {
		if err := SaveSettings(home, Settings{Claude: Local{Binary: set}}); err == nil {
			t.Errorf("saved claude.binary = %q", set)
		}
	}
	if err := SaveSettings(home, Settings{Claude: Local{Binary: good}, Codex: Local{Binary: filepath.Join(dir, "bin", "codex")}}); err != nil {
		t.Errorf("the agents' own programs: %v", err)
	}
	if err := SaveSettings(home, Settings{}); err != nil { // "" is found by itself
		t.Error(err)
	}
}

// The warm-up's routes take JSON only as JSON: a web form can post
// JSON-looking text, but not that content type.
func TestRoutesWantJSONContentType(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	s.find = func(name, set string) (string, error) { return "", os.ErrNotExist }
	mux := http.NewServeMux()
	s.Routes(mux)
	call := func(method, path, ct, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		if code := call("PUT", "/api/v1/warmup", ct, `{"paused":true}`); code != http.StatusBadRequest {
			t.Errorf("PUT as %q: %d", ct, code)
		}
		if code := call("POST", "/api/v1/warmup/run", ct, `{"target":"local:claude"}`); code != http.StatusBadRequest {
			t.Errorf("run as %q: %d", ct, code)
		}
	}
	if set, _ := LoadSettings(home); set.Paused {
		t.Fatal("a form changed the settings")
	}
	if code := call("PUT", "/api/v1/warmup", "application/json", `{"paused":true}`); code != http.StatusOK {
		t.Errorf("JSON: %d", code)
	}
	if set, _ := LoadSettings(home); !set.Paused {
		t.Error("JSON did not change the settings")
	}
}
