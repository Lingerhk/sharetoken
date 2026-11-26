package official

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/buyer"
	"sharetoken/internal/provider"
)

// codexState is what the gateway knows of one ChatGPT account's plan.
type codexState struct {
	usage   provider.UsageLimits
	have    bool
	credits bool // the account has credits: past the plan is paid, not locked
	checked time.Time
	held    int // requests held by the guard
}

func (s *codexState) merge(l provider.UsageLimits) {
	s.usage, s.have, s.checked = l, true, now()
}

// maxCodexAccounts bounds Gateway.codex: the account is whatever a request's
// header says, and with `apikey off` any program on the machine sends them.
const maxCodexAccounts = 32

// codexAccount is the state of a ChatGPT account (g.mu held); past
// maxCodexAccounts the one asked about longest ago makes room.
func (g *Gateway) codexAccount(id string) *codexState {
	st := g.codex[id]
	if st == nil {
		if len(g.codex) >= maxCodexAccounts {
			oldest, first := "", true
			for k, s := range g.codex {
				if first || s.checked.Before(g.codex[oldest].checked) {
					oldest, first = k, false
				}
			}
			delete(g.codex, oldest)
		}
		st = &codexState{}
		g.codex[id] = st
	}
	return st
}

// usageRecheck: a held account's usage is asked again after this long, to
// see the window reset.
const usageRecheck = time.Minute

// codexResponses serves a Responses request (sub: "responses" or
// "responses/compact") on an official model.
func (g *Gateway) codexResponses(w http.ResponseWriter, r *http.Request, cred Cred, body []byte, sub string) {
	started := now()
	model := bodyModel(body)
	if cred == CredChatGPT && sub == "responses" {
		if msg := g.codexGuard(r); msg != "" {
			g.log.Info("ChatGPT plan nearly used up: holding the official model", "model", model)
			writeRefusal(w, body, msg)
			return
		}
	}
	url, h := g.vendorURL(cred, r), vendorHeader(r.Header)
	for try := 0; ; try++ {
		resp, err := g.send(r.Context(), http.MethodPost, url, h, body)
		if err != nil {
			g.unreachable(w, r, cred, err)
			return
		}
		g.observe(r, cred, resp.Header)
		if resp.StatusCode == http.StatusBadRequest && try == 0 {
			eb := readError(resp)
			if fixed, ok := dropEncryptedReasoning(body, eb); ok {
				g.log.Info("vendor refused reasoning from another model; retrying without it", "model", model)
				body = fixed
				continue
			}
			writeBuffered(w, resp, eb)
		} else {
			copyResponse(w, resp)
			resp.Body.Close()
		}
		g.logDone(r, cred, model, resp.StatusCode, started)
		return
	}
}

