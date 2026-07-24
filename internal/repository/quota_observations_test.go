package repository

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"

	"gorm.io/gorm"
)

func TestSumQuotaAttributedUsageFiltersCredentialAndHalfOpenWindowAndPreservesBuckets(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	events := []entities.UsageEvent{
		{
			EventKey:            "included",
			AuthType:            "oauth",
			AuthIndex:           "auth-1",
			Model:               "priced",
			Timestamp:           start,
			InputTokens:         1_000_000,
			OutputTokens:        500_000,
			CacheReadTokens:     200_000,
			CacheCreationTokens: 100_000,
			TotalTokens:         1_700_000,
		},
		{EventKey: "wrong-auth-type", AuthType: "apikey", AuthIndex: "auth-1", Model: "priced", Timestamp: start.Add(time.Minute), TotalTokens: 9_000_000},
		{EventKey: "wrong-credential", AuthType: "oauth", AuthIndex: "auth-2", Model: "priced", Timestamp: start.Add(time.Minute), TotalTokens: 8_000_000},
		{EventKey: "at-end", AuthType: "oauth", AuthIndex: "auth-1", Model: "priced", Timestamp: end, TotalTokens: 7_000_000},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed usage events: %v", err)
	}
	resolver := quotaObservationPricingResolver(t, true)

	attribution, err := SumQuotaAttributedUsage(context.Background(), db, "oauth", "auth-1", start, end, events[0].ID, QuotaAttributionTrigger{}, resolver)
	if err != nil {
		t.Fatalf("SumQuotaAttributedUsage returned error: %v", err)
	}
	if attribution.TotalTokens != 1_700_000 ||
		attribution.InputTokens != 1_000_000 ||
		attribution.OutputTokens != 500_000 ||
		attribution.CacheReadTokens != 200_000 ||
		attribution.CacheCreationTokens != 100_000 {
		t.Fatalf("unexpected attributed token composition: %+v", attribution)
	}
	wantCost := 7.0 + 10.0 + 0.2 + 0.3
	if math.Abs(attribution.CostUSD-wantCost) > 1e-9 {
		t.Fatalf("expected cost %.9f, got %.9f", wantCost, attribution.CostUSD)
	}
	if !attribution.CostComplete || attribution.PricingSnapshotHash != resolver.ContentHash() {
		t.Fatalf("expected complete cost with matching snapshot hash, got %+v", attribution)
	}
}

func TestSumQuotaAttributedUsageMarksMixedPricedTokensIncomplete(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	events := []entities.UsageEvent{
		{EventKey: "priced", AuthType: "oauth", AuthIndex: "auth-1", Model: "priced", Timestamp: start.Add(time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000},
		{EventKey: "unpriced", AuthType: "oauth", AuthIndex: "auth-1", Model: "missing", Timestamp: start.Add(2 * time.Minute), OutputTokens: 500_000, TotalTokens: 500_000},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed usage events: %v", err)
	}

	attribution, err := SumQuotaAttributedUsage(
		context.Background(),
		db,
		"oauth",
		"auth-1",
		start,
		end,
		events[len(events)-1].ID,
		QuotaAttributionTrigger{},
		quotaObservationPricingResolver(t, true),
	)
	if err != nil {
		t.Fatalf("SumQuotaAttributedUsage returned error: %v", err)
	}
	if attribution.TotalTokens != 1_500_000 || attribution.CostUSD != 10 || attribution.CostComplete {
		t.Fatalf("expected token-complete but price-incomplete attribution, got %+v", attribution)
	}
}

func TestSumQuotaAttributedUsageMarksTotalOnlyUnpricedTokensIncomplete(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	event := entities.UsageEvent{
		EventKey:    "unpriced-total-only",
		AuthType:    "oauth",
		AuthIndex:   "auth-1",
		Model:       "missing",
		Timestamp:   start,
		TotalTokens: 500,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("seed usage event: %v", err)
	}

	attribution, err := SumQuotaAttributedUsage(
		context.Background(),
		db,
		"oauth",
		"auth-1",
		start,
		start.Add(time.Hour),
		event.ID,
		QuotaAttributionTrigger{},
		quotaObservationPricingResolver(t, true),
	)
	if err != nil {
		t.Fatalf("SumQuotaAttributedUsage returned error: %v", err)
	}
	if attribution.TotalTokens != 500 || attribution.CostComplete {
		t.Fatalf("expected total-only unpriced tokens to make cost incomplete, got %+v", attribution)
	}
}

func TestSumQuotaAttributedUsageStopsAtCapturedWatermark(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{
			EventKey:    "captured",
			AuthType:    "oauth",
			AuthIndex:   "auth-1",
			Model:       "priced",
			Timestamp:   start.Add(time.Minute),
			InputTokens: 100,
			TotalTokens: 100,
		},
		{
			EventKey:     "concurrent-after-watermark",
			AuthType:     "oauth",
			AuthIndex:    "auth-1",
			Model:        "priced",
			Timestamp:    start.Add(2 * time.Minute),
			OutputTokens: 900,
			TotalTokens:  900,
		},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed usage events: %v", err)
	}

	attribution, err := SumQuotaAttributedUsage(
		context.Background(),
		db,
		"oauth",
		"auth-1",
		start,
		start.Add(time.Hour),
		events[0].ID,
		QuotaAttributionTrigger{},
		quotaObservationPricingResolver(t, true),
	)
	if err != nil {
		t.Fatalf("SumQuotaAttributedUsage returned error: %v", err)
	}
	if attribution.TotalTokens != 100 || attribution.InputTokens != 100 || attribution.OutputTokens != 0 {
		t.Fatalf("attribution crossed captured watermark %d: %+v", events[0].ID, attribution)
	}
}

