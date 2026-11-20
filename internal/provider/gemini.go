package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/node"
)

// Google's Code Assist backend (the Gemini CLI's and Antigravity's) speaks
// the native Gemini API inside an envelope: the chat-completions request is
// rewritten into generateContent form, and the stream back into chunks.

// geminiOptions are the differences between the backends.
type geminiOptions struct {
	// validated asks for validated function calling, which Antigravity wants
	// whenever a request has tools.
	validated bool
	// signFunctionCalls gives each function call replayed from the
	// conversation a thought signature, which the backend wants: the one
	// the model gave (carried in the call's id, signedID), or the
	// placeholder for a call it did not sign.
	signFunctionCalls bool
	// signatures is the store the long signatures of this provider are
	// kept in; dropSignatures sends none of them back, only placeholders
	// (the backend refused one).
	signatures     *sigStore
	dropSignatures bool
	// jsonSchema sends tool parameters as parametersJsonSchema, full JSON
	// Schema (the Gemini CLI's backend); otherwise they are reduced to the
	// Schema object (cleanSchema), which Antigravity wants.
	jsonSchema bool
	maxOutput  uint64
	// effort is the reasoning effort the request asked for (chat's
	// reasoning_effort), which the chat request type does not keep.
	effort string
}

const skipThoughtSignature = "skip_thought_signature_validator"

