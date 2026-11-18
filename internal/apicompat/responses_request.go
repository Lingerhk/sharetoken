package apicompat

// The Responses request, read into a neutral form both request converters
// (to Messages and to chat) build from: the conversation as messages,
// reasoning, tool calls and tool outputs, with client tools already lowered
// to functions (responses_tools.go) and tool calls paired with their outputs.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The kinds of respItem.
const (
	respItemMessage   = "message"
	respItemReasoning = "reasoning"
	respItemCall      = "call"
	respItemOutput    = "output"
)

// respPart is a piece of message or tool-output content.
type respPart struct {
	kind     string // "text", "image" or "file"
	text     string
	url      string // image: a data: or http(s) URL
	detail   string
	fileData string // file: a data: URL
	fileURL  string
	fileName string
	fileID   string
}

// respItem is one item of the conversation.
type respItem struct {
	kind  string
	role  string // message: user, assistant or system (developer is system)
	parts []respPart

	summary   string // reasoning: the visible summary text
	encrypted string // reasoning: encrypted_content

	callID string // call and output
	name   string // call: the upstream function name
	args   string // call: JSON arguments

	outText  string     // output: the text form
	outParts []respPart // output: text and media, when the output carried media
	outMedia bool
}

// respRequest is the part of a Responses request the converters use.
type respRequest struct {
	model        string
	instructions string
	items        []respItem
	tools        *respTools
	toolChoice   json.RawMessage
	parallel     *bool
	maxOutput    int
	temperature  *float64
	topP         *float64
	effort       string
	textFormat   json.RawMessage
}

type respRequestWire struct {
	Model             string          `json:"model"`
	Instructions      json.RawMessage `json:"instructions"`
	Input             json.RawMessage `json:"input"`
	Tools             []respToolEntry `json:"tools"`
	ToolChoice        json.RawMessage `json:"tool_choice"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls"`
	MaxOutputTokens   *int            `json:"max_output_tokens"`
	Temperature       *float64        `json:"temperature"`
	TopP              *float64        `json:"top_p"`
	Reasoning         *struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
	Text *struct {
		Format json.RawMessage `json:"format"`
	} `json:"text"`
}

// respParseRequest reads a Responses request body.
func respParseRequest(body []byte) (*respRequest, error) {
	var w respRequestWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("apicompat: parse responses request: %w", err)
	}
	r := &respRequest{
		model: w.Model, toolChoice: w.ToolChoice, parallel: w.ParallelToolCalls,
		temperature: w.Temperature, topP: w.TopP,
	}
	if w.MaxOutputTokens != nil {
		r.maxOutput = *w.MaxOutputTokens
	}
	if w.Reasoning != nil {
		r.effort = strings.ToLower(strings.TrimSpace(w.Reasoning.Effort))
	}
	if w.Text != nil {
		r.textFormat = w.Text.Format
	}

	var rawItems []map[string]json.RawMessage
	input := strings.TrimSpace(string(w.Input))
	switch {
	case input == "" || input == "null":
	case input[0] == '"':
		rawItems = []map[string]json.RawMessage{{
			"type": json.RawMessage(`"message"`), "role": json.RawMessage(`"user"`), "content": w.Input,
		}}
	default:
		var list []json.RawMessage
		if err := json.Unmarshal(w.Input, &list); err != nil {
			return nil, fmt.Errorf("apicompat: parse responses input: %w", err)
		}
		for _, raw := range list {
			var m map[string]json.RawMessage
			if json.Unmarshal(raw, &m) == nil && m != nil {
				rawItems = append(rawItems, m)
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) == nil {
				rawItems = append(rawItems, map[string]json.RawMessage{
					"type": json.RawMessage(`"message"`), "role": json.RawMessage(`"user"`), "content": raw,
				})
			}
		}
	}

	// instructions is a string; an array of input items is accepted too.
	if s := respString(w.Instructions); s != "" {
		r.instructions = s
	} else if len(w.Instructions) > 0 && strings.TrimSpace(string(w.Instructions))[0] == '[' {
		var list []map[string]json.RawMessage
		if json.Unmarshal(w.Instructions, &list) == nil {
			var texts []string
			for _, m := range list {
				for _, p := range respParseContent(m["content"]) {
					if p.kind == "text" && p.text != "" {
						texts = append(texts, p.text)
					}
				}
			}
			r.instructions = strings.Join(texts, "\n\n")
		}
	}

	tools, err := respBuildTools(w.Tools, rawItems)
	if err != nil {
		return nil, fmt.Errorf("apicompat: %w", err)
	}
	r.tools = tools
	r.items = respPairCalls(respLowerItems(rawItems, tools))
	return r, nil
}

