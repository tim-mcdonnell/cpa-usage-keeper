import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { LoadingSpinner } from '@/components/ui/LoadingSpinner'
import { Modal } from '@/components/ui/Modal'
import type {
  UsageQuotaCapacityFlag,
  UsageQuotaCapacityPointClass,
} from '@/lib/types'
import {
  buildCapacityChartGeometry,
  buildCapacityDetailViewModel,
  capacityPointPresentations,
  formatCapacityInterval,
  type CapacityChartPoint,
  type CapacityDetailViewModel,
  type CapacityEpochSummary,
} from './capacityDetailViewModel'
import {
  useCapacityDetail,
  type CapacityDetailTarget,
} from './useCapacityDetail'
import styles from './CapacityDetailModal.module.scss'

export interface CapacityDetailModalTarget extends CapacityDetailTarget {
  credentialName: string
}

interface CapacityDetailModalProps {
  open: boolean
  target: CapacityDetailModalTarget | null
  onClose: () => void
  onAuthRequired?: () => void
}

const CHART_WIDTH = 760
const CHART_HEIGHT = 320
const POINT_CLASSES = Object.keys(capacityPointPresentations) as UsageQuotaCapacityPointClass[]

export function CapacityDetailModal({
  open,
  target,
  onClose,
  onAuthRequired,
}: CapacityDetailModalProps) {
  const { t, i18n } = useTranslation()
  const targetKey = target
    ? `${target.authIndex}\u0000${target.windowKindID}\u0000${target.epochResetAt ?? ''}`
    : ''
  const [selection, setSelection] = useState<{
    targetKey: string
    epochResetAt?: string
  }>({
    targetKey,
    ...(target?.epochResetAt ? { epochResetAt: target.epochResetAt } : {}),
  })
  const selectedEpochResetAt = selection.targetKey === targetKey
    ? selection.epochResetAt
    : target?.epochResetAt

  const requestTarget = open && target
    ? {
        authIndex: target.authIndex,
        windowKindID: target.windowKindID,
        ...(selectedEpochResetAt ? { epochResetAt: selectedEpochResetAt } : {}),
      }
    : null
  const { data, loading, error, retry } = useCapacityDetail({
    target: requestTarget,
    onAuthRequired,
  })
  const viewModel = useMemo(
    () => data ? buildCapacityDetailViewModel(data) : null,
    [data],
  )
  const title = target
    ? t('usage_stats.credentials_capacity_title', { name: target.credentialName })
    : t('usage_stats.credentials_capacity_title_fallback')

  return (
    <Modal
      open={open}
      title={title}
      onClose={() => {
        setSelection({ targetKey: '' })
        onClose()
      }}
      width="min(1080px, calc(100vw - 24px))"
      className={styles.modal}
    >
      <div className={styles.content} aria-busy={loading}>
        {!data && loading && (
          <div className={styles.state} role="status">
            <LoadingSpinner size={22} />
            <span>{t('common.loading')}</span>
          </div>
        )}
        {error && (
          <div className={styles.errorState} role="alert">
            <strong>{t('usage_stats.credentials_capacity_error')}</strong>
            <span>{error}</span>
            <button type="button" onClick={() => void retry()}>
              {t('usage_stats.credentials_capacity_retry')}
            </button>
          </div>
        )}
        {viewModel && !error && (
          <>
            <CapacitySummary
              viewModel={viewModel}
              locale={i18n.language}
              loading={loading}
            />
            <EpochControls
              viewModel={viewModel}
              locale={i18n.language}
              onSelect={(epochResetAt) => {
                setSelection({
                  targetKey,
                  ...(epochResetAt ? { epochResetAt } : {}),
                })
              }}
            />
            <CapacityChart viewModel={viewModel} />
            <CapacityDiagnostics viewModel={viewModel} />
          </>
        )}
      </div>
    </Modal>
  )
}

