package estimate

import (
	"math"
	"sort"
	"strings"
	"time"
)

type costSegment struct {
	hash           string
	points         []fitPoint
	start          time.Time
	end            time.Time
	observationIDs map[int64]struct{}
}

type coverageInterval struct {
	endObservationIndex int
	tokenDelta          float64
	percentDelta        float64
	rate                float64
	clean               bool
}

func (e *estimator) estimateEpoch(epoch *epochSeries, now time.Time) WindowEstimate {
	value := WindowEstimate{
		UsageIdentityID: epoch.key.usageIdentityID,
		AuthType:        epoch.key.authType,
		AuthIndex:       epoch.key.authIndex,
		Provider:        epoch.key.provider,
		WindowKindID:    epoch.key.windowKindID,
		WindowSeconds:   epoch.key.windowSeconds,
		SampleCount:     len(epoch.observations),
		Confidence:      ConfidenceInsufficient,
		Method:          MethodOLSBlockBootstrap,
	}
	if !epoch.resetAt.IsZero() {
		resetAt := epoch.resetAt
		value.EpochResetAt = &resetAt
	}
	flags := make(map[Flag]struct{})
	if epoch.identityChanged {
		flags[FlagIdentityChanged] = struct{}{}
	}
	if identityUnverified(epoch.observations) {
		flags[FlagIdentityUnverified] = struct{}{}
	}
	if staleEpoch(epoch, now) {
		flags[FlagStale] = struct{}{}
	}
	for _, record := range epoch.observations {
		if record.class == PointStaleQuarantined {
			flags[FlagStale] = struct{}{}
		}
	}

	tokenPoints, contaminatedIntervals, totalIntervals := coverageAdjustedTokenPoints(epoch.observations)
	if contaminatedIntervals > 0 {
		flags[FlagCoverageGap] = struct{}{}
	}
	effectiveBeforeOutliers := effectiveFitPoints(tokenPoints)
	filteredTokenPoints, outliers, outlierFraction := removeOutliers(effectiveBeforeOutliers)
	for _, record := range epoch.observations {
		if _, ok := outliers[record.observation.ID]; ok && record.class == PointIncluded {
			record.class = PointOutlier
		}
	}
	filteredTokenPoints = effectiveFitPoints(filteredTokenPoints)

	if mixShifted(epoch.observations) {
		flags[FlagMixShift] = struct{}{}
	}
	if resetAmbiguous(epoch) {
		flags[FlagResetAmbiguous] = struct{}{}
	}
	pricingChanged, unpriced := pricingFlags(epoch.observations)
	if pricingChanged {
		flags[FlagPricingChanged] = struct{}{}
	}
	if unpriced {
		flags[FlagUnpricedModels] = struct{}{}
	}

	value.EffectiveSamples,
		value.DistinctPercents,
		value.PercentResolution,
		value.PercentSpan,
		_ = passesEstimateGates(filteredTokenPoints)
	coverageFraction := 0.0
	if totalIntervals > 0 {
		coverageFraction = float64(contaminatedIntervals) / float64(totalIntervals)
	}
	_, _, _, _, gatesPassed := passesEstimateGates(filteredTokenPoints)
	if gatesPassed && !epoch.suppressed && coverageFraction <= CoverageGapSuppressionFraction {
		e.applyTokenFit(&value, filteredTokenPoints, epoch)
		if value.TokensAt100 != nil {
			e.applyCostFit(&value, epoch)
			value.Confidence = gradeConfidence(value, flags, outlierFraction, coverageFraction)
		}
	} else {
		e.applyCostClassifications(epoch, nil)
	}
	value.Flags = orderedFlags(flags)
	value.Points = diagnostics(epoch.observations)
	return value
}

