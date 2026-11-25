// 设计 4「暮色 · 暗色指挥台」：暗色莫兰迪 + 高密度列表 + 行内展开 + 命令面板，偏键盘操作。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Dropdown } from 'antd'
import {
  Activity, ArrowLeft, ArrowRight, Command, Download, Link2Off, LogOut, Plus, RefreshCw,
  RotateCcw, Route, Rss, ScrollText, Server, Settings, SlidersHorizontal, SquareTerminal, Trash2, Zap,
} from 'lucide-react'
import { buildColumnWidthStyle, calculateTableMinWidth, clampColumnWidth, NODE_LIST_COLUMN_LIMITS } from '../core/columnWidths'
import { EMPTY_CRITERIA } from '../core/nodeFilters'
import { protocolLabel } from '../core/nodeView'
import { ActiveFilters, FilterButton, SearchBox, StatusFilter } from '../shared/filters'
import { BulkBar, CheckProgress, NodesEmpty, RefreshButton, RepoLink, RuntimeBanner, RuntimePill, SelectAll, VersionBadge } from '../shared/chrome'
import { useIncrementalList, useMediaQuery } from '../shared/hooks'
import { BrandLogo, BrandWordmark } from '../shared/Brand'
import { MenuIcon } from '../shared/icons'
import { AddMenu } from '../shared/menus'
import { DetailActions, DetailSections } from '../shared/NodeDetail'
import { Overlays } from '../shared/Overlays'
import { IconButton, Kbd, LangSwitch, SkeletonRows } from '../shared/primitives'
import { SortableList } from '../shared/sortable'
import { usePanel } from '../shared/usePanel'
import { CommandPalette } from './Palette'
import { DuskRow } from './Row'
import ConnectionsPanel from './ConnectionsPanel'
import LogsPanel from './LogsPanel'

const isMac = /Mac|iPhone|iPad/.test(navigator.platform)
const typingInField = () => {
  const element = document.activeElement
  return Boolean(element && (['INPUT', 'TEXTAREA', 'SELECT'].includes(element.tagName) || element.isContentEditable))
}
const overlayOpen = () => Boolean(document.querySelector('.ant-modal-wrap:not([style*="display: none"]), .ant-dropdown:not(.ant-dropdown-hidden), .ant-popover:not(.ant-popover-hidden)'))

function ResizableHeaderCell({ colKey, label, t, className = '', active, currentWidth, onResizeStart, onReset, onStep }) {
  const limits = NODE_LIST_COLUMN_LIMITS[colKey]
  const hint = t('resize_column_hint', { column: label })
  return (
    <div className={`dusk-head__cell ${className}${active ? ' is-resizing' : ''}`} data-col-key={colKey}>
      <span className="dusk-head__label">{label}</span>
      <button
        type="button"
        role="separator"
        aria-orientation="vertical"
        aria-label={hint}
        aria-valuemin={limits?.min}
        aria-valuemax={limits?.max}
        aria-valuenow={currentWidth ?? undefined}
        data-tip={hint}
        data-testid={`col-resizer-${colKey}`}
        className={`dusk-col-resizer${active ? ' is-active' : ''}`}
        onPointerDown={(event) => onResizeStart(colKey, event)}
        onDoubleClick={(event) => {
          event.preventDefault()
          event.stopPropagation()
          onReset(colKey)
        }}
        onKeyDown={(event) => {
          if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
            event.preventDefault()
            event.stopPropagation()
            const delta = (event.key === 'ArrowRight' ? 1 : -1) * (event.shiftKey ? 24 : 12)
            onStep(colKey, delta, event.currentTarget.parentElement)
          } else if (event.key === 'Enter' || event.key === 'Backspace' || event.key === 'Delete') {
            event.preventDefault()
            event.stopPropagation()
            onReset(colKey)
          }
        }}
      />
    </div>
  )
}

