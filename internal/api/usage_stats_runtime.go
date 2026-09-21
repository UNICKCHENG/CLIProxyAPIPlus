package api

import (
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"

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
	migrateUsageStatsState(stateDir)

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
// artifacts: a "state" subdirectory of the auth directory, kept outside the
// auth directory root so directory scanners that enumerate *.json never see
// the internal state files. An unresolvable auth directory yields "", which
// disables persistence rather than writing somewhere unexpected.
func resolveUsageStatsStateDir(authDir string) string {
	resolved, err := util.ResolveAuthDir(authDir)
	if err != nil {
		return ""
	}
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return ""
	}
	return filepath.Join(resolved, "state")
}

// migrateUsageStatsState moves the legacy state files from the auth directory
// root into the state subdirectory so pre-existing installations keep their
// usage history and cached prices. Missing or already-migrated files are
// skipped; an unwritable directory only logs, since both packages treat an
// unreadable state file as a fresh start.
func migrateUsageStatsState(stateDir string) {
	if stateDir == "" {
		return
	}
	authDir := filepath.Dir(stateDir)
	if errMkdir := os.MkdirAll(stateDir, 0o755); errMkdir != nil {
		return
	}
	for _, name := range []string{usagestats.DefaultPersistFileName, modelprice.DefaultCacheFileName} {
		legacy := filepath.Join(authDir, name)
		if _, errStat := os.Stat(legacy); errStat != nil {
			continue
		}
		if errRename := os.Rename(legacy, filepath.Join(stateDir, name)); errRename != nil {
			log.WithError(errRename).Warnf("usage stats: failed to migrate legacy state file %s", legacy)
		}
	}
}
