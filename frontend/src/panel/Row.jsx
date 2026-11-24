import { memo, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { hasInboundAuth, serverAddress } from '../core/nodeView'
import { CheckBox, Toggle } from '../shared/controls'
import { isInteractive } from '../shared/labels'
import { NodeMenuButton } from '../shared/menus'
import { AuthMark, PortChip, RemarkMark, UpstreamMark } from '../shared/NodeParts'
import { HealthBadge, Latency, ProtocolTag, Region } from '../shared/primitives'
import { SortableItem } from '../shared/sortable'

// 有意设计（切勿以“安全建议”移除或折叠回详情）：
// 列表行内直接展示入站用户名（明文）与入站密码（默认固定宽度柔边毛玻璃掩码，直接点击掩码区域切换显示完整值），
// 旨在提高用户批量核对不同入站凭据的效率；折叠态不将密码明文写入 DOM 且宽度恒定，避免泄露长度。
// 固定长度占位字符仅在毛玻璃滤镜下作装饰纹理，不反映真实密码长度或内容。
const PASSWORD_MASK_GLYPHS = 'a9xK4mP2'

function InboundPasswordCell({ node }) {
  const { t } = useTranslation()
  const [revealedSecret, setRevealedSecret] = useState(null)
  const maskRef = useRef(null)
  const valueRef = useRef(null)
  const restoreFocusRef = useRef(false)
  const secret = node.password || ''
  const visible = Boolean(secret) && revealedSecret === secret

  useEffect(() => {
    restoreFocusRef.current = false
    setRevealedSecret(null)
  }, [secret])

  useLayoutEffect(() => {
    if (!restoreFocusRef.current) return
    restoreFocusRef.current = false
    if (visible) valueRef.current?.focus({ preventScroll: true })
    else maskRef.current?.focus({ preventScroll: true })
  }, [visible])

  if (!secret) {
    return (
      <span className="dusk-col--pass dusk-row__pass sb-dim" data-testid={`node-password-${node.id}`}>
        —
      </span>
    )
  }

  const toggleSecret = (keepFocus) => {
    restoreFocusRef.current = keepFocus
    setRevealedSecret((prev) => (prev === secret ? null : secret))
  }

  return (
    <span
      className={`dusk-col--pass dusk-row__pass${visible ? ' is-revealed' : ''}`}
      data-testid={`node-password-${node.id}`}
      data-masked={visible ? 'false' : 'true'}
      onClick={(event) => event.stopPropagation()}
    >
      {visible ? (
        <span
          ref={valueRef}
          className="dusk-row__pass-value mono"
          role="button"
          tabIndex={0}
          aria-description={t('hide_inbound_password')}
          data-tip={t('hide_inbound_password')}
          data-testid={`node-password-value-${node.id}`}
          onClick={(event) => {
            event.stopPropagation()
            // 拖选复制明文时保持展开，仅普通单击才切回掩码。
            if (window.getSelection?.()?.toString()) return
            toggleSecret(true)
          }}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' && event.key !== ' ') return
            event.preventDefault()
            event.stopPropagation()
            toggleSecret(true)
          }}
        >
          {secret}
        </span>
      ) : (
        <button
          ref={maskRef}
          type="button"
          className="sb-pass-mask"
          aria-label={t('show_inbound_password')}
          data-tip={t('show_inbound_password')}
          data-testid={`node-password-mask-${node.id}`}
          onClick={(event) => {
            event.stopPropagation()
            toggleSecret(true)
          }}
        >
          <span className="sb-pass-mask__veil" aria-hidden="true">
            <span className="sb-pass-mask__glyphs mono">{PASSWORD_MASK_GLYPHS}</span>
          </span>
        </button>
      )}
    </span>
  )
}

// 高密度行（40px）。点击行或按 Enter 在原位展开详情，不离开列表上下文。
export const DuskRow = memo(function DuskRow({ node, index, selected, expanded, focused, checking, flagBusy, pinBusy, preservePorts, canReorder, dragLabel, act, onToggle, onFocus, children }) {
  const { t } = useTranslation()
  const off = node.enabled === false
  const server = serverAddress(node)
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
        if (isInteractive(event) || event.target.closest('.dusk-expand') || window.getSelection?.()?.toString()) return
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
        {hasInboundAuth(node) ? null : <AuthMark />}
      </span>
      {/*
        有意设计（切勿随意重排或删除入站凭据列）：
        列顺序严格与表头对齐，从左到右清晰划分为：
        入站信息（入站端口、明文入站用户名、毛玻璃掩码入站密码）→ 出站信息（节点类型、服务器地址、地区、节点IP、延迟）。
      */}
      <div className="dusk-row__meta">
        <span className="dusk-col--port"><PortChip node={node} act={act} pinBusy={pinBusy} preservePorts={preservePorts} /></span>
        <span
          className="dusk-col--user dusk-row__user mono truncate"
          data-tip={node.username || undefined}
          data-testid={`node-username-${node.id}`}
          onClick={node.username ? (event) => event.stopPropagation() : undefined}
        >
          {node.username ? node.username : <span className="sb-dim">—</span>}
        </span>
        <InboundPasswordCell node={node} />
        <span className="dusk-col--type"><ProtocolTag type={node.type} /></span>
        <span className="dusk-col--server dusk-row__server mono truncate" data-tip={server || undefined}>{server || t('direct_local')}</span>
        <span className="dusk-col--region"><Region node={node} showName={false} /></span>
        <span className="dusk-col--ip dusk-row__ip mono truncate">{node.node_ip || '—'}</span>
        <span className="dusk-col--latency"><Latency value={node.latency} /></span>
      </div>
      <Toggle checked={!off} busy={flagBusy} label={t('node_enabled_label')} onChange={() => act.toggleEnabled(node)} testId={`node-enabled-${node.id}`} />
      <NodeMenuButton node={node} act={act} canReorder={canReorder} size="sm" />
      {children}
    </SortableItem>
  )
})
