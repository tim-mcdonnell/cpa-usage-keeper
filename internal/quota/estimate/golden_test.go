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

type goldenEstimate struct {
	Name                 string                      `json:"name"`
	Confidence           estimate.Confidence         `json:"confidence"`
	Flags                []estimate.Flag             `json:"flags"`
	MarginalTokensPer100 *int64                      `json:"marginal_tokens_per_100"`
	TokensAt100          *int64                      `json:"tokens_at_100"`
	TokensCI95           goldenInterval              `json:"tokens_ci_95"`
	SlopeInstability     *float64                    `json:"slope_instability"`
	PointClassCounts     map[estimate.PointClass]int `json:"point_class_counts"`
	NonzeroOffsets       []goldenOffset              `json:"nonzero_offsets"`
}

type goldenInterval struct {
	Low           *float64 `json:"low"`
	High          *float64 `json:"high"`
	UnboundedHigh bool     `json:"unbounded_high"`
}

type goldenOffset struct {
	ObservationID int64   `json:"observation_id"`
	Offset        float64 `json:"offset"`
}

func TestSyntheticCapacityGoldenDatasets(t *testing.T) {
	t.Parallel()
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
	concurrent := linearSeries(10, 100, 10, 5)
	for index := 5; index < len(concurrent); index++ {
		percent := *concurrent[index].UsedPercent + float64(index-4)*3
		concurrent[index].UsedPercent = &percent
	}

	scenarios := []struct {
		name         string
		observations []entities.QuotaObservation
	}{
		{name: "known_rounded_capacity", observations: stable},
		{name: "known_sub_integer_resolution", observations: subInteger},
		{name: "unstable_bootstrap_slope", observations: unstable},
		{name: "zero_coverage_bypass", observations: coverage},
		{name: "concurrent_bypass_biased_without_flag", observations: concurrent},
	}
	actual := make([]goldenEstimate, 0, len(scenarios))
	for _, scenario := range scenarios {
		result := requireSingleEstimate(
			t,
			defaultEstimator().EstimateWindows(scenario.observations, testBaseTime.Add(time.Hour)),
		)
		record := goldenEstimate{
			Name:                 scenario.name,
			Confidence:           result.Confidence,
			Flags:                result.Flags,
			MarginalTokensPer100: result.MarginalTokensPer100,
			TokensAt100:          result.TokensAt100,
			TokensCI95:           projectInterval(result.TokensCI95),
			SlopeInstability:     roundedFloatPointer(result.SlopeInstability),
			PointClassCounts:     make(map[estimate.PointClass]int),
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
		actual = append(actual, record)
	}
	actualJSON, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden estimates: %v", err)
	}
	actualJSON = append(actualJSON, '\n')
	goldenPath := filepath.Join("testdata", "synthetic_capacity.golden.json")
	expectedJSON, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	if string(actualJSON) != string(expectedJSON) {
		t.Fatalf("golden mismatch for %s\nactual:\n%s", goldenPath, actualJSON)
	}
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

func roundFloat(value float64) float64 {
	const scale = 1_000_000.0
	return float64(int64(value*scale+0.5)) / scale
}