// geminiRequest turns a chat-completions request into a generateContent
// request (the part that goes inside the envelope).
func geminiRequest(chat *chatRequest, opt geminiOptions) (map[string]any, error) {
	var system []any
	var contents []map[string]any
	callName := map[string]string{} // tool call ID → function name
	add := func(role string, parts ...any) {
		if len(parts) == 0 {
			return
		}
		if n := len(contents); n > 0 && contents[n-1]["role"] == role {
			contents[n-1]["parts"] = append(contents[n-1]["parts"].([]any), parts...)
			return
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	for i := range chat.Messages {
		m := &chat.Messages[i]
		switch m.Role {
		case "system", "developer":
			if t := m.text(); t != "" {
				system = append(system, map[string]any{"text": t})
			}
		case "assistant":
			add("model", append(geminiParts(m), geminiCalls(m.ToolCalls, callName, opt)...)...)
		case "tool":
			id, _ := unsignedID(m.ToolCallID, nil)
			name := callName[id]
			if name == "" {
				name = m.Name
			}
			add("user", map[string]any{"functionResponse": map[string]any{
				"name": name, "id": id, "response": map[string]any{"result": m.text()},
			}})
		default:
			add("user", geminiParts(m)...)
		}
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("the request has no user message")
	}
	req := map[string]any{"contents": contents}
	if len(system) > 0 {
		req["systemInstruction"] = map[string]any{"role": "user", "parts": system}
	}

	gen := map[string]any{}
	limit := chat.MaxTokens
	if limit > 0 {
		if opt.maxOutput > 0 && limit > opt.maxOutput {
			limit = opt.maxOutput
		}
		gen["maxOutputTokens"] = limit
	}
	if chat.Temperature != nil {
		gen["temperature"] = *chat.Temperature
	}
	if chat.TopP != nil {
		gen["topP"] = *chat.TopP
	}
	if s := chat.stops(); len(s) > 0 {
		gen["stopSequences"] = s
	}
	if tc := geminiThinking(chat.Model, opt.effort, limit); tc != nil {
		gen["thinkingConfig"] = tc
	}

	mode, name := chat.toolChoice()
	var decls []any
	for _, t := range chat.Tools {
		if (t.Type != "" && t.Type != "function") || t.Function.Name == "" {
			continue
		}
		d := map[string]any{"name": t.Function.Name, "description": t.Function.Description}
		if len(t.Function.Parameters) > 0 && string(t.Function.Parameters) != "null" {
			if opt.jsonSchema {
				d["parametersJsonSchema"] = geminiToolSchema(t.Function.Parameters)
			} else {
				d["parameters"] = cleanSchema(t.Function.Parameters)
			}
		}
		decls = append(decls, d)
	}
	if len(decls) > 0 && mode != "none" {
		req["tools"] = []any{map[string]any{"functionDeclarations": decls}}
		cfg := map[string]any{}
		switch {
		case name != "":
			cfg["mode"], cfg["allowedFunctionNames"] = "ANY", []string{name}
		case mode == "required":
			cfg["mode"] = "ANY"
		case opt.validated:
			cfg["mode"] = "VALIDATED"
		case mode == "auto":
			cfg["mode"] = "AUTO"
		}
		if len(cfg) > 0 {
			req["toolConfig"] = map[string]any{"functionCallingConfig": cfg}
		}
	}
	// A JSON answer and function calling don't go together: the backend
	// refuses a JSON mime type next to tools (400 on Gemini 2.x), so a
	// request with tools gets neither. The schema goes as given, in
	// responseJsonSchema (full JSON Schema, Gemini 2.5 and later), not
	// reduced to the OpenAPI subset of responseSchema, which lost every
	// constraint but enum. As raw JSON it also keeps the schema's key order,
	// which the model follows when it writes the object.
	if req["tools"] == nil {
		if wantJSON, _, schema := chat.jsonSchema(); wantJSON {
			gen["responseMimeType"] = "application/json"
			if len(schema) > 0 && string(schema) != "null" {
				gen["responseJsonSchema"] = schema
			}
		}
	}
	if len(gen) > 0 {
		req["generationConfig"] = gen
	}
	return req, nil
}

// geminiThinking is the thinkingConfig for a reasoning effort, or nil to
// leave the model's default (no effort asked, an effort not known, a model
// that does not think or whose id already names its level).
//
// Gemini 3 and later take a thinkingLevel (Gemini 4 refuses a budget), and
// not every level: medium goes to Flash, 3.1 Pro and Gemini 4 only (3 Pro
// had low and high), the rest are asked high. Minimal goes nowhere: 3.7
// and 3.8 Flash refuse it, and Gemini CLI's older Flash ids may be served
// by those. Gemini 3 cannot stop thinking, so effort "none" is its lowest
// level, low, which every Gemini 3 text model takes.
//
// Gemini 2.5 takes a thinkingBudget, within each model's bounds: Pro thinks
// at least 128 tokens and at most 32768, Flash 0 (off) to 24576, Flash-Lite
// 0 or 512 to 24576. A budget is kept under max_tokens, which counts the
// thoughts as well: half of it is left for the answer.
//
// Thoughts are asked back (includeThoughts) whenever the request asked for
// reasoning, so they reach the buyer as reasoning_content; not when it
// turned reasoning off.
func geminiThinking(model, effort string, maxOutput uint64) map[string]any {
	effort = strings.ToLower(strings.TrimSpace(effort))
	off := false
	switch effort {
	case "none", "off", "disabled":
		off = true
	case "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return nil
	}
	m := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	major, minor, ok := geminiVersion(m)
	if !ok {
		return nil
	}
	for _, kind := range []string{"image", "tts", "embedding", "live", "audio"} {
		if strings.Contains(m, kind) {
			return nil
		}
	}
	tc := map[string]any{}
	switch {
	case major >= 3:
		// Antigravity's gemini-3-pro-low and the like: the id picks the level.
		if !geminiLevelInID(m) {
			level := "high"
			switch effort {
			case "none", "off", "disabled", "minimal", "low":
				level = "low"
			case "medium":
				if strings.Contains(m, "flash") || strings.HasPrefix(m, "gemini-3.1-pro") || major >= 4 {
					level = "medium"
				}
			}
			tc["thinkingLevel"] = level
		}
	case major == 2 && minor >= 5:
		offBudget, least, most := 0, 1, 24576
		switch {
		case strings.Contains(m, "pro"):
			offBudget, least, most = 128, 128, 32768
		case strings.Contains(m, "flash-lite"):
			least = 512
		}
		budget := most
		switch effort {
		case "none", "off", "disabled", "minimal":
			budget = offBudget
		case "low":
			budget = 1024
		case "medium":
			budget = 8192
		}
		if budget > 0 {
			if maxOutput > 0 && uint64(budget) >= maxOutput {
				budget = int(maxOutput / 2)
			}
			budget = min(max(budget, least), most)
		}
		tc["thinkingBudget"] = budget
		off = off || budget == 0
	default:
		return nil
	}
	if !off {
		tc["includeThoughts"] = true
	}
	if len(tc) == 0 {
		return nil
	}
	return tc
}

// geminiVersion reads the version of a Gemini model id: gemini-2.5-pro is
// 2.5, gemini-4-argon 4.0.
func geminiVersion(model string) (major, minor int, ok bool) {
	rest, found := strings.CutPrefix(model, "gemini-")
	if !found {
		return 0, 0, false
	}
	version, _, _ := strings.Cut(rest, "-")
	majorText, minorText, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return 0, 0, false
	}
	if minorText != "" {
		if minor, err = strconv.Atoi(minorText); err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
}

// geminiLevelInID tells an id that names its thinking level, as
// Antigravity's variants do (gemini-3.1-pro-high, gemini-3.8-flash-tiered).
func geminiLevelInID(model string) bool {
	for _, suffix := range []string{"-minimal", "-low", "-medium", "-high", "-xhigh", "-tiered"} {
		if strings.HasSuffix(model, suffix) {
			return true
		}
	}
	return false
}

// geminiCalls are an assistant message's tool calls as functionCall parts,
// their ids without the signature signedID put in them. Gemini signs the
// first call of a step: a step with a signature restored sends the others
// as they came, and a step with none gets the placeholder on each call, as
// history the model did not sign (another seller's answer, or a signature
// this one no longer keeps).
func geminiCalls(calls []chatToolCall, callName map[string]string, opt geminiOptions) []any {
	ids, sigs := make([]string, len(calls)), make([]string, len(calls))
	signed := false
	for i, tc := range calls {
		ids[i], sigs[i] = unsignedID(tc.ID, opt.signatures)
		if opt.dropSignatures {
			sigs[i] = ""
		}
		signed = signed || sigs[i] != ""
	}
	var out []any
	for i, tc := range calls {
		callName[ids[i]] = tc.Function.Name
		fc := map[string]any{"functionCall": map[string]any{"name": tc.Function.Name, "args": rawObject(tc.Function.Arguments), "id": ids[i]}}
		if opt.signFunctionCalls {
			switch {
			case sigs[i] != "":
				fc["thoughtSignature"] = sigs[i]
			case !signed:
				fc["thoughtSignature"] = skipThoughtSignature
			}
		}
		out = append(out, fc)
	}
	return out
}

func geminiParts(m *chatMessage) []any {
	var out []any
	for _, p := range m.parts() {
		switch p.Type {
		case "text", "":
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text})
			}
		case "image_url":
			if mt, data, ok := dataURL(p.ImageURL.URL); ok {
				out = append(out, map[string]any{"inlineData": map[string]any{"mimeType": mt, "data": data}})
			}
		}
	}
	return out
}

