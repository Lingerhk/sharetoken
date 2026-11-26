package connect

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CodexConfigPath is Codex's config.toml.
func CodexConfigPath() string {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, ".codex")
	}
	return filepath.Join(dir, "config.toml")
}

// codexProvider is the provider table the client writes.
const codexProvider = "sharetoken"

var tableHeader = regexp.MustCompile(`^\s*\[`)

// Codex connects Codex (its config.toml):
//
//   - a [model_providers.sharetoken] table for the local endpoint, chosen by
//     model_provider; with Subscription it keeps the ChatGPT login
//     (requires_openai_auth) and sends the API key in X-Sharetoken-Key,
//     without it the API key is its bearer token;
//   - with OpenAIProvider, openai_base_url instead: Codex's built-in OpenAI
//     provider comes here (its earlier conversations stay listed), with the
//     API key in the path (it sends no custom headers);
//   - with Unlock, chatgpt_base_url: Codex Desktop's ChatGPT requests come
//     here too (official.ServeBackend), with the API key in the path: its
//     MCP requests are lent Codex's login only when they show the key.
//
// Codex reads the top-level keys only before the first table, so they go
// at the top of the file.
func Codex(home string, o Options) (*Applied, error) {
	path := CodexConfigPath()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	st, err := LoadState(home)
	if err != nil {
		return nil, err
	}
	a := st.Codex
	if a == nil || a.Path != path {
		a = &Applied{Path: path, Created: data == nil}
	}
	if a.Lines == nil {
		a.Lines = map[string]string{}
	}
	text := codexRestore(string(data), a)
	if o.OpenAIProvider {
		text = setTop(text, a, "openai_base_url", o.BaseURL+keyPath(o.Key)+"/v1")
	} else {
		text = setTop(text, a, "model_provider", codexProvider)
		text = strings.TrimRight(text, "\n") + "\n\n" + providerTable(o)
	}
	if o.Unlock {
		text = setTop(text, a, "chatgpt_base_url", o.BaseURL+keyPath(o.Key)+"/backend-api/")
	}
	a.Options, a.At = o, time.Now()
	a.Options.Models = nil
	if text != string(data) {
		b, err := backup(home, path, "codex-config.toml")
		if err != nil {
			return nil, fmt.Errorf("backing up %s: %w", path, err)
		}
		if a.Backup == "" {
			a.Backup = b
		}
		if err := writeAtomic(path, []byte(text), 0o600); err != nil {
			return nil, err
		}
	}
	st.Codex = a
	return a, saveState(home, st)
}

// CodexOff puts Codex's config back as it was.
func CodexOff(home string) error {
	st, err := LoadState(home)
	if err != nil || st.Codex == nil {
		return err
	}
	a := st.Codex
	data, err := os.ReadFile(a.Path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := codexRestore(string(data), a)
	if a.Created && removeIfEmpty(a.Path, []byte(text)) {
		st.Codex = nil
		return saveState(home, st)
	}
	if text != string(data) {
		if _, err := backup(home, a.Path, "codex-config.toml"); err != nil {
			return err
		}
		if err := writeAtomic(a.Path, []byte(text), 0o600); err != nil {
			return err
		}
	}
	st.Codex = nil
	return saveState(home, st)
}

func keyPath(key string) string {
	if key == "" {
		return ""
	}
	return "/k/" + key
}

func providerTable(o Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[model_providers.%s]\n", codexProvider)
	b.WriteString("name = \"ShareToken\"\n")
	fmt.Fprintf(&b, "base_url = %s\n", strconv.Quote(o.BaseURL+"/v1"))
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString("supports_websockets = false\n")
	if o.Subscription {
		b.WriteString("requires_openai_auth = true\n")
		if o.Key != "" {
			fmt.Fprintf(&b, "http_headers = { %s = %s }\n", strconv.Quote(keyHeaderName), strconv.Quote(o.Key))
		}
	} else {
		key := o.Key
		if key == "" {
			key = "sharetoken"
		}
		fmt.Fprintf(&b, "experimental_bearer_token = %s\n", strconv.Quote(key))
	}
	return b.String()
}

// splitTop splits a TOML document before its first table.
func splitTop(text string) (top, rest string) {
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		if tableHeader.MatchString(l) {
			return strings.Join(lines[:i], ""), strings.Join(lines[i:], "")
		}
	}
	return text, ""
}

func keyLine(key string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
}

// setTop sets a top-level key, keeping the line it replaces (or none) in
// a.Lines the first time.
func setTop(text string, a *Applied, key, value string) string {
	top, rest := splitTop(text)
	re := keyLine(key)
	line := key + " = " + strconv.Quote(value)
	lines := strings.Split(top, "\n")
	for i, l := range lines {
		if re.MatchString(l) {
			if _, ok := a.Lines[key]; !ok {
				a.Lines[key] = l
			}
			lines[i] = line
			return strings.Join(lines, "\n") + rest
		}
	}
	if _, ok := a.Lines[key]; !ok {
		a.Lines[key] = ""
	}
	return line + "\n" + top + rest
}

// codexRestore takes out what the client wrote: its provider table, and
// the top-level keys it set, back to their old lines.
func codexRestore(text string, a *Applied) string {
	text = removeTable(text, "model_providers."+codexProvider)
	top, rest := splitTop(text)
	lines := strings.Split(top, "\n")
	for key, orig := range a.Lines {
		if key == "models" {
			continue
		}
		re := keyLine(key)
		for i := 0; i < len(lines); i++ {
			if !re.MatchString(lines[i]) {
				continue
			}
			if orig == "" {
				lines = append(lines[:i], lines[i+1:]...)
			} else {
				lines[i] = orig
			}
			break
		}
	}
	return strings.Join(lines, "\n") + rest
}

// removeTable removes a table and its subtables.
func removeTable(text, name string) string {
	lines := strings.SplitAfter(text, "\n")
	var out []string
	skip := false
	for _, l := range lines {
		if tableHeader.MatchString(l) {
			h := strings.TrimSpace(l)
			h = strings.Trim(h, "[]")
			h = strings.TrimSpace(h)
			skip = h == name || strings.HasPrefix(h, name+".")
			for skip && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
				out = out[:len(out)-1] // the blank line written before it
			}
		}
		if !skip {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}
