package mailer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResendDelivery(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	var keys []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" || r.Header.Get("Authorization") != "Bearer re_test" {
			t.Errorf("request %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies, keys = append(bodies, b), append(keys, r.Header.Get("Idempotency-Key"))
		code := status
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write([]byte(`{"id":"abc-123"}`))
		} else {
			_, _ = w.Write([]byte(`{"message":"nope"}`))
		}
	}))
	defer srv.Close()
	api, err := NewResend("re_test", srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	st := newStore(t)
	w := NewWorker(WorkerConfig{Store: st, API: api, SendTimeout: 10 * time.Second})
	send := func(code int) int64 {
		t.Helper()
		mu.Lock()
		status = code
		mu.Unlock()
		id, err := st.Enqueue(Message{Kind: KindVerify, From: "ShareToken <no-reply@example.test>", To: "Ann <ann@example.org>",
			ReplyTo: "help@example.test", Subject: "code\r\nBcc: x@y", Text: "123456", HTML: "<b>123456</b>"})
		if err != nil {
			t.Fatal(err)
		}
		deliverAll(t, st, w)
		return id
	}

	id := send(http.StatusOK)
	m := outRow(t, st, id)
	if m.Status != StatusSent || m.Remote != "resend abc-123" || m.MessageID == "" {
		t.Fatalf("not sent: %+v", m)
	}
	b := bodies[0]
	if b["from"] != "\"ShareToken\" <no-reply@example.test>" || b["to"].([]any)[0] != "ann@example.org" || b["subject"] != "code Bcc: x@y" ||
		b["text"] != "123456" || b["html"] != "<b>123456</b>" || b["reply_to"] != "help@example.test" {
		t.Fatalf("request body %v", b)
	}
	if h, _ := b["headers"].(map[string]any); h["Auto-Submitted"] != "auto-generated" {
		t.Fatalf("headers %v", b["headers"])
	}
	if keys[0] != m.MessageID {
		t.Fatalf("idempotency key %q, message id %q", keys[0], m.MessageID)
	}

	// The message itself is refused: final.
	if m := outRow(t, st, send(http.StatusUnprocessableEntity)); m.Status != StatusFailed || !strings.Contains(m.LastError, "nope") {
		t.Fatalf("422: %+v", m)
	}
	// The key or the domain is wrong, or the service is down: kept for a retry.
	for _, code := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		if m := outRow(t, st, send(code)); m.Status != StatusQueued || m.LastError == "" {
			t.Fatalf("%d: %+v", code, m)
		}
	}
}
