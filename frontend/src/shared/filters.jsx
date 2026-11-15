// 筛选相关组件：搜索框、状态分段、高级筛选面板、已生效条件标签。
import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Input, Popover, Select } from 'antd'
import { Search, SlidersHorizontal, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { EMPTY_CRITERIA, UNKNOWN_REGION, countAdvancedCriteria, describeCriteria } from '../core/nodeFilters'
import { parseInboundPortFilterExpression } from '../core/nodeModel'
import { protocolLabel } from '../core/nodeView'
import { statusOptions } from './labels'
import { Kbd, Segmented } from './primitives'

export function SearchBox({ ctl, shortcut = '/', className = '', autoFocus = false }) {
  const { t } = useTranslation()
  const ref = useRef(null)
  const value = ctl.criteria.search
  // 按 / 聚焦搜索框（正在输入其它内容时不拦截）。
  useEffect(() => {
    if (!shortcut) return undefined
    const onKey = (event) => {
      if (event.key !== shortcut || event.metaKey || event.ctrlKey || event.altKey) return
      const tag = document.activeElement?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || document.activeElement?.isContentEditable) return
      if (document.querySelector('.ant-modal-wrap:not([style*="display: none"])')) return
      event.preventDefault()
      ref.current?.focus()
      ref.current?.select()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [shortcut])
  return (
    <div className={`sb-search ${className}`}>
      <Search size={15} strokeWidth={1.75} aria-hidden="true" className="sb-search__icon" />
      <input
        ref={ref}
        type="search"
        value={value}
        autoFocus={autoFocus}
        spellCheck={false}
        autoComplete="off"
        placeholder={t('search_placeholder')}
        aria-label={t('search_placeholder')}
        data-testid="nodes-search"
        onChange={(event) => ctl.setCriteria((prev) => ({ ...prev, search: event.target.value }))}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            if (value) ctl.setCriteria((prev) => ({ ...prev, search: '' }))
            else event.currentTarget.blur()
          }
        }}
      />
      {value ? (
        <button type="button" className="sb-search__clear" aria-label={t('clear')} onClick={() => { ctl.setCriteria((prev) => ({ ...prev, search: '' })); ref.current?.focus() }}>
          <X size={14} strokeWidth={2} aria-hidden="true" />
        </button>
      ) : shortcut ? <Kbd>{shortcut}</Kbd> : null}
    </div>
  )
}

export function StatusFilter({ ctl, size = 'md', className = '' }) {
  const { t } = useTranslation()
  return (
    <Segmented
      label={t('status')}
      size={size}
      className={className}
      testId="status-filter"
      value={ctl.criteria.status}
      options={statusOptions(ctl.summary, t)}
      onChange={(status) => ctl.setCriteria((prev) => ({ ...prev, status }))}
    />
  )
}

function ChipGroup({ options, selected, onToggle, render, testId }) {
  return (
    <div className="sb-chipset" data-testid={testId}>
      {options.map((option) => {
        const active = selected.includes(option.value)
        return (
          <button key={option.value} type="button" className={`sb-chip${active ? ' is-active' : ''}`} aria-pressed={active} data-value={option.value} onClick={() => onToggle(option.value)}>
            <span>{render(option)}</span>
            <span className="sb-chip__count mono">{option.count}</span>
          </button>
        )
      })}
    </div>
  )
}

