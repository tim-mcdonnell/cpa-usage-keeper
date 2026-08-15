import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import styles from './PriceRulesModal.module.scss'

const VIEWPORT_PADDING = 16
const TOOLTIP_OFFSET = 8
const TOOLTIP_MAX_WIDTH = 420
const TOOLTIP_ESTIMATED_HEIGHT = 144
const POINTER_TRANSFER_GRACE_MS = 120

interface TooltipPosition {
  left: number
  top: number
  width: number
  maxHeight: number
  placement: 'above' | 'below'
}

const initialPosition: TooltipPosition = {
  left: VIEWPORT_PADDING,
  top: VIEWPORT_PADDING,
  width: TOOLTIP_MAX_WIDTH,
  maxHeight: TOOLTIP_ESTIMATED_HEIGHT,
  placement: 'below',
}

export function PriceRulesHelp() {
  const { t } = useTranslation()
  const tooltipId = useId()
  const buttonRef = useRef<HTMLButtonElement | null>(null)
  const tooltipRef = useRef<HTMLSpanElement | null>(null)
  const hoverCloseTimerRef = useRef<number | null>(null)
  const [hovered, setHovered] = useState(false)
  const [focused, setFocused] = useState(false)
  const [touchOpen, setTouchOpen] = useState(false)
  const [position, setPosition] = useState<TooltipPosition>(initialPosition)
  const visible = hovered || focused || touchOpen

  const cancelScheduledHoverClose = useCallback(() => {
    if (hoverCloseTimerRef.current === null) return
    window.clearTimeout(hoverCloseTimerRef.current)
    hoverCloseTimerRef.current = null
  }, [])

  const openForPointer = useCallback((event: ReactPointerEvent) => {
    if (event.pointerType === 'touch') return
    cancelScheduledHoverClose()
    setHovered(true)
  }, [cancelScheduledHoverClose])

  const schedulePointerClose = useCallback((event: ReactPointerEvent) => {
    if (event.pointerType === 'touch') return
    cancelScheduledHoverClose()
    hoverCloseTimerRef.current = window.setTimeout(() => {
      hoverCloseTimerRef.current = null
      setHovered(false)
    }, POINTER_TRANSFER_GRACE_MS)
  }, [cancelScheduledHoverClose])

  const updatePosition = useCallback(() => {
    const button = buttonRef.current
    if (!button?.isConnected) return

    const viewportWidth = window.innerWidth
    const viewportHeight = window.innerHeight
    const rect = button.getBoundingClientRect()
    const width = Math.min(TOOLTIP_MAX_WIDTH, Math.max(0, viewportWidth - VIEWPORT_PADDING * 2))
    const maxLeft = Math.max(VIEWPORT_PADDING, viewportWidth - VIEWPORT_PADDING - width)
    const left = Math.max(VIEWPORT_PADDING, Math.min(rect.left, maxLeft))
    const spaceBelow = Math.max(0, viewportHeight - rect.bottom - TOOLTIP_OFFSET - VIEWPORT_PADDING)
    const spaceAbove = Math.max(0, rect.top - TOOLTIP_OFFSET - VIEWPORT_PADDING)
    const placement = spaceBelow >= TOOLTIP_ESTIMATED_HEIGHT || spaceBelow >= spaceAbove ? 'below' : 'above'

    setPosition({
      left,
      top: placement === 'above' ? rect.top - TOOLTIP_OFFSET : rect.bottom + TOOLTIP_OFFSET,
      width,
      maxHeight: placement === 'above' ? spaceAbove : spaceBelow,
      placement,
    })
  }, [])

  useLayoutEffect(() => {
    if (visible) updatePosition()
  }, [updatePosition, visible])

  useEffect(() => {
    if (!visible) return
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [updatePosition, visible])

  useEffect(() => () => cancelScheduledHoverClose(), [cancelScheduledHoverClose])

  useEffect(() => {
    if (!touchOpen) return
    const closeOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target
      if (!(target instanceof Node)) return
      if (buttonRef.current?.contains(target) || tooltipRef.current?.contains(target)) return
      setTouchOpen(false)
    }
    document.addEventListener('pointerdown', closeOnOutsidePointer)
    return () => document.removeEventListener('pointerdown', closeOnOutsidePointer)
  }, [touchOpen])

  const tooltip = (
    <span
      ref={tooltipRef}
      id={tooltipId}
      role="tooltip"
      aria-hidden={!visible}
      className={`${styles.helpTooltip} ${visible ? styles.helpTooltipVisible : ''}`}
      style={{
        position: 'fixed',
        left: position.left,
        top: position.top,
        width: position.width,
        maxHeight: position.maxHeight,
        transform: position.placement === 'above' ? 'translateY(-100%)' : 'translateY(0)',
      }}
      onPointerEnter={openForPointer}
      onPointerLeave={schedulePointerClose}
    >
      <p>{t('usage_stats.model_price_rules_help')}</p>
      <span className={styles.helpExamplesLabel}>
        {t('usage_stats.model_price_rules_help_examples')}
      </span>
      <span className={styles.helpExamples}>
        <span>{t('usage_stats.model_price_rules_help_service_tier')}</span>
        <span>{t('usage_stats.model_price_rules_help_reasoning_effort')}</span>
      </span>
    </span>
  )

  return (
    <span
      className={styles.help}
      onPointerEnter={openForPointer}
      onPointerLeave={schedulePointerClose}
    >
      <button
        ref={buttonRef}
        type="button"
        className={styles.helpButton}
        aria-label={t('usage_stats.model_price_rules_help_label')}
        aria-describedby={tooltipId}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onPointerDown={(event) => {
          if (event.pointerType !== 'touch') return
          event.preventDefault()
          if (touchOpen) {
            // 触屏浏览器可能持续保留按钮焦点，关闭时同步清理，避免焦点状态再次撑开提示。
            setTouchOpen(false)
            setFocused(false)
            buttonRef.current?.blur()
            return
          }
          setTouchOpen(true)
        }}
      >
        ?
      </button>
      {typeof document === 'undefined' ? tooltip : createPortal(tooltip, document.body)}
    </span>
  )
}
