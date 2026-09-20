// Package usagestats aggregates token consumption and estimated cost per model
// and per upstream channel (credential), and exposes snapshots for the
// management API.
package usagestats

import (
	"sort"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Aggregation dimensions.
const (
	DimModel    = "model"
	DimChannel  = "channel"
	DimProvider = "provider"
)

// DefaultRetentionDays bounds how far back daily buckets are kept.
const DefaultRetentionDays = 30

// maxBuckets caps the number of retained buckets in each ring.
const maxBuckets = 200000

// tokenFieldsSeparator keeps composite bucket keys unambiguous.
const tokenFieldsSeparator = "\x1f"

// Reporting granularities. Daily buckets answer the long range, hourly buckets
// answer the trailing-hours view.
const (
	GranularityDay  = "day"
	GranularityHour = "hour"
)

// Bucket time formats. parseBucketKey is format agnostic, so both rings share
// the same key layout and only the timestamp width differs.
const (
	dateFormat       = "2006-01-02"
	hourBucketFormat = "2006-01-02T15"
)

// hourlyRetentionHours bounds the fine-grained ring. It is deliberately far
// shorter than DefaultRetentionDays because hourly buckets multiply bucket
// cardinality by 24; keeping that resolution only for the recent window is what
// makes the 24-hour view affordable.
const hourlyRetentionHours = 48

// TokenTotals mirrors the canonical token buckets used for cost estimation.
type TokenTotals struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	Reasoning    int64 `json:"reasoning"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite   int64 `json:"cache_write"`
	Unclassified int64 `json:"unclassified"`
	Total        int64 `json:"total"`
}

func (t *TokenTotals) add(other TokenTotals) {
	t.Input += other.Input
	t.Output += other.Output
	t.Reasoning += other.Reasoning
	t.CacheRead += other.CacheRead
	t.CacheWrite += other.CacheWrite
	t.Unclassified += other.Unclassified
	t.Total += other.Total
}

// acc accumulates counters. It is persisted as-is, so the JSON tags are part of
// the on-disk contract.
type acc struct {
	Requests int64       `json:"requests"`
	Failed   int64       `json:"failed"`
	Priced   int64       `json:"priced"`
	Unpriced int64       `json:"unpriced"`
	Cost     float64     `json:"cost"`
	Tokens   TokenTotals `json:"tokens"`
}

func (a *acc) merge(other acc) {
	a.Requests += other.Requests
	a.Failed += other.Failed
	a.Priced += other.Priced
	a.Unpriced += other.Unpriced
	a.Cost += other.Cost
	a.Tokens.add(other.Tokens)
}

// entity describes one aggregation target.
type entity struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	Provider  string `json:"provider,omitempty"`
	AuthType  string `json:"auth_type,omitempty"`
	AuthIndex string `json:"auth_index,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	Model     string `json:"model,omitempty"`
	Alias     string `json:"alias,omitempty"`
}

// Row is one row of an aggregated report.
type Row struct {
	ID        string      `json:"id"`
	Label     string      `json:"label,omitempty"`
	Provider  string      `json:"provider,omitempty"`
	AuthType  string      `json:"auth_type,omitempty"`
	AuthIndex string      `json:"auth_index,omitempty"`
	BaseURL   string      `json:"base_url,omitempty"`
	Model     string      `json:"model,omitempty"`
	Alias     string      `json:"alias,omitempty"`
	Requests  int64       `json:"requests"`
	Failed    int64       `json:"failed"`
	Priced    int64       `json:"priced"`
	Unpriced  int64       `json:"unpriced"`
	Cost      float64     `json:"cost"`
	Tokens    TokenTotals `json:"tokens"`
}

// DailyPoint is one day of aggregate traffic.
type DailyPoint struct {
	Date string `json:"date"`
	Row
}

// Snapshot is the full report returned to clients.
type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	Currency    string    `json:"currency"`
	// Granularity is GranularityDay or GranularityHour. It tells clients how to
	// read Date in the Daily series: a day stamp or an hour stamp.
	Granularity string `json:"granularity"`
	Days        int    `json:"days"`
	// Hours is set only for hourly snapshots.
	Hours     int          `json:"hours,omitempty"`
	From      string       `json:"from"`
	To        string       `json:"to"`
	Totals    Row          `json:"totals"`
	Models    []Row        `json:"models"`
	Providers []Row        `json:"providers"`
	Channels  []Row        `json:"channels"`
	Daily     []DailyPoint `json:"daily"`
}

