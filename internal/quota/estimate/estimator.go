package estimate

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/timeutil"
)

type estimator struct {
	config Config
}

type seriesKey struct {
	usageIdentityID int64
	authType        string
	authIndex       string
	provider        string
	windowKindID    string
	windowSeconds   int64
}

func (key seriesKey) String() string {
	return strings.Join([]string{
		strconv.FormatInt(key.usageIdentityID, 10),
		key.authType,
		key.authIndex,
		key.provider,
		key.windowKindID,
		strconv.FormatInt(key.windowSeconds, 10),
	}, "\x00")
}

type resetProvenance uint8

const (
	resetProvenanceAbsolute resetProvenance = iota
	resetProvenanceDerived
)

type classifiedObservation struct {
	observation     entities.QuotaObservation
	resetAt         time.Time
	resetProvenance resetProvenance
	class           PointClass
	adjustedPercent *float64
	percentOffset   float64
}

type epochSeries struct {
	key              seriesKey
	resetAt          time.Time
	anchorProvenance resetProvenance
	observations     []*classifiedObservation
	suppressed       bool
	identityChanged  bool
}

func New(config Config) Estimator {
	if config.BootstrapReplicates <= 0 {
		config.BootstrapReplicates = DefaultBootstrapReplicates
	}
	if config.BootstrapSeed == 0 {
		config.BootstrapSeed = DefaultBootstrapSeed
	}
	return &estimator{config: config}
}

func (e *estimator) EstimateWindows(observations []entities.QuotaObservation, now time.Time) []WindowEstimate {
	if e == nil {
		return []WindowEstimate{}
	}
	now = timeutil.NormalizeStorageTime(now)
	grouped := make(map[seriesKey][]entities.QuotaObservation)
	for _, observation := range observations {
		if !IsEstimableWindowKind(strings.TrimSpace(observation.WindowKindID)) {
			continue
		}
		windowSeconds := int64(0)
		if observation.WindowSeconds != nil {
			windowSeconds = *observation.WindowSeconds
		}
		if windowSeconds <= 0 {
			continue
		}
		key := seriesKey{
			usageIdentityID: observation.UsageIdentityID,
			authType:        strings.TrimSpace(observation.AuthType),
			authIndex:       strings.TrimSpace(observation.AuthIndex),
			provider:        strings.ToLower(strings.TrimSpace(observation.Provider)),
			windowKindID:    strings.TrimSpace(observation.WindowKindID),
			windowSeconds:   windowSeconds,
		}
		grouped[key] = append(grouped[key], cloneObservation(observation))
	}
	keys := make([]seriesKey, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left int, right int) bool {
		return keys[left].String() < keys[right].String()
	})
	result := make([]WindowEstimate, 0)
	for _, key := range keys {
		series := grouped[key]
		sortObservations(series)
		epochs := assignEpochs(key, series)
		for _, epoch := range epochs {
			result = append(result, e.estimateEpoch(epoch, now))
		}
	}
	sort.SliceStable(result, func(left int, right int) bool {
		if result[left].AuthIndex != result[right].AuthIndex {
			return result[left].AuthIndex < result[right].AuthIndex
		}
		if result[left].Provider != result[right].Provider {
			return result[left].Provider < result[right].Provider
		}
		if result[left].WindowKindID != result[right].WindowKindID {
			return result[left].WindowKindID < result[right].WindowKindID
		}
		leftReset := result[left].EpochResetAt
		rightReset := result[right].EpochResetAt
		if leftReset == nil || rightReset == nil {
			return leftReset != nil
		}
		if !leftReset.Equal(*rightReset) {
			return leftReset.After(*rightReset)
		}
		return result[left].UsageIdentityID < result[right].UsageIdentityID
	})
	return result
}

func assignEpochs(key seriesKey, observations []entities.QuotaObservation) []*epochSeries {
	records := quarantineStaleSnapshots(key, observations)
	epochs := make([]*epochSeries, 0)
	var current *epochSeries
	for _, record := range records {
		if record.class == PointStaleQuarantined {
			if current != nil {
				current.observations = append(current.observations, record)
			}
			continue
		}
		resetAt := record.resetAt
		provenance := record.resetProvenance
		if resetAt.IsZero() {
			continue
		}
		if current == nil {
			current = &epochSeries{
				key:              key,
				resetAt:          resetAt,
				anchorProvenance: provenance,
			}
			current.observations = append(current.observations, record)
			epochs = append(epochs, current)
			continue
		}
		tolerance := resetTolerance(key.windowSeconds, current.anchorProvenance, provenance)
		resetDifference := resetAt.Sub(current.resetAt)
		if resetDifference > tolerance {
			current = &epochSeries{
				key:              key,
				resetAt:          resetAt,
				anchorProvenance: provenance,
			}
			current.observations = append(current.observations, record)
			epochs = append(epochs, current)
			continue
		}
		current.observations = append(current.observations, record)
	}
	if len(epochs) == 0 && len(records) > 0 {
		for _, record := range records {
			record.class = PointEpochUnassigned
		}
		epochs = append(epochs, &epochSeries{
			key:          key,
			observations: records,
		})
	}
	for _, epoch := range epochs {
		if !epoch.resetAt.IsZero() {
			applyForcedBreaks(epoch)
		}
	}
	return epochs
}