function CapacitySummary({
  viewModel,
  locale,
  loading,
}: {
  viewModel: CapacityDetailViewModel
  locale: string
  loading: boolean
}) {
  const { t } = useTranslation()
  const formatTokens = (value: number) => formatCompactNumber(value, locale)
  const tokenInterval = formatCapacityInterval(
    viewModel.tokensInterval,
    formatTokens,
    t('usage_stats.credentials_capacity_unbounded'),
  )
  const costInterval = formatCapacityInterval(
    viewModel.costInterval,
    (value) => formatCurrency(value, locale),
    t('usage_stats.credentials_capacity_unbounded'),
  )

  return (
    <section className={styles.summary} aria-label={t('usage_stats.credentials_capacity_summary_aria')}>
      <div className={styles.summaryHeading}>
        <div>
          <p className={styles.eyebrow}>{windowKindLabel(viewModel.windowSeconds, t)}</p>
          <p className={styles.mixLabel}>{t('usage_stats.credentials_quota_capacity_recent_mix')}</p>
        </div>
        <span className={`${styles.confidence} ${styles[`confidence${capitalize(viewModel.confidence)}`]}`}>
          {t(`usage_stats.credentials_capacity_confidence_${viewModel.confidence}`)}
        </span>
        {loading && <LoadingSpinner size={14} className={styles.updatingSpinner} />}
      </div>
      <div className={styles.metricGrid}>
        <CapacityMetric
          label={t('usage_stats.credentials_capacity_tokens_at_100')}
          value={viewModel.tokensAt100 === null
            ? t('usage_stats.credentials_capacity_unavailable')
            : formatTokens(viewModel.tokensAt100)}
          detail={tokenInterval}
        />
        <CapacityMetric
          label={t('usage_stats.credentials_capacity_cost_at_100')}
          value={viewModel.costAt100 === null
            ? t('usage_stats.credentials_capacity_suppressed_cost')
            : formatCurrency(viewModel.costAt100, locale)}
          detail={costInterval}
        />
        <CapacityMetric
          label={t('usage_stats.credentials_capacity_evidence')}
          value={t('usage_stats.credentials_capacity_sample_count', {
            count: viewModel.sampleCount,
          })}
          detail={t('usage_stats.credentials_capacity_effective_count', {
            count: viewModel.effectiveSamples,
          })}
        />
      </div>
      {viewModel.costSegment && (
        <p className={styles.segmentNote}>
          {t('usage_stats.credentials_capacity_segment_scoped', {
            hash: shortHash(viewModel.costSegment.pricing_snapshot_hash),
            start: formatEpochDate(viewModel.costSegment.start, locale),
            end: formatEpochDate(viewModel.costSegment.end, locale),
          })}
        </p>
      )}
      {viewModel.confidence === 'insufficient' && (
        <p className={styles.insufficient}>
          {t('usage_stats.credentials_capacity_insufficient')}
        </p>
      )}
    </section>
  )
}

function CapacityMetric({
  label,
  value,
  detail,
}: {
  label: string
  value: string
  detail: string | null
}) {
  return (
    <div className={styles.metric}>
      <span>{label}</span>
      <strong>{value}</strong>
      {detail && <small>{detail}</small>}
    </div>
  )
}

function EpochControls({
  viewModel,
  locale,
  onSelect,
}: {
  viewModel: CapacityDetailViewModel
  locale: string
  onSelect: (epochResetAt: string | undefined) => void
}) {
  const { t } = useTranslation()
  const epochMaximum = Math.max(
    0,
    ...viewModel.epochs.map((epoch) => epoch.tokensAt100 ?? 0),
  )
  return (
    <section className={styles.epochSection} aria-labelledby="capacity-epochs-title">
      <div className={styles.sectionHeading}>
        <div>
          <h3 id="capacity-epochs-title">{t('usage_stats.credentials_capacity_epochs')}</h3>
          <p>{t('usage_stats.credentials_capacity_epochs_description')}</p>
        </div>
        <label className={styles.epochSelector}>
          <span>{t('usage_stats.credentials_capacity_epoch')}</span>
          <select
            aria-label={t('usage_stats.credentials_capacity_epoch_selector')}
            value={viewModel.epochResetAt ?? ''}
            disabled={viewModel.epochs.length === 0}
            onChange={(event) => onSelect(event.target.value || undefined)}
          >
            {viewModel.epochs.map((epoch) => (
              <option key={epoch.key} value={epoch.resetAt ?? ''}>
                {epochLabel(epoch, locale, t)}
              </option>
            ))}
          </select>
        </label>
      </div>
      {viewModel.epochs.length === 0 ? (
        <p className={styles.emptyEpochs}>{t('usage_stats.credentials_capacity_no_epochs')}</p>
      ) : (
        <ol className={styles.epochStrip}>
          {viewModel.epochs.map((epoch) => {
            const percent = epochMaximum > 0 && epoch.tokensAt100 !== null
              ? Math.max(0, Math.min(100, epoch.tokensAt100 / epochMaximum * 100))
              : 0
            return (
              <li
                key={epoch.key}
                className={epoch.selected ? styles.epochSelected : undefined}
                data-epoch-key={epoch.key}
              >
                <span className={styles.epochDate}>{epochLabel(epoch, locale, t)}</span>
                <span className={styles.epochBar} aria-hidden="true">
                  <span style={{ width: `${percent}%` }} />
                </span>
                <strong>
                  {epoch.tokensAt100 === null
                    ? t('usage_stats.credentials_capacity_insufficient_short')
                    : formatCompactNumber(epoch.tokensAt100, locale)}
                </strong>
              </li>
            )
          })}
        </ol>
      )}
    </section>
  )
}

