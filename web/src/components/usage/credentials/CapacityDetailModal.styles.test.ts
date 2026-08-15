import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const styles = readFileSync(new URL('./CapacityDetailModal.module.scss', import.meta.url), 'utf8')

describe('CapacityDetailModal responsive styles', () => {
  it('keeps the chart and epoch comparison usable at mobile widths', () => {
    expect(styles).toMatch(/@media\s*\(max-width:\s*640px\)/)
    expect(styles).toMatch(/\.chartScroller[\s\S]*overflow-x:\s*auto/)
    expect(styles).toMatch(/\.epochStrip[\s\S]*grid-template-columns:\s*repeat/)
  })

  it('removes chart and modal-local motion for reduced-motion users', () => {
    expect(styles).toMatch(/@media\s*\(prefers-reduced-motion:\s*reduce\)/)
    expect(styles).toMatch(/animation:\s*none/)
    expect(styles).toMatch(/transition:\s*none/)
  })
})