// geminiToolSchema is a tool's parameters for parametersJsonSchema, which
// takes JSON Schema whole: unions keep every branch (cleanSchema keeps the
// first), and the constraints stay. The root alone has to be an object;
// one that is a union, has no type or another type is folded into one
// (apicompat.ObjectRootSchema). Parameters already fit go byte for byte,
// in their own key order, which the model follows when it writes the args.
func geminiToolSchema(raw json.RawMessage) json.RawMessage {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil || root == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	_, any1 := root["anyOf"]
	_, one := root["oneOf"]
	_, all := root["allOf"]
	if string(root["type"]) == `"object"` && !any1 && !one && !all {
		return raw
	}
	return apicompat.ObjectRootSchema(raw)
}

// schemaKeys are the JSON Schema keywords the backend takes; it refuses
// requests whose tool parameters use others.
var schemaKeys = map[string]bool{"type": true, "description": true, "properties": true, "required": true, "items": true, "enum": true, "title": true}

// cleanSchema reduces a JSON Schema to what the backend takes.
func cleanSchema(raw json.RawMessage) any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return map[string]any{"type": "object"}
	}
	return cleanSchemaValue(v)
}

func cleanSchemaValue(v any) any {
	in, ok := v.(map[string]any)
	if !ok {
		return map[string]any{"type": "string"}
	}
	// One of several shapes: take the first that is not null.
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if alts, ok := in[k].([]any); ok {
			for _, a := range alts {
				if am, ok := a.(map[string]any); ok && am["type"] != "null" {
					merged := map[string]any{}
					for kk, vv := range in {
						merged[kk] = vv
					}
					delete(merged, k)
					for kk, vv := range am {
						merged[kk] = vv
					}
					in = merged
					break
				}
			}
		}
	}
	out := map[string]any{}
	desc, _ := in["description"].(string)
	if c, ok := in["const"]; ok {
		in["enum"] = []any{c}
	}
	for k, val := range in {
		if !schemaKeys[k] {
			continue
		}
		switch k {
		case "type":
			switch t := val.(type) {
			case string:
				out[k] = strings.ToLower(t)
			case []any: // ["string", "null"]
				for _, one := range t {
					if s, _ := one.(string); s != "" && s != "null" {
						out[k] = strings.ToLower(s)
						break
					}
				}
				if !strings.Contains(desc, "(nullable)") {
					desc = strings.TrimSpace(desc + " (nullable)")
				}
			}
		case "properties":
			props, _ := val.(map[string]any)
			cleaned := map[string]any{}
			for name, p := range props {
				cleaned[name] = cleanSchemaValue(p)
			}
			out[k] = cleaned
		case "items":
			if list, ok := val.([]any); ok && len(list) > 0 {
				val = list[0]
			}
			out[k] = cleanSchemaValue(val)
		case "enum":
			var vals []any
			list, _ := val.([]any)
			for _, e := range list {
				vals = append(vals, fmt.Sprint(e))
			}
			out[k] = vals
		case "description":
		default:
			out[k] = val
		}
	}
	if desc != "" {
		out["description"] = desc
	}
	if _, ok := out["type"]; !ok {
		// The Schema object needs a type on every node: the one its
		// keywords say (properties an object, items an array), else a
		// string, the value that carries anything (and the only type an
		// enum here can have, its values written as strings).
		switch {
		case out["properties"] != nil:
			out["type"] = "object"
		case out["items"] != nil:
			out["type"] = "array"
		default:
			out["type"] = "string"
		}
	}
	props, _ := out["properties"].(map[string]any)
	if req, ok := out["required"].([]any); ok {
		var kept []any
		for _, r := range req {
			if name, _ := r.(string); props[name] != nil {
				kept = append(kept, r)
			}
		}
		if len(kept) > 0 {
			out["required"] = kept
		} else {
			delete(out, "required")
		}
	}
	if out["type"] == "array" && out["items"] == nil {
		out["items"] = map[string]any{"type": "string"}
	}
	// Only an array may have items: the backend refuses them on any other
	// type, which a node can end up with after anyOf or a type list picked
	// another shape than the one the items were written for.
	if out["type"] != "array" {
		delete(out, "items")
	}
	return out
}

