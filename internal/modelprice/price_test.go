package modelprice

import (
	"math"
	"testing"
)

const syntheticCostMap = `{
  "sample_spec": {"input_cost_per_token": 0},
  "anthropic/claude-sonnet-4-5": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000003,
    "output_cost_per_token": 0.000015,
    "cache_read_input_token_cost": 0.0000003,
    "cache_creation_input_token_cost": 0.00000375
  },
  "anthropic/claude-sonnet-4-5-thinking": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000007,
    "output_cost_per_token": 0.000035
  },
  "openai/gpt-5": {
    "litellm_provider": "openai",
    "mode": "chat",
    "input_cost_per_token": 0.00000125,
    "output_cost_per_token": 0.00001
  },
  "openrouter/openai/gpt-5": {
    "litellm_provider": "openrouter",
    "mode": "chat",
    "input_cost_per_token": 0.0000099,
    "output_cost_per_token": 0.000099
  },
  "gemini/gemini-3-pro-preview": {
    "litellm_provider": "gemini",
    "mode": "chat",
    "input_cost_per_token": 0.00000125,
    "output_cost_per_token": 0.00001,
    "cache_read_input_token_cost": 0.000000125
  },
  "gemini/gemini-3-flash-preview": {
    "litellm_provider": "gemini",
    "mode": "chat",
    "input_cost_per_token": 0.0000005,
    "output_cost_per_token": 0.000003
  },
  "text-embedding-3-large": {
    "litellm_provider": "openai",
    "mode": "embedding",
    "input_cost_per_token": 0.00000013
  },
  "claude-opus-4-6": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000005,
    "output_cost_per_token": 0.000025,
    "output_cost_per_reasoning_token": 0.00004
  },
  "anthropic/claude-sonnet-4-20250514": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000003,
    "output_cost_per_token": 0.000015
  },
  "anthropic/claude-sonnet-4-6": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000003,
    "output_cost_per_token": 0.000015
  },
  "anthropic/claude-fable-5": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000001,
    "output_cost_per_token": 0.000005
  },
  "xai/grok-4.6": {
    "litellm_provider": "xai",
    "mode": "chat",
    "input_cost_per_token": 0.000002,
    "output_cost_per_token": 0.000006
  },
  "some-alias-target": {
    "litellm_provider": "openai",
    "mode": "chat",
    "input_cost_per_token": 0.000002,
    "output_cost_per_token": 0.000008,
    "aliases": ["gpt-5-alias"]
  }
}`

func newSyntheticTable(t *testing.T) *Table {
	t.Helper()
	table := NewTable()
	count, err := table.ApplyLiteLLM([]byte(syntheticCostMap))
	if err != nil {
		t.Fatalf("ApplyLiteLLM() error = %v", err)
	}
	if count == 0 {
		t.Fatal("ApplyLiteLLM() indexed no entries")
	}
	return table
}

func TestLookupResolvesBareAndPrefixedNames(t *testing.T) {
	table := newSyntheticTable(t)

	cases := []struct {
		name      string
		model     string
		wantInput float64
	}{
		{"exact prefixed key", "anthropic/claude-sonnet-4-5", 0.000003},
		{"bare name", "claude-sonnet-4-5", 0.000003},
		{"uppercase is normalized", "CLAUDE-SONNET-4-5", 0.000003},
		{"different provider prefix", "vertex_ai/claude-sonnet-4-5", 0.000003},
		{"gemini bare", "gemini-3-pro-preview", 0.00000125},
		{"alias", "gpt-5-alias", 0.000002},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			price, ok := table.Lookup(tc.model, "")
			if !ok {
				t.Fatalf("Lookup(%q) = no match, want a price", tc.model)
			}
			if price.InputPerToken != tc.wantInput {
				t.Errorf("Lookup(%q).InputPerToken = %v, want %v", tc.model, price.InputPerToken, tc.wantInput)
			}
		})
	}
}

