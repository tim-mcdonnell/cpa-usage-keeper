package quota

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"

	"github.com/sirupsen/logrus"
)

func TestQuotaWindowKindIDIsRoleIndependentAndUsesStableV1Components(t *testing.T) {
	seconds := int64(7 * 24 * 60 * 60)
	primary := QuotaRow{
		Key:        "rate_limit.primary_window",
		Scope:      "window",
		WindowRole: "primary",
		Window:     &QuotaWindow{Seconds: &seconds},
	}
	secondary := primary
	secondary.Key = "rate_limit.secondary_window"
	secondary.WindowRole = "secondary"

	if got, want := quotaWindowKindID("Codex", primary), "codex/overall/rate_limit/604800"; got != want {
		t.Fatalf("primary window kind id = %q, want %q", got, want)
	}
	if got := quotaWindowKindID("codex", secondary); got != quotaWindowKindID("codex", primary) {
		t.Fatalf("role changed canonical identity: primary=%q secondary=%q", quotaWindowKindID("codex", primary), got)
	}
	claude := QuotaRow{Key: "five_hour", Scope: "window", Window: &QuotaWindow{Seconds: int64Pointer(18000)}}
	if got, want := quotaWindowKindID("claude", claude), "claude/overall/five_hour/18000"; got != want {
		t.Fatalf("Claude window kind id = %q, want %q", got, want)
	}
	unknown := QuotaRow{Scope: "feature name"}
	if got, want := quotaWindowKindID("Provider Name", unknown), "provider_name/feature_name/none/0"; got != want {
		t.Fatalf("unknown window kind id = %q, want %q", got, want)
	}
	if got, want := quotaWindowKindID("", QuotaRow{}), "unknown_provider/overall/none/0"; got != want {
		t.Fatalf("empty components window kind id = %q, want %q", got, want)
	}
}

func TestQuotaObservationPreservesRawValuesAndCredentialIncarnation(t *testing.T) {
	observedAt := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	accountID := "account-1"
	identityPlan := "old-plan"
	identity := entities.UsageIdentity{
		ID:           7,
		AuthType:     entities.UsageIdentityAuthTypeAuthFile,
		AuthTypeName: "oauth",
		Identity:     "auth-1",
		AccountID:    &accountID,
		PlanType:     &identityPlan,
	}
	row := QuotaRow{
		Key:               "rate_limit.primary_window",
		Scope:             "window",
		PlanType:          "plus",
		Used:              float64Pointer(10),
		Limit:             float64Pointer(100),
		Remaining:         float64Pointer(90),
		UsedPercent:       float64Pointer(10),
		RemainingFraction: float64Pointer(0.9),
		PercentSource:     QuotaPercentSourceReported,
		ResetAt:           "not-a-time",
		ResetRaw:          "  raw-provider-reset  ",
		ResetAfterSeconds: int64Pointer(3600),
		WindowUsageTokens: int64Pointer(123),
		WindowUsageCost:   float64Pointer(4.5),
		Window:            &QuotaWindow{Seconds: int64Pointer(18000)},
	}
	reading := newQuotaReading(identity, "codex", RefreshSourceManual, observedAt, []QuotaRow{row})
	observation := newQuotaObservation(reading, "oauth", reading.rows[0])

	if observation.AccountID == nil || *observation.AccountID != accountID ||
		observation.PlanType == nil || *observation.PlanType != "plus" {
		t.Fatalf("unexpected incarnation snapshot: %+v", observation)
	}
	if observation.ResetRaw == nil || *observation.ResetRaw != "  raw-provider-reset  " {
		t.Fatalf("raw reset was not preserved verbatim: %+v", observation.ResetRaw)
	}
	wantResetAt := observedAt.Add(time.Hour)
	if observation.ResetAt == nil || !observation.ResetAt.Equal(wantResetAt) {
		t.Fatalf("expected reset-after normalization %v, got %v", wantResetAt, observation.ResetAt)
	}
	if observation.ProviderWindowTokens == nil || *observation.ProviderWindowTokens != 123 ||
		observation.ProviderWindowCost == nil || *observation.ProviderWindowCost != 4.5 {
		t.Fatalf("provider utilization provenance changed: %+v", observation)
	}
	if observation.AttributedTokens != nil || observation.AttributedCostUSD != nil {
		t.Fatalf("new observation should distinguish not-computed attribution from zero: %+v", observation)
	}
}

