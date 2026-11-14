package provider

import (
	"net/http"
	"net/url"

	"github.com/imroc/req/v3"
)

// Optional browser TLS transport, matching sub2api's OAuth client dependency.
// It supplies transport behaviour only; the adapter owns headers and bodies.
func chromeHTTPClient(options TransportOptions) *http.Client {
	c := req.C().ImpersonateChrome().SetCookieJar(nil)
	client := c.GetClient()
	client.Timeout = 0 // streaming lifetime is governed by the job context
	switch options.ProxyMode {
	case "direct":
		c.SetProxy(nil)
	case "custom":
		proxy, _ := url.Parse(options.ProxyURL)
		c.SetProxy(http.ProxyURL(proxy))
	default:
		c.SetProxy(http.ProxyFromEnvironment)
	}
	return client
}