func (e *estimator) applyTokenFit(value *WindowEstimate, points []fitPoint, epoch *epochSeries) {
	fit, ok := fitOLS(points)
	if !ok || fit.slope <= numericEpsilon {
		return
	}
	marginal := 100 / fit.slope
	at100Numerator := 100 - fit.intercept
	at100 := at100Numerator / fit.slope
	if marginal < 0 || at100 < 0 || math.IsNaN(at100) || math.IsInf(at100, 0) {
		return
	}
	slopes := bootstrapSlopeCI(
		points,
		e.config.BootstrapReplicates,
		e.config.BootstrapSeed,
		epoch.key.authIndex,
		epoch.key.windowKindID,
		epoch.resetAt.Unix(),
	)
	value.Slope = float64Pointer(fit.slope)
	value.Intercept = float64Pointer(fit.intercept)
	value.RSquared = float64Pointer(fit.rSquared)
	value.MarginalTokensPer100 = roundedInt64(marginal)
	value.TokensAt100 = roundedInt64(at100)
	value.MarginalTokensCI95 = capacityInterval(100, slopes)
	value.TokensCI95 = capacityInterval(at100Numerator, slopes)
	value.SlopeInstability = slopeInstability(points)
	value.FittedSeries = make([]FittedPoint, 0, len(points))
	for _, point := range points {
		value.FittedSeries = append(value.FittedSeries, FittedPoint{
			ObservationID:           point.observationID,
			AttributedTokens:        int64(math.Round(point.x)),
			RawUsedPercent:          point.rawY,
			AdjustedUsedPercent:     point.y,
			CumulativePercentOffset: point.percentOffset,
			FittedPercent:           fit.intercept + fit.slope*point.x,
		})
	}
}

func (e *estimator) applyCostFit(value *WindowEstimate, epoch *epochSeries) {
	segments := qualifyingCostSegments(epoch.observations)
	if len(segments) == 0 {
		e.applyCostClassifications(epoch, nil)
		return
	}
	sort.SliceStable(segments, func(left int, right int) bool {
		if len(segments[left].points) != len(segments[right].points) {
			return len(segments[left].points) > len(segments[right].points)
		}
		leftDuration := segments[left].end.Sub(segments[left].start)
		rightDuration := segments[right].end.Sub(segments[right].start)
		if leftDuration != rightDuration {
			return leftDuration > rightDuration
		}
		return segments[left].end.After(segments[right].end)
	})
	selected := segments[0]
	points := effectiveFitPoints(selected.points)
	_, _, _, _, passed := passesEstimateGates(points)
	if !passed {
		e.applyCostClassifications(epoch, nil)
		return
	}
	fit, ok := fitOLS(points)
	if !ok || fit.slope <= numericEpsilon {
		e.applyCostClassifications(epoch, nil)
		return
	}
	marginal := 100 / fit.slope
	at100Numerator := 100 - fit.intercept
	at100 := at100Numerator / fit.slope
	if marginal < 0 || at100 < 0 || math.IsNaN(at100) || math.IsInf(at100, 0) {
		e.applyCostClassifications(epoch, nil)
		return
	}
	slopes := bootstrapSlopeCI(
		points,
		e.config.BootstrapReplicates,
		e.config.BootstrapSeed,
		epoch.key.authIndex,
		epoch.key.windowKindID,
		epoch.resetAt.Unix(),
	)
	value.MarginalCostPer100 = float64Pointer(marginal)
	value.CostAt100 = float64Pointer(at100)
	value.MarginalCostCI95 = capacityInterval(100, slopes)
	value.CostCI95 = capacityInterval(at100Numerator, slopes)
	value.CostSegment = &SegmentRef{
		PricingSnapshotHash: selected.hash,
		Start:               selected.start,
		End:                 selected.end,
	}
	e.applyCostClassifications(epoch, selected.observationIDs)
}

func (e *estimator) applyCostClassifications(epoch *epochSeries, selected map[int64]struct{}) {
	for _, record := range epoch.observations {
		if record.class != PointIncluded {
			continue
		}
		observation := record.observation
		if observation.AttributedTokens == nil {
			continue
		}
		if observation.AttributedCostUSD == nil ||
			!observation.AttributedCostComplete ||
			strings.TrimSpace(observation.PricingSnapshotHash) == "" {
			record.class = PointPricingExcluded
			continue
		}
		if selected == nil {
			continue
		}
		_, selectedForCost := selected[observation.ID]
		if !selectedForCost {
			record.class = PointPricingExcluded
		}
	}
}

