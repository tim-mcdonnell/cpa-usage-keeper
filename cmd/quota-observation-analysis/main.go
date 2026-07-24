package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota/estimate"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"gorm.io/gorm"
)

const analysisVersion = "quota_observation_aggregate_v1"

type aggregateReport struct {
	AnalysisVersion             string                      `json:"analysis_version"`
	TablePresent                bool                        `json:"table_present"`
	DatabaseSizeBytes           int64                       `json:"database_size_bytes"`
	DatabaseAllocatedBytes      int64                       `json:"database_allocated_bytes"`
	ObservationRows             int64                       `json:"observation_rows"`
	DistinctCredentials         int64                       `json:"distinct_credentials"`
	DistinctWindowKinds         int64                       `json:"distinct_window_kinds"`
	DistinctResetEpochs         int64                       `json:"distinct_reset_epochs"`
	ActiveRecordingDays         int64                       `json:"active_recording_days"`
	EarliestObservation         *string                     `json:"earliest_observation"`
	LatestObservation           *string                     `json:"latest_observation"`
	HistorySpanHours            float64                     `json:"history_span_hours"`
	OldestObservationAgeHours   float64                     `json:"oldest_observation_age_hours"`
	NewestObservationAgeHours   float64                     `json:"newest_observation_age_hours"`
	RowsPerObservedDay          float64                     `json:"rows_per_observed_day"`
	RowsPerActiveDay            float64                     `json:"rows_per_active_day"`
	RowsLast24Hours             int64                       `json:"rows_last_24_hours"`
	RowsLast7Days               int64                       `json:"rows_last_7_days"`
	RowsLast30Days              int64                       `json:"rows_last_30_days"`
	NullAttributedTokenRows     int64                       `json:"null_attributed_token_rows"`
	ZeroAttributedTokenRows     int64                       `json:"zero_attributed_token_rows"`
	PositiveAttributedTokenRows int64                       `json:"positive_attributed_token_rows"`
	EstimateCount               int                         `json:"estimate_count"`
	ConfidenceCounts            map[estimate.Confidence]int `json:"confidence_counts"`
	FlagCounts                  map[estimate.Flag]int       `json:"flag_counts"`
	PointClassCounts            map[estimate.PointClass]int `json:"point_class_counts"`
	MedianEffectiveSamples      float64                     `json:"median_effective_samples"`
	MedianPercentSpan           float64                     `json:"median_percent_span"`
	MedianPercentResolution     float64                     `json:"median_percent_resolution"`
	MedianSlopeInstability      *float64                    `json:"median_slope_instability"`
	MedianFiniteRelativeCIWidth *float64                    `json:"median_finite_relative_ci_width"`
}

type observationAggregates struct {
	ObservationRows             int64
	DistinctCredentials         int64
	DistinctWindowKinds         int64
	DistinctResetEpochs         int64
	ActiveRecordingDays         int64
	EarliestObservation         *string
	LatestObservation           *string
	RowsLast24Hours             int64
	RowsLast7Days               int64
	RowsLast30Days              int64
	NullAttributedTokenRows     int64
	ZeroAttributedTokenRows     int64
	PositiveAttributedTokenRows int64
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

func run(args []string, stdout io.Writer, stderr io.Writer, now func() time.Time) int {
	flags := flag.NewFlagSet("quota-observation-analysis", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("db", "", "path to a CPA Usage Keeper app.db")
	nowValue := flags.String("now", "", "optional RFC3339 analysis time for reproducible age and rate statistics")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*databasePath) == "" {
		fmt.Fprintln(stderr, "--db is required")
		return 2
	}
	analysisTime := now().UTC()
	if strings.TrimSpace(*nowValue) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*nowValue))
		if err != nil {
			fmt.Fprintf(stderr, "parse --now: %v\n", err)
			return 2
		}
		analysisTime = parsed.UTC()
	}
	info, err := os.Stat(*databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "stat database: %v\n", err)
		return 1
	}
	if !info.Mode().IsRegular() {
		fmt.Fprintln(stderr, "database path is not a regular file")
		return 1
	}
	db, err := repository.OpenReadDatabase(config.Config{SQLitePath: *databasePath})
	if err != nil {
		fmt.Fprintf(stderr, "open database read-only: %v\n", err)
		return 1
	}
	sqlDB, err := db.DB()
	if err != nil {
		fmt.Fprintf(stderr, "access database pool: %v\n", err)
		return 1
	}
	defer sqlDB.Close()

	report, err := analyzeQuotaObservations(db, info.Size(), analysisTime)
	if err != nil {
		fmt.Fprintf(stderr, "analyze quota observations: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return 1
	}
	return 0
}

