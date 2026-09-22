// Package modelprice maintains a model price table and computes request costs.
//
// Prices are stored as USD per single token. The table is populated from the
// LiteLLM public cost map (model_prices_and_context_window.json) and can be
// overridden per model through configuration.
package modelprice

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tokens holds the mutually exclusive token buckets used for cost estimation.
// It mirrors the canonical usage.TokenBreakdown contract.
type Tokens struct {
	UncachedInput int64
	CacheRead     int64
	CacheWrite    int64
	// Output holds non-reasoning output tokens.
	Output int64
	// Reasoning holds reasoning output tokens. When the model has no dedicated
	// reasoning price it is billed at the output rate.
	Reasoning int64
}

// Total returns the sum of all token buckets.
func (t Tokens) Total() int64 {
	return t.UncachedInput + t.CacheRead + t.CacheWrite + t.Output + t.Reasoning
}

// Price describes the per-token prices of a single model in USD.
type Price struct {
	InputPerToken      float64 `json:"input_per_token"`
	OutputPerToken     float64 `json:"output_per_token"`
	CacheReadPerToken  float64 `json:"cache_read_per_token"`
	CacheWritePerToken float64 `json:"cache_write_per_token"`
	ReasoningPerToken  float64 `json:"reasoning_per_token"`
	// Source records where the price came from ("litellm", "override", ...).
	Source string `json:"source,omitempty"`
}

// Known reports whether the price carries at least one usable rate.
func (p Price) Known() bool {
	return p.InputPerToken > 0 || p.OutputPerToken > 0 ||
		p.CacheReadPerToken > 0 || p.CacheWritePerToken > 0
}

// Cost returns the estimated USD cost for the given token buckets.
func (p Price) Cost(t Tokens) float64 {
	reasoningRate := p.ReasoningPerToken
	if reasoningRate <= 0 {
		reasoningRate = p.OutputPerToken
	}
	return float64(t.UncachedInput)*p.InputPerToken +
		float64(t.CacheRead)*p.CacheReadPerToken +
		float64(t.CacheWrite)*p.CacheWritePerToken +
		float64(t.Output)*p.OutputPerToken +
		float64(t.Reasoning)*reasoningRate
}

// Table is a concurrency-safe model price lookup table.
type Table struct {
	mu sync.RWMutex
	// entries maps a normalized lookup key to a price.
	entries map[string]Price
	// cursorEntries maps a stable model-ID fingerprint to a price. It is kept
	// separate from entries so synthetic lookup keys never appear in the catalog.
	cursorEntries map[string]Price
	// priority tracks the provider priority of the winning entry per key.
	priority map[string]int
	// overrides are operator-provided prices; they always win over synced data.
	overrides map[string]Price
	// cursorOverrides is the fingerprint index for overrides.
	cursorOverrides map[string]Price

	source    string
	updatedAt time.Time
}

// NewTable returns an empty price table.
func NewTable() *Table {
	return &Table{
		entries:         make(map[string]Price),
		cursorEntries:   make(map[string]Price),
		priority:        make(map[string]int),
		overrides:       make(map[string]Price),
		cursorOverrides: make(map[string]Price),
	}
}

// Entry is one model in the price table, for read-only catalog display.
//
// Model is the literal lookup key the estimator matches against, so the catalog
// doubles as a debugging view for "why was this request unpriced?".
type Entry struct {
	Model string `json:"model"`
	// Override reports whether an operator override shadows the synced price.
	Override bool `json:"override"`
	Price
}

// Entries returns the full table sorted by model key. Keys shadowed by an
// override are reported once, with Override set.
func (t *Table) Entries() []Entry {
	if t == nil {
		return []Entry{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]Entry, 0, len(t.entries)+len(t.overrides))
	for key, price := range t.entries {
		if _, shadowed := t.overrides[key]; shadowed {
			continue
		}
		out = append(out, Entry{Model: key, Price: price})
	}
	for key, price := range t.overrides {
		out = append(out, Entry{Model: key, Override: true, Price: price})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// OverrideCount returns the number of operator-provided override keys.
func (t *Table) OverrideCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.overrides)
}

// EntryCount returns the number of keys in the synced base table.
//
// It deliberately ignores overrides: callers use it to ask "has anything been
// synced yet?" (e.g. whether a cached cost map still needs loading). For the
// number of rows Entries() would return, use CatalogSize.
func (t *Table) EntryCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entries)
}

