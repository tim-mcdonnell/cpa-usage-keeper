// @vitest-environment happy-dom

import { StrictMode, act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { UsageQuotaCapacityDetailResponse, UsageQuotaWindowEstimate } from '@/lib/types'
import {
  useCapacityDetail,
  type CapacityDetailState,
  type CapacityDetailTarget,
} from './useCapacityDetail'

globalThis.IS_REACT_ACT_ENVIRONMENT = true

const apiMocks = vi.hoisted(() => {
  class ApiError extends Error {
    status: number

    constructor(message: string, status: number) {
      super(message)
      this.status = status
    }
  }

  return {
    ApiError,
    fetchUsageQuotaCapacityDetail: vi.fn(),
  }
})

vi.mock('@/lib/api', () => apiMocks)

let latest: CapacityDetailState | null = null

function estimate(epochResetAt: string): UsageQuotaWindowEstimate {
  return {
    provider: 'codex',
    window_kind_id: 'codex/overall/rate_limit/18000',
    window_seconds: 18_000,
    epoch_reset_at: epochResetAt,
    sample_count: 0,
    effective_samples: 0,
    distinct_percents: 0,
    percent_resolution: 0,
    percent_span: 0,
    slope: null,
    intercept: null,
    marginal_tokens_per_100: null,
    tokens_at_100: null,
    marginal_tokens_ci_95: null,
    tokens_ci_95: null,
    marginal_cost_per_100: null,
    cost_at_100: null,
    marginal_cost_ci_95: null,
    cost_ci_95: null,
    cost_segment: null,
    r_squared: null,
    slope_instability: null,
    confidence: 'insufficient',
    flags: [],
    points: [],
    fitted_series: [],
    method: 'ols_moving_block_bootstrap_v1',
  }
}

function detailResponse(epochResetAt: string): UsageQuotaCapacityDetailResponse {
  const value = estimate(epochResetAt)
  return { estimate: value, observations: [], epochs: [value] }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function Harness({
  target,
  onAuthRequired,
}: {
  target: CapacityDetailTarget | null
  onAuthRequired?: () => void
}) {
  const result = useCapacityDetail({ target, onAuthRequired })
  useEffect(() => {
    latest = result
  }, [result])
  return null
}

describe('useCapacityDetail', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    latest = null
    apiMocks.fetchUsageQuotaCapacityDetail.mockReset()
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    vi.useRealTimers()
    container.remove()
  })

  const renderHarness = async (target: CapacityDetailTarget | null, onAuthRequired?: () => void) => {
    await act(async () => {
      root.render(
        <StrictMode>
          <Harness target={target} onAuthRequired={onAuthRequired} />
        </StrictMode>,
      )
    })
    await act(async () => vi.runOnlyPendingTimersAsync())
  }

  it('issues one request under Strict Mode and exposes loading through the public state', async () => {
    const request = deferred<UsageQuotaCapacityDetailResponse>()
    apiMocks.fetchUsageQuotaCapacityDetail.mockReturnValue(request.promise)

    await renderHarness({
      authIndex: 'auth-1',
      windowKindID: 'codex/overall/rate_limit/18000',
      epochResetAt: '2026-07-23T15:00:00Z',
    })

    expect(apiMocks.fetchUsageQuotaCapacityDetail).toHaveBeenCalledTimes(1)
    expect(latest?.loading).toBe(true)
    expect(latest?.data).toBeNull()
  })

  it('aborts superseded requests and ignores stale results after an epoch change', async () => {
    const first = deferred<UsageQuotaCapacityDetailResponse>()
    const second = deferred<UsageQuotaCapacityDetailResponse>()
    apiMocks.fetchUsageQuotaCapacityDetail
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)

    await renderHarness({
      authIndex: 'auth-1',
      windowKindID: 'codex/overall/rate_limit/18000',
      epochResetAt: '2026-07-23T15:00:00Z',
    })
    const firstSignal = apiMocks.fetchUsageQuotaCapacityDetail.mock.calls[0]?.[3] as AbortSignal

    await renderHarness({
      authIndex: 'auth-1',
      windowKindID: 'codex/overall/rate_limit/18000',
      epochResetAt: '2026-07-23T10:00:00Z',
    })

    expect(firstSignal.aborted).toBe(true)
    expect(apiMocks.fetchUsageQuotaCapacityDetail).toHaveBeenCalledTimes(2)

    await act(async () => {
      first.resolve(detailResponse('2026-07-23T15:00:00Z'))
      second.resolve(detailResponse('2026-07-23T10:00:00Z'))
      await Promise.all([first.promise, second.promise])
    })

    expect(latest?.data?.estimate.epoch_reset_at).toBe('2026-07-23T10:00:00Z')
    expect(latest?.loading).toBe(false)
    expect(latest?.error).toBe('')
  })

  it('reports errors, supports retry, and requests reauthentication on 401', async () => {
    const onAuthRequired = vi.fn()
    apiMocks.fetchUsageQuotaCapacityDetail
      .mockRejectedValueOnce(new Error('detail unavailable'))
      .mockRejectedValueOnce(new apiMocks.ApiError('unauthorized', 401))

    await renderHarness({
      authIndex: 'auth-1',
      windowKindID: 'codex/overall/rate_limit/18000',
    }, onAuthRequired)

    expect(latest?.error).toBe('detail unavailable')
    expect(latest?.loading).toBe(false)

    await act(async () => {
      void latest?.retry()
      await vi.runOnlyPendingTimersAsync()
    })
    await act(async () => Promise.resolve())

    expect(apiMocks.fetchUsageQuotaCapacityDetail).toHaveBeenCalledTimes(2)
    expect(onAuthRequired).toHaveBeenCalledTimes(1)
    expect(latest?.error).toBe('unauthorized')
  })
})