func TestQuotaReadingOwnsAnImmutableDeepCopy(t *testing.T) {
	accountID := "account-before"
	planType := "plan-before"
	usedPercent := 10.0
	windowSeconds := int64(18000)
	identity := entities.UsageIdentity{
		ID:        7,
		Identity:  "auth-before",
		AccountID: &accountID,
		PlanType:  &planType,
	}
	rows := []QuotaRow{{
		Key:         "five_hour",
		UsedPercent: &usedPercent,
		Window:      &QuotaWindow{Seconds: &windowSeconds},
	}}

	reading := newQuotaReading(identity, "claude", RefreshSourceManual, time.Now(), rows)
	accountID = "account-after"
	planType = "plan-after"
	usedPercent = 99
	windowSeconds = 1
	rows[0].Key = "changed"

	if reading.identity.AccountID == nil || *reading.identity.AccountID != "account-before" ||
		reading.identity.PlanType == nil || *reading.identity.PlanType != "plan-before" ||
		reading.rows[0].Key != "five_hour" ||
		reading.rows[0].UsedPercent == nil || *reading.rows[0].UsedPercent != 10 ||
		reading.rows[0].Window == nil || reading.rows[0].Window.Seconds == nil || *reading.rows[0].Window.Seconds != 18000 {
		t.Fatalf("producer mutation changed immutable reading: %+v", reading)
	}
}

func TestQuotaObservationAuthTypeNormalizesLegacyIdentityLabels(t *testing.T) {
	testCases := []struct {
		identity entities.UsageIdentity
		want     string
	}{
		{identity: entities.UsageIdentity{AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: " auth_file "}, want: "oauth"},
		{identity: entities.UsageIdentity{AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "api_key"}, want: "apikey"},
		{identity: entities.UsageIdentity{AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: " OAUTH "}, want: "oauth"},
	}
	for _, testCase := range testCases {
		if got := quotaObservationAuthType(testCase.identity); got != testCase.want {
			t.Fatalf("quotaObservationAuthType(%+v) = %q, want %q", testCase.identity, got, testCase.want)
		}
	}
}

func TestNormalizeQuotaRowsCarriesRawResetAndPercentProvenance(t *testing.T) {
	resetEpoch := int64(1_785_000_123)
	codexRows := NormalizeQuotaRows(ProviderOutput{Result: CodexResult{Usage: &CodexUsagePayload{
		RateLimit: &CodexRateLimitInfo{PrimaryWindow: &CodexUsageWindow{
			UsedPercent:        12.5,
			LimitWindowSeconds: 18000,
			ResetAt:            resetEpoch,
		}},
	}}})
	if len(codexRows) != 1 ||
		codexRows[0].ResetRaw != "1785000123" ||
		codexRows[0].PercentSource != QuotaPercentSourceReported ||
		codexRows[0].WindowRole != "primary" {
		t.Fatalf("unexpected Codex observation provenance: %+v", codexRows)
	}

	remaining := 0.75
	antigravityRows := NormalizeQuotaRows(ProviderOutput{Result: AntigravityResult{Quota: &AntigravityQuotaPayload{
		Groups: []AntigravityQuotaGroup{{
			DisplayName: "Main",
			Buckets: []AntigravityQuotaBucket{{
				BucketID:          "bucket",
				Window:            "5h",
				RemainingFraction: &remaining,
				ResetTime:         "provider-verbatim-reset",
			}},
		}},
	}}})
	if len(antigravityRows) != 1 ||
		antigravityRows[0].UsedPercent == nil ||
		*antigravityRows[0].UsedPercent != 25 ||
		antigravityRows[0].PercentSource != QuotaPercentSourceRemainingFraction ||
		antigravityRows[0].ResetRaw != "provider-verbatim-reset" {
		t.Fatalf("unexpected remaining-fraction provenance: %+v", antigravityRows)
	}
}

