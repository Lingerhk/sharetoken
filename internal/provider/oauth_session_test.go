package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (fn testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestSessionLoginSelectsOrganizationAndKeepsCookieEphemeral(t *testing.T) {
	home := t.TempDir()
	oldFactory := makeProviderHTTPClient
	t.Cleanup(func() { makeProviderHTTPClient = oldFactory })
	var state string
	var scopes []string
	tokenCalls := 0
	makeProviderHTTPClient = func(options TransportOptions) *http.Client {
		if options.ProxyMode != "direct" {
			t.Error("login dropped outbound settings")
		}
		return &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
			status, body := 200, ""
			switch r.URL.Path {
			case "/api/organizations":
				cookie, err := r.Cookie("sessionKey")
				if err != nil || cookie.Value != "temporary-cookie" {
					t.Error("missing session cookie")
				}
				body = `[{"uuid":"org-a","name":"Personal"},{"uuid":"org-b","name":"Team"}]`
			case "/v1/oauth/org-b/authorize":
				var request map[string]string
				_ = json.NewDecoder(r.Body).Decode(&request)
				state = request["state"]
				scopes = append(scopes, request["scope"])
				if request["organization_uuid"] != "org-b" || request["code_challenge"] == "" {
					t.Error("wrong authorization payload")
				}
				body = fmt.Sprintf(`{"redirect_uri":"%s?code=code123&state=%s"}`, claudeRedirect, state)
			case "/v1/oauth/token":
				tokenCalls++
				if r.Header.Get("Cookie") != "" {
					t.Error("cookie leaked to token endpoint")
				}
				var request map[string]string
				_ = json.NewDecoder(r.Body).Decode(&request)
				if request["state"] != state || request["code"] != "code123" || request["code_verifier"] == "" {
					t.Error("code/PKCE mismatch")
				}
				if r.Header.Get("Accept") != "application/json, text/plain, */*" {
					t.Error("token Accept overridden")
				}
				body = `{"access_token":"access","refresh_token":"refresh","expires_in":3600,"scope":"user:inference","account":{"uuid":"account-123","email_address":"a@example.test"},"organization":{"uuid":"org-b"}}`
			case "/v1/models":
				status = 503
				body = `{"error":{"type":"unavailable"}}`
			default:
				t.Errorf("unexpected request %s", r.URL)
				status = 500
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}
	}
	opts := LoginOptions{Mode: "inference", Transport: &TransportOptions{ProxyMode: "direct"}, Anthropic: &AnthropicOptions{Profile: "claude-code"}}
	l, err := StartSessionLogin(context.Background(), home, "cookie-account", "temporary-cookie", opts)
	if err != nil || len(l.Organizations) != 2 {
		t.Fatalf("organizations: %+v %v", l, err)
	}
	if _, err := FinishSessionLogin(context.Background(), home, l.ID, "temporary-cookie", "not-a-member", "cookie-account"); err == nil || tokenCalls != 0 {
		t.Fatal("unselected organization accepted")
	}
	added, err := FinishSessionLogin(context.Background(), home, l.ID, "temporary-cookie", "org-b", "cookie-account")
	if err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 || len(scopes) != 1 || scopes[0] != "user:inference" {
		t.Fatal("wrong scope or repeated exchange")
	}
	f, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	e := f.Get("cookie-account")
	if e.OAuth.AccountUUID != "account-123" || e.OAuth.AccountID != "org-b" || e.OAuth.RefreshToken != "refresh" {
		t.Fatal("lost account identity")
	}
	raw, _ := os.ReadFile(Path(home))
	view, _ := json.Marshal(added)
	if strings.Contains(string(raw), "temporary-cookie") || strings.Contains(string(view), "temporary-cookie") || strings.Contains(string(view), "refresh_token") {
		t.Fatal("cookie or token exposed")
	}
	releaseHTTPClient(e.ID)
}
