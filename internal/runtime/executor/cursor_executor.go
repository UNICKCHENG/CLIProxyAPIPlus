package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	cursorruntime "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var (
	_ cliproxyauth.ProviderExecutor       = (*CursorExecutor)(nil)
	_ cliproxyauth.ExecutionSessionCloser = (*CursorExecutor)(nil)
)

// CursorExecutor executes requests against the native Cursor SDK bridge runtime.
type CursorExecutor struct {
	cfg     *config.Config
	runtime *cursorruntime.Runtime
}

// NewCursorExecutor creates a new native Cursor executor owning one shared runtime.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	exec := &CursorExecutor{cfg: cfg}
	exec.applyConfig(cfg)
	return exec
}

// applyConfig (re)applies provider configuration to the runtime.
func (e *CursorExecutor) applyConfig(cfg *config.Config) {
	if e == nil {
		return
	}
	if e.runtime == nil {
		e.runtime = cursorruntime.NewRuntime(cursorSettingsFrom(cfg))
		return
	}
	settings := cursorSettingsFrom(cfg)
	if errConfigure := e.runtime.Configure(settings); errConfigure != nil {
		log.WithError(errConfigure).Warn("cursor executor: applying configuration failed; keeping previous settings")
	}
}

// UsesConfig reports whether the executor's runtime already reflects cfg. Used by the service
// layer to decide between reuse and force-replacement on config reload.
func (e *CursorExecutor) UsesConfig(cfg *config.Config) bool {
	if e == nil || e.runtime == nil {
		return false
	}
	return e.runtime.SettingsEqual(cursorSettingsFrom(cfg))
}

// Close shuts the runtime down: bridges, in-flight runs, sessions, scratch workspaces.
func (e *CursorExecutor) Close() {
	if e == nil || e.runtime == nil {
		return
	}
	e.runtime.Close()
}

func cursorSettingsFrom(cfg *config.Config) cursorruntime.Settings {
	if cfg == nil {
		return cursorruntime.Settings{OptimizeFor: "balanced"}
	}
	optimizeFor := strings.ToLower(strings.TrimSpace(cfg.Cursor.OptimizeFor))
	if optimizeFor == "" {
		optimizeFor = "balanced"
	}
	weights := make(map[string]int, len(cfg.Cursor.Weights))
	for key, weight := range cfg.Cursor.Weights {
		weights[strings.ToLower(strings.TrimSpace(key))] = weight
	}
	return cursorruntime.Settings{
		BridgePath:  strings.TrimSpace(cfg.Cursor.BridgePath),
		ProxyURL:    cursorProxyURLFromConfig(cfg),
		OptimizeFor: optimizeFor,
		Weights:     weights,
	}
}

// cursorProxyURLFromConfig resolves the Cursor egress proxy with the project-wide
// three-level fallback: credential proxy_url > cursor.proxy-url > global proxy-url. The
// credential level is applied per request in buildRunRequest; here the entry-level value
// falls back to the host's global proxy-url, which the bridge runtime, key validation and
// model discovery all inherit through Settings.
func cursorProxyURLFromConfig(cfg *config.Config) string {
	if proxyURL := strings.TrimSpace(cfg.Cursor.ProxyURL); proxyURL != "" {
		return proxyURL
	}
	return strings.TrimSpace(cfg.ProxyURL)
}

// Identifier returns the provider identifier "cursor".
func (e *CursorExecutor) Identifier() string {
	return "cursor"
}

// Runtime exposes the shared Cursor runtime for model discovery and credential validation.
// The service layer and management import flow reuse it rather than constructing another
// long-lived bridge.
func (e *CursorExecutor) Runtime() *cursorruntime.Runtime {
	if e == nil {
		return nil
	}
	return e.runtime
}

