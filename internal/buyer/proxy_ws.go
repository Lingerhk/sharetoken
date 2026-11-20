package buyer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
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

// wsConn is a socket written from the request in flight and from the loop
// that reads the client, one message at a time.
type wsConn struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	lastWrite time.Time
}

// write sends a message unless ctx, a request's, has ended (interrupted, or
// the client gone).
func (c *wsConn) write(ctx context.Context, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastWrite = time.Now()
	return c.conn.WriteMessage(websocket.TextMessage, msg)
}

// quietFor is how long since the last message went out.
func (c *wsConn) quietFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.lastWrite)
}

// wsMsg is a message read from the client, or the error that ended reading.
type wsMsg struct {
	data []byte
	err  error
}

// wsRun is what the request in flight has said: the response it is.
type wsRun struct {
	mu sync.Mutex
	id string
}

func (t *wsRun) setID(id string) {
	t.mu.Lock()
	t.id = id
	t.mu.Unlock()
}

// is reports whether an interrupt naming id is for this response: one that
// names none is for whatever is in flight.
func (t *wsRun) is(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return id == "" || id == t.id
}

// wsInterrupt is whether a message asks to stop the response in flight:
// Codex's response.interrupt (response.cancel taken the same way).
func wsInterrupt(msg []byte) (typ, id string, ok bool) {
	var m struct {
		Type       string `json:"type"`
		ResponseID string `json:"response_id"`
	}
	if json.Unmarshal(msg, &m) != nil || m.Type != "response.interrupt" && m.Type != "response.cancel" {
		return "", "", false
	}
	return m.Type, m.ResponseID, true
}

func wsInterruptReply(kind, id string) []byte {
	b, _ := json.Marshal(map[string]string{"type": kind, "response_id": id})
	return b
}

