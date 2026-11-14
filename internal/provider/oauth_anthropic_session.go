package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type ClaudeOrganization struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type SessionLogin struct {
	ID            string               `json:"id"`
	Organizations []ClaudeOrganization `json:"organizations"`
}

// StartSessionLogin discovers the user's organizations. The cookie is used
// only for this call; the caller supplies it again when selecting an account.
func StartSessionLogin(ctx context.Context, home, name, sessionKey string, options LoginOptions) (*SessionLogin, error) {
	l, err := StartProviderLogin(home, name, KindAnthropic, options)
	if err != nil {
		return nil, err
	}
	organizations, err := l.claudeOrganizations(ctx, sessionKey)
	if err != nil {
		discardLogin(l.ID)
		return nil, err
	}
	return &SessionLogin{ID: l.ID, Organizations: organizations}, nil
}

func FinishSessionLogin(ctx context.Context, home, id, sessionKey, organization, name string) (*Added, error) {
	loginMu.Lock()
	l := logins[id]
	loginMu.Unlock()
	if l == nil || l.Type != KindAnthropic || time.Since(l.created) > loginTTL {
		return nil, errors.New("this login has expired: start again")
	}
	// Recheck membership instead of constructing an authorization URL from an
	// unchecked organization ID. Never select a team account implicitly.
	organizations, err := l.claudeOrganizations(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(organizations, func(o ClaudeOrganization) bool { return o.UUID == organization }) {
		return nil, errors.New("select an organization from this login")
	}
	scope := strings.TrimPrefix(claudeScope, "org:create_api_key ")
	if l.options.Mode == "inference" {
		scope = "user:inference"
	}
	body, _ := json.Marshal(map[string]string{
		"response_type": "code", "client_id": claudeClientID, "organization_uuid": organization,
		"redirect_uri": claudeRedirect, "scope": scope, "state": l.state,
		"code_challenge": challengeS256(l.verifier), "code_challenge_method": "S256",
	})
	var result struct {
		Redirect string `json:"redirect_uri"`
	}
	if err := l.claudeSessionRequest(ctx, sessionKey, http.MethodPost, "/v1/oauth/"+url.PathEscape(organization)+"/authorize", body, &result); err != nil {
		return nil, err
	}
	u, err := url.Parse(result.Redirect)
	if err != nil || u.Query().Get("code") == "" || u.Query().Get("state") != l.state {
		return nil, errors.New("authorization returned an invalid callback state")
	}
	return l.FinishAndSave(ctx, home, result.Redirect, name)
}

func discardLogin(id string) { loginMu.Lock(); delete(logins, id); loginMu.Unlock() }

func (l *Login) claudeOrganizations(ctx context.Context, key string) ([]ClaudeOrganization, error) {
	var organizations []ClaudeOrganization
	if err := l.claudeSessionRequest(ctx, key, http.MethodGet, "/api/organizations", nil, &organizations); err != nil {
		return nil, err
	}
	if len(organizations) == 0 {
		return nil, errors.New("this session has no organizations")
	}
	return organizations, nil
}

func (l *Login) claudeSessionRequest(ctx context.Context, key, method, path string, body []byte, out any) error {
	key = strings.TrimSpace(key)
	if key == "" || (&http.Cookie{Name: "sessionKey", Value: key}).Valid() != nil {
		return errors.New("enter a valid sessionKey cookie value")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client, err := tokenHTTPClient("session:"+l.ID, l.options.Transport)
	if err != nil {
		return err
	}
	defer releaseHTTPClient("session:" + l.ID + ":token")
	req, err := http.NewRequestWithContext(ctx, method, "https://claude.ai"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.AddCookie(&http.Cookie{Name: "sessionKey", Value: key})
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("Referer", "https://claude.ai/new")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Claude session authorization answered HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
