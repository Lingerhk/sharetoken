package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// replayer stands in for the model servers sharetoken has no account at in
// its tests, the ChatGPT Codex backend and Google's Code Assist backend, by
// replaying their streams (testdata/replay). It answers like a model that
// reads a file: offered tools, it calls a shell or file-reading tool; given
// the tool's output, it answers with it, so a client that ran the tool sees
// its own output come back.
type replayer struct {
	t        testing.TB
	fixtures map[string]string
	workdir  string // where the file to read is, for tools that want a full path

	mu   sync.Mutex
	seen map[string][]map[string]any
}

func newReplayer(t testing.TB, workdir string) (*replayer, *httptest.Server) {
	r := &replayer{t: t, fixtures: map[string]string{}, workdir: workdir, seen: map[string][]map[string]any{}}
	files, _ := filepath.Glob("testdata/replay/*.sse")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r.fixtures[strings.TrimSuffix(filepath.Base(f), ".sse")] = string(b)
	}
	if len(r.fixtures) < 4 {
		t.Fatalf("replay fixtures missing (run from test/e2e): %v", files)
	}
	srv := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *replayer) requests(path string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.seen[path]...)
}

func (r *replayer) serve(w http.ResponseWriter, req *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.seen[req.URL.Path] = append(r.seen[req.URL.Path], body)
	r.mu.Unlock()
	var (
		kind, model    string
		tools          []string
		output         string
		answered, asks bool
	)
	switch {
	case req.URL.Path == "/codex/responses":
		if req.Header.Get("chatgpt-account-id") == "" || req.Header.Get("originator") == "" {
			http.Error(w, "not a Codex request", http.StatusUnauthorized)
			return
		}
		kind, model = "codex", fmt.Sprint(body["model"])
		for _, t := range list(body["tools"]) {
			if m, _ := t.(map[string]any); m["type"] == "function" {
				tools = append(tools, fmt.Sprint(m["name"]))
			}
		}
		input := list(body["input"])
		if n := len(input); n > 0 {
			last, _ := input[n-1].(map[string]any)
			if typ := fmt.Sprint(last["type"]); strings.HasSuffix(typ, "_output") {
				answered, output = true, flatText(last["output"])
			}
		}
	case strings.HasPrefix(req.URL.Path, "/gemini/v1internal:streamGenerateContent"):
		inner, _ := body["request"].(map[string]any)
		if body["project"] == nil || inner == nil {
			http.Error(w, "not a Code Assist request", http.StatusBadRequest)
			return
		}
		kind, model = "gemini", fmt.Sprint(body["model"])
		for _, t := range list(inner["tools"]) {
			m, _ := t.(map[string]any)
			for _, d := range list(m["functionDeclarations"]) {
				dm, _ := d.(map[string]any)
				tools = append(tools, fmt.Sprint(dm["name"]))
			}
		}
		contents := list(inner["contents"])
		if n := len(contents); n > 0 {
			last, _ := contents[n-1].(map[string]any)
			for _, p := range list(last["parts"]) {
				if fr, ok := p.(map[string]any)["functionResponse"].(map[string]any); ok {
					answered, output = true, flatText(fr["response"])
				}
			}
		}
	default:
		http.NotFound(w, req)
		return
	}
	raw, _ := json.Marshal(body)
	// A compaction: Codex's own prompt, or the one sharetoken sends for
	// /responses/compact.
	asks = strings.Contains(string(raw), "CONTEXT CHECKPOINT COMPACTION") || strings.Contains(string(raw), "summary of the conversation so far")
	tool, args := r.pickTool(tools)
	fixture := kind + "_text"
	if tool != "" && !answered && !asks {
		fixture = kind + "_tool"
	}
	text := "pong"
	switch {
	case asks:
		text = "<summary>The user asked to read a file.</summary>"
	case answered:
		text = "The file says: " + cleanOutput(output)
	}
	argsJSON, _ := json.Marshal(args)
	id := randomID()
	half := len(text) / 2
	repl := strings.NewReplacer(
		"{{ID}}", id, "{{NOW}}", fmt.Sprint(time.Now().Unix()), "{{MODEL}}", model,
		"{{TOOL}}", tool, "{{CALL}}", "call_"+id,
		"{{ARGS_OBJECT}}", string(argsJSON),
		"{{ARGS}}", jsonInner(string(argsJSON)),
		"{{ARGS_A}}", jsonInner(string(argsJSON[:len(argsJSON)/2])), "{{ARGS_B}}", jsonInner(string(argsJSON[len(argsJSON)/2:])),
		"{{TEXT}}", jsonInner(text), "{{TEXT_A}}", jsonInner(text[:half]), "{{TEXT_B}}", jsonInner(text[half:]),
	)
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	for _, block := range strings.Split(repl.Replace(r.fixtures[fixture]), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		fmt.Fprint(w, block+"\n\n")
		fl.Flush()
		time.Sleep(5 * time.Millisecond)
	}
}

// pickTool chooses the tool a model would use to read a file, and its
// arguments in that tool's schema.
func (r *replayer) pickTool(tools []string) (string, map[string]any) {
	path := "a.txt"
	if r.workdir != "" {
		path = filepath.Join(r.workdir, "a.txt")
	}
	for _, want := range []string{"lookup", "exec_command", "shell_command", "Bash", "shell", "Read", "read_file"} {
		for _, have := range tools {
			if have != want {
				continue
			}
			switch want {
			case "lookup":
				return have, map[string]any{"q": "answer"}
			case "exec_command":
				return have, map[string]any{"cmd": "cat a.txt"}
			case "shell_command":
				return have, map[string]any{"command": "cat a.txt"}
			case "Bash":
				return have, map[string]any{"command": "cat a.txt", "description": "Print a.txt"}
			case "shell":
				return have, map[string]any{"command": []string{"bash", "-lc", "cat a.txt"}}
			default:
				return have, map[string]any{"file_path": path}
			}
		}
	}
	return "", nil
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// flatText is the text of a tool's output: a string, parts, or an object.
func flatText(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				b.WriteString(fmt.Sprint(m["text"]))
			}
		}
		return b.String()
	}
	if m, ok := v.(map[string]any); ok {
		// Gemini's functionResponse.response: {"result": …} or {"output": …}.
		for _, k := range []string{"result", "output", "content"} {
			if x, ok := m[k]; ok {
				return flatText(x)
			}
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// cleanOutput keeps a tool output's last meaningful line (a shell prints
// framing around a command's output; Read numbers the lines).
func cleanOutput(s string) string {
	var keep string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '\t'); i > 0 && strings.Trim(line[:i], "0123456789 →") == "" {
			line = strings.TrimSpace(line[i+1:])
		}
		if line != "" && !strings.HasPrefix(line, "Chunk ID") && !strings.HasPrefix(line, "Wall time") && !strings.HasPrefix(line, "Process exited") &&
			!strings.HasPrefix(line, "Original token") && !strings.HasPrefix(line, "Output:") && !strings.HasPrefix(line, "Exit code") {
			keep = line
		}
	}
	if len(keep) > 200 {
		keep = keep[:200]
	}
	return keep
}

// jsonInner is s as the inside of a JSON string.
func jsonInner(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