func quarantineStaleSnapshots(key seriesKey, observations []entities.QuotaObservation) []*classifiedObservation {
	records := make([]*classifiedObservation, 0, len(observations))
	var newestReset time.Time
	newestProvenance := resetProvenanceAbsolute
	for _, observation := range observations {
		resetAt, provenance, ok := canonicalReset(observation)
		record := &classifiedObservation{
			observation:     observation,
			resetAt:         resetAt,
			resetProvenance: provenance,
			class:           PointIncluded,
		}
		if ok && !newestReset.IsZero() {
			tolerance := resetTolerance(key.windowSeconds, newestProvenance, provenance)
			if resetAt.Before(newestReset.Add(-tolerance)) {
				record.class = PointStaleQuarantined
				records = append(records, record)
				continue
			}
		}
		if ok && (newestReset.IsZero() || resetAt.After(newestReset)) {
			newestReset = resetAt
			newestProvenance = provenance
		}
		records = append(records, record)
	}
	return records
}

func applyForcedBreaks(epoch *epochSeries) {
	candidates := make([]*classifiedObservation, 0, len(epoch.observations))
	percentValues := make([]fitPoint, 0, len(epoch.observations))
	for _, record := range epoch.observations {
		if record.class == PointStaleQuarantined {
			continue
		}
		candidates = append(candidates, record)
		if record.observation.UsedPercent != nil {
			percentValues = append(percentValues, fitPoint{y: *record.observation.UsedPercent})
		}
	}
	_, resolution, _ := percentDiagnostics(percentValues)
	threshold := math.Max(
		MaterialUtilizationDropMinimum,
		MaterialUtilizationDropResolutionMultiples*resolution,
	)
	for index := 1; index < len(candidates); index++ {
		previous := candidates[index-1].observation
		current := candidates[index].observation
		incarnationBreak := identityChanged(&previous, current)
		utilizationBreak := sustainedUtilizationDrop(candidates, index, threshold)
		if !incarnationBreak && !utilizationBreak {
			continue
		}
		for priorIndex := 0; priorIndex < index; priorIndex++ {
			if candidates[priorIndex].class == PointIncluded {
				candidates[priorIndex].class = PointPreBreak
			}
		}
		if incarnationBreak {
			epoch.identityChanged = true
			epoch.suppressed = true
		}
	}
}

func sustainedUtilizationDrop(records []*classifiedObservation, index int, threshold float64) bool {
	if index <= 0 || index >= len(records) {
		return false
	}
	previous := records[index-1].observation.UsedPercent
	current := records[index].observation.UsedPercent
	if previous == nil || current == nil || *current > *previous-threshold {
		return false
	}
	for nextIndex := index + 1; nextIndex < len(records); nextIndex++ {
		next := records[nextIndex].observation.UsedPercent
		if next == nil {
			continue
		}
		return *next <= *previous-threshold
	}
	return true
}

func identityChanged(previous *entities.QuotaObservation, current entities.QuotaObservation) bool {
	if previous == nil {
		return false
	}
	return !equalOptionalString(previous.AccountID, current.AccountID) ||
		!equalOptionalString(previous.PlanType, current.PlanType)
}

func canonicalReset(observation entities.QuotaObservation) (time.Time, resetProvenance, bool) {
	if observation.ResetAt != nil && !observation.ResetAt.IsZero() {
		provenance := resetProvenanceAbsolute
		if observation.ResetAfterSeconds != nil && !rawResetIsAbsolute(observation.ResetRaw) {
			provenance = resetProvenanceDerived
		}
		return timeutil.NormalizeStorageTime(*observation.ResetAt), provenance, true
	}
	if observation.ResetAfterSeconds != nil && *observation.ResetAfterSeconds >= 0 && !observation.ObservedAt.IsZero() {
		return timeutil.NormalizeStorageTime(observation.ObservedAt).
			Add(time.Duration(*observation.ResetAfterSeconds) * time.Second), resetProvenanceDerived, true
	}
	return time.Time{}, resetProvenanceAbsolute, false
}

