package estimate_test

import (
	"encoding/json"
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota/estimate"
)

var testBaseTime = time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

func TestKnownCapacityRoundedAndSubIntegerResolution(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name           string
		observations   []entities.QuotaObservation
		wantResolution float64
		wantMarginal   int64
		wantAt100      int64
	}{
		{
			name:           "rounded percent",
			observations:   linearSeries(10, 100, 10, 5),
			wantResolution: 5,
			wantMarginal:   2000,
			wantAt100:      1800,
		},
		{
			name:           "sub-integer percent",
			observations:   linearSeries(121, 100, 10, 0.25),
			wantResolution: 0.25,
			wantMarginal:   40000,
			wantAt100:      36000,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			results := defaultEstimator().EstimateWindows(testCase.observations, testBaseTime.Add(time.Hour))
			result := requireSingleEstimate(t, results)
			if !closeEnough(result.PercentResolution, testCase.wantResolution, 1e-9) {
				t.Fatalf("percent resolution = %v, want %v", result.PercentResolution, testCase.wantResolution)
			}
			if result.MarginalTokensPer100 == nil || *result.MarginalTokensPer100 != testCase.wantMarginal {
				t.Fatalf("marginal tokens = %v, want %d", result.MarginalTokensPer100, testCase.wantMarginal)
			}
			if result.TokensAt100 == nil || *result.TokensAt100 != testCase.wantAt100 {
				t.Fatalf("tokens at 100 = %v, want %d", result.TokensAt100, testCase.wantAt100)
			}
			if result.Confidence != estimate.ConfidenceHigh {
				t.Fatalf("confidence = %q, want high; flags=%v", result.Confidence, result.Flags)
			}
			if result.RSquared == nil || *result.RSquared < 0.999999 {
				t.Fatalf("R-squared = %v, want diagnostic near one", result.RSquared)
			}
		})
	}
}

func TestBootstrapAndPermutationAreDeterministic(t *testing.T) {
	t.Parallel()
	observations := linearSeries(12, 100, 5, 4)
	first := defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour))
	permuted := append([]entities.QuotaObservation(nil), observations...)
	rand.New(rand.NewSource(73)).Shuffle(len(permuted), func(left int, right int) {
		permuted[left], permuted[right] = permuted[right], permuted[left]
	})
	second := defaultEstimator().EstimateWindows(permuted, testBaseTime.Add(time.Hour))
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first estimate: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second estimate: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("permuted input changed output\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
	result := requireSingleEstimate(t, first)
	if result.TokensCI95 == nil || result.TokensCI95.High == nil || result.TokensCI95.UnboundedHigh {
		t.Fatalf("stable series should have a finite CI, got %+v", result.TokensCI95)
	}
	if estimate.DefaultBootstrapReplicates != 1000 ||
		estimate.BootstrapPRNGAlgorithm != "pcg32_splitmix64_v1" ||
		estimate.BootstrapPercentileInterpolation != "linear_order_statistics_v1" {
		t.Fatal("bootstrap randomness controls changed")
	}
}

func TestUnstableBootstrapAndSplitHalfStayLow(t *testing.T) {
	t.Parallel()
	observations := linearSeries(12, 100, 5, 4)
	percents := []float64{5, 6, 7, 8, 9, 10, 26, 34, 42, 50, 58, 66}
	for index := range observations {
		observations[index].UsedPercent = floatPointer(percents[index])
	}
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if result.Confidence != estimate.ConfidenceLow {
		t.Fatalf("unstable series confidence = %q, want low", result.Confidence)
	}
	if result.SlopeInstability == nil || *result.SlopeInstability <= estimate.MediumMaximumSlopeInstability {
		t.Fatalf("slope instability = %v, want above medium threshold", result.SlopeInstability)
	}
}

func TestBootstrapReportsUnboundedCapacityWhenLowerSlopeCrossesZero(t *testing.T) {
	t.Parallel()
	observations := percentSeries(
		[]float64{10, 20, 5, 25, 10, 30, 15, 35, 20, 40, 25, 45},
		100,
	)
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if result.TokensCI95 == nil ||
		!result.TokensCI95.UnboundedHigh ||
		result.TokensCI95.High != nil {
		t.Fatalf("capacity interval = %+v, want unbounded high", result.TokensCI95)
	}
	if result.Confidence != estimate.ConfidenceLow {
		t.Fatalf("confidence = %q, want low for unbounded interval", result.Confidence)
	}
}

