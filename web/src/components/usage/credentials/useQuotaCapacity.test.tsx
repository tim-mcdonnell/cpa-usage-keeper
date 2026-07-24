// @vitest-environment happy-dom

import { StrictMode, act, useEffect } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { UsageQuotaCapacityResponse } from '@/lib/types'
import { useQuotaCapacity, type QuotaCapacityState } from './useQuotaCapacity'

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
    fetchUsageQuotaCapacity: vi.fn(),
  }
})

vi.mock('@/lib/api', () => apiMocks)

let latest: QuotaCapacityState | null = null

function Harness({
  enabled,
  authIndexes,
  onAuthRequired,
}: {
  enabled: boolean
  authIndexes: string[]
  onAuthRequired?: () => void
}) {
  const result = useQuotaCapacity({ enabled, authIndexes, onAuthRequired })
  useEffect(() => {
    latest = result
  }, [result])
  return null
}

function capacityResponse(...authIndexes: string[]): UsageQuotaCapacityResponse {
  return {
    items: authIndexes.map((authIndex) => ({ auth_index: authIndex, windows: [] })),
  }
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

async function flushEffects() {
  await act(async () => {
    await new Promise((resolve) => window.setTimeout(resolve, 5))
  })
}

describe('useQuotaCapacity', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    latest = null
    apiMocks.fetchUsageQuotaCapacity.mockReset()
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
  })

  const renderHarness = async (enabled: boolean, authIndexes: string[], onAuthRequired?: () => void) => {
    await act(async () => {
      root.render(
        <StrictMode>
          <Harness enabled={enabled} authIndexes={authIndexes} onAuthRequired={onAuthRequired} />
        </StrictMode>,
      )
    })
  }

  it('makes one batched request for each estimated-mode page, including under Strict Mode', async () => {
    apiMocks.fetchUsageQuotaCapacity.mockImplementation(async (authIndexes: string[]) => capacityResponse(...authIndexes))

    await renderHarness(true, ['auth-1', 'auth-2'])
    await flushEffects()

    expect(apiMocks.fetchUsageQuotaCapacity).toHaveBeenCalledTimes(1)
    expect(apiMocks.fetchUsageQuotaCapacity).toHaveBeenLastCalledWith(
      ['auth-1', 'auth-2'],
      expect.any(AbortSignal),
    )
    expect(Object.keys(latest?.capacityByAuthIndex ?? {})).toEqual(['auth-1', 'auth-2'])

    await renderHarness(true, ['auth-3'])
    await flushEffects()

    expect(apiMocks.fetchUsageQuotaCapacity).toHaveBeenCalledTimes(2)
    expect(apiMocks.fetchUsageQuotaCapacity).toHaveBeenLastCalledWith(
      ['auth-3'],
      expect.any(AbortSignal),
    )
    expect(Object.keys(latest?.capacityByAuthIndex ?? {}).sort()).toEqual(['auth-1', 'auth-2', 'auth-3'])
  })

  it('does not request capacity outside estimated mode and aborts an in-flight page request when disabled', async () => {
    const request = deferred<UsageQuotaCapacityResponse>()
    apiMocks.fetchUsageQuotaCapacity.mockReturnValue(request.promise)

    await renderHarness(false, ['auth-1'])
    await flushEffects()
    expect(apiMocks.fetchUsageQuotaCapacity).not.toHaveBeenCalled()

    await renderHarness(true, ['auth-1'])
    await flushEffects()
    const signal = apiMocks.fetchUsageQuotaCapacity.mock.calls[0]?.[1] as AbortSignal
    expect(signal.aborted).toBe(false)
    expect(latest?.loading).toBe(true)

    await renderHarness(false, ['auth-1'])

    expect(signal.aborted).toBe(true)
    expect(latest?.loading).toBe(false)
  })

  it('ignores stale page results, preserves cached successes on errors, and reauthenticates on 401', async () => {
    const first = deferred<UsageQuotaCapacityResponse>()
    const second = deferred<UsageQuotaCapacityResponse>()
    const onAuthRequired = vi.fn()
    apiMocks.fetchUsageQuotaCapacity
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
      .mockRejectedValueOnce(new apiMocks.ApiError('unauthorized', 401))

    await renderHarness(true, ['auth-1'], onAuthRequired)
    await flushEffects()
    await renderHarness(true, ['auth-2'], onAuthRequired)
    await flushEffects()

    await act(async () => {
      first.resolve(capacityResponse('auth-1'))
      second.resolve(capacityResponse('auth-2'))
      await Promise.all([first.promise, second.promise])
    })

    expect(latest?.capacityByAuthIndex).toEqual({
      'auth-2': { auth_index: 'auth-2', windows: [] },
    })
    expect(latest?.loading).toBe(false)
    expect(latest?.error).toBe('')

    await renderHarness(true, ['auth-3'], onAuthRequired)
    await flushEffects()

    expect(onAuthRequired).toHaveBeenCalledTimes(1)
    expect(latest?.capacityByAuthIndex).toEqual({
      'auth-2': { auth_index: 'auth-2', windows: [] },
    })
    expect(latest?.error).toBe('unauthorized')
  })
})