func TestLookupPrefersFirstPartyOverAggregator(t *testing.T) {
	table := newSyntheticTable(t)

	price, ok := table.Lookup("gpt-5", "")
	if !ok {
		t.Fatal("Lookup(gpt-5) = no match, want a price")
	}
	// openai (priority 1) must beat openrouter (priority 60).
	if price.InputPerToken != 0.00000125 {
		t.Errorf("InputPerToken = %v, want the first-party rate 0.00000125", price.InputPerToken)
	}
}

func TestLookupFallsBackToEffortSuffixStripping(t *testing.T) {
	table := newSyntheticTable(t)

	for _, model := range []string{
		"claude-opus-4-6-thinking",
		"claude-opus-4-6-high",
		"gemini-3-pro-preview-low",
	} {
		if _, ok := table.Lookup(model, ""); !ok {
			t.Errorf("Lookup(%q) = no match, want the suffix-stripped base model to match", model)
		}
	}
}

func TestLookupMatchesCursorModelIDsByIdentity(t *testing.T) {
	table := newSyntheticTable(t)

	cases := []struct {
		model     string
		wantInput float64
	}{
		{model: "claude-4.5-sonnet-thinking", wantInput: 0.000007},
		{model: "claude-4.6-opus-thinking", wantInput: 0.000005},
		{model: "claude-4.6-sonnet-thinking", wantInput: 0.000003},
		{model: "claude-4-sonnet-thinking", wantInput: 0.000003},
		{model: "claude-5-fable", wantInput: 0.000001},
		{model: "gpt-5", wantInput: 0.00000125},
		{model: "gemini-3-pro-preview", wantInput: 0.00000125},
		{model: "gemini-3-flash", wantInput: 0.0000005},
		{model: "grok-4.7", wantInput: 0.000002},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			price, ok := table.Lookup(tc.model, "cursor")
			if !ok {
				t.Fatalf("Lookup(%q, cursor) = no match, want a price", tc.model)
			}
			if price.InputPerToken != tc.wantInput {
				t.Errorf("Lookup(%q, cursor).InputPerToken = %v, want %v", tc.model, price.InputPerToken, tc.wantInput)
			}
		})
	}
}

func TestLookupLeavesUnmatchedCursorRouterIDsUnpriced(t *testing.T) {
	table := newSyntheticTable(t)

	for _, model := range []string{"auto-smart", "default", "composer-2"} {
		if _, ok := table.Lookup(model, "cursor"); ok {
			t.Errorf("Lookup(%q, cursor) matched a price, want no match", model)
		}
	}
}

func TestCursorIdentityLookupPrefersSpecificVariant(t *testing.T) {
	table := newSyntheticTable(t)

	price, ok := table.Lookup("claude-4.5-sonnet-thinking", "cursor")
	if !ok {
		t.Fatal("Lookup() = no match, want the specific thinking variant")
	}
	if price.InputPerToken != 0.000007 {
		t.Errorf("InputPerToken = %v, want the thinking variant rate 0.000007", price.InputPerToken)
	}
}

func TestCursorIdentityLookupIsProviderScoped(t *testing.T) {
	table := newSyntheticTable(t)

	if _, ok := table.Lookup("claude-4.5-sonnet-thinking", "openai"); ok {
		t.Error("Lookup() applied Cursor ID normalization to a non-Cursor provider")
	}
}

func TestCursorIdentityLookupRejectsAmbiguousPrices(t *testing.T) {
	const ambiguous = `{
  "claude-sonnet-4-20250514": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000003,
    "output_cost_per_token": 0.000015
  },
  "claude-4-sonnet-20250515": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000004,
    "output_cost_per_token": 0.000020
  }
}`
	table := NewTable()
	if _, err := table.ApplyLiteLLM([]byte(ambiguous)); err != nil {
		t.Fatalf("ApplyLiteLLM() error = %v", err)
	}

	if _, ok := table.Lookup("claude-4-sonnet-thinking", "cursor"); ok {
		t.Error("Lookup() matched an ambiguous Cursor identity, want no match")
	}
}

