// @vitest-environment happy-dom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthFileCredentialsSection } from '../AuthFileCredentialsSection'
import type { AuthFileCredentialRow } from '../credentialViewModels'

globalThis.IS_REACT_ACT_ENVIRONMENT = true

vi.mock('../CapacityDetailModal', () => ({
  CapacityDetailModal: ({
    open,
    target,
  }: {
    open: boolean
    target: { windowKindID: string; epochResetAt?: string } | null
  }) => open && target
    ? <div data-capacity-detail-modal={`${target.windowKindID}:${target.epochResetAt ?? ''}`} />
    : null,
}))

const translations: Record<string, string> = {
  'usage_stats.credentials_quota_usage_mode_current': 'Current',
  'usage_stats.credentials_quota_usage_mode_estimated': 'Estimated',
  'usage_stats.credentials_quota_capacity_recent_mix': 'At your recent usage mix',
  'usage_stats.credentials_quota_confidence_high': 'High confidence',
  'usage_stats.credentials_quota_confidence_medium': 'Medium confidence',
  'usage_stats.credentials_quota_history_hint': 'History still building',
  'usage_stats.credentials_capacity_open': 'Open capacity evidence',
  'usage_stats.credentials_quota_flag_pricing_changed_suppressed': 'Cost capacity is unavailable because pricing changed during this window.',
  'usage_stats.credentials_quota_flag_pricing_changed_segment': 'Cost capacity uses one consistent pricing segment; token capacity uses the full estimate.',
}

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({
    t: (key: string, params?: Record<string, string | number>) => (
      translations[key] ?? `${key}:${params?.tokens ?? ''}:${params?.cost ?? ''}`
    ),
  }),
}))

const createRow = (): AuthFileCredentialRow => ({
  identity: {
    id: '1',
    name: 'Codex Account',
    auth_type: 1,
    auth_type_name: 'Auth File',
    identity: 'codex-auth',
    type: 'codex',
    provider: 'codex',
    total_requests: 1,
    success_count: 1,
    failure_count: 0,
    input_tokens: 1_000,
    output_tokens: 0,
    reasoning_tokens: 0,
    cache_read_tokens: 0,
    total_tokens: 1_000,
    last_aggregated_usage_event_id: '1',
    is_deleted: false,
    created_at: '2026-07-23T00:00:00Z',
    updated_at: '2026-07-23T00:00:00Z',
  },
  displayName: 'Codex Account',
  maskedIdentity: 'codex-auth',
  providerLabel: 'codex',
  typeLabel: 'codex',
  authTypeLabel: 'Auth File',
  totalRequests: 1,
  successCount: 1,
  failureCount: 0,
  successRate: 100,
  totalTokens: 1_000,
  cacheReadRate: 0,
  quota: [],
  quotaLoading: false,
  displayQuotas: [{
    key: 'rate_limit.primary_window',
    label: '5h',
    percent: 25,
    barPercent: 75,
    percentKind: 'used',
    windowUsage: { tokens: '1.00M', cost: '$2.00' },
    windowUsageEstimate: {
      tokens: '8.00M',
      capacitySource: 'regression',
      confidence: 'medium',
      flags: ['pricing_changed'],
      costCapacity: 'suppressed',
    },
    capacityDetail: {
      windowKindID: 'codex/overall/rate_limit/18000',
      epochResetAt: '2026-07-23T15:00:00Z',
    },
    status: 'ok',
  }],
})

const sectionProps = (row: AuthFileCredentialRow) => ({
  rows: [row],
  total: 1,
  page: 1,
  totalPages: 1,
  pageSize: 10,
  activeOnly: false,
  sort: 'priority' as const,
  loading: false,
  quotaRefreshing: false,
  quotaRefreshError: '',
  quotaInspectionStatus: null,
  quotaInspectionLoading: false,
  quotaInspectionStarting: false,
  quotaInspectionError: '',
  onPageChange: () => undefined,
  onPageSizeChange: () => undefined,
  onActiveOnlyChange: () => undefined,
  onSortChange: () => undefined,
  onRefreshQuota: async () => undefined,
  onRefreshQuotaForAuthIndex: async () => undefined,
  onResetQuotaForAuthIndex: async () => undefined,
  onRefreshInspectionStatus: async () => undefined,
  onStartInspection: async () => undefined,
})

