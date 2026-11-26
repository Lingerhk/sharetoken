package official

import (
	"crypto/sha256"
	"encoding/hex"
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

// claudeLogin is what the gateway knows of one Claude plan: the one a
// request's credentials are of.
type claudeLogin struct {
	usage     provider.UsageLimits
	haveUsage bool
	// heldUntil: the plan turned requests away until then; meanwhile they
	// go to the network directly when the fallback is on.
	heldUntil time.Time
	seen      time.Time
}

// claudeState is what the gateway knows of the Claude plans Claude Code is
// signed in with. Each login has its own: a spent plan of somebody else's
// (any program on the machine can send one's requests here) must not send
// this user's conversations to the network.
type claudeState struct {
	logins map[string]*claudeLogin // by a hash of the credentials
	last   string                  // the login seen last: the one Status shows
	// The fallbacks to the network, of all logins.
	fallbacks int
	lastFall  time.Time
	lastModel string // the network model that took the last one
}

// maxClaudeLogins bounds claudeState.logins (a token's renewal is a new one).
const maxClaudeLogins = 16

// claudeOf is the state of the login r carries (g.mu held), or nil for a
// request without the client's API key: what does not prove to be the
// user's own tool neither reads nor writes any state.
func (g *Gateway) claudeOf(r *http.Request) *claudeLogin {
	if KeyMissing(r.Context()) {
		return nil
	}
	sum := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	id := hex.EncodeToString(sum[:8])
	st := g.claude.logins[id]
	if st == nil {
		if len(g.claude.logins) >= maxClaudeLogins {
			oldest := ""
			for k, l := range g.claude.logins {
				if oldest == "" || l.seen.Before(g.claude.logins[oldest].seen) {
					oldest = k
				}
			}
			delete(g.claude.logins, oldest)
		}
		st = &claudeLogin{}
		g.claude.logins[id] = st
	}
	st.seen, g.claude.last = now(), id
	return st
}

// observe keeps what a vendor's answer to r says of the plan's usage,
// unless r came without the client's API key.
func (g *Gateway) observe(r *http.Request, cred Cred, resp http.Header) {
	if KeyMissing(r.Context()) {
		return
	}
	l, ok := provider.UsageFromHeaders(resp, now())
	if !ok {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch cred {
	case CredClaudeOAuth:
		st := g.claudeOf(r)
		st.usage, st.haveUsage = l, true
		if !l.Rejected && st.heldUntil.After(now()) && maxUsed(l) < 100 {
			st.heldUntil = time.Time{}
		}
	case CredChatGPT:
		st := g.codexAccount(r.Header.Get("chatgpt-account-id"))
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
	if cred == CredClaudeOAuth && g.claudeHeld(r) {
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
		g.observe(r, cred, resp.Header)
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
			// Only a spent plan goes to the network: a passing 429 (a
			// burst, an overloaded model) is the tool's to retry, and
			// must not send the conversation to a seller.
			if planSpent(resp.Header) {
				g.claudeRejected(r, resp.Header)
				if g.claudeFallback(w, r, body, model, network, models) {
					return
				}
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

// claudeHeld reports whether the plan of r's login is known to turn
// requests away.
func (g *Gateway) claudeHeld(r *http.Request) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.claudeOf(r)
	return st != nil && !g.settings.ClaudeFallbackOff && st.heldUntil.After(now())
}

// planSpent reports whether a 429's headers say that the plan's allowance
// is used up: the unified status is "rejected", or a window that has not
// reset is full.
func planSpent(h http.Header) bool {
	l, ok := provider.UsageFromHeaders(h, now())
	return ok && (l.Rejected || maxUsed(l) >= 100)
}

// maxHold bounds a hold the headers set (the weekly window).
const maxHold = 7 * 24 * time.Hour

// claudeRejected notes a 429 of the plan of r's login (planSpent): held
// until the full window resets, else for what Retry-After says, else a
// minute.
func (g *Gateway) claudeRejected(r *http.Request, h http.Header) {
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
	st := g.claudeOf(r)
	if st != nil {
		st.heldUntil = until
	}
	g.mu.Unlock()
	if st == nil {
		return // without the client's API key: nothing is held
	}
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
// one the user set, else the same model, else one of its family, else
// another Claude model; "" when the network has none. No other model takes
// over unasked: the conversation was written for Claude.
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