// Store keeps daily counters per aggregation dimension.
type Store struct {
	mu            sync.Mutex
	buckets       map[string]*acc
	hourly        map[string]*acc
	entities      map[string]entity
	retentionDays int
	currency      string
	lastDay       string
	lastHour      string
}

// NewStore returns an empty store with the given retention window.
func NewStore(retentionDays int) *Store {
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	return &Store{
		buckets:       make(map[string]*acc),
		hourly:        make(map[string]*acc),
		entities:      make(map[string]entity),
		retentionDays: retentionDays,
		currency:      "USD",
	}
}

func bucketKey(day, dim, id string) string {
	return day + tokenFieldsSeparator + dim + tokenFieldsSeparator + id
}

func entityKey(dim, id string) string {
	return dim + tokenFieldsSeparator + id
}

func parseBucketKey(key string) (day, dim, id string, ok bool) {
	parts := strings.SplitN(key, tokenFieldsSeparator, 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// SetRetentionDays updates the retention window and prunes immediately.
func (s *Store) SetRetentionDays(days int) {
	if s == nil {
		return
	}
	if days <= 0 {
		days = DefaultRetentionDays
	}
	s.mu.Lock()
	s.retentionDays = days
	s.pruneLocked()
	s.mu.Unlock()
}

// SetCurrency overrides the reporting currency label.
func (s *Store) SetCurrency(currency string) {
	if s == nil {
		return
	}
	currency = strings.TrimSpace(currency)
	if currency == "" {
		currency = "USD"
	}
	s.mu.Lock()
	s.currency = currency
	s.mu.Unlock()
}

// Reset drops all accumulated data. It is intended for tests and admin resets.
func (s *Store) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.buckets = make(map[string]*acc)
	s.hourly = make(map[string]*acc)
	s.entities = make(map[string]entity)
	s.mu.Unlock()
}

// retentionCutoffLocked returns the oldest day that is still retained. The
// window is anchored to the current UTC day, so a record that falls outside it
// (for example a backfilled row) is dropped rather than stored and pruned later.
func (s *Store) retentionCutoffLocked() string {
	days := s.retentionDays
	if days <= 0 {
		days = DefaultRetentionDays
	}
	return time.Now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
}

// pruneLocked drops buckets outside the retention window.
func (s *Store) pruneLocked() {
	minDay := s.retentionCutoffLocked()
	for key := range s.buckets {
		day, _, _, ok := parseBucketKey(key)
		if !ok || day < minDay {
			delete(s.buckets, key)
		}
	}
}

// pruneHourlyLocked drops hourly buckets older than the fine-grained window.
func (s *Store) pruneHourlyLocked() {
	minHour := time.Now().UTC().
		Add(-time.Duration(hourlyRetentionHours) * time.Hour).
		Format(hourBucketFormat)
	for key := range s.hourly {
		hour, _, _, ok := parseBucketKey(key)
		if !ok || hour < minHour {
			delete(s.hourly, key)
		}
	}
}

// enforceCapLocked drops the oldest buckets when a ring exceeds the cap.
func (s *Store) enforceCapLocked() {
	s.enforceBucketCapLocked(s.buckets)
	s.enforceBucketCapLocked(s.hourly)
}

func (s *Store) enforceBucketCapLocked(buckets map[string]*acc) {
	if len(buckets) <= maxBuckets {
		return
	}
	stamps := make([]string, 0, len(buckets))
	seen := make(map[string]struct{}, len(buckets))
	for key := range buckets {
		stamp, _, _, ok := parseBucketKey(key)
		if !ok {
			continue
		}
		if _, exists := seen[stamp]; exists {
			continue
		}
		seen[stamp] = struct{}{}
		stamps = append(stamps, stamp)
	}
	sort.Strings(stamps)
	for _, stamp := range stamps {
		if len(buckets) <= maxBuckets {
			break
		}
		prefix := stamp + tokenFieldsSeparator
		for key := range buckets {
			if strings.HasPrefix(key, prefix) {
				delete(buckets, key)
			}
		}
	}
}

// Record folds one usage record into the store, attributing it to the current
// UTC day.
//
// The record is counted once in each aggregation dimension (provider, model,
// channel) so that per-dimension reports stay independently complete.
func (s *Store) Record(record coreusage.Record) {
	if s == nil {
		return
	}
	s.add(record, time.Now().UTC())
}

func (s *Store) add(record coreusage.Record, now time.Time) {
	day := now.Format(dateFormat)
	hour := now.Format(hourBucketFormat)
	tokens := tokensFromRecord(record)
	cost, priced := costFor(record, tokens)

	entry := acc{
		Requests: 1,
		Cost:     cost,
		Tokens:   tokens.totalTotals(),
	}
	if record.Failed {
		entry.Failed = 1
	}
	if priced {
		entry.Priced = 1
	} else {
		entry.Unpriced = 1
	}

	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	modelID := effectiveModel(record)
	channelID := channelIdentity(record)

	s.mu.Lock()
	if s.retentionDays <= 0 {
		s.retentionDays = DefaultRetentionDays
	}
	if day < s.retentionCutoffLocked() {
		s.mu.Unlock()
		return
	}
	if len(s.buckets) > 0 && day != s.lastDay {
		s.pruneLocked()
	}
	s.lastDay = day
	if len(s.hourly) > 0 && hour != s.lastHour {
		s.pruneHourlyLocked()
	}
	s.lastHour = hour

	s.applyLocked(day, hour, DimProvider, provider, entity{
		ID:       provider,
		Label:    provider,
		Provider: provider,
	}, entry)

	if modelID != "" {
		s.applyLocked(day, hour, DimModel, modelID, entity{
			ID:       modelID,
			Label:    modelID,
			Provider: provider,
			Model:    strings.TrimSpace(record.Model),
			Alias:    strings.TrimSpace(record.Alias),
		}, entry)
	}

	if channelID != "" {
		label := strings.TrimSpace(record.AuthType)
		if label == "" {
			label = provider
		}
		s.applyLocked(day, hour, DimChannel, channelID, entity{
			ID:        channelID,
			Label:     label,
			Provider:  provider,
			AuthType:  strings.TrimSpace(record.AuthType),
			AuthIndex: strings.TrimSpace(record.AuthIndex),
			BaseURL:   strings.TrimSpace(record.BaseURL),
		}, entry)
	}

	s.enforceCapLocked()
	s.mu.Unlock()
}

// applyLocked folds one record into both rings and refreshes entity metadata.
// The daily ring answers long ranges; the hourly ring answers trailing hours.
func (s *Store) applyLocked(day, hour, dim, id string, meta entity, entry acc) {
	s.mergeBucketLocked(s.buckets, bucketKey(day, dim, id), entry)
	s.mergeBucketLocked(s.hourly, bucketKey(hour, dim, id), entry)

	eKey := entityKey(dim, id)
	if existing, ok := s.entities[eKey]; ok {
		meta = mergeEntity(existing, meta)
	}
	s.entities[eKey] = meta
}

func (s *Store) mergeBucketLocked(buckets map[string]*acc, key string, entry acc) {
	bucket, ok := buckets[key]
	if !ok {
		bucket = &acc{}
		buckets[key] = bucket
	}
	bucket.merge(entry)
}

func mergeEntity(base, update entity) entity {
	if update.Label != "" {
		base.Label = update.Label
	}
	if base.Provider == "" {
		base.Provider = update.Provider
	}
	if base.AuthType == "" {
		base.AuthType = update.AuthType
	}
	if base.AuthIndex == "" {
		base.AuthIndex = update.AuthIndex
	}
	if base.BaseURL == "" {
		base.BaseURL = update.BaseURL
	}
	if base.Model == "" {
		base.Model = update.Model
	}
	if base.Alias == "" {
		base.Alias = update.Alias
	}
	if base.ID == "" {
		base.ID = update.ID
	}
	return base
}

// Snapshot aggregates the last `days` days (including today).
func (s *Store) Snapshot(days int) Snapshot {
	if days <= 0 {
		days = 7
	}
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -(days - 1))
	return s.snapshotBuckets(
		s.buckets,
		from.Format(dateFormat),
		to.Format(dateFormat),
		GranularityDay,
		days,
		0,
	)
}