describe('Credentials quota capacity rendering', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => root.unmount())
    container.remove()
  })

  it('switches from current usage to a medium-confidence token-only capacity with honest copy and flag help', async () => {
    await act(async () => {
      root.render(<AuthFileCredentialsSection {...sectionProps(createRow())} />)
    })

    expect(container.textContent).toContain('1.00M')
    expect(container.textContent).toContain('$2.00')

    const estimatedButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent === 'Estimated')
    expect(estimatedButton).not.toBeUndefined()

    await act(async () => estimatedButton?.click())

    expect(container.textContent).toContain('8.00M')
    expect(container.textContent).not.toContain('$2.00')
    expect(container.textContent).toContain('At your recent usage mix')
    expect(container.textContent).toContain('Medium confidence')

    const badge = container.querySelector<HTMLElement>('[data-confidence="medium"]')
    expect(badge).not.toBeNull()

    const flagTarget = container.querySelector<HTMLElement>('[data-capacity-flags]')
    expect(flagTarget?.tabIndex).toBe(0)
    expect(flagTarget?.getAttribute('aria-describedby')).toBeTruthy()
    expect(container.querySelector('[role="tooltip"]')?.textContent).toContain(
      'Cost capacity is unavailable because pricing changed during this window.',
    )
  })

  it('reports an estimated-mode selection to the page data owner', async () => {
    const onQuotaUsageModeChange = vi.fn()
    await act(async () => {
      root.render(
        <AuthFileCredentialsSection
          {...sectionProps(createRow())}
          quotaUsageMode="current"
          onQuotaUsageModeChange={onQuotaUsageModeChange}
        />,
      )
    })

    const estimatedButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent === 'Estimated')
    await act(async () => estimatedButton?.click())

    expect(onQuotaUsageModeChange).toHaveBeenCalledTimes(1)
    expect(onQuotaUsageModeChange).toHaveBeenCalledWith('estimated')
    expect(container.textContent).toContain('1.00M')
    expect(container.textContent).not.toContain('8.00M')
  })

  it('shows high confidence subtly and explains pricing-segment-scoped cost', async () => {
    const row = createRow()
    row.displayQuotas[0].windowUsageEstimate = {
      tokens: '9.00M',
      cost: '$19.00',
      capacitySource: 'regression',
      confidence: 'high',
      flags: ['pricing_changed'],
      costCapacity: 'segment_scoped',
    }

    await act(async () => {
      root.render(
        <AuthFileCredentialsSection
          {...sectionProps(row)}
          quotaUsageMode="estimated"
        />,
      )
    })

    expect(container.textContent).toContain('9.00M')
    expect(container.textContent).toContain('$19.00')
    expect(container.textContent).toContain('High confidence')
    expect(container.querySelector('[data-confidence="high"]')?.className).toContain(
      'credentialQuotaConfidenceBadgeHigh',
    )
    expect(container.querySelector('[role="tooltip"]')?.textContent).toContain(
      'Cost capacity uses one consistent pricing segment; token capacity uses the full estimate.',
    )
  })

  it('uses the one-point value with a small history hint for low confidence', async () => {
    const row = createRow()
    row.displayQuotas[0].windowUsageEstimate = {
      tokens: '4.00M',
      cost: '$8.00',
      capacitySource: 'history',
      historyHint: true,
    }

    await act(async () => {
      root.render(
        <AuthFileCredentialsSection
          {...sectionProps(row)}
          quotaUsageMode="estimated"
        />,
      )
    })

    expect(container.textContent).toContain('4.00M')
    expect(container.textContent).toContain('$8.00')
    expect(container.textContent).toContain('At your recent usage mix')
    expect(container.textContent).toContain('History still building')
    expect(container.querySelector('[data-confidence]')).toBeNull()
  })

  it('preserves the existing one-point presentation when history is unavailable', async () => {
    const row = createRow()
    row.displayQuotas[0].windowUsageEstimate = {
      tokens: '4.00M',
      cost: '$8.00',
    }

    await act(async () => {
      root.render(
        <AuthFileCredentialsSection
          {...sectionProps(row)}
          quotaUsageMode="estimated"
        />,
      )
    })

    expect(container.textContent).toContain('4.00M')
    expect(container.textContent).toContain('$8.00')
    expect(container.textContent).not.toContain('At your recent usage mix')
    expect(container.querySelector('[data-confidence]')).toBeNull()
    expect(container.querySelector('[data-capacity-flags]')).toBeNull()
  })

  it('opens the selected canonical window from the quota-bar evidence affordance', async () => {
    await act(async () => {
      root.render(
        <AuthFileCredentialsSection
          {...sectionProps(createRow())}
          quotaUsageMode="estimated"
        />,
      )
    })

    const openButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Open capacity evidence"]',
    )
    expect(openButton).not.toBeNull()
    await act(async () => openButton?.click())

    expect(document.querySelector('[data-capacity-detail-modal]')?.getAttribute('data-capacity-detail-modal')).toBe(
      'codex/overall/rate_limit/18000:2026-07-23T15:00:00Z',
    )
  })
})
