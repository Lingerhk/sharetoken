package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// chat → Messages. The request becomes an Anthropic Messages request that
// streams; the events come back as chat chunks. Signed thinking blocks travel
// through the chat client as anthropic_thinking: on the stream at the end of
// each block, back on the assistant message of a later request.

func chatToMessages(body []byte, opt Options) (*Conversion, error) {
	r, err := chatParse(body)
	if err != nil {
		return nil, err
	}
	req, err := chatMessagesRequest(r)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	model := opt.Model
	if model == "" {
		model = r.Model
	}
	return &Conversion{Body: out, Translator: &chatFromMessages{out: newChatChunker(model), promptEst: chatPromptEstimate(r), blocks: map[int]*chatMessagesBlock{}}}, nil
}

// chatAnthropicMaxOutput is the output limit of a Claude model, for those
// that refuse more.
func chatAnthropicMaxOutput(model string) uint64 {
	switch {
	case strings.Contains(model, "claude-3-haiku"), strings.Contains(model, "claude-3-opus"), strings.Contains(model, "claude-3-sonnet"):
		return 4096
	case strings.Contains(model, "claude-3-5"):
		return 8192
	}
	return 32000 // Opus 4 and 4.1; later models take more, and a node asks for no more than 32768
}

func chatMessagesRequest(r *chatRequest) (map[string]any, error) {
	limit := chatAnthropicMaxOutput(r.Model)
	maxTokens := r.maxTokens()
	if maxTokens == 0 || maxTokens > limit {
		maxTokens = limit
	}
	req := map[string]any{"model": r.Model, "max_tokens": maxTokens, "stream": true}

	var system []any
	var msgs []map[string]any
	add := func(role string, blocks ...any) {
		if len(blocks) > 0 {
			msgs = append(msgs, map[string]any{"role": role, "content": blocks})
		}
	}
	ids := newChatLegacyIDs(r.Messages)
	for i := range r.Messages {
		m := &r.Messages[i]
		for _, p := range m.parts() {
			if err := p.CacheControl.valid(); err != nil {
				return nil, err
			}
		}
		switch m.Role {
		case "system", "developer":
			for _, p := range m.parts() {
				if p.isText() && p.Text != "" {
					block := map[string]any{"type": "text", "text": p.Text}
					if p.CacheControl != nil {
						block["cache_control"] = p.CacheControl.value()
					}
					system = append(system, block)
				}
			}
		case "assistant":
			blocks, err := chatThinkingHistory(m.AnthropicThinking)
			if err != nil {
				return nil, err
			}
			if len(blocks) > 0 {
				// The signed blocks carry the reasoning: it is not repeated as text.
				blocks = append(blocks, chatAnthropicBlocks(m, false)...)
			} else {
				blocks = chatAnthropicBlocks(m, true)
			}
			for _, tc := range m.toolCalls(ids) {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": chatToolUseID(tc.ID), "name": tc.Function.Name, "input": chatObjectJSON(tc.Function.Arguments)})
			}
			add("assistant", blocks...)
		case "tool", "function":
			result := map[string]any{"type": "tool_result", "tool_use_id": chatToolUseID(m.resultID(ids))}
			content := chatAnthropicBlocks(m, false)
			if len(content) == 0 {
				content = []any{map[string]any{"type": "text", "text": "(empty)"}}
			}
			result["content"] = content
			add("user", result)
		default: // user, and any other role as the user's
			add("user", chatAnthropicBlocks(m, false)...)
		}
	}
	msgs = chatPairTools(msgs)
	if len(msgs) == 0 {
		return nil, fmt.Errorf("the request has no user message")
	}
	// A final assistant turn is a prefill, which may not end in whitespace.
	if last := msgs[len(msgs)-1]; last["role"] == "assistant" {
		blocks := last["content"].([]any)
		if b, ok := blocks[len(blocks)-1].(map[string]any); ok && b["type"] == "text" {
			b["text"] = strings.TrimRight(b["text"].(string), " \t\r\n")
		}
	}
	req["messages"] = msgs

	switch rf := r.responseFormat(); rf.kind {
	case "json_object":
		system = append(system, map[string]any{"type": "text", "text": "Answer with a single JSON object and nothing else."})
	case "json_schema":
		if len(rf.schema) > 0 {
			// Messages has a field for a schema: output_config.format.
			chatOutputConfig(req)["format"] = map[string]any{"type": "json_schema", "schema": rf.schema}
		} else {
			system = append(system, map[string]any{"type": "text", "text": "Answer with a single JSON object and nothing else."})
		}
	}
	if len(system) > 0 {
		req["system"] = system
	}

	// Current Claude models refuse temperature together with top_p.
	switch {
	case r.Temperature != nil:
		req["temperature"] = min(*r.Temperature, 1) // OpenAI's range is 0..2, Anthropic's 0..1
	case r.TopP != nil:
		req["top_p"] = *r.TopP
	}
	// Anthropic refuses a stop sequence that is only whitespace ("\n").
	if s := slices.DeleteFunc(r.stops(), func(s string) bool { return strings.TrimSpace(s) == "" }); len(s) > 0 {
		req["stop_sequences"] = s
	}
	// The chat request's user goes nowhere: it names the buyer's own user,
	// and the seller's model server has no business knowing it.

	forced := false
	tools, err := chatAnthropicTools(r.Tools)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		req["tools"] = tools
		mode, name, _ := r.toolChoice()
		var choice map[string]any
		switch {
		case name != "":
			choice, forced = map[string]any{"type": "tool", "name": name}, true
		case mode == "required":
			choice, forced = map[string]any{"type": "any"}, true
		case mode == "none":
			// The tools stay: a history with tool_use blocks needs them.
			choice = map[string]any{"type": "none"}
		case mode == "auto":
			choice = map[string]any{"type": "auto"}
		}
		if r.ParallelToolCalls != nil && !*r.ParallelToolCalls && (choice == nil || choice["type"] != "none") {
			if choice == nil {
				choice = map[string]any{"type": "auto"}
			}
			choice["disable_parallel_tool_use"] = true
		}
		if choice != nil {
			req["tool_choice"] = choice
		}
	} else {
		var called calledTools
		for _, m := range msgs {
			blocks, _ := m["content"].([]any)
			for _, b := range blocks {
				if block, ok := b.(map[string]any); ok {
					called.add(block)
				}
			}
		}
		called.declare(req)
	}
	chatAnthropicThinking(req, r, maxTokens, forced)
	if _, _, claude := claudeVersion(r.Model); claude {
		chatAutoCache(req) // other vendors' Messages endpoints cache by themselves
	}
	chatNormalizeCache(req)
	return req, nil
}

