package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelprice"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

// The consumption statistics subsystem exposes exactly one config key, the
// operator's price overrides. Everything else is internal policy, so these
// tests pin the wiring and the path derivation rather than a config surface.

func TestResolveUsageStatsStateDir(t *testing.T) {
	authDir := filepath.Join("/srv", "cli-proxy-api")
	stateDir := filepath.Join(authDir, "state")

	t.Run("nests under the configured auth directory", func(t *testing.T) {
		if got := resolveUsageStatsStateDir(authDir); got != stateDir {
			t.Fatalf("got %q, want %q", got, stateDir)
		}
	})

	t.Run("expands a leading tilde", func(t *testing.T) {
		got := resolveUsageStatsStateDir("~/.cli-proxy-api")
		if !filepath.IsAbs(got) {
			t.Fatalf("got %q, want an expanded absolute path", got)
		}
	})

	t.Run("falls back to the default auth directory when empty", func(t *testing.T) {
		// An empty value must not disable persistence: the default auth
		// directory is the documented artifact location.
		if got := resolveUsageStatsStateDir(""); got == "" {
			t.Fatal("empty auth dir unexpectedly disabled persistence")
		}
	})

	t.Run("normalizes redundant separators", func(t *testing.T) {
		got := resolveUsageStatsStateDir("/srv/cli-proxy-api/../cli-proxy-api")
		if got != stateDir {
			t.Fatalf("got %q, want %q", got, stateDir)
		}
	})
}

func TestMigrateUsageStatsState(t *testing.T) {
	t.Run("moves legacy state files into the state directory", func(t *testing.T) {
		authDir := t.TempDir()
		for _, name := range []string{usagestats.DefaultPersistFileName, modelprice.DefaultCacheFileName} {
			if err := os.WriteFile(filepath.Join(authDir, name), []byte("{}"), 0o600); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}

		migrateUsageStatsState(resolveUsageStatsStateDir(authDir))

		stateDir := filepath.Join(authDir, "state")
		for _, name := range []string{usagestats.DefaultPersistFileName, modelprice.DefaultCacheFileName} {
			if _, err := os.Stat(filepath.Join(stateDir, name)); err != nil {
				t.Fatalf("expected %s inside state dir: %v", name, err)
			}
			if _, err := os.Stat(filepath.Join(authDir, name)); !os.IsNotExist(err) {
				t.Fatalf("expected %s removed from auth dir, stat error = %v", name, err)
			}
		}
	})

	t.Run("is a no-op without legacy files", func(t *testing.T) {
		authDir := t.TempDir()
		migrateUsageStatsState(resolveUsageStatsStateDir(authDir))
		entries, err := os.ReadDir(filepath.Join(authDir, "state"))
		if err != nil {
			t.Fatalf("read state dir: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("expected empty state dir, got %d entries", len(entries))
		}
	})

	t.Run("is a no-op with an empty state dir", func(t *testing.T) {
		migrateUsageStatsState("")
	})
}

func TestPriceOverridesFor(t *testing.T) {
	if got := priceOverridesFor(nil); got != nil {
		t.Fatalf("priceOverridesFor(nil) = %v, want nil", got)
	}
	if got := priceOverridesFor(&config.Config{}); got != nil {
		t.Fatalf("priceOverridesFor(empty) = %v, want nil", got)
	}

	cfg := &config.Config{
		ModelPriceOverrides: []config.ModelPriceOverride{
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

	got := priceOverridesFor(cfg)
	if len(got) != 1 {
		t.Fatalf("got %d overrides, want 1", len(got))
	}
	want := cfg.ModelPriceOverrides[0]
	if got[0].Model != want.Model || got[0].Input != want.Input || got[0].Output != want.Output {
		t.Fatalf("got %+v, want it to mirror %+v", got[0], want)
	}
	if got[0].CacheRead != want.CacheRead || got[0].CacheWrite != want.CacheWrite {
		t.Fatalf("got %+v, want cache rates to mirror %+v", got[0], want)
	}
	if got[0].Reasoning != want.Reasoning {
		t.Fatalf("got reasoning %v, want %v", got[0].Reasoning, want.Reasoning)
	}
}

func TestApplyUsageStatsConfigIgnoresNilConfig(t *testing.T) {
	// Called defensively during start-up and reload paths; must not panic and
	// must not start any background work.
	applyUsageStatsConfig(nil)
}