// geminiStream turns generateContent stream answers into chunks.
type geminiStream struct {
	out      *chunks
	prompt   uint64
	output   uint64
	cached   uint64 // prompt tokens served from Gemini's context cache
	thoughts uint64 // reasoning tokens, of which the stream shows a summary at most
	reason   string
	blocked  bool // the prompt was refused: there is no candidate, and no finishReason
	received bool
	content  bool // the answer has text or a tool call (thoughts alone are not an answer)
	// sigs keeps the long thought signatures the calls' ids name.
	sigs *sigStore
	// pending is a call whose args are coming in pieces (gemini_toolcall.go).
	pending *pendingCall
}

func (s *geminiStream) event(data []byte) error {
	// Code Assist wraps each answer in {"response": …}.
	var wrap struct {
		Response json.RawMessage `json:"response"`
		Error    *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &wrap) == nil {
		if wrap.Error != nil {
			status := wrap.Error.Code
			if status < 400 {
				status = http.StatusBadGateway
			}
			se := &node.StatusError{Status: status, Body: clip(wrap.Error.Message, 300)}
			if status == http.StatusTooManyRequests {
				se.RetryAt = limitReset(nil, string(data), timeNow())
			}
			return se
		}
		if len(wrap.Response) > 0 {
			data = wrap.Response
		}
	}
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text             string      `json:"text"`
					Thought          bool        `json:"thought"`
					ThoughtSignature string      `json:"thoughtSignature"`
					FunctionCall     *geminiCall `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		UsageMetadata *struct {
			PromptTokenCount        uint64 `json:"promptTokenCount"`
			CandidatesTokenCount    uint64 `json:"candidatesTokenCount"`
			ThoughtsTokenCount      uint64 `json:"thoughtsTokenCount"`
			CachedContentTokenCount uint64 `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil
	}
	s.received = true
	s.blocked = s.blocked || r.PromptFeedback.BlockReason != ""
	if u := r.UsageMetadata; u != nil {
		s.prompt, s.output = u.PromptTokenCount, u.CandidatesTokenCount+u.ThoughtsTokenCount
		s.cached, s.thoughts = min(u.CachedContentTokenCount, u.PromptTokenCount), u.ThoughtsTokenCount
	}
	if len(r.Candidates) == 0 {
		return nil
	}
	c := r.Candidates[0]
	for _, p := range c.Content.Parts {
		if p.FunctionCall != nil {
			if err := s.functionCall(p.FunctionCall, p.ThoughtSignature); err != nil {
				return err
			}
			continue
		}
		// A signature on a text or a thought has nowhere to go in a chat
		// answer, and the backend does not need it back: it goes.
		if p.Text == "" {
			continue
		}
		if err := s.flushCall(); err != nil {
			return err
		}
		switch {
		case p.Thought:
			if err := s.out.reasoning(p.Text); err != nil {
				return err
			}
		default:
			s.content = s.content || p.Text != ""
			if err := s.out.text(p.Text); err != nil {
				return err
			}
		}
	}
	if c.FinishReason != "" {
		s.reason = c.FinishReason
	}
	return nil
}