func TestDegenerateSeriesAreInsufficient(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		observations []entities.QuotaObservation
	}{
		{name: "empty", observations: nil},
		{name: "too few", observations: linearSeries(3, 100, 10, 5)},
		{name: "flat percent", observations: percentSeries([]float64{10, 10, 10, 10, 10}, 100)},
		{name: "nonpositive slope", observations: percentSeries([]float64{30, 25, 20, 15, 10}, 100)},
		{name: "low span", observations: percentSeries([]float64{10, 12, 14, 16, 18}, 100)},
		{name: "heartbeat only", observations: heartbeatSeries(8)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			results := defaultEstimator().EstimateWindows(testCase.observations, testBaseTime.Add(time.Hour))
			if testCase.name == "empty" {
				if len(results) != 0 {
					t.Fatalf("empty input produced %d estimates", len(results))
				}
				return
			}
			result := requireSingleEstimate(t, results)
			if result.Confidence != estimate.ConfidenceInsufficient {
				t.Fatalf("confidence = %q, want insufficient", result.Confidence)
			}
			if result.TokensAt100 != nil {
				t.Fatalf("insufficient estimate exposed tokens at 100: %v", *result.TokensAt100)
			}
		})
	}
}

func TestObservationsWithoutCanonicalResetAreExcluded(t *testing.T) {
	t.Parallel()
	observations := linearSeries(8, 100, 10, 5)
	for index := range observations {
		observations[index].ResetAt = nil
		observations[index].ResetAfterSeconds = nil
	}
	if results := defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)); len(results) != 0 {
		t.Fatalf("observations without canonical reset produced estimates: %+v", results)
	}
}

func TestStudentizedOutlierIsClassifiedAndExcludedWithoutSeriesBreak(t *testing.T) {
	t.Parallel()
	clean := linearSeries(20, 100, 10, 4)
	withOutlier := linearSeries(20, 100, 10, 4)
	outlierIndex := 10
	withOutlier[outlierIndex].UsedPercent = floatPointer(0)

	cleanResult := requireSingleEstimate(t, defaultEstimator().EstimateWindows(clean, testBaseTime.Add(time.Hour)))
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(withOutlier, testBaseTime.Add(time.Hour)))
	diagnostic := diagnosticByID(t, result, withOutlier[outlierIndex].ID)
	if diagnostic.Class != estimate.PointOutlier {
		t.Fatalf("outlier diagnostic = %+v, want outlier", diagnostic)
	}
	for _, point := range result.Points {
		if point.Class == estimate.PointPreBreak {
			t.Fatalf("transient outlier caused a series break: %+v", result.Points)
		}
	}
	if result.TokensAt100 == nil || cleanResult.TokensAt100 == nil ||
		*result.TokensAt100 != *cleanResult.TokensAt100 {
		t.Fatalf("outlier exclusion changed fitted capacity: clean=%v outlier=%v", cleanResult.TokensAt100, result.TokensAt100)
	}
}

func TestAllowlistUsesCanonicalIdentityAndNeverMixesCredentials(t *testing.T) {
	t.Parallel()
	first := linearSeries(6, 100, 10, 5)
	for index := range first {
		if index%2 == 0 {
			first[index].QuotaKey = "rate_limit.primary_window"
			first[index].WindowRole = "primary"
		} else {
			first[index].QuotaKey = "rate_limit.secondary_window"
			first[index].WindowRole = "secondary"
		}
	}
	feature := linearSeries(6, 100, 10, 5)
	for index := range feature {
		feature[index].ID += 100
		feature[index].WindowKindID = "codex/code_review/code_review_rate_limit/18000"
	}
	second := linearSeries(6, 200, 10, 5)
	for index := range second {
		second[index].ID += 200
		second[index].UsageIdentityID = 2
		second[index].AuthIndex = "credential-2"
	}
	combined := append(append(first, feature...), second...)
	results := defaultEstimator().EstimateWindows(combined, testBaseTime.Add(time.Hour))
	if len(results) != 2 {
		t.Fatalf("estimate count = %d, want two credentials and no feature quota", len(results))
	}
	if results[0].AuthIndex == results[1].AuthIndex {
		t.Fatalf("credentials were mixed: %+v", results)
	}
	if results[0].WindowKindID != estimate.WindowKindCodexFiveHour ||
		results[1].WindowKindID != estimate.WindowKindCodexFiveHour {
		t.Fatalf("unexpected window identities: %+v", results)
	}
}