function CapacityChart({ viewModel }: { viewModel: CapacityDetailViewModel }) {
  const { t, i18n } = useTranslation()
  const geometry = buildCapacityChartGeometry(viewModel, CHART_WIDTH, CHART_HEIGHT)
  const xTicks = tickValues(geometry.xDomain, 4)
  const yTicks = tickValues(geometry.yDomain, 5)
  const toX = (value: number) => geometry.plotLeft
    + ((value - geometry.xDomain[0]) / (geometry.xDomain[1] - geometry.xDomain[0]))
    * (geometry.plotRight - geometry.plotLeft)
  const toY = (value: number) => geometry.plotBottom
    - ((value - geometry.yDomain[0]) / (geometry.yDomain[1] - geometry.yDomain[0]))
    * (geometry.plotBottom - geometry.plotTop)

  return (
    <section className={styles.chartSection} aria-labelledby="capacity-chart-title">
      <div className={styles.sectionHeading}>
        <div>
          <h3 id="capacity-chart-title">{t('usage_stats.credentials_capacity_chart_title')}</h3>
          <p>{t('usage_stats.credentials_capacity_raw_fit_explanation')}</p>
        </div>
      </div>
      <div className={styles.chartScroller}>
        <svg
          className={styles.chart}
          viewBox={`0 0 ${geometry.width} ${geometry.height}`}
          role="img"
          aria-label={t('usage_stats.credentials_capacity_chart_aria')}
        >
          {yTicks.map((tick) => (
            <g key={`y-${tick}`}>
              <line
                className={styles.gridLine}
                x1={geometry.plotLeft}
                x2={geometry.plotRight}
                y1={toY(tick)}
                y2={toY(tick)}
              />
              <text className={styles.axisTick} x={geometry.plotLeft - 10} y={toY(tick) + 4} textAnchor="end">
                {formatPercent(tick, i18n.language)}
              </text>
            </g>
          ))}
          {xTicks.map((tick) => (
            <g key={`x-${tick}`}>
              <line
                className={styles.gridLine}
                x1={toX(tick)}
                x2={toX(tick)}
                y1={geometry.plotTop}
                y2={geometry.plotBottom}
              />
              <text className={styles.axisTick} x={toX(tick)} y={geometry.plotBottom + 21} textAnchor="middle">
                {formatCompactNumber(tick, i18n.language)}
              </text>
            </g>
          ))}
          <text className={styles.axisLabel} x={(geometry.plotLeft + geometry.plotRight) / 2} y={geometry.height - 7} textAnchor="middle">
            {t('usage_stats.credentials_capacity_x_axis')}
          </text>
          <text
            className={styles.axisLabel}
            x={15}
            y={(geometry.plotTop + geometry.plotBottom) / 2}
            textAnchor="middle"
            transform={`rotate(-90 15 ${(geometry.plotTop + geometry.plotBottom) / 2})`}
          >
            {t('usage_stats.credentials_capacity_y_axis')}
          </text>
          {geometry.fittedPolyline.length > 1 && (
            <polyline
              className={styles.fittedLine}
              points={geometry.fittedPolyline.map((point) => `${point.x},${point.y}`).join(' ')}
              data-capacity-fitted-line=""
            />
          )}
          {geometry.at100 && (
            <g data-capacity-at-100="">
              <line
                className={styles.at100Line}
                x1={geometry.at100.x}
                x2={geometry.at100.x}
                y1={geometry.at100.y}
                y2={geometry.plotBottom}
              />
              <circle className={styles.at100Marker} cx={geometry.at100.x} cy={geometry.at100.y} r={6} />
              <text className={styles.at100Label} x={geometry.at100.x - 8} y={geometry.at100.y - 10} textAnchor="end">
                {t('usage_stats.credentials_capacity_at_100_marker')}
              </text>
            </g>
          )}
          {geometry.points.map((point) => (
            <g key={point.observationID}>
              {point.rawY !== null && Math.abs(point.rawY - point.y) > 0.01 && (
                <g data-capacity-raw-marker="">
                  <line
                    className={styles.rawConnector}
                    x1={point.x}
                    x2={point.x}
                    y1={point.rawY}
                    y2={point.y}
                  />
                  <circle className={styles.rawMarker} cx={point.x} cy={point.rawY} r={4} />
                </g>
              )}
              <PointGlyph point={point} />
            </g>
          ))}
        </svg>
      </div>
      {!viewModel.hasFittedSeries && (
        <p className={styles.missingFit}>{t('usage_stats.credentials_capacity_missing_fit')}</p>
      )}
      <ul className={styles.screenReaderPoints}>
        {viewModel.points.map((point) => (
          <li key={point.observationID}>{pointAriaLabel(point, i18n.language, t)}</li>
        ))}
      </ul>
      <div className={styles.legend} aria-label={t('usage_stats.credentials_capacity_legend')}>
        {POINT_CLASSES.map((className) => {
          const presentation = capacityPointPresentations[className]
          return (
            <div key={className}>
              <span
                className={`${styles.legendGlyph} ${styles[`point${capitalize(className)}`]}`}
                data-legend-glyph={presentation.glyph}
                aria-hidden="true"
              >
                {legendGlyph(presentation.glyph)}
              </span>
              <span>{t(presentation.translationKey)}</span>
            </div>
          )
        })}
        <div>
          <span className={styles.legendFit} aria-hidden="true" />
          <span>{t('usage_stats.credentials_capacity_legend_fit')}</span>
        </div>
        <div>
          <span className={styles.legendRaw} aria-hidden="true" />
          <span>{t('usage_stats.credentials_capacity_legend_raw')}</span>
        </div>
      </div>
    </section>
  )
}

