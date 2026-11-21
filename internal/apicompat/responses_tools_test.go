package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func respTestTools(t *testing.T, tools string, input string) (*respTools, error) {
	t.Helper()
	var entries []respToolEntry
	if err := json.Unmarshal([]byte(tools), &entries); err != nil {
		t.Fatal(err)
	}
	var items []map[string]json.RawMessage
	if input != "" {
		if err := json.Unmarshal([]byte(input), &items); err != nil {
			t.Fatal(err)
		}
	}
	return respBuildTools(entries, items)
}

func respDeclNames(tt *respTools) string {
	var names []string
	for _, d := range tt.decls {
		names = append(names, d.name)
	}
	return strings.Join(names, ",")
}

func TestResponsesToolLowering(t *testing.T) {
	tools, err := respTestTools(t, `[
		{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
		"exec",
		{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: x"}},
		{"type":"namespace","name":"team","tools":[{"type":"function","name":"send"},{"type":"custom","name":"skipped"}]},
		{"type":"namespace","name":"alt","children":[{"type":"function","name":"recv"}]},
		{"type":"tool_search","execution":"client"},
		{"type":"tool_search","execution":"client"},
		{"type":"local_shell"},
		{"type":"web_search"},{"type":"file_search"},{"type":"image_generation"},{"type":"code_interpreter"},{"type":"computer_use_preview"},{"type":"mcp"},
		{"type":"tool_search","execution":"server"}
	]`, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := respDeclNames(tools); got != "exec_command,exec,apply_patch,team__send,alt__recv,tool_search,local_shell" {
		t.Fatalf("declarations: %s", got)
	}
	for name, want := range map[string]respClientTool{
		"exec":         {kind: respKindCustom, name: "exec"},
		"apply_patch":  {kind: respKindCustom, name: "apply_patch"},
		"team__send":   {kind: respKindNamespace, name: "send", namespace: "team"},
		"tool_search":  {kind: respKindToolSearch, name: "tool_search"},
		"local_shell":  {kind: respKindLocalShell, name: "local_shell"},
		"exec_command": {kind: respKindFunction, name: "exec_command"},
		"undeclared":   {kind: respKindFunction, name: "undeclared"},
		"team__exec":   {kind: respKindCustom, name: "exec"}, // a model's namespace-style alias of a custom tool
		"team__other":  {kind: respKindFunction, name: "team__other"},
	} {
		if got := tools.resolve(name); got != want {
			t.Errorf("resolve(%s) = %+v, want %+v", name, got, want)
		}
	}
	for _, d := range tools.decls {
		if d.name == "apply_patch" && (!respJSONEqual(d.parameters, json.RawMessage(respCustomInputSchema)) || !strings.Contains(d.description, "start: x")) {
			t.Errorf("custom declaration: %+v", d)
		}
	}
}

func TestResponsesToolConflicts(t *testing.T) {
	for name, tools := range map[string]string{
		"custom and function":       `[{"type":"custom","name":"same"},{"type":"function","name":"same"}]`,
		"tool_search and function":  `[{"type":"tool_search"},{"type":"function","name":"tool_search"}]`,
		"flattened namespace":       `[{"type":"function","name":"team__send"},{"type":"namespace","name":"team","tools":[{"type":"function","name":"send"}]}]`,
		"two namespaces, same flat": `[{"type":"namespace","name":"a__b","tools":[{"type":"function","name":"c"}]},{"type":"namespace","name":"a","tools":[{"type":"function","name":"b__c"}]}]`,
		"local_shell and function":  `[{"type":"local_shell"},{"type":"function","name":"local_shell"}]`,
	} {
		if _, err := respTestTools(t, tools, ""); err == nil {
			t.Errorf("%s: want a conflict error", name)
		}
	}
	// the same declaration twice is not a conflict
	tools, err := respTestTools(t, `[{"type":"function","name":"f"},{"type":"function","name":"f"},
		{"type":"namespace","name":"n","tools":[{"type":"function","name":"g"}]},{"type":"namespace","name":"n","tools":[{"type":"function","name":"g"}]}]`, "")
	if err != nil || respDeclNames(tools) != "f,n__g" {
		t.Errorf("dedupe: %v %v", err, tools)
	}
}

func TestResponsesToolSearchDiscoveries(t *testing.T) {
	input := `[
		{"type":"tool_search_call","call_id":"s1","execution":"client","arguments":{"query":"x"}},
		{"type":"tool_search_output","call_id":"s1","status":"completed","execution":"client","tools":[
			{"type":"namespace","name":"multi_agent","tools":[{"type":"function","name":"spawn","parameters":{"type":"object"}}]},
			{"type":"function","name":"inspect"},
			{"type":"custom","name":"run_script"},
			{"type":"function","name":"static_first"}
		]},
		{"type":"tool_search_output","call_id":"s2","status":"in_progress","tools":[{"type":"function","name":"not_ready"}]},
		{"type":"additional_tools","tools":[{"type":"function","name":"extra"}]}
	]`
	tools, err := respTestTools(t, `[{"type":"function","name":"static_first"},{"type":"tool_search"}]`, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := respDeclNames(tools); got != "static_first,tool_search,extra,multi_agent__spawn,inspect,run_script" {
		t.Errorf("declarations: %s", got)
	}
	if tools.resolve("run_script").kind != respKindCustom {
		t.Errorf("discovered custom tool")
	}
	// without a tool_search declaration, discoveries are not promoted
	tools, err = respTestTools(t, `[{"type":"function","name":"static_first"}]`, input)
	if err != nil || respDeclNames(tools) != "static_first,extra" {
		t.Errorf("no tool_search: %v %s", err, respDeclNames(tools))
	}
}

func TestResponsesToolNames(t *testing.T) {
	long := strings.Repeat("n", 40)
	flat := respFlattenName(long, strings.Repeat("c", 40))
	if len(flat) != respToolNameMax || !strings.HasPrefix(flat, long+"__") {
		t.Errorf("flattened %q (%d)", flat, len(flat))
	}
	if respFlattenName("team", "send") != "team__send" {
		t.Errorf("short names are joined as they are")
	}
	tests := []struct{ in, prefix string }{
		{"ok_name-1", "ok_name-1"},
		{"mcp.server.tool", "mcp_server_tool_"},
		{"名字", "___"},
		{strings.Repeat("x", 70), strings.Repeat("x", 55)},
	}
	for _, tt := range tests {
		got := respSafeToolName(tt.in)
		if !strings.HasPrefix(got, tt.prefix) || len(got) > respToolNameMax {
			t.Errorf("respSafeToolName(%q) = %q", tt.in, got)
		}
		for _, r := range got {
			if !respNameRune(r) {
				t.Errorf("%q has %q", got, r)
			}
		}
	}
	if respSafeToolName("a.b") == respSafeToolName("a_b") {
		t.Errorf("a changed name must not collide with a valid one")
	}
	// a dotted name round-trips through the mapping
	tools, err := respTestTools(t, `[{"type":"function","name":"mcp.fs.read"}]`, "")
	if err != nil {
		t.Fatal(err)
	}
	up := tools.decls[0].name
	if up == "mcp.fs.read" || tools.resolve(up) != (respClientTool{kind: respKindFunction, name: "mcp.fs.read"}) {
		t.Errorf("upstream name %q resolves to %+v", up, tools.resolve(up))
	}
}

// Ported from sub2api's root-union schema tests.
func TestResponsesObjectSchema(t *testing.T) {
	tests := []struct {
		name, in string
		props    []string // property names that must be present
		required string   // JSON of required, "" for none
		check    func(t *testing.T, s map[string]any)
	}{
		{"nil", ``, nil, "", nil},
		{"not an object", `{"type":"string"}`, nil, "", nil},
		{"missing type", `{"properties":{"a":{"type":"string"}}}`, []string{"a"}, "", nil},
		{"object without properties", `{"type":"object"}`, nil, "", nil},
		{"object branches flattened", `{"oneOf":[{"type":"object","properties":{"id":{"type":"string"}}},{"anyOf":[{"type":"object"},{"type":"object","properties":{}}]}]}`,
			[]string{"id"}, "", nil},
		{"branches merge, required intersect", `{"anyOf":[
			{"type":"object","properties":{"mode":{"enum":["view"]},"id":{"type":"string"}},"required":["mode","id"]},
			{"type":"object","properties":{"mode":{"enum":["update"]},"prompt":{"type":"string"}},"required":["mode","prompt"]}]}`,
			[]string{"id", "prompt", "mode"}, `["mode"]`, func(t *testing.T, s map[string]any) {
				if respJSON(t, s["properties"].(map[string]any)["mode"]) != `{"anyOf":[{"enum":["view"]},{"enum":["update"]}]}` {
					t.Errorf("mode: %v", s["properties"])
				}
			}},
		{"allOf required union", `{"allOf":[{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},{"type":"object","properties":{"encoding":{"type":"string"}},"required":["encoding"]}]}`,
			[]string{"path", "encoding"}, `["path","encoding"]`, nil},
		{"non-object branch dropped", `{"oneOf":[{"type":"object","properties":{"path":{"type":"string"}}},{"type":"string"}]}`,
			[]string{"path"}, "", nil},
		{"root properties kept", `{"type":"object","properties":{"shared":{"type":"string"}},"required":["shared"],"oneOf":[{"type":"object","properties":{"extra":{"type":"integer"}}}]}`,
			[]string{"shared", "extra"}, `["shared"]`, nil},
		{"same-property branches kept as identical", `{"oneOf":[{"type":"object","properties":{"mode":{"enum":["view"]},"id":{"type":"string"}},"required":["mode","id"]},{"type":"object","properties":{"mode":{"enum":["update"]},"id":{"type":"string"}},"required":["mode","id"]}]}`,
			[]string{"mode", "id"}, `["mode","id"]`, func(t *testing.T, s map[string]any) {
				if respJSON(t, s["properties"].(map[string]any)["id"]) != `{"type":"string"}` {
					t.Errorf("id: %v", s["properties"])
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s map[string]any
			if err := json.Unmarshal(respObjectSchema(json.RawMessage(tt.in)), &s); err != nil {
				t.Fatal(err)
			}
			if s["type"] != "object" || s["oneOf"] != nil || s["anyOf"] != nil || s["allOf"] != nil {
				t.Fatalf("schema %v", s)
			}
			props, ok := s["properties"].(map[string]any)
			if !ok {
				t.Fatalf("no properties: %v", s)
			}
			for _, p := range tt.props {
				if props[p] == nil {
					t.Errorf("property %s missing: %v", p, props)
				}
			}
			got := ""
			if s["required"] != nil {
				got = respJSON(t, s["required"])
			}
			if got != tt.required {
				t.Errorf("required %s, want %s", got, tt.required)
			}
			if tt.check != nil {
				tt.check(t, s)
			}
		})
	}
	nested := `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}}}`
	if !respJSONEqual(respObjectSchema(json.RawMessage(nested)), json.RawMessage(nested)) {
		t.Errorf("a nested union is left alone")
	}
}

func TestResponsesCustomInput(t *testing.T) {
	tests := []struct{ args, want string }{
		{`{"input":"dir"}`, "dir"},
		{`{"input": "*** Begin Patch\n"}`, "*** Begin Patch\n"},
		{``, ""},
		{`{}`, ""},
		{`not json`, "not json"},
		{`{"input":42}`, `{"input":42}`},
		{`{"cmd":"x"}`, `{"cmd":"x"}`},
	}
	for _, tt := range tests {
		if got := respCustomInput(tt.args); got != tt.want {
			t.Errorf("respCustomInput(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
	if respCustomInput(respCustomArguments("a\"b\n")) != "a\"b\n" {
		t.Errorf("round trip")
	}
}

func TestResponsesLocalShellAction(t *testing.T) {
	a := respLocalShellAction(`{"command":["ls","-la"],"workdir":"/w","timeout_ms":100,"env":{"A":"1"}}`)
	if respJSON(t, a) != `{"command":["ls","-la"],"env":{"A":"1"},"timeout_ms":100,"type":"exec","working_directory":"/w"}` {
		t.Errorf("action %v", a)
	}
	if respJSON(t, respLocalShellAction(`{"command":"echo hi"}`)["command"]) != `["bash","-lc","echo hi"]` {
		t.Errorf("a string command runs in a shell")
	}
	if respJSON(t, respLocalShellAction(`garbage`)["command"]) != `[]` {
		t.Errorf("bad arguments give an empty command")
	}
	args := respLocalShellArguments(json.RawMessage(`{"type":"exec","command":["ls"],"working_directory":"/w","timeout_ms":5}`))
	if args != `{"command":["ls"],"timeout_ms":5,"workdir":"/w"}` {
		t.Errorf("arguments %s", args)
	}
}
