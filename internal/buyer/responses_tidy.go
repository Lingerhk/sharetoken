package buyer

import (
	"bytes"
	"encoding/json"
)

// A Responses client other than Codex knows the API's own stream events and
// no others: one it does not know (the ChatGPT backend's codex.rate_limits,
// keepalive, response.metadata, …) makes a typed client give up on the whole
// turn. Such a client gets the standard events only; Codex gets every event
// as it came, since it reads its limits from the backend's own (after magpie's
// responses_enum.go).
//
// Some upstreams give an output item a new id in every event (GitHub
// Copilot's /responses: the added, each delta, the done and the completed
// response's output each carry another). A client starts an item by the id
// of its output_item.added and finishes it by the done's, so the item is
// shown twice or never finished; each item's events are given the id it was
// added with (magpie's itemids.go).

// responsesEnum is the OpenAI Responses stream event enum.
var responsesEnum = map[string]bool{
	"response.created": true, "response.in_progress": true, "response.completed": true,
	"response.failed": true, "response.incomplete": true, "response.queued": true,
	"response.output_item.added": true, "response.output_item.done": true,
	"response.content_part.added": true, "response.content_part.done": true,
	"response.output_text.delta": true, "response.output_text.done": true,
	"response.output_text.annotation.added": true,
	"response.audio.delta":                  true, "response.audio.done": true,
	"response.audio.transcript.delta": true, "response.audio.transcript.done": true,
	"response.refusal.delta": true, "response.refusal.done": true,
	"response.function_call_arguments.delta": true, "response.function_call_arguments.done": true,
	"response.file_search_call.in_progress": true, "response.file_search_call.searching": true,
	"response.file_search_call.completed":  true,
	"response.web_search_call.in_progress": true, "response.web_search_call.searching": true,
	"response.web_search_call.completed":    true,
	"response.reasoning_summary_part.added": true, "response.reasoning_summary_part.done": true,
	"response.reasoning_summary_text.delta": true, "response.reasoning_summary_text.done": true,
	"response.reasoning_text.delta": true, "response.reasoning_text.done": true,
	"response.image_generation_call.completed": true, "response.image_generation_call.generating": true,
	"response.image_generation_call.in_progress": true, "response.image_generation_call.partial_image": true,
	"response.mcp_call_arguments.delta": true, "response.mcp_call_arguments.done": true,
	"response.mcp_call.completed": true, "response.mcp_call.failed": true, "response.mcp_call.in_progress": true,
	"response.mcp_list_tools.completed": true, "response.mcp_list_tools.failed": true,
	"response.mcp_list_tools.in_progress":        true,
	"response.code_interpreter_call.in_progress": true, "response.code_interpreter_call.interpreting": true,
	"response.code_interpreter_call.completed": true, "response.code_interpreter_call_code.delta": true,
	"response.code_interpreter_call_code.done": true,
	"response.custom_tool_call_input.delta":    true, "response.custom_tool_call_input.done": true,
	"error": true,
}

// respTidy keeps a Responses stream to the standard events, each item under
// the id it was added with. The zero value is ready to use.
type respTidy struct {
	ids map[int64]respItemID // output_index → the item as added
}

type respItemID struct{ id, typ string }

// tidyResult says what becomes of one event.
type tidyResult int

const (
	tidyKeep    tidyResult = iota // send the payload (possibly rewritten)
	tidyDrop                      // send nothing
	tidyComment                   // send an SSE comment instead: a heartbeat
)

// event is the payload to send for one event, and what to do with it.
func (t *respTidy) event(payload []byte) ([]byte, tidyResult) {
	var head struct {
		Type        string           `json:"type"`
		OutputIndex *int64           `json:"output_index"`
		ItemID      *string          `json:"item_id"`
		Item        *json.RawMessage `json:"item"`
	}
	if json.Unmarshal(payload, &head) != nil || head.Type == "" {
		return payload, tidyKeep
	}
	switch {
	case head.Type == "keepalive":
		return nil, tidyComment
	case head.Type == "response.done":
		// What some upstreams end with; the enum's name for it is
		// response.completed (or incomplete, by its status).
		return t.ended(renamedDone(payload)), tidyKeep
	case !responsesEnum[head.Type]:
		return nil, tidyDrop
	case head.Type == "response.output_item.added":
		t.added(head.OutputIndex, head.Item)
		return payload, tidyKeep
	case head.Type == "response.completed" || head.Type == "response.incomplete" || head.Type == "response.failed":
		return t.ended(payload), tidyKeep
	case head.OutputIndex == nil:
		return payload, tidyKeep
	}
	was, ok := t.ids[*head.OutputIndex]
	if !ok {
		return payload, tidyKeep
	}
	var itemID string
	if head.Item != nil {
		var it struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(*head.Item, &it)
		if it.ID != "" && it.ID != was.id && (was.typ == "" || it.Type == was.typ) {
			itemID = it.ID
		}
	}
	if (head.ItemID == nil || *head.ItemID == was.id) && itemID == "" {
		return payload, tidyKeep
	}
	m, ok := decodeEvent(payload)
	if !ok {
		return payload, tidyKeep
	}
	if head.ItemID != nil {
		m["item_id"] = was.id
	}
	if it, _ := m["item"].(map[string]any); it != nil && itemID != "" {
		it["id"] = was.id
	}
	return encodeEvent(m, payload), tidyKeep
}

func (t *respTidy) added(at *int64, item *json.RawMessage) {
	if at == nil || item == nil {
		return
	}
	var it struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if json.Unmarshal(*item, &it) != nil || it.ID == "" {
		return
	}
	if t.ids == nil {
		t.ids = map[int64]respItemID{}
	}
	t.ids[*at] = respItemID{it.ID, it.Type}
}

// ended is the terminal event with each item of its output under the id it
// was added with.
func (t *respTidy) ended(payload []byte) []byte {
	if len(t.ids) == 0 {
		return payload
	}
	m, ok := decodeEvent(payload)
	if !ok {
		return payload
	}
	res, _ := m["response"].(map[string]any)
	out, _ := res["output"].([]any)
	changed := false
	for i, v := range out {
		it, _ := v.(map[string]any)
		was, ok := t.ids[int64(i)]
		if it == nil || !ok {
			continue
		}
		if id, has := it["id"].(string); has && id != was.id && (was.typ == "" || it["type"] == was.typ) {
			it["id"], changed = was.id, true
		}
	}
	if !changed {
		return payload
	}
	return encodeEvent(m, payload)
}

// renamedDone is a response.done event under the enum's name for it.
func renamedDone(payload []byte) []byte {
	m, ok := decodeEvent(payload)
	if !ok {
		return payload
	}
	m["type"] = "response.completed"
	if res, _ := m["response"].(map[string]any); res != nil && res["status"] == "incomplete" {
		m["type"] = "response.incomplete"
	}
	return encodeEvent(m, payload)
}

// decodeEvent reads an event as a map, its numbers as they were written.
func decodeEvent(payload []byte) (map[string]any, bool) {
	d := json.NewDecoder(bytes.NewReader(payload))
	d.UseNumber()
	var m map[string]any
	if d.Decode(&m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// encodeEvent writes m back, or gives the original payload when it cannot.
func encodeEvent(m map[string]any, orig []byte) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(m) != nil {
		return orig
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}
