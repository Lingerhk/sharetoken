package apicompat

// Codex client tools on function-only upstreams.
//
// A Responses client (Codex) declares tools an upstream speaking Messages or
// chat cannot express: custom (freeform) tools such as apply_patch, namespace
// groups of MCP functions, the client-executed tool_search, and local_shell.
// Each request lowers them to plain function tools from its own tools list
// (statelessly) and keeps a map from the upstream's function name back to the
// client's identity, so a call the upstream makes comes back to the client as
// the item type it executes: custom_tool_call with input, function_call with
// namespace, tool_search_call, local_shell_call. History items of those types
// are lowered the same way, so the model sees its earlier calls under the
// names it can call now.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// The kinds of client tool a lowered function stands for.
const (
	respKindFunction   = "function"
	respKindNamespace  = "namespace" // a function inside a namespace
	respKindCustom     = "custom"
	respKindToolSearch = "tool_search"
	respKindLocalShell = "local_shell"
)

// respToolSearchName and respLocalShellName are the function names the
// client-side tool_search and local_shell tools are lowered to.
const (
	respToolSearchName = "tool_search"
	respLocalShellName = "local_shell"
)

// respToolNameMax is the longest function name the upstream APIs accept.
const respToolNameMax = 64

const respCustomInputSchema = `{"type":"object","properties":{"input":{"type":"string","description":"The raw freeform input for this tool, passed through verbatim."}},"required":["input"]}`

const respToolSearchSchema = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for tools or connectors to load."},"limit":{"type":"integer","description":"Maximum number of tool groups to return."}},"required":["query"]}`

const respToolSearchDescription = "Search and load tools, plugins, connectors, and MCP namespaces for the current task."

const respLocalShellSchema = `{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"},"description":"The command and its arguments, for example [\"bash\", \"-lc\", \"ls -la\"]."},"workdir":{"type":"string","description":"Working directory for the command."},"timeout_ms":{"type":"integer","description":"Timeout in milliseconds."},"env":{"type":"object","additionalProperties":{"type":"string"},"description":"Extra environment variables."}},"required":["command"]}`

const respLocalShellDescription = "Runs a shell command on the user's machine and returns its output."

// respClientTool is the identity of a tool on the client's side.
type respClientTool struct {
	kind      string
	name      string // the function or custom tool name; the child name in a namespace
	namespace string
}

// respToolDecl is a function tool declared to the upstream.
type respToolDecl struct {
	name        string
	description string
	parameters  json.RawMessage // an object schema
	strict      *bool
	client      respClientTool
}

// respTools is a request's tool lowering: what the upstream is told, and how
// its calls map back.
type respTools struct {
	decls      []respToolDecl
	byUpstream map[string]respClientTool
	toUpstream map[respClientTool]string
	namespaces []string // declared namespace names, for alias resolution
}

// respToolWire is one entry of a Responses tools array.
type respToolWire struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
	Format      json.RawMessage `json:"format"`
	Execution   string          `json:"execution"`
	Tools       []respToolEntry `json:"tools"`
	Children    []respToolEntry `json:"children"`
}

// respToolEntry tolerates the string shorthand Codex uses for a custom tool.
type respToolEntry struct {
	respToolWire
	raw json.RawMessage
}

func (e *respToolEntry) UnmarshalJSON(b []byte) error {
	e.raw = append(json.RawMessage(nil), b...)
	var name string
	if json.Unmarshal(b, &name) == nil {
		e.respToolWire = respToolWire{Type: "custom", Name: name}
		return nil
	}
	return json.Unmarshal(b, &e.respToolWire)
}

