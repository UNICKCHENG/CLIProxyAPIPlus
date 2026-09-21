package synthesizer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// cursorRefreshInterval keeps the scheduler from re-checking a static API key in a tight loop.
// Cursor API keys carry no expiry the synthesizer can read, and none of them can be rotated
// programmatically: renewal always means the operator supplying a new key.
const cursorRefreshInterval = 365 * 24 * time.Hour

// synthesizeCursorAuth converts one Cursor auth JSON file into a manager auth record.
//
// Cursor is not an OAuth authorization-code provider: the credential is a dashboard API key.
// The record is still marked auth_kind "oauth" deliberately so the existing OAuth
// alias/exclusion/request-scoped-error machinery (oauth-model-alias.cursor,
// oauth-excluded-models.cursor) applies to this non-OAuth credential.
func synthesizeCursorAuth(ctx *SynthesisContext, fullPath string, data []byte, metadata map[string]any) (*coreauth.Auth, error) {
	now := ctx.Now
	cfg := ctx.Config

	apiKey := cursorStringField(metadata, "api_key", "apiKey")
	if apiKey == "" {
		return nil, fmt.Errorf("cursor auth file requires a non-empty api_key")
	}
	expiry := cursorExpiryFromMetadata(metadata)
	if !expiry.IsZero() && !now.UTC().Before(expiry) {
		return nil, fmt.Errorf("cursor api key expired at %s; import a new key with --cursor-login", expiry.Format(time.RFC3339))
	}

	email, _ := metadata["email"].(string)
	email = strings.TrimSpace(email)
	label := email
	if label == "" {
		label = cursorStringField(metadata, "label", "")
	}
	if label == "" {
		label = strings.TrimSuffix(filepath.Base(fullPath), ".json")
	}
	if label == "" {
		label = "cursor"
	}

	proxyURL := cursorStringField(metadata, "proxy_url", "")
	prefix := ""
	if rawPrefix := cursorStringField(metadata, "prefix", ""); rawPrefix != "" {
		trimmed := strings.Trim(strings.TrimSpace(rawPrefix), "/")
		if trimmed != "" && !strings.Contains(trimmed, "/") {
			prefix = trimmed
		}
	}
	disabled, _ := metadata["disabled"].(bool)
	status := coreauth.StatusActive
	if disabled {
		status = coreauth.StatusDisabled
	}

	id := fullPath
	if strings.TrimSpace(ctx.AuthDir) != "" {
		if rel, errRel := filepath.Rel(ctx.AuthDir, fullPath); errRel == nil && rel != "" {
			id = rel
		}
	}

	a := &coreauth.Auth{
		ID:       id,
		Provider: "cursor",
		Label:    label,
		Prefix:   prefix,
		Status:   status,
		Disabled: disabled,
		Attributes: map[string]string{
			coreauth.AttributeSource:        fullPath,
			coreauth.AttributePath:          fullPath,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			coreauth.AttributeAuthKind:      coreauth.AuthKindOAuth,
		},
		ProxyURL:  proxyURL,
		Metadata:  metadata,
		CreatedAt: now,
		UpdatedAt: now,
		// One year when no expiry is present; the parsed expiry otherwise.
		NextRefreshAfter: cursorRefreshDeadline(expiry, now),
	}

	// File-level weight takes precedence over a configured cursor.weights entry.
	if errWeight := coreauth.ApplyAuthWeightMetadata(a, metadata); errWeight != nil {
		return nil, fmt.Errorf("invalid auth weight in %s: %w", filepath.Base(fullPath), errWeight)
	}
	if _, hasFileWeight := metadata[coreauth.AttributeWeight]; !hasFileWeight {
		if weight, configured := cursorConfiguredWeight(cfg, metadata, filepath.Base(fullPath)); configured {
			if a.Attributes == nil {
				a.Attributes = make(map[string]string)
			}
			a.Attributes[coreauth.AttributeWeight] = strconv.Itoa(weight)
		}
	}

	// Priority, note, custom headers, aliases, exclusions: the same OAuth-style machinery.
	if rawPriority, ok := metadata["priority"]; ok {
		switch v := rawPriority.(type) {
		case float64:
			a.Attributes["priority"] = strconv.Itoa(int(v))
		case string:
			priority := strings.TrimSpace(v)
			if _, errAtoi := strconv.Atoi(priority); errAtoi == nil {
				a.Attributes["priority"] = priority
			}
		}
	}
	if rawNote, ok := metadata["note"]; ok {
		if note, isStr := rawNote.(string); isStr {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				a.Attributes["note"] = trimmed
			}
		}
	}
	coreauth.ApplyCustomHeadersFromMetadata(a)
	perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
	perAccountModelAliases := extractOAuthModelAliasesFromMetadata(metadata)
	coreauth.SetOAuthModelAliasesAttribute(a, perAccountModelAliases)
	ApplyAuthExcludedModelsMeta(a, cfg, perAccountExcluded, "oauth")
	applyFingerprintProfileAttribute(a, metadata)
	return a, nil
}

