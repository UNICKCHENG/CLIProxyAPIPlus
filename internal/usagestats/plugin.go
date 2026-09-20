package usagestats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelprice"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// recordTokens is the normalized token view of one usage record.
type recordTokens struct {
	modelprice.Tokens
	Total        int64
	Unclassified int64
}

func (t recordTokens) totalTotals() TokenTotals {
	return TokenTotals{
		Input:        t.UncachedInput,
		Output:       t.Output,
		Reasoning:    t.Reasoning,
		CacheRead:    t.CacheRead,
		CacheWrite:   t.CacheWrite,
		Unclassified: t.Unclassified,
		Total:        t.Total,
	}
}

// tokensFromRecord prefers the canonical v2 breakdown and falls back to the
// legacy flat counters for providers that do not emit it yet.
func tokensFromRecord(record coreusage.Record) recordTokens {
	breakdown := record.Detail.TokenBreakdown
	if breakdown.Valid() {
		tokens := recordTokens{
			Tokens: modelprice.Tokens{
				UncachedInput: breakdown.Input.UncachedTokens,
				CacheRead:     breakdown.Input.CacheReadTokens,
				CacheWrite:    breakdown.Input.CacheWriteTokens,
				Output:        breakdown.Output.NonReasoningTokens,
				Reasoning:     breakdown.Output.ReasoningTokens,
			},
			Total:        breakdown.TotalTokens,
			Unclassified: breakdown.UnclassifiedTokens,
		}
		if tokens.Total == 0 {
			tokens.Total = tokens.Tokens.Total() + tokens.Unclassified
		}
		return tokens
	}

	detail := record.Detail
	cacheRead := detail.CacheReadTokens
	cacheWrite := detail.CacheCreationTokens
	if cacheRead <= 0 && detail.CachedTokens > 0 {
		cacheRead = detail.CachedTokens
	}

	inputTotal := detail.InputTokens
	if inputTotal <= 0 {
		inputTotal = detail.TotalTokens - detail.OutputTokens
	}
	if inputTotal < 0 {
		inputTotal = 0
	}
	uncached := inputTotal - cacheRead - cacheWrite
	if uncached < 0 {
		uncached = 0
	}

	outputTotal := detail.OutputTokens
	if outputTotal < 0 {
		outputTotal = 0
	}
	reasoning := detail.ReasoningTokens
	if reasoning < 0 {
		reasoning = 0
	}
	if reasoning > outputTotal {
		reasoning = outputTotal
	}
	nonReasoning := outputTotal - reasoning

	total := detail.TotalTokens
	if total <= 0 {
		total = inputTotal + outputTotal
	}
	tokens := recordTokens{
		Tokens: modelprice.Tokens{
			UncachedInput: uncached,
			CacheRead:     cacheRead,
			CacheWrite:    cacheWrite,
			Output:        nonReasoning,
			Reasoning:     reasoning,
		},
		Total: total,
	}
	if unclassified := total - (inputTotal + outputTotal); unclassified > 0 {
		tokens.Unclassified = unclassified
	}
	return tokens
}

// costFor resolves the model price and returns the estimated USD cost.
func costFor(record coreusage.Record, tokens recordTokens) (float64, bool) {
	candidates := []string{
		strings.TrimSpace(record.Model),
		strings.TrimSpace(record.ResponseModel),
		strings.TrimSpace(record.Alias),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		price, ok := modelprice.Lookup(candidate, record.Provider)
		if !ok {
			continue
		}
		return price.Cost(tokens.Tokens), true
	}
	return 0, false
}

// effectiveModel returns the name used to group model statistics: the client
// requested alias when present, otherwise the upstream model.
func effectiveModel(record coreusage.Record) string {
	if alias := strings.TrimSpace(record.Alias); alias != "" {
		return alias
	}
	if model := strings.TrimSpace(record.Model); model != "" {
		return model
	}
	return strings.TrimSpace(record.ResponseModel)
}

// channelIdentity builds a stable identifier for the upstream credential that
// served the request.
func channelIdentity(record coreusage.Record) string {
	provider := strings.TrimSpace(record.Provider)
	authType := strings.TrimSpace(record.AuthType)

	identity := strings.TrimSpace(record.AuthID)
	if identity == "" {
		identity = strings.TrimSpace(record.AuthIndex)
	}
	if identity == "" {
		if token := strings.TrimSpace(record.AccessTokenSHA256); token != "" {
			identity = "token:" + shortHash(token)
		}
	}
	if identity == "" {
		if key := strings.TrimSpace(record.APIKey); key != "" {
			identity = "apikey:" + shortHash(key)
		}
	}
	if identity == "" && provider == "" {
		return ""
	}
	if identity == "" {
		identity = "anonymous"
	}

	parts := make([]string, 0, 3)
	for _, part := range []string{provider, authType, identity} {
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "|")
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// plugin forwards usage records into the default store.
type plugin struct{}

// HandleUsage implements coreusage.Plugin.
func (plugin) HandleUsage(_ context.Context, record coreusage.Record) {
	DefaultStore().Record(record)
}

func init() {
	coreusage.RegisterNamedPlugin("usage-stats", plugin{})
}
