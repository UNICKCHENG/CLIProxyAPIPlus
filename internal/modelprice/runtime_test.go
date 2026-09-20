package modelprice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// isolateRuntime swaps the process-wide table and clears the installed source
// URL so a case cannot leak into the next one.
func isolateRuntime(t *testing.T) {
	t.Helper()

	previousTable := defaultTable
	defaultTable = NewTable()

	runtimeMu.Lock()
	previousURL := runtimeURL
	previousPath := runtimeCachePath
	runtimeURL = ""
	runtimeCachePath = ""
	runtimeMu.Unlock()

	t.Cleanup(func() {
		Shutdown()
		defaultTable = previousTable
		runtimeMu.Lock()
		runtimeURL = previousURL
		runtimeCachePath = previousPath
		runtimeMu.Unlock()
	})
}

// newCostMapServer serves the synthetic cost map and counts how many times it
// was asked for it.
func newCostMapServer(t *testing.T, body string, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server, &requests
}

// Configure must keep a source URL that was already chosen instead of
// overwriting it with the public one; the runtime is otherwise pinned to the
// published LiteLLM document.
func TestConfigureKeepsPreinstalledSourceURL(t *testing.T) {
	isolateRuntime(t)

	server, _ := newCostMapServer(t, syntheticCostMap, http.StatusOK)
	runtimeMu.Lock()
	runtimeURL = server.URL
	runtimeMu.Unlock()

	Configure(Options{StateDir: t.TempDir()})

	runtimeMu.Lock()
	got := runtimeURL
	runtimeMu.Unlock()
	if got != server.URL {
		t.Errorf("runtimeURL = %q, want the preinstalled %q", got, server.URL)
	}
}

// The catalog is fetched once per process start and cached to disk. A second
// Configure call with the same state directory must not queue another download,
// which is what "no periodic refresh" means in practice.
func TestConfigureDownloadsOncePerStateDirectory(t *testing.T) {
	isolateRuntime(t)

	stateDir := t.TempDir()
	server, requests := newCostMapServer(t, syntheticCostMap, http.StatusOK)
	runtimeMu.Lock()
	runtimeURL = server.URL
	runtimeMu.Unlock()

	Configure(Options{StateDir: stateDir})

	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("start-up requests = %d, want 1", got)
	}

	// The cache must land where the next start-up will look for it.
	cachePath := filepath.Join(stateDir, DefaultCacheFileName)
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("cache file %s missing after start-up sync: %v", cachePath, err)
	}

	Configure(Options{StateDir: stateDir})
	time.Sleep(200 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Errorf("requests after a repeated Configure = %d, want 1 (no refresh loop)", got)
	}
}

// A start-up download that fails must leave the cached prices in place: the
// table is only replaced after a payload has been fetched and parsed.
func TestFailedSyncKeepsCachedCatalog(t *testing.T) {
	isolateRuntime(t)

	stateDir := t.TempDir()
	if err := SaveCostMapFile(filepath.Join(stateDir, DefaultCacheFileName), []byte(syntheticCostMap)); err != nil {
		t.Fatalf("SaveCostMapFile() error = %v", err)
	}

	server, _ := newCostMapServer(t, "", http.StatusNotFound)
	runtimeMu.Lock()
	runtimeURL = server.URL
	runtimeMu.Unlock()

	Configure(Options{StateDir: stateDir})
	time.Sleep(200 * time.Millisecond)

	if count := defaultTable.EntryCount(); count == 0 {
		t.Fatal("cached prices were dropped after a failed start-up sync")
	}
	if _, ok := defaultTable.Lookup("claude-sonnet-4-5", ""); !ok {
		t.Error("cached price for claude-sonnet-4-5 is no longer resolvable")
	}
}

// A manual sync installs the table and refreshes the cache, so the next start
// can serve the same prices without a network round trip.
func TestSyncNowInstallsAndCachesCostMap(t *testing.T) {
	isolateRuntime(t)

	cachePath := filepath.Join(t.TempDir(), DefaultCacheFileName)
	server, requests := newCostMapServer(t, syntheticCostMap, http.StatusOK)
	runtimeMu.Lock()
	runtimeURL = server.URL
	runtimeCachePath = cachePath
	runtimeMu.Unlock()

	count, err := SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow() error = %v", err)
	}
	if count == 0 {
		t.Fatal("SyncNow() indexed no entries")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}

	price, ok := defaultTable.Lookup("claude-sonnet-4-5", "")
	if !ok {
		t.Fatal("claude-sonnet-4-5 is not resolvable after a sync")
	}
	if got, want := price.InputPerToken, ParsePerMillion(3); got != want {
		t.Errorf("InputPerToken = %v, want %v", got, want)
	}

	data, errRead := os.ReadFile(cachePath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", cachePath, errRead)
	}
	if string(data) != syntheticCostMap {
		t.Errorf("cached cost map = %q, want the downloaded payload", string(data))
	}
}

func TestSyncNowReportsUpstreamStatusFailure(t *testing.T) {
	isolateRuntime(t)

	server, _ := newCostMapServer(t, "", http.StatusNotFound)
	runtimeMu.Lock()
	runtimeURL = server.URL
	runtimeMu.Unlock()

	if _, err := SyncNow(context.Background()); err == nil {
		t.Fatal("SyncNow() error = nil, want a failure for a 404 upstream")
	}
}

func TestSyncNowReportsUnparsablePayload(t *testing.T) {
	isolateRuntime(t)

	server, _ := newCostMapServer(t, "{ not json", http.StatusOK)
	runtimeMu.Lock()
	runtimeURL = server.URL
	runtimeMu.Unlock()

	if _, err := SyncNow(context.Background()); err == nil {
		t.Fatal("SyncNow() error = nil, want a failure for an unparsable cost map")
	}
}

func TestSyncNowHonoursContext(t *testing.T) {
	isolateRuntime(t)

	server, _ := newCostMapServer(t, syntheticCostMap, http.StatusOK)
	runtimeMu.Lock()
	runtimeURL = server.URL
	runtimeMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := SyncNow(ctx); err == nil {
		t.Fatal("SyncNow() error = nil, want a failure for a cancelled context")
	}
}

// A cache file is written next to the configured state directory, so a repeat
// start-up can serve prices without a network round trip.
func TestLoadCostMapFileRestoresCatalog(t *testing.T) {
	isolateRuntime(t)

	path := filepath.Join(t.TempDir(), DefaultCacheFileName)
	if err := SaveCostMapFile(path, []byte(syntheticCostMap)); err != nil {
		t.Fatalf("SaveCostMapFile() error = %v", err)
	}
	count, err := LoadCostMapFile(path)
	if err != nil {
		t.Fatalf("LoadCostMapFile() error = %v", err)
	}
	if count != defaultTable.EntryCount() {
		t.Errorf("LoadCostMapFile() = %d, want %d (EntryCount())", count, defaultTable.EntryCount())
	}
}

func TestStatusReportsSyncAsDisabled(t *testing.T) {
	isolateRuntime(t)

	status := CurrentStatus()
	if status.SyncEnabled {
		t.Error("SyncEnabled = true, want false: the catalog no longer refreshes on a timer")
	}
}