// chatAutoCache marks cache breakpoints on a request whose client marked
// none (OpenCode, Cline and most chat clients know nothing of them): the
// tools, the system prompt and the newest content, as the Responses
// conversion does, so each turn reads what the last one wrote instead of
// paying the whole prompt at the full price (magpie marks them the same
// way). A client's own breakpoints are kept as they are.
func chatAutoCache(req map[string]any) {
	marked := func(b map[string]any) bool { _, ok := b["cache_control"]; return ok }
	tools, _ := req["tools"].([]any)
	system, _ := req["system"].([]any)
	msgs, _ := req["messages"].([]map[string]any)
	for _, list := range [][]any{tools, system} {
		for _, it := range list {
			if b, ok := it.(map[string]any); ok && marked(b) {
				return
			}
		}
	}
	for _, m := range msgs {
		blocks, _ := m["content"].([]any)
		for _, it := range blocks {
			if b, ok := it.(map[string]any); ok && marked(b) {
				return
			}
		}
	}
	mark := func(b map[string]any) { b["cache_control"] = map[string]any{"type": "ephemeral"} }
	if n := len(tools); n > 0 {
		if b, ok := tools[n-1].(map[string]any); ok {
			mark(b)
		}
	}
	if n := len(system); n > 0 {
		if b, ok := system[n-1].(map[string]any); ok {
			mark(b)
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		blocks, _ := msgs[i]["content"].([]any)
		for j := len(blocks) - 1; j >= 0; j-- {
			if b, ok := blocks[j].(map[string]any); ok {
				switch b["type"] {
				case "text", "image", "document", "tool_use", "tool_result":
					mark(b)
					return
				}
			}
		}
	}
}

// claudeVersion reads a Claude model name: its family (opus, sonnet, haiku,
// fable, mythos) and its version as major*10+minor (claude-opus-4-7 is 47,
// claude-sonnet-4-20250514 is 40, claude-3-5-sonnet-latest is 35). ok is
// false for any other model, which an Anthropic-compatible server may well
// serve.
func claudeVersion(model string) (family string, version int, ok bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	i := strings.LastIndex(m, "claude-")
	if i < 0 {
		return "", 0, false
	}
	m, _, _ = strings.Cut(m[i+len("claude-"):], "@")
	var nums []int
	for _, tok := range strings.Split(strings.ReplaceAll(m, ".", "-"), "-") {
		n, err := strconv.Atoi(tok)
		if err != nil && family == "" {
			family = tok // before the version, or (the 3.x names) after it
			continue
		}
		// A date, a third number or a second word ends the version.
		if err != nil || len(tok) > 2 || len(nums) == 2 {
			break
		}
		nums = append(nums, n)
	}
	if family == "" || len(nums) == 0 {
		return "", 0, false
	}
	version = nums[0] * 10
	if len(nums) > 1 {
		version += nums[1]
	}
	return family, version, true
}

// AdaptiveThinking reports whether a Claude model thinks adaptively (thinking
// {"type":"adaptive"} with output_config.effort) and refuses a thinking
// budget: Opus 4.7 and every model after it, Fable and Mythos. A model server
// that refuses either form says so, and the Anthropic adapter switches to the
// other.
func AdaptiveThinking(model string) bool {
	family, v, ok := claudeVersion(model)
	return ok && (family == "fable" || family == "mythos" || v >= 47)
}

// claudeThinkingOff is the thinking setting closest to "no reasoning" for an
// adaptive model: Sonnet 5.5 refuses disabled and has between_tools, Opus 5.5,
// Fable and Mythos cannot turn thinking off, the others take disabled. All of
// them at the lowest effort.
func claudeThinkingOff(model string) map[string]any {
	family, v, _ := claudeVersion(model)
	switch {
	case family == "sonnet" && v >= 55:
		return map[string]any{"type": "between_tools"}
	case family == "fable", family == "mythos", family == "opus" && v >= 55:
		return map[string]any{"type": "adaptive"}
	}
	return map[string]any{"type": "disabled"}
}

// ClaudeThinkingOffAsTaken rewrites a native Messages request that turns
// thinking off (thinking.type disabled or between_tools) into the form the
// model takes, by Anthropic's rules for each line (after magpie's
// thinkingOffAsTaken):
//
//   - Sonnet 5.5 refuses disabled ("To turn thinking off on this model, send
//     "thinking": {"type": "between_tools"} instead of {"type": "disabled"}")
//     and takes between_tools alone in thinking, at effort high or below;
//   - Opus 5.5, Fable 5 and Mythos 5 cannot turn thinking off: thinking is left
//     out (they think adaptively), at effort low unless one is asked, with
//     room for a short answer on top of the thinking;
//   - Opus 5 and Haiku 5.5 take disabled at effort high or below;
//   - older Claudes and Sonnet/Haiku 5 take disabled (between_tools becomes
//     disabled).
//
// Claude Code turns thinking off for side requests (titles, classifiers)
// whenever it cannot tell the model refuses it, and a model reached through
// a gateway is one. A model these rules do not know is left as sent. It
// reports whether req changed.
func ClaudeThinkingOffAsTaken(req map[string]any, model string) bool {
	th, _ := req["thinking"].(map[string]any)
	t, _ := th["type"].(string)
	if t != "disabled" && t != "between_tools" {
		return false
	}
	family, v, ok := claudeVersion(model)
	if !ok {
		return false
	}
	oc, _ := req["output_config"].(map[string]any)
	effort, _ := oc["effort"].(string)
	aboveHigh := effort == "xhigh" || effort == "max"
	const (
		taken = iota
		upToHigh
		betweenTools
		never
	)
	rule := -1
	switch {
	case (family == "fable" || family == "mythos") && v >= 50 && v < 60:
		rule = never
	case family == "fable" || family == "mythos":
	case v < 50:
		rule = taken
	case v == 50 && family == "opus":
		rule = upToHigh
	case v == 50:
		rule = taken
	case v == 55 && family == "sonnet":
		rule = betweenTools
	case v == 55 && family == "opus":
		rule = never
	case v == 55 && family == "haiku":
		rule = upToHigh
	}
	effortTo := ""
	switch rule {
	case taken:
		if t == "disabled" {
			return false
		}
		req["thinking"] = map[string]any{"type": "disabled"}
	case upToHigh:
		if t == "disabled" && !aboveHigh && len(th) == 1 {
			return false
		}
		req["thinking"] = map[string]any{"type": "disabled"}
		if aboveHigh {
			effortTo = "high"
		}
	case betweenTools:
		if t == "between_tools" && len(th) == 1 && !aboveHigh {
			return false
		}
		// between_tools takes no other field (display, budget_tokens)
		req["thinking"] = map[string]any{"type": "between_tools"}
		if aboveHigh {
			effortTo = "high"
		}
	case never:
		delete(req, "thinking")
		if effort == "" {
			effortTo = "low"
		}
		if n := maxTokensOf(req["max_tokens"]); n > 0 && n < claudeThinkingRoom {
			req["max_tokens"] = json.Number(strconv.FormatInt(n+claudeThinkingRoom, 10))
		}
	default:
		return false
	}
	if effortTo != "" {
		if oc == nil {
			oc = map[string]any{}
			req["output_config"] = oc
		}
		oc["effort"] = effortTo
	}
	return true
}

// claudeThinkingRoom is what a model that always thinks is given on top of a
// short answer's max_tokens when thinking off is left out for it, as Claude
// Code gives its classifier (magpie: classifierRoom).
const claudeThinkingRoom = 2048

func maxTokensOf(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// claudeAdaptive maps an OpenAI reasoning effort onto an adaptive model:
// thinking and output_config.effort. A forced tool choice goes without the
// thinking setting, as it does for the models with a budget.
func claudeAdaptive(req map[string]any, model, effort string, forced bool) {
	thinking := map[string]any{"type": "adaptive"}
	switch effort {
	case "none":
		thinking, effort = claudeThinkingOff(model), "low"
	case "minimal":
		effort = "low"
	case "low", "medium", "high", "xhigh", "max":
	default:
		effort = "medium"
	}
	chatOutputConfig(req)["effort"] = effort
	if forced && thinking["type"] == "adaptive" {
		return
	}
	req["thinking"] = thinking
}

// claudeSampling removes the sampling settings a Claude model refuses:
// Opus 4.7 and the models after it take none (the 5.5 Sonnet and Haiku only
// their defaults), and no model takes temperature with thinking on.
func claudeSampling(req map[string]any, model string) {
	if AdaptiveThinking(model) {
		delete(req, "temperature")
		delete(req, "top_p")
		return
	}
	th, _ := req["thinking"].(map[string]any)
	if th == nil || th["type"] == "disabled" {
		return
	}
	delete(req, "temperature")
	if p, ok := req["top_p"].(float64); ok && p < 0.95 {
		delete(req, "top_p")
	}
}

// chatAnthropicThinking maps reasoning_effort to extended thinking. The
// adaptive models think at an effort; earlier models take a budget, which
// must be at least 1024 tokens and is at most half of max_tokens, and refuse
// sampling settings and a forced tool with it.
func chatAnthropicThinking(req map[string]any, r *chatRequest, maxTokens uint64, forced bool) {
	defer claudeSampling(req, r.Model)
	effort := strings.ToLower(strings.TrimSpace(r.ReasoningEffort))
	if effort == "" {
		return
	}
	if AdaptiveThinking(r.Model) {
		claudeAdaptive(req, r.Model, effort, forced)
		return
	}
	if effort == "none" {
		// Said outright: a Messages server whose model thinks unless told
		// otherwise (and a conversion back to chat) must not fall back to
		// its default.
		req["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	budget := map[string]uint64{"low": 1024, "medium": 4096, "high": 10240, "xhigh": 32768, "max": 32768}[effort]
	// At least half of max_tokens stays for the visible answer.
	budget = min(budget, maxTokens/2)
	if budget < 1024 || forced {
		return // none, minimal, unknown; or no room to think
	}
	// In a tool loop the assistant turn that called the tools must start
	// with a thinking block; a client that did not send anthropic_thinking
	// back gets the turn without thinking.
	if msgs, _ := req["messages"].([]map[string]any); !chatTrailingToolTurnHasThinking(msgs) {
		return
	}
	req["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
}

// chatOutputConfig is the request's output_config, made when there is none.
func chatOutputConfig(req map[string]any) map[string]any {
	oc, ok := req["output_config"].(map[string]any)
	if !ok {
		oc = map[string]any{}
		req["output_config"] = oc
	}
	return oc
}

// chatTrailingToolTurnHasThinking reports whether thinking can be enabled:
// when the conversation ends in a tool loop, the assistant turn that called
// the tools must start with a thinking block (as respTrailingToolTurnHasThinking).
func chatTrailingToolTurnHasThinking(msgs []map[string]any) bool {
	has := func(blocks []any, typ string) bool {
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["type"] == typ {
				return true
			}
		}
		return false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		blocks, _ := msgs[i]["content"].([]any)
		if msgs[i]["role"] == "user" {
			if !has(blocks, "tool_result") {
				return true // a fresh user turn: no tool loop to continue
			}
			continue
		}
		if len(blocks) == 0 || !has(blocks, "tool_use") {
			return true
		}
		first, _ := blocks[0].(map[string]any)
		return first["type"] == "thinking" || first["type"] == "redacted_thinking"
	}
	return true
}

// chatThinkingHistory turns anthropic_thinking blocks back into the signed
// blocks they were: a block without its signature (or data) is refused, as
// the upstream would.
func chatThinkingHistory(history []json.RawMessage) ([]any, error) {
	var blocks []any
	for _, raw := range history {
		var b chatThinkingBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("invalid anthropic_thinking block")
		}
		switch b.Type {
		case "thinking":
			if b.Signature == "" {
				return nil, fmt.Errorf("anthropic_thinking requires the original signature")
			}
			blocks = append(blocks, map[string]any{"type": "thinking", "thinking": b.Thinking, "signature": b.Signature})
		case "redacted_thinking":
			if b.Data == "" {
				return nil, fmt.Errorf("redacted thinking requires its original data")
			}
			blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": b.Data})
		default:
			return nil, fmt.Errorf("unsupported anthropic_thinking block type")
		}
	}
	return blocks, nil
}

// chatAnthropicBlocks turns a message's content into content blocks: text,
// images and documents. withReasoning marks an assistant message's reasoning
// and thinking parts with <thinking> tags in its text.
func chatAnthropicBlocks(m *chatMessage, withReasoning bool) []any {
	var out []any
	if m.Role == "assistant" && withReasoning {
		if r := m.reasoning(); r != "" {
			out = append(out, map[string]any{"type": "text", "text": "<thinking>" + r + "</thinking>"})
		}
	}
	for _, p := range m.parts() {
		before := len(out)
		switch {
		case p.isText() || p.Type == "output_text":
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, map[string]any{"type": "text", "text": p.Text})
			}
		case p.Type == "thinking" || p.Type == "reasoning":
			t := chatFirst(p.Thinking, p.Text)
			if withReasoning && t != "" {
				out = append(out, map[string]any{"type": "text", "text": "<thinking>" + t + "</thinking>"})
			}
		case p.Type == "image_url":
			url, _ := p.image()
			if mt, data, ok := chatDataURL(url); ok {
				if strings.TrimSpace(data) != "" {
					out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": data}})
				}
			} else if url != "" {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}})
			}
		case p.Type == "file":
			if p.File == nil {
				continue
			}
			if mt, data, ok := chatDataURL(p.File.FileData); ok && strings.TrimSpace(data) != "" {
				doc := map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": mt, "data": data}}
				if mt == "text/plain" {
					if text, err := chatBase64Text(data); err == nil {
						doc["source"] = map[string]any{"type": "text", "media_type": mt, "data": text}
					}
				}
				if p.File.Filename != "" {
					doc["title"] = p.File.Filename
				}
				out = append(out, doc)
			}
		}
		if p.CacheControl != nil && len(out) > before {
			out[len(out)-1].(map[string]any)["cache_control"] = p.CacheControl.value()
		}
	}
	return out
}