func TestIdentityUnverifiedIsExplicit(t *testing.T) {
	t.Parallel()
	observations := linearSeries(8, 100, 10, 5)
	for index := range observations {
		observations[index].Provider = "claude"
		observations[index].WindowKindID = estimate.WindowKindClaudeFiveHour
		observations[index].AccountID = nil
		observations[index].PlanType = nil
	}
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if !slices.Contains(result.Flags, estimate.FlagIdentityUnverified) {
		t.Fatalf("flags = %v, want identity_unverified", result.Flags)
	}
	if result.Confidence == estimate.ConfidenceHigh {
		t.Fatal("identity-unverified estimate must not be high confidence")
	}
}

func defaultEstimator() estimate.Estimator {
	return estimate.New(estimate.DefaultConfig())
}

func linearSeries(count int, tokenStep int64, intercept float64, percentStep float64) []entities.QuotaObservation {
	percents := make([]float64, count)
	for index := range percents {
		percents[index] = intercept + float64(index)*percentStep
	}
	return seriesWithTokens(percents, tokenStep)
}

func percentSeries(percents []float64, tokenStep int64) []entities.QuotaObservation {
	return seriesWithTokens(percents, tokenStep)
}

func seriesWithTokens(percents []float64, tokenStep int64) []entities.QuotaObservation {
	resetAt := testBaseTime.Add(5 * time.Hour)
	resetRaw := resetAt.Format(time.RFC3339Nano)
	accountID := "account-1"
	planType := "plus"
	seconds := int64(5 * time.Hour / time.Second)
	observations := make([]entities.QuotaObservation, len(percents))
	for index, percent := range percents {
		tokens := int64(index) * tokenStep
		input := tokens * 6 / 10
		output := tokens * 2 / 10
		cacheRead := tokens - input - output
		cacheCreation := int64(0)
		cost := float64(tokens) / 1000
		observations[index] = entities.QuotaObservation{
			ID:                            int64(index + 1),
			UsageIdentityID:               1,
			AuthType:                      "oauth",
			AuthIndex:                     "credential-1",
			AccountID:                     &accountID,
			PlanType:                      &planType,
			Provider:                      "codex",
			WindowKindID:                  estimate.WindowKindCodexFiveHour,
			QuotaKey:                      "rate_limit.primary_window",
			Scope:                         "window",
			WindowRole:                    "primary",
			WindowSeconds:                 &seconds,
			ObservedAt:                    testBaseTime.Add(time.Duration(index) * 10 * time.Minute),
			UsedPercent:                   floatPointer(percent),
			PercentSource:                 "reported",
			ResetAt:                       timePointer(resetAt),
			ResetRaw:                      &resetRaw,
			AttributedTokens:              int64Pointer(tokens),
			AttributedInputTokens:         int64Pointer(input),
			AttributedOutputTokens:        int64Pointer(output),
			AttributedCacheReadTokens:     int64Pointer(cacheRead),
			AttributedCacheCreationTokens: int64Pointer(cacheCreation),
			AttributedCostUSD:             floatPointer(cost),
			AttributedCostComplete:        true,
			PricingSnapshotHash:           "pricing-a",
		}
	}
	return observations
}

func heartbeatSeries(count int) []entities.QuotaObservation {
	observations := linearSeries(count, 0, 10, 0)
	for index := range observations {
		observations[index].AttributedTokens = int64Pointer(100)
		observations[index].AttributedInputTokens = int64Pointer(60)
		observations[index].AttributedOutputTokens = int64Pointer(20)
		observations[index].AttributedCacheReadTokens = int64Pointer(20)
	}
	return observations
}

func requireSingleEstimate(t *testing.T, values []estimate.WindowEstimate) estimate.WindowEstimate {
	t.Helper()
	if len(values) != 1 {
		t.Fatalf("estimate count = %d, want 1: %+v", len(values), values)
	}
	return values[0]
}

func closeEnough(left float64, right float64, tolerance float64) bool {
	return math.Abs(left-right) <= tolerance
}

func floatPointer(value float64) *float64 {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}

func timePointer(value time.Time) *time.Time {
	return &value
}
