package estimate

import (
	"time"

	"cpa-usage-keeper/internal/entities"
)

const (
	AbsoluteResetTolerance       = 10 * time.Second
	DerivedResetMinimumTolerance = 2 * time.Minute
	DerivedResetWindowFraction   = 0.005
	DerivedResetMaximumTolerance = 30 * time.Minute

	MinimumEffectiveSamples    = 4
	MinimumDistinctPercents    = 3
	MinimumPercentSpan         = 10.0
	MinimumResolutionMultiples = 4.0

	HighMinimumEffectiveSamples = 8
	HighMinimumPercentSpan      = 25.0
	HighMaximumRelativeCIWidth  = 0.25
	HighMaximumSlopeInstability = 0.25

	MediumMinimumEffectiveSamples = 5
	MediumMinimumPercentSpan      = 15.0
	MediumMaximumRelativeCIWidth  = 0.50
	MediumMaximumSlopeInstability = 0.40

	OutlierStudentizedResidualThreshold        = 3.0
	OutlierLowConfidenceFraction               = 0.20
	CoverageGapSuppressionFraction             = 0.30
	StaleWindowFraction                        = 0.10
	MaterialUtilizationDropMinimum             = 0.50
	MaterialUtilizationDropResolutionMultiples = 2.0
	MixShiftShareThreshold                     = 0.20
	ResetAmbiguityToleranceFraction            = 0.50

	DefaultBootstrapReplicates            = 1000
	DefaultBootstrapSeed            int64 = 1
	BootstrapLowerPercentile              = 0.025
	BootstrapUpperPercentile              = 0.975
	BootstrapMinimumSuccessFraction       = 0.50
	BootstrapMinimumSuccessfulFits        = 20

	BootstrapPRNGAlgorithm           = "pcg32_splitmix64_v1"
	BootstrapSeedHashAlgorithm       = "fnv1a_64_v1"
	BootstrapPercentileInterpolation = "linear_order_statistics_v1"
)

const (
	WindowKindClaudeFiveHour = "claude/overall/five_hour/18000"
	WindowKindClaudeSevenDay = "claude/overall/seven_day/604800"
	WindowKindCodexFiveHour  = "codex/overall/rate_limit/18000"
	WindowKindCodexSevenDay  = "codex/overall/rate_limit/604800"
)

const (
	MethodOLSBlockBootstrap = "ols_moving_block_bootstrap_v1"
)

type Confidence string

const (
	ConfidenceHigh         Confidence = "high"
	ConfidenceMedium       Confidence = "medium"
	ConfidenceLow          Confidence = "low"
	ConfidenceInsufficient Confidence = "insufficient"
)

type Flag string

const (
	FlagPricingChanged     Flag = "pricing_changed"
	FlagUnpricedModels     Flag = "unpriced_models"
	FlagCoverageGap        Flag = "coverage_gap"
	FlagMixShift           Flag = "mix_shift"
	FlagResetAmbiguous     Flag = "reset_ambiguous"
	FlagIdentityChanged    Flag = "identity_changed"
	FlagIdentityUnverified Flag = "identity_unverified"
	FlagStale              Flag = "stale"
)

type PointClass string

const (
	PointIncluded            PointClass = "included"
	PointOutlier             PointClass = "outlier"
	PointCoverageGapInterval PointClass = "coverage_gap_interval"
	PointStaleQuarantined    PointClass = "stale_quarantined"
	PointPricingExcluded     PointClass = "pricing_excluded"
	PointPreBreak            PointClass = "pre_break"
)

type Interval struct {
	Low           float64  `json:"low"`
	High          *float64 `json:"high"`
	UnboundedHigh bool     `json:"unbounded_high,omitempty"`
}

type SegmentRef struct {
	PricingSnapshotHash string    `json:"pricing_snapshot_hash"`
	Start               time.Time `json:"start"`
	End                 time.Time `json:"end"`
}

type PointDiagnostic struct {
	ObservationID           int64      `json:"observation_id"`
	Class                   PointClass `json:"class"`
	CumulativePercentOffset float64    `json:"cumulative_percent_offset"`
}

type FittedPoint struct {
	ObservationID           int64   `json:"observation_id"`
	AttributedTokens        int64   `json:"attributed_tokens"`
	RawUsedPercent          float64 `json:"raw_used_percent"`
	AdjustedUsedPercent     float64 `json:"adjusted_used_percent"`
	CumulativePercentOffset float64 `json:"cumulative_percent_offset"`
	FittedPercent           float64 `json:"fitted_percent"`
}

type WindowEstimate struct {
	UsageIdentityID      int64             `json:"-"`
	AuthType             string            `json:"-"`
	AuthIndex            string            `json:"-"`
	Provider             string            `json:"provider"`
	WindowKindID         string            `json:"window_kind_id"`
	WindowSeconds        int64             `json:"window_seconds"`
	EpochResetAt         time.Time         `json:"epoch_reset_at"`
	SampleCount          int               `json:"sample_count"`
	EffectiveSamples     int               `json:"effective_samples"`
	DistinctPercents     int               `json:"distinct_percents"`
	PercentResolution    float64           `json:"percent_resolution"`
	PercentSpan          float64           `json:"percent_span"`
	Slope                *float64          `json:"slope"`
	Intercept            *float64          `json:"intercept"`
	MarginalTokensPer100 *int64            `json:"marginal_tokens_per_100"`
	TokensAt100          *int64            `json:"tokens_at_100"`
	MarginalTokensCI95   *Interval         `json:"marginal_tokens_ci_95"`
	TokensCI95           *Interval         `json:"tokens_ci_95"`
	MarginalCostPer100   *float64          `json:"marginal_cost_per_100"`
	CostAt100            *float64          `json:"cost_at_100"`
	MarginalCostCI95     *Interval         `json:"marginal_cost_ci_95"`
	CostCI95             *Interval         `json:"cost_ci_95"`
	CostSegment          *SegmentRef       `json:"cost_segment"`
	RSquared             *float64          `json:"r_squared"`
	SlopeInstability     *float64          `json:"slope_instability"`
	Confidence           Confidence        `json:"confidence"`
	Flags                []Flag            `json:"flags"`
	Points               []PointDiagnostic `json:"points,omitempty"`
	FittedSeries         []FittedPoint     `json:"fitted_series,omitempty"`
	Method               string            `json:"method"`
}

type Estimator interface {
	EstimateWindows(observations []entities.QuotaObservation, now time.Time) []WindowEstimate
}

// Config controls only estimator-local deterministic computation.
// BootstrapSeed is never read from application settings.
// Each fit derives a stable local seed from this value and the series identity,
// so identical observations produce identical intervals regardless of call order.
type Config struct {
	BootstrapReplicates int
	BootstrapSeed       int64
}

func DefaultConfig() Config {
	return Config{
		BootstrapReplicates: DefaultBootstrapReplicates,
		BootstrapSeed:       DefaultBootstrapSeed,
	}
}

func IsEstimableWindowKind(windowKindID string) bool {
	switch windowKindID {
	case WindowKindClaudeFiveHour,
		WindowKindClaudeSevenDay,
		WindowKindCodexFiveHour,
		WindowKindCodexSevenDay:
		return true
	default:
		return false
	}
}

func WithoutDetail(value WindowEstimate) WindowEstimate {
	value.Points = nil
	value.FittedSeries = nil
	return value
}
