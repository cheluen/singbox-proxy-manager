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

export function Section({ title, action, children, className = '' }) {
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

export function ExitSection({ node, checking }) {
  const { t } = useTranslation()
  return (
    <Section title={t('section_exit')}>
      <dl className="sb-fields">
        <Field label={t('status')}><HealthBadge node={node} checking={checking} /></Field>
        <Field label={t('latency')}><Latency value={node.latency} /></Field>
        <Field label={t('node_ip')}>
          {node.node_ip ? <span className="sb-inline"><span className="mono">{node.node_ip}</span><CopyButton text={node.node_ip} /></span> : dash}
        </Field>
        <Field label={t('location')}><Region node={node} full /></Field>
      </dl>
      {node.enabled === false ? <p className="sb-dim sb-section__note">{t('check_disabled_hint')}</p> : null}
    </Section>
  )
}

export function ConnectSection({ node }) {
  const { t } = useTranslation()
  return (
    <Section title={t('section_connect')}>
      <ConnectInfo node={node} />
    </Section>
  )
}

export function InboundSection({ node, act, pinBusy, flagBusy, preservePorts }) {
  const { t } = useTranslation()
  return (
    <Section title={t('section_inbound')}>
      <dl className="sb-fields">
        <Field label={t('inbound_port')}><PortChip node={node} act={act} pinBusy={pinBusy} preservePorts={preservePorts} copyable /></Field>
        <Field label={t('tcp_reuse')}>
          <span className="sb-inline">
            <Toggle size="sm" checked={node.tcp_reuse_enabled !== false} busy={flagBusy} label={t('tcp_reuse')} onChange={() => act.toggleTcpReuse(node)} testId={`node-tcp-reuse-${node.id}`} />
            <span className="sb-dim">{node.tcp_reuse_enabled !== false ? t('enabled') : t('disabled')}</span>
          </span>
        </Field>
        <Field label={t('username')}>
          {node.username ? <span className="sb-inline"><span className="mono">{node.username}</span><CopyButton text={node.username} /></span> : dash}
        </Field>
        <Field label={t('auth_secret')}><SecretText value={node.password} label={t('auth_secret')} /></Field>
      </dl>
    </Section>
  )
}

export function OutboundSection({ node }) {
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

export function UpstreamSection({ node, act }) {
  const { t } = useTranslation()
  const mode = initialUpstreamMode(node)
  const custom = mode === 'custom'
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
            <Field label={t('upstream_address')}>{upstreamAddress(node) ? <span className="mono">{upstreamAddress(node)}</span> : dash}</Field>
            <Field label={t('upstream_ip')}>{node.upstream_ip ? <span className="mono">{node.upstream_ip}</span> : dash}</Field>
            <Field label={t('upstream_location')}><Region location={node.upstream_location} code={node.upstream_country_code} full /></Field>
            <Field label={t('upstream_latency')}><Latency value={node.upstream_latency} /></Field>
          </>
        ) : null}
      </dl>
      {custom && node.upstream_error ? <p className="sb-callout sb-callout--bad sb-section__note">{node.upstream_error}</p> : null}
    </Section>
  )
}

export function RemarkSection({ node, act }) {
  const { t } = useTranslation()
  return (
    <Section title={t('remark')}>
      <RemarkEditor node={node} act={act} />
    </Section>
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

export function EnabledSwitch({ node, act, flagBusy, size = 'md' }) {
  const { t } = useTranslation()
  const on = node.enabled !== false
  return (
    <span className="sb-inline sb-enabled-switch">
      <Toggle size={size} checked={on} busy={flagBusy} label={t('node_enabled_label')} onChange={() => act.toggleEnabled(node)} testId={`node-enabled-detail-${node.id}`} />
      <span>{on ? t('enabled') : t('disabled')}</span>
    </span>
  )
}

// 完整的详情分区集合。layout 由外层容器的样式决定（单列 / 多列）。
export function DetailSections({ node, ctl, act }) {
  const checking = ctl.checks.checkingIds.has(node.id)
  return (
    <>
      <ConnectSection node={node} />
      <ExitSection node={node} checking={checking} />
      <InboundSection node={node} act={act} pinBusy={Boolean(ctl.pinBusy[node.id])} flagBusy={Boolean(ctl.flagBusy[node.id])} preservePorts={ctl.preservePorts} />
      <OutboundSection node={node} />
      <UpstreamSection node={node} act={act} />
      <RemarkSection node={node} act={act} />
    </>
  )
}
