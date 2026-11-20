package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/protocol"
)

// An error a chat server sends inside its stream fails the job, and so does a
// stream that stops before a finish_reason or [DONE]: neither is an answer.
func TestOpenAICompatStreamErrors(t *testing.T) {
	stream := func(lines ...string) (int, error) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, l := range lines {
				fmt.Fprintf(w, "data: %s\n\n", l)
			}
		}))
		defer srv.Close()
		n := 0
		err := (&OpenAICompat{BaseURL: srv.URL}).Stream(context.Background(), &Request{API: protocol.APIChat, Body: []byte(`{}`)}, func([]byte) error {
			n++
			return nil
		})
		return n, err
	}
	const chunk = `{"choices":[{"index":0,"delta":{"content":"hel"},"finish_reason":null}]}`

	var se *StatusError
	if n, err := stream(chunk, `{"error":{"message":"CUDA out of memory","type":"server_error"}}`, "[DONE]"); !errors.As(err, &se) ||
		se.Status != http.StatusBadGateway || !strings.Contains(se.Body, "CUDA out of memory") || n != 1 {
		t.Fatalf("error object: %d chunks, %v", n, err)
	}
	if _, err := stream(`{"error":"model not loaded"}`); !errors.As(err, &se) || !strings.Contains(se.Body, "model not loaded") {
		t.Fatalf("error string: %v", err)
	}
	if _, err := stream(chunk); err == nil {
		t.Fatal("a stream without finish_reason or [DONE] was accepted")
	}
	if n, err := stream(chunk, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`); err != nil || n != 2 {
		t.Fatalf("finish_reason: %d chunks, %v", n, err)
	}
	if n, err := stream(chunk, "[DONE]"); err != nil || n != 1 {
		t.Fatalf("[DONE]: %d chunks, %v", n, err)
	}
	// A chunk that only mentions an error is a chunk.
	if n, err := stream(`{"choices":[{"index":0,"delta":{"content":"the \"error\" field"},"finish_reason":"stop"}],"error":null}`); err != nil || n != 1 {
		t.Fatalf("chunk with a null error: %d chunks, %v", n, err)
	}
}

// What an adapter lets the node bill of the prompt: a cap, and a discount
// off the model server's own count only.
func TestBillingPrompt(t *testing.T) {
	u := protocol.Usage{PromptTokens: 1000, CompletionTokens: 7, CacheReadTokens: 600, CacheWriteTokens: 300}
	if got := (&Billing{}).Prompt(u, true); got != u {
		t.Fatalf("no adjustment: %+v", got)
	}
	// The uncached part goes first, then cache writes.
	got := (&Billing{PromptDiscount: 150}).Prompt(u, true)
	if got.PromptTokens != 850 || got.CacheReadTokens != 600 || got.CacheWriteTokens != 250 || got.CompletionTokens != 7 {
		t.Fatalf("discount: %+v", got)
	}
	if got := (&Billing{PromptDiscount: 150}).Prompt(u, false); got != u {
		t.Fatalf("an estimate was discounted: %+v", got)
	}
	if got := (&Billing{PromptDiscount: 5000}).Prompt(u, true); got.PromptTokens != 0 || got.CacheReadTokens != 0 || got.CacheWriteTokens != 0 {
		t.Fatalf("discount above the prompt: %+v", got)
	}
	if got := (&Billing{PromptCap: 400}).Prompt(u, false); got.PromptTokens != 400 || got.CacheReadTokens != 400 {
		t.Fatalf("cap: %+v", got)
	}
}

// A summary the model did not finish fails the compaction: the buyer keeps
// its conversation rather than half a summary of it.
func TestSummarizerRefusesACutSummary(t *testing.T) {
	run := func(events ...string) (string, error) {
		var last string
		s := &summarizer{emit: func(p []byte) error {
			last = string(p)
			return nil
		}}
		for _, ev := range events {
			if err := s.event([]byte(ev)); err != nil {
				return last, err
			}
		}
		return last, s.finish()
	}
	const delta = `{"type":"response.output_text.delta","delta":"<summary>half of"}`
	for _, end := range []string{
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
		`{"type":"response.done","response":{"status":"incomplete"}}`,
	} {
		if last, err := run(delta, end); err == nil || strings.Contains(last, "compaction") {
			t.Fatalf("%s: err %v, last %s", end, err, last)
		}
	}
	last, err := run(`{"type":"response.output_text.delta","delta":"<summary>all of it</summary>"}`, `{"type":"response.completed","response":{"status":"completed"}}`)
	if err != nil || !strings.Contains(last, apicompat.EncodeCompaction("all of it")) {
		t.Fatalf("whole summary: %v %s", err, last)
	}
}