// The client is read on a goroutine of its own, so that what it says while a
// response streams is heard then: an interrupt stops the job at once (billed
// as stopped, as when an HTTP client goes away), and a client that leaves
// takes the job with it instead of being found gone at the next write.
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
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	in := make(chan wsMsg)
	go func() {
		for {
			mt, msg, err := conn.ReadMessage()
			if err == nil && mt != websocket.TextMessage {
				continue
			}
			select {
			case in <- wsMsg{msg, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	sc := &wsConn{conn: conn}
	var last *wsTurn
	var queued []wsMsg
	for {
		var m wsMsg
		if len(queued) > 0 {
			m, queued = queued[0], queued[1:]
		} else {
			select {
			case m = <-in:
			case <-ctx.Done():
				return
			}
		}
		if m.err != nil {
			return
		}
		if typ, id, ok := wsInterrupt(m.data); ok {
			// Nothing is in flight: it ended before the ask came.
			if sc.write(ctx, wsInterruptReply(typ+".failed", id)) != nil {
				return
			}
			continue
		}
		turn, more, err := p.wsServe(ctx, r, sc, m.data, last, in)
		queued = append(queued, more...)
		if err != nil {
			return // the connection broke, or the client left
		}
		if turn != nil {
			last = turn
		}
	}
}

// wsServe runs one request while it listens to the client: an interrupt for
// it stops it, other messages wait for their turn (returned as more).
func (p *Proxy) wsServe(ctx context.Context, r *http.Request, sc *wsConn, msg []byte, last *wsTurn, in <-chan wsMsg) (turn *wsTurn, more []wsMsg, err error) {
	reqCtx, stop := context.WithCancel(ctx)
	defer stop()
	run := &wsRun{}
	type result struct {
		turn *wsTurn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		t, err := p.wsRequest(reqCtx, r, sc, msg, last, run)
		done <- result{t, err}
	}()
	for {
		select {
		case res := <-done:
			return res.turn, more, res.err
		case n := <-in:
			if n.err != nil {
				// The client went: what is in flight ends with it.
				stop()
				<-done
				return nil, more, n.err
			}
			typ, id, ok := wsInterrupt(n.data)
			if !ok {
				more = append(more, n)
				continue
			}
			if !run.is(id) {
				if err := sc.write(ctx, wsInterruptReply(typ+".failed", id)); err != nil {
					return nil, more, err
				}
				continue
			}
			stop()
			<-done
			run.mu.Lock()
			if id == "" {
				id = run.id
			}
			run.mu.Unlock()
			slog.Debug("responses websocket interrupt", "component", "buyer", "response", id)
			// The response ends as interrupted: Codex keeps none of it and
			// waits for its end.
			end, _ := json.Marshal(map[string]any{"type": "response.incomplete", "response": map[string]any{
				"id": id, "object": "response", "status": "incomplete",
				"incomplete_details": map[string]any{"reason": "interrupted"}, "output": []any{}}})
			if err := sc.write(ctx, wsInterruptReply(typ+".accepted", id)); err != nil {
				return nil, more, err
			}
			return nil, more, sc.write(ctx, end)
		}
	}
}

// wsRequest handles one message. It returns the connection's new last turn,
// and an error only when the connection failed. A request stopped (ctx
// ended) writes nothing more and returns neither.
func (p *Proxy) wsRequest(ctx context.Context, r *http.Request, sc *wsConn, msg []byte, last *wsTurn, run *wsRun) (*wsTurn, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(msg, &req); err != nil {
		return nil, wsError(ctx, sc, http.StatusBadRequest, "invalid_request_error", "the message is not a JSON object")
	}
	var typ string
	_ = json.Unmarshal(req["type"], &typ)
	if typ != "response.create" {
		return nil, wsError(ctx, sc, http.StatusBadRequest, "invalid_request_error", "unsupported message type "+typ+"; send response.create")
	}
	delete(req, "type")
	generate := true
	if raw, ok := req["generate"]; ok {
		_ = json.Unmarshal(raw, &generate)
		delete(req, "generate")
	}
	input, err := inputItems(req["input"])
	if err != nil {
		return nil, wsError(ctx, sc, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	var prev string
	_ = json.Unmarshal(req["previous_response_id"], &prev)
	delete(req, "previous_response_id")
	if prev != "" {
		if last == nil || last.id != prev {
			return nil, wsErrorCode(ctx, sc, http.StatusBadRequest, "invalid_request_error", "previous_response_not_found",
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
		return wsWarmup(ctx, sc, model, input)
	}
	if model == "" || len(input) == 0 {
		return nil, wsError(ctx, sc, http.StatusBadRequest, "invalid_request_error", "model and input are required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cr := p.request(r, protocol.APIResponses, model, body)
	var (
		writeErr error
		sent     bool
		watch    turnWatch
	)
	var tidy *respTidy
	if !codexClient(r) {
		tidy = &respTidy{}
	}
	// Codex counts only events toward its idle timeout: a slow model's
	// heartbeats go to it as the keepalive event its backend sends.
	cr.OnProgress = func() {
		if tidy == nil && sc.quietFor() >= keepaliveEvery {
			_ = sc.write(ctx, []byte(`{"type":"keepalive"}`))
		}
	}
	handle := func(payload []byte) error {
		watch.see(payload)
		if watch.id != "" {
			run.setID(watch.id)
		}
		if tidy != nil {
			var what tidyResult
			if payload, what = tidy.event(payload); what != tidyKeep {
				return nil
			}
		}
		sent = true
		writeErr = sc.write(ctx, payload)
		return writeErr
	}
	if events, ok := p.toolLoopAnswer(cr); ok {
		for _, ev := range events {
			_ = handle(ev)
		}
	} else {
		deliver, guard := p.guardStream(cr.API, tidy == nil, handle)
		_, err = p.Client.Chat(ctx, cr, deliver)
		if guard.cut(err) {
			slog.Warn("loop guard stopped an answer", "component", "buyer", "api", cr.API, "loop", guard.trip.String())
			for _, ev := range guard.closer.closing() {
				_ = handle(ev)
			}
			err = nil
		}
	}
	if ctx.Err() != nil {
		return nil, nil // stopped: the caller tells the client
	}
	if writeErr != nil {
		return nil, writeErr
	}
	if err != nil {
		if tooLong(err) {
			// Codex compacts on this event alone, before output or after.
			return nil, sc.write(ctx, tooLongFailed(err.Error()))
		}
		if sent {
			return nil, sc.write(ctx, streamErrorJSON(protocol.APIResponses, err.Error(), codeFor(err)))
		}
		return nil, wsErrorCode(ctx, sc, statusFor(err), "server_error", codeFor(err), err.Error())
	}
	return watch.turn(input), nil
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
func wsWarmup(ctx context.Context, sc *wsConn, model string, input []json.RawMessage) (*wsTurn, error) {
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
		if err := sc.write(ctx, ev); err != nil {
			return nil, err
		}
	}
	return &wsTurn{id: id, items: input}, nil
}

func wsError(ctx context.Context, sc *wsConn, status int, typ, msg string) error {
	return wsErrorCode(ctx, sc, status, typ, "", msg)
}

// wsErrorCode sends an error event the way the Responses WebSocket does.
func wsErrorCode(ctx context.Context, sc *wsConn, status int, typ, code, msg string) error {
	ev, _ := json.Marshal(map[string]any{"type": "error", "status": status,
		"error": map[string]any{"type": typ, "code": code, "message": msg}})
	return sc.write(ctx, ev)
}
