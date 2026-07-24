import type {
  UsageQuotaCapacityDetailResponse,
  UsageQuotaCapacityInterval,
  UsageQuotaCapacityPointClass,
  UsageQuotaWindowEstimate,
} from '@/lib/types'

export interface CapacityPointPresentation {
  glyph: 'circle' | 'cross' | 'diamond' | 'slashed-square' | 'triangle' | 'plus' | 'ring'
  translationKey: string
}

export const capacityPointPresentations: Record<UsageQuotaCapacityPointClass, CapacityPointPresentation> = {
  included: {
    glyph: 'circle',
    translationKey: 'usage_stats.credentials_capacity_class_included',
  },
  outlier: {
    glyph: 'cross',
    translationKey: 'usage_stats.credentials_capacity_class_outlier',
  },
  coverage_gap_interval: {
    glyph: 'diamond',
    translationKey: 'usage_stats.credentials_capacity_class_coverage_gap_interval',
  },
  stale_quarantined: {
    glyph: 'slashed-square',
    translationKey: 'usage_stats.credentials_capacity_class_stale_quarantined',
  },
  pricing_excluded: {
    glyph: 'triangle',
    translationKey: 'usage_stats.credentials_capacity_class_pricing_excluded',
  },
  pre_break: {
    glyph: 'plus',
    translationKey: 'usage_stats.credentials_capacity_class_pre_break',
  },
  epoch_unassigned: {
    glyph: 'ring',
    translationKey: 'usage_stats.credentials_capacity_class_epoch_unassigned',
  },
}

export interface CapacityDetailPoint {
  observationID: number
  className: UsageQuotaCapacityPointClass
  observedAt: string | null
  source: string | null
  attributedTokens: number | null
  rawPercent: number | null
  adjustedPercent: number | null
  plotPercent: number | null
  fittedPercent: number | null
  cumulativePercentOffset: number
}

export interface CapacityEpochSummary {
  key: string
  resetAt: string | null
  windowKindID: string
  tokensAt100: number | null
  costAt100: number | null
  confidence: UsageQuotaWindowEstimate['confidence']
  flags: UsageQuotaWindowEstimate['flags']
  selected: boolean
}

export interface CapacityDetailViewModel {
  provider: string
  windowKindID: string
  windowSeconds: number
  epochResetAt: string | null
  confidence: UsageQuotaWindowEstimate['confidence']
  flags: UsageQuotaWindowEstimate['flags']
  tokensAt100: number | null
  costAt100: number | null
  tokensInterval: UsageQuotaCapacityInterval | null
  costInterval: UsageQuotaCapacityInterval | null
  costSegment: UsageQuotaWindowEstimate['cost_segment']
  sampleCount: number
  effectiveSamples: number
  points: CapacityDetailPoint[]
  epochs: CapacityEpochSummary[]
  hasFittedSeries: boolean
  method: string
}

export interface CapacityChartPoint extends CapacityDetailPoint {
  x: number
  y: number
  rawY: number | null
}

export interface CapacityChartCoordinate {
  x: number
  y: number
}

export interface CapacityChartGeometry {
  width: number
  height: number
  plotLeft: number
  plotRight: number
  plotTop: number
  plotBottom: number
  xDomain: [number, number]
  yDomain: [number, number]
  points: CapacityChartPoint[]
  fittedPolyline: CapacityChartCoordinate[]
  at100: CapacityChartCoordinate | null
}

export function buildCapacityDetailViewModel(response: UsageQuotaCapacityDetailResponse): CapacityDetailViewModel {
  const fittedByObservationID = new Map(
    (response.estimate.fitted_series ?? []).map((point) => [point.observation_id, point]),
  )
  const observationByID = new Map(response.observations.map((observation) => [observation.id, observation]))
  const diagnostics = response.estimate.points ?? []
  const points = diagnostics.map((diagnostic) => {
    const observation = observationByID.get(diagnostic.observation_id)
    const fitted = fittedByObservationID.get(diagnostic.observation_id)
    const rawPercent = finiteOrNull(fitted?.raw_used_percent ?? observation?.used_percent)
    const adjustedPercent = finiteOrNull(fitted?.adjusted_used_percent)
    return {
      observationID: diagnostic.observation_id,
      className: diagnostic.class,
      observedAt: observation?.observed_at ?? null,
      source: observation?.source ?? null,
      attributedTokens: finiteOrNull(fitted?.attributed_tokens ?? observation?.attributed_tokens),
      rawPercent,
      adjustedPercent,
      plotPercent: adjustedPercent ?? rawPercent,
      fittedPercent: finiteOrNull(fitted?.fitted_percent),
      cumulativePercentOffset: finiteNumber(diagnostic.cumulative_percent_offset, 0),
    }
  })
  points.sort(compareCapacityPoints)

  return {
    provider: response.estimate.provider,
    windowKindID: response.estimate.window_kind_id,
    windowSeconds: response.estimate.window_seconds,
    epochResetAt: response.estimate.epoch_reset_at,
    confidence: response.estimate.confidence,
    flags: [...response.estimate.flags],
    tokensAt100: finiteOrNull(response.estimate.tokens_at_100),
    costAt100: finiteOrNull(response.estimate.cost_at_100),
    tokensInterval: response.estimate.tokens_ci_95,
    costInterval: response.estimate.cost_ci_95,
    costSegment: response.estimate.cost_segment,
    sampleCount: response.estimate.sample_count,
    effectiveSamples: response.estimate.effective_samples,
    points,
    epochs: response.epochs.map((epoch) => ({
      key: capacityEpochKey(epoch.window_kind_id, epoch.epoch_reset_at),
      resetAt: epoch.epoch_reset_at,
      windowKindID: epoch.window_kind_id,
      tokensAt100: finiteOrNull(epoch.tokens_at_100),
      costAt100: finiteOrNull(epoch.cost_at_100),
      confidence: epoch.confidence,
      flags: [...epoch.flags],
      selected: epoch.epoch_reset_at === response.estimate.epoch_reset_at,
    })),
    hasFittedSeries: (response.estimate.fitted_series?.length ?? 0) > 0,
    method: response.estimate.method,
  }
}