// SnapshotHours aggregates the trailing `hours` hours from the hourly ring. The
// window is capped at that ring's own retention bound.
func (s *Store) SnapshotHours(hours int) Snapshot {
	if hours <= 0 {
		hours = 24
	}
	if hours > hourlyRetentionHours {
		hours = hourlyRetentionHours
	}
	now := time.Now().UTC()
	return s.snapshotBuckets(
		s.hourly,
		now.Add(-time.Duration(hours)*time.Hour).Format(hourBucketFormat),
		now.Format(hourBucketFormat),
		GranularityHour,
		0,
		hours,
	)
}

// SnapshotRange aggregates daily buckets between fromDay and toDay inclusive.
// Days is left for the caller to fill, matching the pre-existing contract.
func (s *Store) SnapshotRange(fromDay, toDay string) Snapshot {
	return s.snapshotBuckets(s.buckets, fromDay, toDay, GranularityDay, 0, 0)
}

// snapshotBuckets aggregates one ring over an inclusive [fromKey, toKey] window.
// Both rings share the key layout, so only the ring, the window and the reported
// granularity differ between a daily and an hourly report.
func (s *Store) snapshotBuckets(
	ring map[string]*acc,
	fromKey, toKey, granularity string,
	days, hours int,
) Snapshot {
	snap := Snapshot{
		GeneratedAt: time.Now().UTC(),
		Currency:    "USD",
		Granularity: granularity,
		Days:        days,
		Hours:       hours,
		From:        fromKey,
		To:          toKey,
		Models:      []Row{},
		Providers:   []Row{},
		Channels:    []Row{},
		Daily:       []DailyPoint{},
	}
	if s == nil {
		return snap
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snap.Currency = s.currency
	if fromKey == "" || toKey == "" {
		return snap
	}

	agg := make(map[string]*Row)
	daily := make(map[string]*Row)
	totals := &Row{ID: "total"}

	for key, bucket := range ring {
		stamp, dim, id, ok := parseBucketKey(key)
		if !ok || stamp < fromKey || stamp > toKey {
			continue
		}
		meta := s.entities[entityKey(dim, id)]

		row := rowFor(meta, dim, id)
		applyBucket(row, bucket)

		// Per-request totals and the daily series come from the provider
		// dimension alone. Every record produces exactly one provider bucket,
		// whereas model and channel rows only exist for records that carry those
		// identities, so summing all three dimensions would triple-count.
		if dim == DimProvider {
			totals.Cost += bucket.Cost
			totals.Requests += bucket.Requests
			totals.Failed += bucket.Failed
			totals.Priced += bucket.Priced
			totals.Unpriced += bucket.Unpriced
			totals.Tokens.add(bucket.Tokens)

			point, ok := daily[stamp]
			if !ok {
				point = &Row{ID: stamp}
				daily[stamp] = point
			}
			point.Cost += bucket.Cost
			point.Requests += bucket.Requests
			point.Failed += bucket.Failed
			point.Priced += bucket.Priced
			point.Unpriced += bucket.Unpriced
			point.Tokens.add(bucket.Tokens)
		}

		key2 := dim + tokenFieldsSeparator + id
		if existing, ok := agg[key2]; ok {
			existing.Cost += bucket.Cost
			existing.Requests += bucket.Requests
			existing.Failed += bucket.Failed
			existing.Priced += bucket.Priced
			existing.Unpriced += bucket.Unpriced
			existing.Tokens.add(bucket.Tokens)
		} else {
			agg[key2] = row
		}
	}

	for key, row := range agg {
		dim := strings.SplitN(key, tokenFieldsSeparator, 2)[0]
		switch dim {
		case DimModel:
			snap.Models = append(snap.Models, *row)
		case DimChannel:
			snap.Channels = append(snap.Channels, *row)
		case DimProvider:
			snap.Providers = append(snap.Providers, *row)
		}
	}

	for stamp, row := range daily {
		snap.Daily = append(snap.Daily, DailyPoint{Date: stamp, Row: *row})
	}

	sortRows(snap.Models)
	sortRows(snap.Providers)
	sortRows(snap.Channels)
	sort.Slice(snap.Daily, func(i, j int) bool { return snap.Daily[i].Date < snap.Daily[j].Date })

	snap.Totals = *totals
	snap.Totals.Tokens = totals.Tokens
	return snap
}

func rowFor(meta entity, dim, id string) *Row {
	row := &Row{
		ID:        id,
		Label:     meta.Label,
		Provider:  meta.Provider,
		AuthType:  meta.AuthType,
		AuthIndex: meta.AuthIndex,
		BaseURL:   meta.BaseURL,
		Model:     meta.Model,
		Alias:     meta.Alias,
	}
	if row.Label == "" {
		row.Label = id
	}
	if dim == DimProvider {
		row.ID = id
	}
	return row
}

func applyBucket(row *Row, bucket *acc) {
	row.Cost += bucket.Cost
	row.Requests += bucket.Requests
	row.Failed += bucket.Failed
	row.Priced += bucket.Priced
	row.Unpriced += bucket.Unpriced
	row.Tokens.add(bucket.Tokens)
}

// sortRows orders rows by cost desc, then tokens desc, then id.
func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Cost != rows[j].Cost {
			return rows[i].Cost > rows[j].Cost
		}
		if rows[i].Tokens.Total != rows[j].Tokens.Total {
			return rows[i].Tokens.Total > rows[j].Tokens.Total
		}
		return rows[i].ID < rows[j].ID
	})
}
