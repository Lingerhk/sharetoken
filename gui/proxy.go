package main

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
)

// proxy passes the window's requests to the daemon's local API, so that the
// pages run on the window's own origin, whatever scheme the platform's webview
// uses for it. Everything here comes from this app's own window, so the
// request is sent on as a local one: the daemon's checks against web pages
// (Origin, Host) are for browsers.
type proxy struct {
	rp       *httputil.ReverseProxy
	startErr error
	result   chan<- string

	mu   sync.Mutex
	last string
}

// selftestPath is where selftestJS reports; it never reaches the daemon.
const selftestPath = "/__selftest"

// selftestJS checks in the real webview that the page's script ran under its
// CSP, and that a POST with a body gets through to the daemon and back.
const selftestJS = `(async () => {
  let r;
  try {
    const resp = await fetch('/api/portal/probe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ portal: 'ftp://body-reached-the-daemon' }) });
    const data = await resp.json();
    const pageRan = !!document.querySelector('#app > *');
    r = (resp.status === 400 && data.error.code === 'bad_portal' && pageRan) ? 'ok' : 'unexpected reply ' + resp.status + ' ' + JSON.stringify(data) + ' pageRan=' + pageRan;
    r += ' origin=' + location.origin;
  } catch (e) { r = 'failed: ' + e; }
  await fetch('` + selftestPath + `?r=' + encodeURIComponent(r));
})();`

func newProxy(sup *supervisor, startErr error, result chan<- string) *proxy {
	target := &url.URL{Scheme: "http", Host: sup.addr}
	p := &proxy{startErr: startErr, result: result}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = sup.addr
			r.Out.Header.Del("Origin")
			r.Out.Header.Del("Referer")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintln(w, `{"error":{"code":"daemon_down","message":"the sharetoken service is not responding"}}`)
		},
	}
	return p
}

func (p *proxy) lastResult() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == selftestPath {
		res := r.URL.Query().Get("r")
		p.mu.Lock()
		p.last = res
		p.mu.Unlock()
		select {
		case p.result <- res:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if p.startErr != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>sharetoken</title>
<body style="font:14px -apple-system,sans-serif;padding:40px;max-width:560px;margin:auto">
<h2>sharetoken 后台服务无法启动</h2><p>%s</p></body>`, html.EscapeString(p.startErr.Error()))
		return
	}
	p.rp.ServeHTTP(w, r)
}
