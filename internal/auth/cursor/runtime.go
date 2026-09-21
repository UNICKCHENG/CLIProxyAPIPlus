package cursor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"

	sdkv1 "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/sdk/v1"
)

// loginValidateTimeout bounds the Me call that checks a key during credential acquisition. A
// timeout is acceptable here because this is acquisition, not an established upstream call.
const loginValidateTimeout = 60 * time.Second

// validOptimizeModes are the Cursor Router modes the configuration accepts.
var validOptimizeModes = map[string]struct{}{
	"cost":         {},
	"balanced":     {},
	"intelligence": {},
}

// Configure applies new settings atomically and stops bridges whose binding changed.
//
// A bridge process is bound to its binary and its egress proxy: the proxy is part of the child
// process environment and of the loopback listener it calls Cursor through. Changing either
// therefore requires new processes; the next request after the swap starts them.
func (r *Runtime) Configure(cfg Settings) error {
	if r == nil {
		return nil
	}
	if errValidate := validateSettings(cfg); errValidate != nil {
		return errValidate
	}
	r.mu.Lock()
	old := r.settings
	r.settings = cfg
	r.mu.Unlock()
	if old.BridgePath != cfg.BridgePath || old.ProxyURL != cfg.ProxyURL {
		r.stopBridges()
	}
	return nil
}

// validateSettings mirrors the configuration normalization already applied when the YAML is
// loaded, so a Settings built programmatically (tests, management handler) gets the same checks.
func validateSettings(cfg Settings) error {
	bridgePath := strings.TrimSpace(cfg.BridgePath)
	if bridgePath != cfg.BridgePath {
		return fmt.Errorf("cursor bridge-path must not be surrounded by whitespace")
	}
	proxyURL := strings.TrimSpace(cfg.ProxyURL)
	if proxyURL != cfg.ProxyURL {
		return fmt.Errorf("cursor proxy-url must not be surrounded by whitespace")
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.OptimizeFor))
	if mode == "" {
		mode = defaultOptimizeFor
	}
	if _, ok := validOptimizeModes[mode]; !ok {
		return fmt.Errorf("cursor optimize-for must be one of cost, balanced, intelligence")
	}
	return nil
}

// Close stops bridges, cancels in-flight runs and parked tool runs, evicts sessions, and
// removes scratch workspaces. Safe to call more than once.
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.quiesce()
	r.stopBridges()
}

// quiesce cancels in-flight executor work and parked tool runs and evicts sessions without
// stopping the bridges. Config reloads that only change routing call this; shutdown calls
// Close.
func (r *Runtime) quiesce() {
	r.inFlight.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.inFlight.cancels))
	for _, cancel := range r.inFlight.cancels {
		cancels = append(cancels, cancel)
	}
	r.inFlight.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	r.abortParkedToolRuns(errors.New("cursor runtime quiesce"))
	r.evictAllSessions()
}

// ValidateAPIKey proves a key works and returns the account email it authenticates as. It uses
// the acquisition timeout only.
func (r *Runtime) ValidateAPIKey(ctx context.Context, apiKey string) (email string, err error) {
	if r == nil {
		return "", errors.New("cursor runtime is not available")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", errors.New("cursor api key is empty")
	}
	process, errProcess := r.acquireBridge(r.settingsSnapshot().ProxyURL)
	if errProcess != nil {
		return "", errProcess
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, loginValidateTimeout)
	defer cancel()
	response, errMe := process.cursor.Me(ctx, connect.NewRequest(&sdkv1.MeRequest{
		Options: &sdkv1.CursorRequestOptions{ApiKey: apiKey},
	}))
	if errMe != nil {
		// Return the classified error, not a flattened message: callers (the management
		// import handler, the CLI login) map the failure's status onto their own contracts,
		// and a plain string erases the distinction between a rejected key and an outage.
		return "", &upstreamError{failure: failureFrom(errMe)}
	}
	return strings.TrimSpace(response.Msg.GetUser().GetUserEmail()), nil
}

