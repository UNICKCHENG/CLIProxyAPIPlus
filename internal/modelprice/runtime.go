package modelprice

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// DefaultCacheFileName is the cached cost map inside the state directory.
const DefaultCacheFileName = "model-prices.json"

// syncClientTimeout caps a single cost-map download.
const syncClientTimeout = 60 * time.Second

// Options configures the process-wide price table runtime.
type Options struct {
	// StateDir caches the downloaded cost map so start-up works offline.
	// Empty disables caching; the table then holds only the overrides plus
	// whatever the first sync returns.
	StateDir string
	// Overrides are operator-provided per-model prices. They always win over
	// synced prices.
	Overrides []Override
}

var (
	runtimeMu        sync.Mutex
	runtimeCancel    context.CancelFunc
	runtimeSignature string
	runtimeURL       string
	runtimeCachePath string
	// syncMu serializes downloads so the start-up attempt and a manual sync
	// cannot install or cache conflicting payloads.
	syncMu sync.Mutex
)

// Configure applies overrides, loads the cached cost map, and — once per state
// directory — starts a single background download attempt.
//
// There is deliberately no periodic refresh. The catalog is fetched when the
// process starts, and thereafter only when an operator asks for it through
// SyncNow. A failed start-up download is not retried: the table falls back to
// the cache plus the overrides, which keeps offline and air-gapped installs
// working and stops an unreachable upstream from being hammered.
func Configure(opts Options) {
	ApplyOverrides(opts.Overrides)

	path := cachePathFor(opts.StateDir)

	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	if path == runtimeSignature && runtimeCancel != nil {
		return
	}
	if runtimeCancel != nil {
		runtimeCancel()
		runtimeCancel = nil
	}
	runtimeSignature = path
	runtimeCachePath = path
	// Only install the default when no source has been chosen yet, so a caller
	// can point the runtime at a mirror instead.
	if runtimeURL == "" {
		runtimeURL = DefaultLiteLLMURL
	}

	// EntryCount (base only, ignoring overrides) answers "has a sync ever
	// landed?" — CatalogSize would report overrides and skip loading the cache.
	if path != "" && defaultTable.EntryCount() == 0 {
		if count, err := LoadCostMapFile(path); err != nil {
			if !os.IsNotExist(err) {
				log.WithError(err).Warn("model prices: failed to load cached cost map")
			}
		} else {
			log.Infof("model prices: loaded %d entries from cache", count)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	runtimeCancel = cancel
	go runStartupSync(ctx)
}

// cachePathFor returns the on-disk cache location inside the state directory.
func cachePathFor(stateDir string) string {
	dir := strings.TrimSpace(stateDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, DefaultCacheFileName)
}

// Shutdown stops an in-flight start-up download.
func Shutdown() {
	runtimeMu.Lock()
	cancel := runtimeCancel
	runtimeCancel = nil
	runtimeSignature = ""
	runtimeCachePath = ""
	runtimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runStartupSync makes the single download attempt allowed per process start.
func runStartupSync(ctx context.Context) {
	if _, err := syncOnce(ctx); err != nil {
		// One attempt per start: an outage, a moved upstream or a bad payload is
		// not retried. The catalog keeps serving the cache plus the overrides.
		log.WithError(err).Warn("model prices: start-up sync failed, serving cached prices")
	}
}

// SyncNow downloads the cost map immediately and reports the indexed entry
// count. It backs the on-demand refresh and is the only way to pick up upstream
// price changes without restarting the process.
func SyncNow(ctx context.Context) (int, error) {
	count, err := syncOnce(ctx)
	if err != nil {
		log.WithError(err).Warn("model prices: manual sync failed")
		return 0, err
	}
	return count, nil
}

// syncOnce fetches, installs and caches the cost map. Callers are serialized so
// a manual sync cannot interleave with the start-up attempt.
func syncOnce(ctx context.Context) (int, error) {
	syncMu.Lock()
	defer syncMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	runtimeMu.Lock()
	url := runtimeURL
	cachePath := runtimeCachePath
	runtimeMu.Unlock()
	if url == "" {
		url = DefaultLiteLLMURL
	}

	data, err := FetchCostMap(ctx, &http.Client{Timeout: syncClientTimeout}, url)
	if err != nil {
		return 0, err
	}
	count, err := defaultTable.ApplyLiteLLM(data)
	if err != nil {
		return 0, fmt.Errorf("parse cost map: %w", err)
	}
	if cachePath != "" {
		if errSave := SaveCostMapFile(cachePath, data); errSave != nil {
			// The in-memory table is already updated, so a cache write failure is
			// not worth failing the sync over.
			log.WithError(errSave).Warn("model prices: failed to cache cost map")
		}
	}
	log.Infof("model prices: synced %d entries", count)
	return count, nil
}

// Status describes the current price table state.
type Status struct {
	EntryCount int `json:"entry_count"`
	// OverrideCount is the number of operator-provided override keys.
	OverrideCount int       `json:"override_count"`
	Source        string    `json:"source"`
	UpdatedAt     time.Time `json:"updated_at"`
	// SyncEnabled reported whether the table refreshed on a timer. That is now
	// false by design, and the field is kept so older clients reading the
	// payload still find the key they expect.
	SyncEnabled bool   `json:"sync_enabled"`
	SyncURL     string `json:"sync_url"`
}

// CurrentStatus returns the price table status.
func CurrentStatus() Status {
	runtimeMu.Lock()
	url := runtimeURL
	runtimeMu.Unlock()
	return Status{
		// CatalogSize, not EntryCount: this number labels the catalog table, so it
		// must include overrides for models the upstream price list omits.
		EntryCount:    defaultTable.CatalogSize(),
		OverrideCount: defaultTable.OverrideCount(),
		Source:        defaultTable.Source(),
		UpdatedAt:     defaultTable.UpdatedAt(),
		SyncEnabled:   false,
		SyncURL:       url,
	}
}