func coverageAdjustedTokenPoints(records []*classifiedObservation) ([]fitPoint, int, int) {
	type rawPoint struct {
		record  *classifiedObservation
		tokens  int64
		percent float64
	}
	raw := make([]rawPoint, 0, len(records))
	resolutionPoints := make([]fitPoint, 0, len(records))
	for _, record := range records {
		if record.class != PointIncluded ||
			record.observation.UsedPercent == nil ||
			record.observation.AttributedTokens == nil {
			continue
		}
		raw = append(raw, rawPoint{
			record:  record,
			tokens:  *record.observation.AttributedTokens,
			percent: *record.observation.UsedPercent,
		})
		resolutionPoints = append(resolutionPoints, fitPoint{y: *record.observation.UsedPercent})
	}
	if len(raw) == 0 {
		return nil, 0, 0
	}
	_, resolution, _ := percentDiagnostics(resolutionPoints)
	points := make([]fitPoint, 0, len(raw))
	coverageOffset := 0.0
	contaminated := 0
	for index, point := range raw {
		if index > 0 {
			percentDelta := point.percent - raw[index-1].percent
			tokenDelta := point.tokens - raw[index-1].tokens
			if resolution > 0 &&
				tokenDelta == 0 &&
				percentDelta+numericEpsilon >= resolution {
				coverageOffset += percentDelta
				point.record.class = PointCoverageGapInterval
				point.record.percentOffset = coverageOffset
				adjusted := point.percent - coverageOffset
				point.record.adjustedPercent = &adjusted
				contaminated++
				continue
			}
		}
		adjusted := point.percent - coverageOffset
		point.record.percentOffset = coverageOffset
		point.record.adjustedPercent = &adjusted
		points = append(points, fitPoint{
			observationID:    point.record.observation.ID,
			observationIndex: index,
			x:                float64(point.tokens),
			y:                adjusted,
			rawY:             point.percent,
			percentOffset:    coverageOffset,
		})
	}
	residualGaps := residualCoverageGaps(points, resolution)
	if len(residualGaps) == 0 {
		return points, contaminated, max(0, len(raw)-1)
	}
	refined := make([]fitPoint, 0, len(points)-len(residualGaps))
	residualOffset := 0.0
	for _, point := range points {
		residual, gap := residualGaps[point.observationIndex]
		record := raw[point.observationIndex].record
		if gap {
			residualOffset += residual
			record.class = PointCoverageGapInterval
			record.percentOffset = point.percentOffset + residualOffset
			adjusted := point.y - residualOffset
			record.adjustedPercent = &adjusted
			contaminated++
			continue
		}
		point.y -= residualOffset
		point.percentOffset += residualOffset
		record.percentOffset = point.percentOffset
		adjusted := point.y
		record.adjustedPercent = &adjusted
		refined = append(refined, point)
	}
	return refined, contaminated, max(0, len(raw)-1)
}

