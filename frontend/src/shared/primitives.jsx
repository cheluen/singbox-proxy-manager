import { forwardRef, memo, useEffect, useRef, useState } from 'react'
import { Check, Copy, Eye, EyeOff, Loader, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { normalizeCountryCode } from '../core/nodeModel'
import { copyText, latencyTone, nodeHealth, protocolLabel } from '../core/nodeView'
import { LANGUAGES, changeLanguage } from '../core/i18n'
import { HealthGlyph, SignalBars } from './icons'
import { healthLabel } from './labels'

export function Spinner({ size = 16 }) {
  return <Loader size={size} strokeWidth={2} className="sb-spin" aria-hidden="true" />
}

export const IconButton = forwardRef(function IconButton(
  { label, icon: IconComponent, onClick, busy, disabled, size = 'md', tip, className = '', ...rest },
  ref
) {
  return (
    <button
      ref={ref}
      type="button"
      className={`sb-icon-btn sb-icon-btn--${size} ${className}`}
      aria-label={label}
      data-tip={tip === false ? undefined : tip || label}
      disabled={disabled || busy}
      onClick={onClick}
      {...rest}
    >
      {busy ? <Spinner size={size === 'sm' ? 14 : 16} /> : <IconComponent size={size === 'sm' ? 14 : 16} strokeWidth={1.75} aria-hidden="true" />}
    </button>
  )
})

export const HealthBadge = memo(function HealthBadge({ node, checking, compact = false }) {
  const { t } = useTranslation()
  const health = nodeHealth(node)
  const label = checking ? t('checking') : healthLabel(health, t)
  const tip = checking
    ? undefined
    : health === 'failed'
      ? (node?.check_error ? `${label} · ${node.check_error}` : t('check_failed'))
      : health === 'untested'
        ? `${label} · ${t('status_untested_tip')}`
        : undefined
  if (compact) {
    return (
      <span
        className={`sb-health-dot sb-health--${health}`}
        role="img"
        aria-label={label}
        data-health={health}
        data-tip={tip || label}
        data-testid={node?.id ? `node-health-${node.id}` : undefined}
      >
        {checking ? <Spinner size={12} /> : <HealthGlyph health={health} />}
      </span>
    )
  }
  return (
    <span className={`sb-badge sb-health sb-health--${health}`} data-health={health} data-tip={tip}>
      {checking ? <Spinner size={12} /> : <HealthGlyph health={health} />}
      <span>{label}</span>
    </span>
  )
})

export const ProtocolTag = memo(function ProtocolTag({ type }) {
  return <span className="sb-tag sb-tag--protocol">{protocolLabel(type)}</span>
})

export const Latency = memo(function Latency({ value, failed = false, healthy = false, untested = false, error = '' }) {
  const { t } = useTranslation()
  if (failed) {
    const tip = String(error || '').trim() || t('check_failed')
    return (
      <span
        className="sb-latency sb-latency--failed"
        role="img"
        aria-label={tip}
        data-tip={tip}
        data-testid="latency-failed"
      >
        <X size={13} strokeWidth={2.2} aria-hidden="true" />
      </span>
    )
  }
  if (untested) return <span className="sb-dim">—</span>
  const tone = latencyTone(value)
  if (tone === 'none') {
    if (healthy) {
      return (
        <span className="sb-latency" data-tone="fast">
          <SignalBars tone="fast" />
          <span className="mono">&lt;1</span>
          <span className="sb-latency__unit">ms</span>
        </span>
      )
    }
    return <span className="sb-dim">—</span>
  }
  return (
    <span className="sb-latency" data-tone={tone}>
      <SignalBars tone={tone} />
      <span className="mono">{value}</span>
      <span className="sb-latency__unit">ms</span>
    </span>
  )
})

export const Region = memo(function Region({ node, location = node?.location, code: rawCode = node?.country_code, showName = true }) {
  const code = normalizeCountryCode(rawCode)
  const name = showName ? String(location || '').trim() : ''
  if (!code && !name) return <span className="sb-dim">—</span>
  return (
    <span className="sb-region" title={location || undefined}>
      {code ? <span className="sb-region__code mono">{code.toUpperCase()}</span> : null}
      {name ? <span className="truncate">{name}</span> : null}
    </span>
  )
})

export function CopyButton({ text, label, size = 'sm', className = '' }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const handleCopy = async (event) => {
    event.stopPropagation()
    const ok = await copyText(text)
    setCopied(ok)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setCopied(false), 1400)
  }
  return (
    <IconButton
      label={copied ? t('copied') : label || t('copy')}
      icon={copied ? Check : Copy}
      size={size}
      className={`${copied ? 'is-success' : ''} ${className}`}
      disabled={!text}
      onClick={handleCopy}
    />
  )
}

