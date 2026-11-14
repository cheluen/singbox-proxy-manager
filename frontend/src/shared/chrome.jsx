// 面板外围的通用部件：批量操作条、运行状态、测 IP 进度、版本标记、空状态。
import { Button, Dropdown } from 'antd'
import { CircleArrowUp, CopyMinus, Download, Ellipsis, KeyRound, Power, RefreshCw, RotateCcw, Square, Trash2, TriangleAlert, X, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { EMPTY_CRITERIA } from '../core/nodeFilters'
import { isNodeExportable } from '../core/nodeModel'
import { OFFICIAL_GITHUB_URL } from '../core/project'
import { frontendBuildFingerprint, frontendBuildVersion } from '../core/version'
import { CheckBox } from './controls'
import { GithubMark } from './icons'
import { EmptyState, IconButton, Spinner } from './primitives'
import { AddMenu } from './menus'

const icon = (Component) => <Component size={15} strokeWidth={1.75} aria-hidden="true" />

// 选中节点后出现的批量操作条。docked = 固定在内容区底部的悬浮条；否则为行内条。
export function BulkBar({ ctl, variant = 'docked', compact = false }) {
  const { t } = useTranslation()
  const count = ctl.selectedIds.size
  if (count === 0) return null
  const exportable = ctl.selectedNodes.some(isNodeExportable)
  const more = {
    items: [
      { key: 'dedupe', icon: icon(CopyMinus), label: t('outbound_dedup') },
      { type: 'divider' },
      { key: 'delete', icon: icon(Trash2), label: t('batch_delete'), danger: true },
    ],
    onClick: ({ key }) => (key === 'dedupe' ? ctl.dedupeSelected() : ctl.deleteSelected()),
  }
  return (
    <div className={`sb-bulk sb-bulk--${variant}${compact ? ' sb-bulk--compact' : ''}`} role="toolbar" aria-label={t('bulk_actions')} data-testid="dashboard-toolbar-selection">
      <span className="sb-bulk__count" data-testid="nodes-selected-count">
        <strong className="mono">{count}</strong>
        <span>{t('selected_suffix')}</span>
      </span>
      <span className="sb-bulk__sep" />
      <Button type="text" icon={icon(Zap)} loading={Boolean(ctl.checks.progress)} onClick={ctl.checkSelected} data-testid="nodes-batch-check">{t('check_ip')}</Button>
      <Button type="text" icon={icon(KeyRound)} onClick={() => ctl.open({ kind: 'auth' })} data-testid="nodes-batch-auth">{t('set_auth')}</Button>
      <Button type="text" icon={icon(Download)} loading={Boolean(ctl.working.export)} disabled={!exportable} onClick={ctl.exportSelected} data-testid="nodes-batch-export">{t('export')}</Button>
      <span className="sb-bulk__wide">
        <Button type="text" icon={icon(CopyMinus)} loading={Boolean(ctl.working.dedupe)} onClick={ctl.dedupeSelected} data-testid="nodes-batch-dedupe">{t('outbound_dedup')}</Button>
        <Button type="text" danger icon={icon(Trash2)} loading={Boolean(ctl.working.delete)} onClick={ctl.deleteSelected} data-testid="nodes-batch-delete">{t('delete')}</Button>
      </span>
      <span className="sb-bulk__narrow">
        <Dropdown menu={more} trigger={['click']} placement="topRight">
          <Button type="text" icon={icon(Ellipsis)} aria-label={t('more_actions')} data-testid="nodes-batch-more" />
        </Dropdown>
      </span>
      <span className="sb-bulk__sep" />
      <IconButton label={t('clear_selection')} icon={X} onClick={ctl.clearSelection} data-testid="nodes-clear-selection" />
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
      <Button icon={icon(RotateCcw)} loading={ctl.runtimeRestarting} onClick={ctl.restartRuntime} data-testid="runtime-restart">{t('runtime_restart')}</Button>
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
  const tip = `${t('frontend_build')}: ${frontendBuildVersion} (${frontendBuildFingerprint})`
  if (available) {
    return (
      <a className={`sb-version is-update ${className}`} href={update.release_url || `${OFFICIAL_GITHUB_URL}/releases/latest`} target="_blank" rel="noreferrer" data-tip={`${t('update_available')}: ${update.latest_version}`} data-testid="version-badge">
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
