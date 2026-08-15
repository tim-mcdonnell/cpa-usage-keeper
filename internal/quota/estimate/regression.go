package estimate

import (
	"math"
	"sort"
)

const numericEpsilon = 1e-12

type fitPoint struct {
	observationID    int64
	observationIndex int
	x                float64
	y                float64
	rawY             float64
	percentOffset    float64
}

type linearFit struct {
	slope     float64
	intercept float64
	rSquared  float64
	residuals []float64
	leverages []float64
}

func fitOLS(points []fitPoint) (linearFit, bool) {
	if len(points) < 2 {
		return linearFit{}, false
	}
	var sumX float64
	var sumY float64
	for _, point := range points {
		sumX += point.x
		sumY += point.y
	}
	meanX := sumX / float64(len(points))
	meanY := sumY / float64(len(points))
	var xx float64
	var xy float64
	var yy float64
	for _, point := range points {
		dx := point.x - meanX
		dy := point.y - meanY
		xx += dx * dx
		xy += dx * dy
		yy += dy * dy
	}
	if xx <= numericEpsilon {
		return linearFit{}, false
	}
	slope := xy / xx
	intercept := meanY - slope*meanX
	residuals := make([]float64, len(points))
	leverages := make([]float64, len(points))
	var residualSumSquares float64
	for index, point := range points {
		residual := point.y - (intercept + slope*point.x)
		residuals[index] = residual
		residualSumSquares += residual * residual
		leverages[index] = 1/float64(len(points)) + ((point.x-meanX)*(point.x-meanX))/xx
	}
	rSquared := 1.0
	if yy > numericEpsilon {
		rSquared = 1 - residualSumSquares/yy
	}
	return linearFit{
		slope:     slope,
		intercept: intercept,
		rSquared:  rSquared,
		residuals: residuals,
		leverages: leverages,
	}, true
}

func removeOutliers(points []fitPoint) ([]fitPoint, map[int64]struct{}, float64) {
	fit, ok := fitOLS(points)
	if !ok || len(points) <= 3 {
		return append([]fitPoint(nil), points...), nil, 0
	}
	var residualSumSquares float64
	for _, residual := range fit.residuals {
		residualSumSquares += residual * residual
	}
	degreesOfFreedom := len(points) - 2
	if degreesOfFreedom <= 0 || residualSumSquares <= numericEpsilon {
		return append([]fitPoint(nil), points...), nil, 0
	}
	residualStandardError := math.Sqrt(residualSumSquares / float64(degreesOfFreedom))
	if residualStandardError <= numericEpsilon {
		return append([]fitPoint(nil), points...), nil, 0
	}
	outliers := make(map[int64]struct{})
	filtered := make([]fitPoint, 0, len(points))
	for index, point := range points {
		leverageScale := math.Sqrt(math.Max(1-fit.leverages[index], numericEpsilon))
		studentized := math.Abs(fit.residuals[index]) / (residualStandardError * leverageScale)
		if studentized > OutlierStudentizedResidualThreshold {
			outliers[point.observationID] = struct{}{}
			continue
		}
		filtered = append(filtered, point)
	}
	if len(outliers) == 0 {
		return append([]fitPoint(nil), points...), nil, 0
	}
	return filtered, outliers, float64(len(outliers)) / float64(len(points))
}

func effectiveFitPoints(points []fitPoint) []fitPoint {
	if len(points) == 0 {
		return nil
	}
	result := make([]fitPoint, 0, len(points))
	for _, point := range points {
		duplicate := false
		for _, existing := range result {
			if nearlyEqual(existing.x, point.x) && nearlyEqual(existing.y, point.y) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, point)
		}
	}
	return result
}

func percentDiagnostics(points []fitPoint) (distinct int, resolution float64, span float64) {
	if len(points) == 0 {
		return 0, 0, 0
	}
	values := make([]float64, 0, len(points))
	for _, point := range points {
		found := false
		for _, value := range values {
			if nearlyEqual(value, point.y) {
				found = true
				break
			}
		}
		if !found {
			values = append(values, point.y)
		}
	}
	sort.Float64s(values)
	if len(values) > 1 {
		span = values[len(values)-1] - values[0]
		resolution = math.Inf(1)
		for index := 1; index < len(values); index++ {
			difference := values[index] - values[index-1]
			if difference > numericEpsilon && difference < resolution {
				resolution = difference
			}
		}
		if math.IsInf(resolution, 1) {
			resolution = 0
		}
	}
	return len(values), resolution, span
}

func passesEstimateGates(points []fitPoint) (int, int, float64, float64, bool) {
	effective := effectiveFitPoints(points)
	distinct, resolution, span := percentDiagnostics(effective)
	if len(effective) < MinimumEffectiveSamples ||
		distinct < MinimumDistinctPercents ||
		span < MinimumPercentSpan ||
		(resolution > 0 && span < MinimumResolutionMultiples*resolution) {
		return len(effective), distinct, resolution, span, false
	}
	fit, ok := fitOLS(effective)
	return len(effective), distinct, resolution, span, ok && fit.slope > numericEpsilon
}

func slopeInstability(points []fitPoint) *float64 {
	if len(points) < 4 {
		return nil
	}
	minimumX := points[0].x
	maximumX := points[0].x
	for _, point := range points[1:] {
		minimumX = math.Min(minimumX, point.x)
		maximumX = math.Max(maximumX, point.x)
	}
	if maximumX-minimumX <= numericEpsilon {
		return nil
	}
	midpoint := minimumX + (maximumX-minimumX)/2
	first := make([]fitPoint, 0, len(points))
	second := make([]fitPoint, 0, len(points))
	for _, point := range points {
		if point.x <= midpoint {
			first = append(first, point)
		}
		if point.x >= midpoint {
			second = append(second, point)
		}
	}
	firstFit, firstOK := fitOLS(effectiveFitPoints(first))
	secondFit, secondOK := fitOLS(effectiveFitPoints(second))
	if !firstOK || !secondOK || firstFit.slope <= numericEpsilon || secondFit.slope <= numericEpsilon {
		return nil
	}
	denominator := math.Max(math.Abs(firstFit.slope), math.Abs(secondFit.slope))
	if denominator <= numericEpsilon {
		return nil
	}
	value := math.Abs(firstFit.slope-secondFit.slope) / denominator
	return &value
}

func nearlyEqual(left float64, right float64) bool {
	scale := math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
	return math.Abs(left-right) <= numericEpsilon*scale
}

func roundedInt64(value float64) *int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > math.MaxInt64 {
		return nil
	}
	rounded := int64(math.Round(value))
	return &rounded
}

func float64Pointer(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func relativeIntervalWidth(interval *Interval, estimate float64) float64 {
	if interval == nil || interval.UnboundedHigh || interval.High == nil || estimate <= numericEpsilon {
		return math.Inf(1)
	}
	return math.Abs(*interval.High-interval.Low) / math.Abs(estimate)
}
