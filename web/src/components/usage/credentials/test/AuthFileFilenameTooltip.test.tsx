// @vitest-environment happy-dom

import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { AuthFileCredentialsSection } from '../AuthFileCredentialsSection'
import type { AuthFileCredentialRow } from '../credentialViewModels'

const longFileName = 'claude-account-with-a-very-long-workspace-and-profile-name-2026-07-30.json'

const row = (fileName?: string) => ({
  identity: {
    id: 'auth-1',
    identity: 'auth-1',
    file_name: fileName,
    is_deleted: false,
  },
  displayName: 'same@example.com',
  maskedIdentity: 'auth-1',
  providerLabel: 'Claude',
  typeLabel: 'claude',
  authTypeLabel: 'oauth',
  totalRequests: 0,
  successCount: 0,
  failureCount: 0,
  successRate: null,
  totalTokens: 0,
  cacheReadRate: null,
  quota: [],
  quotaLoading: false,
  displayQuotas: [],
}) as AuthFileCredentialRow

const sectionProps = {
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
  onSaveAlias: async () => undefined,
  onRefreshInspectionStatus: async () => undefined,
  onStartInspection: async () => undefined,
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('AuthFileCredentialsSection filename tooltip', () => {
  it('shows the complete filename only in the shared portal tooltip', async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    await act(async () => root.render(<AuthFileCredentialsSection {...sectionProps} rows={[row(longFileName)]} />))

    const name = container.querySelector('[data-auth-file-name-tooltip-target]') as HTMLSpanElement
    expect(name.textContent).toBe('same@example.com')
    expect(name.getAttribute('title')).toBeNull()
    expect(name.tabIndex).toBe(0)
    expect(container.textContent).not.toContain(longFileName)

    await act(async () => name.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
    let tooltip = document.body.querySelector('[role="tooltip"]')
    expect(tooltip?.textContent).toBe(longFileName)
    expect(tooltip?.querySelectorAll('span')).toHaveLength(1)

    await act(async () => name.dispatchEvent(new MouseEvent('mouseout', { bubbles: true })))
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await act(async () => name.focus())
    tooltip = document.body.querySelector('[role="tooltip"]')
    expect(tooltip?.textContent).toBe(longFileName)

    await act(async () => name.blur())
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await act(async () => root.unmount())
    container.remove()
  })

  it('does not make a name without a filename interactive', async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    await act(async () => root.render(<AuthFileCredentialsSection {...sectionProps} rows={[row()]} />))

    const name = container.querySelector('[data-auth-file-name-tooltip-target]') as HTMLSpanElement
    expect(name.tabIndex).toBe(-1)
    await act(async () => name.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await act(async () => root.unmount())
    container.remove()
  })

  it('clears stale tooltip content only when the current filename mapping changes', async () => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    const renderRows = async (rows: AuthFileCredentialRow[]) => {
      await act(async () => root.render(<AuthFileCredentialsSection {...sectionProps} rows={rows} total={rows.length} />))
    }

    await renderRows([row(longFileName)])
    let name = container.querySelector('[data-auth-file-name-tooltip-target]') as HTMLSpanElement
    await act(async () => name.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe(longFileName)

    await renderRows([{ ...row(longFileName), totalRequests: 1 }])
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe(longFileName)

    const renamedFile = 'renamed-claude-account.json'
    await renderRows([row(renamedFile)])
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    name = container.querySelector('[data-auth-file-name-tooltip-target]') as HTMLSpanElement
    await act(async () => name.dispatchEvent(new MouseEvent('mouseover', { bubbles: true })))
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe(renamedFile)

    await renderRows([])
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()

    await act(async () => root.unmount())
    container.remove()
  })
})
