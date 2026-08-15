package estimate_test

import (
	"slices"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota/estimate"
)

func TestCoverageGapIsAdjustedClassifiedAndLowConfidence(t *testing.T) {
	t.Parallel()
	observations := linearSeries(10, 100, 10, 5)
	observations[4].AttributedTokens = int64Pointer(300)
	setComposition(&observations[4], 300, 180, 60, 60, 0)
	observations[4].AttributedCostUSD = floatPointer(0.3)
	for index := 4; index < len(observations); index++ {
		raw := *observations[index].UsedPercent + 10
		if index == 4 {
			raw = 35
		}
		observations[index].UsedPercent = &raw
	}
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if !slices.Contains(result.Flags, estimate.FlagCoverageGap) {
		t.Fatalf("flags = %v, want coverage_gap", result.Flags)
	}
	if result.Confidence != estimate.ConfidenceLow {
		t.Fatalf("confidence = %q, want low", result.Confidence)
	}
	if result.TokensAt100 == nil || *result.TokensAt100 != 1800 {
		t.Fatalf("coverage-adjusted tokens at 100 = %v, want 1800; fitted=%+v", pointerValue(result.TokensAt100), result.FittedSeries)
	}
	diagnostic := diagnosticByID(t, result, observations[4].ID)
	if diagnostic.Class != estimate.PointCoverageGapInterval ||
		diagnostic.CumulativePercentOffset != 10 {
		t.Fatalf("gap diagnostic = %+v", diagnostic)
	}
	for _, point := range result.FittedSeries {
		if point.ObservationID > observations[4].ID &&
			point.CumulativePercentOffset != 10 {
			t.Fatalf("later fitted point did not retain cumulative offset: %+v", point)
		}
		if point.ObservationID == observations[4].ID {
			t.Fatalf("coverage-gap interval endpoint remained in fitted series: %+v", point)
		}
	}
}

func TestResidualCoverageGapIsDetectedThroughEstimatorInterface(t *testing.T) {
	t.Parallel()
	clean := linearSeries(12, 100, 10, 5)
	contaminated := linearSeries(12, 100, 10, 5)
	const bypassPercent = 12.0
	const bypassIndex = 6
	for index := bypassIndex; index < len(contaminated); index++ {
		percent := *contaminated[index].UsedPercent + bypassPercent
		contaminated[index].UsedPercent = &percent
	}

	cleanResult := requireSingleEstimate(t, defaultEstimator().EstimateWindows(clean, testBaseTime.Add(time.Hour)))
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(contaminated, testBaseTime.Add(time.Hour)))
	if !slices.Contains(result.Flags, estimate.FlagCoverageGap) {
		t.Fatalf("nonzero-spend bypass flags = %v, want coverage_gap", result.Flags)
	}
	diagnostic := diagnosticByID(t, result, contaminated[bypassIndex].ID)
	if diagnostic.Class != estimate.PointCoverageGapInterval {
		t.Fatalf("nonzero-spend bypass diagnostic = %+v, want coverage_gap_interval", diagnostic)
	}
	if diagnostic.CumulativePercentOffset != bypassPercent {
		t.Fatalf("nonzero-spend bypass offset = %v, want %v", diagnostic.CumulativePercentOffset, bypassPercent)
	}
	if result.TokensAt100 == nil || cleanResult.TokensAt100 == nil ||
		*result.TokensAt100 != *cleanResult.TokensAt100 {
		t.Fatalf("residual refinement did not recover clean fit: clean=%v contaminated=%v", cleanResult.TokensAt100, result.TokensAt100)
	}
	if result.Confidence != estimate.ConfidenceLow {
		t.Fatalf("residual coverage confidence = %q, want low", result.Confidence)
	}
}

