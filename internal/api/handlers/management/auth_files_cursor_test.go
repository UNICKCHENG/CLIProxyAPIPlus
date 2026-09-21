package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	cursorruntime "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// fakeCursorRuntime stands in for the native runtime behind the import endpoint.
type fakeCursorRuntime struct {
	email     string
	err       error
	lastKey   string
	validateN int
}

func (f *fakeCursorRuntime) ValidateAPIKey(_ context.Context, apiKey string) (string, error) {
	f.validateN++
	f.lastKey = apiKey
	if f.err != nil {
		return "", f.err
	}
	return f.email, nil
}

func newCursorImportTestHandler(t *testing.T, runtime cursorRuntimeProvider) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{AuthDir: t.TempDir()}
	t.Cleanup(func() { _ = cfg.AuthDir })
	h := &Handler{
		cfg:            cfg,
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	h.SetCursorServiceRuntime(runtime)
	router := gin.New()
	router.POST("/v0/management/cursor-auth", h.ImportCursorAPIKey)
	return h, router
}

func postCursorImport(t *testing.T, router *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/cursor-auth", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

// TestImportCursorAPIKeySuccess proves a valid key persists cursor-<account>.json with the
// key, type and email, and the response carries no secret.
func TestImportCursorAPIKeySuccess(t *testing.T) {
	runtime := &fakeCursorRuntime{email: "dev.user+cli@example.com"}
	h, router := newCursorImportTestHandler(t, runtime)

	rec := postCursorImport(t, router, map[string]string{"api_key": "key_abc123"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string `json:"status"`
		Name   string `json:"name"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body = %s", rec.Body.String())
	}
	if body.Status != "ok" || body.Name != "cursor-dev.user-cli-example.com.json" || body.Label != "dev.user+cli@example.com" {
		t.Fatalf("response = %+v", body)
	}
	if strings.Contains(rec.Body.String(), "key_abc123") {
		t.Fatal("response leaked the API key")
	}
	saved := readCursorAuthFile(t, filepath.Join(h.cfg.AuthDir, body.Name))
	if saved["type"] != "cursor" || saved["api_key"] != "key_abc123" || saved["email"] != "dev.user+cli@example.com" {
		t.Fatalf("persisted file = %v", saved)
	}
}

// TestImportCursorAPIKeyPreservesOperatorFields proves re-import keeps label, prefix,
// proxy_url, disabled, note, aliases, exclusions and unknown fields.
func TestImportCursorAPIKeyPreservesOperatorFields(t *testing.T) {
	runtime := &fakeCursorRuntime{email: "user@example.com"}
	h, router := newCursorImportTestHandler(t, runtime)

	existing := `{"type":"cursor","api_key":"key_old","email":"user@example.com","label":"Team","prefix":"teamA","proxy_url":"socks5://127.0.0.1:1080","disabled":true,"note":"prod","model_aliases":[{"name":"a","alias":"b"}],"excluded-models":["auto-smart"],"custom_field":"keep"}`
	writeFileOrFail(t, filepath.Join(h.cfg.AuthDir, "cursor-user-example.com.json"), existing)

	rec := postCursorImport(t, router, map[string]string{"api_key": "key_new"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	saved := readCursorAuthFile(t, filepath.Join(h.cfg.AuthDir, "cursor-user-example.com.json"))
	if saved["api_key"] != "key_new" {
		t.Fatalf("api_key = %v, want the newly imported key", saved["api_key"])
	}
	for key, want := range map[string]any{
		"label": "Team", "prefix": "teamA", "proxy_url": "socks5://127.0.0.1:1080",
		"disabled": true, "note": "prod", "custom_field": "keep",
	} {
		if saved[key] != want {
			t.Fatalf("%s = %v, want %v", key, saved[key], want)
		}
	}
	if _, ok := saved["model_aliases"]; !ok {
		t.Fatal("model_aliases were dropped")
	}
	if _, ok := saved["excluded_models"]; !ok {
		// MergeExistingAuthMetadata canonicalizes "excluded-models" to "excluded_models";
		// the entries must survive under whichever spelling is present.
		if _, legacy := saved["excluded-models"]; !legacy {
			t.Fatal("excluded-models were dropped (neither canonical nor legacy spelling present)")
		}
	}
}

func TestImportCursorAPIKeyInvalid(t *testing.T) {
	// The runtime returns classified failures, so the fake mirrors that shape rather than
	// flattening the status into message text.
	runtime := &fakeCursorRuntime{err: cursorruntime.NewFailureError(cursorruntime.Failure{
		Message:    "cursor upstream error 401: Invalid User API Key",
		HTTPStatus: http.StatusUnauthorized,
	})}
	h, router := newCursorImportTestHandler(t, runtime)

	for name, body := range map[string]any{
		"blank":  map[string]string{"api_key": "  "},
		"no-key": map[string]string{},
	} {
		rec := postCursorImport(t, router, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
	}

	rec := postCursorImport(t, router, map[string]string{"api_key": "key_bad"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("rejected key: status = %d, want 401", rec.Code)
	}
	entries := readAuthDir(t, h.cfg.AuthDir)
	if len(entries) != 0 {
		t.Fatalf("auth dir = %v, want no persisted files", entries)
	}
}

// TestImportCursorAPIKeyProviderUnavailable proves a missing native runtime yields the
// stable 503 and persists nothing.
func TestImportCursorAPIKeyProviderUnavailable(t *testing.T) {
	h, router := newCursorImportTestHandler(t, nil)
	rec := postCursorImport(t, router, map[string]string{"api_key": "key_abc"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cursor provider unavailable") {
		t.Fatalf("body = %s, want the stable error", rec.Body.String())
	}
	if entries := readAuthDir(t, h.cfg.AuthDir); len(entries) != 0 {
		t.Fatalf("auth dir = %v, want no persisted files", entries)
	}
}

// TestImportCursorAPIKeyMalformedBody proves a non-JSON body is a 400.
func TestImportCursorAPIKeyMalformedBody(t *testing.T) {
	_, router := newCursorImportTestHandler(t, &fakeCursorRuntime{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/cursor-auth", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestImportCursorAPIKeyStatusClassification proves the structured failure classification:
// upstream 401/403/429 surface their own status, and transport or unclassified failures map to
// 502 rather than 401, so a bridge outage never reads to the client as an invalid key.
func TestImportCursorAPIKeyStatusClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "rejected key is 401",
			err:  cursorruntime.NewFailureError(cursorruntime.Failure{Message: "cursor upstream error 401: Invalid User API Key", HTTPStatus: http.StatusUnauthorized}),
			want: http.StatusUnauthorized,
		},
		{
			name: "forbidden key is 403",
			err:  cursorruntime.NewFailureError(cursorruntime.Failure{Message: "cursor upstream error 403: role forbidden", HTTPStatus: http.StatusForbidden}),
			want: http.StatusForbidden,
		},
		{
			name: "rate limited is 429",
			err:  cursorruntime.NewFailureError(cursorruntime.Failure{Message: "cursor upstream error 429: rate limit", HTTPStatus: http.StatusTooManyRequests}),
			want: http.StatusTooManyRequests,
		},
		{
			name: "transport outage is 502",
			err:  errors.New("could not reach the cursor api: check that the plugin's proxy-url is set"),
			want: http.StatusBadGateway,
		},
		{
			name: "unclassified error is 502, not 401",
			err:  errors.New("something novel went wrong"),
			want: http.StatusBadGateway,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, router := newCursorImportTestHandler(t, &fakeCursorRuntime{err: tc.err})
			rec := postCursorImport(t, router, map[string]string{"api_key": "key_x"})
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if entries := readAuthDir(t, h.cfg.AuthDir); len(entries) != 0 {
				t.Fatalf("auth dir = %v, want no persisted files", entries)
			}
		})
	}
}

func writeFileOrFail(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCursorAuthFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

func readAuthDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
