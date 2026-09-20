package config

import "strings"

// ModelPriceOverride is a per-model price override in USD per one million
// tokens, matching how vendors publish prices.
//
// This is the only cost-estimation setting the operator owns: everything else
// (price table source, refresh cadence, cache location, retention) is internal
// policy with a sensible default. Overrides exist because no upstream table
// covers every model or every negotiated rate.
type ModelPriceOverride struct {
	// Model is the model id to override. Provider prefixes are ignored when
	// matching, so "anthropic/claude-sonnet-4-5" also matches
	// "claude-sonnet-4-5".
	Model string `yaml:"model" json:"model"`
	// Input is the uncached input price per million tokens.
	Input float64 `yaml:"input,omitempty" json:"input,omitempty"`
	// Output is the output price per million tokens.
	Output float64 `yaml:"output,omitempty" json:"output,omitempty"`
	// CacheRead is the cache read price per million tokens.
	CacheRead float64 `yaml:"cache-read,omitempty" json:"cache-read,omitempty"`
	// CacheWrite is the cache write price per million tokens.
	CacheWrite float64 `yaml:"cache-write,omitempty" json:"cache-write,omitempty"`
	// Reasoning is the reasoning output price per million tokens.
	Reasoning float64 `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
}

// NormalizeModelPriceOverrides trims override entries and drops the ones that
// cannot match a model. It is called from both the config loader and the
// runtime wiring so programmatically built configs behave like parsed ones.
func (c *Config) NormalizeModelPriceOverrides() {
	if c == nil {
		return
	}
	cleaned := make([]ModelPriceOverride, 0, len(c.ModelPriceOverrides))
	for _, override := range c.ModelPriceOverrides {
		override.Model = strings.TrimSpace(override.Model)
		if override.Model == "" {
			continue
		}
		cleaned = append(cleaned, override)
	}
	c.ModelPriceOverrides = cleaned
}
