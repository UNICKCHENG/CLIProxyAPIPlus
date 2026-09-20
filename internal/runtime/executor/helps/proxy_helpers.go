package helps

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := cachedProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}

	return httpClient
}

// proxyTransportCacheCapacity bounds how many proxied connection pools stay alive.
// An unused entry costs well under a kilobyte and no goroutines, while evicting a pool
// that is still in use forces the next request through that proxy to redo the TCP + TLS
// handshake, so the bound only exists to stop entries from accumulating when proxy
// settings churn (rotating a credential's proxy, for example).
const proxyTransportCacheCapacity = 1024

// proxyTransportCache memoizes one transport per proxy URL so that every request routed
// through the same proxy shares a single connection pool. Building a transport per
// request gave each request a private pool: it paid a fresh TCP (and, through CONNECT,
// TLS) handshake and left an idle connection and its goroutines behind until
// IdleConnTimeout. The key is the trimmed proxy URL, which is the entire input to the
// builder, so a configuration reload that changes the proxy selects a different pool
// instead of reusing a stale one.
var proxyTransportCache = NewTransportCache[string](proxyTransportCacheCapacity)

// cachedProxyTransport returns the shared transport for proxyURL, building it on first
// use. It returns nil when the proxy setting cannot be turned into a transport, matching
// buildProxyTransport so callers keep their existing fallback behaviour.
func cachedProxyTransport(proxyURL string) *http.Transport {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil
	}
	transport, errGet := proxyTransportCache.Get(proxyURL, func() (*http.Transport, error) {
		return buildProxyTransportErr(proxyURL)
	})
	if errGet != nil {
		log.Errorf("%v", errGet)
		return nil
	}
	return transport
}

var devinTransportCache = NewTransportCache[string](DefaultTransportCacheCapacity)

// NewDevinHTTPClient creates an HTTP client customized for Devin Connect-RPC upstream.
// Suppresses automatic Accept-Encoding: gzip while preserving connection reuse across requests.
func NewDevinHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Respect explicitly injected context RoundTripper (e.g. from Conductor, Home, or integration test fixtures)
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			if tr, ok := rt.(*http.Transport); ok {
				key := fmt.Sprintf("rt:%p", tr)
				cloned, err := devinTransportCache.Get(key, func() (*http.Transport, error) {
					c := tr.Clone()
					c.DisableCompression = true
					return c, nil
				})
				if err == nil && cloned != nil {
					return &http.Client{
						Transport: cloned,
						Timeout:   timeout,
					}
				}
			}
			return &http.Client{
				Transport: devinNoGzipRoundTripper{base: rt},
				Timeout:   timeout,
			}
		}
	}

	proxyURL := ""
	if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	} else if cfg != nil && strings.TrimSpace(cfg.ProxyURL) != "" {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	tr, err := devinTransportCache.Get(proxyURL, func() (*http.Transport, error) {
		var base *http.Transport
		if proxyURL != "" {
			base = buildProxyTransport(proxyURL)
		}
		if base == nil {
			if dt, ok := http.DefaultTransport.(*http.Transport); ok {
				base = dt.Clone()
			} else {
				base = &http.Transport{}
			}
		}
		base.DisableCompression = true
		return base, nil
	})
	if err != nil || tr == nil {
		tr = &http.Transport{DisableCompression: true}
	}

	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}

type devinNoGzipRoundTripper struct {
	base http.RoundTripper
}

func (rt devinNoGzipRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}
	return rt.base.RoundTrip(req)
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, errBuild := buildProxyTransportErr(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}

// buildProxyTransportErr is buildProxyTransport without logging, so its result can be
// cached: a failed build must surface as an error instead of taking a cache slot.
func buildProxyTransportErr(proxyURL string) (*http.Transport, error) {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		return nil, errBuild
	}
	if transport == nil {
		return nil, fmt.Errorf("proxy %s produced no transport", proxyutil.Redact(proxyURL))
	}
	return transport, nil
}
