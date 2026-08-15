package estimate

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
)

const (
	pcg32Multiplier uint64 = 6364136223846793005
	splitMixGamma   uint64 = 0x9e3779b97f4a7c15
)

type intervalDelta struct {
	spend   float64
	percent float64
}

type slopeInterval struct {
	low  float64
	high float64
}

type pcg32 struct {
	state     uint64
	increment uint64
}

func newPCG32(seed uint64) *pcg32 {
	splitState := seed
	initialState := splitMix64(&splitState)
	initialSequence := splitMix64(&splitState)
	generator := &pcg32{increment: (initialSequence << 1) | 1}
	generator.next()
	generator.state += initialState
	generator.next()
	return generator
}

func (generator *pcg32) next() uint32 {
	oldState := generator.state
	generator.state = oldState*pcg32Multiplier + generator.increment
	xorShifted := uint32(((oldState >> 18) ^ oldState) >> 27)
	rotation := int(oldState >> 59)
	return bits.RotateLeft32(xorShifted, -rotation)
}

func (generator *pcg32) intN(bound int) int {
	if bound <= 1 {
		return 0
	}
	unsignedBound := uint32(bound)
	threshold := -unsignedBound % unsignedBound
	for {
		value := generator.next()
		if value >= threshold {
			return int(value % unsignedBound)
		}
	}
}

func splitMix64(state *uint64) uint64 {
	*state += splitMixGamma
	value := *state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func bootstrapSlopeCI(
	points []fitPoint,
	replicates int,
	seed int64,
	authIndex string,
	windowKindID string,
	epochResetUnix int64,
) *slopeInterval {
	if replicates <= 0 || len(points) < 3 {
		return nil
	}
	deltas := make([]intervalDelta, 0, len(points)-1)
	for index := 1; index < len(points); index++ {
		deltas = append(deltas, intervalDelta{
			spend:   points[index].x - points[index-1].x,
			percent: points[index].y - points[index-1].y,
		})
	}
	if len(deltas) < 2 {
		return nil
	}
	blockLength := min(len(deltas), max(1, int(math.Ceil(math.Sqrt(float64(len(deltas)))))))
	generator := newPCG32(deriveSeriesSeed(seed, authIndex, windowKindID, epochResetUnix))
	slopes := make([]float64, 0, replicates)
	for range replicates {
		resampled := movingBlockSample(generator, deltas, blockLength)
		slope, ok := fitDeltaSlope(resampled)
		if !ok {
			continue
		}
		slopes = append(slopes, slope)
	}
	minimumSuccessfulFits := max(
		BootstrapMinimumSuccessfulFits,
		int(math.Ceil(float64(replicates)*BootstrapMinimumSuccessFraction)),
	)
	if len(slopes) < minimumSuccessfulFits {
		return nil
	}
	sort.Float64s(slopes)
	return &slopeInterval{
		low:  percentile(slopes, BootstrapLowerPercentile),
		high: percentile(slopes, BootstrapUpperPercentile),
	}
}

func movingBlockSample(generator *pcg32, values []intervalDelta, blockLength int) []intervalDelta {
	result := make([]intervalDelta, 0, len(values))
	blockCount := int(math.Ceil(float64(len(values)) / float64(blockLength)))
	overlappingBlockCount := len(values) - blockLength + 1
	for range blockCount {
		start := generator.intN(overlappingBlockCount)
		for offset := 0; offset < blockLength && len(result) < len(values); offset++ {
			result = append(result, values[start+offset])
		}
	}
	return result
}

func fitDeltaSlope(values []intervalDelta) (float64, bool) {
	var spendSquares float64
	var spendPercent float64
	for _, value := range values {
		spendSquares += value.spend * value.spend
		spendPercent += value.spend * value.percent
	}
	if spendSquares <= numericEpsilon {
		return 0, false
	}
	slope := spendPercent / spendSquares
	if math.IsNaN(slope) || math.IsInf(slope, 0) {
		return 0, false
	}
	return slope, true
}

func capacityInterval(numerator float64, slopes *slopeInterval) *Interval {
	if slopes == nil || slopes.high <= numericEpsilon || numerator < 0 {
		return nil
	}
	low := numerator / slopes.high
	if slopes.low <= numericEpsilon {
		return &Interval{
			Low:           low,
			High:          nil,
			UnboundedHigh: true,
		}
	}
	high := numerator / slopes.low
	return &Interval{Low: low, High: &high}
}

func percentile(sorted []float64, probability float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if probability <= 0 {
		return sorted[0]
	}
	if probability >= 1 {
		return sorted[len(sorted)-1]
	}
	position := probability * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func deriveSeriesSeed(base int64, authIndex string, windowKindID string, epochResetUnix int64) uint64 {
	hash := fnv.New64a()
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], uint64(base))
	_, _ = hash.Write(encoded[:])
	_, _ = hash.Write([]byte(strings.TrimSpace(authIndex)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strings.TrimSpace(windowKindID)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatInt(epochResetUnix, 10)))
	return hash.Sum64()
}