// cursorStringField reads the first non-empty string among the given keys.
func cursorStringField(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if value, ok := metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// cursorExpiryFromMetadata reads the expiry recorded by an import flow. Hand-written
// dashboard keys omit it, in which case the synthesizer cannot know when the key lapses.
func cursorExpiryFromMetadata(metadata map[string]any) time.Time {
	if raw := cursorStringField(metadata, "expires_at", "expiresAt"); raw != "" {
		if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
			return parsed.UTC()
		}
	}
	for _, key := range []string{"expires_at_ms", "apiKeyExpiresAtMs"} {
		if raw, ok := metadata[key]; ok {
			switch value := raw.(type) {
			case float64:
				if value > 0 {
					return time.UnixMilli(int64(value)).UTC()
				}
			case int64:
				if value > 0 {
					return time.UnixMilli(value).UTC()
				}
			case json.Number:
				if millis, errParse := value.Int64(); errParse == nil && millis > 0 {
					return time.UnixMilli(millis).UTC()
				}
			}
		}
	}
	return time.Time{}
}

// cursorRefreshDeadline tells the scheduler when to look at this credential again: at expiry
// for keys that report one, otherwise far in the future because there is nothing to re-check.
func cursorRefreshDeadline(expiry time.Time, now time.Time) time.Time {
	if expiry.IsZero() {
		return now.UTC().Add(cursorRefreshInterval)
	}
	return expiry
}

// cursorConfiguredWeight resolves the weight for one credential from cursor.weights.
// Lookup order is case-insensitive account email, filename with .json, filename without
// .json. It never overrides a file-level weight.
func cursorConfiguredWeight(cfg *config.Config, metadata map[string]any, fileName string) (int, bool) {
	if cfg == nil || len(cfg.Cursor.Weights) == 0 {
		return 0, false
	}
	lookup := make([]string, 0, 3)
	add := func(value string) {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			return
		}
		for _, existing := range lookup {
			if existing == normalized {
				return
			}
		}
		lookup = append(lookup, normalized)
	}
	add(cursorStringField(metadata, "email"))
	name := strings.ToLower(strings.TrimSpace(fileName))
	add(name)
	add(strings.TrimSuffix(name, ".json"))
	for _, key := range lookup {
		if weight, ok := cfg.Cursor.Weights[key]; ok {
			return weight, true
		}
	}
	return 0, false
}

// existingCursorAuthStorage reads the auth file an import is about to replace. A missing,
// unreadable or malformed file simply yields nothing to carry over. Exported for the
// management import flow so re-imports preserve operator-owned fields.
func existingCursorAuthStorage(authDir, fileName string) map[string]any {
	authDir = strings.TrimSpace(authDir)
	fileName = strings.TrimSpace(fileName)
	if authDir == "" || fileName == "" {
		return make(map[string]any)
	}
	raw, errRead := os.ReadFile(filepath.Join(authDir, fileName))
	if errRead != nil || len(raw) == 0 {
		return make(map[string]any)
	}
	var existing map[string]any
	if errUnmarshal := json.Unmarshal(raw, &existing); errUnmarshal != nil || existing == nil {
		return make(map[string]any)
	}
	return existing
}
