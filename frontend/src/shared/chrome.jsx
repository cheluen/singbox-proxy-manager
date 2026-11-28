// 面板外围的通用部件：批量操作条、运行状态、测 IP 进度、版本标记、空状态。
import { useEffect, useRef, useState } from 'react'
import { Button, Dropdown } from 'antd'
import { CircleArrowUp, CopyMinus, Download, Ellipsis, FolderInput, KeyRound, Power, PowerOff, RefreshCw, RotateCcw, Square, Trash2, TriangleAlert, X, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { EMPTY_CRITERIA } from '../core/nodeFilters'
import { isNodeExportable } from '../core/nodeModel'
import { OFFICIAL_GITHUB_URL } from '../core/project'
import { frontendBuildVersion } from '../core/version'
import { CheckBox } from './controls'
import { useMediaQuery } from './hooks'
import { GithubMark, MenuIcon } from './icons'
import { EmptyState, IconButton, Spinner } from './primitives'
import { AddMenu } from './menus'

const BULK_LEAVE_FALLBACK_MS = 220

// 选中节点后出现的底部悬浮批量操作条：定位层与动效层分离，进退场丝滑且零水平跳变。
export function BulkBar({ ctl }) {
  const { t } = useTranslation()
  const compact = useMediaQuery('(max-width: 640px)')
  const count = ctl.selectedIds.size
  const active = count > 0
  const exportable = ctl.selectedNodes.some(isNodeExportable)
  const canEnable = ctl.selectedNodes.some((node) => node.enabled === false)
  const canDisable = ctl.selectedNodes.some((node) => node.enabled !== false)

  const [mounted, setMounted] = useState(active)
  const [visible, setVisible] = useState(false)
  const [retained, setRetained] = useState(() => ({ count, exportable, canEnable, canDisable }))
  const barRef = useRef(null)

  useEffect(() => {
    if (!active) return
    setRetained((prev) => (
      prev.count === count &&
      prev.exportable === exportable &&
      prev.canEnable === canEnable &&
      prev.canDisable === canDisable
        ? prev
        : { count, exportable, canEnable, canDisable }
    ))
  }, [active, count, exportable, canEnable, canDisable])

  useEffect(() => {
    const reducedMotion = Boolean(window.matchMedia?.('(prefers-reduced-motion: reduce)').matches)
    if (active) {
      setMounted(true)
      if (reducedMotion) {
        setVisible(true)
        return undefined
      }
      let frame2 = 0
      const frame1 = window.requestAnimationFrame(() => {
        frame2 = window.requestAnimationFrame(() => setVisible(true))
      })
      return () => {
        window.cancelAnimationFrame(frame1)
        window.cancelAnimationFrame(frame2)
      }
    }
    if (barRef.current && document.activeElement instanceof HTMLElement && barRef.current.contains(document.activeElement)) {
      document.activeElement.blur()
    }
    setVisible(false)
    if (reducedMotion) {
      setMounted(false)
      return undefined
    }
    const timer = window.setTimeout(() => setMounted(false), BULK_LEAVE_FALLBACK_MS)
    return () => window.clearTimeout(timer)
  }, [active])

  if (!mounted) return null

  const displayCount = active ? count : retained.count
  const displayExportable = active ? exportable : retained.exportable
  const displayCanEnable = active ? canEnable : retained.canEnable
  const displayCanDisable = active ? canDisable : retained.canDisable
  const enableBusy = Boolean(ctl.working.batchEnable)
  const disableBusy = Boolean(ctl.working.batchDisable)
  const canMoveGroup = (ctl.groups?.length || 0) > 1
  const state = active ? (visible ? 'open' : 'entering') : 'leaving'

  const more = {
    items: [
      ...(canMoveGroup ? [{ key: 'move', icon: <MenuIcon icon={FolderInput} />, label: t('move_to_group'), disabled: Boolean(ctl.working.move) }] : []),
      { key: 'dedupe', icon: <MenuIcon icon={CopyMinus} />, label: t('outbound_dedup'), disabled: Boolean(ctl.working.dedupe) },
      { type: 'divider' },
      { key: 'delete', icon: <MenuIcon icon={Trash2} />, label: t('batch_delete'), danger: true, disabled: Boolean(ctl.working.delete) },
    ],
    onClick: ({ key }) => {
      if (!active) return
      if (key === 'move') ctl.open({ kind: 'move-group', nodeIds: [...ctl.selectedIds] })
      else if (key === 'dedupe') ctl.dedupeSelected()
      else if (key === 'delete') ctl.deleteSelected()
    },
  }

  return (
    <div className="sb-bulk-dock">
      <div
        ref={barRef}
        className={`sb-bulk${state === 'open' ? ' is-open' : ''}${state === 'leaving' ? ' is-leaving' : ''}`}
        role="toolbar"
        aria-label={t('bulk_actions')}
        aria-hidden={!active}
        data-state={state}
        data-testid="dashboard-toolbar-selection"
        onTransitionEnd={(event) => {
          if (!active && event.target === barRef.current && event.propertyName === 'opacity') {
            setMounted(false)
          }
        }}
      >
        <span
          className="sb-bulk__count"
          aria-live="polite"
          aria-atomic="true"
          aria-label={`${displayCount} ${t('selected_suffix')}`}
          data-testid="nodes-selected-count"
        >
          <strong key={displayCount} className="sb-bulk__num mono">{displayCount}</strong>
          <span>{t('selected_suffix')}</span>
        </span>
        <span className="sb-bulk__sep" />
        <Button
          type="text"
          className="sb-bulk__btn--enable"
          icon={<MenuIcon icon={Power} />}
          aria-label={t('batch_enable')}
          data-tip={compact ? t('batch_enable') : undefined}
          loading={enableBusy}
          disabled={!active || !displayCanEnable || disableBusy}
          onClick={ctl.enableSelected}
          data-testid="nodes-batch-enable"
        >
          {t('batch_enable')}
        </Button>
        <Button
          type="text"
          className="sb-bulk__btn--disable"
          icon={<MenuIcon icon={PowerOff} />}
          aria-label={t('batch_disable')}
          data-tip={compact ? t('batch_disable') : undefined}
          loading={disableBusy}
          disabled={!active || !displayCanDisable || enableBusy}
          onClick={ctl.disableSelected}
          data-testid="nodes-batch-disable"
        >
          {t('batch_disable')}
        </Button>
        <Button
          type="text"
          icon={<MenuIcon icon={Zap} />}
          aria-label={t('check_ip')}
          data-tip={compact ? t('check_ip') : undefined}
          loading={Boolean(ctl.checks.progress)}
          disabled={!active}
          onClick={ctl.checkSelected}
          data-testid="nodes-batch-check"
        >
          {t('check_ip')}
        </Button>
        <Button
          type="text"
          icon={<MenuIcon icon={KeyRound} />}
          aria-label={t('set_auth')}
          data-tip={compact ? t('set_auth') : undefined}
          disabled={!active}
          onClick={() => ctl.open({ kind: 'auth' })}
          data-testid="nodes-batch-auth"
        >
          {t('set_auth')}
        </Button>
        <Button
          type="text"
          icon={<MenuIcon icon={Download} />}
          aria-label={t('export')}
          data-tip={compact ? t('export') : undefined}
          loading={Boolean(ctl.working.export)}
          disabled={!active || !displayExportable}
          onClick={ctl.exportSelected}
          data-testid="nodes-batch-export"
        >
          {t('export')}
        </Button>
        <span className="sb-bulk__wide">
          {canMoveGroup ? (
            <Button
              type="text"
              icon={<MenuIcon icon={FolderInput} />}
              loading={Boolean(ctl.working.move)}
              disabled={!active}
              onClick={() => ctl.open({ kind: 'move-group', nodeIds: [...ctl.selectedIds] })}
              data-testid="nodes-batch-move"
            >
              {t('move_to_group')}
            </Button>
          ) : null}
          <Button
            type="text"
            icon={<MenuIcon icon={CopyMinus} />}
            loading={Boolean(ctl.working.dedupe)}
            disabled={!active}
            onClick={ctl.dedupeSelected}
            data-testid="nodes-batch-dedupe"
          >
            {t('outbound_dedup')}
          </Button>
          <Button
            type="text"
            danger
            icon={<MenuIcon icon={Trash2} />}
            loading={Boolean(ctl.working.delete)}
            disabled={!active}
            onClick={ctl.deleteSelected}
            data-testid="nodes-batch-delete"
          >
            {t('delete')}
          </Button>
        </span>
        <span className="sb-bulk__narrow">
          <Dropdown menu={more} trigger={['click']} placement="topRight" disabled={!active}>
            <Button type="text" icon={<MenuIcon icon={Ellipsis} />} aria-label={t('more_actions')} data-tip={t('more_actions')} disabled={!active} data-testid="nodes-batch-more" />
          </Dropdown>
        </span>
        <span className="sb-bulk__sep" />
        <IconButton label={t('clear_selection')} icon={X} disabled={!active} onClick={ctl.clearSelection} data-testid="nodes-clear-selection" />
      </div>
    </div>
  )
}

// 批量测 IP 的进度条 + 停止按钮（替代旧面板里常驻的通知弹层）。
export function CheckProgress({ ctl, className = '' }) {
  const { t } = useTranslation()
  const progress = ctl.checks.progress
  if (!progress) return null
  const percent = Math.round((progress.done / progress.total) * 100)
  return (
    <div className={`sb-progress ${className}`} role="status" data-testid="check-progress">
      <Spinner size={14} />
      <span className="sb-progress__text">{t('batch_check_ip_running', { current: progress.done, total: progress.total })}</span>
      <span className="sb-progress__track" role="progressbar" aria-valuemin={0} aria-valuemax={progress.total} aria-valuenow={progress.done}>
        <span className="sb-progress__fill" style={{ width: `${percent}%` }} />
      </span>
      {progress.failed > 0 ? <span className="sb-progress__failed">{t('check_failed_count', { count: progress.failed })}</span> : null}
      <Button size="small" icon={<Square size={12} strokeWidth={2} />} onClick={ctl.checks.stopChecks} data-testid="check-stop">{t('stop')}</Button>
    </div>
  )
}

// 内核异常横幅：只在降级时出现。
export function RuntimeBanner({ ctl, className = '' }) {
  const { t } = useTranslation()
  if (!ctl.runtime?.degraded) return null
  return (
    <div className={`sb-banner sb-banner--bad ${className}`} role="alert" data-testid="runtime-status-degraded">
      <TriangleAlert size={18} strokeWidth={1.75} aria-hidden="true" />
      <div className="sb-banner__text">
        <strong>{t('runtime_degraded')}</strong>
        <span>{ctl.runtime.message || t('runtime_degraded_desc')}</span>
      </div>
      <Button icon={<MenuIcon icon={RotateCcw} />} loading={ctl.runtimeRestarting} onClick={ctl.restartRuntime} data-testid="runtime-restart">{t('runtime_restart')}</Button>
    </div>
  )
}

// 内核状态指示：正常时安静地显示「运行中」，异常时变为醒目状态并可直接重启。
export function RuntimePill({ ctl, showLabel = true }) {
  const { t } = useTranslation()
  const runtime = ctl.runtime
  const state = !runtime ? 'unknown' : runtime.degraded ? 'bad' : 'ok'
  const label = { unknown: t('runtime_unknown'), bad: t('runtime_degraded'), ok: t('runtime_running') }[state]
  return (
    <span className={`sb-runtime sb-runtime--${state}`} role="status" data-tip={state === 'ok' && runtime?.pid ? `sing-box · PID ${runtime.pid}` : label} data-testid="runtime-pill">
      <span className="sb-runtime__dot" aria-hidden="true">{state === 'bad' ? <Power size={11} strokeWidth={2.25} /> : null}</span>
      {showLabel ? <span>{label}</span> : <span className="sr-only">{label}</span>}
    </span>
  )
}

export function VersionBadge({ ctl, className = '' }) {
  const { t } = useTranslation()
  const update = ctl.version.update
  const available = Boolean(update?.available)
  const tip = `${t('frontend_build')}: ${frontendBuildVersion}`
  const latestHref = update?.latest_release_url || `${OFFICIAL_GITHUB_URL}/releases/latest`
  if (available) {
    return (
      <a className={`sb-version is-update ${className}`} href={latestHref} target="_blank" rel="noreferrer" data-tip={`${t('update_available')}: ${update.latest_version}`} data-testid="version-badge">
        <CircleArrowUp size={13} strokeWidth={2} aria-hidden="true" />
        <span className="mono">v{ctl.version.server || '—'}</span>
        <span className="sr-only">{t('update_available')}: {update.latest_version}</span>
      </a>
    )
  }
  return <span className={`sb-version ${className}`} data-tip={tip} data-testid="version-badge"><span className="mono">v{ctl.version.server || '—'}</span></span>
}

export function RepoLink({ withLabel = false, className = '' }) {
  const { t } = useTranslation()
  return (
    <a className={`sb-repo ${className}`} href={OFFICIAL_GITHUB_URL} target="_blank" rel="noreferrer" aria-label={t('official_repository')} data-tip={withLabel ? undefined : t('official_repository')}>
      <GithubMark size={16} />
      {withLabel ? <span>{t('official_repository')}</span> : null}
    </a>
  )
}

// 表头的全选框：只作用于当前可见（筛选后）的节点。
export function SelectAll({ ctl }) {
  const { t } = useTranslation()
  const total = ctl.visibleNodes.length
  const count = ctl.selectedIds.size
  return <CheckBox checked={total > 0 && count === total} indeterminate={count > 0 && count < total} onChange={ctl.selectAllVisible} label={t('select_all')} testId="nodes-select-all" />
}

export function RefreshButton({ ctl }) {
  const { t } = useTranslation()
  return <IconButton label={t('refresh')} icon={RefreshCw} busy={ctl.loading} onClick={() => ctl.refreshAll()} data-testid="nodes-refresh" />
}

// 列表为空时的两种情况：还没有任何节点 / 筛选后没有结果。
export function NodesEmpty({ ctl, icon: IconComponent }) {
  const { t } = useTranslation()
  if (ctl.nodes.length === 0) {
    return (
      <EmptyState icon={IconComponent} title={t('empty_title')} hint={t('empty_hint')}>
        <AddMenu act={ctl} />
        <Button onClick={() => ctl.open({ kind: 'import' })}>{t('batch_import')}</Button>
      </EmptyState>
    )
  }
  return (
    <EmptyState icon={IconComponent} title={t('empty_filtered_title')} hint={t('empty_filtered_hint')}>
      <Button onClick={() => ctl.setCriteria(EMPTY_CRITERIA)}>{t('clear_filters')}</Button>
    </EmptyState>
  )
}
