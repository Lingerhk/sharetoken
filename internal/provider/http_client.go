package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
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

// headerTimeout is how long a model server may take to start answering. Every
// request asks for a stream, whose headers come before the model has thought
// of anything; a server that sends none (seen with a Gemini model that never
// answered) would otherwise hold a job slot until the job's own timeout.
const headerTimeout = 3 * time.Minute

var makeProviderHTTPClient = func(o TransportOptions) *http.Client {
	if o.TLSProfile == "chrome" {
		return chromeHTTPClient(o)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = providerDialer().DialContext
	tr.ResponseHeaderTimeout = headerTimeout
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

func (u *outboundUpstream) APIs() []string { return u.up.APIs() }

func (u *outboundUpstream) Stream(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	if err := u.onHold(); err != nil {
		return err
	}
	err := retryTransient(ctx, emit, func(emit func([]byte) error) error {
		return requestLimited(u.up.Stream(withHTTPClient(ctx, &limitsRecorder{inner: u.client, owner: u.owner}), r, emit))
	})
	recordProviderStatus(u.owner, err)
	if err == nil {
		noteSuccess(u.owner, time.Now())
	}
	return err
}

// transientRetries is how often a request is sent again after the model
// server failed it before answering anything.
const transientRetries = 2

var (
	transientBackoff = 500 * time.Millisecond // doubled for each retry
	// shortLimit: a rate limit that ends this soon is waited out instead of
	// failing the job and putting the provider on hold.
	shortLimit = 5 * time.Second
)

// retryTransient runs do, and again when the model server failed in a way
// that passes (overloaded, a gateway error, a broken connection, a rate
// limit of a few seconds) before any of its answer was passed on. A node has
// one account per model: without this, every such moment is a failed job.
//
// The events that only open an answer (message_start, response.created, a
// chat chunk with the role alone, pings) are held back until the first one
// with content: an overload told right after them (Anthropic's in-stream
// overloaded_error, the ChatGPT backend's response.failed) is still a
// failure before any answer, and sent again (sub2api 38eb97bd7, f1013f177).
func retryTransient(ctx context.Context, emit func([]byte) error, do func(emit func([]byte) error) error) error {
	sent := false
	var held [][]byte
	flush := func() error {
		for _, h := range held {
			if err := emit(h); err != nil {
				return err
			}
		}
		held = nil
		return nil
	}
	out := func(p []byte) error {
		if !sent && openingEvent(p) && len(held) < maxHeldOpening {
			held = append(held, append([]byte(nil), p...))
			return nil
		}
		sent = true
		if err := flush(); err != nil {
			return err
		}
		return emit(p)
	}
	for attempt := 0; ; attempt++ {
		held = nil
		err := do(out)
		wait, again := transientWait(err, attempt)
		if !again || sent || attempt >= transientRetries || ctx.Err() != nil {
			if !sent {
				// An answer of opening events alone, or a failure that
				// is not tried again: the buyer still gets what came.
				if ferr := flush(); err == nil {
					err = ferr
				}
			}
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}

// maxHeldOpening bounds the opening events held back before an answer.
const maxHeldOpening = 32

// openingEvent: see protocol.OpeningEvent.
func openingEvent(p []byte) bool { return protocol.OpeningEvent(p) }

// transientWait tells whether err is worth another try, and after how long.
func transientWait(err error, attempt int) (time.Duration, bool) {
	backoff := transientBackoff << attempt
	var se *node.StatusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
			return backoff, true
		case http.StatusTooManyRequests:
			if left := time.Until(se.RetryAt); !se.RetryAt.IsZero() && left <= shortLimit {
				return max(left, backoff), true
			}
		}
		return 0, false
	}
	// The connection failed or broke: nothing says the request was wrong.
	// A server that did not answer in time is not asked again: the buyer
	// has waited long enough to go to another node.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return 0, false
	}
	if errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) {
		return backoff, true
	}
	return 0, false
}

// outboundCompactor is an outboundUpstream whose model server has a native
// compact endpoint.
type outboundCompactor struct{ *outboundUpstream }

func (u outboundCompactor) Compact(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	if err := u.onHold(); err != nil {
		return err
	}
	err := retryTransient(ctx, emit, func(emit func([]byte) error) error {
		return requestLimited(u.up.(node.Compactor).Compact(withHTTPClient(ctx, u.client), r, emit))
	})
	recordProviderStatus(u.owner, err)
	return err
}

// wrapOutbound returns the outbound wrapper of up, a node.Compactor when up is.
func wrapOutbound(o *outboundUpstream) node.Upstream {
	if _, ok := o.up.(node.Compactor); ok {
		return outboundCompactor{o}
	}
	return o
}

func (u *outboundUpstream) ReadyAt() time.Time { return providerStatus(u.owner).RetryAt }

// onHold is the error of a request to a provider that is waiting out a rate
// limit, or whose credentials were refused a moment ago.
func (u *outboundUpstream) onHold() error {
	st := providerStatus(u.owner)
	if !st.RetryAt.After(time.Now()) {
		return nil
	}
	body := "provider is temporarily rate limited"
	if st.Status == http.StatusUnauthorized {
		body = "provider credentials were refused: sign in again or replace the key"
	}
	return &node.StatusError{Status: http.StatusTooManyRequests, Body: body, RetryAt: st.RetryAt}
}

// tokenError returns only the endpoint's error fields, never token payloads.
func tokenError(status int, code, description string) error {
	return &loginError{status: status, msg: clip(strings.TrimSpace(code+" "+description), 300)}
}

// loginError is a login server's answer other than a token.
type loginError struct {
	status int
	msg    string
}

func (e *loginError) Error() string {
	return fmt.Sprintf("the login server answered HTTP %d: %s", e.status, e.msg)
}

// refused tells that the server turned the login itself down (invalid_grant,
// a refresh token used twice or revoked): asking again will not help.
func (e *loginError) refused() bool {
	return e.status == http.StatusBadRequest || e.status == http.StatusUnauthorized || e.status == http.StatusForbidden
}