// RequestToFormat reports that Cursor consumes and emits OpenAI chat-completions payloads, so
// the host's existing translators handle every client protocol.
func (e *CursorExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// PrepareRequest is a no-op: Cursor traffic never passes through the host HTTP client.
func (e *CursorExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

// HttpRequest is unsupported: Cursor exposes no passthrough HTTP surface through the SDK.
func (e *CursorExecutor) HttpRequest(_ context.Context, _ *cliproxyauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, &coreauthStatusError{
		status:  http.StatusNotImplemented,
		message: "cursor provider does not support raw HTTP passthrough",
	}
}

// Refresh re-validates the stored key without rotating it. Cursor has no refresh token and no
// programmatic key rotation: a key is replaced only by the operator importing a new one.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, errors.New("cursor executor: auth is nil")
	}
	apiKey := cursorAPIKeyFromAuth(auth)
	if apiKey == "" {
		return nil, &coreauthStatusError{
			status:  http.StatusUnauthorized,
			message: "cursor auth no longer contains an api_key",
		}
	}
	if expiry, ok := cursorExpiryFromAuth(auth); ok && !timeNowUTC().Before(expiry) {
		return nil, &coreauthStatusError{
			status:  http.StatusUnauthorized,
			message: fmt.Sprintf("cursor api key expired at %s; import a new key with --cursor-login", expiry.Format("2006-01-02T15:04:05Z07:00")),
		}
	}
	// Local state only: the scheduler already treats 401s from Execute as terminal for this
	// credential, and a network probe here would couple refresh scheduling to bridge uptime.
	return auth, nil
}

// CountTokens approximates usage. The Agent SDK reports token counts only after a run, so
// there is no upstream endpoint to ask ahead of time.
func (e *CursorExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	estimate, errEstimate := cursorruntime.CountTokensEstimate(req.Payload)
	if errEstimate != nil {
		return cliproxyexecutor.Response{}, &coreauthStatusError{
			status:        http.StatusBadRequest,
			message:       errEstimate.Error(),
			requestScoped: true,
		}
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"input_tokens": estimate,
		"total_tokens": estimate,
	})
	if errMarshal != nil {
		return cliproxyexecutor.Response{}, errMarshal
	}
	return cliproxyexecutor.Response{Payload: payload}, nil
}

// Execute runs a non-streaming completion and returns an OpenAI chat.completion body.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.runtime == nil {
		return cliproxyexecutor.Response{}, &coreauthStatusError{
			status:  http.StatusServiceUnavailable,
			message: "cursor provider unavailable",
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	}
	runReq := e.buildRunRequest(auth, req, model, opts)
	// Track the run so a Close() on this executor cancels it first: the caller then sees
	// context.Canceled (the manager's cancellation contract) rather than an unclassified
	// bridge transport failure from the process being killed underneath it.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	release := e.runtime.TrackInFlight(cancelRun)
	defer release()
	payload, usage, errRun := e.runtime.RunChat(runCtx, runReq)
	if errRun != nil {
		return cliproxyexecutor.Response{}, cursorErrorFromRuntime(errRun)
	}
	publishCursorUsage(ctx, e, model, auth, usage, opts)
	return cliproxyexecutor.Response{Payload: payload}, nil
}

// ExecuteStream runs a streaming completion, emitting OpenAI chat-completion chunks. The
// stream framing decision belongs to the response translator path: this executor emits bare
// "data: " SSE frames plus the terminating [DONE] marker, matching the other OpenAI-format
// executors.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.runtime == nil {
		return nil, &coreauthStatusError{
			status:  http.StatusServiceUnavailable,
			message: "cursor provider unavailable",
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	}
	runReq := e.buildRunRequest(auth, req, model, opts)

	out := make(chan cliproxyexecutor.StreamChunk)
	streamCtx, cancel := context.WithCancel(ctx)
	// Track the stream so a Close() on this executor cancels it before bridges die; the
	// goroutine below already treats streamCtx cancellation as a normal end of stream.
	release := e.runtime.TrackInFlight(cancel)
	go func() {
		defer close(out)
		defer cancel()
		defer release()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Errorf("cursor stream panic: %v", recovered)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("cursor stream panic: %v", recovered)}
			}
		}()
		usage, errRun := e.runtime.RunChatStream(streamCtx, runReq, func(payload []byte) error {
			frame := make([]byte, 0, len(payload)+8)
			frame = append(frame, "data: "...)
			frame = append(frame, payload...)
			frame = append(frame, '\n', '\n')
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: frame}:
				return nil
			case <-streamCtx.Done():
				// Nobody is reading the output any more; RunChatStream aborts the upstream
				// run when emit returns, so Cursor stops billing work that is discarded.
				return streamCtx.Err()
			}
		})
		if errRun != nil {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: cursorErrorFromRuntime(errRun)}:
			case <-streamCtx.Done():
			}
			return
		}
		publishCursorUsage(ctx, e, model, auth, usage, opts)
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}:
		case <-streamCtx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Chunks: out}, nil
}