// respBuildTools lowers the declared tools, the tools of additional_tools input
// items, and the tools a completed tool_search made available.
func respBuildTools(declared []respToolEntry, items []map[string]json.RawMessage) (*respTools, error) {
	all := append([]respToolEntry(nil), declared...)
	for _, item := range items {
		if respString(item["type"]) != "additional_tools" {
			continue
		}
		var extra []respToolEntry
		if err := json.Unmarshal(item["tools"], &extra); err != nil {
			return nil, fmt.Errorf("additional_tools: %w", err)
		}
		all = append(all, extra...)
	}
	hasSearch := false
	for _, t := range all {
		if t.Type == "tool_search" && t.Execution != "server" {
			hasSearch = true
		}
	}
	if hasSearch {
		// Tools a completed client tool search discovered are callable for
		// the rest of the conversation: declare them after the static ones.
		for _, item := range items {
			if respString(item["type"]) != "tool_search_output" {
				continue
			}
			if st, ok := item["status"]; ok && respString(st) != "completed" {
				continue
			}
			var found []respToolEntry
			if json.Unmarshal(item["tools"], &found) != nil {
				continue
			}
			all = append(all, found...)
		}
	}

	t := &respTools{byUpstream: map[string]respClientTool{}, toUpstream: map[respClientTool]string{}}
	for _, tool := range all {
		switch tool.Type {
		case "function":
			if tool.Name == "" {
				continue
			}
			if err := t.add(respToolDecl{
				name: tool.Name, description: tool.Description, parameters: tool.Parameters, strict: tool.Strict,
				client: respClientTool{kind: respKindFunction, name: tool.Name},
			}); err != nil {
				return nil, err
			}
		case "custom":
			if tool.Name == "" {
				continue
			}
			if err := t.add(respToolDecl{
				name: tool.Name, description: respCustomDescription(tool.Description, tool.Format),
				parameters: json.RawMessage(respCustomInputSchema),
				client:     respClientTool{kind: respKindCustom, name: tool.Name},
			}); err != nil {
				return nil, err
			}
		case "namespace":
			if tool.Name == "" {
				continue
			}
			children := tool.Tools
			if len(children) == 0 {
				children = tool.Children
			}
			known := false
			for _, ns := range t.namespaces {
				known = known || ns == tool.Name
			}
			if !known {
				t.namespaces = append(t.namespaces, tool.Name)
			}
			for _, child := range children {
				if child.Type != "function" || child.Name == "" {
					continue
				}
				desc := child.Description
				if tool.Description != "" && desc == "" {
					desc = tool.Description
				}
				if err := t.add(respToolDecl{
					name: respFlattenName(tool.Name, child.Name), description: desc,
					parameters: child.Parameters, strict: child.Strict,
					client: respClientTool{kind: respKindNamespace, name: child.Name, namespace: tool.Name},
				}); err != nil {
					return nil, err
				}
			}
		case "tool_search":
			if tool.Execution == "server" {
				continue // hosted; the node removes those
			}
			if err := t.add(respToolDecl{
				name: respToolSearchName, description: respToolSearchDescription,
				parameters: json.RawMessage(respToolSearchSchema),
				client:     respClientTool{kind: respKindToolSearch, name: respToolSearchName},
			}); err != nil {
				return nil, err
			}
		case "local_shell":
			if err := t.add(respToolDecl{
				name: respLocalShellName, description: respLocalShellDescription,
				parameters: json.RawMessage(respLocalShellSchema),
				client:     respClientTool{kind: respKindLocalShell, name: respLocalShellName},
			}); err != nil {
				return nil, err
			}
		}
		// Hosted tools (web_search, file_search, code_interpreter, image
		// generation, computer use, mcp, ...) have no function form: dropped.
	}
	return t, nil
}

// add declares a tool under a name valid for the upstream. The same tool
// declared twice is kept once; two different tools that end up under one
// upstream name cannot be told apart in the upstream's calls, so the request
// is refused.
func (t *respTools) add(d respToolDecl) error {
	d.name = respSafeToolName(d.name)
	d.parameters = respObjectSchema(d.parameters)
	if prev, ok := t.byUpstream[d.name]; ok {
		if prev != d.client {
			return fmt.Errorf("tools %s and %s both become the function %q for this upstream; rename one of them",
				respDescribeTool(prev), respDescribeTool(d.client), d.name)
		}
		return nil // a repeated declaration (for example rediscovered by tool_search): keep the first
	}
	t.byUpstream[d.name] = d.client
	t.toUpstream[d.client] = d.name
	t.decls = append(t.decls, d)
	return nil
}

func respDescribeTool(c respClientTool) string {
	switch c.kind {
	case respKindNamespace:
		return fmt.Sprintf("%q in namespace %q", c.name, c.namespace)
	case respKindCustom:
		return fmt.Sprintf("custom tool %q", c.name)
	case respKindToolSearch:
		return "tool_search"
	case respKindLocalShell:
		return "local_shell"
	}
	return fmt.Sprintf("function %q", c.name)
}