// respLowerItems turns input items into neutral items.
func respLowerItems(raw []map[string]json.RawMessage, tools *respTools) []respItem {
	var items []respItem
	skipped := map[string]bool{} // calls left out, whose outputs go too
	for _, m := range raw {
		typ := respString(m["type"])
		switch typ {
		case "", "message":
			role := strings.ToLower(respString(m["role"]))
			switch role {
			case "developer", "system":
				role = "system"
			case "assistant":
			default:
				role = "user"
			}
			content := m["content"]
			if len(content) == 0 {
				content = m["text"]
			}
			parts := respParseContent(content)
			if len(parts) > 0 {
				items = append(items, respItem{kind: respItemMessage, role: role, parts: parts})
			}
		case "input_text", "text", "input_image", "input_file":
			// a bare content part is a user turn
			if p, ok := respParsePart(m); ok {
				items = append(items, respItem{kind: respItemMessage, role: "user", parts: []respPart{p}})
			}
		case "reasoning":
			items = append(items, respItem{
				kind: respItemReasoning, summary: respReasoningText(m), encrypted: respString(m["encrypted_content"]),
			})
		case "compaction", "compaction_summary":
			// Only a compaction item sharetoken made can be read; one made by
			// the real Codex backend is opaque to every other upstream.
			if summary, ok := DecodeCompaction(respString(m["encrypted_content"])); ok {
				items = append(items, respItem{kind: respItemMessage, role: "user",
					parts: []respPart{{kind: "text", text: SummaryMessage(summary)}}})
			}
		case "function_call", "custom_tool_call", "tool_search_call", "local_shell_call":
			callID := respString(m["call_id"])
			if callID == "" {
				callID = respString(m["id"])
			}
			var name, args string
			switch typ {
			case "function_call":
				c := respClientTool{kind: respKindFunction, name: respString(m["name"])}
				if ns := respString(m["namespace"]); ns != "" {
					c = respClientTool{kind: respKindNamespace, name: c.name, namespace: ns}
				}
				name = tools.upstreamName(c)
				args = respString(m["arguments"])
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				if !json.Valid([]byte(args)) {
					// A call cut short in an earlier turn: the upstream would
					// refuse the whole request, so drop it and its output.
					skipped[callID] = true
					continue
				}
			case "custom_tool_call":
				name = tools.upstreamName(respClientTool{kind: respKindCustom, name: respString(m["name"])})
				args = respCustomArguments(respString(m["input"]))
			case "tool_search_call":
				name = respToolSearchName
				args = respString(m["arguments"])
				if args == "" {
					args = strings.TrimSpace(string(m["arguments"]))
				}
				if args == "" || args == "null" || !json.Valid([]byte(args)) {
					args = "{}"
				}
			case "local_shell_call":
				name = respLocalShellName
				args = respLocalShellArguments(m["action"])
			}
			if callID == "" || name == "" {
				continue
			}
			items = append(items, respItem{kind: respItemCall, callID: callID, name: name, args: args})
		case "function_call_output", "custom_tool_call_output", "tool_search_output",
			"local_shell_call_output", "mcp_tool_call_output":
			callID := respString(m["call_id"])
			if text, ok := ExternalContext(typ, respString(m["namespace"]), callID, respString(m["name"]), respString(m["output"])); ok {
				items = append(items, respItem{kind: respItemMessage, role: "user", parts: []respPart{{kind: "text", text: text}}})
				continue
			}
			if callID == "" || skipped[callID] {
				continue
			}
			out := m["output"]
			if typ == "tool_search_output" && (len(out) == 0 || string(out) == "null") {
				out = m["tools"] // newer clients return the discovered tools here
			}
			it := respItem{kind: respItemOutput, callID: callID}
			it.outText, it.outParts, it.outMedia = respParseOutput(out)
			items = append(items, it)
		case "agent_message":
			// Codex multi-agent messages: the text and plaintext payload, in order.
			if text := respAgentMessageText(m["content"]); text != "" {
				items = append(items, respItem{kind: respItemMessage, role: "user",
					parts: []respPart{{kind: "text", text: text}}})
			}
		case "additional_tools", "item_reference":
			// tools are lifted into the declarations; references need server state
		default:
			// An unknown item keeps what it has of plain content, as a user
			// turn; one without content (web_search_call, ...) is left out.
			parts := respParseContent(m["content"])
			if len(parts) > 0 {
				items = append(items, respItem{kind: respItemMessage, role: "user", parts: parts})
			}
		}
	}
	return items
}

