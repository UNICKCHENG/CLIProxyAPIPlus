package usagestats

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// persistenceVersion is the on-disk schema version for the state file.
	persistenceVersion = 1
	// DefaultPersistInterval is how often dirty state is flushed to disk.
	DefaultPersistInterval = 2 * time.Minute
	// DefaultPersistFileName is the state file inside the state directory.
	DefaultPersistFileName = "usage-stats.json"
)

var (
	defaultStore = NewStore(DefaultRetentionDays)

	runtimeMu     sync.Mutex
	runtimeCancel context.CancelFunc
	runtimePath   string
	runtimeDirty  atomic.Bool
)

// DefaultStore returns the process-wide statistics store.
func DefaultStore() *Store { return defaultStore }

// MarkDirty flags the store as having unsaved changes.
func MarkDirty() { runtimeDirty.Store(true) }

// Options configures the aggregation runtime.
type Options struct {
	// StateDir persists aggregation state across restarts. Empty keeps the
	// counters in memory only.
	StateDir string
}

// Configure loads persisted state on first use and (re)starts the background
// flusher. Calling it repeatedly with the same effective settings is a no-op.
//
// Aggregation is always on: the store is bounded by DefaultRetentionDays and a
// hard bucket cap, so it needs no operator switch.
func Configure(opts Options) {
	path := persistPathFor(opts.StateDir)

	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	if path == runtimePath && runtimeCancel != nil {
		return
	}

	if runtimeCancel != nil {
		runtimeCancel()
		runtimeCancel = nil
	}
	runtimePath = path

	if path == "" {
		return
	}

	if err := defaultStore.LoadFromFile(path); err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("usage stats: failed to load persisted state")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	runtimeCancel = cancel
	go runFlusher(ctx, path, DefaultPersistInterval)
}

// persistPathFor returns the on-disk state location inside the state directory.
func persistPathFor(stateDir string) string {
	dir := strings.TrimSpace(stateDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, DefaultPersistFileName)
}

// Shutdown stops the flusher and flushes pending state.
func Shutdown() {
	runtimeMu.Lock()
	cancel := runtimeCancel
	path := runtimePath
	runtimeCancel = nil
	runtimeMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if path == "" {
		return
	}
	if err := defaultStore.SaveToFile(path); err != nil {
		log.WithError(err).Warn("usage stats: failed to persist state on shutdown")
	}
}

func runFlusher(ctx context.Context, path string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !runtimeDirty.Swap(false) {
				continue
			}
			if err := defaultStore.SaveToFile(path); err != nil {
				log.WithError(err).Warn("usage stats: failed to persist state")
			}
		}
	}
}

// state is the on-disk representation of the store.
type state struct {
	Version       int               `json:"version"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Currency      string            `json:"currency"`
	RetentionDays int               `json:"retention_days"`
	Buckets       map[string]*acc   `json:"buckets"`
	Entities      map[string]entity `json:"entities"`
}

// LoadFromFile restores store contents from disk.
func (s *Store) LoadFromFile(path string) error {
	if s == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// First run: nothing has been persisted yet.
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var restored state
	if err := json.Unmarshal(data, &restored); err != nil {
		return fmt.Errorf("decode usage stats state: %w", err)
	}
	if restored.Version != persistenceVersion {
		return fmt.Errorf("unsupported usage stats state version %d", restored.Version)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if restored.Buckets != nil {
		s.buckets = restored.Buckets
	}
	if restored.Entities != nil {
		s.entities = restored.Entities
	}
	if restored.Currency != "" {
		s.currency = restored.Currency
	}
	if restored.RetentionDays > 0 {
		s.retentionDays = restored.RetentionDays
	}
	s.pruneLocked()
	return nil
}

// SaveToFile writes the store contents to disk atomically.
func (s *Store) SaveToFile(path string) error {
	if s == nil || strings.TrimSpace(path) == "" {
		return nil
	}

	s.mu.Lock()
	s.pruneLocked()
	snapshot := state{
		Version:       persistenceVersion,
		UpdatedAt:     time.Now().UTC(),
		Currency:      s.currency,
		RetentionDays: s.retentionDays,
		Buckets:       make(map[string]*acc, len(s.buckets)),
		Entities:      make(map[string]entity, len(s.entities)),
	}
	for key, bucket := range s.buckets {
		cloned := *bucket
		snapshot.Buckets[key] = &cloned
	}
	for key, meta := range s.entities {
		snapshot.Entities[key] = meta
	}
	s.mu.Unlock()

	data, err := json.Marshal(&snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
