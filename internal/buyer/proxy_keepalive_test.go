package buyer

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/protocol"
)

// Heartbeats reach the client at most every keepaliveEvery, a stream with
// nothing sent yet is opened for them only after keepaliveOpen, and each
// client gets them in a form it counts.
func TestKeepalivesByClient(t *testing.T) {
	for _, c := range []struct {
		api   string
		codex bool
		want  string
	}{
		{protocol.APIMessages, false, "event: ping\ndata: {\"type\":\"ping\"}\n\n"},
		{protocol.APIResponses, true, "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"},
		{protocol.APIResponses, false, ": keepalive\n\n"},
		{protocol.APIChat, false, ": keepalive\n\n"},
	} {
		rec := httptest.NewRecorder()
		now := time.Now()
		s := &sseWriter{w: rec, api: c.api, codex: c.codex, begun: now, lastWrite: now}
		s.keepalive()
		if s.started || rec.Body.Len() != 0 {
			t.Fatalf("%s: a keepalive right away", c.api)
		}
		s.begun, s.lastWrite = now.Add(-keepaliveOpen), now.Add(-keepaliveOpen)
		s.keepalive()
		s.keepalive() // too soon after the first
		if !s.started || rec.Code != 200 || rec.Body.String() != c.want {
			t.Fatalf("%s codex=%v: %d %q", c.api, c.codex, rec.Code, rec.Body.String())
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("%s: content type %q", c.api, rec.Header().Get("Content-Type"))
		}
	}
}
