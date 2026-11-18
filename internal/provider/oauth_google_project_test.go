package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// googleBackend answers loadCodeAssist with load and onboardUser with
// onboard, and keeps the bodies it was sent by method. Cloud Resource
// Manager refuses to list projects (403), as for an account whose login
// may not.
func googleBackend(t *testing.T, load, onboard string) (googleApp, map[string][]map[string]any) {
	return googleBackendWith(t, load, onboard, "")
}

// googleBackendWith is googleBackend whose Cloud Resource Manager lists
// projects ("" for a 403).
func googleBackendWith(t *testing.T, load, onboard, projects string) (googleApp, map[string][]map[string]any) {
	t.Helper()
	sent := map[string][]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects" {
			sent["projects"] = append(sent["projects"], map[string]any{"auth": r.Header.Get("Authorization")})
			if projects == "" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`)
				return
			}
			_, _ = io.WriteString(w, projects)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, ":")+1:]
		sent[method] = append(sent[method], body)
		switch method {
		case "loadCodeAssist":
			_, _ = io.WriteString(w, load)
		case "onboardUser":
			if onboard == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`)
				return
			}
			_, _ = io.WriteString(w, onboard)
		}
	}))
	t.Cleanup(srv.Close)
	old := onboardPoll
	onboardPoll = time.Millisecond
	t.Cleanup(func() { onboardPoll = old })
	oldCRM := resourceManagerURL
	resourceManagerURL = srv.URL + "/v1/projects"
	t.Cleanup(func() { resourceManagerURL = oldCRM })
	app := geminiApp
	app.hosts = []string{srv.URL}
	return app, sent
}

// The project a seller gave goes to loadCodeAssist, and is the one used.
func TestFindProjectSendsGivenProject(t *testing.T) {
	app, sent := googleBackend(t, `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"other"}`, "")
	tok := &Token{AccessToken: "at", ProjectID: "mine"}
	if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "mine" || tok.Plan != "standard-tier" {
		t.Fatalf("%v %+v", err, tok)
	}
	got := asJSON(t, sent["loadCodeAssist"][0])
	if !strings.Contains(got, `"cloudaicompanionProject":"mine"`) || !strings.Contains(got, `"duetProject":"mine"`) || !strings.Contains(got, `"ideType":"ANTIGRAVITY"`) {
		t.Errorf("loadCodeAssist: %s", got)
	}
	if _, ok := geminiApp.metadata["duetProject"]; ok {
		t.Error("the app's own metadata was changed")
	}
	// Without one, the backend's project is used, and none is named.
	tok = &Token{AccessToken: "at"}
	if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "other" {
		t.Fatalf("%v %+v", err, tok)
	}
	if got := asJSON(t, sent["loadCodeAssist"][1]); strings.Contains(got, "duetProject") || strings.Contains(got, "cloudaicompanionProject") {
		t.Errorf("loadCodeAssist without a project: %s", got)
	}
}

// Google's reason the account has no tier reaches the seller at login.
func TestFindProjectSaysWhy(t *testing.T) {
	const ineligible = `"ineligibleTiers":[{"tierId":"free-tier","reasonCode":"INELIGIBLE_ACCOUNT","reasonMessage":"Your account belongs to an organization and needs a Gemini Code Assist license."}]`
	for name, c := range map[string]struct{ load, onboard, want string }{
		"tier needs a project": {
			load: `{"allowedTiers":[{"id":"standard-tier","isDefault":true,"userDefinedCloudaicompanionProject":true}],` + ineligible + `}`,
			want: "Google says: Your account belongs to an organization and needs a Gemini Code Assist license. (INELIGIBLE_ACCOUNT). this Google account's Code Assist tier (standard-tier) bills a Google Cloud project",
		},
		"no tier": {
			load: `{` + ineligible + `}`,
			want: "needs a Gemini Code Assist license. (INELIGIBLE_ACCOUNT). this Google account has no Code Assist tier: log in again and give a Google Cloud project ID of the account with -project",
		},
		"tier without project": {
			load: `{"currentTier":{"id":"standard-tier"}}`,
			want: "this Google account has Code Assist (standard-tier) but no project: log in again",
		},
		"onboarding refused": {
			load: `{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`,
			want: "this Google account has no Code Assist project (signing it up for free-tier: ",
		},
	} {
		app, sent := googleBackend(t, c.load, c.onboard)
		err := app.findProject(context.Background(), &Token{AccessToken: "at"})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		if name != "onboarding refused" && len(sent["onboardUser"]) != 0 {
			t.Errorf("%s: onboarded", name)
		}
	}
}

// A new account on a tier that makes its own project is signed up for it.
func TestFindProjectOnboards(t *testing.T) {
	app, sent := googleBackend(t, `{"allowedTiers":[{"id":"legacy-tier"},{"id":"free-tier","isDefault":true}]}`,
		`{"done":true,"response":{"cloudaicompanionProject":{"id":"proj-1"}}}`)
	tok := &Token{AccessToken: "at"}
	if err := app.findProject(context.Background(), tok); err != nil || tok.ProjectID != "proj-1" || tok.Plan != "free-tier" {
		t.Fatalf("%v %+v", err, tok)
	}
	if got := asJSON(t, sent["onboardUser"][0]); !strings.Contains(got, `"tierId":"free-tier"`) {
		t.Errorf("onboardUser: %s", got)
	}
}

// The fallback list names the newer Gemini CLI models.
func TestGeminiCLIModelsFallback(t *testing.T) {
	for _, id := range []string{"gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash", "gemini-4-argon", "gemini-4-carbon"} {
		found := false
		for _, m := range geminiCLIModels {
			found = found || m == id
		}
		if !found {
			t.Errorf("%s missing", id)
		}
	}
}