func resetTolerance(windowSeconds int64, left resetProvenance, right resetProvenance) time.Duration {
	if left == resetProvenanceAbsolute && right == resetProvenanceAbsolute {
		return AbsoluteResetTolerance
	}
	tolerance := time.Duration(float64(windowSeconds) * DerivedResetWindowFraction * float64(time.Second))
	tolerance = max(tolerance, DerivedResetMinimumTolerance)
	return min(tolerance, DerivedResetMaximumTolerance)
}

func rawResetIsAbsolute(value *string) bool {
	if value == nil {
		return false
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return false
	}
	if _, err := timeutil.ParseStorageTime(trimmed); err == nil {
		return true
	}
	epoch, err := strconv.ParseInt(trimmed, 10, 64)
	return err == nil && epoch > 0
}

func sortObservations(observations []entities.QuotaObservation) {
	sort.SliceStable(observations, func(left int, right int) bool {
		leftValue := observations[left]
		rightValue := observations[right]
		if !leftValue.ObservedAt.Equal(rightValue.ObservedAt) {
			return leftValue.ObservedAt.Before(rightValue.ObservedAt)
		}
		leftReset, _, leftOK := canonicalReset(leftValue)
		rightReset, _, rightOK := canonicalReset(rightValue)
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && !leftReset.Equal(rightReset) {
			return leftReset.Before(rightReset)
		}
		leftPercent := optionalFloat(leftValue.UsedPercent)
		rightPercent := optionalFloat(rightValue.UsedPercent)
		if leftPercent != rightPercent {
			return leftPercent < rightPercent
		}
		leftTokens := optionalInt64(leftValue.AttributedTokens)
		rightTokens := optionalInt64(rightValue.AttributedTokens)
		if leftTokens != rightTokens {
			return leftTokens < rightTokens
		}
		leftTieBreaker := observationTieBreaker(leftValue)
		rightTieBreaker := observationTieBreaker(rightValue)
		if leftTieBreaker != rightTieBreaker {
			return leftTieBreaker < rightTieBreaker
		}
		return leftValue.ID < rightValue.ID
	})
}

func observationTieBreaker(observation entities.QuotaObservation) string {
	return fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%.17g\x00%s",
		observation.Source,
		observation.QuotaKey,
		observation.GroupKey,
		observation.PricingSnapshotHash,
		optionalString(observation.ResetRaw),
		optionalString(observation.AccountID),
		optionalString(observation.PlanType),
		optionalInt64(observation.AttributedInputTokens),
		optionalInt64(observation.AttributedOutputTokens),
		optionalInt64(observation.AttributedCacheReadTokens),
		optionalInt64(observation.AttributedCacheCreationTokens),
		optionalFloat(observation.AttributedCostUSD),
		strconv.FormatBool(observation.AttributedCostComplete),
	)
}

func optionalFloat(value *float64) float64 {
	if value == nil {
		return math.Inf(-1)
	}
	return *value
}

func optionalInt64(value *int64) int64 {
	if value == nil {
		return math.MinInt64
	}
	return *value
}

func equalOptionalString(left *string, right *string) bool {
	return optionalString(left) == optionalString(right) &&
		(left == nil) == (right == nil)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func cloneObservation(value entities.QuotaObservation) entities.QuotaObservation {
	value.AccountID = cloneStringPointer(value.AccountID)
	value.PlanType = cloneStringPointer(value.PlanType)
	value.WindowSeconds = cloneInt64Pointer(value.WindowSeconds)
	value.UsedPercent = cloneFloat64Pointer(value.UsedPercent)
	value.RemainingFraction = cloneFloat64Pointer(value.RemainingFraction)
	value.Used = cloneFloat64Pointer(value.Used)
	value.LimitValue = cloneFloat64Pointer(value.LimitValue)
	value.Remaining = cloneFloat64Pointer(value.Remaining)
	value.ResetAt = cloneTimePointer(value.ResetAt)
	value.ResetRaw = cloneStringPointer(value.ResetRaw)
	value.ResetAfterSeconds = cloneInt64Pointer(value.ResetAfterSeconds)
	value.ProviderWindowTokens = cloneInt64Pointer(value.ProviderWindowTokens)
	value.ProviderWindowCost = cloneFloat64Pointer(value.ProviderWindowCost)
	value.AttributedTokens = cloneInt64Pointer(value.AttributedTokens)
	value.AttributedInputTokens = cloneInt64Pointer(value.AttributedInputTokens)
	value.AttributedOutputTokens = cloneInt64Pointer(value.AttributedOutputTokens)
	value.AttributedCacheReadTokens = cloneInt64Pointer(value.AttributedCacheReadTokens)
	value.AttributedCacheCreationTokens = cloneInt64Pointer(value.AttributedCacheCreationTokens)
	value.AttributedCostUSD = cloneFloat64Pointer(value.AttributedCostUSD)
	value.TriggeringEventKey = cloneStringPointer(value.TriggeringEventKey)
	return value
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
