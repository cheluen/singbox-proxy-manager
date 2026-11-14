import { memo } from 'react'
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { hasInboundAuth, serverAddress } from '../core/nodeView'
import { CheckBox, Toggle } from '../shared/controls'
import { isInteractive } from '../shared/labels'
import { NodeMenuButton } from '../shared/menus'
import { AuthMark, PortChip, RemarkMark, UpstreamMark } from '../shared/NodeParts'
import { HealthBadge, Latency, ProtocolTag, Region } from '../shared/primitives'
import { SortableItem } from '../shared/sortable'

// 高密度行（40px）。点击行或按 Enter 在原位展开详情，不离开列表上下文。
export const DuskRow = memo(function DuskRow({ node, index, selected, expanded, focused, checking, flagBusy, pinBusy, preservePorts, canReorder, dragLabel, act, onToggle, onFocus, children }) {
  const { t } = useTranslation()
  const off = node.enabled === false
  return (
    <SortableItem
      id={node.id}
      disabled={!canReorder}
      handleLabel={dragLabel}
      className={`dusk-row${selected ? ' is-selected' : ''}${expanded ? ' is-expanded' : ''}${focused ? ' is-focused' : ''}${off ? ' is-off' : ''}`}
      role="listitem"
      data-testid={`node-row-${node.id}`}
      data-row-id={node.id}
      onClick={(event) => {
        if (isInteractive(event) || event.target.closest('.dusk-expand')) return
        onFocus(node.id)
        onToggle(node.id)
      }}
    >
      <CheckBox checked={selected} onChange={(on) => act.toggleSelect(node.id, on)} label={t('select_node', { name: node.name })} testId={`node-select-${node.id}`} />
      <span className="dusk-row__index mono">{String(index + 1).padStart(2, '0')}</span>
      <HealthBadge node={node} checking={checking} compact />
      <button type="button" className="dusk-row__name" data-tip={node.name} aria-expanded={expanded} onClick={() => { onFocus(node.id); onToggle(node.id) }} data-testid={`node-open-${node.id}`}>
        <ChevronRight size={14} strokeWidth={2} aria-hidden="true" className="dusk-row__chevron" />
        <span className="truncate">{node.name}</span>
      </button>
      <span className="dusk-row__marks">
        <RemarkMark node={node} />
        <UpstreamMark node={node} />
        {hasInboundAuth(node) ? null : <AuthMark node={node} />}
      </span>
      <div className="dusk-row__meta">
        <ProtocolTag type={node.type} />
        <span className="dusk-row__server mono truncate">{serverAddress(node) || t('direct_local')}</span>
        <PortChip node={node} act={act} pinBusy={pinBusy} preservePorts={preservePorts} />
        <Region node={node} showName={false} />
        <span className="dusk-row__ip mono truncate">{node.node_ip || '—'}</span>
        <Latency value={node.latency} />
      </div>
      <Toggle size="sm" checked={!off} busy={flagBusy} label={t('node_enabled_label')} onChange={() => act.toggleEnabled(node)} testId={`node-enabled-${node.id}`} />
      <NodeMenuButton node={node} act={act} canReorder={canReorder} size="sm" />
      {children}
    </SortableItem>
  )
})
