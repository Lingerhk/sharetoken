package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"sharetoken/internal/node"
)

func TestBrowserTLSAndIndependentTokenTransport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "adapter-test" || r.Header.Get("Accept") != "text/event-stream" {
			t.Error("transport replaced adapter headers")
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	c := chromeHTTPClient(TransportOptions{ProxyMode: "direct"})
	c.Transport.(*req.Transport).SetTLSClientConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	request, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	request.Header.Set("User-Agent", "adapter-test")
	request.Header.Set("Accept", "text/event-stream")
	resp, err := c.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	c.CloseIdleConnections()
	if err != nil || string(body) != "ok" {
		t.Fatalf("TLS response: %s %v", body, err)
	}
	options := &TransportOptions{ProxyMode: "direct", TLSProfile: "chrome", TokenTLSProfile: "native"}
	tokenClient, err := tokenHTTPClient(t.Name(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tokenClient.(*http.Client).Transport.(*http.Transport); !ok {
		t.Fatal("token path inherited inference TLS")
	}
	messageClient, err := providerHTTPClient(t.Name(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := messageClient.(*http.Client).Transport.(*req.Transport); !ok {
		t.Fatal("inference TLS profile ignored")
	}
	t.Cleanup(func() { releaseHTTPClient(t.Name()); releaseHTTPClient(t.Name() + ":token") })
}

func TestTransportPatchesPreserveProxySecrets(t *testing.T) {
	old := &TransportOptions{ProxyMode: "custom", ProxyURL: "http://user:password@proxy.test:8080", TokenTLSProfile: "chrome"}
	next := mergeTransport(old, &TransportOptions{TLSProfile: "chrome"})
	if next.ProxyURL != old.ProxyURL || next.TokenTLSProfile != "chrome" {
		t.Fatal("TLS change erased proxy configuration")
	}
	clear := mergeTransport(next, &TransportOptions{ProxyMode: "direct", TokenTLSProfile: "native"})
	if clear.ProxyURL != "" || clear.ProxyMode != "direct" || clear.TokenTLSProfile != "native" {
		t.Fatal("could not reset connection options")
	}
	if old.ProxyMode != "custom" {
		t.Fatal("patch mutated original configuration")
	}
}

func TestProviderCooldownAndRecovery(t *testing.T) {
	owner := t.Name()
	defer recordProviderStatus(owner, nil)
	resp := &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"30"}, "Request-Id": {"request-123"}}}
	err := responseStatus(resp, "rate limited")
	if left := time.Until(err.RetryAt); left < 29*time.Second || left > 31*time.Second || err.RequestID != "request-123" {
		t.Fatalf("retry metadata: %+v", err)
	}
	recordProviderStatus(owner, err)
	u := &outboundUpstream{owner: owner}
	if e := u.Stream(context.Background(), nil, nil); e == nil || !strings.Contains(e.Error(), "rate limited") {
		t.Fatalf("cooldown not enforced: %v", e)
	}
	if !u.ReadyAt().After(time.Now()) {
		t.Fatal("cooldown missing")
	}
	recordProviderStatus(owner, nil)
	if !u.ReadyAt().IsZero() {
		t.Fatal("successful probe did not clear cooldown")
	}
	recordProviderStatus(owner, &node.StatusError{Status: 400})
	if !u.ReadyAt().IsZero() {
		t.Fatal("invalid parameters cooled down the account")
	}
}
