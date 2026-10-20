package synthesizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func cursorSynthesisContext(t *testing.T, cfg *config.Config) *SynthesisContext {
	t.Helper()
	return &SynthesisContext{Config: cfg, AuthDir: t.TempDir(), Now: time.Now(), IDGenerator: NewStableIDGenerator()}
}

// TestSynthesizeCursorAuthRequiresKey proves a Cursor file without a key is rejected.
func TestSynthesizeCursorAuthRequiresKey(t *testing.T) {
	ctx := cursorSynthesisContext(t, &config.Config{})
	path := filepath.Join(ctx.AuthDir, "cursor-x.json")
	auths, err := synthesizeFileAuths(ctx, path, []byte(`{"type":"cursor"}`))
	if err == nil {
		t.Fatalf("expected an error for a missing api_key, got auths=%v", auths)
	}
}

// TestSynthesizeCursorAuthDefaults proves a valid Cursor file synthesizes with provider
// cursor, oauth auth_kind, and a one-year refresh horizon.
func TestSynthesizeCursorAuthDefaults(t *testing.T) {
	ctx := cursorSynthesisContext(t, &config.Config{})
	path := filepath.Join(ctx.AuthDir, "cursor-x.json")
	auths, err := synthesizeFileAuths(ctx, path, []byte(`{"type":"cursor","api_key":"key_abc"}`))
	if err != nil {
		t.Fatalf("synthesizeFileAuths() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("synthesizeFileAuths() produced %d auths", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "cursor" {
		t.Fatalf("Provider = %q", auth.Provider)
	}
	if auth.AuthKind() != "oauth" {
		t.Fatalf("AuthKind() = %q, want oauth so OAuth alias/exclusion machinery applies", auth.AuthKind())
	}
	if auth.NextRefreshAfter.Before(ctx.Now) || auth.NextRefreshAfter.After(ctx.Now.Add(366*24*time.Hour)) {
		t.Fatalf("NextRefreshAfter = %v, want roughly one year out", auth.NextRefreshAfter)
	}
}

// TestSynthesizeCursorAuthExpiry proves an expired key is rejected at synthesis.
func TestSynthesizeCursorAuthExpiry(t *testing.T) {
	ctx := cursorSynthesisContext(t, &config.Config{})
	path := filepath.Join(ctx.AuthDir, "cursor-x.json")
	raw := `{"type":"cursor","api_key":"key_abc","expires_at":"2020-01-01T00:00:00Z"}`
	if _, err := synthesizeFileAuths(ctx, path, []byte(raw)); err == nil {
		t.Fatal("expected an error for an expired key")
	}
}

// TestSynthesizeCursorAuthWeightPrecedence proves a file-level weight beats a configured one.
func TestSynthesizeCursorAuthWeightPrecedence(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cursor.Weights = map[string]int{"cursor-x": 7}
	ctx := cursorSynthesisContext(t, cfg)
	path := filepath.Join(ctx.AuthDir, "cursor-x.json")
	auths, err := synthesizeFileAuths(ctx, path, []byte(`{"type":"cursor","api_key":"key_abc","weight":3}`))
	if err != nil {
		t.Fatalf("synthesizeFileAuths() error = %v", err)
	}
	if got := auths[0].Attributes["weight"]; got != "3" {
		t.Fatalf("file weight = %q, want 3 to take precedence", got)
	}
}

// TestSynthesizeCursorConfiguredWeight proves the configured weights map applies when the
// file has no weight of its own, keyed by filename with or without .json or email.
func TestSynthesizeCursorConfiguredWeight(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cursor.Weights = map[string]int{"cursor-x.json": 7}
	ctx := cursorSynthesisContext(t, cfg)
	path := filepath.Join(ctx.AuthDir, "cursor-x.json")
	auths, err := synthesizeFileAuths(ctx, path, []byte(`{"type":"cursor","api_key":"key_abc"}`))
	if err != nil {
		t.Fatalf("synthesizeFileAuths() error = %v", err)
	}
	if got := auths[0].Attributes["weight"]; got != "7" {
		t.Fatalf("configured weight = %q, want 7", got)
	}
}

// TestSynthesizeCursorAuthExclusionPriority keeps credential rules separate from
// global fallback rules, matching the shared OAuth registration policy.
func TestSynthesizeCursorAuthExclusionPriority(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"credential override", `,"excluded_models":["auto-smart"]`, "auto-smart"},
		{"missing inherits", ``, ""},
		{"empty inherits", `,"excluded_models":[]`, ""},
		{"null inherits", `,"excluded_models":null`, ""},
		{"invalid retains valid rules", `,"excluded_models":["auto-smart",42]`, "auto-smart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{OAuthExcludedModels: map[string][]string{"cursor": {"global-*"}}}
			ctx := cursorSynthesisContext(t, cfg)
			path := filepath.Join(ctx.AuthDir, "cursor-x.json")
			raw := `{"type":"cursor","api_key":"key_abc"` + tc.fields + `}`
			auths, err := synthesizeFileAuths(ctx, path, []byte(raw))
			if err != nil || len(auths) != 1 {
				t.Fatalf("synthesizeFileAuths() = %v, %v", auths, err)
			}
			if got := auths[0].Attributes["excluded_models"]; got != tc.want {
				t.Fatalf("excluded_models = %q, want %q", got, tc.want)
			}
			if auths[0].Attributes["auth_kind"] != "oauth" {
				t.Fatalf("auth_kind = %q", auths[0].Attributes["auth_kind"])
			}
		})
	}
}

// TestExistingCursorAuthStoragePreservesFields proves re-import reads back operator fields.
func TestExistingCursorAuthStoragePreservesFields(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]any{
		"label":  "Team account",
		"prefix": "teamA",
		"note":   "prod",
		"custom": "keep me",
	}
	raw, _ := json.Marshal(existing)
	if err := os.WriteFile(filepath.Join(dir, "cursor-x.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got := existingCursorAuthStorage(dir, "cursor-x.json")
	if got["label"] != "Team account" || got["prefix"] != "teamA" || got["note"] != "prod" || got["custom"] != "keep me" {
		t.Fatalf("existingCursorAuthStorage() = %v", got)
	}
	if empty := existingCursorAuthStorage(dir, "missing.json"); len(empty) != 0 {
		t.Fatalf("missing file returned %v, want empty", empty)
	}
}

func jsonContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		(len(haystack) > len(needle) && (contains(haystack, needle))))
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
