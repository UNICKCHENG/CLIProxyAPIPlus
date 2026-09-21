package management

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	cursorruntime "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// errCursorProviderUnavailable is the stable error reported when no native Cursor executor is
// registered, so early-server-construction imports fail with a recognizable message instead of
// constructing an unrelated long-lived bridge.
const errCursorProviderUnavailable = "cursor provider unavailable"

// cursorService resolves the shared native Cursor runtime for import requests. Package-level
// so tests can substitute a fake; production prefers the runtime wired from the service
// composition root and falls back to the registered executor's runtime.
var cursorService = func(h *Handler) cursorRuntimeProvider {
	if h == nil {
		return nil
	}
	if h.serviceRuntime != nil {
		return h.serviceRuntime
	}
	if h.authManager == nil {
		return nil
	}
	exec, ok := h.authManager.Executor("cursor")
	if !ok || exec == nil {
		return nil
	}
	if typed, isCursor := exec.(cursorRuntimeProvider); isCursor {
		return typed
	}
	return nil
}

// SetCursorServiceRuntime wires the native Cursor runtime from the service composition root,
// so the HTTP handler never constructs a second untracked persistent runtime.
func (h *Handler) SetCursorServiceRuntime(runtime cursorRuntimeProvider) {
	if h == nil {
		return
	}
	h.serviceRuntime = runtime
}

// ImportCursorAPIKey handles POST /v0/management/cursor-auth.
//
// Wire contract: JSON body {"api_key": "key_..."}; success {"status":"ok",
// "name":"cursor-<account>.json","label":"..."} with no secret in the response. Blank or
// malformed input returns 400; Cursor validation failure returns the source status/message
// without persisting; a missing native Cursor provider returns 503.
func (h *Handler) ImportCursorAPIKey(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	var body struct {
		APIKey string `json:"api_key"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body must be JSON with an api_key field"})
		return
	}
	apiKey := strings.TrimSpace(body.APIKey)
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key is required"})
		return
	}
	runtime := cursorService(h)
	if runtime == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errCursorProviderUnavailable})
		return
	}

	email, errValidate := runtime.ValidateAPIKey(c.Request.Context(), apiKey)
	if errValidate != nil {
		// Validation failures must not persist anything. Classify via the runtime's
		// structured failure rather than message text: 401 = bad key, 403 = forbidden,
		// 429 = quota, a transport outage = 502, and an unclassified failure keeps 502 so
		// a novel error never reads to the client as "invalid key".
		status := http.StatusBadGateway
		failure := cursorruntime.FailureFrom(errValidate)
		switch failure.HTTPStatus {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			status = failure.HTTPStatus
		}
		c.JSON(status, gin.H{"error": failure.Message})
		return
	}

	record := cursorImportRecord(apiKey, email)

	if _, errSave := h.saveTokenRecord(c.Request.Context(), record); errSave != nil {
		log.WithError(errSave).Warn("failed to save Cursor auth record")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save authentication tokens"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"name":   record.FileName,
		"label":  record.Label,
	})
}

// cursorImportRecord builds the persisted record for a validated key: type cursor, the key,
// and the validated email. saveTokenRecord -> mergeExistingAuthFileMetadata carries over
// operator-owned fields from any existing file of the same account name, so re-imports
// preserve label, prefix, proxy, disabled, note, aliases, exclusions, and unknown fields.
func cursorImportRecord(apiKey, email string) *coreauth.Auth {
	fileName := cursorImportFileName(email)
	label := email
	if label == "" {
		label = strings.TrimSuffix(fileName, ".json")
	}
	metadata := map[string]any{
		"type":    "cursor",
		"api_key": apiKey,
	}
	if email != "" {
		metadata["email"] = email
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "cursor",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
		},
	}
}

// cursorImportFileName derives a stable per-account file name so importing a key for the same
// account replaces its credential instead of accumulating duplicates.
func cursorImportFileName(email string) string {
	account := cursorSanitizeFileComponent(email)
	if account == "" {
		return "cursor-apikey.json"
	}
	return "cursor-" + account + ".json"
}

// cursorSanitizeFileComponent reduces an email to a safe file name fragment.
func cursorSanitizeFileComponent(value string) string {
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

// CursorRuntimeAdapter resolves the native Cursor runtime through the core auth manager's
// registered executor. It is the composition-root wiring for SetCursorServiceRuntime and
// tolerates a nil or not-yet-registered manager, which yields the stable 503 in the handler.
type CursorRuntimeAdapter struct {
	AuthManager *coreauth.Manager
}

// ValidateAPIKey validates one Cursor API key through the registered native executor's
// shared runtime.
func (a *CursorRuntimeAdapter) ValidateAPIKey(ctx context.Context, apiKey string) (string, error) {
	if a == nil || a.AuthManager == nil {
		return "", errors.New(errCursorProviderUnavailable)
	}
	exec, ok := a.AuthManager.Executor("cursor")
	if !ok || exec == nil {
		return "", errors.New(errCursorProviderUnavailable)
	}
	runtime := runtimeFromCursorExecutor(exec)
	if runtime == nil {
		return "", errors.New(errCursorProviderUnavailable)
	}
	return runtime.ValidateAPIKey(ctx, apiKey)
}

// runtimeFromCursorExecutor extracts the shared runtime from a registered cursor executor.
func runtimeFromCursorExecutor(exec coreauth.ProviderExecutor) *cursorruntime.Runtime {
	typed, isCursor := exec.(*executor.CursorExecutor)
	if !isCursor || typed == nil {
		return nil
	}
	return typed.Runtime()
}
