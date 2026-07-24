import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ApiError, fetchUsageQuotaCapacityDetail } from '@/lib/api'
import type { UsageQuotaCapacityDetailResponse } from '@/lib/types'

export interface CapacityDetailTarget {
  authIndex: string
  windowKindID: string
  epochResetAt?: string
}

interface UseCapacityDetailOptions {
  target: CapacityDetailTarget | null
  onAuthRequired?: () => void
}

export interface CapacityDetailState {
  data: UsageQuotaCapacityDetailResponse | null
  loading: boolean
  error: string
  retry: () => Promise<void>
}

export function useCapacityDetail({
  target,
  onAuthRequired,
}: UseCapacityDetailOptions): CapacityDetailState {
  const [data, setData] = useState<UsageQuotaCapacityDetailResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const controllerRef = useRef<AbortController | null>(null)
  const targetKey = JSON.stringify(target)
  const stableTarget = useMemo(
    () => JSON.parse(targetKey) as CapacityDetailTarget | null,
    [targetKey],
  )

  const load = useCallback(async () => {
    controllerRef.current?.abort()
    controllerRef.current = null
    if (!stableTarget) {
      setData(null)
      setLoading(false)
      setError('')
      return
    }

    const controller = new AbortController()
    controllerRef.current = controller
    setData(null)
    setLoading(true)
    setError('')
    try {
      const response = await fetchUsageQuotaCapacityDetail(
        stableTarget.authIndex,
        stableTarget.windowKindID,
        stableTarget.epochResetAt,
        controller.signal,
      )
      if (controller.signal.aborted || controllerRef.current !== controller) {
        return
      }
      setData(response)
    } catch (nextError) {
      if (controller.signal.aborted || controllerRef.current !== controller) {
        return
      }
      const message = nextError instanceof Error
        ? nextError.message
        : 'Failed to load quota capacity detail'
      setError(message)
      if (nextError instanceof ApiError && nextError.status === 401) {
        onAuthRequired?.()
      }
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null
        setLoading(false)
      }
    }
  }, [onAuthRequired, stableTarget])

  useEffect(() => {
    if (!stableTarget) {
      controllerRef.current?.abort()
      controllerRef.current = null
      let cancelled = false
      queueMicrotask(() => {
        if (cancelled) return
        setData(null)
        setLoading(false)
        setError('')
      })
      return () => {
        cancelled = true
      }
    }

    const timeoutID = window.setTimeout(() => {
      void load()
    }, 0)
    return () => {
      window.clearTimeout(timeoutID)
      controllerRef.current?.abort()
      controllerRef.current = null
    }
  }, [load, stableTarget])

  return {
    data,
    loading,
    error,
    retry: load,
  }
}