// codexGuard is the message that holds an official model while a window of
// the account's plan is at the guard's level, or "". A request without the
// client's API key is not held: it neither reads nor writes any state.
func (g *Gateway) codexGuard(r *http.Request) string {
	if KeyMissing(r.Context()) {
		return ""
	}
	g.mu.Lock()
	limit := g.settings.codexGuard()
	account := r.Header.Get("chatgpt-account-id")
	st := g.codexAccount(account)
	stale := !st.have || now().Sub(st.checked) > usageRecheck
	full := st.have && maxUsed(st.usage) >= limit
	g.mu.Unlock()
	if limit >= 100 {
		return ""
	}
	if !st.have || (full && stale) {
		if l, credits, ok := g.fetchCodexUsage(r); ok {
			g.mu.Lock()
			st.merge(l)
			st.credits = credits
			g.mu.Unlock()
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if st.credits || !st.have {
		return ""
	}
	var worst *provider.UsageWindow
	for i, w := range st.usage.Windows {
		if (w.ResetsAt.IsZero() || w.ResetsAt.After(now())) && w.Used >= limit && (worst == nil || w.Used > worst.Used) {
			worst = &st.usage.Windows[i]
		}
	}
	if worst == nil {
		return ""
	}
	st.held++
	return guardMessage(*worst)
}

// guardMessage tells the user why the official model is held.
func guardMessage(w provider.UsageWindow) string {
	zh, en := w.Name, w.Name
	switch w.Name {
	case "5h":
		zh, en = "5 小时", "5-hour"
	case "7d":
		zh, en = "本周", "weekly"
	case "limit":
		zh, en = "", "usage"
	}
	reset, resetEn := "", ""
	if !w.ResetsAt.IsZero() {
		t := w.ResetsAt.Local()
		reset = fmt.Sprintf("，%d 月 %d 日 %s 重置后自动恢复", t.Month(), t.Day(), t.Format("15:04"))
		resetEn = " They come back when it resets, " + t.Format("Jan 2 15:04") + "."
	}
	return fmt.Sprintf("ChatGPT 订阅%s额度已用 %.0f%%，ShareToken 已暂停官方模型，以免额度用尽后 Codex Desktop 锁住输入框。请在模型菜单换成 ShareToken 的模型（st-…）继续%s。\n\n"+
		"Your ChatGPT plan's %s window is %.0f%% used: ShareToken holds the official models so that Codex Desktop does not lock its input once it is spent. Switch to a ShareToken model (st-…) in the model menu to go on.%s",
		zh, w.Used, reset, en, w.Used, resetEn)
}

// fetchCodexUsage asks the ChatGPT backend for the account's usage
// (wham/usage, as Codex does), with the request's own credentials.
func (g *Gateway) fetchCodexUsage(r *http.Request) (provider.UsageLimits, bool, bool) {
	ctx, cancel := contextTimeout(r, 5*time.Second)
	defer cancel()
	resp, err := g.send(ctx, http.MethodGet, g.up.ChatGPTBackend+"/wham/usage", vendorHeader(r.Header), nil)
	if err != nil {
		g.log.Warn("ChatGPT usage unreadable", "err", err)
		return provider.UsageLimits{}, false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		g.log.Warn("ChatGPT usage unreadable", "status", resp.StatusCode)
		return provider.UsageLimits{}, false, false
	}
	return parseWhamUsage(resp.Body)
}

// parseWhamUsage reads a wham/usage answer.
func parseWhamUsage(body io.Reader) (provider.UsageLimits, bool, bool) {
	type window struct {
		Used    *float64 `json:"used_percent"`
		Seconds int64    `json:"limit_window_seconds"`
		ResetAt int64    `json:"reset_at"`
	}
	var u struct {
		RateLimit struct {
			Allowed   *bool   `json:"allowed"`
			Primary   *window `json:"primary_window"`
			Secondary *window `json:"secondary_window"`
		} `json:"rate_limit"`
		Credits struct {
			Has       bool `json:"has_credits"`
			Unlimited bool `json:"unlimited"`
		} `json:"credits"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&u); err != nil {
		return provider.UsageLimits{}, false, false
	}
	l := provider.UsageLimits{At: now()}
	for _, w := range []*window{u.RateLimit.Primary, u.RateLimit.Secondary} {
		if w == nil || w.Used == nil {
			continue
		}
		uw := provider.UsageWindow{Name: spanName(w.Seconds), SpanSeconds: w.Seconds, Used: *w.Used}
		if w.ResetAt > 0 {
			uw.ResetsAt = time.Unix(w.ResetAt, 0)
		}
		l.Windows = append(l.Windows, uw)
	}
	if a := u.RateLimit.Allowed; a != nil && !*a && maxUsedRaw(l) < 100 {
		l.Windows = append(l.Windows, provider.UsageWindow{Name: "limit", Used: 100})
	}
	return l, u.Credits.Has || u.Credits.Unlimited, len(l.Windows) > 0
}

func maxUsedRaw(l provider.UsageLimits) float64 {
	var m float64
	for _, w := range l.Windows {
		m = max(m, w.Used)
	}
	return m
}

func spanName(sec int64) string {
	switch {
	case sec <= 0:
		return "?"
	case sec%86400 == 0:
		return fmt.Sprintf("%dd", sec/86400)
	case sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	}
	return fmt.Sprintf("%dm", sec/60)
}

// writeRefusal answers a Responses request with an assistant message that
// says msg, streamed when the request streams.
func writeRefusal(w http.ResponseWriter, body []byte, msg string) {
	var q struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	_ = json.Unmarshal(body, &q)
	id := fmt.Sprintf("resp_st_hold_%d", now().UnixNano())
	item := map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": msg, "annotations": []any{}}}}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	done := map[string]any{"id": id, "object": "response", "created_at": now().Unix(), "status": "completed",
		"model": q.Model, "output": []any{item}, "usage": usage}
	if q.Stream != nil && !*q.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(done)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	inProgress := map[string]any{"id": id, "object": "response", "created_at": now().Unix(), "status": "in_progress", "model": q.Model, "output": []any{}}
	open := map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
	seq := 0
	ev := func(typ string, fields map[string]any) {
		fields["type"] = typ
		fields["sequence_number"] = seq
		seq++
		b, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
	}
	ev("response.created", map[string]any{"response": inProgress})
	ev("response.output_item.added", map[string]any{"output_index": 0, "item": open})
	ev("response.content_part.added", map[string]any{"item_id": "msg_" + id, "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	ev("response.output_text.delta", map[string]any{"item_id": "msg_" + id, "output_index": 0, "content_index": 0, "delta": msg})
	ev("response.output_text.done", map[string]any{"item_id": "msg_" + id, "output_index": 0, "content_index": 0, "text": msg})
	ev("response.content_part.done", map[string]any{"item_id": "msg_" + id, "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": msg, "annotations": []any{}}})
	ev("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	ev("response.completed", map[string]any{"response": done})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// dropEncryptedReasoning: when the vendor cannot read the encrypted
// reasoning in a conversation (another model or account wrote it), the
// conversation without it.
func dropEncryptedReasoning(body, errBody []byte) ([]byte, bool) {
	if !strings.Contains(string(errBody), "encrypted_content") && !strings.Contains(string(errBody), "invalid_encrypted") {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	var input []map[string]json.RawMessage
	if json.Unmarshal(m["input"], &input) != nil {
		return nil, false
	}
	kept := input[:0]
	for _, it := range input {
		var typ string
		_ = json.Unmarshal(it["type"], &typ)
		if _, enc := it["encrypted_content"]; enc && (typ == "reasoning" || typ == "compaction") {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(input) {
		return nil, false
	}
	m["input"], _ = json.Marshal(kept)
	out, err := json.Marshal(m)
	return out, err == nil
}

// turnModels remembers which model each Codex turn runs on: its image
// requests, and its subagents, follow it.
type turnModels struct {
	mu    sync.Mutex
	max   int
	order []string
	model map[string]string
}

func newTurnModels(n int) turnModels { return turnModels{max: n, model: map[string]string{}} }

// turnMeta is what Codex says of a request's turn: in client_metadata, or
// in its x-codex-turn-metadata header.
func turnMeta(h http.Header, body []byte) (turn, parent string) {
	var q struct {
		Meta map[string]json.RawMessage `json:"client_metadata"`
	}
	_ = json.Unmarshal(body, &q)
	raw := q.Meta["x-codex-turn-metadata"]
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s)
	}
	if len(raw) == 0 {
		raw = []byte(h.Get("x-codex-turn-metadata"))
	}
	var m struct {
		Turn   string `json:"turn_id"`
		Parent string `json:"parent_turn_id"`
	}
	_ = json.Unmarshal(raw, &m)
	if m.Turn == "" {
		_ = json.Unmarshal(q.Meta["turn_id"], &m.Turn)
	}
	return m.Turn, m.Parent
}

func (t *turnModels) note(h http.Header, body []byte, model string) {
	turn, _ := turnMeta(h, body)
	if turn == "" || model == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.model[turn]; !ok {
		t.order = append(t.order, turn)
		if len(t.order) > t.max {
			delete(t.model, t.order[0])
			t.order = t.order[1:]
		}
	}
	t.model[turn] = model
}

func (t *turnModels) get(turn string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.model[turn]
}

// parentOfSubagent is the network model a subagent's parent turn runs on,
// or "".
func (t *turnModels) parentOfSubagent(h http.Header, body []byte) string {
	kind := h.Get("x-openai-subagent")
	if kind == "" || kind == "collab_spawn" {
		return ""
	}
	_, parent := turnMeta(h, body)
	if m := t.get(parent); buyer.IsListedModel(m) {
		return m
	}
	return ""
}

// images serves Codex's image requests: by the vendor, unless the turn runs
// on a network model, which makes no images.
func (g *Gateway) images(w http.ResponseWriter, r *http.Request, cred Cred, body []byte) {
	if m := g.turns.get(r.Header.Get("x-codex-image-turn-id")); buyer.IsListedModel(m) {
		writeVendorError(w, cred, http.StatusBadRequest, "image generation is not available on ShareToken models ("+m+"): switch to an official model for images")
		return
	}
	g.passthrough(w, r, cred, body)
}

// officialModels keeps the vendor's model lists, by account and query, for
// when the vendor cannot be asked.
type officialModels struct {
	mu     sync.Mutex
	loaded bool
	lists  map[string]json.RawMessage
}

const officialModelsFile = "official-models.json"

// The lists kept are bounded: their keys come from requests (the account
// header and the query), which any program on the machine can vary.
const (
	maxModelLists    = 32
	maxModelListsKey = 256
)

func (o *officialModels) load(home string) {
	if o.loaded {
		return
	}
	o.loaded = true
	o.lists = map[string]json.RawMessage{}
	if data, err := os.ReadFile(filepath.Join(home, officialModelsFile)); err == nil {
		_ = json.Unmarshal(data, &o.lists)
	}
}

func (o *officialModels) get(home, key string) json.RawMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.load(home)
	return o.lists[key]
}

func (o *officialModels) put(home, key string, list json.RawMessage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.load(home)
	if list == nil {
		if _, ok := o.lists[key]; !ok {
			return
		}
		delete(o.lists, key)
	} else {
		if len(key) > maxModelListsKey {
			return
		}
		if _, ok := o.lists[key]; !ok {
			for k := range o.lists { // any other makes room: they are only for when the vendor is down
				if len(o.lists) < maxModelLists {
					break
				}
				delete(o.lists, k)
			}
		}
		o.lists[key] = list
	}
	if data, err := json.Marshal(o.lists); err == nil {
		_ = os.WriteFile(filepath.Join(home, officialModelsFile), data, 0o600)
	}
}

// serveModels serves GET /v1/models for a tool with vendor credentials:
// Codex signed in with ChatGPT gets the network's models and its plan's
// (asked of the ChatGPT backend with its own credentials), others what the
// network lists.
func (g *Gateway) serveModels(w http.ResponseWriter, r *http.Request, cred Cred, network http.Handler, models ModelsFunc) {
	if cred != CredChatGPT || !r.URL.Query().Has("client_version") {
		if KeyMissing(r.Context()) {
			g.passthrough(w, r, cred, nil)
			return
		}
		g.toNetwork(w, r, nil, "", network)
		return
	}
	var list []any
	if models != nil && !KeyMissing(r.Context()) {
		if ms, err := models(r.Context()); err == nil {
			if cat, ok := buyer.CodexCatalog(ms)["models"].([]map[string]any); ok {
				for _, e := range cat {
					e["priority"] = 1000 + len(list)
					list = append(list, e)
				}
			}
		}
	}
	official := g.vendorModels(r)
	out := make([]json.RawMessage, 0, len(list)+len(official))
	out = append(out, official...)
	for _, e := range list {
		b, _ := json.Marshal(e)
		out = append(out, b)
	}
	g.log.Info("Codex model list", "official", len(official), "network", len(list), "client_version", r.URL.Query().Get("client_version"))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"models": out})
}

// vendorModels is the ChatGPT backend's model list for the request's
// account: asked now, else as last seen.
func (g *Gateway) vendorModels(r *http.Request) []json.RawMessage {
	sum := sha256.Sum256([]byte(r.Header.Get("chatgpt-account-id")))
	key := hex.EncodeToString(sum[:8]) + "\n" + r.URL.RawQuery
	ctx, cancel := contextTimeout(r, 5*time.Second)
	defer cancel()
	cached := func() []json.RawMessage {
		var l []json.RawMessage
		_ = json.Unmarshal(g.official.get(g.home, key), &l)
		return l
	}
	resp, err := g.send(ctx, http.MethodGet, g.vendorURL(CredChatGPT, r), vendorHeader(r.Header), nil)
	if err != nil {
		g.log.Warn("ChatGPT model list unreadable; using the last one", "err", err)
		return cached()
	}
	defer resp.Body.Close()
	// A request without the client's API key gets its list and leaves none.
	keep := !KeyMissing(r.Context())
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if keep {
			g.official.put(g.home, key, nil)
		}
		return nil
	}
	var body struct {
		Models json.RawMessage `json:"models"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body) != nil {
		return cached()
	}
	var l []json.RawMessage
	if json.Unmarshal(body.Models, &l) != nil {
		return cached()
	}
	if keep {
		g.official.put(g.home, key, body.Models)
	}
	return l
}