// Codex hands the model context from outside the conversation (the page a
// browser shows, the goal it works on, text it does not trust) as a
// function_call_output of namespace "agent" with no call_id: an output no
// call asked for. The Responses API over HTTP refuses such an output, and
// the other APIs have nowhere to put it, so it goes as what older Codex
// versions sent: a user message with the text in <external_NAME> tags.
var codexSelectedPage = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

// ExternalContext returns the user message text an input item of type typ
// stands for when it is such external context, and false when it is not
// (an ordinary tool output, or one whose content is not what Codex writes
// for that name).
func ExternalContext(typ, namespace, callID, name, output string) (string, bool) {
	if typ != "function_call_output" || namespace != "agent" || strings.TrimSpace(callID) != "" {
		return "", false
	}
	switch name {
	case "browser_info", "goal_objective", "untrusted_context":
		if strings.TrimSpace(output) == "" {
			return "", false
		}
		return "<external_" + name + ">" + output + "</external_" + name + ">", true
	}
	if !externalPage(name, output) {
		return "", false
	}
	return "<external_" + name + ">\n" + output + "\n</external_" + name + ">", true
}

// externalPage tells whether output is a page snapshot Codex sends under
// name: a JSON object with a page_id, whole or in numbered parts.
func externalPage(name, output string) bool {
	if name == "codex_apps_open_page" {
		return externalSnapshot(output)
	}
	m := codexSelectedPage.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	part, err1 := strconv.Atoi(m[1])
	total, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || part > total {
		return false
	}
	header, snapshot, ok := strings.Cut(output, "\n")
	if !ok || strings.TrimSuffix(header, "\r") != fmt.Sprintf("Companion Page snapshot part %d of %d:", part, total) || strings.TrimSpace(snapshot) == "" {
		return false
	}
	// A snapshot in several parts may be cut inside a JSON string: each part
	// goes as it is.
	return total > 1 || externalSnapshot(snapshot)
}

func externalSnapshot(text string) bool {
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &snapshot) != nil {
		return false
	}
	id, ok := snapshot["page_id"]
	if !ok {
		return false
	}
	if strings.TrimSpace(string(id)) == "null" {
		return true
	}
	var s string
	return json.Unmarshal(id, &s) == nil && strings.TrimSpace(s) != ""
}

// respParseContent reads message content: a string or an array of parts.
func respParseContent(raw json.RawMessage) []respPart {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		s := respString(raw)
		if s == "" {
			return nil
		}
		return []respPart{{kind: "text", text: s}}
	}
	var list []map[string]json.RawMessage
	if trimmed[0] == '{' {
		var one map[string]json.RawMessage
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		list = []map[string]json.RawMessage{one}
	} else if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var parts []respPart
	for _, m := range list {
		if p, ok := respParsePart(m); ok {
			parts = append(parts, p)
		}
	}
	return parts
}

