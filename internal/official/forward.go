package official

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// upstreamTimeout bounds the wait for a vendor's headers and the gaps in
// its stream: a long thinking turn can be silent for minutes.
const upstreamTimeout = 10 * time.Minute

// newUpstreamClient is the client for the vendors: through the proxy the
// environment names (HTTPS_PROXY: where the vendors are reachable only
// through one), and following no redirect, so credentials reach the fixed
// hosts only.
func newUpstreamClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	tr.ResponseHeaderTimeout = upstreamTimeout
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("the vendor redirected the request; not followed")
	}}
}

// hopHeaders are a connection's own; dropRequest are not passed on either:
// the vendor sees the request as the tool would have sent it.
var (
	hopHeaders  = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}
	dropRequest = []string{"Host", "Content-Length", "Accept-Encoding", "Cookie", "Origin", "Referer", HeaderKey, "X-Sharetoken-Node", "X-Sharetoken-Require-Native"}
)

// vendorURL is where a request to the local endpoint goes at the vendor of
// its credentials.
func (g *Gateway) vendorURL(cred Cred, r *http.Request) string {
	p := r.URL.EscapedPath()
	var u string
	switch cred {
	case CredClaudeOAuth, CredAnthropicKey:
		u = g.up.Anthropic + p
	case CredChatGPT:
		u = g.up.ChatGPT + strings.TrimPrefix(p, "/v1")
	default:
		u = g.up.OpenAI + strings.TrimPrefix(p, "/v1")
	}
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	return u
}

// vendorHeader is the request's header as the vendor gets it.
func vendorHeader(in http.Header) http.Header {
	h := in.Clone()
	for _, k := range append(append([]string(nil), hopHeaders...), dropRequest...) {
		h.Del(k)
	}
	return h
}

// send makes one request of a vendor.
func (g *Gateway) send(ctx context.Context, method, url string, h http.Header, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header = h.Clone()
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	return g.hc.Do(req)
}

// passthrough sends a request to its vendor as it came and its answer back.
func (g *Gateway) passthrough(w http.ResponseWriter, r *http.Request, cred Cred, body []byte) {
	if body == nil && r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			writeVendorError(w, cred, http.StatusBadRequest, "request body unreadable")
			return
		}
		body = b
	}
	started := now()
	resp, err := g.send(r.Context(), r.Method, g.vendorURL(cred, r), vendorHeader(r.Header), body)
	if err != nil {
		g.unreachable(w, r, cred, err)
		return
	}
	defer resp.Body.Close()
	g.observe(cred, r.Header, resp.Header)
	copyResponse(w, resp)
	g.logDone(r, cred, "", resp.StatusCode, started)
}

// unreachable answers a request the vendor could not be asked.
func (g *Gateway) unreachable(w http.ResponseWriter, r *http.Request, cred Cred, err error) {
	if r.Context().Err() != nil {
		return
	}
	g.log.Warn("vendor unreachable", "path", r.URL.Path, "cred", cred.String(), "err", err)
	vendor := "Anthropic"
	if cred.openai() {
		vendor = "OpenAI"
	}
	writeVendorError(w, cred, http.StatusBadGateway, "could not reach "+vendor+": "+err.Error()+
		" (where the vendor is reachable only through a proxy, set HTTPS_PROXY for the sharetoken client)")
}

// logDone logs a vendor request's outcome, never its credentials or content.
func (g *Gateway) logDone(r *http.Request, cred Cred, model string, status int, started time.Time) {
	lvl := g.log.Info
	if status >= 400 {
		lvl = g.log.Warn
	}
	lvl("vendor request", "path", r.URL.Path, "cred", cred.String(), "model", model, "status", status,
		"ms", strconv.FormatInt(now().Sub(started).Milliseconds(), 10))
}

// copyResponse writes a vendor's answer, flushing as it streams.
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	writeHead(w, resp.StatusCode, resp.Header, resp.Uncompressed)
	streamBody(w, resp.Body)
}

// writeHead writes a vendor's status and headers.
func writeHead(w http.ResponseWriter, status int, h http.Header, uncompressed bool) {
	out := w.Header()
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	for _, k := range hopHeaders {
		out.Del(k)
	}
	if uncompressed {
		out.Del("Content-Length")
		out.Del("Content-Encoding")
	}
	w.WriteHeader(status)
}

// streamBody copies body to w, flushing each read.
func streamBody(w http.ResponseWriter, body io.Reader) {
	f, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// writeBuffered writes an answer whose body was read already.
func writeBuffered(w http.ResponseWriter, resp *http.Response, body []byte) {
	writeHead(w, resp.StatusCode, resp.Header, true)
	_, _ = w.Write(body)
}

// readError reads a failed answer's body (bounded): to see what it says
// before it is passed on or replaced.
func readError(resp *http.Response) []byte {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return b
}