// end closes the answer once the stream is over: the token counts come with
// the last answers, so there is nothing to close before.
func (s *geminiStream) end() error {
	if err := s.flushCall(); err != nil {
		return err
	}
	if !s.received {
		return fmt.Errorf("the provider answered with an empty stream")
	}
	reason := "stop"
	switch s.reason {
	case "":
		// Every answer ends with a finishReason: without one the stream was
		// cut on the way, unless the prompt itself was refused.
		if !s.blocked {
			return fmt.Errorf("the stream ended before the answer did")
		}
		reason = "content_filter"
	case "MAX_TOKENS":
		reason = "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		reason = "content_filter"
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		// The model wrote a tool call the backend could not read and gave
		// nothing else: a failure worth another try, not an empty answer.
		if !s.content {
			return &node.StatusError{Status: http.StatusBadGateway, Body: "the model server could not read the model's tool call (" + s.reason + ")"}
		}
	}
	// Gemini 3 sometimes ends with STOP having written nothing, most often
	// on a long context. That is no answer to bill as one: a 502 lets the
	// seller try again or the buyer go to another node.
	if reason == "stop" && !s.content {
		return &node.StatusError{Status: http.StatusBadGateway, Body: "the model ended its answer (" + s.reason + ") without any text or tool call"}
	}
	return s.out.finishDetailed(reason, s.prompt, s.output, s.cached, s.thoughts)
}

// streamGemini posts an enveloped request and writes the answer as chunks.
func streamGemini(ctx context.Context, url string, header http.Header, envelope map[string]any, model string, sigs *sigStore, emit func([]byte) error) error {
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	st := &geminiStream{out: newChunks(model, emit), sigs: sigs}
	if err := postSSE(ctx, url, header, body, st.event); err != nil {
		return err
	}
	return st.end()
}
