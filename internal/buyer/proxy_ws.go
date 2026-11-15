package buyer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"sharetoken/internal/protocol"
)

// Codex can speak the Responses API over a WebSocket (a provider with
// supports_websockets): it opens GET /v1/responses, sends each request as a
// {"type":"response.create", …} message and reads the events as messages.
// Each request runs as a job of its own, as over HTTP.
//
// Over a WebSocket Codex sends only what is new since the previous response
// and names it with previous_response_id. Nodes keep nothing between jobs, so
// the connection keeps the previous request's input and output here and
// sends the whole conversation. A request with generate=false only warms the
// connection up: it is answered here, without a job.

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  64 << 10,
	WriteBufferSize: 64 << 10,
	// A web page may open a WebSocket to localhost; tools do not send an
	// Origin. Refusing pages keeps them from spending the user's credits.
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

const wsMaxMessage = maxProxyBody

// wsTurn is what a connection remembers of its last response.
type wsTurn struct {
	id    string
	items []json.RawMessage // the request's whole input, then the response's output
}

func (p *Proxy) responsesSocket(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		writeAPIError(w, protocol.APIResponses, http.StatusMethodNotAllowed, "GET /v1/responses is the WebSocket endpoint; POST requests", protocol.CodeBadRequest)
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(wsMaxMessage)
	var last *wsTurn
	for {
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		turn, err := p.wsRequest(r, conn, msg, last)
		if err != nil {
			return // the connection broke
		}
		if turn != nil {
			last = turn
		}
	}
}

// wsRequest handles one message. It returns the connection's new last turn,
// and an error only when the connection failed.
func (p *Proxy) wsRequest(r *http.Request, conn *websocket.Conn, msg []byte, last *wsTurn) (*wsTurn, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(msg, &req); err != nil {
		return nil, wsError(conn, http.StatusBadRequest, "invalid_request_error", "the message is not a JSON object")
	}
	var typ string
	_ = json.Unmarshal(req["type"], &typ)
	if typ != "response.create" {
		return nil, wsError(conn, http.StatusBadRequest, "invalid_request_error", "unsupported message type "+typ+"; send response.create")
	}
	delete(req, "type")
	generate := true
	if raw, ok := req["generate"]; ok {
		_ = json.Unmarshal(raw, &generate)
		delete(req, "generate")
	}
	input, err := inputItems(req["input"])
	if err != nil {
		return nil, wsError(conn, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	var prev string
	_ = json.Unmarshal(req["previous_response_id"], &prev)
	delete(req, "previous_response_id")
	if prev != "" {
		if last == nil || last.id != prev {
			return nil, wsErrorCode(conn, http.StatusBadRequest, "invalid_request_error", "previous_response_not_found",
				"previous response "+prev+" is not on this connection; send the whole conversation")
		}
		input = append(append([]json.RawMessage(nil), last.items...), input...)
	}
	slog.Debug("responses websocket request", "component", "buyer", "previous", prev != "", "generate", generate, "new_items", len(input)-len(itemsOf(last, prev)), "items", len(input))
	req["input"], _ = json.Marshal(input)
	req["stream"] = json.RawMessage("true")
	var model string
	_ = json.Unmarshal(req["model"], &model)
	if !generate {
		return wsWarmup(conn, model, input)
	}
	if model == "" || len(input) == 0 {
		return nil, wsError(conn, http.StatusBadRequest, "invalid_request_error", "model and input are required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cr := p.request(r, protocol.APIResponses, model, body)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var (
		writeErr error
		sent     bool
		id       string
		output   []json.RawMessage
		done     []json.RawMessage
	)
	_, err = p.Client.Chat(ctx, cr, func(payload []byte) error {
		var ev struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Response struct {
				ID     string            `json:"id"`
				Output []json.RawMessage `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal(payload, &ev) == nil {
			switch ev.Type {
			case "response.created":
				id = ev.Response.ID
			case "response.output_item.done":
				done = append(done, ev.Item)
			case "response.completed", "response.incomplete", "response.done":
				id, output = ev.Response.ID, ev.Response.Output
			}
		}
		sent = true
		if writeErr = conn.WriteMessage(websocket.TextMessage, payload); writeErr != nil {
			cancel()
		}
		return writeErr
	})
	if writeErr != nil {
		return nil, writeErr
	}
	if err != nil {
		if sent {
			return nil, conn.WriteMessage(websocket.TextMessage, streamErrorJSON(protocol.APIResponses, err.Error(), codeFor(err)))
		}
		return nil, wsErrorCode(conn, statusFor(err), "server_error", codeFor(err), err.Error())
	}
	if len(output) == 0 {
		output = done
	}
	if id == "" {
		return nil, nil
	}
	return &wsTurn{id: id, items: append(input, output...)}, nil
}

// itemsOf is what a request with previous response prev builds on.
func itemsOf(last *wsTurn, prev string) []json.RawMessage {
	if prev == "" || last == nil {
		return nil
	}
	return last.items
}

// inputItems reads a request's input as a list of items: a string is one
// user message.
func inputItems(raw json.RawMessage) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		return items, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, err
	}
	item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}})
	return []json.RawMessage{item}, nil
}

// wsWarmup answers a generate=false request: an empty response, which the
// next request may build on with previous_response_id.
func wsWarmup(conn *websocket.Conn, model string, input []json.RawMessage) (*wsTurn, error) {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := "resp_" + hex.EncodeToString(b)
	resp := map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "in_progress",
		"model": model, "output": []any{}}
	for i, typ := range []string{"response.created", "response.completed"} {
		if typ == "response.completed" {
			resp["status"] = "completed"
			resp["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0,
				"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}
		}
		ev, _ := json.Marshal(map[string]any{"type": typ, "sequence_number": i, "response": resp})
		if err := conn.WriteMessage(websocket.TextMessage, ev); err != nil {
			return nil, err
		}
	}
	return &wsTurn{id: id, items: input}, nil
}

func wsError(conn *websocket.Conn, status int, typ, msg string) error {
	return wsErrorCode(conn, status, typ, "", msg)
}

// wsErrorCode sends an error event the way the Responses WebSocket does.
func wsErrorCode(conn *websocket.Conn, status int, typ, code, msg string) error {
	ev, _ := json.Marshal(map[string]any{"type": "error", "status": status,
		"error": map[string]any{"type": typ, "code": code, "message": msg}})
	return conn.WriteMessage(websocket.TextMessage, ev)
}