func TestResidualCoverageBaselineExcludesEverySuspectIntervalBeforeRefit(t *testing.T) {
	t.Parallel()
	clean := linearSeries(16, 100, 10, 5)
	contaminated := linearSeries(16, 100, 10, 5)
	for index := 5; index < len(contaminated); index++ {
		percent := *contaminated[index].UsedPercent + 12
		contaminated[index].UsedPercent = &percent
	}
	for index := 10; index < len(contaminated); index++ {
		percent := *contaminated[index].UsedPercent + 14
		contaminated[index].UsedPercent = &percent
	}

	cleanResult := requireSingleEstimate(t, defaultEstimator().EstimateWindows(clean, testBaseTime.Add(time.Hour)))
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(contaminated, testBaseTime.Add(time.Hour)))
	if result.TokensAt100 == nil || cleanResult.TokensAt100 == nil ||
		*result.TokensAt100 != *cleanResult.TokensAt100 {
		t.Fatalf("multiple-suspect refit changed capacity: clean=%v contaminated=%v", cleanResult.TokensAt100, result.TokensAt100)
	}
	first := diagnosticByID(t, result, contaminated[5].ID)
	second := diagnosticByID(t, result, contaminated[10].ID)
	if first.Class != estimate.PointCoverageGapInterval ||
		first.CumulativePercentOffset != 12 ||
		second.Class != estimate.PointCoverageGapInterval ||
		second.CumulativePercentOffset != 26 {
		t.Fatalf("multiple-suspect diagnostics = first:%+v second:%+v", first, second)
	}
	for _, fitted := range result.FittedSeries {
		if fitted.ObservationID == contaminated[5].ID ||
			fitted.ObservationID == contaminated[10].ID {
			t.Fatalf("suspect interval remained in final fit: %+v", fitted)
		}
	}
}

func TestResidualCoverageLeavesUpwardTransientToOutlierPolicy(t *testing.T) {
	t.Parallel()
	observations := linearSeries(20, 100, 10, 4)
	const outlierIndex = 10
	percent := *observations[outlierIndex].UsedPercent + 6
	observations[outlierIndex].UsedPercent = &percent

	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if slices.Contains(result.Flags, estimate.FlagCoverageGap) {
		t.Fatalf("upward transient flags = %v, do not want coverage_gap", result.Flags)
	}
	if diagnosticByID(t, result, observations[outlierIndex].ID).Class != estimate.PointOutlier {
		t.Fatalf(
			"upward transient diagnostic = %+v, want outlier",
			diagnosticByID(t, result, observations[outlierIndex].ID),
		)
	}
}

func TestResidualCoverageLeavesCoherentMixSlopeToMixPolicy(t *testing.T) {
	t.Parallel()
	observations := percentSeries(
		[]float64{10, 15, 20, 25, 30, 35, 40, 45, 55, 65, 75, 85},
		100,
	)
	var input int64
	var output int64
	for index := range observations {
		if index < 8 {
			input += 100
		} else {
			output += 100
		}
		tokens := int64(index+1) * 100
		setComposition(&observations[index], tokens, input, output, 0, 0)
	}

	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if slices.Contains(result.Flags, estimate.FlagCoverageGap) {
		t.Fatalf("coherent mix slope flags = %v, do not want coverage_gap", result.Flags)
	}
	if !slices.Contains(result.Flags, estimate.FlagMixShift) {
		t.Fatalf("coherent mix slope flags = %v, want mix_shift", result.Flags)
	}
	for _, point := range result.Points {
		if point.Class == estimate.PointCoverageGapInterval {
			t.Fatalf("coherent mix slope received coverage classification: %+v", result.Points)
		}
	}
}

