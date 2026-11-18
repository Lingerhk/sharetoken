package e2e

// Model metadata end to end: a seller fills in what it knows of its models
// (the built-in table, its own settings), the coordinator merges it into
// the model list, and the buyer's /v1/models and Codex catalog show it, from
// a list kept a minute. And the buyer's loop guard: an answer that goes
// round in a loop is cut as a length stop, and the node stops the model.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sharetoken/internal/buyer"
	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

func getList(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// byKey indexes a list of objects by one of their fields.
func byKey(list any, key string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, v := range list.([]any) {
		m := v.(map[string]any)
		out[m[key].(string)] = m
	}
	return out
}

func TestCatalogShowsModelMetadata(t *testing.T) {
	m := newMarket(t)
	h, k := newHost(t)
	cfg := node.Config{
		Coordinator: m.info,
		Models: []node.ModelConfig{
			// known to the table by the upstream's name for it
			{ID: "sonnet", UpstreamModel: "claude-sonnet-4-5-20250929", PriceInMicros: priceIn, PriceOutMicros: priceOut},
			// known by its network name, with the seller's own window
			{ID: "gpt-5.5", UpstreamModel: "deployment-7", PriceInMicros: priceIn, PriceOutMicros: priceOut,
				Meta: &protocol.ModelMeta{ContextWindow: 400000}},
			// unknown
			{ID: "house-7b", UpstreamModel: "house-7b", PriceInMicros: priceIn, PriceOutMicros: priceOut},
		},
		Upstream:      &node.OpenAICompat{BaseURL: "http://127.0.0.1:1/v1"},
		OfferInterval: 200 * time.Millisecond,
	}
	svc, err := node.NewService(m.ctx, h, k, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(1_000_000)
	proxy := httptest.NewServer((&buyer.Proxy{Client: cl, BudgetMicros: 1_000_000}).Handler())
	t.Cleanup(proxy.Close)

	list := byKey(getList(t, proxy.URL+"/v1/models")["data"], "id")
	sonnet, gpt, house := list["sonnet"], list["gpt-5.5"], list["house-7b"]
	if sonnet == nil || gpt == nil || house == nil {
		t.Fatalf("models: %v", list)
	}
	if sonnet["context_window"] != 200000.0 || sonnet["context_length"] != 200000.0 || sonnet["max_input_tokens"] != 200000.0 ||
		sonnet["max_output_tokens"] != 64000.0 || sonnet["max_tokens"] != 64000.0 || sonnet["reasoning"] != true {
		t.Errorf("sonnet: %v", sonnet)
	}
	if gpt["context_window"] != 400000.0 || gpt["max_output_tokens"] != 128000.0 ||
		fmt.Sprint(gpt["supported_reasoning_levels"]) != "[none low medium high xhigh]" || fmt.Sprint(gpt["reasoning_levels"]) != "[none low medium high xhigh]" {
		t.Errorf("gpt-5.5: %v", gpt)
	}
	if _, ok := house["context_window"]; ok {
		t.Errorf("an unknown model has no window: %v", house)
	}

	codex := byKey(getList(t, proxy.URL+"/v1/models?client_version=0.166.0")["models"], "slug")
	cs, cg, ch := codex["sonnet"], codex["gpt-5.5"], codex["house-7b"]
	if cs["context_window"] != 200000.0 || cs["max_context_window"] != 200000.0 || cs["auto_compact_token_limit"] != nil {
		t.Errorf("codex sonnet: window %v max %v compact %v", cs["context_window"], cs["max_context_window"], cs["auto_compact_token_limit"])
	}
	if cg["context_window"] != 400000.0 || cg["auto_compact_token_limit"] != 272000.0 || cg["default_reasoning_level"] != "medium" {
		t.Errorf("codex gpt-5.5: window %v compact %v default %v", cg["context_window"], cg["auto_compact_token_limit"], cg["default_reasoning_level"])
	}
	if n := len(cg["supported_reasoning_levels"].([]any)); n != 5 {
		t.Errorf("codex gpt-5.5 levels: %v", cg["supported_reasoning_levels"])
	}
	if _, ok := ch["context_window"]; ok || ch["auto_compact_token_limit"] != 272000.0 {
		t.Errorf("codex house-7b: %v", ch)
	}
	for slug, e := range codex {
		if bi, _ := e["base_instructions"].(string); !strings.HasPrefix(bi, "You are a coding agent running in the Codex CLI") {
			t.Errorf("%s: base_instructions %.60q", slug, bi)
		}
	}

	// The list is kept: with the seller gone and the coordinator listing
	// nothing, the buyer still answers from what it has, without asking.
	svc.Close()
	waitFor(t, 10*time.Second, "the offer to be withdrawn", func() bool { return len(m.coord.Engine.Models()) == 0 })
	start := time.Now()
	again := byKey(getList(t, proxy.URL+"/v1/models?client_version=0.166.0")["models"], "slug")
	if len(again) != 3 || time.Since(start) > time.Second {
		t.Fatalf("the second list must come from the buyer's copy at once: %d models in %v", len(again), time.Since(start))
	}
}

// loopingUpstream is a chat model server stuck in a loop: it streams the
// same few sentences until the request is given up, and says whether it
// was (stopped) before its 5,000 pieces were out.
func loopingUpstream(t *testing.T, stopped *atomic.Bool) *httptest.Server {
	words := []string{"Now. ", "Writing. ", "Go. ", "Let me write. ", "OK. ", "Producing.\n"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := range 5000 {
			piece, _ := json.Marshal(words[i%len(words)])
			if _, err := fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%s}}]}\n\n", piece); err != nil {
				stopped.Store(true)
				return
			}
			fl.Flush()
			select {
			case <-r.Context().Done():
				stopped.Store(true)
				return
			case <-time.After(time.Millisecond):
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5000}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoopGuardCutsALoopingAnswer(t *testing.T) {
	cases := []struct {
		api, path, body string
		closing         []string // the events that end the cut answer, in order
	}{
		{protocol.APIChat, "/v1/chat/completions", `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			[]string{`"finish_reason":"length"`}},
		{protocol.APIMessages, "/v1/messages", `{"model":"test-model","stream":true,"max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`,
			[]string{`"content_block_stop"`, `"stop_reason":"max_tokens"`, `"message_stop"`}},
		{protocol.APIResponses, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`,
			[]string{`"response.output_item.done"`, `"response.incomplete"`}},
	}
	for _, c := range cases {
		t.Run(c.api, func(t *testing.T) {
			m := newMarket(t)
			var stopped atomic.Bool
			m.addNodeCfg(loopingUpstream(t, &stopped).URL, priceIn, priceOut, nil, nil)
			m.waitOffers(1)
			cl, _, _ := m.addBuyer(50_000_000)
			proxy := httptest.NewServer((&buyer.Proxy{Client: cl, BudgetMicros: 20_000_000}).Handler())
			t.Cleanup(proxy.Close)

			resp, err := http.Post(proxy.URL+c.path, "application/json", bytes.NewReader([]byte(c.body)))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var data []string
			trailer := ""
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 1<<20), 8<<20)
			for sc.Scan() {
				line := sc.Text()
				if p, ok := strings.CutPrefix(line, "data: "); ok {
					data = append(data, p)
				} else if strings.HasPrefix(line, ": sharetoken") {
					trailer = line
				}
			}
			if !strings.Contains(trailer, "loop_guard=text_loop") {
				t.Fatalf("no loop guard verdict; trailer %q, %d events", trailer, len(data))
			}
			if len(data) > 2000 {
				t.Fatalf("the loop ran on for %d events", len(data))
			}
			tail := data
			if c.api == protocol.APIChat {
				if data[len(data)-1] != "[DONE]" {
					t.Fatalf("chat stream must end in [DONE]: %q", data[len(data)-1])
				}
				tail = data[:len(data)-1]
			}
			tail = tail[len(tail)-len(c.closing):]
			for i, want := range c.closing {
				if !strings.Contains(tail[i], want) {
					t.Fatalf("closing event %d: %s, want %s", i, tail[i], want)
				}
			}
			waitFor(t, 10*time.Second, "the node to stop the looping model", stopped.Load)
		})
	}
}
