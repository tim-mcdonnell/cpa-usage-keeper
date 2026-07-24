// @vitest-environment happy-dom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type {
  UsageQuotaCapacityDetailResponse,
  UsageQuotaCapacityObservation,
  UsageQuotaCapacityPointClass,
  UsageQuotaWindowEstimate,
} from '@/lib/types'
import { CapacityDetailModal, type CapacityDetailModalTarget } from './CapacityDetailModal'

globalThis.IS_REACT_ACT_ENVIRONMENT = true

const hookMocks = vi.hoisted(() => ({
  useCapacityDetail: vi.fn(),
}))

vi.mock('./useCapacityDetail', () => hookMocks)

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, string | number>) => {
      if (!options) return key
      return `${key}:${Object.entries(options).map(([name, value]) => `${name}=${value}`).join(',')}`
    },
    i18n: { language: 'en' },
  }),
}))

const target: CapacityDetailModalTarget = {
  authIndex: 'auth-1',
  credentialName: 'Work Codex',
  windowKindID: 'codex/overall/rate_limit/18000',
  epochResetAt: '2026-07-23T15:00:00Z',
}

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
    window_kind_id: target.windowKindID,
    window_seconds: 18_000,
    epoch_reset_at: target.epochResetAt ?? null,
    sample_count: 7,
    effective_samples: 5,
    distinct_percents: 5,
    percent_resolution: 1,
    percent_span: 40,
    slope: 0.1,
    intercept: 0,
    marginal_tokens_per_100: 1_000,
    tokens_at_100: 1_000,
    marginal_tokens_ci_95: { low: 900, high: null, unbounded_high: true },
    tokens_ci_95: { low: 850, high: 1_150 },
    marginal_cost_per_100: 20,
    cost_at_100: 18,
    marginal_cost_ci_95: { low: 17, high: 22 },
    cost_ci_95: { low: 15, high: 21 },
    cost_segment: {
      pricing_snapshot_hash: 'abc123',
      start: '2026-07-23T10:00:00Z',
      end: '2026-07-23T14:00:00Z',
    },
    r_squared: 0.99,
    slope_instability: 0.1,
    confidence: 'medium',
    flags: ['pricing_changed', 'coverage_gap', 'identity_unverified'],
    points: pointClasses.map((className, index) => ({
      observation_id: index + 1,
      class: className,
      cumulative_percent_offset: index === 0 ? 2 : 0,
    })),
    fitted_series: [{
      observation_id: 1,
      attributed_tokens: 100,
      raw_used_percent: 12,
      adjusted_used_percent: 10,
      cumulative_percent_offset: 2,
      fitted_percent: 10,
    }, {
      observation_id: 5,
      attributed_tokens: 500,
      raw_used_percent: 50,
      adjusted_used_percent: 48,
      cumulative_percent_offset: 2,
      fitted_percent: 50,
    }],
    method: 'ols_moving_block_bootstrap_v1',
    ...overrides,
  }
}

function observation(id: number): UsageQuotaCapacityObservation {
  return {
    id,
    auth_index: target.authIndex,
    provider: 'codex',
    window_kind_id: target.windowKindID,
    quota_key: 'rate_limit.primary_window',
    scope: 'window',
    group_key: '',
    window_role: id % 2 === 0 ? 'secondary' : 'primary',
    observed_at: `2026-07-23T${String(8 + id).padStart(2, '0')}:00:00Z`,
    source: id % 2 === 0 ? 'header' : 'poll',
    used_percent: id * 10 + 2,
    attributed_tokens: id * 100,
    attributed_cost_usd: id * 2,
    attributed_cost_complete: true,
    pricing_snapshot_hash: 'abc123',
  }
}

function detailResponse(overrides: Partial<UsageQuotaCapacityDetailResponse> = {}): UsageQuotaCapacityDetailResponse {
  const selected = estimate()
  return {
    estimate: selected,
    observations: pointClasses.map((_, index) => observation(index + 1)),
    epochs: [
      selected,
      estimate({
        epoch_reset_at: '2026-07-23T10:00:00Z',
        tokens_at_100: null,
        cost_at_100: null,
        confidence: 'insufficient',
        points: [],
        fitted_series: [],
      }),
      estimate({
        epoch_reset_at: '2026-07-23T05:00:00Z',
        tokens_at_100: 0,
        cost_at_100: 0,
        confidence: 'low',
        points: [],
        fitted_series: [],
      }),
    ],
    ...overrides,
  }
}