var chatToolUseIDBad = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// chatToolUseID makes a call ID one Anthropic takes (letters, digits, _ and
// -), the same for a call and its result.
func chatToolUseID(id string) string {
	if id == "" {
		return "toolu_empty"
	}
	return chatToolUseIDBad.ReplaceAllString(id, "_")
}

// chatPairTools makes the turns what Anthropic requires: every tool_use is
// answered by a tool_result in the very next user turn (results are moved
// there from wherever the history had them; calls without a result and
// results without a call are dropped; a repeated call ID is made unique), the
// roles alternate (consecutive
// turns of a role become one: tool results, which chat sends as a message
// each, are blocks of a single user turn), and a user turn starts with its
// tool results.
func chatPairTools(msgs []map[string]any) []map[string]any {
	// The results of each call ID, in order: an ID a history uses again (some
	// model servers number the calls of every answer from zero) pairs its
	// calls with its results in order of appearance.
	results := map[string][]map[string]any{}
	for _, m := range msgs {
		if m["role"] != "user" {
			continue
		}
		for _, b := range m["content"].([]any) {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
				id := bm["tool_use_id"].(string)
				results[id] = append(results[id], bm)
			}
		}
	}
	calls := map[string]int{}
	var out []map[string]any
	add := func(role any, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1]["role"] == role {
			out[n-1]["content"] = append(out[n-1]["content"].([]any), blocks...)
			return
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	for _, m := range msgs {
		var kept, answers []any
		for _, b := range m["content"].([]any) {
			bm, _ := b.(map[string]any)
			switch {
			case bm != nil && bm["type"] == "tool_use":
				id := bm["id"].(string)
				if q := results[id]; len(q) > 0 {
					r := q[0]
					results[id] = q[1:] // each result answers one call
					if calls[id]++; calls[id] > 1 {
						// Anthropic wants tool_use IDs unique in a request.
						unique := repeatedCallID(id, calls[id])
						bm["id"], r["tool_use_id"] = unique, unique
					}
					kept, answers = append(kept, b), append(answers, r)
				}
				continue
			case bm != nil && bm["type"] == "tool_result":
				continue // it goes after its call
			}
			kept = append(kept, b)
		}
		add(m["role"], kept)
		add("user", answers)
	}
	for _, m := range out {
		if m["role"] == "user" {
			slices.SortStableFunc(m["content"].([]any), func(a, b any) int { return chatResultRank(a) - chatResultRank(b) })
		}
	}
	return out
}