func TestCursorIdentityLookupHonoursOverrides(t *testing.T) {
	table := newSyntheticTable(t)
	table.SetOverrides(map[string]Price{
		"claude-sonnet-4-5": {
			InputPerToken:  ParsePerMillion(9),
			OutputPerToken: ParsePerMillion(45),
		},
	})

	price, ok := table.Lookup("claude-4.5-sonnet-thinking", "cursor")
	if !ok {
		t.Fatal("Lookup() = no match, want the normalized override")
	}
	if price.Source != "override" || price.InputPerToken != ParsePerMillion(9) {
		t.Errorf("Lookup() = %+v, want normalized override pricing", price)
	}
}

func TestLookupIgnoresNonBillableModes(t *testing.T) {
	table := newSyntheticTable(t)

	if _, ok := table.Lookup("text-embedding-3-large", ""); ok {
		t.Error("embedding-mode entries must not be indexed for token cost estimation")
	}
}

func TestLookupMissReturnsFalse(t *testing.T) {
	table := newSyntheticTable(t)

	if _, ok := table.Lookup("model-that-does-not-exist", ""); ok {
		t.Error("Lookup() of an unknown model = match, want no match")
	}
	if _, ok := table.Lookup("", ""); ok {
		t.Error("Lookup(\"\") = match, want no match")
	}
}

func TestOverridesWinOverSyncedPrices(t *testing.T) {
	table := newSyntheticTable(t)

	table.SetOverrides(map[string]Price{
		"claude-sonnet-4-5": {
			InputPerToken:  ParsePerMillion(9),
			OutputPerToken: ParsePerMillion(45),
		},
	})

	price, ok := table.Lookup("anthropic/claude-sonnet-4-5", "")
	if !ok {
		t.Fatal("Lookup() = no match, want the override")
	}
	if price.InputPerToken != ParsePerMillion(9) {
		t.Errorf("InputPerToken = %v, want override %v", price.InputPerToken, ParsePerMillion(9))
	}
	if price.Source != "override" {
		t.Errorf("Source = %q, want \"override\"", price.Source)
	}
}

func TestCostComputation(t *testing.T) {
	table := newSyntheticTable(t)

	price, ok := table.Lookup("claude-sonnet-4-5", "")
	if !ok {
		t.Fatal("Lookup() = no match")
	}

	tokens := Tokens{
		UncachedInput: 1000,
		CacheRead:     2000,
		CacheWrite:    500,
		Output:        300,
		Reasoning:     200,
	}

	want := 1000*0.000003 + 2000*0.0000003 + 500*0.00000375 + 300*0.000015 + 200*0.000015
	got := price.Cost(tokens)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost() = %v, want %v", got, want)
	}
}

func TestReasoningUsesDedicatedRateWhenPresent(t *testing.T) {
	table := newSyntheticTable(t)

	price, ok := table.Lookup("claude-opus-4-6", "")
	if !ok {
		t.Fatal("Lookup() = no match")
	}

	reasoningOnly := Tokens{Reasoning: 1000}
	want := 1000 * 0.00004
	if got := price.Cost(reasoningOnly); math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost(reasoning) = %v, want the dedicated reasoning rate %v", got, want)
	}
}

func TestReasoningFallsBackToOutputRate(t *testing.T) {
	table := newSyntheticTable(t)

	price, ok := table.Lookup("claude-sonnet-4-5", "")
	if !ok {
		t.Fatal("Lookup() = no match")
	}
	if price.ReasoningPerToken != 0 {
		t.Fatalf("precondition failed: ReasoningPerToken = %v, want 0", price.ReasoningPerToken)
	}

	reasoningOnly := Tokens{Reasoning: 1000}
	want := 1000 * price.OutputPerToken
	if got := price.Cost(reasoningOnly); math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost(reasoning) = %v, want the output rate fallback %v", got, want)
	}
}

