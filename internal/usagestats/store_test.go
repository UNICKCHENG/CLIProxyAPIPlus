package usagestats

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelprice"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// completeBreakdown builds a breakdown that satisfies the v2 accounting
// invariants declared by sdk/cliproxy/usage.
func completeBreakdown(uncached, cacheRead, cacheWrite, nonReasoning, reasoning int64) coreusage.TokenBreakdown {
	inputTotal := uncached + cacheRead + cacheWrite
	outputTotal := nonReasoning + reasoning
	return coreusage.TokenBreakdown{
		SchemaVersion: coreusage.TokenAccountingSchemaVersion,
		Quality:       coreusage.TokenAccountingQualityComplete,
		TotalTokens:   inputTotal + outputTotal,
		Input: coreusage.TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   uncached,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: coreusage.TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: nonReasoning,
			ReasoningTokens:    reasoning,
		},
	}
}

func sampleRecord() coreusage.Record {
	return coreusage.Record{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
		AuthID:   "credential-a",
		AuthType: "oauth",
		Detail: coreusage.Detail{
			TokenBreakdown: completeBreakdown(1000, 200, 0, 300, 100),
		},
	}
}

func TestStoreAggregatesPerModelAndChannel(t *testing.T) {
	store := NewStore(DefaultRetentionDays)

	first := sampleRecord()
	store.Record(first)

	second := sampleRecord()
	second.AuthID = "credential-b"
	store.Record(second)

	snapshot := store.Snapshot(7)

	if len(snapshot.Models) != 1 {
		t.Fatalf("Models has %d rows, want 1 (single model)", len(snapshot.Models))
	}
	if got := snapshot.Models[0].Requests; got != 2 {
		t.Errorf("model Requests = %d, want 2", got)
	}
	if got := snapshot.Models[0].Tokens.Total; got != 3200 {
		t.Errorf("model Tokens.Total = %d, want 3200", got)
	}

	if len(snapshot.Channels) != 2 {
		t.Fatalf("Channels has %d rows, want 2 (two credentials)", len(snapshot.Channels))
	}
	for _, row := range snapshot.Channels {
		if row.Requests != 1 {
			t.Errorf("channel %q Requests = %d, want 1", row.ID, row.Requests)
		}
	}

	if got := snapshot.Totals.Requests; got != 2 {
		t.Errorf("Totals.Requests = %d, want 2", got)
	}
}

func TestStoreCountsFailedAndUnpriced(t *testing.T) {
	store := NewStore(DefaultRetentionDays)

	failed := sampleRecord()
	failed.Failed = true
	store.Record(failed)

	// No price is installed for this model, so it must be counted as unpriced
	// rather than silently costed at zero.
	unpriced := sampleRecord()
	unpriced.Model = "mystery-model-without-a-price"
	store.Record(unpriced)

	snapshot := store.Snapshot(7)

	if got := snapshot.Totals.Failed; got != 1 {
		t.Errorf("Totals.Failed = %d, want 1", got)
	}

	var unpricedRow *Row
	for i := range snapshot.Models {
		if snapshot.Models[i].ID == "mystery-model-without-a-price" {
			unpricedRow = &snapshot.Models[i]
		}
	}
	if unpricedRow == nil {
		t.Fatal("unknown model is missing from the snapshot")
	}
	if unpricedRow.Unpriced != 1 {
		t.Errorf("Unpriced = %d, want 1", unpricedRow.Unpriced)
	}
	if unpricedRow.Cost != 0 {
		t.Errorf("Cost = %v, want 0 for an unpriced model", unpricedRow.Cost)
	}
}

