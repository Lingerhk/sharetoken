package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/cli"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/p2p"
	"sharetoken/internal/portal"
	"sharetoken/internal/relaypass"
	"sharetoken/internal/version"
)

// PortalClient calls the portal's HTTP API.
type PortalClient struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// NewPortalClient returns a client for the portal at base. A non-empty pin
// ("sha256:<hex>", see portal.CertFingerprint) accepts exactly that server
// certificate, for portals with a self-signed one; without a pin the
// certificate must chain to a trusted root as usual.
func NewPortalClient(base, token, pin string) *PortalClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if pin != "" {
		tr.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// The pin replaces chain and name verification: only the
			// certificate the user accepted is trusted.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 || !strings.EqualFold(portal.CertFingerprint(raw[0]), pin) {
					return fmt.Errorf("portal certificate does not match the pinned fingerprint %s", pin)
				}
				return nil
			},
		}
	}
	return &PortalClient{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: tr}}
}

// PortalCert describes the certificate a portal presents.
type PortalCert struct {
	Fingerprint string    // what to pin
	Trusted     bool      // chains to a system root for this host name: no pin needed
	Subject     string    // certificate subject
	NotAfter    time.Time // expiry
}

// ProbePortal fetches the certificate of an https:// portal without trusting
// it, so the user can be shown its fingerprint before logging in.
func ProbePortal(ctx context.Context, base string) (*PortalCert, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("not an https URL: %s", base)
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("portal presented no certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, verr := certs[0].Verify(x509.VerifyOptions{DNSName: u.Hostname(), Intermediates: inter})
	return &PortalCert{Fingerprint: portal.CertFingerprint(certs[0].Raw), Trusted: verr == nil,
		Subject: certs[0].Subject.String(), NotAfter: certs[0].NotAfter}, nil
}

// InsecurePortalURL reports whether base is plain HTTP to another machine,
// where sign-in codes and tokens would cross the network in clear.
func InsecurePortalURL(base string) bool {
	u, err := url.Parse(base)
	return err == nil && u.Scheme == "http" && !cli.IsLoopback(u.Hostname())
}

// PortalError is an error response from the portal.
type PortalError struct {
	Status  int
	Code    string
	Message string
	// Challenge is the id of a sign-in waiting for its second factor
	// (Code mfa_required or mfa_setup_required).
	Challenge string
}

func (e *PortalError) Error() string { return fmt.Sprintf("portal: %s (%s)", e.Message, e.Code) }

// IsUnauthorized reports whether err means the token is missing, expired or revoked.
func IsUnauthorized(err error) bool {
	pe, ok := err.(*PortalError)
	return ok && pe.Status == http.StatusUnauthorized
}

// Do sends a request with an optional JSON body and decodes a JSON reply into out.
func (p *PortalClient) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.Base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set(portal.ClientVersionHeader, version.Version)
	req.Header.Set(portal.ClientPlatformHeader, version.Platform())
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var eb struct {
			portal.ErrorBody
			Challenge string `json:"challenge"`
		}
		if json.Unmarshal(data, &eb) == nil && eb.Error.Code != "" {
			return &PortalError{Status: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message, Challenge: eb.Challenge}
		}
		return &PortalError{Status: resp.StatusCode, Code: "http_" + fmt.Sprint(resp.StatusCode), Message: strings.TrimSpace(string(data))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// AuthResult is a successful sign-in.
type AuthResult struct {
	User    accounts.User `json:"user"`
	Token   string        `json:"token"`
	Created bool          `json:"created,omitempty"` // the address had no account before
}

// CodeSent is the portal's answer to a request for a sign-in code.
type CodeSent struct {
	NewAccount       bool `json:"new_account"` // signing in will create the account
	CooldownSeconds  int  `json:"cooldown_seconds"`
	ExpiresInSeconds int  `json:"expires_in_seconds"`
}

// RequestCode asks the portal to mail a sign-in code to email. invite is the
// invite code a new account needs on portals that require one.
func (p *PortalClient) RequestCode(ctx context.Context, email, invite string) (*CodeSent, error) {
	var out CodeSent
	err := p.Do(ctx, http.MethodPost, "/api/v1/auth/code", map[string]string{"email": email, "invite_code": invite}, &out)
	return &out, err
}

// SignIn redeems a mailed code for a token, creating the account of a new
// address. totp is an authenticator code for accounts with two-factor
// authentication; without it such an account gets a *PortalError with Code
// mfa_required and a Challenge for CompleteMFA.
func (p *PortalClient) SignIn(ctx context.Context, email, code, invite, totp, tokenName string) (*AuthResult, error) {
	var out AuthResult
	err := p.Do(ctx, http.MethodPost, "/api/v1/auth/verify", map[string]string{"email": email, "code": code,
		"invite_code": invite, "totp": totp, "token_name": tokenName}, &out)
	return &out, err
}

// CompleteMFA finishes a sign-in that SignIn left waiting for its second factor.
func (p *PortalClient) CompleteMFA(ctx context.Context, challenge, code, tokenName string) (*AuthResult, error) {
	var out AuthResult
	err := p.Do(ctx, http.MethodPost, "/api/v1/mfa", map[string]string{"challenge": challenge, "code": code, "token_name": tokenName}, &out)
	return &out, err
}

// Info returns what the portal publishes before anyone signs in.
func (p *PortalClient) Info(ctx context.Context) (*portal.Info, error) {
	var out portal.Info
	err := p.Do(ctx, http.MethodGet, "/api/v1/info", nil, &out)
	return &out, err
}

// Logout revokes the client's token.
func (p *PortalClient) Logout(ctx context.Context) error {
	return p.Do(ctx, http.MethodPost, "/api/v1/logout", nil, nil)
}

// Network returns the coordinator's peer ID and the relays.
func (p *PortalClient) Network(ctx context.Context) (*portal.NetworkInfo, error) {
	var out portal.NetworkInfo
	err := p.Do(ctx, http.MethodGet, "/api/v1/network", nil, &out)
	return &out, err
}

// BindDevice binds keys' peer ID to the logged-in user, proving possession
// of the device key.
func (p *PortalClient) BindDevice(ctx context.Context, keys *p2p.Keys, username, name string) (*accounts.Device, error) {
	ts := time.Now().UnixNano()
	req := portal.BindRequest{Peer: keys.ID(), Name: name, UnixNano: ts, Sig: accounts.SignBind(keys.Signer, username, keys.ID(), ts)}
	var out accounts.Device
	err := p.Do(ctx, http.MethodPost, "/api/v1/devices", req, &out)
	return &out, err
}

// Me returns the signed-in user's account summary.
func (p *PortalClient) Me(ctx context.Context) (*portal.Me, error) {
	var out portal.Me
	err := p.Do(ctx, http.MethodGet, "/api/v1/me", nil, &out)
	return &out, err
}

// Jobs lists the user's jobs; role is "buy", "sell" or "".
func (p *PortalClient) Jobs(ctx context.Context, role string, limit int) ([]coordinator.JobInfo, error) {
	var out struct {
		Jobs []coordinator.JobInfo `json:"jobs"`
	}
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if role != "" {
		q.Set("role", role)
	}
	err := p.Do(ctx, http.MethodGet, "/api/v1/jobs?"+q.Encode(), nil, &out)
	return out.Jobs, err
}

// RelayPass asks the portal for a relay pass for this user's device peer.
func (p *PortalClient) RelayPass(ctx context.Context, peer string) (*relaypass.Pass, error) {
	var out relaypass.Pass
	err := p.Do(ctx, http.MethodPost, "/api/v1/relay-pass", map[string]string{"peer": peer}, &out)
	return &out, err
}