func TestResidualCoverageComposesWithPricingMixAndResetPolicy(t *testing.T) {
	t.Parallel()
	t.Run("pricing and residual coverage", func(t *testing.T) {
		observations := linearSeries(14, 100, 10, 5)
		for index := 6; index < len(observations); index++ {
			percent := *observations[index].UsedPercent + 12
			observations[index].UsedPercent = &percent
		}
		for index := 7; index < len(observations); index++ {
			observations[index].PricingSnapshotHash = "pricing-b"
			cost := float64(*observations[index].AttributedTokens) / 500
			observations[index].AttributedCostUSD = &cost
		}
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if !slices.Contains(result.Flags, estimate.FlagCoverageGap) ||
			!slices.Contains(result.Flags, estimate.FlagPricingChanged) {
			t.Fatalf("composed flags = %v, want coverage_gap and pricing_changed", result.Flags)
		}
		if result.TokensAt100 == nil || *result.TokensAt100 != 1800 {
			t.Fatalf("composed token fit = %v, want 1800", pointerValue(result.TokensAt100))
		}
		if result.CostSegment == nil || result.CostSegment.PricingSnapshotHash != "pricing-b" {
			t.Fatalf("composed cost segment = %+v, want pricing-b", result.CostSegment)
		}
	})

	t.Run("mix and residual coverage", func(t *testing.T) {
		observations := linearSeries(14, 100, 10, 5)
		var input int64
		var output int64
		for index := range observations {
			if index < 7 {
				input += 100
			} else {
				output += 100
			}
			tokens := int64(index) * 100
			setComposition(&observations[index], tokens, input, output, 0, 0)
		}
		for index := 6; index < len(observations); index++ {
			percent := *observations[index].UsedPercent + 12
			observations[index].UsedPercent = &percent
		}
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if !slices.Contains(result.Flags, estimate.FlagCoverageGap) ||
			!slices.Contains(result.Flags, estimate.FlagMixShift) {
			t.Fatalf("composed flags = %v, want coverage_gap and mix_shift", result.Flags)
		}
		if result.Confidence != estimate.ConfidenceLow {
			t.Fatalf("composed confidence = %q, want low", result.Confidence)
		}
	})

	t.Run("reset epochs remain isolated", func(t *testing.T) {
		first := linearSeries(12, 100, 10, 5)
		for index := 6; index < len(first); index++ {
			percent := *first[index].UsedPercent + 12
			first[index].UsedPercent = &percent
		}
		second := linearSeries(12, 100, 10, 5)
		nextReset := testBaseTime.Add(10 * time.Hour)
		nextResetRaw := nextReset.Format(time.RFC3339Nano)
		for index := range second {
			second[index].ID += 100
			second[index].ObservedAt = second[index].ObservedAt.Add(5 * time.Hour)
			second[index].ResetAt = timePointer(nextReset)
			second[index].ResetRaw = &nextResetRaw
		}
		results := defaultEstimator().EstimateWindows(append(first, second...), testBaseTime.Add(6*time.Hour))
		if len(results) != 2 {
			t.Fatalf("composed epoch count = %d, want 2", len(results))
		}
		if slices.Contains(results[0].Flags, estimate.FlagCoverageGap) {
			t.Fatalf("clean reset epoch inherited coverage flag: %+v", results[0])
		}
		if !slices.Contains(results[1].Flags, estimate.FlagCoverageGap) {
			t.Fatalf("contaminated reset epoch lost coverage flag: %+v", results[1])
		}
	})
}

func TestCoverageNullZeroAndDegenerateInputsRemainDistinct(t *testing.T) {
	t.Parallel()
	t.Run("null attribution is not zero coverage", func(t *testing.T) {
		observations := linearSeries(12, 100, 10, 5)
		observations[6].AttributedTokens = nil
		observations[6].AttributedInputTokens = nil
		observations[6].AttributedOutputTokens = nil
		observations[6].AttributedCacheReadTokens = nil
		observations[6].AttributedCacheCreationTokens = nil
		observations[6].AttributedCostUSD = nil
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if slices.Contains(result.Flags, estimate.FlagCoverageGap) {
			t.Fatalf("null attribution was treated as zero coverage: %+v", result)
		}
		if diagnosticByID(t, result, observations[6].ID).Class == estimate.PointCoverageGapInterval {
			t.Fatalf("null attribution received coverage-gap classification: %+v", result.Points)
		}
	})

	t.Run("zero attribution remains zero coverage", func(t *testing.T) {
		observations := linearSeries(12, 100, 10, 5)
		zero := *observations[5].AttributedTokens
		observations[6].AttributedTokens = &zero
		setComposition(
			&observations[6],
			zero,
			*observations[5].AttributedInputTokens,
			*observations[5].AttributedOutputTokens,
			*observations[5].AttributedCacheReadTokens,
			*observations[5].AttributedCacheCreationTokens,
		)
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if !slices.Contains(result.Flags, estimate.FlagCoverageGap) {
			t.Fatalf("zero attribution did not produce coverage flag: %+v", result)
		}
		if diagnosticByID(t, result, observations[6].ID).Class != estimate.PointCoverageGapInterval {
			t.Fatalf("zero attribution diagnostic = %+v", diagnosticByID(t, result, observations[6].ID))
		}
	})

	t.Run("too few clean intervals do not invent residual coverage", func(t *testing.T) {
		observations := linearSeries(5, 100, 10, 5)
		for index := 3; index < len(observations); index++ {
			percent := *observations[index].UsedPercent + 20
			observations[index].UsedPercent = &percent
		}
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if slices.Contains(result.Flags, estimate.FlagCoverageGap) {
			t.Fatalf("degenerate residual series produced a coverage claim: %+v", result)
		}
		if result.Confidence == estimate.ConfidenceHigh {
			t.Fatalf("degenerate residual series received high confidence: %+v", result)
		}
	})
}

