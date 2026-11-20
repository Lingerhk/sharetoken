package buyer

import (
	"encoding/json"
	"net/http"

	"sharetoken/internal/protocol"
)

// messages serves POST /v1/messages (Anthropic Messages, Claude Code).
func (p *Proxy) messages(w http.ResponseWriter, r *http.Request) {
	const api = protocol.APIMessages
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, api, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &head); err != nil || head.Model == "" {
		writeAPIError(w, api, http.StatusBadRequest, "body must be a JSON Messages request with a model", protocol.CodeBadRequest)
		return
	}
	if len(head.Messages) == 0 {
		writeAPIError(w, api, http.StatusBadRequest, "messages must be a non-empty array", protocol.CodeBadRequest)
		return
	}
	req := p.request(r, api, head.Model, body)
	if head.Stream {
		p.stream(w, r, req)
		return
	}
	p.aggregate(w, r, req)
}

// countTokens serves POST /v1/messages/count_tokens with a local estimate:
// asking a node would cost a job.
func (p *Proxy) countTokens(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, protocol.APIMessages, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	n, err := protocol.EstimatePromptTokens(protocol.APIMessages, body)
	if err != nil {
		writeAPIError(w, protocol.APIMessages, http.StatusBadRequest, "body must be a JSON Messages request", protocol.CodeBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]uint64{"input_tokens": n})
}

// responsesHead is what the proxy reads of a Responses request.
type responsesHead struct {
	Model              string          `json:"model"`
	Stream             bool            `json:"stream"`
	Input              json.RawMessage `json:"input"`
	PreviousResponseID string          `json:"previous_response_id"`
}

func (h *responsesHead) check() string {
	switch {
	case h.Model == "":
		return "body must be a JSON Responses request with a model"
	case len(h.Input) == 0 || string(h.Input) == "null":
		return "input is required"
	}
	return ""
}

// responses serves POST /v1/responses (OpenAI Responses, Codex).
func (p *Proxy) responses(w http.ResponseWriter, r *http.Request) {
	const api = protocol.APIResponses
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, api, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head responsesHead
	if err := json.Unmarshal(body, &head); err != nil {
		head = responsesHead{}
	}
	if msg := head.check(); msg != "" {
		writeAPIError(w, api, http.StatusBadRequest, msg, protocol.CodeBadRequest)
		return
	}
	scope := prevScope(r)
	var prior *wsTurn
	if head.PreviousResponseID != "" {
		if prior = p.prev.get(scope, head.PreviousResponseID); prior == nil {
			writePrevNotFound(w, head.PreviousResponseID)
			return
		}
	}
	// What is kept of this answer for a later previous_response_id: nothing
	// when the client asks that nothing be stored.
	keep := !storeOff(body)
	var input []json.RawMessage
	var err error
	switch {
	case prior != nil:
		body, input, err = prevExpand(body, prior)
	case keep:
		input, err = inputItems(head.Input)
	}
	if err != nil {
		writeAPIError(w, api, http.StatusBadRequest, "input: "+err.Error(), protocol.CodeBadRequest)
		return
	}
	req := p.request(r, api, head.Model, body)
	if head.Stream {
		var watch turnWatch
		see := watch.see
		if !keep {
			see = nil
		}
		if err := p.streamWith(w, r, req, see); err == nil && keep {
			p.prev.put(scope, watch.turn(input))
		}
		return
	}
	answer, err := p.aggregateBody(w, r, req)
	if err == nil && keep {
		p.prev.put(scope, answerTurn(answer, input))
	}
}

// answerTurn is what is kept of a whole Responses answer, after input.
func answerTurn(answer []byte, input []json.RawMessage) *wsTurn {
	var res struct {
		ID     string            `json:"id"`
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(answer, &res) != nil {
		return nil
	}
	w := turnWatch{id: res.ID, output: res.Output}
	return w.turn(input)
}

// compact serves POST /v1/responses/compact: Codex asks for its
// conversation to be compacted, and gets a response whose output is one
// compaction item. A conversation whose compactions keep failing is told so
// at once for a while (compactFails).
func (p *Proxy) compact(w http.ResponseWriter, r *http.Request) {
	const api = protocol.APIResponses
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, api, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head responsesHead
	if err := json.Unmarshal(body, &head); err != nil {
		head = responsesHead{}
	}
	msg := head.check()
	if msg == "" && head.PreviousResponseID != "" {
		msg = "previous_response_id is not supported for compaction: send the whole conversation"
	}
	if msg != "" {
		writeAPIError(w, api, http.StatusBadRequest, msg, protocol.CodeBadRequest)
		return
	}
	key := compactKey(body)
	if msg, held := p.compactFails.held(key, head.Model); held {
		writeAPIError(w, api, http.StatusBadRequest, msg, protocol.CodeBadRequest)
		return
	}
	req := p.request(r, api, head.Model, body)
	req.Header[protocol.HeaderOp] = protocol.OpCompact
	if _, err := p.aggregateBody(w, r, req); err != nil {
		p.compactFails.failed(key, err)
		return
	}
	p.compactFails.done(key)
}

// models serves GET /v1/models: in OpenAI's list format with Anthropic's
// fields as well (both read their own, modelObject), or as Codex's model
// manifest when Codex asks (?client_version=).
func (p *Proxy) models(w http.ResponseWriter, r *http.Request) {
	ms, err := p.Client.Models(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), codeFor(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Query().Has("client_version") {
		_ = json.NewEncoder(w).Encode(codexManifest(ms))
		return
	}
	data := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		data = append(data, modelObject(m))
	}
	resp := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		resp["first_id"], resp["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	_ = json.NewEncoder(w).Encode(resp)
}
