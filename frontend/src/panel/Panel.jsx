// 设计 4「暮色 · 暗色指挥台」：暗色莫兰迪 + 高密度列表 + 行内展开 + 命令面板，偏键盘操作。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from 'antd'
import { Command, LogOut, Settings, SquareTerminal, Zap } from 'lucide-react'
import { EMPTY_CRITERIA } from '../core/nodeFilters'
import { ActiveFilters, FilterButton, SearchBox, StatusFilter } from '../shared/filters'
import { BulkBar, CheckProgress, NodesEmpty, RefreshButton, RepoLink, RuntimeBanner, RuntimePill, SelectAll, VersionBadge } from '../shared/chrome'
import { useIncrementalList, useMediaQuery } from '../shared/hooks'
import { BrandLogo, BrandWordmark } from '../shared/Brand'
import { AddMenu } from '../shared/menus'
import { DetailActions, DetailSections } from '../shared/NodeDetail'
import { Overlays } from '../shared/Overlays'
import { IconButton, Kbd, LangSwitch, SkeletonRows } from '../shared/primitives'
import { SortableList } from '../shared/sortable'
import { usePanel } from '../shared/usePanel'
import { CommandPalette } from './Palette'
import { DuskRow } from './Row'

const isMac = /Mac|iPhone|iPad/.test(navigator.platform)
const typingInField = () => {
  const element = document.activeElement
  return Boolean(element && (['INPUT', 'TEXTAREA', 'SELECT'].includes(element.tagName) || element.isContentEditable))
}
const overlayOpen = () => Boolean(document.querySelector('.ant-modal-wrap:not([style*="display: none"]), .ant-dropdown:not(.ant-dropdown-hidden), .ant-popover:not(.ant-popover-hidden)'))

export default function DuskPanel({ session }) {
  const { ctl, act, t } = usePanel(session)
  const [expandedId, setExpandedId] = useState(null)
  const [focusedId, setFocusedId] = useState(null)
  const [paletteOpen, setPaletteOpen] = useState(false)
  const desktop = useMediaQuery('(min-width: 900px)')
  const { shown, hasMore, sentinelRef, reveal } = useIncrementalList(ctl.visibleNodes)
  const ids = useMemo(() => shown.map((node) => node.id), [shown])
  // 键盘监听只注册一次，通过 ref 读取最新状态。
  const stateRef = useRef({ ctl, shown, focusedId })
  useEffect(() => {
    stateRef.current = { ctl, shown, focusedId }
  })

  const toggleExpand = useCallback((id) => setExpandedId((prev) => (prev === id ? null : id)), [])
  const jumpTo = useCallback((id) => {
    // 目标节点可能被当前筛选隐藏，跳转前先清空筛选。
    stateRef.current.ctl.setCriteria(EMPTY_CRITERIA)
    const index = stateRef.current.ctl.nodes.findIndex((node) => node.id === id)
    if (index >= 0) reveal(index)
    setFocusedId(id)
    setExpandedId(id)
    window.setTimeout(() => document.querySelector(`[data-row-id="${id}"]`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }), 80)
  }, [reveal])

  // 键盘操作：Ctrl/⌘K 命令面板；j/k 或方向键移动；Enter 展开；x 选择；e 编辑；t 测 IP。
  useEffect(() => {
    const onKey = (event) => {
      // 拖拽手柄和抓起状态下的方向键、回车、空格、Esc 由 dnd-kit 独占。
      if (event.target?.closest?.('.sb-drag') || document.querySelector('.is-dragging')) return
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen((prev) => !prev)
        return
      }
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

  return (
    <div className="dusk">
      <header className="dusk-top">
        <div className="dusk-top__brand">
          <BrandLogo />
          <strong className="dusk-top__title" lang="en" data-testid="project-brand"><BrandWordmark /></strong>
        </div>
        <RuntimePill ctl={ctl} showLabel={desktop} />
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

      <div className="dusk-bar">
        <StatusFilter ctl={ctl} size="sm" className="dusk-bar__status" />
        <SearchBox ctl={ctl} className="dusk-bar__search" />
        <FilterButton ctl={ctl} compact={!desktop} />
        <span className="dusk-top__spacer" />
        <Button icon={<Zap size={15} strokeWidth={1.75} />} aria-label={t('check_all_visible')} loading={Boolean(ctl.checks.progress)} disabled={ctl.visibleNodes.length === 0} onClick={ctl.checkVisible} data-testid="check-visible">
          {desktop ? t('check_all_visible') : null}
        </Button>
        <RefreshButton ctl={ctl} />
        <AddMenu act={ctl} compact={!desktop} />
      </div>

      <div className="dusk__scroll">
        <RuntimeBanner ctl={ctl} />
        <CheckProgress ctl={ctl} />
        <ActiveFilters ctl={ctl} />

        <section className="dusk-table" aria-label={t('node_list')}>
          <div className="dusk-head">
            <span />
            <SelectAll ctl={ctl} />
            <span>#</span>
            <span />
            <span>{t('node_name')}</span>
            <span />
            {/*
              有意设计（切勿随意移除或打乱）：
              1. 列表直接展示入站用户名（明文）与入站密码（默认固定宽度柔边毛玻璃掩码，直接点击掩码区域展开完整值），
                 目的是方便高频快速核对各入站凭据，请勿以安全为由移除或折叠回详情。
              2. 列顺序按「基础标识（#、节点名称）→ 入站信息（入站端口、入站用户名、入站密码）→ 出站信息（节点类型、服务器地址、地区、节点IP、延迟）」
                 从左到右分组排列，明确分隔入站与出站信息。
            */}
            <div className="dusk-row__meta">
              <span className="dusk-col--port">{t('inbound_port')}</span>
              <span className="dusk-col--user">{t('inbound_username')}</span>
              <span className="dusk-col--pass">{t('inbound_password')}</span>
              <span className="dusk-col--type">{t('node_type')}</span>
              <span className="dusk-col--server">{t('server_address')}</span>
              <span className="dusk-col--region">{t('region')}</span>
              <span className="dusk-col--ip">{t('node_ip')}</span>
              <span className="dusk-col--latency">{t('latency')}</span>
            </div>
            <span />
            <span />
          </div>
          {!ctl.loaded ? (
            <SkeletonRows rows={12} height={32} />
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
        </section>
      </div>

      {desktop ? (
        <footer className="dusk-foot">
          <span>{t('count_nodes', { count: ctl.visibleNodes.length })}</span>
          <span className="dusk-foot__stat"><i className="dusk-dot dusk-dot--ok" />{ctl.summary.healthy} {t('status_healthy')}</span>
          <span className="dusk-foot__stat"><i className="dusk-dot dusk-dot--warn" />{ctl.summary.unverified} {t('status_unverified_short')}</span>
          <span className="dusk-foot__stat"><i className="dusk-dot dusk-dot--muted" />{ctl.summary.disabled} {t('disabled')}</span>
          <span className="dusk-top__spacer" />
          <span className="dusk-foot__keys"><Kbd>j</Kbd><Kbd>k</Kbd>{t('key_move')}<Kbd>↵</Kbd>{t('key_expand')}<Kbd>x</Kbd>{t('key_select')}<Kbd>e</Kbd>{t('edit')}<Kbd>t</Kbd>{t('check_ip')}<Kbd>/</Kbd>{t('search')}</span>
        </footer>
      ) : null}
      <BulkBar ctl={ctl} />
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} ctl={ctl} session={session} onJump={jumpTo} />
      <Overlays ctl={ctl} />
    </div>
  )
}
