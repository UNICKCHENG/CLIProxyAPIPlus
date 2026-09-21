package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	cursorruntime "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CursorAuthenticator imports Cursor dashboard API keys. Cursor is not an OAuth
// authorization-code provider: there is no browser callback and no refresh token, so Login
// validates the key through the shared native runtime and persists it with the account email.
type CursorAuthenticator struct {
	// Runtime validates keys. Nil means the caller's server has no native Cursor executor
	// (for example in CLI login before the server started), in which case a throwaway
	// runtime is created for the validation round trip.
	Runtime *cursorruntime.Runtime
}

// NewCursorAuthenticator constructs a new Cursor authenticator.
func NewCursorAuthenticator() Authenticator {
	return &CursorAuthenticator{}
}

// NewCursorAuthenticatorWithRuntime constructs a Cursor authenticator bound to an existing
// runtime, so management-driven imports reuse the registered bridge instead of spawning one.
func NewCursorAuthenticatorWithRuntime(runtime *cursorruntime.Runtime) Authenticator {
	return &CursorAuthenticator{Runtime: runtime}
}

// Provider returns the provider key for Cursor.
func (a *CursorAuthenticator) Provider() string {
	return "cursor"
}

// RefreshLead returns nil: Cursor keys have no refresh token and nothing to re-check on a
// schedule.
func (a *CursorAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// runtimeFor resolves the validation runtime, constructing a temporary one when the shared
// runtime is unavailable. A temporary runtime validates the key and is discarded; it may
// still start a bridge for the Me call, which Close stops.
func (a *CursorAuthenticator) runtimeFor(cfg *config.Config) *cursorruntime.Runtime {
	if a != nil && a.Runtime != nil {
		return a.Runtime
	}
	settings := cursorruntime.Settings{OptimizeFor: "balanced"}
	if cfg != nil {
		settings = cursorruntime.Settings{
			BridgePath:  strings.TrimSpace(cfg.Cursor.BridgePath),
			ProxyURL:    strings.TrimSpace(cfg.Cursor.ProxyURL),
			OptimizeFor: "balanced",
		}
		if settings.OptimizeFor == "" {
			settings.OptimizeFor = "balanced"
		}
	}
	return cursorruntime.NewRuntime(settings)
}

// Login validates a Cursor API key and returns the persisted record.
//
// The key comes from LoginOptions.Metadata["api_key"] or, in a TTY session, from the prompt.
// The environment is deliberately not consulted: a credential that can be picked up from the
// ambient environment is one an operator cannot see the server using.
func (a *CursorAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	apiKey := ""
	if opts.Metadata != nil {
		apiKey = strings.TrimSpace(opts.Metadata["api_key"])
	}
	if apiKey == "" && opts.Prompt != nil {
		prompted, errPrompt := opts.Prompt("Paste your Cursor API key (https://cursor.com/dashboard): ")
		if errPrompt != nil {
			return nil, fmt.Errorf("cursor: read the api key: %w", errPrompt)
		}
		apiKey = strings.TrimSpace(prompted)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("cursor: no api key was entered; pass --cursor-api-key or paste one when prompted")
	}

	runtime := a.runtimeFor(cfg)
	if a == nil || a.Runtime == nil {
		// A throwaway runtime owns whatever bridge it started for validation.
		defer runtime.Close()
	}
	email, errValidate := runtime.ValidateAPIKey(ctx, apiKey)
	if errValidate != nil {
		return nil, fmt.Errorf("cursor: %w", errValidate)
	}

	fileName := cursorCredentialFileName(email)
	label := email
	if label == "" {
		label = "Cursor"
	}

	storage := map[string]any{
		"type":    "cursor",
		"api_key": apiKey,
	}
	if email != "" {
		storage["email"] = email
	}

	metadata := map[string]any{
		"type": "cursor",
	}
	for key, value := range storage {
		metadata[key] = value
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
		},
	}, nil
}

// cursorCredentialFileName derives a stable per-account file name so importing a key for the
// same account replaces its credential instead of accumulating duplicates.
func cursorCredentialFileName(email string) string {
	account := sanitizeCursorFileComponent(email)
	if account == "" {
		return "cursor-apikey.json"
	}
	return fmt.Sprintf("cursor-%s.json", account)
}

// sanitizeCursorFileComponent reduces an email to a safe file name fragment.
func sanitizeCursorFileComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '-', char == '_', char == '.':
			builder.WriteRune(char)
		case char == '@', char == '+':
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-._")
}