function AdvancedFilters({ ctl }) {
  const { t } = useTranslation()
  const { criteria, facets } = ctl
  const [ports, setPorts] = useState(criteria.ports)
  useEffect(() => setPorts(criteria.ports), [criteria.ports])
  const portsInvalid = Boolean(ports.trim()) && !parseInboundPortFilterExpression(ports)
  // 范围表达式在输入到一半时必然不合法（例如「30001-」），所以错误提示延迟到停止输入之后再出现。
  const [portsErrorVisible, setPortsErrorVisible] = useState(false)
  useEffect(() => {
    setPortsErrorVisible(false)
    if (!portsInvalid) return undefined
    const timer = window.setTimeout(() => setPortsErrorVisible(true), 700)
    return () => window.clearTimeout(timer)
  }, [ports, portsInvalid])
  const setCriteria = ctl.setCriteria
  const applyPorts = useCallback((value) => {
    if (value.trim() && !parseInboundPortFilterExpression(value)) return
    setCriteria((prev) => (prev.ports === value ? prev : { ...prev, ports: value }))
  }, [setCriteria])
  // 停止输入一小会儿再应用：逐字输入「30001」时，「3」「30」…本身也是合法的端口，立即应用会让列表反复闪空。
  useEffect(() => {
    const timer = window.setTimeout(() => applyPorts(ports), 320)
    return () => window.clearTimeout(timer)
  }, [applyPorts, ports])
  const update = (patch) => setCriteria((prev) => ({ ...prev, ...patch }))
  const toggleIn = (key) => (value) =>
    ctl.setCriteria((prev) => ({ ...prev, [key]: prev[key].includes(value) ? prev[key].filter((item) => item !== value) : [...prev[key], value] }))
  const tri = [{ value: 'any', label: t('any') }, { value: 'on', label: t('enabled') }, { value: 'off', label: t('disabled') }]
  return (
    <div className="sb-filter-panel" data-testid="nodes-filter-panel">
      <div className="sb-filter-panel__group">
        <span className="sb-filter-panel__label">{t('node_type')}</span>
        <ChipGroup testId="filter-types" options={facets.types} selected={criteria.types} onToggle={toggleIn('types')} render={(option) => protocolLabel(option.value)} />
      </div>
      <div className="sb-filter-panel__group">
        <span className="sb-filter-panel__label">{t('location')}</span>
        <ChipGroup testId="filter-regions" options={facets.regions} selected={criteria.regions} onToggle={toggleIn('regions')} render={(option) => (option.value === UNKNOWN_REGION ? t('region_unknown') : [option.value.toUpperCase(), option.name].filter(Boolean).join(' '))} />
      </div>
      <div className="sb-filter-panel__row">
        <label className="sb-filter-panel__group">
          <span className="sb-filter-panel__label">{t('inbound_port')}</span>
          <Input
            value={ports}
            allowClear
            status={portsErrorVisible ? 'error' : undefined}
            placeholder="30001-30005, 30008"
            data-testid="nodes-filter-ports"
            onChange={(event) => setPorts(event.target.value)}
            onBlur={() => applyPorts(ports)}
            onPressEnter={() => applyPorts(ports)}
          />
          {portsErrorVisible ? <span className="sb-linkbar__error" role="alert">{t('filter_ports_invalid')}</span> : null}
        </label>
        <label className="sb-filter-panel__group">
          <span className="sb-filter-panel__label">{t('username')}</span>
          <Select
            allowClear
            showSearch
            value={criteria.username || undefined}
            placeholder={t('any')}
            options={facets.usernames.map((value) => ({ value, label: value }))}
            onChange={(value) => update({ username: value || '' })}
          />
        </label>
      </div>
      <div className="sb-filter-panel__row">
        <div className="sb-filter-panel__group">
          <span className="sb-filter-panel__label">{t('inbound_authentication')}</span>
          <Segmented size="sm" label={t('inbound_authentication')} value={criteria.auth} options={tri} onChange={(auth) => update({ auth })} testId="filter-auth" />
        </div>
        <div className="sb-filter-panel__group">
          <span className="sb-filter-panel__label">{t('tcp_reuse')}</span>
          <Segmented size="sm" label={t('tcp_reuse')} value={criteria.tcpReuse} options={tri} onChange={(tcpReuse) => update({ tcpReuse })} testId="filter-tcp-reuse" />
        </div>
      </div>
      <div className="sb-filter-panel__foot">
        <span className="sb-dim">{t('filter_result_count', { shown: ctl.visibleNodes.length, total: ctl.nodes.length })}</span>
        <Button size="small" type="link" onClick={() => ctl.setCriteria((prev) => ({ ...EMPTY_CRITERIA, search: prev.search, status: prev.status }))} disabled={countAdvancedCriteria(criteria) === 0}>
          {t('clear_filters')}
        </Button>
      </div>
    </div>
  )
}

export function FilterButton({ ctl, compact = false, placement = 'bottomRight' }) {
  const { t } = useTranslation()
  const count = countAdvancedCriteria(ctl.criteria)
  const [open, setOpen] = useState(false)
  const anchorRef = useRef(null)
  // 在面板内按 Esc：关闭面板并把焦点还给「筛选」按钮（下拉选择框展开时，Esc 先交给它自己收起）。
  const handleKeyDown = (event) => {
    if (event.key !== 'Escape' || event.target.closest?.('.ant-select-open')) return
    event.stopPropagation()
    setOpen(false)
    anchorRef.current?.querySelector('button')?.focus()
  }
  return (
    <Popover
      trigger="click"
      open={open}
      onOpenChange={setOpen}
      placement={placement}
      arrow={false}
      destroyOnHidden
      content={<div onKeyDown={handleKeyDown}><AdvancedFilters ctl={ctl} /></div>}
    >
      <span ref={anchorRef} className="sb-popover-anchor">
        <Button icon={<SlidersHorizontal size={15} strokeWidth={1.75} />} className={count > 0 ? 'sb-btn-active' : ''} data-testid="nodes-filter-button" aria-label={t('filters')} aria-expanded={open}>
          {compact ? null : t('filters')}
          {count > 0 ? <span className="sb-count-dot mono">{count}</span> : null}
        </Button>
      </span>
    </Popover>
  )
}

// 已生效的筛选条件：逐个可移除，并给出「显示 x / 共 y」。
export function ActiveFilters({ ctl, className = '' }) {
  const { t } = useTranslation()
  if (!ctl.filtersActive) return null
  const chips = describeCriteria(ctl.criteria, t)
  return (
    <div className={`sb-active-filters ${className}`} data-testid="nodes-active-filters">
      <span className="sb-dim">{t('filter_result_count', { shown: ctl.visibleNodes.length, total: ctl.nodes.length })}</span>
      {chips.map((chip) => (
        <button key={chip.id} type="button" className="sb-chip is-active sb-chip--removable" onClick={() => ctl.setCriteria(chip.clear)} aria-label={`${t('remove_filter')}: ${chip.label}`}>
          <span className="truncate">{chip.label}</span>
          <X size={12} strokeWidth={2.25} aria-hidden="true" />
        </button>
      ))}
      <button type="button" className="sb-link" onClick={() => ctl.setCriteria(EMPTY_CRITERIA)}>{t('clear_filters')}</button>
      {!ctl.canReorder && ctl.visibleNodes.length > 1 ? <span className="sb-dim sb-active-filters__note">{t('drag_sort_disabled_filtered')}</span> : null}
    </div>
  )
}