function PointGlyph({ point }: { point: CapacityChartPoint }) {
  const presentation = capacityPointPresentations[point.className]
  const className = `${styles.point} ${styles[`point${capitalize(point.className)}`]}`
  const common = {
    className,
    'data-point-class': point.className,
  }
  switch (presentation.glyph) {
    case 'circle':
      return <circle {...common} cx={point.x} cy={point.y} r={5} />
    case 'cross':
      return (
        <g {...common}>
          <line x1={point.x - 5} x2={point.x + 5} y1={point.y - 5} y2={point.y + 5} />
          <line x1={point.x - 5} x2={point.x + 5} y1={point.y + 5} y2={point.y - 5} />
        </g>
      )
    case 'diamond':
      return <path {...common} d={`M ${point.x} ${point.y - 7} L ${point.x + 7} ${point.y} L ${point.x} ${point.y + 7} L ${point.x - 7} ${point.y} Z`} />
    case 'slashed-square':
      return (
        <g {...common}>
          <rect x={point.x - 5} y={point.y - 5} width={10} height={10} />
          <line x1={point.x - 7} x2={point.x + 7} y1={point.y + 7} y2={point.y - 7} />
        </g>
      )
    case 'triangle':
      return <path {...common} d={`M ${point.x} ${point.y - 7} L ${point.x + 7} ${point.y + 6} L ${point.x - 7} ${point.y + 6} Z`} />
    case 'plus':
      return (
        <g {...common}>
          <line x1={point.x - 6} x2={point.x + 6} y1={point.y} y2={point.y} />
          <line x1={point.x} x2={point.x} y1={point.y - 6} y2={point.y + 6} />
        </g>
      )
    case 'ring':
      return <circle {...common} cx={point.x} cy={point.y} r={6} />
  }
}