func TestCoverageGapAboveThirtyPercentSuppressesEstimate(t *testing.T) {
	t.Parallel()
	observations := linearSeries(11, 100, 10, 5)
	tokens := []int64{0, 0, 100, 100, 200, 200, 300, 300, 400, 500, 600}
	for index := range observations {
		observations[index].AttributedTokens = int64Pointer(tokens[index])
		setComposition(&observations[index], tokens[index], tokens[index], 0, 0, 0)
	}
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if result.Confidence != estimate.ConfidenceInsufficient {
		t.Fatalf("confidence = %q, want insufficient", result.Confidence)
	}
	if !slices.Contains(result.Flags, estimate.FlagCoverageGap) {
		t.Fatalf("flags = %v, want coverage_gap", result.Flags)
	}
	if result.TokensAt100 != nil {
		t.Fatalf("suppressed estimate exposed tokens at 100: %v", *result.TokensAt100)
	}
}

func TestConcurrentBypassRemainsBiasedWithoutFlag(t *testing.T) {
	t.Parallel()
	clean := linearSeries(10, 100, 10, 5)
	concurrent := linearSeries(10, 100, 10, 5)
	for index := 5; index < len(concurrent); index++ {
		shift := float64(index-4) * 3
		percent := *concurrent[index].UsedPercent + shift
		concurrent[index].UsedPercent = &percent
	}
	cleanResult := requireSingleEstimate(t, defaultEstimator().EstimateWindows(clean, testBaseTime.Add(time.Hour)))
	concurrentResult := requireSingleEstimate(t, defaultEstimator().EstimateWindows(concurrent, testBaseTime.Add(time.Hour)))
	if slices.Contains(concurrentResult.Flags, estimate.FlagCoverageGap) {
		t.Fatalf("concurrent bypass incorrectly flagged: %v", concurrentResult.Flags)
	}
	if concurrentResult.TokensAt100 == nil || cleanResult.TokensAt100 == nil ||
		*concurrentResult.TokensAt100 >= *cleanResult.TokensAt100 {
		t.Fatalf("concurrent bypass was not biased as expected: clean=%v concurrent=%v", cleanResult.TokensAt100, concurrentResult.TokensAt100)
	}
}

func TestSustainedTransientAndTrailingUtilizationDrops(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name             string
		percents         []float64
		wantPreBreak     bool
		wantInsufficient bool
	}{
		{
			name:         "sustained drop",
			percents:     []float64{35, 40, 5, 10, 15, 20, 25, 30},
			wantPreBreak: true,
		},
		{
			name:         "transient dip recovers",
			percents:     []float64{35, 40, 5, 45, 50, 55, 60, 65},
			wantPreBreak: false,
		},
		{
			name:             "trailing drop",
			percents:         []float64{10, 15, 20, 25, 30, 35, 5},
			wantPreBreak:     true,
			wantInsufficient: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			observations := percentSeries(testCase.percents, 100)
			result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
			hasPreBreak := false
			for _, point := range result.Points {
				hasPreBreak = hasPreBreak || point.Class == estimate.PointPreBreak
			}
			if hasPreBreak != testCase.wantPreBreak {
				t.Fatalf("pre-break classification = %v, want %v; points=%+v", hasPreBreak, testCase.wantPreBreak, result.Points)
			}
			if testCase.wantInsufficient && result.Confidence != estimate.ConfidenceInsufficient {
				t.Fatalf("confidence = %q, want insufficient", result.Confidence)
			}
		})
	}
}

