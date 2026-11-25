// 命令面板（Ctrl/⌘ K）：一个输入框里既能跳到节点，也能执行任何操作。
import { useEffect, useMemo, useRef, useState } from 'react'
import { Modal } from 'antd'
import { CornerDownLeft, Download, FileStack, KeyRound, LogOut, Plus, RefreshCw, RotateCcw, Search, Server, Settings, Trash2, X, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { matchesSearch, protocolLabel } from '../core/nodeView'
import { Kbd } from '../shared/primitives'

const MAX_NODE_RESULTS = 8

export function CommandPalette({ open, onClose, ctl, session, onJump }) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const listRef = useRef(null)
  const inputRef = useRef(null)
  const selected = ctl.selectedIds.size

  useEffect(() => {
    if (open) {
      setQuery('')
      setCursor(0)
    }
  }, [open])

  useEffect(() => {
    if (!open) return undefined
    const escape = (event) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      onClose()
    }
    window.addEventListener('keydown', escape, true)
    return () => window.removeEventListener('keydown', escape, true)
  }, [onClose, open])

  const items = useMemo(() => {
    const commands = [
      { id: 'add', icon: Plus, label: t('add_node'), run: () => ctl.open({ kind: 'node', node: null }) },
      { id: 'link', icon: Plus, label: t('add_from_link'), run: () => ctl.open({ kind: 'node', node: null, focusLink: true }) },
      { id: 'import', icon: FileStack, label: t('batch_import'), run: () => ctl.open({ kind: 'import' }) },
      { id: 'check', icon: Zap, label: t('check_all_visible'), run: () => ctl.checkVisible() },
      { id: 'refresh', icon: RefreshCw, label: t('refresh'), run: () => ctl.refreshAll() },
      { id: 'select-all', icon: Server, label: t('select_all'), run: () => ctl.selectAllVisible(true) },
      ...(selected > 0
        ? [
            { id: 'sel-check', icon: Zap, label: t('cmd_selected', { action: t('check_ip'), count: selected }), run: () => ctl.checkSelected() },
            { id: 'sel-auth', icon: KeyRound, label: t('cmd_selected', { action: t('set_auth'), count: selected }), run: () => ctl.open({ kind: 'auth' }) },
            { id: 'sel-export', icon: Download, label: t('cmd_selected', { action: t('export'), count: selected }), run: () => ctl.exportSelected() },
            { id: 'sel-delete', icon: Trash2, label: t('cmd_selected', { action: t('delete'), count: selected }), run: () => ctl.deleteSelected() },
            { id: 'sel-clear', icon: X, label: t('clear_selection'), run: () => ctl.clearSelection() },
          ]
        : []),
      ...(ctl.runtime?.degraded ? [{ id: 'restart', icon: RotateCcw, label: t('runtime_restart'), run: () => ctl.restartRuntime() }] : []),
      { id: 'settings', icon: Settings, label: t('settings'), run: () => ctl.open({ kind: 'settings' }) },
      { id: 'logout', icon: LogOut, label: t('logout'), run: () => session.logout() },
    ]
    const needle = query.trim().toLowerCase()
    const matchedCommands = needle ? commands.filter((command) => command.label.toLowerCase().includes(needle)) : commands
    const matchedNodes = needle
      ? (ctl.allNodes || ctl.nodes).filter((node) => matchesSearch(node, needle)).slice(0, MAX_NODE_RESULTS).map((node) => {
          const groupName = ctl.groupById?.get(node.group_id)?.name
          const portText = node.inbound_port ? `:${node.inbound_port}` : t('group_no_port_badge')
          return {
            id: `node-${node.id}`,
            icon: Server,
            label: node.name,
            hint: [groupName, protocolLabel(node.type, t), portText].filter(Boolean).join(' · '),
            group: 'nodes',
            run: () => onJump(node.id),
          }
        })
      : []
    return [...matchedNodes, ...matchedCommands.map((command) => ({ ...command, group: 'commands' }))]
  }, [ctl, onJump, query, selected, session, t])

  const active = Math.min(cursor, Math.max(0, items.length - 1))
  useEffect(() => {
    listRef.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active])

  const run = (item) => {
    if (!item) return
    onClose()
    item.run()
  }

  const onKeyDown = (event) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setCursor((active + 1) % Math.max(1, items.length))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setCursor((active - 1 + items.length) % Math.max(1, items.length))
    } else if (event.key === 'Enter') {
      event.preventDefault()
      run(items[active])
    }
  }

  return (
    <Modal open={open} onCancel={onClose} afterOpenChange={(visible) => { if (visible) inputRef.current?.focus() }} footer={null} closable={false} width={560} destroyOnHidden rootClassName="sb-overlay dusk-palette" style={{ top: 96 }} styles={{ content: { padding: 0 }, body: { margin: 0, padding: 0, maxHeight: 'none' } }}>
      <div className="dusk-palette__input">
        <Search size={16} strokeWidth={1.75} aria-hidden="true" />
        <input
          ref={inputRef}
          autoFocus
          value={query}
          spellCheck={false}
          placeholder={t('cmd_placeholder')}
          aria-label={t('cmd_placeholder')}
          role="combobox"
          aria-expanded="true"
          aria-controls="dusk-palette-list"
          aria-activedescendant={items[active] ? `dusk-cmd-${items[active].id}` : undefined}
          data-testid="palette-input"
          onChange={(event) => {
            setQuery(event.target.value)
            setCursor(0)
          }}
          onKeyDown={onKeyDown}
        />
        <Kbd>Esc</Kbd>
      </div>
      <ul className="dusk-palette__list" id="dusk-palette-list" role="listbox" ref={listRef}>
        {items.length === 0 ? <li className="dusk-palette__empty">{t('cmd_empty')}</li> : null}
        {items.map((item, index) => {
          const Icon = item.icon
          const heading = index === 0 || items[index - 1].group !== item.group
          return (
            <li key={item.id} role="presentation">
              {heading ? <span className="dusk-palette__group">{item.group === 'nodes' ? t('nodes') : t('cmd_actions')}</span> : null}
              <button type="button" id={`dusk-cmd-${item.id}`} role="option" aria-selected={index === active} className="dusk-palette__item" onMouseEnter={() => setCursor(index)} onClick={() => run(item)} data-testid={`palette-${item.id}`}>
                <Icon size={15} strokeWidth={1.75} aria-hidden="true" />
                <span className="truncate">{item.label}</span>
                {item.hint ? <span className="dusk-palette__hint mono">{item.hint}</span> : null}
                {index === active ? <CornerDownLeft size={13} strokeWidth={1.75} aria-hidden="true" className="dusk-palette__enter" /> : null}
              </button>
            </li>
          )
        })}
      </ul>
    </Modal>
  )
}
