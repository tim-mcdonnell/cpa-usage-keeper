import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ApiError, fetchUsageQuotaCapacity } from '@/lib/api'
import type { UsageQuotaCapacityItem } from '@/lib/types'

interface UseQuotaCapacityOptions {
  enabled: boolean
  authIndexes: string[]
  onAuthRequired?: () => void
}

export interface QuotaCapacityState {
  capacityByAuthIndex: Record<string, UsageQuotaCapacityItem>
  loading: boolean
  error: string
  refreshQuotaCapacity: () => Promise<void>
}

export function useQuotaCapacity({
  enabled,
  authIndexes,
  onAuthRequired,
}: UseQuotaCapacityOptions): QuotaCapacityState {
  const [capacityByAuthIndex, setCapacityByAuthIndex] = useState<Record<string, UsageQuotaCapacityItem>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const requestControllerRef = useRef<AbortController | null>(null)
  const authIndexesKey = JSON.stringify(authIndexes)
  const stableAuthIndexes = useMemo(() => JSON.parse(authIndexesKey) as string[], [authIndexesKey])

  const refreshQuotaCapacity = useCallback(async () => {
    requestControllerRef.current?.abort()
    requestControllerRef.current = null
    if (!enabled || stableAuthIndexes.length === 0) {
      setLoading(false)
      setError('')
      return
    }

    const controller = new AbortController()
    requestControllerRef.current = controller
    setLoading(true)
    setError('')
    try {
      const response = await fetchUsageQuotaCapacity(stableAuthIndexes, controller.signal)
      if (controller.signal.aborted || requestControllerRef.current !== controller) {
        return
      }
      const returnedAuthIndexes = new Set(response.items.map((item) => item.auth_index))
      setCapacityByAuthIndex((current) => {
        const next = { ...current }
        for (const item of response.items) {
          next[item.auth_index] = item
        }
        for (const authIndex of stableAuthIndexes) {
          if (!returnedAuthIndexes.has(authIndex)) {
            delete next[authIndex]
          }
        }
        return next
      })
    } catch (nextError) {
      if (controller.signal.aborted || requestControllerRef.current !== controller) {
        return
      }
      const message = nextError instanceof Error ? nextError.message : 'Failed to load quota capacity estimates'
      setError(message)
      if (nextError instanceof ApiError && nextError.status === 401) {
        onAuthRequired?.()
      }
    } finally {
      if (requestControllerRef.current === controller) {
        requestControllerRef.current = null
        setLoading(false)
      }
    }
  }, [enabled, onAuthRequired, stableAuthIndexes])

  useEffect(() => {
    if (!enabled || stableAuthIndexes.length === 0) {
      requestControllerRef.current?.abort()
      requestControllerRef.current = null
      return
    }

    // Deferring one task prevents React Strict Mode's development-only effect replay
    // from sending a duplicate page request before its first cleanup runs.
    const timeoutID = window.setTimeout(() => {
      void refreshQuotaCapacity()
    }, 0)
    return () => {
      window.clearTimeout(timeoutID)
      requestControllerRef.current?.abort()
      requestControllerRef.current = null
    }
  }, [enabled, refreshQuotaCapacity, stableAuthIndexes])

  return {
    capacityByAuthIndex,
    loading: enabled && stableAuthIndexes.length > 0 ? loading : false,
    error: enabled && stableAuthIndexes.length > 0 ? error : '',
    refreshQuotaCapacity,
  }
}