func TestAccountAndPlanChangesSuppressUntilNaturalReset(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"account", "plan"} {
		t.Run(field, func(t *testing.T) {
			observations := linearSeries(8, 100, 10, 5)
			account := "account-2"
			plan := "pro"
			for index := 4; index < len(observations); index++ {
				if field == "account" {
					observations[index].AccountID = &account
				} else {
					observations[index].PlanType = &plan
				}
			}
			result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
			if result.Confidence != estimate.ConfidenceInsufficient ||
				!slices.Contains(result.Flags, estimate.FlagIdentityChanged) {
				t.Fatalf("identity break result = %+v", result)
			}
			for index := 0; index < 4; index++ {
				if diagnosticByID(t, result, observations[index].ID).Class != estimate.PointPreBreak {
					t.Fatalf("observation %d not classified pre_break", observations[index].ID)
				}
			}
		})
	}

	observations := linearSeries(8, 100, 10, 5)
	nextReset := testBaseTime.Add(10 * time.Hour)
	nextResetRaw := nextReset.Format(time.RFC3339Nano)
	account := "account-2"
	for index := 4; index < len(observations); index++ {
		observations[index].AccountID = &account
	}
	for index := 0; index < 8; index++ {
		next := observations[index]
		next.ID += 100
		next.ObservedAt = testBaseTime.Add(5*time.Hour + time.Duration(index)*10*time.Minute)
		next.ResetAt = timePointer(nextReset)
		next.ResetRaw = &nextResetRaw
		next.AccountID = &account
		observations = append(observations, next)
	}
	results := defaultEstimator().EstimateWindows(observations, testBaseTime.Add(6*time.Hour))
	if len(results) != 2 {
		t.Fatalf("epoch count = %d, want two", len(results))
	}
	if results[0].Confidence == estimate.ConfidenceInsufficient {
		t.Fatalf("natural reset did not end suppression: %+v", results[0])
	}
	if results[1].Confidence != estimate.ConfidenceInsufficient ||
		!slices.Contains(results[1].Flags, estimate.FlagIdentityChanged) {
		t.Fatalf("spanning epoch should remain suppressed: %+v", results[1])
	}
}

