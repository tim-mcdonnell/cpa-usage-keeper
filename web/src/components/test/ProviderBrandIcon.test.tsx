import { existsSync, readFileSync } from 'node:fs'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ProviderBrandIcon, providerBrandIconKey } from '../ProviderBrandIcon'

const providerIconStyles = readFileSync(new URL('../ProviderBrandIcon.module.scss', import.meta.url), 'utf8')

const lobeProviderIconAssets = [
  ['antigravity.svg', 'Antigravity'],
  ['claude.svg', 'Claude'],
  ['codex.svg', 'Codex'],
  ['gemini.svg', 'Gemini'],
  ['kimi.svg', 'Kimi'],
  ['openai.svg', 'OpenAI'],
  ['vertex.svg', 'VertexAI'],
  ['xai.svg', 'Grok'],
] as const

describe('ProviderBrandIcon', () => {
  it('normalizes CPA identity types and supported aliases into the shared brand set', () => {
    expect([
      'antigravity',
      'claude',
      'codex',
      'gemini',
      'kimi',
      'openai',
      'vertex',
      'xai',
    ].map((providerType) => providerBrandIconKey(providerType))).toEqual([
      'antigravity',
      'claude',
      'codex',
      'gemini',
      'kimi',
      'openai',
      'vertex',
      'xai',
    ])
    expect(providerBrandIconKey('gemini-cli')).toBe('gemini')
    expect(providerBrandIconKey('gemini-interactions')).toBe('gemini')
  })

  it('does not assign logos to plugin-only or unsupported identity types', () => {
    expect(providerBrandIconKey('gemini-cli-code-assist')).toBeUndefined()
    expect(providerBrandIconKey('iflow')).toBeUndefined()
    expect(providerBrandIconKey('future-provider')).toBeUndefined()
    expect(providerBrandIconKey(undefined)).toBeUndefined()
  })

  it('renders a decorative icon with an explicit context size by default', () => {
    const html = renderToStaticMarkup(<ProviderBrandIcon providerType="claude" size={30} />)

    expect(html).toContain('data-provider-brand-icon="claude"')
    expect(html).toContain('style="width:30px;height:30px"')
    expect(html).toContain('aria-hidden="true"')
    expect(html).not.toContain('role="img"')
    expect(html).not.toContain('aria-label=')
  })

  it('exposes the provider type when the icon is the only type indicator', () => {
    const html = renderToStaticMarkup(<ProviderBrandIcon providerType="claude" size={30} ariaLabel=" Claude " />)

    expect(html).toContain('role="img"')
    expect(html).toContain('aria-label="Claude"')
    expect(html).not.toContain('aria-hidden=')
  })

  it('accepts a relative size for framed contexts', () => {
    const html = renderToStaticMarkup(<ProviderBrandIcon providerType="claude" size="100%" />)

    expect(html).toContain('style="width:100%;height:100%"')
  })

  it('keeps all eight provider assets on the Lobe Icons 24px SVG canvas', () => {
    for (const [fileName, title] of lobeProviderIconAssets) {
      const assetUrl = new URL(`../../assets/icons/${fileName}`, import.meta.url)
      expect(existsSync(assetUrl), fileName).toBe(true)
      if (!existsSync(assetUrl)) {
        continue
      }
      const source = readFileSync(assetUrl, 'utf8')
      expect(source, fileName).toContain('viewBox="0 0 24 24"')
      expect(source, fileName).toContain(`<title>${title}</title>`)
    }
  })

  it('renders the monochrome Lobe Icons xAI asset with a dark-mode inversion', () => {
    const html = renderToStaticMarkup(<ProviderBrandIcon providerType="xai" size={30} />)

    expect(html).toContain('data-provider-brand-icon="xai"')
    expect(html).toContain('data-provider-brand-icon-tone="monochrome"')
    expect(html.match(/<img/g)).toHaveLength(1)
    expect(providerIconStyles).toMatch(/:global\(\[data-theme='dark'\]\) \.providerBrandIconMonochrome\s*\{[\s\S]*?filter:\s*invert\(1\);/)
  })

  it('keeps the Lobe Icons Kimi color mark visible on a stable dark tile', () => {
    const html = renderToStaticMarkup(<ProviderBrandIcon providerType="kimi" size={30} />)

    expect(html).toContain('data-provider-brand-icon-tone="framed"')
    expect(providerIconStyles).toMatch(/\.providerBrandIconFramed\s*\{[\s\S]*?border-radius:\s*25%;[\s\S]*?background:\s*#050505;/)
  })

  it('renders nothing for a type outside the unified set', () => {
    expect(renderToStaticMarkup(<ProviderBrandIcon providerType="iflow" size={30} />)).toBe('')
  })
})