export function buildCapacityChartGeometry(
  viewModel: CapacityDetailViewModel,
  width: number,
  height: number,
): CapacityChartGeometry {
  const safeWidth = Math.max(240, finiteNumber(width, 760))
  const safeHeight = Math.max(220, finiteNumber(height, 320))
  const plotLeft = 58
  const plotRight = safeWidth - 20
  const plotTop = 18
  const plotBottom = safeHeight - 46
  const xValues = [0]
  const yValues = [0, 100]

  for (const point of viewModel.points) {
    if (point.attributedTokens !== null) {
      xValues.push(point.attributedTokens)
    }
    if (point.plotPercent !== null) {
      yValues.push(point.plotPercent)
    }
    if (point.rawPercent !== null) {
      yValues.push(point.rawPercent)
    }
    if (point.fittedPercent !== null) {
      yValues.push(point.fittedPercent)
    }
  }
  if (viewModel.tokensAt100 !== null) {
    xValues.push(viewModel.tokensAt100)
  }

  const xDomain = nonDegenerateDomain(xValues)
  const yDomain = nonDegenerateDomain(yValues)
  const scaleX = (value: number) => plotLeft + ((value - xDomain[0]) / (xDomain[1] - xDomain[0])) * (plotRight - plotLeft)
  const scaleY = (value: number) => plotBottom - ((value - yDomain[0]) / (yDomain[1] - yDomain[0])) * (plotBottom - plotTop)
  const points = viewModel.points.flatMap<CapacityChartPoint>((point) => {
    if (point.attributedTokens === null || point.plotPercent === null) {
      return []
    }
    return [{
      ...point,
      x: scaleX(point.attributedTokens),
      y: scaleY(point.plotPercent),
      rawY: point.rawPercent === null ? null : scaleY(point.rawPercent),
    }]
  })
  const fittedPolyline = viewModel.points
    .filter((point) => point.attributedTokens !== null && point.fittedPercent !== null)
    .sort((left, right) => (left.attributedTokens ?? 0) - (right.attributedTokens ?? 0) || left.observationID - right.observationID)
    .map((point) => ({
      x: scaleX(point.attributedTokens as number),
      y: scaleY(point.fittedPercent as number),
    }))

  return {
    width: safeWidth,
    height: safeHeight,
    plotLeft,
    plotRight,
    plotTop,
    plotBottom,
    xDomain,
    yDomain,
    points,
    fittedPolyline,
    at100: viewModel.tokensAt100 === null
      ? null
      : { x: scaleX(viewModel.tokensAt100), y: scaleY(100) },
  }
}

export function formatCapacityInterval(
  interval: UsageQuotaCapacityInterval | null,
  formatter: (value: number) => string,
  unboundedLabel: string,
): string | null {
  if (!interval) {
    return null
  }
  const high = interval.unbounded_high || interval.high === null
    ? unboundedLabel
    : formatter(interval.high)
  return `${formatter(interval.low)} - ${high}`
}

function capacityEpochKey(windowKindID: string, epochResetAt: string | null): string {
  return `${windowKindID}:${epochResetAt ?? 'unassigned'}`
}

function compareCapacityPoints(left: CapacityDetailPoint, right: CapacityDetailPoint): number {
  if (left.observedAt !== right.observedAt) {
    if (left.observedAt === null) return 1
    if (right.observedAt === null) return -1
    return left.observedAt.localeCompare(right.observedAt)
  }
  return left.observationID - right.observationID
}

function nonDegenerateDomain(values: number[]): [number, number] {
  const finiteValues = values.filter(Number.isFinite)
  const minimum = finiteValues.length > 0 ? Math.min(...finiteValues) : 0
  const maximum = finiteValues.length > 0 ? Math.max(...finiteValues) : 1
  if (maximum > minimum) {
    return [minimum, maximum]
  }
  const padding = Math.max(1, Math.abs(minimum) * 0.05)
  return [minimum - padding, maximum + padding]
}

function finiteOrNull(value: number | null | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function finiteNumber(value: number | null | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}