func residualCoverageGaps(points []fitPoint, resolution float64) map[int]float64 {
	intervals := make([]coverageInterval, 0, max(0, len(points)-1))
	for index := 1; index < len(points); index++ {
		tokenDelta := points[index].x - points[index-1].x
		if tokenDelta <= numericEpsilon {
			continue
		}
		percentDelta := points[index].y - points[index-1].y
		intervals = append(intervals, coverageInterval{
			endObservationIndex: points[index].observationIndex,
			tokenDelta:          tokenDelta,
			percentDelta:        percentDelta,
			rate:                percentDelta / tokenDelta,
		})
	}
	if len(intervals) < ResidualCoverageMinimumCleanIntervals+1 {
		return nil
	}
	rates := make([]float64, len(intervals))
	for index := range intervals {
		rates[index] = intervals[index].rate
	}
	rateMedian := median(rates)
	rateMAD := medianAbsoluteDeviation(rates, rateMedian)
	rateUpperBound := rateMedian +
		ResidualCoverageRateMADMultiplier*normalMADScale*rateMAD
	// Positive bypass raises utilization per attributed token.
	// Select the robust lower consensus before fitting so a suspect interval
	// cannot pull its own baseline upward.
	clean := make([]coverageInterval, 0, len(intervals))
	for index := range intervals {
		intervals[index].clean = intervals[index].rate <= rateUpperBound+numericEpsilon
		if intervals[index].clean {
			clean = append(clean, intervals[index])
		}
	}
	if len(clean) < ResidualCoverageMinimumCleanIntervals {
		return nil
	}
	baselineSlope, ok := fitIntervalSlope(clean)
	if !ok || baselineSlope <= numericEpsilon {
		return nil
	}
	cleanResiduals := make([]float64, len(clean))
	for index, interval := range clean {
		cleanResiduals[index] = interval.percentDelta - baselineSlope*interval.tokenDelta
	}
	residualMedian := median(cleanResiduals)
	residualMAD := medianAbsoluteDeviation(cleanResiduals, residualMedian)
	residualThreshold := math.Max(
		ResidualCoverageMinimumPercent,
		math.Max(
			ResidualCoverageResolutionMultiples*resolution,
			ResidualCoverageResidualMADMultiplier*normalMADScale*residualMAD,
		),
	)
	residuals := make([]float64, len(intervals))
	for index, interval := range intervals {
		residuals[index] = interval.percentDelta - baselineSlope*interval.tokenDelta
	}
	suspect := make([]bool, len(intervals))
	for index, interval := range intervals {
		if interval.clean {
			continue
		}
		if residuals[index] <= residualThreshold+numericEpsilon {
			continue
		}
		// A compensating negative residual immediately before or after a
		// positive residual is a point outlier, not evidence of bypass.
		recoversBefore := index > 0 && residuals[index-1] < -residualThreshold
		recoversAfter := index+1 < len(intervals) &&
			residuals[index+1] < -residualThreshold
		if recoversBefore || recoversAfter {
			continue
		}
		suspect[index] = true
	}
	result := make(map[int]float64)
	for start := 0; start < len(suspect); {
		if !suspect[start] {
			start++
			continue
		}
		end := start + 1
		for end < len(suspect) && suspect[end] {
			end++
		}
		// Consecutive positive residuals form a coherent alternative slope.
		// Treating that regime as bypass would turn normal workload-mix shifts
		// into coverage claims and would overstate what the estimator can infer.
		if end-start == 1 {
			result[intervals[start].endObservationIndex] = residuals[start]
		}
		start = end
	}
	return result
}

const normalMADScale = 1.4826

