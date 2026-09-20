package config

import "testing"

func TestNormalizeModelPriceOverridesTrimsAndDropsBlankEntries(t *testing.T) {
	cfg := &Config{
		ModelPriceOverrides: []ModelPriceOverride{
			{Model: "  claude-sonnet-4-5  ", Input: 3, Output: 15},
			{Model: "   "},
			{Model: ""},
			{Model: "\tgemini-3-pro-preview\n", Input: 1.25, Output: 10},
		},
	}

	cfg.NormalizeModelPriceOverrides()

	if len(cfg.ModelPriceOverrides) != 2 {
		t.Fatalf("got %d overrides, want blank entries dropped", len(cfg.ModelPriceOverrides))
	}
	if got := cfg.ModelPriceOverrides[0].Model; got != "claude-sonnet-4-5" {
		t.Errorf("model = %q, want it trimmed", got)
	}
	if got := cfg.ModelPriceOverrides[1].Model; got != "gemini-3-pro-preview" {
		t.Errorf("model = %q, want it trimmed", got)
	}
}

func TestNormalizeModelPriceOverridesKeepsRates(t *testing.T) {
	cfg := &Config{
		ModelPriceOverrides: []ModelPriceOverride{
			{
				Model:      "claude-sonnet-4-5",
				Input:      3,
				Output:     15,
				CacheRead:  0.3,
				CacheWrite: 3.75,
				Reasoning:  15,
			},
		},
	}

	cfg.NormalizeModelPriceOverrides()

	if len(cfg.ModelPriceOverrides) != 1 {
		t.Fatalf("got %d overrides, want 1", len(cfg.ModelPriceOverrides))
	}
	got := cfg.ModelPriceOverrides[0]
	if got.Input != 3 || got.Output != 15 || got.CacheRead != 0.3 || got.CacheWrite != 3.75 || got.Reasoning != 15 {
		t.Fatalf("normalization altered rates: %+v", got)
	}
}

func TestNormalizeModelPriceOverridesHandlesNilAndEmpty(t *testing.T) {
	var nilCfg *Config
	nilCfg.NormalizeModelPriceOverrides() // must not panic

	empty := &Config{}
	empty.NormalizeModelPriceOverrides()
	if len(empty.ModelPriceOverrides) != 0 {
		t.Fatalf("got %d overrides, want 0", len(empty.ModelPriceOverrides))
	}
}
