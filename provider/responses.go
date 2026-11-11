package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"sharetoken/node"
	"sharetoken/protocol"
)

// The ChatGPT (Codex) backend speaks the OpenAI Responses API: the
// chat-completions request is rewritten into it, and its stream events back
// into chunks.

// The tool name "python" is reserved by the backend.
const (
	reservedTool      = "python"
	reservedToolAlias = "python__st"
)

func toolNameOut(name string) string {
	if name == reservedTool {
		return reservedToolAlias
	}
	return name
}

func toolNameIn(name string) string {
	if name == reservedToolAlias {
		return reservedTool
	}
	return name
}

// responsesRequest turns a chat-completions request into a Responses request
// as the Codex backend takes it: no sampling settings, no output limit, never
// stored.
func responsesRequest(chat *chatRequest) (map[string]any, error) {
	var instructions []string
	var input []any
	for i := range chat.Messages {
		m := &chat.Messages[i]
		switch m.Role {
		case "system":
			if t := m.text(); t != "" {
				instructions = append(instructions, t)
			}
		case "assistant":
			if t := m.text(); t != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": t}}})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": toolNameOut(tc.Function.Name), "arguments": args})
			}
		case "tool":
			out := m.text()
			if out == "" {
				out = "(empty)"
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": out})
		default: // user, developer
			role := m.Role
			if role != "developer" {
				role = "user"
			}
			var content []any
			for _, p := range m.parts() {
				switch p.Type {
				case "text", "":
					content = append(content, map[string]any{"type": "input_text", "text": p.Text})
				case "image_url":
					if p.ImageURL.URL != "" {
						content = append(content, map[string]any{"type": "input_image", "image_url": p.ImageURL.URL})
					}
				}
			}
			if len(content) == 0 {
				content = []any{map[string]any{"type": "input_text", "text": ""}}
			}
			input = append(input, map[string]any{"type": "message", "role": role, "content": content})
		}
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("the request has no user message")
	}
	req := map[string]any{
		"model":        chat.Model,
		"instructions": strings.Join(instructions, "\n\n"),
		"input":        input,
		"stream":       true,
		"store":        false,
		"include":      []string{"reasoning.encrypted_content"},
	}
	mode, name := chat.toolChoice()
	if len(chat.Tools) > 0 && mode != "none" {
		var tools []any
		for _, t := range chat.Tools {
			if (t.Type != "" && t.Type != "function") || t.Function.Name == "" {
				continue
			}
			params := t.Function.Parameters
			if len(params) == 0 || string(params) == "null" {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{"type": "function", "name": toolNameOut(t.Function.Name),
				"description": t.Function.Description, "parameters": params, "strict": false})
		}
		if len(tools) > 0 {
			req["tools"] = tools
			switch {
			case name != "":
				req["tool_choice"] = map[string]any{"type": "function", "name": toolNameOut(name)}
			case mode != "":
				req["tool_choice"] = mode
			}
		}
	}
	if wantJSON, name, schema := chat.jsonSchema(); wantJSON {
		if len(schema) > 0 {
			if name == "" {
				name = "answer"
			}
			req["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": name, "schema": schema}}
		} else {
			req["text"] = map[string]any{"format": map[string]any{"type": "json_object"}}
		}
	}
	return req, nil
}

// responsesStream turns Responses stream events into chunks. limit is the
// output the job may produce: the Codex backend takes no limit, so the stream
// is cut here when the answer outgrows what the buyer reserved for.
type responsesStream struct {
	out     *chunks
	tool    map[int]int // output index → tool call index
	limit   uint64
	text    strings.Builder
	done    bool
	started bool // something was sent on
}

// errOutputLimit ends a stream that outgrew its limit.
var errOutputLimit = fmt.Errorf("output limit reached")

func (s *responsesStream) event(data []byte) error {
	var ev struct {
		Type        string `json:"type"`
		Delta       string `json:"delta"`
		OutputIndex int    `json:"output_index"`
		Item        struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		} `json:"item"`
		Response struct {
			Usage struct {
				InputTokens  uint64 `json:"input_tokens"`
				OutputTokens uint64 `json:"output_tokens"`
			} `json:"usage"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil
	}
	count := func(d string) error {
		s.started = true
		s.text.WriteString(d)
		if s.limit > 0 && protocol.EstimateTokens(s.text.String()) > s.limit {
			return errOutputLimit
		}
		return nil
	}
	switch ev.Type {
	case "response.output_text.delta":
		if err := s.out.text(ev.Delta); err != nil {
			return err
		}
		return count(ev.Delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if err := s.out.reasoning(ev.Delta); err != nil {
			return err
		}
		return count(ev.Delta)
	case "response.output_item.added":
		if ev.Item.Type == "function_call" {
			i, err := s.out.toolStart(ev.Item.CallID, toolNameIn(ev.Item.Name), "")
			s.tool[ev.OutputIndex], s.started = i, true
			return err
		}
	case "response.function_call_arguments.delta":
		if i, ok := s.tool[ev.OutputIndex]; ok {
			if err := s.out.toolArgs(i, ev.Delta); err != nil {
				return err
			}
			return count(ev.Delta)
		}
	case "response.completed", "response.done", "response.incomplete":
		s.done = true
		reason := "stop"
		switch ev.Response.IncompleteDetails.Reason {
		case "max_output_tokens":
			reason = "length"
		case "content_filter":
			reason = "content_filter"
		}
		return s.out.finish(reason, ev.Response.Usage.InputTokens, ev.Response.Usage.OutputTokens)
	case "response.failed", "error":
		msg := ev.Response.Error.Message
		if msg == "" {
			msg = ev.Message
		}
		return &node.StatusError{Status: http.StatusBadGateway, Body: clip(ev.Type+": "+msg, 300)}
	}
	return nil
}

// streamResponses posts a Responses request and writes the answer as chunks.
func streamResponses(ctx context.Context, url string, header http.Header, req map[string]any, model string, limit, estPrompt uint64, emit func([]byte) error) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := &responsesStream{out: newChunks(model, emit), tool: map[int]int{}, limit: limit}
	err = postSSE(ctx, url, header, body, st.event)
	switch {
	case err == errOutputLimit:
		// The buyer gets what its reservation covers, marked as cut short.
		return st.out.finish("length", estPrompt, protocol.EstimateTokens(st.text.String()))
	case err != nil:
		return err
	case !st.done && st.started:
		return st.out.finish("stop", estPrompt, protocol.EstimateTokens(st.text.String()))
	case !st.done:
		return fmt.Errorf("the stream ended before the response did")
	}
	return nil
}