func TestEpochToleranceAndStaleQuarantine(t *testing.T) {
	t.Parallel()
	t.Run("absolute at and beyond tolerance", func(t *testing.T) {
		atTolerance := linearSeries(4, 100, 10, 5)
		anchor := *atTolerance[0].ResetAt
		for index := range atTolerance {
			reset := anchor.Add(time.Duration(index%2) * estimate.AbsoluteResetTolerance)
			raw := reset.Format(time.RFC3339Nano)
			atTolerance[index].ResetAt = &reset
			atTolerance[index].ResetRaw = &raw
		}
		if results := defaultEstimator().EstimateWindows(atTolerance, testBaseTime.Add(time.Hour)); len(results) != 1 {
			t.Fatalf("at-tolerance observations formed %d epochs", len(results))
		}
		beyond := append([]entities.QuotaObservation(nil), atTolerance...)
		reset := anchor.Add(estimate.AbsoluteResetTolerance + time.Second)
		raw := reset.Format(time.RFC3339Nano)
		beyond[len(beyond)-1].ResetAt = &reset
		beyond[len(beyond)-1].ResetRaw = &raw
		if results := defaultEstimator().EstimateWindows(beyond, testBaseTime.Add(time.Hour)); len(results) != 2 {
			t.Fatalf("beyond-tolerance observations formed %d epochs", len(results))
		}
	})

	t.Run("derived at and beyond tolerance", func(t *testing.T) {
		tolerance := 2 * time.Minute
		atTolerance := linearSeries(4, 100, 10, 5)
		anchor := *atTolerance[0].ResetAt
		for index := range atTolerance {
			reset := anchor
			if index == len(atTolerance)-1 {
				reset = anchor.Add(tolerance)
			}
			atTolerance[index].ResetAt = &reset
			atTolerance[index].ResetRaw = nil
			after := int64(reset.Sub(atTolerance[index].ObservedAt) / time.Second)
			atTolerance[index].ResetAfterSeconds = &after
		}
		if results := defaultEstimator().EstimateWindows(atTolerance, testBaseTime.Add(time.Hour)); len(results) != 1 {
			t.Fatalf("at-tolerance derived observations formed %d epochs", len(results))
		}
		beyond := append([]entities.QuotaObservation(nil), atTolerance...)
		reset := anchor.Add(tolerance + time.Second)
		beyond[len(beyond)-1].ResetAt = &reset
		after := int64(reset.Sub(beyond[len(beyond)-1].ObservedAt) / time.Second)
		beyond[len(beyond)-1].ResetAfterSeconds = &after
		if results := defaultEstimator().EstimateWindows(beyond, testBaseTime.Add(time.Hour)); len(results) != 2 {
			t.Fatalf("beyond-tolerance derived observations formed %d epochs", len(results))
		}
	})

	t.Run("replayed snapshot is quarantined", func(t *testing.T) {
		observations := linearSeries(8, 100, 10, 5)
		nextReset := testBaseTime.Add(10 * time.Hour)
		nextRaw := nextReset.Format(time.RFC3339Nano)
		for index := 4; index < len(observations); index++ {
			observations[index].ResetAt = &nextReset
			observations[index].ResetRaw = &nextRaw
		}
		replay := observations[2]
		replay.ID = 99
		replay.ObservedAt = observations[len(observations)-1].ObservedAt.Add(time.Minute)
		observations = append(observations, replay)
		results := defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour))
		if len(results) != 2 {
			t.Fatalf("epoch count = %d, want two", len(results))
		}
		latest := results[0]
		if diagnosticByID(t, latest, replay.ID).Class != estimate.PointStaleQuarantined {
			t.Fatalf("replay was not quarantined: %+v", latest.Points)
		}
		if !slices.Contains(latest.Flags, estimate.FlagStale) {
			t.Fatalf("stale replay did not carry stale flag: %v", latest.Flags)
		}
	})
}

func TestResetAmbiguityUsesDerivedMaxDeviationFromAbsoluteAnchor(t *testing.T) {
	t.Parallel()
	observations := linearSeries(8, 100, 10, 5)
	anchor := *observations[0].ResetAt
	for index := 1; index < len(observations); index++ {
		reset := anchor.Add(time.Duration(index%2) * 70 * time.Second)
		observations[index].ResetAt = &reset
		observations[index].ResetRaw = nil
		after := int64(reset.Sub(observations[index].ObservedAt) / time.Second)
		observations[index].ResetAfterSeconds = &after
	}
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if !slices.Contains(result.Flags, estimate.FlagResetAmbiguous) {
		t.Fatalf("flags = %v, want reset_ambiguous", result.Flags)
	}
	if result.Confidence == estimate.ConfidenceHigh {
		t.Fatal("reset ambiguity must block high confidence")
	}
}