// respCustomDescription tells the model how the freeform input travels, and
// keeps the grammar the client declared, which a function schema cannot carry.
func respCustomDescription(desc string, format json.RawMessage) string {
	var f struct {
		Type       string `json:"type"`
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	_ = json.Unmarshal(format, &f)
	var b strings.Builder
	b.WriteString(strings.TrimSpace(desc))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("Pass the tool's raw freeform input, exactly as it should be received, as the string argument `input`.")
	if f.Type == "grammar" && strings.TrimSpace(f.Definition) != "" {
		syntax := f.Syntax
		if syntax == "" {
			syntax = "grammar"
		}
		fmt.Fprintf(&b, " The input must match this %s grammar:\n%s", syntax, f.Definition)
	}
	return b.String()
}

// upstreamName is the function name the upstream knows a client tool by. A
// tool the request no longer declares (only met in history) keeps a name
// derived the same way.
func (t *respTools) upstreamName(c respClientTool) string {
	if n, ok := t.toUpstream[c]; ok {
		return n
	}
	switch c.kind {
	case respKindNamespace:
		return respSafeToolName(respFlattenName(c.namespace, c.name))
	case respKindToolSearch:
		return respToolSearchName
	case respKindLocalShell:
		return respLocalShellName
	}
	return respSafeToolName(c.name)
}

// resolve maps a function name the upstream called back to the client's tool.
func (t *respTools) resolve(name string) respClientTool {
	if c, ok := t.byUpstream[name]; ok {
		return c
	}
	// Models that see flattened namespace functions sometimes call a custom
	// tool under the same prefix (functions__exec beside functions__wait).
	match := ""
	for upstream, c := range t.byUpstream {
		if c.kind != respKindCustom {
			continue
		}
		for _, ns := range t.namespaces {
			if respSafeToolName(respFlattenName(ns, c.name)) == name {
				if match != "" && match != upstream {
					return respClientTool{kind: respKindFunction, name: name}
				}
				match = upstream
			}
		}
	}
	if match != "" {
		return t.byUpstream[match]
	}
	return respClientTool{kind: respKindFunction, name: name}
}

// respFlattenName is the function name of a namespace child; a long one is cut
// and made unique with a short hash.
func respFlattenName(namespace, name string) string {
	return respCapName(namespace + "__" + name)
}

func respCapName(full string) string {
	if len(full) <= respToolNameMax {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "__" + hex.EncodeToString(sum[:4])
	return full[:respToolNameMax-len(suffix)] + suffix
}

// respSafeToolName makes a name match ^[a-zA-Z0-9_-]{1,64}$, which both
// upstream APIs require. A name that needed changes gets a hash of the
// original, so two different names never collapse into one.
func respSafeToolName(name string) string {
	ok := name != "" && len(name) <= respToolNameMax
	for _, r := range name {
		if !respNameRune(r) {
			ok = false
			break
		}
	}
	if ok {
		return name
	}
	var b strings.Builder
	for _, r := range name {
		if respNameRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:4])
	base := b.String()
	if len(base) > respToolNameMax-len(suffix) {
		base = base[:respToolNameMax-len(suffix)]
	}
	return base + suffix
}

func respNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}

// respObjectSchema makes a tool's parameters an object schema: both upstream
// APIs reject any other root. A root-level union of object branches (which
// Codex declares for some tools) is flattened into one object.
func respObjectSchema(schema json.RawMessage) json.RawMessage {
	const empty = `{"type":"object","properties":{}}`
	var m map[string]json.RawMessage
	if json.Unmarshal(schema, &m) != nil || m == nil {
		return json.RawMessage(empty)
	}
	respFlattenRootUnions(m)
	if raw, ok := m["type"]; !ok || string(raw) == "null" {
		m["type"] = json.RawMessage(`"object"`)
	} else if respString(raw) != "object" {
		return json.RawMessage(empty)
	}
	if _, ok := m["properties"]; !ok {
		m["properties"] = json.RawMessage(`{}`)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(empty)
	}
	return out
}

