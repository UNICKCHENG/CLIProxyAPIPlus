package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cursorruntime "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestCursorErrorMapping proves runtime failures land in the manager's error contracts:
// 401 retires the credential, 429 cools it retryably, 400 is request-scoped, and
// unclassified transport failures carry no status so no credential is cooled.
func TestCursorErrorMapping(t *testing.T) {
	cases := []struct {
		name          string
		message       string
		status        int
		wantStatus    int
		wantScoped    bool
		wantRetryable bool
	}{
		{name: "unauthorized", message: "cursor upstream error 401: Invalid User API Key", status: 401, wantStatus: 401},
		{name: "request fault", message: `{"error":{"type":"invalid_request_error","code":"model_not_available","message":"nope"}}`, status: 400, wantStatus: 400, wantScoped: true},
		{name: "transient", message: "could not reach the cursor api", status: 0, wantRetryable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cursorErrorFromRuntime(cursorruntime.NewFailureError(cursorruntime.Failure{
				Message: tc.message, HTTPStatus: tc.status, Retryable: tc.wantRetryable,
			}))
			statusErr, ok := err.(*coreauthStatusError)
			if !ok {
				t.Fatalf("cursorErrorFromRuntime() = %T, want *coreauthStatusError", err)
			}
			if statusErr.StatusCode() != tc.wantStatus {
				t.Fatalf("StatusCode() = %d, want %d", statusErr.StatusCode(), tc.wantStatus)
			}
			if statusErr.IsRequestScoped() != tc.wantScoped {
				t.Fatalf("IsRequestScoped() = %v, want %v", statusErr.IsRequestScoped(), tc.wantScoped)
			}
			if statusErr.Retryable() != tc.wantRetryable && tc.wantRetryable {
				t.Fatalf("Retryable() = %v, want %v", statusErr.Retryable(), tc.wantRetryable)
			}
		})
	}
}

// TestCursorErrorMappingPreservesCancellation proves context cancellation passes through
// untouched so the conductor's connection-lifecycle handling applies.
func TestCursorErrorMappingPreservesCancellation(t *testing.T) {
	err := cursorErrorFromRuntime(context.Canceled)
	if err != context.Canceled {
		t.Fatalf("cursorErrorFromRuntime(context.Canceled) = %v, want passthrough", err)
	}
}

// TestCursorExecutorIdentifierAndFormat proves the facade contract.
func TestCursorExecutorIdentifierAndFormat(t *testing.T) {
	exec := NewCursorExecutor(nil)
	if exec.Identifier() != "cursor" {
		t.Fatalf("Identifier() = %q", exec.Identifier())
	}
	if _, err := exec.HttpRequest(context.Background(), nil, nil); err == nil {
		t.Fatal("HttpRequest() expected 501")
	} else if statusErr, ok := err.(*coreauthStatusError); !ok || statusErr.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("HttpRequest() error = %v, want 501", err)
	}
}

// TestCursorExecutorUsesConfig proves config comparison drives reuse vs replacement.
func TestCursorExecutorUsesConfig(t *testing.T) {
	cfg := minimalCursorConfig("balanced", nil)
	exec := NewCursorExecutor(cfg)
	if !exec.UsesConfig(cfg) {
		t.Fatal("UsesConfig() = false for identical config")
	}
	other := minimalCursorConfig("intelligence", nil)
	if exec.UsesConfig(other) {
		t.Fatal("UsesConfig() = true for a changed optimize mode")
	}
}

// TestCursorRefreshWithoutKeyErrors proves refresh on a keyless record reports 401.
func TestCursorRefreshWithoutKeyErrors(t *testing.T) {
	exec := NewCursorExecutor(nil)
	_, err := exec.Refresh(context.Background(), newCursorTestAuth(""))
	if err == nil {
		t.Fatal("Refresh() expected an error without an api key")
	}
	if statusErr, ok := err.(*coreauthStatusError); !ok || statusErr.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("Refresh() error = %v, want 401", err)
	}
}

// TestCursorRefreshWithKeyReturnsAuthUnchanged proves refresh on a keyed record is a no-op.
func TestCursorRefreshWithKeyReturnsAuthUnchanged(t *testing.T) {
	exec := NewCursorExecutor(nil)
	auth := newCursorTestAuth("key_abc")
	got, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got != auth {
		t.Fatal("Refresh() returned a different record")
	}
}

func minimalCursorConfig(optimizeFor string, weights map[string]int) *config.Config {
	cfg := &config.Config{}
	cfg.Cursor.OptimizeFor = optimizeFor
	cfg.Cursor.Weights = weights
	return cfg
}

func newCursorTestAuth(apiKey string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "cursor-test.json",
		Provider: "cursor",
		Metadata: map[string]any{"type": "cursor", "api_key": apiKey},
	}
}

var _ = errors.New