func analyzeQuotaObservations(db *gorm.DB, databaseSize int64, now time.Time) (aggregateReport, error) {
	if db == nil {
		return aggregateReport{}, errors.New("database is nil")
	}
	report := aggregateReport{
		AnalysisVersion:   analysisVersion,
		DatabaseSizeBytes: databaseSize,
		ConfidenceCounts:  make(map[estimate.Confidence]int),
		FlagCounts:        make(map[estimate.Flag]int),
		PointClassCounts:  make(map[estimate.PointClass]int),
	}
	var tableCount int64
	if err := db.Raw(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'quota_observations'",
	).Scan(&tableCount).Error; err != nil {
		return aggregateReport{}, fmt.Errorf("check quota observation table: %w", err)
	}
	var pageCount int64
	if err := db.Raw("PRAGMA page_count").Scan(&pageCount).Error; err != nil {
		return aggregateReport{}, fmt.Errorf("read database page count: %w", err)
	}
	var pageSize int64
	if err := db.Raw("PRAGMA page_size").Scan(&pageSize).Error; err != nil {
		return aggregateReport{}, fmt.Errorf("read database page size: %w", err)
	}
	report.DatabaseAllocatedBytes = pageCount * pageSize
	if tableCount == 0 {
		return report, nil
	}
	report.TablePresent = true

	var aggregates observationAggregates
	query := `
SELECT
	COUNT(*) AS observation_rows,
	COUNT(DISTINCT usage_identity_id) AS distinct_credentials,
	COUNT(DISTINCT window_kind_id) AS distinct_window_kinds,
	(
		SELECT COUNT(*)
		FROM (
			SELECT 1
			FROM quota_observations
			GROUP BY usage_identity_id, window_kind_id, reset_at
		)
	) AS distinct_reset_epochs,
	COUNT(DISTINCT date(observed_at)) AS active_recording_days,
	MIN(observed_at) AS earliest_observation,
	MAX(observed_at) AS latest_observation,
	SUM(CASE WHEN julianday(observed_at) >= julianday(?) THEN 1 ELSE 0 END) AS rows_last24_hours,
	SUM(CASE WHEN julianday(observed_at) >= julianday(?) THEN 1 ELSE 0 END) AS rows_last7_days,
	SUM(CASE WHEN julianday(observed_at) >= julianday(?) THEN 1 ELSE 0 END) AS rows_last30_days,
	SUM(CASE WHEN attributed_tokens IS NULL THEN 1 ELSE 0 END) AS null_attributed_token_rows,
	SUM(CASE WHEN attributed_tokens = 0 THEN 1 ELSE 0 END) AS zero_attributed_token_rows,
	SUM(CASE WHEN attributed_tokens > 0 THEN 1 ELSE 0 END) AS positive_attributed_token_rows
FROM quota_observations`
	if err := db.Raw(
		query,
		timeutil.FormatStorageTime(now.Add(-24*time.Hour)),
		timeutil.FormatStorageTime(now.Add(-7*24*time.Hour)),
		timeutil.FormatStorageTime(now.Add(-30*24*time.Hour)),
	).Scan(&aggregates).Error; err != nil {
		return aggregateReport{}, fmt.Errorf("aggregate quota observations: %w", err)
	}
	report.ObservationRows = aggregates.ObservationRows
	report.DistinctCredentials = aggregates.DistinctCredentials
	report.DistinctWindowKinds = aggregates.DistinctWindowKinds
	report.DistinctResetEpochs = aggregates.DistinctResetEpochs
	report.ActiveRecordingDays = aggregates.ActiveRecordingDays
	report.RowsLast24Hours = aggregates.RowsLast24Hours
	report.RowsLast7Days = aggregates.RowsLast7Days
	report.RowsLast30Days = aggregates.RowsLast30Days
	report.NullAttributedTokenRows = aggregates.NullAttributedTokenRows
	report.ZeroAttributedTokenRows = aggregates.ZeroAttributedTokenRows
	report.PositiveAttributedTokenRows = aggregates.PositiveAttributedTokenRows
	if aggregates.ObservationRows == 0 {
		return report, nil
	}
	earliest, err := parseAggregateTime(aggregates.EarliestObservation)
	if err != nil {
		return aggregateReport{}, fmt.Errorf("parse earliest observation: %w", err)
	}
	latest, err := parseAggregateTime(aggregates.LatestObservation)
	if err != nil {
		return aggregateReport{}, fmt.Errorf("parse latest observation: %w", err)
	}
	earliestText := earliest.UTC().Format(time.RFC3339Nano)
	latestText := latest.UTC().Format(time.RFC3339Nano)
	report.EarliestObservation = &earliestText
	report.LatestObservation = &latestText
	report.HistorySpanHours = roundSix(latest.Sub(earliest).Hours())
	report.OldestObservationAgeHours = roundSix(max(0, now.Sub(earliest).Hours()))
	report.NewestObservationAgeHours = roundSix(max(0, now.Sub(latest).Hours()))
	observedDays := math.Max(1, latest.Sub(earliest).Hours()/24)
	report.RowsPerObservedDay = roundSix(float64(aggregates.ObservationRows) / observedDays)
	if aggregates.ActiveRecordingDays > 0 {
		report.RowsPerActiveDay = roundSix(
			float64(aggregates.ObservationRows) / float64(aggregates.ActiveRecordingDays),
		)
	}
	var observations []entities.QuotaObservation
	if err := db.Order("observed_at ASC, id ASC").Find(&observations).Error; err != nil {
		return aggregateReport{}, fmt.Errorf("load observations for estimator aggregates: %w", err)
	}
	estimates := estimate.New(estimate.DefaultConfig()).EstimateWindows(observations, now)
	report.EstimateCount = len(estimates)
	effectiveSamples := make([]float64, 0, len(estimates))
	percentSpans := make([]float64, 0, len(estimates))
	percentResolutions := make([]float64, 0, len(estimates))
	slopeInstabilities := make([]float64, 0, len(estimates))
	finiteRelativeCIWidths := make([]float64, 0, len(estimates))
	for _, windowEstimate := range estimates {
		report.ConfidenceCounts[windowEstimate.Confidence]++
		effectiveSamples = append(effectiveSamples, float64(windowEstimate.EffectiveSamples))
		percentSpans = append(percentSpans, windowEstimate.PercentSpan)
		percentResolutions = append(percentResolutions, windowEstimate.PercentResolution)
		for _, flag := range windowEstimate.Flags {
			report.FlagCounts[flag]++
		}
		for _, point := range windowEstimate.Points {
			report.PointClassCounts[point.Class]++
		}
		if windowEstimate.SlopeInstability != nil {
			slopeInstabilities = append(slopeInstabilities, *windowEstimate.SlopeInstability)
		}
		if width, ok := finiteRelativeCIWidth(windowEstimate); ok {
			finiteRelativeCIWidths = append(finiteRelativeCIWidths, width)
		}
	}
	report.MedianEffectiveSamples = roundSix(medianFloat(effectiveSamples))
	report.MedianPercentSpan = roundSix(medianFloat(percentSpans))
	report.MedianPercentResolution = roundSix(medianFloat(percentResolutions))
	report.MedianSlopeInstability = roundedMedianPointer(slopeInstabilities)
	report.MedianFiniteRelativeCIWidth = roundedMedianPointer(finiteRelativeCIWidths)
	return report, nil
}

func parseAggregateTime(value *string) (time.Time, error) {
	if value == nil {
		return time.Time{}, errors.New("aggregate time is null")
	}
	return timeutil.ParseStorageTime(*value)
}

func roundSix(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	slices.Sort(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func roundedMedianPointer(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	value := roundSix(medianFloat(values))
	return &value
}

func finiteRelativeCIWidth(value estimate.WindowEstimate) (float64, bool) {
	if value.TokensAt100 == nil ||
		*value.TokensAt100 <= 0 ||
		value.TokensCI95 == nil ||
		value.TokensCI95.UnboundedHigh ||
		value.TokensCI95.High == nil {
		return 0, false
	}
	width := math.Abs(*value.TokensCI95.High-value.TokensCI95.Low) /
		float64(*value.TokensAt100)
	if math.IsNaN(width) || math.IsInf(width, 0) {
		return 0, false
	}
	return width, true
}
