package warmup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The agents installed on this machine, warmed up through their own CLI:
// the request is the CLI's own, as the user signed it in, and nothing of
// sharetoken's network is involved.

// Agent IDs.
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

const (
	sendTimeout = 2 * time.Minute
	readTimeout = time.Minute
)

// runner runs a command and is what it printed (stdout and stderr apart);
// tests swap it.
type runner func(ctx context.Context, c command) (stdout, stderr []byte, err error)

type command struct {
	path  string
	args  []string
	stdin string
	dir   string
	env   []string
}

func runCommand(ctx context.Context, c command) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, c.path, c.args...)
	cmd.Dir = c.dir
	cmd.Env = c.env
	cmd.Stdin = strings.NewReader(c.stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	detach(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// findBinary is the agent's program: the one set, else the one on the
// PATH, else the usual places an installer puts it (a desktop app starts
// with a short PATH). A wrapper of a terminal app (a "shims" directory)
// is taken only when nothing else is found.
func findBinary(name, set string) (string, error) {
	if set != "" {
		if isFile(set) {
			return set, nil
		}
		return "", fmt.Errorf("%s not found at %s", name, set)
	}
	var shim string
	if p, err := exec.LookPath(name); err == nil {
		if !isShim(p) {
			return p, nil
		}
		shim = p
	}
	for _, dir := range searchDirs(name) {
		for _, n := range exeNames(name) {
			p := filepath.Join(dir, n)
			if isFile(p) && !isShim(p) {
				return p, nil
			}
		}
	}
	if shim != "" {
		return shim, nil
	}
	return "", fmt.Errorf("%s is not installed (or not found): set its path in the warm-up settings", name)
}

func isShim(p string) bool {
	p = filepath.ToSlash(strings.ToLower(p))
	return strings.Contains(p, "/shims/") || strings.Contains(p, "-shims/")
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func exeNames(name string) []string {
	if runtime.GOOS == "windows" {
		return []string{name + ".exe", name + ".cmd", name + ".bat"}
	}
	return []string{name}
}

// searchDirs are where installers put the agents' programs.
func searchDirs(name string) []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if name == AgentClaude {
		dirs = append(dirs, filepath.Join(home, ".claude", "local"))
	}
	dirs = append(dirs,
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, ".cargo", "bin"))
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("APPDATA"); v != "" {
			dirs = append(dirs, filepath.Join(v, "npm"))
		}
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			dirs = append(dirs, filepath.Join(v, "Programs", name), filepath.Join(v, "Microsoft", "WinGet", "Links"))
		}
		for _, k := range []string{"ProgramFiles", "ProgramW6432"} {
			if v := os.Getenv(k); v != "" {
				dirs = append(dirs, filepath.Join(v, "nodejs")) // an npm install's .cmd runs node
			}
		}
	case "darwin":
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	default:
		dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/snap/bin")
	}
	return dirs
}

// agentEnv is the environment the agent runs in: the user's, without what
// would send its request elsewhere than the subscription it is signed in
// to (an API key, another base URL: sharetoken's own endpoint among them),
// and with a PATH that finds the program's interpreter (an npm install is
// a node script).
func agentEnv(bin string) []string {
	drop := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT",
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY"}
	var env []string
	path := ""
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "PATH") {
			path = v
			continue
		}
		skip := false
		for _, d := range drop {
			if strings.EqualFold(k, d) {
				skip = true
				break
			}
		}
		if !skip {
			env = append(env, kv)
		}
	}
	// A program started without a login environment has no USER: Claude
	// Code finds its sign-in in the macOS keychain under it.
	if u, err := user.Current(); err == nil && u.Username != "" {
		for _, k := range []string{"USER", "LOGNAME"} {
			if os.Getenv(k) == "" {
				env = append(env, k+"="+u.Username)
			}
		}
	}
	parts := []string{filepath.Dir(bin)}
	if path != "" {
		parts = append(parts, path)
	}
	parts = append(parts, searchDirs(filepath.Base(bin))...)
	return append(env, "PATH="+strings.Join(parts, string(os.PathListSeparator)))
}

// inTemp runs fn in a new empty directory, removed after: the agent finds
// no project there, and leaves nothing in one.
func inTemp(fn func(dir string) error) error {
	dir, err := os.MkdirTemp("", "sharetoken-warmup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return fn(dir)
}

// claudeSendArgs ask Claude Code one "hi" (on stdin) with no tools, no MCP
// servers, none of the user's settings or hooks, and no session kept.
func claudeSendArgs(model string) []string {
	if model == "" {
		model = "haiku"
	}
	return []string{"-p", "--output-format", "json", "--model", model,
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--no-session-persistence"}
}

// codexSendArgs ask Codex one "hi" through the ChatGPT sign-in (the
// built-in openai provider, whatever config.toml picks), read-only, with
// no session kept.
func codexSendArgs(model string) []string {
	args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only",
		"-c", "model_provider=\"openai\"", "-c", "model_reasoning_effort=\"low\""}
	if model != "" {
		args = append(args, "-m", model)
	}
	return append(args, "hi")
}

// sendLocal has the agent answer one "hi".
func sendLocal(ctx context.Context, run runner, agent, bin, model string) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return inTemp(func(dir string) error {
		c := command{path: bin, dir: dir, env: agentEnv(bin)}
		if agent == AgentClaude {
			c.args, c.stdin = claudeSendArgs(model), "hi"
		} else {
			c.args = codexSendArgs(model)
		}
		out, errOut, err := run(ctx, c)
		if agent == AgentClaude {
			if res, ok := claudeResult(out); ok && res.IsError {
				return errors.New("Claude Code: " + clip(res.Result))
			} else if ok && err == nil {
				return nil
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%s did not answer within %s", agent, sendTimeout)
			}
			msg := lastLines(errOut)
			if msg == "" {
				msg = lastLines(out)
			}
			if msg == "" {
				return err
			}
			return fmt.Errorf("%s: %s", agent, clip(msg))
		}
		return nil
	})
}

type claudeOutput struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// claudeResult is the result line of Claude Code's JSON output.
func claudeResult(out []byte) (claudeOutput, bool) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var r claudeOutput
		if json.Unmarshal([]byte(l), &r) == nil && (r.Type == "result" || r.Result != "") {
			return r, true
		}
	}
	return claudeOutput{}, false
}

// lastLines are the last few non-empty lines of out.
func lastLines(out []byte) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > 3 {
		keep = keep[len(keep)-3:]
	}
	return strings.Join(keep, " ")
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

// version is what the agent's --version prints, its first line.
func version(ctx context.Context, run runner, bin string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var v string
	_ = inTemp(func(dir string) error {
		out, _, err := run(ctx, command{path: bin, args: []string{"--version"}, dir: dir, env: agentEnv(bin)})
		if err == nil {
			v, _, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
		}
		return nil
	})
	return strings.TrimSpace(v)
}