func TestLookupCandidatesOrder(t *testing.T) {
	got := lookupCandidates("gemini/gemini-3.6-flash-high")
	want := []string{"gemini/gemini-3.6-flash-high", "gemini-3.6-flash-high", "gemini/gemini-3.6-flash", "gemini-3.6-flash"}

	for _, candidate := range want {
		found := false
		for _, value := range got {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("lookupCandidates() = %v, missing %q", got, candidate)
		}
	}
}

func TestIndexKeysStripsVendorPrefix(t *testing.T) {
	got := indexKeys("bedrock/us.anthropic.claude-sonnet-4-5")
	want := map[string]bool{"bedrock/us.anthropic.claude-sonnet-4-5": true, "claude-sonnet-4-5": true}

	for _, value := range got {
		if !want[value] {
			t.Errorf("indexKeys() produced unexpected key %q", value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("indexKeys() = %v, want keys %v", got, want)
	}
}

func TestOverrideToPriceUsesPerMillionInput(t *testing.T) {
	price := Override{Model: "x", Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}.ToPrice()

	if price.InputPerToken != 0.000003 {
		t.Errorf("InputPerToken = %v, want 0.000003", price.InputPerToken)
	}
	if price.OutputPerToken != 0.000015 {
		t.Errorf("OutputPerToken = %v, want 0.000015", price.OutputPerToken)
	}
	if price.CacheReadPerToken != 0.0000003 {
		t.Errorf("CacheReadPerToken = %v, want 0.0000003", price.CacheReadPerToken)
	}
	if price.CacheWritePerToken != 0.00000375 {
		t.Errorf("CacheWritePerToken = %v, want 0.00000375", price.CacheWritePerToken)
	}
}

func TestApplyLiteLLMRejectsInvalidJSON(t *testing.T) {
	table := NewTable()
	if _, err := table.ApplyLiteLLM([]byte("{not json")); err == nil {
		t.Error("ApplyLiteLLM() of invalid JSON = nil error, want an error")
	}
}

func TestPriceKnownAndTokensTotal(t *testing.T) {
	if (Price{}).Known() {
		t.Error("zero Price.Known() = true, want false")
	}
	if !(Price{InputPerToken: 1}).Known() {
		t.Error("Price{InputPerToken: 1}.Known() = false, want true")
	}

	tokens := Tokens{UncachedInput: 1, CacheRead: 2, CacheWrite: 3, Output: 4, Reasoning: 5}
	if got := tokens.Total(); got != 15 {
		t.Errorf("Tokens.Total() = %d, want 15", got)
	}
}

func TestEntriesReportsShadowedKeyOnceAndFlagsOverrides(t *testing.T) {
	table := newSyntheticTable(t)

	table.SetOverrides(map[string]Price{
		"claude-sonnet-4-5": {
			InputPerToken:  ParsePerMillion(9),
			OutputPerToken: ParsePerMillion(45),
		},
	})

	entries := table.Entries()
	if len(entries) == 0 {
		t.Fatal("Entries() returned nothing")
	}

	counts := make(map[string]int, len(entries))
	for _, entry := range entries {
		counts[entry.Model]++
	}
	// The shadowed base key must be reported once, not twice.
	if counts["claude-sonnet-4-5"] != 1 {
		t.Errorf("Entries() reported claude-sonnet-4-5 %d times, want 1", counts["claude-sonnet-4-5"])
	}

	var overridden *Entry
	for i := range entries {
		if entries[i].Model == "claude-sonnet-4-5" {
			overridden = &entries[i]
		}
	}
	if overridden == nil {
		t.Fatal("Entries() is missing the overridden model")
	}
	if !overridden.Override {
		t.Error("Override = false, want true for an operator override")
	}
	if overridden.Source != "override" {
		t.Errorf("Source = %q, want %q", overridden.Source, "override")
	}
	if overridden.InputPerToken != ParsePerMillion(9) {
		t.Errorf("InputPerToken = %v, want %v", overridden.InputPerToken, ParsePerMillion(9))
	}
	if table.OverrideCount() != 1 {
		t.Errorf("OverrideCount() = %d, want 1", table.OverrideCount())
	}
}

func TestEntriesIsSortedCompleteAndUsable(t *testing.T) {
	table := newSyntheticTable(t)

	entries := table.Entries()
	if len(entries) != table.CatalogSize() {
		t.Errorf("Entries() len = %d, want CatalogSize() = %d", len(entries), table.CatalogSize())
	}
	if len(entries) != table.EntryCount() {
		t.Errorf("Entries() len = %d, want EntryCount() = %d", len(entries), table.EntryCount())
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Model >= entries[i].Model {
			t.Fatalf("Entries() not sorted: %q before %q", entries[i-1].Model, entries[i].Model)
		}
	}
	// Every catalog row must carry a usable rate; a dead row would be noise.
	for _, entry := range entries {
		if !entry.Known() {
			t.Errorf("entry %q has no usable rate", entry.Model)
		}
	}
}

func TestEntriesWithoutOverridesFlagsNothing(t *testing.T) {
	table := newSyntheticTable(t)

	if table.OverrideCount() != 0 {
		t.Fatalf("OverrideCount() = %d, want 0", table.OverrideCount())
	}
	for _, entry := range table.Entries() {
		if entry.Override {
			t.Errorf("entry %q: Override = true, want false", entry.Model)
		}
	}
}

func TestEntriesHandlesNilTable(t *testing.T) {
	var table *Table

	entries := table.Entries()
	if entries == nil {
		t.Error("Entries() on a nil table returned nil, want an empty slice")
	}
	if len(entries) != 0 {
		t.Errorf("Entries() on a nil table = %d entries, want 0", len(entries))
	}
	if table.OverrideCount() != 0 {
		t.Errorf("OverrideCount() on a nil table = %d, want 0", table.OverrideCount())
	}
	if table.CatalogSize() != 0 {
		t.Errorf("CatalogSize() on a nil table = %d, want 0", table.CatalogSize())
	}
}

// The catalog header shows EntryCount next to OverrideCount, and the table below
// it shows Entries(). An override for a model the upstream price list omits is the
// main reason to configure one, so the two numbers must agree in that case too;
// counting only the base table would render "0 models, 1 override".
func TestCatalogSizeCountsOverridesForModelsAbsentUpstream(t *testing.T) {
	table := newSyntheticTable(t)
	baseCount := table.EntryCount()
	if baseCount == 0 {
		t.Fatal("fixture has no base entries")
	}

	table.SetOverrides(map[string]Price{
		// Shadows a synced key: replaces a row rather than adding one.
		"claude-sonnet-4-5": {
			InputPerToken:  ParsePerMillion(9),
			OutputPerToken: ParsePerMillion(45),
		},
		// Absent upstream: must add a row.
		"in-house-model-not-in-litellm": {
			InputPerToken:  ParsePerMillion(1),
			OutputPerToken: ParsePerMillion(2),
		},
	})

	entries := table.Entries()
	if got := table.CatalogSize(); got != len(entries) {
		t.Errorf("CatalogSize() = %d, want %d (len(Entries()))", got, len(entries))
	}
	if got, want := table.CatalogSize(), baseCount+1; got != want {
		t.Errorf("CatalogSize() = %d, want %d (base + the non-shadowing override)", got, want)
	}
	// The synced-base count is intentionally unchanged by either override.
	if got := table.EntryCount(); got != baseCount {
		t.Errorf("EntryCount() = %d, want %d (overrides must not change it)", got, baseCount)
	}
	if got := table.OverrideCount(); got != 2 {
		t.Errorf("OverrideCount() = %d, want 2", got)
	}
}