func TestQuotaObservationNewEventGateUsesCredentialCompositeIndex(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	var planRows []struct {
		Detail string
	}
	if err := db.Raw(
		"EXPLAIN QUERY PLAN SELECT COALESCE(MAX(id), 0) FROM usage_events WHERE auth_type = ? AND auth_index = ? AND id > ? AND timestamp < ?",
		"oauth",
		"auth-1",
		0,
		"2026-07-23T12:00:00Z",
	).Scan(&planRows).Error; err != nil {
		t.Fatalf("explain usage event watermark query: %v", err)
	}
	details := make([]string, len(planRows))
	for index, row := range planRows {
		details[index] = row.Detail
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "USING INDEX") ||
		(!strings.Contains(plan, "idx_usage_events_auth_type_auth_index_id") &&
			!strings.Contains(plan, "idx_usage_events_auth_index_timestamp_id")) {
		t.Fatalf("expected watermark query to use a credential index, got %v", details)
	}
}

func TestQuotaObservationWatermarkExcludesEventsAfterObservationTime(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	observedAt := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	events := []entities.UsageEvent{
		{EventKey: "eligible", AuthType: "oauth", AuthIndex: "auth-1", Timestamp: observedAt.Add(-time.Second)},
		{EventKey: "future", AuthType: "oauth", AuthIndex: "auth-1", Timestamp: observedAt.Add(time.Second)},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed usage events: %v", err)
	}

	watermark, err := MaxUsageEventIDForCredential(context.Background(), db, "oauth", "auth-1", 0, observedAt, QuotaAttributionTrigger{})
	if err != nil {
		t.Fatalf("MaxUsageEventIDForCredential returned error: %v", err)
	}
	if watermark != events[0].ID {
		t.Fatalf("watermark = %d, want eligible event ID %d", watermark, events[0].ID)
	}
}

