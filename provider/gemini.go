package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"sharetoken/node"
)

// Google's Code Assist backend (the Gemini CLI's and Antigravity's) speaks
// the native Gemini API inside an envelope: the chat-completions request is
// rewritten into generateContent form, and the stream back into chunks.

// geminiOptions are the differences between the backends.
type geminiOptions struct {
	// validated asks for validated function calling, which Antigravity wants
	// whenever a request has tools.
	validated bool
	// signFunctionCalls adds the placeholder thought signature the backend
	// wants on function calls replayed from the conversation.
	signFunctionCalls bool
	maxOutput         uint64
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
			parts := geminiParts(m)
			for _, tc := range m.ToolCalls {
				callName[tc.ID] = tc.Function.Name
				fc := map[string]any{"functionCall": map[string]any{"name": tc.Function.Name, "args": rawObject(tc.Function.Arguments), "id": tc.ID}}
				if opt.signFunctionCalls {
					fc["thoughtSignature"] = skipThoughtSignature
				}
				parts = append(parts, fc)
			}
			add("model", parts...)
		case "tool":
			name := callName[m.ToolCallID]
			if name == "" {
				name = m.Name
			}
			add("user", map[string]any{"functionResponse": map[string]any{
				"name": name, "id": m.ToolCallID, "response": map[string]any{"result": m.text()},
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
	if limit := chat.MaxTokens; limit > 0 {
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
	if wantJSON, _, schema := chat.jsonSchema(); wantJSON {
		gen["responseMimeType"] = "application/json"
		if len(schema) > 0 {
			gen["responseSchema"] = cleanSchema(schema)
		}
	}
	if len(gen) > 0 {
		req["generationConfig"] = gen
	}

	mode, name := chat.toolChoice()
	var decls []any
	for _, t := range chat.Tools {
		if (t.Type != "" && t.Type != "function") || t.Function.Name == "" {
			continue
		}
		d := map[string]any{"name": t.Function.Name, "description": t.Function.Description}
		if len(t.Function.Parameters) > 0 && string(t.Function.Parameters) != "null" {
			d["parameters"] = cleanSchema(t.Function.Parameters)
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
	return req, nil
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
		if _, ok := out["properties"]; ok {
			out["type"] = "object"
		} else {
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
	return out
}

// geminiStream turns generateContent stream answers into chunks.
type geminiStream struct {
	out      *chunks
	prompt   uint64
	output   uint64
	reason   string
	received bool
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
			return &node.StatusError{Status: status, Body: clip(wrap.Error.Message, 300)}
		}
		if len(wrap.Response) > 0 {
			data = wrap.Response
		}
	}
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
						ID   string          `json:"id"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     uint64 `json:"promptTokenCount"`
			CandidatesTokenCount uint64 `json:"candidatesTokenCount"`
			ThoughtsTokenCount   uint64 `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil
	}
	s.received = true
	if u := r.UsageMetadata; u != nil {
		s.prompt, s.output = u.PromptTokenCount, u.CandidatesTokenCount+u.ThoughtsTokenCount
	}
	if len(r.Candidates) == 0 {
		return nil
	}
	c := r.Candidates[0]
	for _, p := range c.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			id := p.FunctionCall.ID
			if id == "" {
				id = "call_" + randomHex(12)
			}
			args := string(p.FunctionCall.Args)
			if args == "" || args == "null" {
				args = "{}"
			}
			if _, err := s.out.toolStart(id, p.FunctionCall.Name, args); err != nil {
				return err
			}
		case p.Thought:
			if err := s.out.reasoning(p.Text); err != nil {
				return err
			}
		default:
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
	if !s.received {
		return fmt.Errorf("the provider answered with an empty stream")
	}
	reason := "stop"
	switch s.reason {
	case "MAX_TOKENS":
		reason = "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		reason = "content_filter"
	}
	return s.out.finish(reason, s.prompt, s.output)
}

// streamGemini posts an enveloped request and writes the answer as chunks.
func streamGemini(ctx context.Context, url string, header http.Header, envelope map[string]any, model string, emit func([]byte) error) error {
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	st := &geminiStream{out: newChunks(model, emit)}
	if err := postSSE(ctx, url, header, body, st.event); err != nil {
		return err
	}
	return st.end()
}