// 凭据默认打码显示，可切换明文并一键复制。
export function SecretText({ value, label }) {
  const { t } = useTranslation()
  const [visible, setVisible] = useState(false)
  if (!value) return <span className="sb-dim">—</span>
  return (
    <span className="sb-secret">
      <span className="mono sb-secret__value">{visible ? value : '•'.repeat(Math.min(10, Math.max(6, value.length)))}</span>
      <IconButton label={visible ? t('hide') : t('show')} icon={visible ? EyeOff : Eye} size="sm" onClick={(event) => { event.stopPropagation(); setVisible((prev) => !prev) }} />
      <CopyButton text={value} label={label ? `${t('copy')} ${label}` : undefined} />
    </span>
  )
}

export function Kbd({ children }) {
  return <kbd className="sb-kbd">{children}</kbd>
}

export function EmptyState({ icon: IconComponent, title, hint, children }) {
  return (
    <div className="sb-empty">
      {IconComponent ? <span className="sb-empty__icon"><IconComponent size={22} strokeWidth={1.5} aria-hidden="true" /></span> : null}
      <p className="sb-empty__title">{title}</p>
      {hint ? <p className="sb-empty__hint">{hint}</p> : null}
      {children ? <div className="sb-empty__actions">{children}</div> : null}
    </div>
  )
}

export function SkeletonRows({ rows = 6, height = 44 }) {
  return (
    <div className="sb-skeleton" aria-hidden="true">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="sb-skeleton__row" style={{ height, animationDelay: `${index * 70}ms` }} />
      ))}
    </div>
  )
}

export function LangSwitch({ className = '' }) {
  const { i18n, t } = useTranslation()
  const current = i18n.language?.startsWith('zh') ? 'zh' : 'en'
  return (
    <div className={`sb-seg sb-seg--sm ${className}`} role="group" aria-label={t('language')}>
      {LANGUAGES.map((language) => (
        <button
          key={language.value}
          type="button"
          className={`sb-seg__item${current === language.value ? ' is-active' : ''}`}
          aria-pressed={current === language.value}
          title={language.label}
          onClick={() => changeLanguage(language.value)}
        >
          {language.short}
        </button>
      ))}
    </div>
  )
}

// 轻量分段选择：options = [{ value, label, count? }]
export function Segmented({ value, options, onChange, label, size = 'md', className = '', testId }) {
  return (
    <div className={`sb-seg sb-seg--${size} ${className}`} role="group" aria-label={label} data-testid={testId}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={`sb-seg__item${value === option.value ? ' is-active' : ''}`}
          aria-pressed={value === option.value}
          data-value={option.value}
          onClick={() => onChange(option.value)}
        >
          <span>{option.label}</span>
          {typeof option.count === 'number' ? <span className="sb-seg__count mono">{option.count}</span> : null}
        </button>
      ))}
    </div>
  )
}

export function Field({ label, children, wide = false }) {
  return (
    <div className={`sb-field${wide ? ' sb-field--wide' : ''}`}>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}
