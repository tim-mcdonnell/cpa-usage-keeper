import { describe, expect, it } from 'vitest'
import type {
  UsageQuotaCapacityDetailResponse,
  UsageQuotaCapacityObservation,
  UsageQuotaCapacityPointClass,
  UsageQuotaWindowEstimate,
} from '@/lib/types'
import {
  buildCapacityChartGeometry,
  buildCapacityDetailViewModel,
  capacityPointPresentations,
  formatCapacityInterval,
} from './capacityDetailViewModel'

const pointClasses: UsageQuotaCapacityPointClass[] = [
  'included',
  'outlier',
  'coverage_gap_interval',
  'stale_quarantined',
  'pricing_excluded',
  'pre_break',
  'epoch_unassigned',
]

function estimate(overrides: Partial<UsageQuotaWindowEstimate> = {}): UsageQuotaWindowEstimate {
  return {
    provider: 'codex',
    window_kind_id: 'codex/overall/rate_limit/18000',
    window_seconds: 18_000,
    epoch_reset_at: '2026-07-23T15:00:00Z',
    sample_count: 2,
    effective_samples: 2,
    distinct_percents: 2,
    percent_resolution: 1,
    percent_span: 10,
    slope: 0.1,
    intercept: 0,
    marginal_tokens_per_100: 1_000,
    tokens_at_100: 1_000,
    marginal_tokens_ci_95: { low: 900, high: null, unbounded_high: true },
    tokens_ci_95: { low: 800, high: 1_200 },
    marginal_cost_per_100: null,
    cost_at_100: null,
    marginal_cost_ci_95: null,
    cost_ci_95: null,
    cost_segment: null,
    r_squared: 0.99,
    slope_instability: 0.1,
    confidence: 'medium',
    flags: [],
    points: [],
    fitted_series: [],
    method: 'ols_moving_block_bootstrap_v1',
    ...overrides,
  }
}

function observation(
  id: number,
  overrides: Partial<UsageQuotaCapacityObservation> = {},
): UsageQuotaCapacityObservation {
  return {
    id,
    auth_index: 'auth-1',
    provider: 'codex',
    window_kind_id: 'codex/overall/rate_limit/18000',
    quota_key: 'rate_limit.primary_window',
    scope: 'window',
    group_key: '',
    window_role: 'primary',
    observed_at: `2026-07-23T1${id}:00:00Z`,
    source: 'poll',
    used_percent: id * 10,
    attributed_tokens: id * 100,
    attributed_cost_usd: null,
    attributed_cost_complete: false,
    pricing_snapshot_hash: '',
    ...overrides,
  }
}

describe('capacity detail view model', () => {
  it('joins every backend diagnostic to raw observations without collapsing null into zero', () => {
    const response: UsageQuotaCapacityDetailResponse = {
      estimate: estimate({
        tokens_at_100: 0,
        points: [
          { observation_id: 2, class: 'outlier', cumulative_percent_offset: 0 },
          { observation_id: 1, class: 'included', cumulative_percent_offset: 2 },
        ],
        fitted_series: [{
          observation_id: 1,
          attributed_tokens: 0,
          raw_used_percent: 12,
          adjusted_used_percent: 10,
          cumulative_percent_offset: 2,
          fitted_percent: 9,
        }],
      }),
      observations: [
        observation(2, { attributed_tokens: null, used_percent: null }),
        observation(1, { attributed_tokens: 0, used_percent: 12 }),
      ],
      epochs: [],
    }

    const result = buildCapacityDetailViewModel(response)

    expect(result.points.map((point) => point.observationID)).toEqual([1, 2])
    expect(result.points[0]).toMatchObject({
      className: 'included',
      attributedTokens: 0,
      rawPercent: 12,
      adjustedPercent: 10,
      plotPercent: 10,
      fittedPercent: 9,
      cumulativePercentOffset: 2,
    })
    expect(result.points[1]).toMatchObject({
      className: 'outlier',
      attributedTokens: null,
      rawPercent: null,
      adjustedPercent: null,
      plotPercent: null,
      fittedPercent: null,
    })
    expect(result.tokensAt100).toBe(0)
    expect(result.costAt100).toBeNull()
  })

  it('keeps canonical epoch history stable and strips provider role provenance from keys', () => {
    const response: UsageQuotaCapacityDetailResponse = {
      estimate: estimate({ epoch_reset_at: '2026-07-23T15:00:00Z' }),
      observations: [],
      epochs: [
        estimate({ epoch_reset_at: '2026-07-23T15:00:00Z', tokens_at_100: 1_000 }),
        estimate({ epoch_reset_at: '2026-07-23T10:00:00Z', tokens_at_100: null, confidence: 'insufficient' }),
        estimate({ epoch_reset_at: null, tokens_at_100: 0 }),
      ],
    }

    const result = buildCapacityDetailViewModel(response)

    expect(result.epochs.map((epoch) => epoch.key)).toEqual([
      'codex/overall/rate_limit/18000:2026-07-23T15:00:00Z',
      'codex/overall/rate_limit/18000:2026-07-23T10:00:00Z',
      'codex/overall/rate_limit/18000:unassigned',
    ])
    expect(result.epochs.map((epoch) => epoch.tokensAt100)).toEqual([1_000, null, 0])
    expect(result.epochs.map((epoch) => epoch.selected)).toEqual([true, false, false])
    expect(result.epochs.every((epoch) => !epoch.key.includes('primary'))).toBe(true)
  })

  it('assigns every live classification a unique non-color glyph', () => {
    expect(Object.keys(capacityPointPresentations)).toEqual(pointClasses)
    expect(new Set(Object.values(capacityPointPresentations).map((item) => item.glyph)).size).toBe(pointClasses.length)
  })

  it('scales zero-width and zero-height domains to finite SVG coordinates', () => {
    const response: UsageQuotaCapacityDetailResponse = {
      estimate: estimate({
        tokens_at_100: 0,
        points: [{ observation_id: 1, class: 'included', cumulative_percent_offset: 0 }],
        fitted_series: [{
          observation_id: 1,
          attributed_tokens: 0,
          raw_used_percent: 0,
          adjusted_used_percent: 0,
          cumulative_percent_offset: 0,
          fitted_percent: 0,
        }],
      }),
      observations: [observation(1, { attributed_tokens: 0, used_percent: 0 })],
      epochs: [],
    }
    const viewModel = buildCapacityDetailViewModel(response)

    const geometry = buildCapacityChartGeometry(viewModel, 760, 320)

    expect(geometry.points).toHaveLength(1)
    expect(Number.isFinite(geometry.points[0].x)).toBe(true)
    expect(Number.isFinite(geometry.points[0].y)).toBe(true)
    expect(Number.isFinite(geometry.at100?.x)).toBe(true)
    expect(geometry.fittedPolyline).toHaveLength(1)
    expect(geometry.xDomain[1]).toBeGreaterThan(geometry.xDomain[0])
    expect(geometry.yDomain[1]).toBeGreaterThan(geometry.yDomain[0])
  })

  it('formats bounded and unbounded intervals without inventing a zero upper bound', () => {
    const formatter = (value: number) => `${value} tokens`

    expect(formatCapacityInterval({ low: 0, high: 100 }, formatter, 'unbounded')).toBe('0 tokens - 100 tokens')
    expect(formatCapacityInterval({ low: 900, high: null, unbounded_high: true }, formatter, 'unbounded')).toBe('900 tokens - unbounded')
    expect(formatCapacityInterval(null, formatter, 'unbounded')).toBeNull()
  })
})