// respParsePart reads one content part; parts with no counterpart upstream
// (reasoning_text, file ids, ...) are left out.
func respParsePart(m map[string]json.RawMessage) (respPart, bool) {
	switch respString(m["type"]) {
	case "input_text", "output_text", "text", "":
		if t := respString(m["text"]); t != "" {
			return respPart{kind: "text", text: t}, true
		}
	case "refusal":
		if t := respString(m["refusal"]); t != "" {
			return respPart{kind: "text", text: t}, true
		}
	case "input_image", "image_url":
		url := respString(m["image_url"])
		if url == "" {
			var nested struct {
				URL    string `json:"url"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(m["image_url"], &nested)
			url = nested.URL
		}
		if strings.TrimSpace(url) == "" || strings.HasPrefix(url, "data:") && respDataPayloadEmpty(url) {
			return respPart{}, false
		}
		return respPart{kind: "image", url: url, detail: respString(m["detail"])}, true
	case "input_file", "file":
		p := respPart{kind: "file", fileData: respString(m["file_data"]), fileURL: respString(m["file_url"]),
			fileName: respString(m["filename"]), fileID: respString(m["file_id"])}
		if nested, ok := m["file"]; ok {
			var f struct {
				FileData string `json:"file_data"`
				Filename string `json:"filename"`
				FileID   string `json:"file_id"`
			}
			_ = json.Unmarshal(nested, &f)
			p.fileData, p.fileName, p.fileID = f.FileData, f.Filename, f.FileID
		}
		if p.fileData != "" || p.fileURL != "" || p.fileID != "" {
			return p, true
		}
	}
	return respPart{}, false
}

func respDataPayloadEmpty(url string) bool {
	_, payload, ok := strings.Cut(url, ",")
	return !ok || strings.TrimSpace(payload) == ""
}

// respReasoningText is a reasoning item's visible text: its summary, else its
// raw reasoning content.
func respReasoningText(m map[string]json.RawMessage) string {
	collect := func(raw json.RawMessage) []string {
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			if s := respString(raw); s != "" {
				return []string{s}
			}
			return nil
		}
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return out
	}
	texts := collect(m["summary"])
	if len(texts) == 0 {
		texts = collect(m["content"])
	}
	return strings.Join(texts, "\n")
}

func respAgentMessageText(raw json.RawMessage) string {
	if s := respString(raw); s != "" {
		return s
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		switch respString(p["type"]) {
		case "input_text", "text":
			b.WriteString(respString(p["text"]))
		case "encrypted_content":
			b.WriteString(respString(p["encrypted_content"]))
		}
	}
	return b.String()
}

// respMediaMarker stands in a tool output's text for media moved elsewhere.
const respMediaMarker = "[Tool output media moved to the following user message]"

// respParseOutput reads a tool output: a string, an array of content parts,
// or other JSON. Images in it (Codex view_image returns an input_image part;
// MCP tools nest them in JSON) are returned as parts, with a marker in the
// text form where they were.
func respParseOutput(raw json.RawMessage) (text string, parts []respPart, media bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil, false
	}
	if trimmed[0] == '"' {
		s := respString(raw)
		if respIsImageDataURL(s) {
			return respMediaMarker, []respPart{{kind: "image", url: s}}, true
		}
		// a JSON document in a string may nest images too
		st := strings.TrimSpace(s)
		if st != "" && (st[0] == '{' || st[0] == '[') && json.Valid([]byte(st)) {
			if t, ps, ok := respRewriteMedia(json.RawMessage(st)); ok {
				return t, ps, true
			}
		}
		return s, nil, false
	}
	if trimmed[0] == '[' {
		var list []map[string]json.RawMessage
		if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
			all := true
			var ps []respPart
			for _, m := range list {
				switch respString(m["type"]) {
				case "input_text", "output_text", "text", "input_image", "image_url", "input_file":
				default:
					all = false
				}
				if p, ok := respParsePart(m); ok {
					ps = append(ps, p)
				}
			}
			if all {
				var texts []string
				hasMedia := false
				for _, p := range ps {
					if p.kind == "text" {
						texts = append(texts, p.text)
					} else {
						hasMedia = true
						texts = append(texts, respMediaMarker)
					}
				}
				if !hasMedia {
					return strings.Join(texts, "\n"), nil, false
				}
				return strings.Join(texts, "\n"), ps, true
			}
		}
	}
	if t, ps, ok := respRewriteMedia(raw); ok {
		return t, ps, true
	}
	return trimmed, nil, false
}

// respRewriteMedia replaces image nodes in a JSON document with a marker and
// returns the document as text plus the images. ok is false when it has none.
func respRewriteMedia(raw json.RawMessage) (string, []respPart, bool) {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return "", nil, false
	}
	var images []respPart
	var walk func(any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		case map[string]any:
			typ, _ := t["type"].(string)
			if typ == "input_image" || typ == "image_url" {
				url, _ := t["image_url"].(string)
				if nested, ok := t["image_url"].(map[string]any); ok {
					url, _ = nested["url"].(string)
				}
				if strings.TrimSpace(url) != "" {
					images = append(images, respPart{kind: "image", url: url})
					return map[string]any{"type": "input_text", "text": respMediaMarker}
				}
			}
			if typ == "image" { // MCP image content
				data, _ := t["data"].(string)
				mime, _ := t["mimeType"].(string)
				if data != "" && strings.HasPrefix(mime, "image/") {
					images = append(images, respPart{kind: "image", url: "data:" + mime + ";base64," + data})
					return map[string]any{"type": "text", "text": respMediaMarker}
				}
			}
			for k, c := range t {
				t[k] = walk(c)
			}
		}
		return v
	}
	v = walk(v)
	if len(images) == 0 {
		return "", nil, false
	}
	b, err := respMarshal(v)
	if err != nil {
		return "", nil, false
	}
	return string(b), images, true
}

func respIsImageDataURL(s string) bool {
	rest, ok := strings.CutPrefix(s, "data:image/")
	if !ok {
		return false
	}
	i := strings.Index(rest, ";base64,")
	return i > 0 && i+len(";base64,") < len(rest)
}

// respPairCalls enforces what both upstream APIs require of tool calls: the
// calls of one assistant turn are followed directly by their outputs, in call
// order. Codex histories break that (a developer notice between a call and its
// output, a parallel call whose output never came, an output whose call was
// trimmed away): calls without an output and outputs without a call are left
// out, and whatever sat between a call and its output comes after the outputs.
// A call id a history uses again (some model servers number the calls of
// every answer from zero) pairs its calls with its outputs in order of
// appearance, the later pairs under an id of their own; of several outputs
// for one call the last one wins.
func respPairCalls(items []respItem) []respItem {
	outputs := map[string][]respItem{}
	calls := map[string]int{}
	for _, it := range items {
		switch it.kind {
		case respItemOutput:
			outputs[it.callID] = append(outputs[it.callID], it)
		case respItemCall:
			calls[it.callID]++
		}
	}
	seen := map[string]int{}
	var out, group []respItem
	flush := func() {
		var results []respItem
		for _, it := range group {
			if it.kind == respItemCall {
				id := it.callID
				q := outputs[id]
				if len(q) == 0 {
					continue // unanswered
				}
				seen[id]++
				o := q[0]
				outputs[id] = q[1:]
				if seen[id] == calls[id] {
					o, outputs[id] = q[len(q)-1], nil // its last call: the last output
				}
				if seen[id] > 1 {
					it.callID = repeatedCallID(id, seen[id])
					o.callID = it.callID
				}
				results = append(results, o)
			}
			out = append(out, it)
		}
		out = append(out, results...)
		group = nil
	}
	for _, it := range items {
		switch {
		case it.kind == respItemReasoning, it.kind == respItemCall,
			it.kind == respItemMessage && it.role == "assistant":
			group = append(group, it)
		case it.kind == respItemOutput:
			flush() // placed after its call, or an orphan
		default:
			flush()
			out = append(out, it)
		}
	}
	flush()
	return out
}

// respDataURL splits a data: URL into its media type and base64 payload.
func respDataURL(url string) (mediaType, data string, ok bool) {
	rest, found := strings.CutPrefix(url, "data:")
	if !found {
		return "", "", false
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	mediaType, params, _ := strings.Cut(meta, ";")
	if !strings.Contains(";"+params, ";base64") {
		// percent-encoded or plain data: re-encode as base64
		return mediaType, base64.StdEncoding.EncodeToString([]byte(payload)), mediaType != ""
	}
	return mediaType, payload, mediaType != "" && payload != ""
}

// respMarshal encodes JSON without escaping <, > and &.
func respMarshal(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}
