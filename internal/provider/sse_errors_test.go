package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/node"
)

// An error written as a bare JSON line in a stream, or a 200 that is one
// JSON error, is the job's error with the server's own words and status, not
// "the stream ended".
func TestBareStreamErrorsAreRead(t *testing.T) {
	for _, c := range []struct {
		name, ct, body string
		status         int
	}{
		{"bare line", "text/event-stream", "data: {\"x\":1}\n\n{\"error\":{\"code\":429,\"message\":\"Resource has been exhausted\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n", 429},
		{"json error", "application/json", `{"error":{"type":"invalid_request_error","message":"prompt is too long: 300000 tokens > 200000 maximum"}}`, 400},
		{"json answer", "application/json", `{"id":"x","choices":[]}`, 502},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.ct)
			fmt.Fprint(w, c.body)
		}))
		var got []string
		err := postSSE(context.Background(), srv.URL, http.Header{}, []byte(`{}`), func(d []byte) error { got = append(got, string(d)); return nil })
		srv.Close()
		var se *node.StatusError
		if !errors.As(err, &se) || se.Status != c.status {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.name != "json answer" && !strings.Contains(se.Body, "message") {
			t.Fatalf("%s: the server's words are lost: %s", c.name, se.Body)
		}
		if c.name == "bare line" && len(got) != 1 {
			t.Fatalf("events before the error: %v", got)
		}
	}
}