function GroupBar({ ctl, t, onSelectGroup }) {
  const groups = ctl.groups || []
  const active = ctl.activeGroup
  const activeIndex = groups.findIndex((item) => item.id === active?.id)
  const hasSub = Boolean(String(active?.subscription_url || '').trim())
  const subBusy = Boolean(active?.id && ctl.refreshingGroupId === active.id)

  const shiftGroup = (delta) => {
    if (activeIndex < 0) return
    const target = activeIndex + delta
    if (target < 0 || target >= groups.length) return
    const ids = groups.map((item) => item.id)
    ;[ids[activeIndex], ids[target]] = [ids[target], ids[activeIndex]]
    void ctl.reorderGroups(ids)
  }

  const groupMenu = {
    items: [
      { key: 'edit', icon: <MenuIcon icon={SlidersHorizontal} />, label: t('group_edit'), disabled: !active },
      ...(hasSub ? [{ key: 'refresh-sub', icon: <MenuIcon icon={RefreshCw} />, label: t('group_subscription_refresh') }] : []),
      { type: 'divider' },
      { key: 'move-left', icon: <MenuIcon icon={ArrowLeft} />, label: t('group_move_left'), disabled: activeIndex <= 0 },
      { key: 'move-right', icon: <MenuIcon icon={ArrowRight} />, label: t('group_move_right'), disabled: activeIndex < 0 || activeIndex >= groups.length - 1 },
      { type: 'divider' },
      { key: 'delete', icon: <MenuIcon icon={Trash2} />, label: t('group_delete'), danger: true, disabled: !active || groups.length <= 1 },
    ],
    onClick: ({ key }) => {
      if (!active) return
      if (key === 'edit') ctl.open({ kind: 'group-form', group: active })
      else if (key === 'refresh-sub') void ctl.refreshGroupSubscription(active)
      else if (key === 'move-left') shiftGroup(-1)
      else if (key === 'move-right') shiftGroup(1)
      else if (key === 'delete') ctl.open({ kind: 'group-delete', group: active })
    },
  }

  if (groups.length === 0) return null

  return (
    <div className="dusk-groups" role="region" aria-label={t('groups_bar_label')} data-testid="node-groups-bar">
      <div className="dusk-groups__tabs" role="tablist" aria-label={t('groups_bar_label')}>
        {groups.map((group) => {
          const selected = group.id === active?.id
          const count = typeof group.node_count === 'number'
            ? group.node_count
            : ctl.allNodes.filter((node) => node.group_id === group.id).length
          const groupHasSub = Boolean(String(group.subscription_url || '').trim())
          const subErr = Boolean(group.subscription_last_error)
          return (
            <button
              key={group.id}
              type="button"
              role="tab"
              aria-selected={selected}
              className={`dusk-group-tab${selected ? ' is-active' : ''}`}
              data-testid={`group-tab-${group.id}`}
              data-group-id={group.id}
              onClick={() => onSelectGroup(group.id)}
            >
              <span className="dusk-group-tab__name">{group.name}</span>
              <span className="dusk-group-tab__count mono">{count}</span>
              {group.allocate_port === false ? (
                <span className="dusk-group-tab__badge" data-tip={t('group_no_port_badge')} aria-label={t('group_no_port_badge')}>
                  <Link2Off size={12} strokeWidth={1.9} aria-hidden="true" />
                </span>
              ) : null}
              {group.upstream_enabled ? (
                <span
                  className="dusk-group-tab__badge is-upstream"
                  data-tip={t('group_upstream_active_tip', { type: protocolLabel(group.upstream_type, t) })}
                  aria-label={t('group_upstream_badge')}
                >
                  <Route size={12} strokeWidth={1.9} aria-hidden="true" />
                </span>
              ) : null}
              {groupHasSub ? (
                <span
                  className={`dusk-group-tab__badge${subErr ? ' is-err' : ''}`}
                  data-tip={subErr ? `${t('group_subscription_last_error')}: ${group.subscription_last_error}` : t('group_subscription')}
                  aria-label={t('group_subscription')}
                >
                  <Rss size={12} strokeWidth={1.9} aria-hidden="true" />
                </span>
              ) : null}
            </button>
          )
        })}
      </div>

      <div className="dusk-groups__actions">
        {active ? (
          <span className="dusk-groups__meta">
            {active.allocate_port !== false ? (
              <span className="dusk-groups__pill mono" data-testid="active-group-port-pill" data-tip={t('start_port')}>
                :{active.start_port}
              </span>
            ) : (
              <span className="dusk-groups__pill is-noport" data-testid="active-group-noport-pill">
                <Link2Off size={11} strokeWidth={1.9} aria-hidden="true" />
                {t('group_no_port_badge')}
              </span>
            )}
            {active.upstream_enabled ? (
              <span
                className="dusk-groups__pill is-upstream"
                data-testid="active-group-upstream-pill"
                data-tip={t('group_upstream_active_tip', { type: protocolLabel(active.upstream_type, t) })}
              >
                <Route size={11} strokeWidth={1.9} aria-hidden="true" />
                {`${t('group_upstream_badge')} · ${protocolLabel(active.upstream_type, t)}`}
              </span>
            ) : null}
            {active.subscription_last_error ? (
              <span className="dusk-groups__pill is-err" data-tip={active.subscription_last_error}>
                {t('group_sub_error_short')}
              </span>
            ) : null}
          </span>
        ) : null}
        {hasSub ? (
          <IconButton
            size="sm"
            label={t('group_subscription_refresh')}
            icon={RefreshCw}
            busy={subBusy}
            onClick={() => void ctl.refreshGroupSubscription(active)}
            data-testid="group-sub-refresh"
          />
        ) : null}
        <Dropdown menu={groupMenu} trigger={['click']} placement="bottomRight">
          <IconButton
            size="sm"
            label={t('group_manage')}
            icon={SlidersHorizontal}
            data-testid="group-settings-button"
          />
        </Dropdown>
        <IconButton
          size="sm"
          label={t('group_create')}
          icon={Plus}
          onClick={() => ctl.open({ kind: 'group-form', group: null })}
          data-testid="group-create-button"
        />
      </div>
    </div>
  )
}

