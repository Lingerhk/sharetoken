package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sharetoken/accounts"
	"sharetoken/internal/version"
	"sharetoken/portal"
)

func TestUpdateFor(t *testing.T) {
	c := portal.ClientVersions{Min: "1.0.0", Recommended: "1.1.0", Latest: "1.2.0", DownloadURL: "https://x/d"}
	for v, want := range map[string][2]bool{ // available, required
		"0.9.0":         {true, true},
		"1.0.0":         {true, false},
		"1.1.0":         {true, false},
		"1.2.0":         {false, false},
		"1.3.0":         {false, false},
		"1.0.0-dev.abc": {true, false},
		"0.9.0-dev.abc": {true, true},
		"dev":           {false, false},
	} {
		u := updateFor(v, c)
		if u.Available != want[0] || u.Required != want[1] || u.Latest != "1.2.0" || u.DownloadURL != "https://x/d" {
			t.Errorf("%s: %+v, want %v", v, u, want)
		}
	}
	if u := updateFor("1.0.0", portal.ClientVersions{}); u.Available || u.Required {
		t.Errorf("no policy: %+v", u)
	}
}

// Every request to the portal says what is asking, and refusals keep their code.
func TestPortalRequestsCarryVersion(t *testing.T) {
	var ver, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ver, ua = r.Header.Get(portal.ClientVersionHeader), r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"upgrade_required","message":"too old"}}`))
	}))
	defer srv.Close()
	err := NewPortalClient(srv.URL, "", "").Do(context.Background(), http.MethodGet, "/api/v1/me", nil, nil)
	pe, ok := err.(*PortalError)
	if !ok || pe.Code != "upgrade_required" || pe.Status != http.StatusUpgradeRequired {
		t.Fatalf("%v", err)
	}
	if ver != version.Version || !strings.HasPrefix(ua, "sharetoken-client/"+version.Version) {
		t.Fatalf("version %q, user agent %q", ver, ua)
	}
}

// A configuration of an unknown format is not guessed at.
func TestConfigVersion(t *testing.T) {
	home := t.TempDir()
	c := DefaultConfig()
	c.User = "alice"
	if err := SaveConfig(home, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(home)
	if err != nil || got.User != "alice" || got.Version != ConfigVersion {
		t.Fatalf("%+v %v", got, err)
	}
	writeConfig := func(s string) {
		if err := osWriteConfig(home, s); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(`{"version":99,"env":"dev","user":"alice"}`)
	if _, err := LoadConfig(home); err == nil || !strings.Contains(err.Error(), "newer client") {
		t.Fatalf("newer format: %v", err)
	}
	writeConfig(`{"env":"dev","user":"alice"}`)
	got, err = LoadConfig(home)
	if err != nil || got.User != "" {
		t.Fatalf("unversioned format is signed out: %+v %v", got, err)
	}
}

func osWriteConfig(home, s string) error {
	return os.WriteFile(filepath.Join(home, ConfigFile), []byte(s), 0o600)
}

// The session says what the daemon is and what the portal recommends.
func TestSessionReportsVersionAndUpdate(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	t.Setenv("SHARETOKEN_PORTAL", e.url)
	if err := accounts.SaveVersionPolicy(e.portal.Store().DB, accounts.VersionPolicy{LatestClient: "99.0.0", DownloadURL: "https://example.com/d", AllowDev: true}); err != nil {
		t.Fatal(err)
	}
	d := e.daemon()
	d.checkUpdate(context.Background())
	s := d.Session()
	if s.Version != version.Version || s.APIVersion != version.API {
		t.Fatalf("%+v", s)
	}
	if s.Update.Latest != "99.0.0" || s.Update.DownloadURL != "https://example.com/d" {
		t.Fatalf("%+v", s.Update)
	}
	got := updateFor("1.0.0", portal.ClientVersions{Latest: s.Update.Latest})
	if !got.Available || got.Required {
		t.Fatalf("%+v", got)
	}
}
