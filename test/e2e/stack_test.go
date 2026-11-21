//go:build stack

package e2e

// A whole market on this machine for real clients: run it, then point
// Claude Code, Codex or any OpenAI client at the buyer's endpoint.
//
//	ST_STACK_SELLERS=replay-codex,replay-gemini,deepseek,deepseek-chat,openai \
//	ST_STACK_PORT=18999 ST_STACK_WORKDIR=/path/with/a.txt \
//	DEEPSEEK_API_KEY=… OPENAI_API_KEY=… \
//	scripts/test/test.sh -tags stack -run TestStack -timeout 0 ./test/e2e/
//
// Each seller sells a model of its own name, so a client picks the seller
// by model:
//
//	replay-codex   a ChatGPT (Codex) login, replayed (testdata/replay)
//	replay-gemini  a Gemini CLI login, replayed
//	deepseek       DeepSeek with an API key: chat, and Messages at /anthropic
//	deepseek-chat  DeepSeek's chat API only (Messages and Responses converted)
//	openai         OpenAI with an API key: chat and Responses ($ST_OPENAI_MODEL, default gpt-5-mini)
//	anthropic      Anthropic with an API key ($ANTHROPIC_WORKSPACE_ID optional; $ST_ANTHROPIC_MODEL, default claude-haiku-5-5)
//	gemini         Gemini with an AI Studio key: its chat API ($ST_GEMINI_MODEL, default gemini-3.8-flash)
//
// ST_STACK_DEBUG=1 logs at debug level.
//
// It stops after $ST_STACK_SECONDS (default an hour) or when the file
// $ST_STACK_WORKDIR/stop appears; usage and node IDs go to stack.json there.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
	"sharetoken/internal/provider"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestStack(t *testing.T) {
	if os.Getenv("ST_STACK_DEBUG") != "" {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	work := os.Getenv("ST_STACK_WORKDIR")
	if work == "" {
		work = t.TempDir()
	}
	port := os.Getenv("ST_STACK_PORT")
	if port == "" {
		port = "18999"
	}
	m := newMarketWith(t, coordinator.Config{WarmUp: -1})
	_, srv := newReplayer(t, work)
	openaiModel := os.Getenv("ST_OPENAI_MODEL")
	if openaiModel == "" {
		openaiModel = "gpt-5-mini"
	}
	nodes := map[string]string{}
	for _, s := range strings.Split(os.Getenv("ST_STACK_SELLERS"), ",") {
		s = strings.TrimSpace(s)
		var (
			e        *provider.Entry
			ts       provider.TokenSource
			upstream string
			maxOut   uint64
		)
		switch s {
		case "":
			continue
		case "replay-codex":
			tok := &provider.Token{AccessToken: "at", AccountID: "acct"}
			e, ts, upstream = &provider.Entry{ID: s, Name: s, Type: provider.KindOpenAI, Auth: provider.AuthOAuth, OAuth: tok, BaseURL: srv.URL + "/codex"}, provider.FixedToken(tok), "gpt-5.5"
		case "replay-gemini":
			tok := &provider.Token{AccessToken: "at", ProjectID: "project"}
			e, ts, upstream = &provider.Entry{ID: s, Name: s, Type: provider.KindGemini, Auth: provider.AuthOAuth, OAuth: tok, BaseURL: srv.URL + "/gemini"}, provider.FixedToken(tok), "gemini-2.5-pro"
		case "deepseek":
			e, upstream, maxOut = &provider.Entry{ID: s, Name: s, Type: provider.KindDeepSeek, Auth: provider.AuthAPIKey, APIKey: os.Getenv("DEEPSEEK_API_KEY")}, "deepseek-chat", 8192
		case "deepseek-chat":
			e, upstream, maxOut = &provider.Entry{ID: s, Name: s, Type: provider.KindCustom, Auth: provider.AuthAPIKey, APIKey: os.Getenv("DEEPSEEK_API_KEY"), BaseURL: "https://api.deepseek.com"}, "deepseek-chat", 8192
		case "openai":
			e, upstream = &provider.Entry{ID: s, Name: s, Type: provider.KindOpenAI, Auth: provider.AuthAPIKey, APIKey: os.Getenv("OPENAI_API_KEY")}, openaiModel
		case "anthropic":
			e, upstream = &provider.Entry{ID: s, Name: s, Type: provider.KindAnthropic, Auth: provider.AuthAPIKey, APIKey: os.Getenv("ANTHROPIC_API_KEY"),
				Headers: map[string]string{"anthropic-workspace-id": os.Getenv("ANTHROPIC_WORKSPACE_ID")}}, envOr("ST_ANTHROPIC_MODEL", "claude-haiku-5-5")
		case "gemini":
			e, upstream = &provider.Entry{ID: s, Name: s, Type: provider.KindGemini, Auth: provider.AuthAPIKey, APIKey: os.Getenv("GEMINI_API_KEY")}, envOr("ST_GEMINI_MODEL", "gemini-3.8-flash")
		default:
			t.Fatalf("unknown seller %q", s)
		}
		e.Models = []node.ModelConfig{{ID: s, UpstreamModel: upstream, MaxOutputTokens: maxOut}}
		up, err := e.Upstream(ts)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		_, k := m.addNodeCfg(srv.URL, 1_000_000, 4_000_000, nil, func(c *node.Config) {
			c.Upstream = up
			c.Models = []node.ModelConfig{{ID: s, UpstreamModel: upstream, PriceInMicros: 1_000_000, PriceOutMicros: 4_000_000, MaxOutputTokens: maxOut, Upstream: up}}
		})
		nodes[s] = k.ID()
	}
	m.waitOffers(len(nodes))
	cl, _, buyerKeys := m.addBuyer(1_000_000_000)
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: (&buyer.Proxy{Client: cl, BudgetMicros: 50_000_000}).Handler()}
	go func() { _ = hs.Serve(ln) }()
	defer hs.Close()
	write := func() {
		b, _ := json.MarshalIndent(map[string]any{"endpoint": "http://127.0.0.1:" + port, "nodes": nodes,
			"buyer_balance": m.balance(buyerKeys)}, "", " ")
		_ = os.WriteFile(filepath.Join(work, "stack.json"), b, 0o644)
	}
	write()
	t.Logf("stack up at http://127.0.0.1:%s with %v", port, nodes)
	secs, _ := strconv.Atoi(os.Getenv("ST_STACK_SECONDS"))
	if secs <= 0 {
		secs = 3600
	}
	ctx, cancel := context.WithTimeout(m.ctx, time.Duration(secs)*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if _, err := os.Stat(filepath.Join(work, "stop")); err == nil {
			break
		}
		write()
		time.Sleep(time.Second)
	}
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Error(err)
	}
}