export default function DuskPanel({ session }) {
  const { ctl, act, t } = usePanel(session)
  const [activeTab, setActiveTab] = useState('nodes')
  const [expandedId, setExpandedId] = useState(null)
  const [focusedId, setFocusedId] = useState(null)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [draftWidths, setDraftWidths] = useState(null)
  const [resizingCol, setResizingCol] = useState(null)
  const [viewportWidth, setViewportWidth] = useState(() => (typeof window === 'undefined' ? 1440 : window.innerWidth))
  const [tableClientWidth, setTableClientWidth] = useState(0)
  const [pendingJump, setPendingJump] = useState(null)
  const tableRef = useRef(null)
  const dragCleanupRef = useRef(null)
  const desktop = useMediaQuery('(min-width: 900px)')
  const { shown, hasMore, sentinelRef, reveal } = useIncrementalList(ctl.visibleNodes)
  const ids = useMemo(() => shown.map((node) => node.id), [shown])
  const effectiveWidths = draftWidths ?? ctl.columnWidths
  const hasCustomWidths = Object.keys(effectiveWidths).length > 0
  const tableMinWidth = desktop ? calculateTableMinWidth(effectiveWidths, viewportWidth) : 0
  const tableScrollable = desktop && hasCustomWidths && tableMinWidth > 0 && (tableClientWidth === 0 || tableMinWidth > tableClientWidth + 1)
  const tableStyle = useMemo(
    () => buildColumnWidthStyle(effectiveWidths, desktop, viewportWidth),
    [desktop, effectiveWidths, viewportWidth]
  )
  // 键盘监听只注册一次，通过 ref 读取最新状态。
  const stateRef = useRef({ ctl, shown, focusedId, activeTab })
  useEffect(() => {
    stateRef.current = { ctl, shown, focusedId, activeTab }
  })

  useEffect(() => {
    const onResize = () => setViewportWidth(window.innerWidth)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  useEffect(() => {
    const node = tableRef.current
    if (!node) return undefined
    const update = () => setTableClientWidth(node.clientWidth)
    update()
    if (typeof ResizeObserver === 'undefined') return undefined
    const observer = new ResizeObserver(update)
    observer.observe(node)
    return () => observer.disconnect()
  }, [])

  useEffect(() => () => {
    dragCleanupRef.current?.()
  }, [])

  const startColumnResize = useCallback((colKey, event) => {
    if (event.button !== 0) return
    event.preventDefault()
    event.stopPropagation()
    const cell = event.currentTarget.parentElement
    if (!cell) return
    dragCleanupRef.current?.()

    const startX = event.clientX
    const startWidth = Math.round(cell.getBoundingClientRect().width)
    const baseWidths = { ...stateRef.current.ctl.columnWidths }
    let latestWidths = baseWidths
    let moved = false

    const handle = event.currentTarget
    try { handle.setPointerCapture?.(event.pointerId) } catch { /* 兼容非指针捕获环境 */ }
    setResizingCol(colKey)
    document.body.classList.add('is-resizing-cols')

    const onMove = (moveEvent) => {
      const next = clampColumnWidth(colKey, startWidth + (moveEvent.clientX - startX))
      if (next === null) return
      if (latestWidths[colKey] === next) return
      moved = true
      latestWidths = { ...latestWidths, [colKey]: next }
      setDraftWidths(latestWidths)
    }

    const finish = (commit) => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onCancel)
      document.body.classList.remove('is-resizing-cols')
      try { handle.releasePointerCapture?.(event.pointerId) } catch { /* 忽略 */ }
      dragCleanupRef.current = null
      setResizingCol(null)
      setDraftWidths(null)
      if (commit && moved) void stateRef.current.ctl.saveColumnWidths(latestWidths)
    }
    const onUp = () => finish(true)
    const onCancel = () => finish(false)

    dragCleanupRef.current = () => finish(false)
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onCancel)
  }, [])

  const resetColumnWidth = useCallback((colKey) => {
    const current = stateRef.current.ctl.columnWidths
    if (!Object.hasOwn(current, colKey)) return
    const next = { ...current }
    delete next[colKey]
    void stateRef.current.ctl.saveColumnWidths(next)
  }, [])

  const stepColumnWidth = useCallback((colKey, delta, cell) => {
    const current = stateRef.current.ctl.columnWidths
    const measured = current[colKey] ?? Math.round(cell?.getBoundingClientRect().width || NODE_LIST_COLUMN_LIMITS[colKey]?.fallbackMin || 100)
    const clamped = clampColumnWidth(colKey, measured + delta)
    if (clamped === null || current[colKey] === clamped) return
    void stateRef.current.ctl.saveColumnWidths({ ...current, [colKey]: clamped })
  }, [])

  const resetAllColumnWidths = useCallback(() => {
    if (Object.keys(stateRef.current.ctl.columnWidths).length === 0) return
    void stateRef.current.ctl.saveColumnWidths({})
  }, [])

  const handleSelectGroup = useCallback((groupId) => {
    setExpandedId(null)
    setFocusedId(null)
    ctl.selectGroup(groupId)
  }, [ctl])

  const toggleExpand = useCallback((id) => setExpandedId((prev) => (prev === id ? null : id)), [])
  const jumpTo = useCallback((id) => {
    setActiveTab('nodes')
    const currentCtl = stateRef.current.ctl
    const pool = currentCtl.allNodes || currentCtl.nodes
    const targetNode = pool.find((node) => node.id === id)
    const fallbackGroupId = currentCtl.groups?.[0]?.id ?? null
    const targetGroupId = targetNode?.group_id ?? currentCtl.activeGroupId ?? fallbackGroupId
    if (targetGroupId != null && targetGroupId !== currentCtl.activeGroupId) {
      currentCtl.selectGroup(targetGroupId)
    }
    // 目标节点可能被当前筛选隐藏，跳转前先清空筛选。
    currentCtl.setCriteria(EMPTY_CRITERIA)
    const targetGroupNodes = targetGroupId == null
      ? pool
      : pool.filter((node) => (node.group_id == null ? targetGroupId === fallbackGroupId : node.group_id === targetGroupId))
    const index = targetGroupNodes.findIndex((node) => node.id === id)
    if (index >= 0) reveal(index)
    setPendingJump((prev) => ({ id, groupId: targetGroupId, seq: (prev?.seq || 0) + 1 }))
    setFocusedId(id)
    setExpandedId(id)
  }, [reveal])

  useEffect(() => {
    if (!pendingJump || activeTab !== 'nodes') return
    if (pendingJump.groupId != null && ctl.activeGroupId !== pendingJump.groupId) return
    const index = ctl.visibleNodes.findIndex((node) => node.id === pendingJump.id)
    if (index < 0) return
    if (index >= shown.length) {
      reveal(index)
      return
    }
    const targetId = pendingJump.id
    const targetSeq = pendingJump.seq
    const frame = window.requestAnimationFrame(() => {
      document.querySelector(`[data-row-id="${targetId}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
      setPendingJump((current) => (current?.seq === targetSeq ? null : current))
    })
    return () => window.cancelAnimationFrame(frame)
  }, [activeTab, ctl.activeGroupId, ctl.visibleNodes, pendingJump, reveal, shown.length])

  // 键盘操作：Ctrl/⌘K 命令面板；j/k 或方向键移动；Enter 展开；x 选择；e 编辑；t 测 IP。
  useEffect(() => {
    const onKey = (event) => {
      // 拖拽手柄、列宽调节手柄和抓起状态下的方向键、回车、空格、Esc 由对应控件独占。
      if (event.target?.closest?.('.sb-drag, .dusk-col-resizer') || document.querySelector('.is-dragging')) return
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen((prev) => !prev)
        return
      }
      if (stateRef.current.activeTab !== 'nodes') return
      if (event.metaKey || event.ctrlKey || event.altKey || typingInField() || overlayOpen()) return
      const { ctl: current, shown: rows, focusedId: focus } = stateRef.current
      const index = rows.findIndex((node) => node.id === focus)
      const node = index >= 0 ? rows[index] : null
      const move = (delta) => {
        if (rows.length === 0) return
        const next = rows[Math.min(rows.length - 1, Math.max(0, index < 0 ? 0 : index + delta))]
        setFocusedId(next.id)
        document.querySelector(`[data-row-id="${next.id}"]`)?.scrollIntoView({ block: 'nearest' })
      }
      const key = event.key
      if (key === 'j' || key === 'ArrowDown') { event.preventDefault(); move(1) }
      else if (key === 'k' || key === 'ArrowUp') { event.preventDefault(); move(-1) }
      else if (key === 'Escape') { setExpandedId(null) }
      else if (!node) { return }
      else if (key === 'Enter') {
        // 焦点在按钮或链接上时，Enter 属于那个控件，不重复触发展开。
        if (document.activeElement?.closest?.('button, a, [role="switch"], [role="button"]')) return
        event.preventDefault()
        toggleExpand(node.id)
      }
      else if (key === 'x') { current.toggleSelect(node.id) }
      else if (key === 'e') { current.open({ kind: 'node', node }) }
      else if (key === 't') { current.checkNodes([node.id]) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggleExpand])

  const dragLabel = ctl.canReorder || ctl.visibleNodes.length < 2 ? t('drag_sort_hint_short') : t('drag_sort_disabled_filtered')
  const navItems = [
    { key: 'nodes', icon: Server, label: t('nav_nodes') },
    { key: 'connections', icon: Activity, label: t('nav_connections') },
    { key: 'logs', icon: ScrollText, label: t('nav_logs') },
  ]
  const exportMenu = {
    items: [
      { key: 'group', icon: <MenuIcon icon={Download} />, label: t('export_current_group') },
      { key: 'all', icon: <MenuIcon icon={Download} />, label: t('export_all_groups') },
    ],
    onClick: ({ key }) => void ctl.exportScope(key),
  }

  return (
    <div className="dusk">
      <header className="dusk-top">
        <div className="dusk-top__brand">
          <BrandLogo />
          <strong className="dusk-top__title" lang="en" data-testid="project-brand"><BrandWordmark /></strong>
        </div>
        <RuntimePill ctl={ctl} showLabel={desktop} />
        {desktop ? (
          <nav className="dusk-nav" aria-label={t('main_nav')} data-testid="main-nav">
            {navItems.map((item) => {
              const Icon = item.icon
              return (
                <button
                  key={item.key}
                  type="button"
                  className={`dusk-nav__btn${activeTab === item.key ? ' is-active' : ''}`}
                  aria-current={activeTab === item.key ? 'page' : undefined}
                  data-testid={`nav-tab-${item.key}`}
                  onClick={() => setActiveTab(item.key)}
                >
                  <Icon size={14} strokeWidth={1.8} aria-hidden="true" />
                  <span>{item.label}</span>
                </button>
              )
            })}
          </nav>
        ) : null}
        <button type="button" className="dusk-cmd" aria-label={t('cmd_open')} onClick={() => setPaletteOpen(true)} data-testid="open-palette">
          <Command size={14} strokeWidth={1.75} aria-hidden="true" />
          <span className="truncate">{t('cmd_open')}</span>
          <Kbd>{isMac ? '⌘' : 'Ctrl'}</Kbd><Kbd>K</Kbd>
        </button>
        <span className="dusk-top__spacer" />
        {desktop ? <VersionBadge ctl={ctl} /> : null}
        {desktop ? <RepoLink /> : null}
        <LangSwitch />
        <IconButton label={t('settings')} icon={Settings} onClick={() => ctl.open({ kind: 'settings' })} data-testid="open-settings" />
        <IconButton label={t('logout')} icon={LogOut} onClick={() => session.logout()} data-testid="logout" />
      </header>

      {!desktop ? (
        <div className="dusk-mobile-nav">
          <nav className="dusk-nav" aria-label={t('main_nav')} data-testid="main-nav">
            {navItems.map((item) => {
              const Icon = item.icon
              return (
                <button
                  key={item.key}
                  type="button"
                  className={`dusk-nav__btn${activeTab === item.key ? ' is-active' : ''}`}
                  aria-current={activeTab === item.key ? 'page' : undefined}
                  data-testid={`nav-tab-${item.key}`}
                  onClick={() => setActiveTab(item.key)}
                >
                  <Icon size={14} strokeWidth={1.8} aria-hidden="true" />
                  <span>{item.label}</span>
                </button>
              )
            })}
          </nav>
        </div>
      ) : null}

      {activeTab === 'nodes' ? (
        <>
          <GroupBar ctl={ctl} t={t} onSelectGroup={handleSelectGroup} />

          <div className="dusk-bar">
            <StatusFilter ctl={ctl} size="sm" className="dusk-bar__status" />
            <SearchBox ctl={ctl} className="dusk-bar__search" />
            <FilterButton ctl={ctl} compact={!desktop} />
            <span className="dusk-top__spacer" />
            <Dropdown menu={exportMenu} trigger={['click']} placement="bottomRight">
              <IconButton label={t('export')} icon={Download} busy={Boolean(ctl.working.export)} data-testid="nodes-export-menu" />
            </Dropdown>
            <Button icon={<Zap size={15} strokeWidth={1.75} />} aria-label={t('check_all_visible')} loading={Boolean(ctl.checks.progress)} disabled={ctl.visibleNodes.length === 0} onClick={ctl.checkVisible} data-testid="check-visible">
              {desktop ? t('check_all_visible') : null}
            </Button>
            <RefreshButton ctl={ctl} />
            <AddMenu act={ctl} compact={!desktop} />
          </div>

          <div className="dusk__scroll">
            <RuntimeBanner ctl={ctl} />
            {ctl.groupsError ? (
              <Alert
                type="error"
                showIcon
                className="sb-gap"
                message={t('groups_load_failed')}
                description={ctl.groupsError}
                action={<Button size="small" onClick={() => void ctl.loadGroups()} data-testid="groups-retry">{t('retry_load')}</Button>}
                data-testid="groups-load-error"
              />
            ) : null}
            <CheckProgress ctl={ctl} />
            <ActiveFilters ctl={ctl} />

            <section
              ref={tableRef}
              className={`dusk-table${hasCustomWidths ? ' has-custom-widths' : ''}${tableScrollable ? ' is-scrollable' : ''}`}
              style={tableStyle}
              aria-label={t('node_list')}
            >
              <div className="dusk-table__canvas">
                <div className="dusk-head">
                  <span />
                  <SelectAll ctl={ctl} />
                  <span>#</span>
                  <span />
                  <ResizableHeaderCell
                    colKey="name"
                    label={t('node_name')}
                    t={t}
                    className="dusk-col--name"
                    active={resizingCol === 'name'}
                    currentWidth={effectiveWidths.name}
                    onResizeStart={startColumnResize}
                    onReset={resetColumnWidth}
                    onStep={stepColumnWidth}
                  />
                  <span />
                  {/*
                    有意设计（切勿随意移除或打乱）：
                    1. 列表直接展示入站用户名（明文）与入站密码（默认固定宽度柔边毛玻璃掩码，直接点击掩码区域展开完整值），
                       目的是方便高频快速核对各入站凭据，请勿以安全为由移除或折叠回详情。
                    2. 列顺序按「基础标识（#、节点名称）→ 入站信息（入站端口、入站用户名、入站密码）→ 出站信息（节点类型、服务器地址、地区、节点IP、延迟）」
                       从左到右分组排列，明确分隔入站与出站信息。
                  */}
                  <div className="dusk-row__meta">
                    <ResizableHeaderCell colKey="port" label={t('col_inbound_port')} t={t} className="dusk-col--port" active={resizingCol === 'port'} currentWidth={effectiveWidths.port} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                    <ResizableHeaderCell colKey="user" label={t('inbound_username')} t={t} className="dusk-col--user" active={resizingCol === 'user'} currentWidth={effectiveWidths.user} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                    <ResizableHeaderCell colKey="pass" label={t('inbound_password')} t={t} className="dusk-col--pass" active={resizingCol === 'pass'} currentWidth={effectiveWidths.pass} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                    <ResizableHeaderCell colKey="type" label={t('node_type')} t={t} className="dusk-col--type" active={resizingCol === 'type'} currentWidth={effectiveWidths.type} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                    <ResizableHeaderCell colKey="server" label={t('server_address')} t={t} className="dusk-col--server" active={resizingCol === 'server'} currentWidth={effectiveWidths.server} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                    <ResizableHeaderCell colKey="region" label={t('region')} t={t} className="dusk-col--region" active={resizingCol === 'region'} currentWidth={effectiveWidths.region} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                    <ResizableHeaderCell colKey="ip" label={t('node_ip')} t={t} className="dusk-col--ip" active={resizingCol === 'ip'} currentWidth={effectiveWidths.ip} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                    <ResizableHeaderCell colKey="latency" label={t('latency')} t={t} className="dusk-col--latency" active={resizingCol === 'latency'} currentWidth={effectiveWidths.latency} onResizeStart={startColumnResize} onReset={resetColumnWidth} onStep={stepColumnWidth} />
                  </div>
                  <span />
                  <span className="dusk-head__actions">
                    {hasCustomWidths ? (
                      <button
                        type="button"
                        className="dusk-head__reset"
                        aria-label={t('reset_column_widths')}
                        data-tip={t('reset_column_widths')}
                        data-testid="reset-column-widths"
                        onClick={resetAllColumnWidths}
                      >
                        <RotateCcw size={12} strokeWidth={1.9} aria-hidden="true" />
                      </button>
                    ) : null}
                  </span>
                </div>
                {!ctl.loaded || (!ctl.groupsLoaded && !ctl.groupsError) ? (
                  <SkeletonRows rows={12} height={32} />
                ) : ctl.groupsError && ctl.groups.length === 0 ? (
                  <NodesEmpty ctl={ctl} icon={SquareTerminal} />
                ) : ctl.visibleNodes.length === 0 ? (
                  <NodesEmpty ctl={ctl} icon={SquareTerminal} />
                ) : (
                  <SortableList ids={ids} disabled={!ctl.canReorder} onReorder={ctl.reorder}>
                    <div role="list" data-testid="nodes-list">
                      {shown.map((node, index) => (
                        <DuskRow
                          key={node.id}
                          node={node}
                          index={index}
                          selected={ctl.selectedIds.has(node.id)}
                          expanded={node.id === expandedId}
                          focused={node.id === focusedId}
                          checking={ctl.checks.checkingIds.has(node.id)}
                          flagBusy={Boolean(ctl.flagBusy[node.id])}
                          pinBusy={Boolean(ctl.pinBusy[node.id])}
                          preservePorts={ctl.preservePorts}
                          canReorder={ctl.canReorder}
                          dragLabel={dragLabel}
                          act={act}
                          onToggle={toggleExpand}
                          onFocus={setFocusedId}
                        >
                          {node.id === expandedId ? (
                            <div className="dusk-expand" data-testid="node-inspector" data-node-id={node.id}>
                              <div className="dusk-expand__actions">
                                <DetailActions node={node} act={act} checking={ctl.checks.checkingIds.has(node.id)} canReorder={ctl.canReorder} />
                              </div>
                              <div className="dusk-expand__grid">
                                <DetailSections node={node} ctl={ctl} act={act} />
                              </div>
                            </div>
                          ) : null}
                        </DuskRow>
                      ))}
                    </div>
                  </SortableList>
                )}
                {hasMore ? <div ref={sentinelRef} className="dusk-more">{t('loading_more', { shown: shown.length, total: ctl.visibleNodes.length })}</div> : null}
              </div>
            </section>
          </div>

          {desktop ? (
            <footer className="dusk-foot">
              <span>{t('count_nodes', { count: ctl.visibleNodes.length })}</span>
              <span className="dusk-foot__stat"><i className="dusk-dot dusk-dot--ok" />{ctl.summary.healthy} {t('status_healthy')}</span>
              <span className="dusk-foot__stat"><i className="dusk-dot dusk-dot--warn" />{ctl.summary.untested} {t('status_untested_short')}</span>
              <span className="dusk-foot__stat"><i className="dusk-dot dusk-dot--bad" />{ctl.summary.failed} {t('status_failed_short')}</span>
              <span className="dusk-foot__stat"><i className="dusk-dot dusk-dot--muted" />{ctl.summary.disabled} {t('disabled')}</span>
              <span className="dusk-top__spacer" />
              <span className="dusk-foot__keys"><Kbd>j</Kbd><Kbd>k</Kbd>{t('key_move')}<Kbd>↵</Kbd>{t('key_expand')}<Kbd>x</Kbd>{t('key_select')}<Kbd>e</Kbd>{t('edit')}<Kbd>t</Kbd>{t('check_ip')}<Kbd>/</Kbd>{t('search')}</span>
            </footer>
          ) : null}
          <BulkBar ctl={ctl} />
        </>
      ) : (
        <div className="dusk__scroll" data-testid={`panel-view-${activeTab}`}>
          <RuntimeBanner ctl={ctl} />
          {activeTab === 'connections' ? <ConnectionsPanel token={session.token} /> : <LogsPanel ctl={ctl} />}
        </div>
      )}
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} ctl={ctl} session={session} onJump={jumpTo} />
      <Overlays ctl={ctl} />
    </div>
  )
}
