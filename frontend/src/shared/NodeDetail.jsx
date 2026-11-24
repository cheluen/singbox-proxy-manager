import { Button } from 'antd'
import { Pencil, Route, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { formatDateTime, initialUpstreamMode, protocolLabel, serverAddress, upstreamAddress } from '../core/nodeView'
import { Toggle } from './controls'
import { NodeMenuButton } from './menus'
import { upstreamModeLabel } from './labels'
import { ConnectInfo, PortChip, RemarkEditor } from './NodeParts'
import { CopyButton, Field, HealthBadge, Latency, Region, SecretText } from './primitives'

const dash = <span className="sb-dim">—</span>

function Section({ title, action, children, className = '' }) {
  return (
    <section className={`sb-section ${className}`}>
      <header className="sb-section__head">
        <h3>{title}</h3>
        {action || null}
      </header>
      {children}
    </section>
  )
}

function ExitSection({ node, checking }) {
  const { t } = useTranslation()
  return (
    <Section title={t('section_exit')}>
      <dl className="sb-fields">
        <Field label={t('status')}><HealthBadge node={node} checking={checking} /></Field>
        <Field label={t('latency')}><Latency value={node.latency} /></Field>
        <Field label={t('node_ip')}>
          {node.node_ip ? <span className="sb-inline"><span className="mono">{node.node_ip}</span><CopyButton text={node.node_ip} /></span> : dash}
        </Field>
        <Field label={t('location')}><Region node={node} /></Field>
      </dl>
      {node.enabled === false ? <p className="sb-dim sb-section__note">{t('check_disabled_hint')}</p> : null}
    </Section>
  )
}

function ConnectSection({ node }) {
  const { t } = useTranslation()
  return (
    <Section title={t('section_connect')}>
      <ConnectInfo node={node} />
    </Section>
  )
}

function InboundSection({ node, act, pinBusy, flagBusy, preservePorts }) {
  const { t } = useTranslation()
  return (
    <Section title={t('section_inbound')}>
      <dl className="sb-fields">
        <Field label={t('inbound_port')}><PortChip node={node} act={act} pinBusy={pinBusy} preservePorts={preservePorts} copyable /></Field>
        <Field label={t('tcp_reuse')}>
          <span className="sb-inline">
            <Toggle checked={node.tcp_reuse_enabled !== false} busy={flagBusy} label={t('tcp_reuse')} onChange={() => act.toggleTcpReuse(node)} testId={`node-tcp-reuse-${node.id}`} />
            <span className="sb-dim">{node.tcp_reuse_enabled !== false ? t('enabled') : t('disabled')}</span>
          </span>
        </Field>
        <Field label={t('username')}>
          {node.username ? <span className="sb-inline"><span className="mono">{node.username}</span><CopyButton text={node.username} /></span> : dash}
        </Field>
        <Field label={t('password_auth')}><SecretText value={node.password} label={t('password_auth')} /></Field>
      </dl>
    </Section>
  )
}

function OutboundSection({ node }) {
  const { t, i18n } = useTranslation()
  const address = serverAddress(node)
  const locale = i18n.language?.startsWith('zh') ? 'zh' : 'en'
  return (
    <Section title={t('section_outbound')}>
      <dl className="sb-fields">
        <Field label={t('server_address')} wide>
          {address ? <span className="sb-inline"><span className="mono truncate">{address}</span><CopyButton text={address} /></span> : dash}
        </Field>
        <Field label={t('node_type')}>{protocolLabel(node.type)}</Field>
        <Field label={t('updated_at')}>{formatDateTime(node.updated_at, locale)}</Field>
      </dl>
    </Section>
  )
}

function UpstreamSection({ node, act }) {
  const { t } = useTranslation()
  const mode = initialUpstreamMode(node)
  const custom = mode === 'custom'
  const address = custom ? upstreamAddress(node) : ''
  return (
    <Section
      title={t('upstream_info')}
      action={
        <Button size="small" icon={<Route size={13} strokeWidth={1.75} />} onClick={() => act.open({ kind: 'upstream', node })} data-testid={`node-upstream-action-${node.id}`}>
          {t('configure')}
        </Button>
      }
    >
      <dl className="sb-fields">
        <Field label={t('upstream_policy')}>{upstreamModeLabel(mode, t)}</Field>
        {custom ? (
          <>
            <Field label={t('node_type')}>{node.upstream_type ? protocolLabel(node.upstream_type) : dash}</Field>
            <Field label={t('upstream_address')}>{address ? <span className="mono">{address}</span> : dash}</Field>
            <Field label={t('upstream_ip')}>{node.upstream_ip ? <span className="mono">{node.upstream_ip}</span> : dash}</Field>
            <Field label={t('upstream_location')}><Region location={node.upstream_location} code={node.upstream_country_code} /></Field>
            <Field label={t('upstream_latency')}><Latency value={node.upstream_latency} /></Field>
          </>
        ) : null}
      </dl>
      {custom && node.upstream_error ? <p className="sb-callout sb-callout--bad sb-section__note">{node.upstream_error}</p> : null}
    </Section>
  )
}

function RemarkSection({ node, act }) {
  const { t } = useTranslation()
  return (
    <div className="sb-detail-card sb-detail-note-group" data-detail-group="meta">
      <Section title={t('full_node_name')} className="sb-full-name-section">
        <div className="sb-full-name">
          <span className="sb-full-name__text" data-testid={`node-full-name-${node.id}`}>{node.name}</span>
          <CopyButton text={node.name} label={`${t('copy')} ${t('node_name')}`} />
        </div>
      </Section>
      <Section title={t('remark')} className="sb-remark-section">
        <RemarkEditor node={node} act={act} />
      </Section>
    </div>
  )
}

// 详情头部的主要操作：编辑、测 IP，其余操作收进「更多」菜单。
export function DetailActions({ node, act, checking, canReorder }) {
  const { t } = useTranslation()
  return (
    <div className="sb-detail-actions">
      <Button type="primary" icon={<Pencil size={14} strokeWidth={1.75} />} onClick={() => act.open({ kind: 'node', node })} data-testid={`node-edit-${node.id}`}>{t('edit')}</Button>
      <Button icon={<Zap size={14} strokeWidth={1.75} />} loading={checking} disabled={node.enabled === false} onClick={() => act.checkNodes([node.id])} data-testid={`node-check-${node.id}`}>{t('check_ip')}</Button>
      <NodeMenuButton node={node} act={act} canReorder={canReorder} />
    </div>
  )
}

// 完整的详情分区集合：按「入站与连接 → 出站与链路 → 名称与备注」三组等高卡片排列，
// 与列表「入站 → 出站」从左到右（窄屏从上到下）的顺序保持一致，消除旧布局的不对称空洞。
export function DetailSections({ node, ctl, act }) {
  const checking = ctl.checks.checkingIds.has(node.id)
  return (
    <>
      <div className="sb-detail-card" data-detail-group="inbound">
        <InboundSection node={node} act={act} pinBusy={Boolean(ctl.pinBusy[node.id])} flagBusy={Boolean(ctl.flagBusy[node.id])} preservePorts={ctl.preservePorts} />
        <ConnectSection node={node} />
      </div>
      <div className="sb-detail-card" data-detail-group="outbound">
        <OutboundSection node={node} />
        <ExitSection node={node} checking={checking} />
        <UpstreamSection node={node} act={act} />
      </div>
      <RemarkSection node={node} act={act} />
    </>
  )
}
