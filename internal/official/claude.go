package official

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/buyer"
	"sharetoken/internal/protocol"
	"sharetoken/internal/provider"
)

// claudeState is what the gateway knows of the Claude plan Claude Code is
// signed in with.
type claudeState struct {
	usage     provider.UsageLimits
	haveUsage bool
	// heldUntil: the plan turned requests away until then; meanwhile they
	// go to the network directly when the fallback is on.
	heldUntil time.Time
	fallbacks int
	lastFall  time.Time
	lastModel string // the network model that took the last one
}

// observe keeps what a vendor's answer says of the plan's usage.
func (g *Gateway) observe(cred Cred, req, resp http.Header) {
	l, ok := provider.UsageFromHeaders(resp, now())
	if !ok {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch cred {
	case CredClaudeOAuth:
		g.claude.usage, g.claude.haveUsage = l, true
		if !l.Rejected && g.claude.heldUntil.After(now()) && maxUsed(l) < 100 {
			g.claude.heldUntil = time.Time{}
		}
	case CredChatGPT:
		st := g.codexAccount(req.Get("chatgpt-account-id"))
		st.merge(l)
	}
}

func maxUsed(l provider.UsageLimits) float64 {
	var m float64
	for _, w := range l.Windows {
		if w.ResetsAt.IsZero() || w.ResetsAt.After(now()) {
			m = max(m, w.Used)
		}
	}
	return m
}

// claudeMessages serves a Messages request on a model of Anthropic's: to
// Anthropic with the user's own credentials, or to the network when the
// plan is used up and the fallback is on.
func (g *Gateway) claudeMessages(w http.ResponseWriter, r *http.Request, cred Cred, body []byte, model string, network http.Handler, models ModelsFunc) {
	started := now()
	if cred == CredClaudeOAuth && g.claudeHeld() {
		if g.claudeFallback(w, r, body, model, network, models) {
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
		g.observe(cred, r.Header, resp.Header)
		switch {
		case resp.StatusCode == http.StatusBadRequest && try < 2:
			eb := readError(resp)
			if fixed, ok := retryThinking(body, eb, try); ok {
				g.log.Info("vendor refused thinking blocks from another model; retrying without them", "model", model)
				body = fixed
				continue
			}
			writeBuffered(w, resp, eb)
		case resp.StatusCode == http.StatusTooManyRequests && cred == CredClaudeOAuth:
			eb := readError(resp)
			g.claudeRejected(resp.Header)
			if g.claudeFallback(w, r, body, model, network, models) {
				return
			}
			writeBuffered(w, resp, eb)
		default:
			copyResponse(w, resp)
			resp.Body.Close()
		}
		g.logDone(r, cred, model, resp.StatusCode, started)
		return
	}
}

// claudeHeld reports whether the plan is known to turn requests away.
func (g *Gateway) claudeHeld() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.settings.ClaudeFallbackOff && g.claude.heldUntil.After(now())
}

// maxHold bounds a hold the headers set (the weekly window).
const maxHold = 7 * 24 * time.Hour

// claudeRejected notes a 429 of the plan: held until the full window
// resets, else for what Retry-After says, else a minute.
func (g *Gateway) claudeRejected(h http.Header) {
	t := now()
	until := t.Add(time.Minute)
	if l, ok := provider.UsageFromHeaders(h, t); ok {
		for _, w := range l.Windows {
			if w.Used >= 100 && w.ResetsAt.After(until) {
				until = w.ResetsAt
			}
		}
	}
	if at, err := strconv.ParseInt(strings.TrimSpace(h.Get("anthropic-ratelimit-unified-reset")), 10, 64); err == nil && at > 0 {
		if u := time.Unix(at, 0); u.After(until) {
			until = u
		}
	}
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s > 0 {
		if u := t.Add(time.Duration(s) * time.Second); u.After(until) {
			until = u
		}
	}
	if limit := t.Add(maxHold); until.After(limit) {
		until = limit
	}
	g.mu.Lock()
	g.claude.heldUntil = until
	g.mu.Unlock()
	g.log.Info("Claude plan turned a request away", "until", until.Format(time.RFC3339))
}

// claudeFallback answers a request from the network on a model like the one
// asked for; false when it cannot (fallback off, no API key, not connected,
// no model).
func (g *Gateway) claudeFallback(w http.ResponseWriter, r *http.Request, body []byte, model string, network http.Handler, models ModelsFunc) bool {
	s := g.Settings()
	if s.ClaudeFallbackOff || KeyMissing(r.Context()) || models == nil {
		return false
	}
	ms, err := models(r.Context())
	if err != nil || len(ms) == 0 {
		return false
	}
	pick := pickFallback(model, ms, s.ClaudeFallbackModel)
	if pick == "" {
		return false
	}
	g.mu.Lock()
	g.claude.fallbacks++
	g.claude.lastFall, g.claude.lastModel = now(), pick
	g.mu.Unlock()
	g.log.Info("Claude plan used up: answering from the network", "asked", model, "network_model", pick)
	g.toNetwork(w, r, body, buyer.ListedModel(pick), network)
	return true
}

// pickFallback chooses the network model that takes over from model: the
// one set, else the same model, else one of its family, else a Claude
// model, else one that speaks Messages, else the first.
func pickFallback(model string, ms []protocol.ModelSummary, set string) string {
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.Model)
	}
	if set != "" && slices.Contains(ids, buyer.NetworkModel(set)) {
		return buyer.NetworkModel(set)
	}
	want := strings.ToLower(buyer.NetworkModel(model))
	if slices.Contains(ids, want) {
		return want
	}
	lower := func(s string) string { return strings.ToLower(s) }
	// The same family first, then the nearest in strength: a light model
	// for a light one, a strong one otherwise.
	order := []string{"opus", "fable", "sonnet", "haiku"}
	if strings.Contains(want, "haiku") {
		order = []string{"haiku", "sonnet", "opus", "fable"}
	}
	for _, fam := range order {
		if strings.Contains(want, fam) {
			order = append([]string{fam}, slices.DeleteFunc(slices.Clone(order), func(f string) bool { return f == fam })...)
			break
		}
	}
	for _, fam := range order {
		for _, id := range ids {
			if strings.Contains(lower(id), "claude") && strings.Contains(lower(id), fam) {
				return id
			}
		}
	}
	for _, id := range ids {
		if strings.Contains(lower(id), "claude") {
			return id
		}
	}
	for _, m := range ms {
		if slices.Contains(m.APIs, protocol.APIMessages) {
			return m.Model
		}
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// retryThinking: when Anthropic refuses a conversation for thinking blocks
// another model wrote (their signatures are not its own), the conversation
// without them; on the second refusal, also without thinking for the turn.
func retryThinking(body, errBody []byte, try int) ([]byte, bool) {
	e := strings.ToLower(string(errBody))
	if !strings.Contains(e, "thinking") && !strings.Contains(e, "signature") {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(m["messages"], &msgs) != nil {
		return nil, false
	}
	changed := false
	for _, msg := range msgs {
		var content []map[string]json.RawMessage
		if json.Unmarshal(msg["content"], &content) != nil {
			continue
		}
		kept := content[:0]
		for _, b := range content {
			var typ string
			_ = json.Unmarshal(b["type"], &typ)
			if typ == "thinking" || typ == "redacted_thinking" {
				changed = true
				continue
			}
			kept = append(kept, b)
		}
		msg["content"], _ = json.Marshal(kept)
	}
	if try > 0 {
		for _, k := range []string{"thinking", "context_management"} {
			if _, ok := m[k]; ok {
				delete(m, k)
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	m["messages"], _ = json.Marshal(msgs)
	out, err := json.Marshal(m)
	return out, err == nil
}
