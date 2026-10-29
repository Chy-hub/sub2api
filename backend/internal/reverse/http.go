package reverse

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	transportMu sync.Mutex
	transports  = map[string]*http.Transport{}
)

// HTTPClient returns a shared transport/client for reverse upstream calls.
// An empty proxyURL keeps the environment proxy (HTTP_PROXY/HTTPS_PROXY).
// There is deliberately no total client timeout: streaming generations can
// run for minutes; callers bound them with contexts.
func HTTPClient(proxyURL string) *http.Client {
	key := strings.TrimSpace(proxyURL)
	transportMu.Lock()
	transport := transports[key]
	if transport == nil {
		cloned := http.DefaultTransport.(*http.Transport).Clone()
		cloned.ResponseHeaderTimeout = 120 * time.Second
		if key != "" {
			parsed, err := url.Parse(key)
			if err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "socks5" || parsed.Scheme == "socks5h") {
				cloned.Proxy = http.ProxyURL(parsed)
			} else {
				// An invalid account proxy must fail instead of silently sending
				// its credentials over a different route. Do not expose the URL.
				cloned.Proxy = func(*http.Request) (*url.URL, error) {
					return nil, fmt.Errorf("invalid reverse account proxy URL")
				}
			}
		}
		transports[key] = cloned
		transport = cloned
	}
	transportMu.Unlock()
	return &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}