func TestSumQuotaAttributedUsageIncludesExactHeaderTriggerOnceAndPreservesWatermark(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	events := []entities.UsageEvent{
		{
			EventKey:     "trigger-at-end",
			AuthType:     "oauth",
			AuthIndex:    "auth-1",
			Model:        "priced",
			Timestamp:    start.Add(-time.Minute),
			OutputTokens: 5_000,
			TotalTokens:  5_000,
		},
		{
			EventKey:            "inside-and-trigger",
			AuthType:            "oauth",
			AuthIndex:           "auth-1",
			Model:               "priced",
			Timestamp:           end.Add(-time.Minute),
			InputTokens:         100,
			OutputTokens:        200,
			CacheReadTokens:     300,
			CacheCreationTokens: 400,
			TotalTokens:         1_000,
		},
		{
			EventKey:            "trigger-at-end",
			AuthType:            "oauth",
			AuthIndex:           "auth-1",
			Model:               "priced",
			Timestamp:           end,
			InputTokens:         10,
			OutputTokens:        20,
			CacheReadTokens:     30,
			CacheCreationTokens: 40,
			TotalTokens:         100,
		},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed usage events: %v", err)
	}

	watermark, err := MaxUsageEventIDForCredential(
		context.Background(),
		db,
		"oauth",
		"auth-1",
		0,
		end,
		QuotaAttributionTrigger{EventID: events[2].ID, EventKey: "trigger-at-end"},
	)
	if err != nil {
		t.Fatalf("MaxUsageEventIDForCredential returned error: %v", err)
	}
	if watermark != events[2].ID {
		t.Fatalf("header watermark = %d, want trigger ID %d", watermark, events[2].ID)
	}
	laterEvents := []entities.UsageEvent{
		{
			EventKey:     "after-watermark",
			AuthType:     "oauth",
			AuthIndex:    "auth-1",
			Model:        "priced",
			Timestamp:    end.Add(-time.Second),
			OutputTokens: 9_000,
			TotalTokens:  9_000,
		},
		{
			EventKey:    "wrong-credential-trigger",
			AuthType:    "oauth",
			AuthIndex:   "auth-2",
			Model:       "priced",
			Timestamp:   end,
			TotalTokens: 8_000,
		},
	}
	if err := db.Create(&laterEvents).Error; err != nil {
		t.Fatalf("seed post-watermark usage events: %v", err)
	}

	attribution, err := SumQuotaAttributedUsage(
		context.Background(),
		db,
		"oauth",
		"auth-1",
		start,
		end,
		watermark,
		QuotaAttributionTrigger{EventID: events[2].ID, EventKey: "trigger-at-end"},
		quotaObservationPricingResolver(t, true),
	)
	if err != nil {
		t.Fatalf("SumQuotaAttributedUsage returned error: %v", err)
	}
	if attribution.TotalTokens != 1_100 ||
		attribution.InputTokens != 110 ||
		attribution.OutputTokens != 220 ||
		attribution.CacheReadTokens != 330 ||
		attribution.CacheCreationTokens != 440 {
		t.Fatalf("unexpected header attribution or double count: %+v", attribution)
	}
	if !attribution.CostComplete || attribution.PricingSnapshotHash == "" {
		t.Fatalf("expected complete frozen pricing attribution, got %+v", attribution)
	}

	alreadyInside, err := SumQuotaAttributedUsage(
		context.Background(),
		db,
		"oauth",
		"auth-1",
		start,
		end,
		watermark,
		QuotaAttributionTrigger{EventID: events[1].ID, EventKey: "inside-and-trigger"},
		quotaObservationPricingResolver(t, true),
	)
	if err != nil {
		t.Fatalf("SumQuotaAttributedUsage inside trigger returned error: %v", err)
	}
	if alreadyInside.TotalTokens != 1_000 {
		t.Fatalf("trigger inside half-open bound counted more than once: %+v", alreadyInside)
	}
}

