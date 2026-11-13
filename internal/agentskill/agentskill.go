// Package agentskill installs the client's skill (SKILL.md) for coding agents on
// this machine: Claude Code and Codex. Both read the Agent Skills format: a
// directory named after the skill that holds SKILL.md with name and description
// in its front matter.
//
//	Claude Code: $CLAUDE_CONFIG_DIR/skills, else ~/.claude/skills
//	Codex:       $CODEX_HOME/skills, else ~/.codex/skills
//
// The same paths hold on Windows, under %USERPROFILE%.
package agentskill

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Name is the skill's name and the name of its directory.
const Name = "sharetoken"

//go:embed SKILL.md
var template string

// Params fill in the placeholders of SKILL.md for the client that installs it.
type Params struct {
	Command string // how to run the client, e.g. "sharetoken" or its full path
	Env     string // build type: dev or prod
	Portal  string
	Home    string // state directory
	HTTP    string // local API address, host:port
	OS      string // runtime.GOOS of the machine; "windows" adds a note on the shell
}

const windowsNote = `
This machine runs Windows. The examples use POSIX shell syntax; in PowerShell, call ` + "`curl.exe`" + ` (not the ` + "`curl`" + ` alias) with the JSON body in a file (` + "`-d \"@body.json\"`" + `), and pass a provider key with ` + "`$env:SHARETOKEN_PROVIDER_KEY = …`" + ` or by piping it: ` + "`$key | sharetoken provider add deepseek`" + `.
`

// Render returns SKILL.md for p.
func Render(p Params) string {
	return strings.NewReplacer(
		"@CMD@", p.Command,
		"@ENV@", p.Env,
		"@PORTAL@", p.Portal,
		"@HOME@", p.Home,
		"@HTTP@", p.HTTP,
		"@PLATFORM@", platformNote(p.OS),
	).Replace(template)
}

func platformNote(goos string) string {
	if goos == "windows" {
		return windowsNote
	}
	return ""
}

// Agent is a coding agent that reads skills from a directory.
type Agent struct {
	ID   string // claude, codex
	Name string
	Dir  string // the agent's configuration directory, e.g. ~/.claude
}

// SkillsDir is where the agent looks for user-level skills.
func (a Agent) SkillsDir() string { return filepath.Join(a.Dir, "skills") }

// Path is where the agent's copy of this skill goes.
func (a Agent) Path() string { return filepath.Join(a.SkillsDir(), Name, "SKILL.md") }

// Present reports whether the agent's configuration directory exists, that is
// whether the agent has been used on this machine.
func (a Agent) Present() bool {
	st, err := os.Stat(a.Dir)
	return err == nil && st.IsDir()
}

// Agents lists the agents this package knows, with their directories for the
// current user. getenv and home are os.Getenv and os.UserHomeDir in use; tests
// pass their own.
func Agents(getenv func(string) string, home string) []Agent {
	dir := func(env, sub string) string {
		if d := getenv(env); d != "" {
			return d
		}
		return filepath.Join(home, sub)
	}
	return []Agent{
		{ID: "claude", Name: "Claude Code", Dir: dir("CLAUDE_CONFIG_DIR", ".claude")},
		{ID: "codex", Name: "Codex", Dir: dir("CODEX_HOME", ".codex")},
	}
}

// DefaultAgents is Agents for the current user.
func DefaultAgents() ([]Agent, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot find the home directory: %w", err)
	}
	return Agents(os.Getenv, home), nil
}

// Select picks agents by ID ("claude", "codex", "all"; a comma-separated list
// is allowed). An empty selection means every agent that is present; it is an
// error when none is.
func Select(agents []Agent, which string) ([]Agent, error) {
	which = strings.ToLower(strings.TrimSpace(which))
	if which == "" {
		var out []Agent
		for _, a := range agents {
			if a.Present() {
				out = append(out, a)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("found neither %s: name one with -agent (%s)", names(agents), ids(agents))
		}
		return out, nil
	}
	if which == "all" {
		return agents, nil
	}
	var out []Agent
	for id := range strings.SplitSeq(which, ",") {
		id = strings.TrimSpace(id)
		found := false
		for _, a := range agents {
			if a.ID == id {
				if !contains(out, id) {
					out = append(out, a)
				}
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown agent %q: %s or all", id, ids(agents))
		}
	}
	return out, nil
}

// Result says what Install or Remove did for one agent.
type Result struct {
	Agent   Agent
	Path    string
	Changed bool // false: the file was already up to date, or not there to remove
}

// Install writes content as the agent's copy of the skill, creating the skills
// directory if needed. A file that differs from content is replaced only with
// force, unless it is a copy this package wrote (it holds marker): reinstalling after an update just works, while a skill of the same
// name that someone else wrote is kept.
func Install(a Agent, content string, force bool) (Result, error) {
	path := a.Path()
	res := Result{Agent: a, Path: path}
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if string(old) == content {
			return res, nil
		}
		if !force && !ours(string(old)) {
			return res, fmt.Errorf("%s exists and was not written by sharetoken: -force replaces it", path)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return res, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return res, err
	}
	res.Changed = true
	return res, nil
}

// Remove deletes the agent's copy of the skill and its directory, if it is one
// this package wrote (or with force).
func Remove(a Agent, force bool) (Result, error) {
	path := a.Path()
	res := Result{Agent: a, Path: path}
	old, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if !force && !ours(string(old)) {
		return res, fmt.Errorf("%s was not written by sharetoken: -force removes it", path)
	}
	if err := os.Remove(path); err != nil {
		return res, err
	}
	os.Remove(filepath.Dir(path)) // only when empty: keeps anything the user added
	res.Changed = true
	return res, nil
}

// Installed reports whether the agent has a copy of the skill, and whether it
// equals content.
func Installed(a Agent, content string) (present, current bool) {
	old, err := os.ReadFile(a.Path())
	if err != nil {
		return false, false
	}
	return true, string(old) == content
}

// marker is in every copy this package writes.
const marker = "<!-- Installed by `sharetoken skill install`"

// ours reports whether a SKILL.md was written by this package.
func ours(s string) bool { return strings.Contains(s, marker) }

func contains(as []Agent, id string) bool {
	for _, a := range as {
		if a.ID == id {
			return true
		}
	}
	return false
}

func ids(as []Agent) string {
	var s []string
	for _, a := range as {
		s = append(s, a.ID)
	}
	return strings.Join(s, ", ")
}

func names(as []Agent) string {
	var s []string
	for _, a := range as {
		s = append(s, fmt.Sprintf("%s (%s)", a.Name, a.Dir))
	}
	return strings.Join(s, " nor ")
}
