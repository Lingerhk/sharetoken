package apicompat

import "regexp"

// Anthropic's Messages API refuses a request whose history has tool_use or
// tool_result blocks and that declares no tools ("Requests which include
// `tool_use` or `tool_result` blocks must define tools"). OpenAI's APIs take
// one: an agent that offers no tools this turn, or one that loads its tools
// as it needs them, sends a history of calls beside none. A conversion to
// Messages then declares a stub for each tool the history called, and sets
// tool_choice none, as the client offered nothing to call (after magpie's
// undeclared_tools.go).

// stubToolDescription is a stub's description: the call it stands for was
// made earlier and the tool is not offered now.
const stubToolDescription = "Called earlier in this conversation; not available now."

// anthropicToolName is a name Anthropic takes for a tool.
var anthropicToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// calledTools collects the names of the tools a Messages history calls, in
// order, each once.
type calledTools struct {
	names []string
	seen  map[string]bool
}

// add notes the tool a content block calls, if it is a tool_use block.
func (c *calledTools) add(block map[string]any) {
	if block["type"] != "tool_use" {
		return
	}
	name, _ := block["name"].(string)
	if c.seen[name] || !anthropicToolName.MatchString(name) {
		return
	}
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[name] = true
	c.names = append(c.names, name)
}

// declare gives req, a Messages request with no tools, a stub for each tool
// its history called, and tool_choice none.
func (c *calledTools) declare(req map[string]any) {
	if len(c.names) == 0 {
		return
	}
	if tools, _ := req["tools"].([]map[string]any); len(tools) > 0 {
		return
	}
	if tools, _ := req["tools"].([]any); len(tools) > 0 {
		return
	}
	tools := make([]any, 0, len(c.names))
	for _, n := range c.names {
		tools = append(tools, map[string]any{"name": n, "description": stubToolDescription,
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}}})
	}
	req["tools"] = tools
	req["tool_choice"] = map[string]any{"type": "none"}
}