func TestPricingSegmentsAndUnpricedModelsAffectCostOnly(t *testing.T) {
	t.Parallel()
	t.Run("coverage gap reconnects one pricing-pure segment", func(t *testing.T) {
		observations := linearSeries(8, 100, 10, 2.5)
		observations[4].AttributedTokens = int64Pointer(300)
		setComposition(&observations[4], 300, 180, 60, 60, 0)
		observations[4].AttributedCostUSD = floatPointer(0.3)
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if !slices.Contains(result.Flags, estimate.FlagCoverageGap) {
			t.Fatalf("flags = %v, want coverage_gap", result.Flags)
		}
		if result.CostAt100 == nil || result.CostSegment == nil {
			t.Fatalf("reconnected pricing-pure series did not qualify for cost: %+v", result)
		}
		if result.CostSegment.PricingSnapshotHash != "pricing-a" {
			t.Fatalf("cost segment hash = %q", result.CostSegment.PricingSnapshotHash)
		}
	})

	t.Run("early pure-pricing accrual stays included", func(t *testing.T) {
		observations := linearSeries(3, 100, 10, 5)
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if result.Confidence != estimate.ConfidenceInsufficient {
			t.Fatalf("confidence = %q, want insufficient", result.Confidence)
		}
		for _, point := range result.Points {
			if point.Class != estimate.PointIncluded {
				t.Fatalf("pure-pricing point %d class = %q, want included", point.ObservationID, point.Class)
			}
		}
	})

	t.Run("longest qualifying segment is selected", func(t *testing.T) {
		observations := linearSeries(12, 100, 5, 5)
		for index := 6; index < len(observations); index++ {
			observations[index].PricingSnapshotHash = "pricing-b"
			cost := float64(*observations[index].AttributedTokens) / 500
			observations[index].AttributedCostUSD = &cost
		}
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if !slices.Contains(result.Flags, estimate.FlagPricingChanged) {
			t.Fatalf("flags = %v, want pricing_changed", result.Flags)
		}
		if result.CostSegment == nil || result.CostSegment.PricingSnapshotHash != "pricing-b" {
			t.Fatalf("cost segment = %+v, want pricing-b", result.CostSegment)
		}
		if result.CostAt100 == nil || result.TokensAt100 == nil {
			t.Fatalf("qualifying segment did not produce both estimates: %+v", result)
		}
	})

	t.Run("no qualifying segment suppresses cost", func(t *testing.T) {
		observations := linearSeries(8, 100, 10, 5)
		for index := range observations {
			if index%3 == 0 {
				observations[index].PricingSnapshotHash = "pricing-b"
			}
		}
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if result.CostAt100 != nil || result.CostSegment != nil {
			t.Fatalf("non-qualifying pricing runs produced cost: %+v", result)
		}
		if result.TokensAt100 == nil {
			t.Fatal("pricing changes suppressed token estimate")
		}
	})

	t.Run("unpriced models exclude cost only", func(t *testing.T) {
		observations := linearSeries(10, 100, 10, 5)
		observations[4].AttributedCostComplete = false
		result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
		if !slices.Contains(result.Flags, estimate.FlagUnpricedModels) {
			t.Fatalf("flags = %v, want unpriced_models", result.Flags)
		}
		if result.TokensAt100 == nil {
			t.Fatal("unpriced model suppressed token estimate")
		}
		if diagnosticByID(t, result, observations[4].ID).Class != estimate.PointPricingExcluded {
			t.Fatalf("unpriced observation classification = %+v", diagnosticByID(t, result, observations[4].ID))
		}
	})
}

func TestMixShiftUsesIntervalDeltaTVDAndCapsAtMedium(t *testing.T) {
	t.Parallel()
	observations := linearSeries(10, 100, 10, 5)
	var input int64
	var output int64
	for index := range observations {
		if index < 5 {
			input += 100
		} else {
			output += 100
		}
		tokens := int64(index+1) * 100
		observations[index].AttributedTokens = &tokens
		setComposition(&observations[index], tokens, input, output, 0, 0)
	}
	result := requireSingleEstimate(t, defaultEstimator().EstimateWindows(observations, testBaseTime.Add(time.Hour)))
	if !slices.Contains(result.Flags, estimate.FlagMixShift) {
		t.Fatalf("flags = %v, want mix_shift", result.Flags)
	}
	if result.Confidence == estimate.ConfidenceHigh {
		t.Fatal("mix shift must cap confidence at medium")
	}
}

func diagnosticByID(t *testing.T, value estimate.WindowEstimate, id int64) estimate.PointDiagnostic {
	t.Helper()
	for _, point := range value.Points {
		if point.ObservationID == id {
			return point
		}
	}
	t.Fatalf("missing diagnostic for observation %d", id)
	return estimate.PointDiagnostic{}
}

func pointerValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func setComposition(
	observation *entities.QuotaObservation,
	total int64,
	input int64,
	output int64,
	cacheRead int64,
	cacheCreation int64,
) {
	observation.AttributedTokens = int64Pointer(total)
	observation.AttributedInputTokens = int64Pointer(input)
	observation.AttributedOutputTokens = int64Pointer(output)
	observation.AttributedCacheReadTokens = int64Pointer(cacheRead)
	observation.AttributedCacheCreationTokens = int64Pointer(cacheCreation)
}