// respFlattenRootUnions replaces a root oneOf/anyOf/allOf by one object
// schema: the properties of every object branch (a property declared
// differently by two branches becomes an anyOf of both), required fields all
// branches of oneOf/anyOf share, and every required field of allOf. Branches
// that are not objects add nothing.
func respFlattenRootUnions(schema map[string]json.RawMessage) {
	props := map[string]json.RawMessage{}
	_ = json.Unmarshal(schema["properties"], &props)
	if props == nil {
		props = map[string]json.RawMessage{}
	}
	required := respRequired(schema)
	found := false
	for _, kw := range []string{"oneOf", "anyOf", "allOf"} {
		raw, ok := schema[kw]
		if !ok {
			continue
		}
		found = true
		delete(schema, kw)
		var rawBranches []json.RawMessage
		if json.Unmarshal(raw, &rawBranches) != nil || len(rawBranches) == 0 {
			continue
		}
		var shared []string
		for i, rb := range rawBranches {
			var branch map[string]json.RawMessage
			if json.Unmarshal(rb, &branch) != nil || branch == nil {
				branch = nil
			} else {
				respFlattenRootUnions(branch)
			}
			isObject := false
			if branch != nil {
				if tr, ok := branch["type"]; ok {
					isObject = respString(tr) == "object"
				} else {
					_, isObject = branch["properties"]
				}
			}
			var req []string
			if isObject {
				var bp map[string]json.RawMessage
				_ = json.Unmarshal(branch["properties"], &bp)
				for name, p := range bp {
					if prev, ok := props[name]; ok && !respJSONEqual(prev, p) {
						merged, _ := json.Marshal(map[string][]json.RawMessage{"anyOf": {prev, p}})
						props[name] = merged
					} else if !ok {
						props[name] = p
					}
				}
				req = respRequired(branch)
			}
			if kw == "allOf" {
				required = respUnion(required, req)
				continue
			}
			if i == 0 {
				shared = req
			} else {
				shared = respIntersect(shared, req)
			}
		}
		required = respUnion(required, shared)
	}
	if !found {
		return
	}
	if len(props) > 0 {
		schema["properties"], _ = json.Marshal(props)
	}
	if len(required) > 0 {
		schema["required"], _ = json.Marshal(required)
	}
}

func respRequired(m map[string]json.RawMessage) []string {
	var r []string
	_ = json.Unmarshal(m["required"], &r)
	return r
}

func respUnion(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, s := range b {
		if !respContains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func respIntersect(a, b []string) []string {
	var out []string
	for _, s := range a {
		if respContains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func respContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func respJSONEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

// respCustomInput recovers a custom tool's freeform input from the arguments
// of the function it was lowered to. Arguments that do not follow the schema
// are passed on as they are, for the client to reject and the model to retry.
func respCustomInput(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &obj) != nil {
		return trimmed
	}
	if raw, ok := obj["input"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return trimmed
	}
	if len(obj) == 0 {
		return ""
	}
	return trimmed
}

// respCustomArguments is the inverse of respCustomInput.
func respCustomArguments(input string) string {
	b, _ := json.Marshal(map[string]string{"input": input})
	return string(b)
}

// respArgumentsObject is a tool_search_call's arguments as the JSON object the
// item carries on the wire.
func respArgumentsObject(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	b, _ := json.Marshal(arguments)
	return b
}

// respLocalShellAction turns the lowered local_shell arguments into the action
// of a local_shell_call item.
func respLocalShellAction(arguments string) map[string]any {
	var a struct {
		Command   any               `json:"command"`
		Workdir   string            `json:"workdir"`
		TimeoutMS *int64            `json:"timeout_ms"`
		Env       map[string]string `json:"env"`
	}
	_ = json.Unmarshal([]byte(arguments), &a)
	command := []string{}
	switch c := a.Command.(type) {
	case []any:
		for _, v := range c {
			if s, ok := v.(string); ok {
				command = append(command, s)
			}
		}
	case string:
		if c != "" {
			command = []string{"bash", "-lc", c}
		}
	}
	action := map[string]any{"type": "exec", "command": command}
	if a.Workdir != "" {
		action["working_directory"] = a.Workdir
	}
	if a.TimeoutMS != nil {
		action["timeout_ms"] = *a.TimeoutMS
	}
	if len(a.Env) > 0 {
		action["env"] = a.Env
	}
	return action
}

// respLocalShellArguments lowers a local_shell_call's action for history.
func respLocalShellArguments(action json.RawMessage) string {
	var a struct {
		Command    []string          `json:"command"`
		WorkingDir string            `json:"working_directory"`
		TimeoutMS  *int64            `json:"timeout_ms"`
		Env        map[string]string `json:"env"`
	}
	_ = json.Unmarshal(action, &a)
	out := map[string]any{"command": a.Command}
	if a.Command == nil {
		out["command"] = []string{}
	}
	if a.WorkingDir != "" {
		out["workdir"] = a.WorkingDir
	}
	if a.TimeoutMS != nil {
		out["timeout_ms"] = *a.TimeoutMS
	}
	if len(a.Env) > 0 {
		out["env"] = a.Env
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// respString reads a JSON string; anything else is "".
func respString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