func TestQuotaObservationRecorderPolicyUsesCheapGatesBeforeAttribution(t *testing.T) {
	store := &quotaObservationStoreStub{
		maxUsageEventID: 1,
		attribution: repository.QuotaAttributedUsage{
			CostComplete:        true,
			PricingSnapshotHash: "snapshot",
		},
	}
	recorder := &quotaObservationRecorder{store: store, pricing: pricing.NewCatalog(pricing.EmptySnapshot())}
	states := make(map[quotaObservationSeriesKey]quotaObservationRecordedState)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	resetAt := start.Add(5 * time.Hour)

	recorder.record(context.Background(), quotaObservationTestReading(start, resetAt, 10), states)
	recorder.record(context.Background(), quotaObservationTestReading(start.Add(time.Minute), resetAt, 10), states)
	recorder.record(context.Background(), quotaObservationTestReading(start.Add(2*time.Minute), resetAt, 11), states)
	recorder.record(context.Background(), quotaObservationTestReading(start.Add(5*time.Minute), resetAt, 11), states)

	store.setMaxUsageEventID(2)
	recorder.record(context.Background(), quotaObservationTestReading(start.Add(10*time.Minute), resetAt, 11), states)
	recorder.record(context.Background(), quotaObservationTestReading(start.Add(15*time.Minute), resetAt, 11), states)
	recorder.record(context.Background(), quotaObservationTestReading(start.Add(40*time.Minute), resetAt, 11), states)
	recorder.record(context.Background(), quotaObservationTestReading(start.Add(41*time.Minute), resetAt.Add(5*time.Hour), 0), states)

	attributionCalls, insertCalls := store.counts()
	if attributionCalls != 5 || insertCalls != 5 {
		t.Fatalf("expected only accepted readings to query attribution and insert, attribution=%d insert=%d", attributionCalls, insertCalls)
	}
	inserted := store.insertedRows()
	if len(inserted) != 5 {
		t.Fatalf("expected five inserted observations, got %+v", inserted)
	}
	wantTimes := []time.Time{
		start,
		start.Add(5 * time.Minute),
		start.Add(10 * time.Minute),
		start.Add(40 * time.Minute),
		start.Add(41 * time.Minute),
	}
	for index, want := range wantTimes {
		if !inserted[index].ObservedAt.Equal(want) {
			t.Fatalf("insert %d observed_at = %v, want %v", index, inserted[index].ObservedAt, want)
		}
	}
	if inserted[0].AttributedTokens == nil || *inserted[0].AttributedTokens != 0 {
		t.Fatalf("computed zero attribution must be stored as zero, got %+v", inserted[0].AttributedTokens)
	}
}

func TestQuotaObservationRecorderSkipsAttributionForNonEstimableWindows(t *testing.T) {
	store := &quotaObservationStoreStub{maxUsageEventID: 1}
	recorder := &quotaObservationRecorder{store: store, pricing: pricing.NewCatalog(pricing.EmptySnapshot())}
	states := make(map[quotaObservationSeriesKey]quotaObservationRecordedState)
	reading := quotaObservationTestReading(
		time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC),
		10,
	)
	reading.rows[0].Key = "code_review_rate_limit.primary_window"
	reading.rows[0].Scope = "code_review"

	recorder.record(context.Background(), reading, states)

	attributionCalls, insertCalls := store.counts()
	if attributionCalls != 0 || insertCalls != 1 {
		t.Fatalf("expected non-estimable window to insert without attribution, attribution=%d insert=%d", attributionCalls, insertCalls)
	}
	inserted := store.insertedRows()[0]
	if inserted.AttributedTokens != nil || inserted.AttributedCostUSD != nil {
		t.Fatalf("non-estimable attribution must remain null, got %+v", inserted)
	}
}