func TestStoreEstimatesCostFromPriceTable(t *testing.T) {
	modelprice.ApplyOverrides([]modelprice.Override{
		{Model: "priced-model", Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	})
	t.Cleanup(func() { modelprice.ApplyOverrides(nil) })

	store := NewStore(DefaultRetentionDays)
	record := coreusage.Record{
		Provider: "anthropic",
		Model:    "priced-model",
		AuthID:   "credential-a",
		Detail: coreusage.Detail{
			TokenBreakdown: completeBreakdown(1000000, 0, 0, 1000000, 0),
		},
	}
	store.Record(record)

	snapshot := store.Snapshot(7)

	if len(snapshot.Models) != 1 {
		t.Fatalf("Models has %d rows, want 1", len(snapshot.Models))
	}
	row := snapshot.Models[0]
	if row.Priced != 1 {
		t.Errorf("Priced = %d, want 1", row.Priced)
	}
	// 1M input at $3/M plus 1M output at $15/M = $18.
	if math.Abs(row.Cost-18) > 1e-9 {
		t.Errorf("Cost = %v, want 18", row.Cost)
	}
}

func TestRetentionPrunesOldBuckets(t *testing.T) {
	store := NewStore(2)

	now := time.Now().UTC()
	for offset := 0; offset < 6; offset++ {
		record := sampleRecord()
		store.add(record, now.AddDate(0, 0, -offset))
	}

	from := now.AddDate(0, 0, -10).Format("2006-01-02")
	to := now.Format("2006-01-02")
	snapshot := store.SnapshotRange(from, to)

	// Retention is 2 days, so only today and yesterday may survive.
	if len(snapshot.Daily) > 2 {
		t.Errorf("Daily has %d points, want <= 2 after retention pruning", len(snapshot.Daily))
	}
	for _, point := range snapshot.Daily {
		limit := now.AddDate(0, 0, -1).Format("2006-01-02")
		if point.Date < limit {
			t.Errorf("Daily contains expired day %q (retention 2 days)", point.Date)
		}
	}
}

func TestSnapshotRangeLimitsToRequestedDays(t *testing.T) {
	store := NewStore(DefaultRetentionDays)

	now := time.Now().UTC()
	old := sampleRecord()
	old.Model = "older-model"
	store.add(old, now.AddDate(0, 0, -5))

	recent := sampleRecord()
	recent.Model = "recent-model"
	store.add(recent, now)

	// A 2-day window starting two days ago must exclude the 5-day-old record.
	from := now.AddDate(0, 0, -2).Format("2006-01-02")
	to := now.Format("2006-01-02")
	snapshot := store.SnapshotRange(from, to)

	for _, row := range snapshot.Models {
		if row.ID == "older-model" {
			t.Error("SnapshotRange included a model outside the requested window")
		}
	}

	var found bool
	for _, row := range snapshot.Models {
		if row.ID == "recent-model" {
			found = true
		}
	}
	if !found {
		t.Error("SnapshotRange omitted a model inside the requested window")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	store := NewStore(DefaultRetentionDays)
	store.Record(sampleRecord())
	store.SetCurrency("EUR")

	path := filepath.Join(t.TempDir(), "usage-stats.json")
	if err := store.SaveToFile(path); err != nil {
		t.Fatalf("SaveToFile() error = %v", err)
	}

	restored := NewStore(DefaultRetentionDays)
	if err := restored.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	original := store.Snapshot(7)
	reloaded := restored.Snapshot(7)

	if reloaded.Totals.Requests != original.Totals.Requests {
		t.Errorf("reloaded Requests = %d, want %d", reloaded.Totals.Requests, original.Totals.Requests)
	}
	if reloaded.Totals.Tokens.Total != original.Totals.Tokens.Total {
		t.Errorf("reloaded Tokens.Total = %d, want %d", reloaded.Totals.Tokens.Total, original.Totals.Tokens.Total)
	}
	if reloaded.Currency != "EUR" {
		t.Errorf("reloaded Currency = %q, want %q", reloaded.Currency, "EUR")
	}
}

func TestLoadFromFileMissingFileIsNotFatal(t *testing.T) {
	store := NewStore(DefaultRetentionDays)
	if err := store.LoadFromFile(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("LoadFromFile() of a missing file = %v, want nil", err)
	}
}

func TestTokensFromRecordPrefersCanonicalBreakdown(t *testing.T) {
	record := coreusage.Record{
		Detail: coreusage.Detail{
			InputTokens:    999,
			OutputTokens:   999,
			TotalTokens:    999,
			TokenBreakdown: completeBreakdown(1000, 200, 50, 300, 100),
		},
	}

	tokens := tokensFromRecord(record)

	if tokens.UncachedInput != 1000 {
		t.Errorf("UncachedInput = %d, want 1000 from the breakdown", tokens.UncachedInput)
	}
	if tokens.CacheRead != 200 {
		t.Errorf("CacheRead = %d, want 200", tokens.CacheRead)
	}
	if tokens.CacheWrite != 50 {
		t.Errorf("CacheWrite = %d, want 50", tokens.CacheWrite)
	}
	if tokens.Output != 300 {
		t.Errorf("Output = %d, want the non-reasoning count 300", tokens.Output)
	}
	if tokens.Reasoning != 100 {
		t.Errorf("Reasoning = %d, want 100", tokens.Reasoning)
	}
	if tokens.Total != 1650 {
		t.Errorf("Total = %d, want 1650", tokens.Total)
	}
}

func TestTokensFromRecordFallsBackToFlatCounters(t *testing.T) {
	record := coreusage.Record{
		Detail: coreusage.Detail{
			InputTokens:     1000,
			OutputTokens:    400,
			ReasoningTokens: 100,
			CacheReadTokens: 200,
			TotalTokens:     1400,
		},
	}

	tokens := tokensFromRecord(record)

	if tokens.UncachedInput != 800 {
		t.Errorf("UncachedInput = %d, want 800 (1000 input minus 200 cache read)", tokens.UncachedInput)
	}
	if tokens.CacheRead != 200 {
		t.Errorf("CacheRead = %d, want 200", tokens.CacheRead)
	}
	if tokens.Output != 300 {
		t.Errorf("Output = %d, want 300 (400 output minus 100 reasoning)", tokens.Output)
	}
	if tokens.Reasoning != 100 {
		t.Errorf("Reasoning = %d, want 100", tokens.Reasoning)
	}
	if tokens.Total != 1400 {
		t.Errorf("Total = %d, want 1400", tokens.Total)
	}
}

func TestTokensFromRecordUsesCachedTokensAlias(t *testing.T) {
	record := coreusage.Record{
		Detail: coreusage.Detail{
			InputTokens:  1000,
			CachedTokens: 250,
			TotalTokens:  1000,
		},
	}

	tokens := tokensFromRecord(record)

	if tokens.CacheRead != 250 {
		t.Errorf("CacheRead = %d, want the CachedTokens fallback 250", tokens.CacheRead)
	}
	if tokens.UncachedInput != 750 {
		t.Errorf("UncachedInput = %d, want 750", tokens.UncachedInput)
	}
}

func TestTokensFromRecordNeverNegative(t *testing.T) {
	record := coreusage.Record{
		Detail: coreusage.Detail{
			InputTokens:     100,
			CacheReadTokens: 900,
			ReasoningTokens: -5,
			OutputTokens:    -3,
		},
	}

	tokens := tokensFromRecord(record)

	if tokens.UncachedInput < 0 || tokens.Output < 0 || tokens.Reasoning < 0 {
		t.Errorf("tokensFromRecord() produced negative buckets: %+v", tokens)
	}
}

func TestEffectiveModelPrefersAlias(t *testing.T) {
	cases := []struct {
		name   string
		record coreusage.Record
		want   string
	}{
		{"alias wins", coreusage.Record{Alias: "gpt-5", Model: "gpt-5-2025-01-01"}, "gpt-5"},
		{"model when no alias", coreusage.Record{Model: "gpt-5"}, "gpt-5"},
		{"response model last", coreusage.Record{ResponseModel: "gpt-5-resp"}, "gpt-5-resp"},
		{"whitespace only alias", coreusage.Record{Alias: "   ", Model: "gpt-5"}, "gpt-5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveModel(tc.record); got != tc.want {
				t.Errorf("effectiveModel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChannelIdentityPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		record coreusage.Record
		want   string
	}{
		{
			"auth id wins",
			coreusage.Record{Provider: "anthropic", AuthType: "oauth", AuthID: "id", AuthIndex: "idx"},
			"anthropic|oauth|id",
		},
		{
			"auth index fallback",
			coreusage.Record{Provider: "anthropic", AuthType: "oauth", AuthIndex: "idx"},
			"anthropic|oauth|idx",
		},
		{
			"empty when nothing identifies the channel",
			coreusage.Record{},
			"",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := channelIdentity(tc.record); got != tc.want {
				t.Errorf("channelIdentity() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChannelIdentityHashesSecrets(t *testing.T) {
	record := coreusage.Record{Provider: "openai", AuthType: "apikey", APIKey: "sk-super-secret-value"}

	got := channelIdentity(record)

	if got == "" {
		t.Fatal("channelIdentity() = \"\", want a hashed identity")
	}
	if contains(got, "sk-super-secret-value") {
		t.Errorf("channelIdentity() leaked the raw API key: %q", got)
	}
	if contains(got, shortHash("sk-super-secret-value")) == false {
		t.Errorf("channelIdentity() = %q, want it to contain the 12-char hash", got)
	}
}

func TestPluginRecordsUsage(t *testing.T) {
	DefaultStore().Reset()

	plugin{}.HandleUsage(t.Context(), sampleRecord())

	if got := DefaultStore().Snapshot(7).Totals.Requests; got != 1 {
		t.Errorf("plugin recorded %d requests, want 1", got)
	}
}

func TestBucketKeyRoundTrip(t *testing.T) {
	key := bucketKey("2026-02-14", DimModel, "claude-sonnet-4-5")
	day, dim, id, ok := parseBucketKey(key)

	if !ok {
		t.Fatalf("parseBucketKey(%q) failed", key)
	}
	if day != "2026-02-14" || dim != DimModel || id != "claude-sonnet-4-5" {
		t.Errorf("parseBucketKey() = (%q, %q, %q), want (2026-02-14, %q, claude-sonnet-4-5)", day, dim, id, DimModel)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestSnapshotHoursReportsHourlyGranularity(t *testing.T) {
	store := NewStore(DefaultRetentionDays)

	now := time.Now().UTC()
	store.add(sampleRecord(), now)

	threeHoursAgo := sampleRecord()
	threeHoursAgo.AuthID = "credential-b"
	store.add(threeHoursAgo, now.Add(-3*time.Hour))

	snapshot := store.SnapshotHours(24)

	if snapshot.Granularity != GranularityHour {
		t.Errorf("Granularity = %q, want %q", snapshot.Granularity, GranularityHour)
	}
	if snapshot.Hours != 24 {
		t.Errorf("Hours = %d, want 24", snapshot.Hours)
	}
	if len(snapshot.Daily) != 2 {
		t.Fatalf("Daily has %d points, want 2", len(snapshot.Daily))
	}
	for _, point := range snapshot.Daily {
		if len(point.Date) != len(hourBucketFormat) {
			t.Errorf("Daily date %q is not an hour bucket (%q)", point.Date, hourBucketFormat)
		}
	}
	// Two requests in two different hours, each counted once in the provider
	// dimension only, so the total is not multiplied by the other dimensions.
	if snapshot.Totals.Requests != 2 {
		t.Errorf("Totals.Requests = %d, want 2", snapshot.Totals.Requests)
	}
}

func TestSnapshotHoursExcludesRecordsOutsideWindow(t *testing.T) {
	store := NewStore(DefaultRetentionDays)

	now := time.Now().UTC()
	old := sampleRecord()
	old.Model = "older-model"
	store.add(old, now.Add(-30*time.Hour))

	recent := sampleRecord()
	recent.Model = "recent-model"
	store.add(recent, now)

	snapshot := store.SnapshotHours(24)

	for _, row := range snapshot.Models {
		if row.ID == "older-model" {
			t.Error("SnapshotHours included a model outside the 24 hour window")
		}
	}
	var found bool
	for _, row := range snapshot.Models {
		if row.ID == "recent-model" {
			found = true
		}
	}
	if !found {
		t.Error("SnapshotHours omitted a model inside the 24 hour window")
	}
}

func TestSnapshotHoursWindowIsCappedByRingRetention(t *testing.T) {
	store := NewStore(DefaultRetentionDays)
	snapshot := store.SnapshotHours(1000)

	if snapshot.Hours != hourlyRetentionHours {
		t.Errorf("Hours = %d, want %d (capped)", snapshot.Hours, hourlyRetentionHours)
	}
	if snapshot.Granularity != GranularityHour {
		t.Errorf("Granularity = %q, want %q", snapshot.Granularity, GranularityHour)
	}
}

func TestHourlyRingIsPrunedToRetentionWindow(t *testing.T) {
	store := NewStore(DefaultRetentionDays)

	now := time.Now().UTC()
	// Spread records past the ring's window, ending on a record that triggers
	// the hourly prune.
	for offset := hourlyRetentionHours + 4; offset >= 0; offset-- {
		store.add(sampleRecord(), now.Add(-time.Duration(offset)*time.Hour))
	}

	cutoff := now.Add(-time.Duration(hourlyRetentionHours) * time.Hour).Format(hourBucketFormat)

	store.mu.Lock()
	defer store.mu.Unlock()

	for key := range store.hourly {
		hour, _, _, ok := parseBucketKey(key)
		if !ok || hour < cutoff {
			t.Errorf("hourly ring kept expired bucket %q (cutoff %q)", hour, cutoff)
		}
	}
	// Each hour holds at most one bucket per dimension (provider, model,
	// channel), so an exceeded bound means pruning never ran.
	if max := (hourlyRetentionHours + 1) * 3; len(store.hourly) > max {
		t.Errorf("hourly ring holds %d buckets, want <= %d", len(store.hourly), max)
	}
}

func TestDailyAndHourlyRingsCoexist(t *testing.T) {
	store := NewStore(DefaultRetentionDays)

	now := time.Now().UTC()
	store.add(sampleRecord(), now)

	daily := store.Snapshot(7)
	if daily.Granularity != GranularityDay {
		t.Errorf("Granularity = %q, want %q", daily.Granularity, GranularityDay)
	}
	if len(daily.Daily) != 1 || daily.Daily[0].Date != now.Format(dateFormat) {
		t.Fatalf("daily points = %+v, want one %q point", daily.Daily, now.Format(dateFormat))
	}

	hourly := store.SnapshotHours(24)
	if len(hourly.Daily) != 1 || hourly.Daily[0].Date != now.Format(hourBucketFormat) {
		t.Fatalf("hourly points = %+v, want one %q point", hourly.Daily, now.Format(hourBucketFormat))
	}

	// The same record is costed identically in both rings; the hourly ring is a
	// finer view of the same data, not a separate tally.
	if math.Abs(daily.Totals.Cost-hourly.Totals.Cost) > 1e-9 {
		t.Errorf("daily cost %v != hourly cost %v", daily.Totals.Cost, hourly.Totals.Cost)
	}
	if daily.Totals.Requests != hourly.Totals.Requests {
		t.Errorf("daily requests %d != hourly requests %d", daily.Totals.Requests, hourly.Totals.Requests)
	}
}

func TestBucketKeyRoundTripHourly(t *testing.T) {
	key := bucketKey("2026-02-14T09", DimProvider, "anthropic")
	stamp, dim, id, ok := parseBucketKey(key)

	if !ok {
		t.Fatalf("parseBucketKey(%q) failed", key)
	}
	if stamp != "2026-02-14T09" || dim != DimProvider || id != "anthropic" {
		t.Errorf("parseBucketKey() = (%q, %q, %q), want (2026-02-14T09, %q, anthropic)", stamp, dim, id, DimProvider)
	}
}