func chatResultRank(b any) int {
	if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
		return 0
	}
	return 1
}

// chatAnthropicTools converts function tools (their schema made an object
// schema Anthropic takes); other tools are left out.
func chatAnthropicTools(tools []chatTool) ([]any, error) {
	var out []any
	for i := range tools {
		t := &tools[i]
		if err := t.CacheControl.valid(); err != nil {
			return nil, err
		}
		// Function tools only: a built-in tool (web_search, ...) would run on
		// the upstream's side, which the node does not price.
		if !t.isFunction() {
			continue
		}
		tool := map[string]any{"name": t.Function.Name, "input_schema": chatAnthropicSchema(t.Function.Parameters)}
		if t.Function.Description != "" {
			tool["description"] = t.Function.Description
		}
		if t.CacheControl != nil {
			tool["cache_control"] = t.CacheControl.value()
		}
		out = append(out, tool)
	}
	return out, nil
}

// chatAnthropicSchema makes a tool's parameters an input_schema Anthropic
// takes: an object at the root, with properties, and no oneOf, anyOf or
// allOf there. A root union becomes one object: the properties of all
// branches (one a branch defines differently becomes an anyOf of both), and
// as required what every branch of a oneOf or anyOf requires, and what any
// branch of an allOf does.
func chatAnthropicSchema(schema json.RawMessage) json.RawMessage {
	const empty = `{"type":"object","properties":{}}`
	var m map[string]json.RawMessage
	if len(schema) == 0 || json.Unmarshal(schema, &m) != nil || m == nil {
		return json.RawMessage(empty)
	}
	chatFlattenUnions(m, 0)
	if raw, ok := m["type"]; !ok || string(raw) == "null" {
		m["type"] = json.RawMessage(`"object"`)
	} else {
		var typ string
		if json.Unmarshal(raw, &typ) != nil || typ != "object" {
			return json.RawMessage(empty)
		}
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

// maxUnionDepth is how deep unions nested in unions are flattened. A tool
// schema is a buyer's: each level parses its branch again, so without a
// bound a schema nested thousands deep holds a job slot and a CPU for as
// long as it likes, before the job's timeout applies.
const maxUnionDepth = 32

func chatFlattenUnions(schema map[string]json.RawMessage, depth int) {
	props := map[string]json.RawMessage{}
	if raw, ok := schema["properties"]; ok {
		_ = json.Unmarshal(raw, &props)
		if props == nil {
			props = map[string]json.RawMessage{}
		}
	}
	required := chatRequired(schema)
	found := false
	for _, kw := range []string{"oneOf", "anyOf", "allOf"} {
		raw, ok := schema[kw]
		if !ok {
			continue
		}
		found = true
		delete(schema, kw)
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
			continue
		}
		var common []string
		for i, item := range items {
			var branch map[string]json.RawMessage
			if depth >= maxUnionDepth || json.Unmarshal(item, &branch) != nil || branch == nil {
				branch = nil
			} else {
				chatFlattenUnions(branch, depth+1)
			}
			object := false
			if branch != nil {
				if t, ok := branch["type"]; ok {
					var typ string
					object = json.Unmarshal(t, &typ) == nil && typ == "object"
				} else {
					_, object = branch["properties"]
				}
			}
			var req []string
			if object {
				var bp map[string]json.RawMessage
				if json.Unmarshal(branch["properties"], &bp) == nil {
					for name, p := range bp {
						if have, ok := props[name]; ok && !chatSameJSON(have, p) {
							both, _ := json.Marshal(map[string][]json.RawMessage{"anyOf": {have, p}})
							props[name] = both
						} else if !ok {
							props[name] = p
						}
					}
				}
				req = chatRequired(branch)
			}
			if kw == "allOf" {
				required = chatUnion(required, req)
				continue
			}
			if i == 0 {
				common = req
			} else {
				common = slices.DeleteFunc(common, func(n string) bool { return !slices.Contains(req, n) })
			}
		}
		if kw != "allOf" {
			required = chatUnion(required, common)
		}
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

func chatRequired(schema map[string]json.RawMessage) []string {
	var req []string
	if raw, ok := schema["required"]; ok {
		_ = json.Unmarshal(raw, &req)
	}
	return req
}

func chatUnion(a, b []string) []string {
	out := slices.Clone(a)
	for _, n := range b {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func chatSameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && x.String() == y.String()
}

// chatNormalizeCache keeps the request's cache breakpoints within what
// Anthropic takes: at most four (removed first from the tools' end, then the
// messages' start, then the system's end), never on thinking blocks, and no
// 1h breakpoint after a 5m one in processing order (tools, system,
// messages). Run it again after adding blocks with breakpoints.
func chatNormalizeCache(req map[string]any) {
	var tools, system, messages []map[string]any
	collect := func(raw []any, dst *[]map[string]any) {
		for _, item := range raw {
			b, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "thinking" || b["type"] == "redacted_thinking" {
				delete(b, "cache_control")
				continue
			}
			if _, ok := b["cache_control"].(map[string]any); ok {
				*dst = append(*dst, b)
			}
			if inner, ok := b["content"].([]any); ok && b["type"] == "tool_result" {
				for _, c := range inner {
					if cb, ok := c.(map[string]any); ok {
						if _, ok := cb["cache_control"].(map[string]any); ok {
							*dst = append(*dst, cb)
						}
					}
				}
			}
		}
	}
	if raw, ok := req["tools"].([]any); ok {
		collect(raw, &tools)
	}
	if raw, ok := req["system"].([]any); ok {
		collect(raw, &system)
	}
	if raw, ok := req["messages"].([]map[string]any); ok {
		for _, msg := range raw {
			if blocks, ok := msg["content"].([]any); ok {
				collect(blocks, &messages)
			}
		}
	}
	remaining := len(tools) + len(system) + len(messages) - 4
	remove := func(b map[string]any) {
		if remaining > 0 {
			delete(b, "cache_control")
			remaining--
		}
	}
	for i := len(tools) - 1; i >= 0; i-- {
		remove(tools[i])
	}
	for _, b := range messages {
		remove(b)
	}
	for i := len(system) - 1; i >= 0; i-- {
		remove(system[i])
	}
	ordered := append(append(append([]map[string]any{}, tools...), system...), messages...)
	longer := false
	for i := len(ordered) - 1; i >= 0; i-- {
		cc, ok := ordered[i]["cache_control"].(map[string]any)
		if !ok {
			continue
		}
		if cc["ttl"] == "1h" {
			longer = true
		}
		if longer {
			cc["ttl"] = "1h"
		}
	}
}

// chatFromMessages turns Messages events into chat chunks.
type chatFromMessages struct {
	out       *chatChunker
	promptEst uint64
	blocks    map[int]*chatMessagesBlock // open content blocks by index
	usage     chatMessagesUsage
	started   bool // message_start came
	stop      string
}

type chatMessagesBlock struct {
	kind     string // text, thinking, tool, other
	tool     int
	seed     string // a tool_use's input given on its start
	streamed bool   // input_json_delta came
	sent     bool   // arguments went out
	thinking chatThinkingBlock
}

type chatMessagesUsage struct {
	InputTokens   uint64 `json:"input_tokens"`
	OutputTokens  uint64 `json:"output_tokens"`
	CacheCreation uint64 `json:"cache_creation_input_tokens"`
	CacheRead     uint64 `json:"cache_read_input_tokens"`
	// Some relays report cache writes by lifetime only.
	ByTTL *struct {
		FiveMinutes uint64 `json:"ephemeral_5m_input_tokens"`
		OneHour     uint64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// cacheCreation is the cache writes: the total, else the sum by lifetime.
func (u *chatMessagesUsage) cacheCreation() uint64 {
	if u.CacheCreation == 0 && u.ByTTL != nil {
		return u.ByTTL.FiveMinutes + u.ByTTL.OneHour
	}
	return u.CacheCreation
}

// merge takes the counts an event reports: Anthropic sends input counts on
// message_start and, from some upstreams, again on message_delta.
func (u *chatMessagesUsage) merge(v *chatMessagesUsage) {
	if v == nil {
		return
	}
	if v.InputTokens > 0 {
		u.InputTokens = v.InputTokens
	}
	if v.OutputTokens > 0 {
		u.OutputTokens = v.OutputTokens
	}
	if n := v.cacheCreation(); n > 0 {
		u.CacheCreation = n
	}
	if v.CacheRead > 0 {
		u.CacheRead = v.CacheRead
	}
}

// chat is the usage as chat reports it: the prompt counts cached input and
// cache writes, which Anthropic counts apart.
func (u *chatMessagesUsage) chat() chatUsage {
	return chatUsage{Prompt: u.InputTokens + u.CacheRead + u.CacheCreation, Completion: u.OutputTokens,
		Cached: u.CacheRead, CacheCreation: u.CacheCreation}
}

func (s *chatFromMessages) Event(payload []byte, emit func([]byte) error) error {
	var ev struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message *struct {
			Usage *chatMessagesUsage `json:"usage"`
		} `json:"message"`
		ContentBlock *struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			Data      string          `json:"data"`
			Input     json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage *chatMessagesUsage `json:"usage"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil // not ours to judge: skip what cannot be read
	}
	s.out.emit = emit
	if s.out.finished && ev.Type != "error" {
		return nil
	}
	switch ev.Type {
	case "message_start":
		s.started = true
		if ev.Message != nil {
			s.usage.merge(ev.Message.Usage)
		}
	case "content_block_start":
		cb := ev.ContentBlock
		if cb == nil {
			return nil
		}
		switch cb.Type {
		case "text":
			s.blocks[ev.Index] = &chatMessagesBlock{kind: "text"}
			return s.out.content(cb.Text)
		case "thinking", "redacted_thinking":
			s.blocks[ev.Index] = &chatMessagesBlock{kind: "thinking",
				thinking: chatThinkingBlock{Type: cb.Type, Thinking: cb.Thinking, Signature: cb.Signature, Data: cb.Data}}
			return s.out.reasoning(cb.Thinking)
		case "tool_use":
			i, err := s.out.toolStart(cb.ID, cb.Name, "")
			b := &chatMessagesBlock{kind: "tool", tool: i}
			// Some relays give the whole input here and stream none: kept
			// until the block ends, unless deltas come.
			if in := strings.TrimSpace(string(cb.Input)); in != "" && in != "{}" && in != "null" {
				b.seed = in
			}
			s.blocks[ev.Index] = b
			return err
		default: // server tools and their results run upstream: not the client's
			s.blocks[ev.Index] = &chatMessagesBlock{kind: "other"}
		}
	case "content_block_delta":
		b, d := s.blocks[ev.Index], ev.Delta
		if d == nil {
			return nil
		}
		switch d.Type {
		case "text_delta":
			return s.out.content(d.Text)
		case "thinking_delta":
			if b != nil && b.kind == "thinking" {
				b.thinking.Thinking += d.Thinking
			}
			return s.out.reasoning(d.Thinking)
		case "signature_delta":
			if b != nil && b.kind == "thinking" {
				b.thinking.Signature += d.Signature
			}
		case "input_json_delta":
			if b != nil && b.kind == "tool" {
				b.streamed = true
				b.sent = b.sent || d.PartialJSON != ""
				return s.out.toolArgs(b.tool, d.PartialJSON)
			}
		}
	case "content_block_stop":
		b := s.blocks[ev.Index]
		delete(s.blocks, ev.Index)
		if b == nil {
			return nil
		}
		return s.closeBlock(b)
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			s.stop = ev.Delta.StopReason
		}
		s.usage.merge(ev.Usage)
	case "message_stop":
		if err := s.closeAll(); err != nil {
			return err
		}
		return s.out.finish(chatStopReason(s.stop), s.usage.chat())
	case "error":
		e := &chatUpstreamError{Kind: "error", Message: "the upstream failed"}
		if ev.Error != nil {
			e.Kind, e.Message = chatFirst(ev.Error.Type, e.Kind), ev.Error.Message
		}
		return e
	}
	return nil
}

// closeBlock finishes a content block: a signed or redacted thinking block
// goes out whole as anthropic_thinking (also when its text was omitted, so
// the reasoning is not lost to the next turn), a tool_use without streamed
// input gets the input its start gave, or "{}".
func (s *chatFromMessages) closeBlock(b *chatMessagesBlock) error {
	switch b.kind {
	case "thinking":
		if b.thinking.Signature != "" || (b.thinking.Type == "redacted_thinking" && b.thinking.Data != "") {
			return s.out.thinking(b.thinking)
		}
	case "tool":
		if !b.streamed && b.seed != "" {
			b.streamed, b.sent = true, true
			return s.out.toolArgs(b.tool, b.seed)
		}
		if !b.sent {
			// A tool without input streams no arguments (one empty delta):
			// chat clients parse the arguments as JSON, so they are "{}".
			b.sent = true
			return s.out.toolArgs(b.tool, "{}")
		}
	}
	return nil
}

func (s *chatFromMessages) closeAll() error {
	idx := make([]int, 0, len(s.blocks))
	for i := range s.blocks {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	for _, i := range idx {
		b := s.blocks[i]
		delete(s.blocks, i)
		if err := s.closeBlock(b); err != nil {
			return err
		}
	}
	return nil
}

// chatStopReason maps an Anthropic stop_reason to a chat finish_reason.
func chatStopReason(stop string) string {
	switch stop {
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return "stop" // end_turn, stop_sequence, pause_turn
}

func (s *chatFromMessages) Close(emit func([]byte) error) error {
	s.out.emit = emit
	if s.out.finished {
		return nil
	}
	if err := s.closeAll(); err != nil {
		return err
	}
	if !s.out.emitted {
		return errChatStreamEnded
	}
	// Cut short: closed as if it had stopped here. The prompt is what
	// message_start reported, else an estimate; the output what was sent.
	u := s.usage.chat()
	if u.Prompt == 0 {
		u.Prompt = s.promptEst
	}
	u.Completion = max(u.Completion, s.out.output)
	return s.out.finish(chatStopReason(s.stop), u)
}