func TestQuotaObservationQueueDropsOldestWithoutBlockingProducer(t *testing.T) {
	previousOutput := logrus.StandardLogger().Out
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	t.Cleanup(func() { logrus.SetOutput(previousOutput) })

	store := &quotaObservationStoreStub{
		maxUsageEventID: 1,
		attribution: repository.QuotaAttributedUsage{
			CostComplete:        true,
			PricingSnapshotHash: "snapshot",
		},
		attributionEntered: make(chan struct{}, 1),
		attributionBlock:   make(chan struct{}),
	}
	recorder := newQuotaObservationRecorder(store, pricing.NewCatalog(pricing.EmptySnapshot()), 1)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	resetAt := start.Add(5 * time.Hour)
	recorder.enqueue(quotaObservationTestReading(start, resetAt, 10))
	select {
	case <-store.attributionEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for recorder to enter attribution")
	}

	enqueueStarted := time.Now()
	recorder.enqueue(quotaObservationTestReading(start.Add(5*time.Minute), resetAt, 11))
	recorder.enqueue(quotaObservationTestReading(start.Add(10*time.Minute), resetAt, 12))
	if elapsed := time.Since(enqueueStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("queue overflow blocked producer for %s", elapsed)
	}
	close(store.attributionBlock)
	recorder.stop()

	inserted := store.insertedRows()
	if len(inserted) != 2 ||
		!inserted[0].ObservedAt.Equal(start) ||
		!inserted[1].ObservedAt.Equal(start.Add(10*time.Minute)) {
		t.Fatalf("expected oldest queued reading to be dropped, got %+v", inserted)
	}
	if recorder.dropped.Load() != 1 || !strings.Contains(logs.String(), "dropped_count=1") {
		t.Fatalf("expected logged drop count, dropped=%d logs=%s", recorder.dropped.Load(), logs.String())
	}
}

func TestQuotaObservationRecorderInsertFailureIsAsynchronousAndLogged(t *testing.T) {
	previousOutput := logrus.StandardLogger().Out
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	t.Cleanup(func() { logrus.SetOutput(previousOutput) })

	store := &quotaObservationStoreStub{
		maxUsageEventID: 1,
		attribution: repository.QuotaAttributedUsage{
			CostComplete:        true,
			PricingSnapshotHash: "snapshot",
		},
		insertErr: errors.New("forced insert failure"),
	}
	recorder := newQuotaObservationRecorder(store, pricing.NewCatalog(pricing.EmptySnapshot()), 1)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	enqueueStarted := time.Now()
	recorder.enqueue(quotaObservationTestReading(start, start.Add(5*time.Hour), 10))
	if elapsed := time.Since(enqueueStarted); elapsed > 100*time.Millisecond {
		t.Fatalf("insert failure blocked producer for %s", elapsed)
	}
	recorder.stop()
	if !strings.Contains(logs.String(), "quota observation recording failed") ||
		!strings.Contains(logs.String(), "forced insert failure") {
		t.Fatalf("expected structured insert failure log, got %s", logs.String())
	}
}

func TestQuotaObservationRecorderLogsDailyLimitRefusal(t *testing.T) {
	previousOutput := logrus.StandardLogger().Out
	var logs bytes.Buffer
	logrus.SetOutput(&logs)
	t.Cleanup(func() { logrus.SetOutput(previousOutput) })

	store := &quotaObservationStoreStub{
		maxUsageEventID: 1,
		attribution: repository.QuotaAttributedUsage{
			CostComplete:        true,
			PricingSnapshotHash: "snapshot",
		},
		insertResult: repository.QuotaObservationDailyLimit,
	}
	recorder := &quotaObservationRecorder{store: store, pricing: pricing.NewCatalog(pricing.EmptySnapshot())}
	states := make(map[quotaObservationSeriesKey]quotaObservationRecordedState)
	start := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	recorder.record(context.Background(), quotaObservationTestReading(start, start.Add(5*time.Hour), 10), states)

	if !strings.Contains(logs.String(), "quota observation daily limit reached") ||
		!strings.Contains(logs.String(), "daily_limit=400") {
		t.Fatalf("expected logged daily limit refusal, got %s", logs.String())
	}
}