func fitIntervalSlope(intervals []coverageInterval) (float64, bool) {
	var tokenSquares float64
	var tokenPercent float64
	for _, interval := range intervals {
		tokenSquares += interval.tokenDelta * interval.tokenDelta
		tokenPercent += interval.tokenDelta * interval.percentDelta
	}
	if tokenSquares <= numericEpsilon {
		return 0, false
	}
	slope := tokenPercent / tokenSquares
	if math.IsNaN(slope) || math.IsInf(slope, 0) {
		return 0, false
	}
	return slope, true
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func medianAbsoluteDeviation(values []float64, center float64) float64 {
	deviations := make([]float64, len(values))
	for index, value := range values {
		deviations[index] = math.Abs(value - center)
	}
	return median(deviations)
}

func qualifyingCostSegments(records []*classifiedObservation) []costSegment {
	segments := make([]costSegment, 0)
	var current *costSegment
	flush := func() {
		if current == nil || len(current.points) == 0 {
			current = nil
			return
		}
		_, _, _, _, passed := passesEstimateGates(effectiveFitPoints(current.points))
		if passed {
			segments = append(segments, *current)
		}
		current = nil
	}
	for _, record := range records {
		observation := record.observation
		priced := observation.AttributedCostUSD != nil &&
			observation.AttributedCostComplete &&
			strings.TrimSpace(observation.PricingSnapshotHash) != ""
		if !priced {
			flush()
			continue
		}
		hash := strings.TrimSpace(observation.PricingSnapshotHash)
		if current != nil && current.hash != hash {
			flush()
		}
		if record.class != PointIncluded || record.adjustedPercent == nil {
			continue
		}
		if current == nil {
			current = &costSegment{
				hash:           hash,
				start:          observation.ObservedAt,
				observationIDs: make(map[int64]struct{}),
			}
		}
		current.end = observation.ObservedAt
		current.points = append(current.points, fitPoint{
			observationID: observation.ID,
			x:             *observation.AttributedCostUSD,
			y:             *record.adjustedPercent,
			rawY:          *observation.UsedPercent,
			percentOffset: record.percentOffset,
		})
		current.observationIDs[observation.ID] = struct{}{}
	}
	flush()
	return segments
}

func pricingFlags(records []*classifiedObservation) (changed bool, unpriced bool) {
	hashes := make(map[string]struct{})
	for _, record := range records {
		observation := record.observation
		if observation.AttributedTokens == nil ||
			record.class == PointPreBreak ||
			record.class == PointStaleQuarantined {
			continue
		}
		if !observation.AttributedCostComplete {
			unpriced = true
		}
		if hash := strings.TrimSpace(observation.PricingSnapshotHash); hash != "" {
			hashes[hash] = struct{}{}
		}
	}
	return len(hashes) > 1, unpriced
}

func gradeConfidence(
	value WindowEstimate,
	flags map[Flag]struct{},
	outlierFraction float64,
	coverageFraction float64,
) Confidence {
	if value.TokensAt100 == nil || value.Slope == nil || *value.Slope <= numericEpsilon {
		return ConfidenceInsufficient
	}
	if coverageFraction > CoverageGapSuppressionFraction {
		return ConfidenceInsufficient
	}
	at100 := float64(*value.TokensAt100)
	ciWidth := relativeIntervalWidth(value.TokensCI95, at100)
	instability := math.Inf(1)
	if value.SlopeInstability != nil {
		instability = *value.SlopeInstability
	}
	if len(flags) == 0 &&
		outlierFraction <= OutlierLowConfidenceFraction &&
		value.EffectiveSamples >= HighMinimumEffectiveSamples &&
		value.PercentSpan >= HighMinimumPercentSpan &&
		ciWidth <= HighMaximumRelativeCIWidth &&
		instability <= HighMaximumSlopeInstability {
		return ConfidenceHigh
	}
	if _, gap := flags[FlagCoverageGap]; gap ||
		outlierFraction > OutlierLowConfidenceFraction {
		return ConfidenceLow
	}
	if value.EffectiveSamples >= MediumMinimumEffectiveSamples &&
		value.PercentSpan >= MediumMinimumPercentSpan &&
		ciWidth <= MediumMaximumRelativeCIWidth &&
		instability <= MediumMaximumSlopeInstability {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

func diagnostics(records []*classifiedObservation) []PointDiagnostic {
	result := make([]PointDiagnostic, 0, len(records))
	for _, record := range records {
		result = append(result, PointDiagnostic{
			ObservationID:           record.observation.ID,
			Class:                   record.class,
			CumulativePercentOffset: record.percentOffset,
		})
	}
	return result
}

func orderedFlags(values map[Flag]struct{}) []Flag {
	order := []Flag{
		FlagPricingChanged,
		FlagUnpricedModels,
		FlagCoverageGap,
		FlagMixShift,
		FlagResetAmbiguous,
		FlagIdentityChanged,
		FlagIdentityUnverified,
		FlagStale,
	}
	result := make([]Flag, 0, len(values))
	for _, value := range order {
		if _, ok := values[value]; ok {
			result = append(result, value)
		}
	}
	return result
}

func identityUnverified(records []*classifiedObservation) bool {
	if len(records) == 0 {
		return false
	}
	for _, record := range records {
		if record.class == PointPreBreak {
			continue
		}
		if record.observation.AccountID != nil || record.observation.PlanType != nil {
			return false
		}
	}
	return true
}

func resetAmbiguous(epoch *epochSeries) bool {
	if len(epoch.observations) < 2 || epoch.resetAt.IsZero() {
		return false
	}
	anchor := epoch.resetAt
	for _, record := range epoch.observations {
		if record.class == PointStaleQuarantined ||
			record.class == PointPreBreak ||
			record.resetAt.IsZero() {
			continue
		}
		if record.resetProvenance == resetProvenanceAbsolute {
			anchor = record.resetAt
			break
		}
	}
	derivedCount := 0
	var maximumDeviation time.Duration
	for _, record := range epoch.observations {
		if record.class == PointStaleQuarantined ||
			record.class == PointPreBreak ||
			record.resetAt.IsZero() ||
			record.resetProvenance != resetProvenanceDerived {
			continue
		}
		derivedCount++
		deviation := record.resetAt.Sub(anchor)
		if deviation < 0 {
			deviation = -deviation
		}
		maximumDeviation = max(maximumDeviation, deviation)
	}
	if derivedCount < 2 {
		return false
	}
	tolerance := resetTolerance(epoch.key.windowSeconds, resetProvenanceDerived, resetProvenanceDerived)
	return maximumDeviation > time.Duration(float64(tolerance)*ResetAmbiguityToleranceFraction)
}

func staleEpoch(epoch *epochSeries, now time.Time) bool {
	var newest time.Time
	for _, record := range epoch.observations {
		if record.class == PointStaleQuarantined {
			continue
		}
		if record.observation.ObservedAt.After(newest) {
			newest = record.observation.ObservedAt
		}
	}
	if newest.IsZero() || epoch.key.windowSeconds <= 0 {
		return false
	}
	reference := now
	if !epoch.resetAt.IsZero() && epoch.resetAt.Before(reference) {
		reference = epoch.resetAt
	}
	return reference.Sub(newest) > time.Duration(float64(epoch.key.windowSeconds)*StaleWindowFraction*float64(time.Second))
}

func mixShifted(records []*classifiedObservation) bool {
	type compositionPoint struct {
		total   int64
		buckets [4]int64
	}
	points := make([]compositionPoint, 0, len(records))
	for _, record := range records {
		observation := record.observation
		if record.class != PointIncluded ||
			observation.AttributedTokens == nil ||
			observation.AttributedInputTokens == nil ||
			observation.AttributedOutputTokens == nil ||
			observation.AttributedCacheReadTokens == nil ||
			observation.AttributedCacheCreationTokens == nil {
			continue
		}
		points = append(points, compositionPoint{
			total: *observation.AttributedTokens,
			buckets: [4]int64{
				*observation.AttributedInputTokens,
				*observation.AttributedOutputTokens,
				*observation.AttributedCacheReadTokens,
				*observation.AttributedCacheCreationTokens,
			},
		})
	}
	if len(points) < 3 {
		return false
	}
	totalSpend := points[len(points)-1].total
	if totalSpend <= 0 {
		return false
	}
	midpointSpend := totalSpend / 2
	boundaryIndex := -1
	for index := range points {
		if points[index].total >= midpointSpend {
			boundaryIndex = index
			break
		}
	}
	if boundaryIndex <= 0 {
		return false
	}
	firstBuckets := subtractBuckets(points[boundaryIndex-1].buckets, points[0].buckets)
	secondBuckets := subtractBuckets(points[len(points)-1].buckets, points[boundaryIndex-1].buckets)
	firstShares, firstOK := bucketShares(firstBuckets)
	secondShares, secondOK := bucketShares(secondBuckets)
	if !firstOK || !secondOK {
		return false
	}
	var totalVariation float64
	for index := range firstShares {
		totalVariation += math.Abs(secondShares[index] - firstShares[index])
	}
	return 0.5*totalVariation >= MixShiftShareThreshold
}

func subtractBuckets(left [4]int64, right [4]int64) [4]int64 {
	var result [4]int64
	for index := range result {
		result[index] = max(int64(0), left[index]-right[index])
	}
	return result
}

func bucketShares(values [4]int64) ([4]float64, bool) {
	var total int64
	for _, value := range values {
		total += max(int64(0), value)
	}
	if total <= 0 {
		return [4]float64{}, false
	}
	var result [4]float64
	for index, value := range values {
		result[index] = float64(max(int64(0), value)) / float64(total)
	}
	return result, true
}