func TestInsertQuotaObservationEnforcesSpacingResetExceptionAndDailyCap(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	observedAt := time.Date(2026, 7, 23, 10, 0, 0, 0, time.Local)
	resetAt := observedAt.Add(time.Hour)
	first := quotaObservationRepositoryRow(observedAt, resetAt)
	result, err := InsertQuotaObservationIfDue(context.Background(), db, first, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationInserted {
		t.Fatalf("insert first observation: result=%s err=%v", result, err)
	}

	spaced := quotaObservationRepositoryRow(observedAt.Add(time.Minute), resetAt)
	result, err = InsertQuotaObservationIfDue(context.Background(), db, spaced, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationSkipped {
		t.Fatalf("expected spacing skip: result=%s err=%v", result, err)
	}

	resetBoundary := quotaObservationRepositoryRow(observedAt.Add(time.Minute), resetAt.Add(7*24*time.Hour))
	result, err = InsertQuotaObservationIfDue(context.Background(), db, resetBoundary, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationInserted {
		t.Fatalf("expected reset-boundary exception: result=%s err=%v", result, err)
	}

	outOfOrder := quotaObservationRepositoryRow(observedAt.Add(-time.Hour), resetAt)
	outOfOrder.Source = "usage_header"
	result, err = InsertQuotaObservationIfDue(context.Background(), db, outOfOrder, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationInserted {
		t.Fatalf("expected spaced out-of-order header insert: result=%s err=%v", result, err)
	}
	result, err = InsertQuotaObservationIfDue(context.Background(), db, outOfOrder, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationSkipped {
		t.Fatalf("expected out-of-order header replay skip: result=%s err=%v", result, err)
	}
	nearOutOfOrder := quotaObservationRepositoryRow(observedAt.Add(-time.Hour+time.Minute), resetAt)
	nearOutOfOrder.Source = "usage_header"
	result, err = InsertQuotaObservationIfDue(context.Background(), db, nearOutOfOrder, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationInserted {
		t.Fatalf("expected out-of-order header to bypass forward-only spacing: result=%s err=%v", result, err)
	}

	var currentCount int64
	if err := db.Model(&entities.QuotaObservation{}).Count(&currentCount).Error; err != nil {
		t.Fatalf("count current observations: %v", err)
	}
	rows := make([]entities.QuotaObservation, 0, 400-currentCount)
	for index := currentCount; index < 400; index++ {
		row := quotaObservationRepositoryRow(observedAt.Add(time.Duration(index)*time.Second), resetAt.Add(time.Duration(index)*time.Second))
		rows = append(rows, row)
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed observations to daily cap: %v", err)
	}
	overCap := quotaObservationRepositoryRow(observedAt.Add(2*time.Minute), resetAt.Add(8*24*time.Hour))
	result, err = InsertQuotaObservationIfDue(context.Background(), db, overCap, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationDailyLimit {
		t.Fatalf("expected absolute daily cap: result=%s err=%v", result, err)
	}
}

func TestInsertQuotaObservationDoesNotTreatDerivedResetJitterAsBoundary(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	observedAt := time.Date(2026, 7, 23, 10, 0, 0, 0, time.Local)
	resetAt := observedAt.Add(5 * time.Hour)
	first := quotaObservationRepositoryRow(observedAt, resetAt)
	first.ResetRaw = nil
	first.ResetAfterSeconds = int64TestPointer(5 * 60 * 60)
	result, err := InsertQuotaObservationIfDue(context.Background(), db, first, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationInserted {
		t.Fatalf("insert first derived-reset observation: result=%s err=%v", result, err)
	}

	jittered := quotaObservationRepositoryRow(observedAt.Add(time.Minute), resetAt.Add(time.Second))
	jittered.ResetRaw = nil
	jittered.ResetAfterSeconds = int64TestPointer(5*60*60 - 60)
	result, err = InsertQuotaObservationIfDue(context.Background(), db, jittered, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationSkipped {
		t.Fatalf("derived reset jitter bypassed spacing: result=%s err=%v", result, err)
	}

	nextBoundary := quotaObservationRepositoryRow(observedAt.Add(2*time.Minute), resetAt.Add(5*time.Hour))
	nextBoundary.ResetRaw = nil
	nextBoundary.ResetAfterSeconds = int64TestPointer(10*60*60 - 120)
	result, err = InsertQuotaObservationIfDue(context.Background(), db, nextBoundary, 5*time.Minute, 400)
	if err != nil || result != QuotaObservationInserted {
		t.Fatalf("true derived reset boundary did not bypass spacing: result=%s err=%v", result, err)
	}
}

func TestInsertQuotaObservationPreventsConcurrentDuplicateAcrossWorkers(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	row := quotaObservationRepositoryRow(
		time.Date(2026, 7, 23, 10, 0, 0, 0, time.Local),
		time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local),
	)
	var wait sync.WaitGroup
	results := make(chan QuotaObservationInsertResult, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := InsertQuotaObservationIfDue(context.Background(), db, row, 5*time.Minute, 400)
			results <- result
			errorsCh <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent insert returned error: %v", err)
		}
	}
	inserted := 0
	skipped := 0
	for result := range results {
		switch result {
		case QuotaObservationInserted:
			inserted++
		case QuotaObservationSkipped:
			skipped++
		}
	}
	if inserted != 1 || skipped != 1 {
		t.Fatalf("expected one insert and one skip, got inserted=%d skipped=%d", inserted, skipped)
	}
	var count int64
	if err := db.Model(&entities.QuotaObservation{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one stored observation, count=%d err=%v", count, err)
	}
}

func TestListQuotaObservationsOrdersCapsAndMarksTruncation(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)
	rows := make([]entities.QuotaObservation, 5001)
	for index := range rows {
		rows[index] = quotaObservationRepositoryRow(
			start.Add(time.Duration(5000-index)*time.Second),
			start.Add(5*time.Hour),
		)
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed observation series: %v", err)
	}

	items, truncated, err := ListQuotaObservations(
		context.Background(),
		db,
		1,
		"codex/overall/rate_limit/18000",
		start,
		start.Add(24*time.Hour),
		5000,
	)
	if err != nil {
		t.Fatalf("ListQuotaObservations returned error: %v", err)
	}
	if len(items) != 5000 || !truncated {
		t.Fatalf("expected 5000 rows with truncation, got rows=%d truncated=%v", len(items), truncated)
	}
	if !items[0].ObservedAt.Equal(start) || !items[len(items)-1].ObservedAt.Equal(start.Add(4999*time.Second)) {
		t.Fatalf("expected ascending oldest 5000 rows, first=%v last=%v", items[0].ObservedAt, items[len(items)-1].ObservedAt)
	}
}

func TestInsertQuotaObservationSkipsColdRestartDuplicate(t *testing.T) {
	db := openQuotaObservationRepositoryDatabase(t)
	observedAt := time.Date(2026, 7, 23, 10, 0, 0, 0, time.Local)
	row := quotaObservationRepositoryRow(observedAt, observedAt.Add(5*time.Hour))
	first, err := InsertQuotaObservationIfDue(context.Background(), db, row, 5*time.Minute, 400)
	if err != nil || first != QuotaObservationInserted {
		t.Fatalf("insert first process observation: result=%s err=%v", first, err)
	}
	second, err := InsertQuotaObservationIfDue(context.Background(), db, row, 5*time.Minute, 400)
	if err != nil || second != QuotaObservationSkipped {
		t.Fatalf("expected cold restart duplicate skip: result=%s err=%v", second, err)
	}
}

func openQuotaObservationRepositoryDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "quota-observations.db")})
	if err != nil {
		t.Fatalf("OpenDatabase returned error: %v", err)
	}
	closeTestDatabase(t, db)
	return db
}

func quotaObservationPricingResolver(t *testing.T, includePrice bool) pricing.Resolver {
	t.Helper()
	configs := []pricing.ModelConfig{}
	if includePrice {
		one := 1.0
		configs = append(configs, pricing.ModelConfig{Pricing: entities.ModelPriceSetting{
			Model:                "priced",
			PricingStyle:         entities.ModelPricingStyleOpenAI,
			PromptPricePer1M:     10,
			CompletionPricePer1M: 20,
			CacheReadPricePer1M:  1,
			CacheWritePricePer1M: 3,
			PriceMultiplier:      &one,
		}})
	}
	snapshot, err := pricing.CompileSnapshot(configs)
	if err != nil {
		t.Fatalf("CompileSnapshot returned error: %v", err)
	}
	return pricing.NewCatalog(snapshot).NewResolver()
}

func quotaObservationRepositoryRow(observedAt time.Time, resetAt time.Time) entities.QuotaObservation {
	return entities.QuotaObservation{
		UsageIdentityID:     1,
		AuthType:            "oauth",
		AuthIndex:           "auth-1",
		Provider:            "codex",
		WindowKindID:        "codex/overall/rate_limit/18000",
		QuotaKey:            "rate_limit.primary_window",
		Scope:               "window",
		WindowRole:          "primary",
		WindowSeconds:       int64TestPointer(18000),
		ObservedAt:          observedAt,
		Source:              "manual",
		UsedPercent:         float64TestPointer(20),
		PercentSource:       "reported",
		ResetAt:             &resetAt,
		PricingSnapshotHash: "hash",
		CreatedAt:           observedAt,
	}
}

func int64TestPointer(value int64) *int64 {
	return &value
}

func float64TestPointer(value float64) *float64 {
	return &value
}