describe('CapacityDetailModal', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    hookMocks.useCapacityDetail.mockReset()
    hookMocks.useCapacityDetail.mockReturnValue({
      data: detailResponse(),
      loading: false,
      error: '',
      retry: vi.fn(),
    })
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    vi.useRealTimers()
    container.remove()
    document.body.replaceChildren()
  })

  async function renderModal(open = true, onClose = vi.fn()) {
    await act(async () => {
      root.render(
        <CapacityDetailModal
          open={open}
          target={target}
          onClose={onClose}
        />,
      )
      await Promise.resolve()
      await vi.advanceTimersByTimeAsync(0)
      await Promise.resolve()
      await vi.advanceTimersByTimeAsync(0)
    })
    return onClose
  }

  it('renders every backend classification with a distinct glyph and explains raw versus fitted values', async () => {
    await renderModal()

    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
    expect(document.querySelector('svg[aria-label="usage_stats.credentials_capacity_chart_aria"]')).not.toBeNull()
    expect(document.querySelector('[data-capacity-fitted-line]')).not.toBeNull()
    expect(document.querySelector('[data-capacity-at-100]')).not.toBeNull()
    expect(document.querySelector('[data-capacity-raw-marker]')).not.toBeNull()
    expect(Array.from(document.querySelectorAll('[data-point-class]')).map((node) => node.getAttribute('data-point-class'))).toEqual(pointClasses)
    expect(new Set(Array.from(document.querySelectorAll('[data-legend-glyph]')).map((node) => node.getAttribute('data-legend-glyph'))).size).toBe(pointClasses.length)
    expect(document.body.textContent).toContain('usage_stats.credentials_capacity_raw_fit_explanation')
    expect(document.body.textContent).toContain('usage_stats.credentials_capacity_concurrent_bypass')
  })

  it('renders loading, error, insufficient, and missing-fit states without stale plot content', async () => {
    hookMocks.useCapacityDetail.mockReturnValue({
      data: null,
      loading: true,
      error: '',
      retry: vi.fn(),
    })
    await renderModal()
    expect(document.querySelector('[role="status"]')?.textContent).toContain('common.loading')
    expect(document.querySelector('[data-capacity-fitted-line]')).toBeNull()

    const retry = vi.fn()
    hookMocks.useCapacityDetail.mockReturnValue({
      data: null,
      loading: false,
      error: 'detail unavailable',
      retry,
    })
    await renderModal()
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('detail unavailable')
    const retryButton = Array.from(document.querySelectorAll('button')).find((button) => (
      button.textContent === 'usage_stats.credentials_capacity_retry'
    ))
    retryButton?.click()
    expect(retry).toHaveBeenCalledTimes(1)

    const insufficient = estimate({
      confidence: 'insufficient',
      tokens_at_100: null,
      cost_at_100: null,
      points: [{ observation_id: 7, class: 'epoch_unassigned', cumulative_percent_offset: 0 }],
      fitted_series: [],
    })
    hookMocks.useCapacityDetail.mockReturnValue({
      data: detailResponse({ estimate: insufficient, epochs: [] }),
      loading: false,
      error: '',
      retry: vi.fn(),
    })
    await renderModal()
    expect(document.body.textContent).toContain('usage_stats.credentials_capacity_insufficient')
    expect(document.body.textContent).toContain('usage_stats.credentials_capacity_missing_fit')
    expect(document.body.textContent).toContain('usage_stats.credentials_capacity_no_epochs')
  })

  it('requests a newly selected epoch from the same detail endpoint state', async () => {
    await renderModal()
    const selector = document.querySelector<HTMLSelectElement>('[aria-label="usage_stats.credentials_capacity_epoch_selector"]')
    expect(selector).not.toBeNull()

    await act(async () => {
      if (!selector) return
      selector.value = '2026-07-23T10:00:00Z'
      selector.dispatchEvent(new Event('change', { bubbles: true }))
    })

    const lastCall = hookMocks.useCapacityDetail.mock.calls.at(-1)?.[0]
    expect(lastCall.target).toMatchObject({
      authIndex: target.authIndex,
      windowKindID: target.windowKindID,
      epochResetAt: '2026-07-23T10:00:00Z',
    })
  })

  it('restores focus after keyboard dismissal and the reduced-motion close interval', async () => {
    const opener = document.createElement('button')
    opener.textContent = 'Open capacity'
    document.body.appendChild(opener)
    opener.focus()
    const onClose = await renderModal(true)

    const closeButton = document.querySelector<HTMLButtonElement>('button[aria-label="common.close"]')
    expect(closeButton).not.toBeNull()
    closeButton?.focus()

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(onClose).toHaveBeenCalledTimes(1)

    await renderModal(false, onClose)
    await act(async () => vi.advanceTimersByTimeAsync(350))

    expect(document.activeElement).toBe(opener)
  })
})
