package estimate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota/estimate"
)

type goldenScenario struct {
	Name      string           `json:"name"`
	Estimates []goldenEstimate `json:"estimates"`
}

type goldenEstimate struct {
	EpochResetAt         *string                     `json:"epoch_reset_at"`
	Confidence           estimate.Confidence         `json:"confidence"`
	Flags                []estimate.Flag             `json:"flags"`
	SampleCount          int                         `json:"sample_count"`
	EffectiveSamples     int                         `json:"effective_samples"`
	DistinctPercents     int                         `json:"distinct_percents"`
	PercentResolution    float64                     `json:"percent_resolution"`
	PercentSpan          float64                     `json:"percent_span"`
	MarginalTokensPer100 *int64                      `json:"marginal_tokens_per_100"`
	TokensAt100          *int64                      `json:"tokens_at_100"`
	TokensCI95           goldenInterval              `json:"tokens_ci_95"`
	CostAt100            *float64                    `json:"cost_at_100"`
	CostSegment          *goldenSegment              `json:"cost_segment"`
	SlopeInstability     *float64                    `json:"slope_instability"`
	PointClassCounts     map[estimate.PointClass]int `json:"point_class_counts"`
	NonzeroOffsets       []goldenOffset              `json:"nonzero_offsets"`
}

type goldenInterval struct {
	Low           *float64 `json:"low"`
	High          *float64 `json:"high"`
	UnboundedHigh bool     `json:"unbounded_high"`
}