function CapacityDiagnostics({ viewModel }: { viewModel: CapacityDetailViewModel }) {
  const { t } = useTranslation()
  return (
    <section className={styles.diagnostics} aria-labelledby="capacity-diagnostics-title">
      <div className={styles.sectionHeading}>
        <div>
          <h3 id="capacity-diagnostics-title">{t('usage_stats.credentials_capacity_diagnostics')}</h3>
          <p>{t('usage_stats.credentials_capacity_diagnostics_description')}</p>
        </div>
      </div>
      {viewModel.flags.length === 0 ? (
        <p className={styles.noFlags}>{t('usage_stats.credentials_capacity_no_flags')}</p>
      ) : (
        <ul className={styles.flagList}>
          {viewModel.flags.map((flag) => (
            <li key={flag}>
              <strong>{t(`usage_stats.credentials_capacity_flag_title_${flag}`)}</strong>
              <span>{t(capacityFlagDescriptionKey(flag))}</span>
            </li>
          ))}
        </ul>
      )}
      <aside className={styles.limitation}>
        <strong>{t('usage_stats.credentials_capacity_limitation_title')}</strong>
        <span>{t('usage_stats.credentials_capacity_concurrent_bypass')}</span>
      </aside>
    </section>
  )
}

function capacityFlagDescriptionKey(flag: UsageQuotaCapacityFlag): string {
  return `usage_stats.credentials_quota_flag_${flag}`
}

function pointAriaLabel(
  point: CapacityDetailViewModel['points'][number],
  locale: string,
  t: (key: string, options?: Record<string, string | number>) => string,
): string {
  return t('usage_stats.credentials_capacity_point_aria', {
    id: point.observationID,
    classification: t(capacityPointPresentations[point.className].translationKey),
    tokens: point.attributedTokens === null
      ? t('usage_stats.credentials_capacity_unavailable')
      : formatCompactNumber(point.attributedTokens, locale),
    raw: point.rawPercent === null
      ? t('usage_stats.credentials_capacity_unavailable')
      : formatPercent(point.rawPercent, locale),
    adjusted: point.adjustedPercent === null
      ? t('usage_stats.credentials_capacity_not_adjusted')
      : formatPercent(point.adjustedPercent, locale),
  })
}

function epochLabel(
  epoch: CapacityEpochSummary,
  locale: string,
  t: (key: string, options?: Record<string, string | number>) => string,
): string {
  if (!epoch.resetAt) {
    return t('usage_stats.credentials_capacity_epoch_unassigned')
  }
  return formatEpochDate(epoch.resetAt, locale)
}

function formatEpochDate(value: string, locale: string): string {
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) {
    return value
  }
  return new Intl.DateTimeFormat(locale, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  }).format(date)
}

function formatCompactNumber(value: number, locale: string): string {
  return new Intl.NumberFormat(locale, {
    notation: Math.abs(value) >= 1_000 ? 'compact' : 'standard',
    maximumFractionDigits: 2,
  }).format(value)
}

function formatCurrency(value: number, locale: string): string {
  return new Intl.NumberFormat(locale, {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 2,
  }).format(value)
}

function formatPercent(value: number, locale: string): string {
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value)}%`
}

function tickValues(domain: [number, number], count: number): number[] {
  if (count <= 1) return [domain[0]]
  return Array.from({ length: count }, (_, index) => (
    domain[0] + (domain[1] - domain[0]) * index / (count - 1)
  ))
}

function windowKindLabel(
  windowSeconds: number,
  t: (key: string, options?: Record<string, string | number>) => string,
): string {
  if (windowSeconds === 18_000) {
    return t('usage_stats.credentials_capacity_window_five_hour')
  }
  if (windowSeconds === 604_800) {
    return t('usage_stats.credentials_capacity_window_weekly')
  }
  return t('usage_stats.credentials_capacity_window_seconds', { count: windowSeconds })
}

function shortHash(value: string): string {
  return value.length > 10 ? `${value.slice(0, 10)}...` : value
}

function capitalize(value: string): string {
  return value
    .split('_')
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join('')
}

function legendGlyph(glyph: CapacityPointPresentationGlyph): ReactNode {
  switch (glyph) {
    case 'circle': return '●'
    case 'cross': return '×'
    case 'diamond': return '◆'
    case 'slashed-square': return '▧'
    case 'triangle': return '▲'
    case 'plus': return '+'
    case 'ring': return '○'
  }
}

type CapacityPointPresentationGlyph = (
  typeof capacityPointPresentations
)[UsageQuotaCapacityPointClass]['glyph']
