package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/node"
)

// TransportOptions controls a provider's outbound connections. An empty mode
// uses the process proxy environment; direct explicitly ignores it.
type TransportOptions struct {
	ProxyMode       string `json:"proxy_mode,omitempty"`
	ProxyURL        string `json:"proxy_url,omitempty"`
	TLSProfile      string `json:"tls_profile,omitempty"`
	TokenTLSProfile string `json:"token_tls_profile,omitempty"`
}

func mergeTransport(old, next *TransportOptions) *TransportOptions {
	if next == nil {
		return old
	}
	merged := TransportOptions{}
	if old != nil {
		merged = *old
	}
	if next.ProxyMode != "" || next.ProxyURL != "" {
		merged.ProxyMode, merged.ProxyURL = next.ProxyMode, next.ProxyURL
		if merged.ProxyURL != "" && merged.ProxyMode == "" {
			merged.ProxyMode = "custom"
		}
	}
	if next.TLSProfile != "" {
		merged.TLSProfile = next.TLSProfile
	}
	if next.TokenTLSProfile != "" {
		merged.TokenTLSProfile = next.TokenTLSProfile
	}
	return &merged
}

func (o TransportOptions) validate() error {
	for _, profile := range []string{o.TLSProfile, o.TokenTLSProfile} {
		if profile != "" && profile != "native" && profile != "chrome" {
			return fmt.Errorf("TLS profile must be native or chrome")
		}
	}
	switch o.ProxyMode {
	case "", "environment", "direct":
		if o.ProxyURL != "" {
			return fmt.Errorf("proxy_url requires proxy_mode=custom")
		}
	case "custom":
		u, err := url.Parse(o.ProxyURL)
		if err != nil || u.Hostname() == "" || u.Fragment != "" || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("invalid proxy URL")
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("proxy must use http, https, socks5 or socks5h")
		}
	default:
		return fmt.Errorf("proxy_mode must be environment, direct or custom")
	}
	return nil
}

// summary never returns proxy credentials, even from malformed configuration.
func (o TransportOptions) summary() string {
	if o.ProxyMode != "custom" {
		return ""
	}
	u, err := url.Parse(o.ProxyURL)
	if err != nil || u.Hostname() == "" {
		return "configured"
	}
	return u.Scheme + "://" + u.Host
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type httpClientKey struct{}

func withHTTPClient(ctx context.Context, client httpDoer) context.Context {
	return context.WithValue(ctx, httpClientKey{}, client)
}

func clientForContext(ctx context.Context) httpDoer {
	if client, ok := ctx.Value(httpClientKey{}).(httpDoer); ok {
		return client
	}
	return httpClient
}

var outboundClients = struct {
	sync.Mutex
	byOwner map[string]cachedHTTPClient
}{byOwner: map[string]cachedHTTPClient{}}

type cachedHTTPClient struct {
	options TransportOptions
	client  *http.Client
}

func providerHTTPClient(owner string, options *TransportOptions) (httpDoer, error) {
	o := TransportOptions{}
	if options != nil {
		o = *options
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	// Keep the default injectable for tests and callers using the default path.
	if owner == "" && options == nil {
		return httpClient, nil
	}
	outboundClients.Lock()
	defer outboundClients.Unlock()
	if old, ok := outboundClients.byOwner[owner]; ok && old.options == o {
		return old.client, nil
	}
	c := makeProviderHTTPClient(o)
	if old, ok := outboundClients.byOwner[owner]; ok {
		old.client.CloseIdleConnections()
	}
	outboundClients.byOwner[owner] = cachedHTTPClient{options: o, client: c}
	return c, nil
}

var makeProviderHTTPClient = func(o TransportOptions) *http.Client {
	if o.TLSProfile == "chrome" {
		return chromeHTTPClient(o)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.ProxyMode == "direct" {
		tr.Proxy = nil
	}
	if o.ProxyMode == "custom" {
		u, _ := url.Parse(o.ProxyURL)
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr}
}

func (e *Entry) httpClient() (httpDoer, error) {
	return providerHTTPClient(e.ID, e.Transport)
}

func tokenHTTPClient(owner string, options *TransportOptions) (httpDoer, error) {
	o := TransportOptions{}
	if options != nil {
		o = *options
	}
	o.TLSProfile = o.TokenTLSProfile
	return providerHTTPClient(owner+":token", &o)
}

func releaseHTTPClient(owner string) {
	outboundClients.Lock()
	defer outboundClients.Unlock()
	if c, ok := outboundClients.byOwner[owner]; ok {
		c.client.CloseIdleConnections()
		delete(outboundClients.byOwner, owner)
	}
}

type outboundUpstream struct {
	up     node.Upstream
	client httpDoer
	owner  string
}

func (u *outboundUpstream) Stream(ctx context.Context, body []byte, emit func([]byte) error) error {
	if ready := u.ReadyAt(); ready.After(time.Now()) {
		return &node.StatusError{Status: 429, Body: "provider is temporarily rate limited", RetryAt: ready}
	}
	err := u.up.Stream(withHTTPClient(ctx, u.client), body, emit)
	recordProviderStatus(u.owner, err)
	return err
}

func (u *outboundUpstream) ReadyAt() time.Time { return providerStatus(u.owner).RetryAt }

// tokenError returns only the endpoint's error fields, never token payloads.
func tokenError(status int, code, description string) error {
	return fmt.Errorf("the login server answered HTTP %d: %s", status, clip(strings.TrimSpace(code+" "+description), 300))
}