type goldenSegment struct {
	Hash  string `json:"hash"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type goldenOffset struct {
	ObservationID int64   `json:"observation_id"`
	Offset        float64 `json:"offset"`
}

type syntheticScenario struct {
	name         string
	observations []entities.QuotaObservation
}

func TestSyntheticCapacityGoldenDatasets(t *testing.T) {
	scenarios := syntheticGoldenScenarios()
	actual := make([]goldenScenario, 0, len(scenarios))
	for _, scenario := range scenarios {
		results := defaultEstimator().EstimateWindows(scenario.observations, testBaseTime.Add(time.Hour))
		record := goldenScenario{
			Name:      scenario.name,
			Estimates: make([]goldenEstimate, 0, len(results)),
		}
		for _, result := range results {
			record.Estimates = append(record.Estimates, projectEstimate(result))
		}
		actual = append(actual, record)
	}
	actualJSON, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden estimates: %v", err)
	}
	actualJSON = append(actualJSON, '\n')
	goldenPath := filepath.Join("testdata", "synthetic_capacity.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, actualJSON, 0o644); err != nil {
			t.Fatalf("update %s: %v", goldenPath, err)
		}
	}
	expectedJSON, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	if string(actualJSON) != string(expectedJSON) {
		t.Fatalf("golden mismatch for %s\nactual:\n%s", goldenPath, actualJSON)
	}
}

func syntheticGoldenScenarios() []syntheticScenario {
	stable := linearSeries(10, 100, 10, 5)
	subInteger := linearSeries(121, 100, 10, 0.25)

	unstable := linearSeries(12, 100, 5, 4)
	unstablePercents := []float64{5, 6, 7, 8, 9, 10, 26, 34, 42, 50, 58, 66}
	for index := range unstable {
		unstable[index].UsedPercent = floatPointer(unstablePercents[index])
	}

	coverage := linearSeries(10, 100, 10, 5)
	coverage[4].AttributedTokens = int64Pointer(300)
	setComposition(&coverage[4], 300, 180, 60, 60, 0)
	coverage[4].AttributedCostUSD = floatPointer(0.3)
	for index := 4; index < len(coverage); index++ {
		raw := *coverage[index].UsedPercent + 10
		if index == 4 {
			raw = 35
		}
		coverage[index].UsedPercent = &raw
	}

	coverageSuppressed := linearSeries(11, 100, 10, 5)
	coverageTokens := []int64{0, 0, 100, 100, 200, 200, 300, 300, 400, 500, 600}
	for index := range coverageSuppressed {
		coverageSuppressed[index].AttributedTokens = int64Pointer(coverageTokens[index])
		setComposition(
			&coverageSuppressed[index],
			coverageTokens[index],
			coverageTokens[index],
			0,
			0,
			0,
		)
	}

	residualCoverage := linearSeries(12, 100, 10, 5)
	for index := 6; index < len(residualCoverage); index++ {
		percent := *residualCoverage[index].UsedPercent + 12
		residualCoverage[index].UsedPercent = &percent
	}

	cleanNoise := percentSeries(
		[]float64{10, 15, 21, 25, 30, 36, 40, 45, 51, 55, 60, 66},
		100,
	)

	concurrent := linearSeries(10, 100, 10, 5)
	for index := 5; index < len(concurrent); index++ {
		percent := *concurrent[index].UsedPercent + float64(index-4)*3
		concurrent[index].UsedPercent = &percent
	}

	absoluteJitter := linearSeries(4, 100, 10, 5)
	absoluteAnchor := *absoluteJitter[0].ResetAt
	for index := range absoluteJitter {
		reset := absoluteAnchor.Add(time.Duration(index%2) * estimate.AbsoluteResetTolerance)
		absoluteJitter[index].ResetAt = &reset
		absoluteJitter[index].ResetRaw = stringPointer(reset.Format(time.RFC3339Nano))
	}
	absoluteBeyond := absoluteAnchor.Add(estimate.AbsoluteResetTolerance + time.Second)
	absoluteJitter[len(absoluteJitter)-1].ResetAt = &absoluteBeyond
	absoluteJitter[len(absoluteJitter)-1].ResetRaw = stringPointer(absoluteBeyond.Format(time.RFC3339Nano))

	derivedJitter := linearSeries(4, 100, 10, 5)
	derivedAnchor := *derivedJitter[0].ResetAt
	for index := range derivedJitter {
		reset := derivedAnchor
		if index == len(derivedJitter)-1 {
			reset = derivedAnchor.Add(estimate.DerivedResetMinimumTolerance + time.Second)
		}
		derivedJitter[index].ResetAt = &reset
		derivedJitter[index].ResetRaw = nil
		after := int64(reset.Sub(derivedJitter[index].ObservedAt) / time.Second)
		derivedJitter[index].ResetAfterSeconds = &after
	}

	staleReplay := linearSeries(8, 100, 10, 5)
	nextReset := testBaseTime.Add(10 * time.Hour)
	nextRaw := nextReset.Format(time.RFC3339Nano)
	for index := 4; index < len(staleReplay); index++ {
		staleReplay[index].ResetAt = &nextReset
		staleReplay[index].ResetRaw = &nextRaw
	}
	replay := staleReplay[2]
	replay.ID = 99
	replay.ObservedAt = staleReplay[len(staleReplay)-1].ObservedAt.Add(time.Minute)
	staleReplay = append(staleReplay, replay)

	accountBreak := linearSeries(8, 100, 10, 5)
	account := "account-2"
	for index := 4; index < len(accountBreak); index++ {
		accountBreak[index].AccountID = &account
	}
	planBreak := linearSeries(8, 100, 10, 5)
	plan := "pro"
	for index := 4; index < len(planBreak); index++ {
		planBreak[index].PlanType = &plan
	}
	utilizationDrop := percentSeries([]float64{35, 40, 5, 10, 15, 20, 25, 30}, 100)

	pricingQualifying := linearSeries(12, 100, 5, 5)
	for index := 6; index < len(pricingQualifying); index++ {
		pricingQualifying[index].PricingSnapshotHash = "pricing-b"
		cost := float64(*pricingQualifying[index].AttributedTokens) / 500
		pricingQualifying[index].AttributedCostUSD = &cost
	}
	pricingSuppressed := linearSeries(8, 100, 10, 5)
	for index := range pricingSuppressed {
		if index%3 == 0 {
			pricingSuppressed[index].PricingSnapshotHash = "pricing-b"
		}
	}
	unpriced := linearSeries(10, 100, 10, 5)
	unpriced[4].AttributedCostComplete = false

	mixShift := linearSeries(10, 100, 10, 5)
	var input int64
	var output int64
	for index := range mixShift {
		if index < 5 {
			input += 100
		} else {
			output += 100
		}
		tokens := int64(index+1) * 100
		setComposition(&mixShift[index], tokens, input, output, 0, 0)
	}

	resetAmbiguous := linearSeries(8, 100, 10, 5)
	ambiguityAnchor := *resetAmbiguous[0].ResetAt
	for index := 1; index < len(resetAmbiguous); index++ {
		reset := ambiguityAnchor.Add(time.Duration(index%2) * 70 * time.Second)
		resetAmbiguous[index].ResetAt = &reset
		resetAmbiguous[index].ResetRaw = nil
		after := int64(reset.Sub(resetAmbiguous[index].ObservedAt) / time.Second)
		resetAmbiguous[index].ResetAfterSeconds = &after
	}

	noReset := linearSeries(8, 100, 10, 5)
	for index := range noReset {
		noReset[index].ResetAt = nil
		noReset[index].ResetAfterSeconds = nil
	}
	featureQuota := linearSeries(8, 100, 10, 5)
	for index := range featureQuota {
		featureQuota[index].WindowKindID = "codex/code_review/code_review_rate_limit/18000"
	}
	roleChanges := linearSeries(8, 100, 10, 5)
	for index := range roleChanges {
		if index%2 == 0 {
			roleChanges[index].QuotaKey = "rate_limit.primary_window"
			roleChanges[index].WindowRole = "primary"
		} else {
			roleChanges[index].QuotaKey = "rate_limit.secondary_window"
			roleChanges[index].WindowRole = "secondary"
		}
	}
	identityUnverified := linearSeries(8, 100, 10, 5)
	for index := range identityUnverified {
		identityUnverified[index].Provider = "claude"
		identityUnverified[index].WindowKindID = estimate.WindowKindClaudeFiveHour
		identityUnverified[index].AccountID = nil
		identityUnverified[index].PlanType = nil
	}

	return []syntheticScenario{
		{name: "known_rounded_capacity", observations: stable},
		{name: "known_sub_integer_resolution", observations: subInteger},
		{name: "unstable_bootstrap_slope", observations: unstable},
		{name: "zero_coverage_bypass", observations: coverage},
		{name: "coverage_contamination_suppressed", observations: coverageSuppressed},
		{name: "residual_nonzero_coverage_bypass", observations: residualCoverage},
		{name: "residual_clean_model_noise", observations: cleanNoise},
		{name: "concurrent_bypass_biased_without_flag", observations: concurrent},
		{name: "absolute_reset_beyond_tolerance", observations: absoluteJitter},
		{name: "derived_reset_beyond_tolerance", observations: derivedJitter},
		{name: "stale_replayed_snapshot", observations: staleReplay},
		{name: "account_break_suppressed", observations: accountBreak},
		{name: "plan_break_suppressed", observations: planBreak},
		{name: "utilization_drop_break", observations: utilizationDrop},
		{name: "pricing_change_qualifying_segment", observations: pricingQualifying},
		{name: "pricing_change_without_qualifying_segment", observations: pricingSuppressed},
		{name: "unpriced_model_cost_exclusion", observations: unpriced},
		{name: "mix_shift", observations: mixShift},
		{name: "reset_ambiguity", observations: resetAmbiguous},
		{name: "nonpositive_slope", observations: percentSeries([]float64{30, 25, 20, 15, 10}, 100)},
		{name: "too_few_samples", observations: linearSeries(3, 100, 10, 5)},
		{name: "low_percent_span", observations: percentSeries([]float64{10, 12, 14, 16, 18}, 100)},
		{name: "empty_series", observations: nil},
		{name: "canonical_reset_unassigned", observations: noReset},
		{name: "feature_quota_rejected", observations: featureQuota},
		{name: "role_changes_do_not_split", observations: roleChanges},
		{name: "identity_unverified", observations: identityUnverified},
	}
}

func projectEstimate(result estimate.WindowEstimate) goldenEstimate {
	record := goldenEstimate{
		Confidence:           result.Confidence,
		Flags:                result.Flags,
		SampleCount:          result.SampleCount,
		EffectiveSamples:     result.EffectiveSamples,
		DistinctPercents:     result.DistinctPercents,
		PercentResolution:    roundFloat(result.PercentResolution),
		PercentSpan:          roundFloat(result.PercentSpan),
		MarginalTokensPer100: result.MarginalTokensPer100,
		TokensAt100:          result.TokensAt100,
		TokensCI95:           projectInterval(result.TokensCI95),
		CostAt100:            roundedFloatPointer(result.CostAt100),
		SlopeInstability:     roundedFloatPointer(result.SlopeInstability),
		PointClassCounts:     make(map[estimate.PointClass]int),
	}
	if result.EpochResetAt != nil {
		value := result.EpochResetAt.UTC().Format(time.RFC3339Nano)
		record.EpochResetAt = &value
	}
	if result.CostSegment != nil {
		record.CostSegment = &goldenSegment{
			Hash:  result.CostSegment.PricingSnapshotHash,
			Start: result.CostSegment.Start.Format(time.RFC3339Nano),
			End:   result.CostSegment.End.Format(time.RFC3339Nano),
		}
	}
	for _, point := range result.Points {
		record.PointClassCounts[point.Class]++
		if point.CumulativePercentOffset != 0 {
			record.NonzeroOffsets = append(record.NonzeroOffsets, goldenOffset{
				ObservationID: point.ObservationID,
				Offset:        roundFloat(point.CumulativePercentOffset),
			})
		}
	}
	return record
}

func projectInterval(value *estimate.Interval) goldenInterval {
	if value == nil {
		return goldenInterval{}
	}
	low := roundFloat(value.Low)
	result := goldenInterval{
		Low:           &low,
		UnboundedHigh: value.UnboundedHigh,
	}
	if value.High != nil {
		high := roundFloat(*value.High)
		result.High = &high
	}
	return result
}

func roundedFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	rounded := roundFloat(*value)
	return &rounded
}

func stringPointer(value string) *string {
	return &value
}

func roundFloat(value float64) float64 {
	const scale = 1_000_000.0
	return float64(int64(value*scale+0.5)) / scale
}
