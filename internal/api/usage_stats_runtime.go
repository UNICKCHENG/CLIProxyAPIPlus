package api

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelprice"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// applyUsageStatsConfig wires the consumption statistics aggregator and the
// model price table to the current configuration. It is called on start-up and
// on every configuration reload; repeated calls with the same settings are
// cheap no-ops.
//
// The only setting read from the config is the operator's price overrides.
// Price source, refresh cadence, cache location, retention and currency are
// internal policy owned by the packages below, so the public config surface
// stays limited to the data only the operator can supply.
func applyUsageStatsConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.NormalizeModelPriceOverrides()

	stateDir := resolveUsageStatsStateDir(cfg.AuthDir)

	usagestats.Configure(usagestats.Options{StateDir: stateDir})

	modelprice.Configure(modelprice.Options{
		StateDir:  stateDir,
		Overrides: priceOverridesFor(cfg),
	})
}

// priceOverridesFor converts operator-configured overrides into the price table
// form. It is pure so the conversion can be verified without starting the
// background sync or touching the filesystem.
func priceOverridesFor(cfg *config.Config) []modelprice.Override {
	if cfg == nil || len(cfg.ModelPriceOverrides) == 0 {
		return nil
	}
	overrides := make([]modelprice.Override, 0, len(cfg.ModelPriceOverrides))
	for _, override := range cfg.ModelPriceOverrides {
		overrides = append(overrides, modelprice.Override{
			Model:      override.Model,
			Input:      override.Input,
			Output:     override.Output,
			CacheRead:  override.CacheRead,
			CacheWrite: override.CacheWrite,
			Reasoning:  override.Reasoning,
		})
	}
	return overrides
}

// shutdownUsageStatsRuntime flushes pending state and stops background loops.
func shutdownUsageStatsRuntime() {
	usagestats.Shutdown()
	modelprice.Shutdown()
}

// resolveUsageStatsStateDir resolves the directory holding the persisted usage
// artifacts. An unresolvable auth directory yields "", which disables
// persistence rather than writing somewhere unexpected.
func resolveUsageStatsStateDir(authDir string) string {
	resolved, err := util.ResolveAuthDir(authDir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(resolved)
}
