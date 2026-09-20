package helps

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewDevinHTTPClient_ReusesTransportFromContext(t *testing.T) {
	baseTransport := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", baseTransport)

	c1 := NewDevinHTTPClient(ctx, nil, nil, 0)
	c2 := NewDevinHTTPClient(ctx, nil, nil, 0)

	if c1.Transport != c2.Transport {
		t.Errorf("expected c1.Transport == c2.Transport across requests, got different pointers %p vs %p", c1.Transport, c2.Transport)
	}

	tr, ok := c1.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c1.Transport)
	}
	if !tr.DisableCompression {
		t.Error("expected DisableCompression = true")
	}
}

func TestNewDevinHTTPClient_NonStandardRoundTripperDisablesGzip(t *testing.T) {
	var seenEncoding string
	customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenEncoding = req.Header.Get("Accept-Encoding")
		return &http.Response{StatusCode: 200}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customRT)

	c := NewDevinHTTPClient(ctx, nil, nil, 0)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid", nil)
	_, _ = c.Transport.RoundTrip(req)

	if seenEncoding != "identity" {
		t.Errorf("expected Accept-Encoding: identity, got %q", seenEncoding)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestNewProxyAwareHTTPClientReusesProxyTransport is the regression test for the bug
// where every proxied request built its own transport, and therefore its own
// connection pool: each request paid a fresh TCP handshake and left an idle
// connection behind until IdleConnTimeout.
func TestNewProxyAwareHTTPClientReusesProxyTransport(t *testing.T) {
	t.Parallel()

	proxyURL := "http://127.0.0.1:3128"
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxyURL}}

	first := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	second := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	if first.Transport != second.Transport {
		t.Fatalf("expected one shared proxy transport, got %p and %p", first.Transport, second.Transport)
	}

	// An auth-scoped proxy overrides the global one and must not share its pool.
	other := NewProxyAwareHTTPClient(
		context.Background(),
		cfg,
		&cliproxyauth.Auth{ProxyURL: "http://127.0.0.1:3129"},
		0,
	)
	if other.Transport == first.Transport {
		t.Fatal("expected a distinct transport for a different proxy URL")
	}

	// A malformed proxy must still fall back to the context transport instead of
	// caching the failure.
	fallback := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(fallback))
	broken := NewProxyAwareHTTPClient(ctx, &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "://invalid"}}, nil, 0)
	if broken.Transport != http.RoundTripper(fallback) {
		t.Fatalf("expected the context transport for an invalid proxy, got %#v", broken.Transport)
	}
}

// TestNewProxyAwareHTTPClientReusesProxyConnection proves the shared transport is a
// single upstream connection pool: N sequential requests through one proxy must dial
// the proxy once, not N times.
func TestNewProxyAwareHTTPClientReusesProxyConnection(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	proxyAddr, accepts := startCountingForwardProxy(t)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://" + proxyAddr}}

	const requests = 5
	for i := 0; i < requests; i++ {
		client := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
		resp, errGet := client.Get(target.URL)
		if errGet != nil {
			t.Fatalf("request %d failed: %v", i, errGet)
		}
		if _, errCopy := io.Copy(io.Discard, resp.Body); errCopy != nil {
			t.Fatalf("read body %d failed: %v", i, errCopy)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close body %d failed: %v", i, errClose)
		}
	}

	if got := atomic.LoadInt64(accepts); got != 1 {
		t.Fatalf("proxy accepted %d connections for %d requests, want 1", got, requests)
	}
}

// startCountingForwardProxy runs a minimal forwarding HTTP proxy that counts accepted
// connections and keeps them alive, so callers can observe pool reuse.
func startCountingForwardProxy(t *testing.T) (string, *int64) {
	t.Helper()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })

	var accepts int64
	upstream := &http.Transport{Proxy: nil}
	go func() {
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			atomic.AddInt64(&accepts, 1)
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				reader := bufio.NewReader(c)
				for {
					req, errRead := http.ReadRequest(reader)
					if errRead != nil {
						return
					}
					req.RequestURI = ""
					resp, errDo := upstream.RoundTrip(req)
					if errDo != nil {
						_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
						return
					}
					resp.Header.Set("Connection", "keep-alive")
					_ = resp.Write(c)
					_ = resp.Body.Close()
				}
			}(conn)
		}
	}()

	return listener.Addr().String(), &accepts
}
