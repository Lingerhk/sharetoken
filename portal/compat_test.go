package portal

import (
	"net/http"
	"strings"
	"testing"
)

// A newer client may send fields this portal does not know yet: they are
// ignored, and in particular grant nothing. Malformed JSON is still refused.
func TestRequestsIgnoreUnknownFields(t *testing.T) {
	_, a := newPortal(t, Config{OpenRegistration: true})

	var reg AuthResponse
	a.call("POST", "/api/v1/register", "", map[string]any{
		"username": "alice", "password": "correct horse",
		"client_feature": "from a newer client", "is_admin": true,
	}, 201, &reg)
	if reg.User.Username != "alice" || reg.User.Admin {
		t.Fatalf("register: %+v", reg.User)
	}

	resp, err := http.Post(a.url+"/api/v1/login", "application/json", strings.NewReader(`{"username":`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status %d", resp.StatusCode)
	}
}
