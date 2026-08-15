import antigravityIcon from '@/assets/icons/antigravity.svg'
import claudeIcon from '@/assets/icons/claude.svg'
import codexIcon from '@/assets/icons/codex.svg'
import geminiIcon from '@/assets/icons/gemini.svg'
import kimiIcon from '@/assets/icons/kimi.svg'
import openaiIcon from '@/assets/icons/openai.svg'
import vertexIcon from '@/assets/icons/vertex.svg'
import xaiIcon from '@/assets/icons/xai.svg'
import styles from './ProviderBrandIcon.module.scss'

export const PROVIDER_BRAND_ICON_KEYS = [
  'antigravity',
  'claude',
  'codex',
  'gemini',
  'kimi',
  'openai',
  'vertex',
  'xai',
] as const

export type ProviderBrandIconKey = typeof PROVIDER_BRAND_ICON_KEYS[number]

export interface ProviderBrandIconProps {
  providerType: string | null | undefined
  size: number | string
  ariaLabel?: string
  className?: string
}

// 仅映射 CPA 能稳定提供 identity 的类型；Gemini CLI 兼容行与 Interactions 复用 Gemini 品牌。
const providerBrandIconKeyByType: Readonly<Record<string, ProviderBrandIconKey>> = {
  antigravity: 'antigravity',
  claude: 'claude',
  codex: 'codex',
  gemini: 'gemini',
  'gemini-cli': 'gemini',
  'gemini-interactions': 'gemini',
  kimi: 'kimi',
  openai: 'openai',
  vertex: 'vertex',
  xai: 'xai',
}

// 所有品牌资源统一取自 @lobehub/icons-static-svg@1.94.0；单色品牌跟随 Keeper 主题反相。
const providerBrandIconUrlByKey: Readonly<Record<ProviderBrandIconKey, string>> = {
  antigravity: antigravityIcon,
  claude: claudeIcon,
  codex: codexIcon,
  gemini: geminiIcon,
  kimi: kimiIcon,
  openai: openaiIcon,
  vertex: vertexIcon,
  xai: xaiIcon,
}

const monochromeProviderBrandIconKeys = new Set<ProviderBrandIconKey>(['openai', 'xai'])

export function providerBrandIconKey(providerType: string | null | undefined): ProviderBrandIconKey | undefined {
  const normalized = providerType?.trim().toLowerCase()
  return normalized ? providerBrandIconKeyByType[normalized] : undefined
}

export function ProviderBrandIcon({ providerType, size, ariaLabel, className }: ProviderBrandIconProps) {
  const iconKey = providerBrandIconKey(providerType)
  if (!iconKey) {
    return null
  }

  const rootClassName = `${styles.providerBrandIcon} ${className ?? ''}`.trim()
  const accessibleLabel = ariaLabel?.trim() || undefined
  const monochrome = monochromeProviderBrandIconKeys.has(iconKey)
  const framed = iconKey === 'kimi'
  const tone = monochrome ? 'monochrome' : framed ? 'framed' : undefined

  return (
    <span
      className={rootClassName}
      data-provider-brand-icon={iconKey}
      data-provider-brand-icon-tone={tone}
      style={{ width: size, height: size }}
      role={accessibleLabel ? 'img' : undefined}
      aria-label={accessibleLabel}
      aria-hidden={accessibleLabel ? undefined : true}
    >
      <img className={`${styles.providerBrandIconAsset} ${monochrome ? styles.providerBrandIconMonochrome : ''} ${framed ? styles.providerBrandIconFramed : ''}`.trim()} src={providerBrandIconUrlByKey[iconKey]} alt="" />
    </span>
  )
}