// DiscoverModels asks Cursor which models one credential can reach and maps them onto registry
// model info. Discovery forces a catalog refresh: this is the path whose whole purpose is to
// report the current list, and the cache it fills keeps model resolution off the hot path for
// subsequent requests.
func (r *Runtime) DiscoverModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	if r == nil {
		return nil, errors.New("cursor runtime is not available")
	}
	apiKey := apiKeyFromAuth(auth)
	if apiKey == "" {
		return nil, errors.New("cursor auth does not contain an api_key")
	}
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
		if proxyURL == "" {
			proxyURL = r.settingsSnapshot().ProxyURL
		}
	}
	process, errProcess := r.acquireBridge(proxyURL)
	if errProcess != nil {
		return nil, errProcess
	}
	catalog, errCatalog := r.catalogFor(ctx, process, apiKey, true)
	if errCatalog != nil {
		return nil, errCatalog
	}
	models := make([]*registry.ModelInfo, 0, len(catalog))
	for _, model := range catalog {
		if info, ok := modelInfoFromCatalog(model); ok {
			models = append(models, info)
		}
	}
	return models, nil
}

// apiKeyFromAuth reads the credential from the auth record's persisted metadata, tolerating
// both the documented snake_case field and the camelCase spelling used by the Cursor SDK.
func apiKeyFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	for _, field := range []string{"api_key", "apiKey"} {
		if value, ok := auth.Metadata[field].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// CountTokensEstimate mirrors the plugin's character estimate for one chat payload.
func CountTokensEstimate(payload []byte) (int, error) {
	chat, errParse := parseChatRequest(payload)
	if errParse != nil {
		return 0, errParse
	}
	return (len(chat.Prompt) + 3) / 4, nil
}

// unsupportedHTTPResponse is the body and status CountTokens/HttpRequest surfaces for paths the
// SDK does not expose.
func unsupportedHTTPResponse() (int, []byte) {
	return http.StatusNotImplemented, []byte(`{"error":{"message":"cursor provider does not support raw HTTP passthrough","type":"unsupported"}}`)
}

var errRuntimeStopped = errors.New("cursor runtime is stopped")

// defaultOptimizeFor is applied to the auto-smart router model, which rejects requests
// that omit the optimize_for parameter.
const defaultOptimizeFor = "balanced"

// SettingsEqual reports whether the runtime's settings match cfg field for field. The service
// layer uses this to decide between reusing and force-replacing the executor on config reload.
func (r *Runtime) SettingsEqual(cfg Settings) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings.BridgePath != cfg.BridgePath || r.settings.ProxyURL != cfg.ProxyURL || r.settings.OptimizeFor != cfg.OptimizeFor {
		return false
	}
	if len(r.settings.Weights) != len(cfg.Weights) {
		return false
	}
	for key, value := range cfg.Weights {
		if existing, ok := r.settings.Weights[key]; !ok || existing != value {
			return false
		}
	}
	return true
}

// CloseAllSessions evicts every cached session and tears its agent down.
func (r *Runtime) CloseAllSessions() {
	if r == nil {
		return
	}
	r.evictAllSessions()
}

// TrackInFlight registers a cancellable in-flight executor run so Close/quiesce can cancel it
// before bridges are torn down. The returned release function must be called when the run ends.
// Cancelling first lets in-flight callers observe context.Canceled (which the executor maps to
// the manager's cancellation contract) instead of an unclassified bridge transport failure.
func (r *Runtime) TrackInFlight(cancel context.CancelFunc) (release func()) {
	if r == nil || cancel == nil {
		return func() {}
	}
	id := r.inFlight.register(cancel)
	return func() { r.inFlight.unregister(id) }
}

// CloseSession releases one conversation session when the runtime has one matching the id.
//
// Cursor conversation sessions are keyed by an internal conversation fingerprint, not by the
// downstream execution-session id, so an exact match is generally impossible; matching would
// require keying the session cache by the host session id. Per the contract above the entry,
// an unmatched id is a no-op -- evicting everything here would let one client's session close
// evict every other concurrent conversation.
func (r *Runtime) CloseSession(sessionID string) {
	if r == nil {
		return
	}
	_ = sessionID
}