func TestObservationSeriesValidationRequiresBoundedNinetyDayRange(t *testing.T) {
	service := &Service{}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	testCases := []ObservationSeriesRequest{
		{WindowKindID: "kind", Start: start, End: start.Add(time.Hour)},
		{AuthIndex: "auth-1", Start: start, End: start.Add(time.Hour)},
		{AuthIndex: "auth-1", WindowKindID: "kind", Start: start.Add(time.Hour), End: start},
		{AuthIndex: "auth-1", WindowKindID: "kind", Start: start, End: start.Add(90*24*time.Hour + time.Nanosecond)},
	}
	for _, request := range testCases {
		if _, err := service.ListObservations(context.Background(), request); !errors.Is(err, ErrValidation) {
			t.Fatalf("expected validation error for %+v, got %v", request, err)
		}
	}
}

func quotaObservationTestReading(observedAt time.Time, resetAt time.Time, usedPercent float64) QuotaReading {
	return newQuotaReading(
		entities.UsageIdentity{
			ID:           1,
			AuthType:     entities.UsageIdentityAuthTypeAuthFile,
			AuthTypeName: "oauth",
			Identity:     "auth-1",
		},
		"codex",
		RefreshSourceManual,
		observedAt,
		[]QuotaRow{{
			Key:           "rate_limit.primary_window",
			Scope:         "window",
			WindowRole:    "primary",
			Window:        &QuotaWindow{Seconds: int64Pointer(18000)},
			UsedPercent:   float64Pointer(usedPercent),
			PercentSource: QuotaPercentSourceReported,
			ResetAt:       resetAt.Format(time.RFC3339Nano),
			ResetRaw:      resetAt.Format(time.RFC3339Nano),
		}},
	)
}

type quotaObservationStoreStub struct {
	mu sync.Mutex

	maxUsageEventID  int64
	attribution      repository.QuotaAttributedUsage
	attributionCalls int
	insertCalls      int
	inserted         []entities.QuotaObservation
	insertErr        error
	insertResult     repository.QuotaObservationInsertResult

	attributionEntered chan struct{}
	attributionBlock   chan struct{}
}

func (s *quotaObservationStoreStub) MaxUsageEventID(context.Context, string, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxUsageEventID, nil
}

func (s *quotaObservationStoreStub) SumAttributedUsage(
	context.Context,
	string,
	string,
	time.Time,
	time.Time,
	pricing.Resolver,
) (repository.QuotaAttributedUsage, error) {
	s.mu.Lock()
	s.attributionCalls++
	entered := s.attributionEntered
	block := s.attributionBlock
	attribution := s.attribution
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	return attribution, nil
}

func (s *quotaObservationStoreStub) InsertIfDue(
	_ context.Context,
	observation entities.QuotaObservation,
	_ time.Duration,
	_ int64,
) (repository.QuotaObservationInsertResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insertCalls++
	if s.insertErr != nil {
		return "", s.insertErr
	}
	if s.insertResult != "" {
		return s.insertResult, nil
	}
	s.inserted = append(s.inserted, observation)
	return repository.QuotaObservationInserted, nil
}

func (s *quotaObservationStoreStub) setMaxUsageEventID(value int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxUsageEventID = value
}

func (s *quotaObservationStoreStub) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attributionCalls, s.insertCalls
}

func (s *quotaObservationStoreStub) insertedRows() []entities.QuotaObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]entities.QuotaObservation(nil), s.inserted...)
}