// CloseExecutionSession releases bridge-owned resources. The sentinel id closes every Cursor
// session; a specific session id releases only a matching conversation session.
func (e *CursorExecutor) CloseExecutionSession(sessionID string) {
	if e == nil || e.runtime == nil {
		return
	}
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID {
		e.runtime.CloseAllSessions()
		return
	}
	e.runtime.CloseSession(sessionID)
}

// buildRunRequest assembles the runtime request from the auth record and executor options.
func (e *CursorExecutor) buildRunRequest(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, model string, opts cliproxyexecutor.Options) cursorruntime.ChatRunRequest {
	proxyURL := ""
	optimizeFor := ""
	if e.cfg != nil {
		optimizeFor = strings.ToLower(strings.TrimSpace(e.cfg.Cursor.OptimizeFor))
		proxyURL = cursorProxyURLFromConfig(e.cfg)
	}
	if optimizeFor == "" {
		optimizeFor = "balanced"
	}
	// The credential's own proxy_url overrides the provider-level fallback.
	if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	return cursorruntime.ChatRunRequest{
		APIKey:      cursorAPIKeyFromAuth(auth),
		ProxyURL:    proxyURL,
		Model:       model,
		OptimizeFor: optimizeFor,
		Payload:     req.Payload,
	}
}

// cursorAPIKeyFromAuth reads the credential from the persisted auth metadata, tolerating both
// the documented snake_case field and the camelCase spelling used by the Cursor SDK.
func cursorAPIKeyFromAuth(auth *cliproxyauth.Auth) string {
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

// cursorExpiryFromAuth reads the expiry recorded by an import flow, if any.
func cursorExpiryFromAuth(auth *cliproxyauth.Auth) (time.Time, bool) {
	if auth == nil || auth.Metadata == nil {
		return time.Time{}, false
	}
	if raw, ok := auth.Metadata["expires_at"].(string); ok {
		if parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(raw)); errParse == nil {
			return parsed.UTC(), true
		}
	}
	for _, field := range []string{"expires_at_ms", "apiKeyExpiresAtMs"} {
		switch value := auth.Metadata[field].(type) {
		case float64:
			if value > 0 {
				return time.UnixMilli(int64(value)).UTC(), true
			}
		case int64:
			if value > 0 {
				return time.UnixMilli(value).UTC(), true
			}
		}
	}
	return time.Time{}, false
}

// cursorErrorFromRuntime converts a runtime failure into the manager's error contracts.
//
// 401 retires the credential, 403 marks role failure, 429 cools with retryability, and 400
// bodies that classify as request faults (model/plan/validation) stay request-scoped so one
// bad model does not park the credential. Transport/bridge lifecycle failures remain
// unclassified so one shared bridge outage does not cool every credential.
func cursorErrorFromRuntime(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	failure := cursorruntime.FailureFrom(err)
	status := failure.HTTPStatus
	if status == 0 {
		// Unclassified: no status is right for an outage that affects every credential
		// equally rather than discrediting this one. The message preserves the retryability
		// hint the runtime computed.
		return &coreauthStatusError{
			message:   failure.Message,
			retryable: failure.Retryable,
		}
	}
	return &coreauthStatusError{
		status:        status,
		message:       failure.Message,
		retryable:     failure.Retryable,
		requestScoped: status == http.StatusBadRequest,
	}
}

// publishCursorUsage forwards terminal token accounting to the usage pipeline.
func publishCursorUsage(ctx context.Context, exec *CursorExecutor, model string, auth *cliproxyauth.Auth, usage *cursorruntime.ChatUsage, _ cliproxyexecutor.Options) {
	if usage == nil {
		return
	}
	reporter := helps.NewExecutorUsageReporter(ctx, exec, model, auth)
	reporter.Publish(ctx, coreusage.Detail{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	})
}