// CatalogSize returns the number of rows Entries() reports: every base key that
// is not shadowed by an override, plus every override.
//
// It matches len(Entries()) without building or sorting the slice, so it is
// cheap enough to call while rendering a status block. Use this (not
// EntryCount) whenever the number is shown next to override counts, otherwise an
// override for a model absent upstream makes the catalog look smaller than the
// table it labels.
func (t *Table) CatalogSize() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	size := len(t.overrides)
	for key := range t.entries {
		if _, shadowed := t.overrides[key]; !shadowed {
			size++
		}
	}
	return size
}

// UpdatedAt returns the last time the base table was replaced.
func (t *Table) UpdatedAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.updatedAt
}

// Source returns a short description of the base table origin.
func (t *Table) Source() string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.source
}

// Lookup resolves a price for the given model name. The provider enables
// provider-specific ID normalization after exact candidates have been tried.
func (t *Table) Lookup(model string, provider string) (Price, bool) {
	if t == nil {
		return Price{}, false
	}
	candidates := lookupCandidates(model)
	if len(candidates) == 0 {
		return Price{}, false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	isCursor := strings.EqualFold(strings.TrimSpace(provider), "cursor")
	for _, candidate := range candidates {
		if price, ok := t.overrides[candidate]; ok {
			price.Source = "override"
			return price, true
		}
		if isCursor {
			if price, ok := t.cursorOverrides[cursorModelIdentity(candidate)]; ok {
				price.Source = "override"
				return price, true
			}
		}
	}
	for _, candidate := range candidates {
		if price, ok := t.entries[candidate]; ok {
			return price, true
		}
		if isCursor {
			if price, ok := t.cursorEntries[cursorModelIdentity(candidate)]; ok {
				return price, true
			}
		}
	}
	return Price{}, false
}

// SetOverrides replaces the operator-provided price overrides.
func (t *Table) SetOverrides(overrides map[string]Price) {
	if t == nil {
		return
	}
	normalized := make(map[string]Price, len(overrides))
	for key, price := range overrides {
		keys := indexKeys(key)
		for _, candidate := range keys {
			if candidate == "" {
				continue
			}
			stored := price
			stored.Source = "override"
			normalized[candidate] = stored
		}
	}
	t.mu.Lock()
	t.overrides = normalized
	t.cursorOverrides = buildCursorIdentityIndex(normalized)
	t.mu.Unlock()
}

// replaceBase atomically replaces the synced base table.
func (t *Table) replaceBase(entries map[string]Price, priorities map[string]int, source string) {
	t.mu.Lock()
	t.entries = entries
	t.cursorEntries = buildCursorIdentityIndex(entries)
	t.priority = priorities
	t.source = source
	// UTC throughout: the value is serialized to the client as-is.
	t.updatedAt = time.Now().UTC()
	t.mu.Unlock()
}

// buildCursorIdentityIndex creates an ambiguity-safe secondary index for the
// model IDs returned by Cursor. Cursor sometimes orders Claude version and
// family tokens differently from public provider IDs. A fingerprint is only
// usable when every matching catalog key publishes the same rates.
func buildCursorIdentityIndex(prices map[string]Price) map[string]Price {
	index := make(map[string]Price)
	ambiguous := make(map[string]struct{})
	identities := make(map[string]Price)
	for key, price := range prices {
		// Provider-qualified rows coexist with their bare winner in entries. Only
		// index the bare key so reseller-specific rates cannot create false
		// ambiguity against the first-party price selected by setEntry.
		if key != bareModelName(key) {
			continue
		}
		identity := cursorModelIdentity(key)
		if identity == "" {
			continue
		}
		if existing, ok := identities[identity]; ok && existing != price {
			delete(identities, identity)
			ambiguous[identity] = struct{}{}
			continue
		}
		if _, blocked := ambiguous[identity]; !blocked {
			identities[identity] = price
		}
	}
	for identity, price := range identities {
		index[identity] = price
	}
	// Cursor can expose a newly released version before the cached LiteLLM map
	// has the corresponding row. Add only documented same-price compatibility
	// aliases, and never let an alias overwrite a direct or ambiguous identity.
	for identity, price := range identities {
		for _, alias := range cursorIdentityAliases(identity) {
			if _, blocked := ambiguous[alias]; blocked {
				continue
			}
			if existing, ok := index[alias]; ok {
				if existing != price {
					delete(index, alias)
					ambiguous[alias] = struct{}{}
				}
				continue
			}
			index[alias] = price
		}
	}
	return index
}

func cursorIdentityAliases(identity string) []string {
	if identity == "4-6-grok" {
		return []string{"4-7-grok"}
	}
	return nil
}

// cursorModelIdentity reduces a model ID to stable, order-independent tokens.
// Release dates and "latest" aliases do not identify a separately priced model.
// Exact lookup still runs first, so explicitly priced variants always win.
func cursorModelIdentity(model string) string {
	value := strings.ToLower(strings.TrimSpace(bareModelName(model)))
	if value == "" {
		return ""
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	identity := parts[:0]
	for _, part := range parts {
		if part == "" || part == "latest" || isModelReleaseDate(part) {
			continue
		}
		identity = append(identity, part)
	}
	if len(identity) == 0 {
		return ""
	}
	sort.Strings(identity)
	return strings.Join(identity, "-")
}

func isModelReleaseDate(value string) bool {
	if len(value) != 8 || !strings.HasPrefix(value, "20") {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// setEntry inserts one entry when it wins the provider priority contest.
func setEntry(entries map[string]Price, priorities map[string]int, key string, price Price, priority int) {
	if key == "" || !price.Known() {
		return
	}
	if existing, ok := priorities[key]; ok && existing <= priority {
		return
	}
	entries[key] = price
	priorities[key] = priority
}

// lookupCandidates returns the normalized keys to try, most specific first.
func lookupCandidates(model string) []string {
	base := strings.ToLower(strings.TrimSpace(model))
	if base == "" {
		return nil
	}

	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}

	add(base)
	add(bareModelName(base))
	for _, suffix := range effortSuffixes {
		trimmed := strings.TrimSuffix(base, suffix)
		if trimmed != base {
			add(trimmed)
			add(bareModelName(trimmed))
		}
	}
	return out
}

// indexKeys returns every key a model definition should be indexed under.
func indexKeys(model string) []string {
	base := strings.ToLower(strings.TrimSpace(model))
	if base == "" {
		return nil
	}
	seen := make(map[string]struct{}, 4)
	out := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	add(base)
	add(bareModelName(base))
	return out
}

// bareModelName strips a provider prefix ("gemini/gemini-2.5-pro") and any
// bedrock-style vendor prefixes ("us.anthropic.claude-sonnet-4-5") to leave the
// model id.
//
// A dotted segment is only treated as a vendor prefix when everything before it
// is digit-free. That keeps version-bearing names such as "gemini-3.6-flash"
// (where "3" precedes the dot) intact while still unwrapping real vendor
// namespaces.
func bareModelName(model string) string {
	value := model
	if idx := strings.LastIndex(value, "/"); idx >= 0 && idx+1 < len(value) {
		value = value[idx+1:]
	}
	// Unwrap at most a few leading vendor namespaces ("us.anthropic.model").
	for range 3 {
		idx := strings.Index(value, ".")
		if idx <= 0 || idx+1 >= len(value) {
			break
		}
		if strings.ContainsAny(value[:idx], "0123456789") {
			break
		}
		value = value[idx+1:]
	}
	return value
}

// effortSuffixes are reasoning-effort and mode markers appended by clients.
// They are only tried as a fallback after an exact match fails.
var effortSuffixes = []string{
	"-thinking",
	"-non-thinking",
	"-non-reasoning",
	"-reasoning",
	"-xhigh",
	"-high",
	"-medium",
	"-minimal",
	"-low",
}

var defaultTable = NewTable()

// Default returns the process-wide price table.
func Default() *Table { return defaultTable }

// Lookup resolves a price using the default table.
func Lookup(model, provider string) (Price, bool) { return defaultTable.Lookup(model, provider) }

// Cost estimates the USD cost for a model request using the default table.
func Cost(model, provider string, tokens Tokens) (float64, bool) {
	price, ok := defaultTable.Lookup(model, provider)
	if !ok {
		return 0, false
	}
	return price.Cost(tokens), true
}

// ParsePerMillion converts a per-million-token price into per-token form.
func ParsePerMillion(value float64) float64 {
	if value <= 0 {
		return 0
	}
	return value / 1_000_000
}

// FormatUSD renders a cost with a stable, compact representation.
func FormatUSD(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
