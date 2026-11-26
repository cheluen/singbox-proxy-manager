import { Button } from 'antd'
import { Link2, Pencil, Route, TriangleAlert, Unlink, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { isPolicyNode } from '../core/nodeModel'
import { configEndpointAddress, formatDateTime, nodeHealth, parsedNodeConfig, protocolLabel, resolveEffectiveUpstream, serverAddress } from '../core/nodeView'
import { Toggle } from './controls'
import { NodeMenuButton } from './menus'
import { policyWarningLabel, upstreamModeLabel } from './labels'
import { ConnectInfo, InboundModeSwitch, PortChip, RemarkEditor } from './NodeParts'
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
  const health = nodeHealth(node)
  const failed = health === 'failed'
  return (
    <Section title={t('section_exit')}>
      <dl className="sb-fields">
        <Field label={t('status')}><HealthBadge node={node} checking={checking} /></Field>
        <Field label={t('latency')}><Latency value={node.latency} failed={failed} healthy={health === 'healthy'} untested={health === 'untested'} error={node.check_error} /></Field>
        <Field label={t('node_ip')}>
          {node.node_ip ? <span className="sb-inline"><span className="mono">{node.node_ip}</span><CopyButton text={node.node_ip} /></span> : dash}
        </Field>
        <Field label={t('location')}><Region node={node} /></Field>
      </dl>
      {failed && node.check_error ? <p className="sb-callout sb-callout--bad sb-section__note" data-testid={`node-check-error-${node.id}`}>{node.check_error}</p> : null}
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

function InboundSection({ node, ctl, act, pinBusy, flagBusy, preservePorts }) {
  const { t } = useTranslation()
  const hasPort = Number(node.inbound_port) > 0
  return (
    <Section title={t('section_inbound')}>
      <dl className="sb-fields">
        <Field label={t('inbound_port')}><PortChip node={node} act={act} pinBusy={pinBusy} preservePorts={preservePorts} copyable /></Field>
        <Field label={t('inbound_mode')}>
          <InboundModeSwitch
            value={node.inbound_mode === 'https' ? 'https' : 'mixed'}
            tlsStatus={ctl.settings?.inbound_tls}
            disabled={!hasPort}
            busy={flagBusy}
            onChange={(mode) => act.setInboundMode(node, mode)}
            onRejected={(reason) => ctl.helpers.notify.warning(reason)}
            testId={`node-inbound-mode-${node.id}`}
          />
        </Field>
        <Field label={t('tcp_reuse')}>
          <span className="sb-inline">
            <Toggle checked={hasPort && node.tcp_reuse_enabled !== false} busy={flagBusy} disabled={!hasPort} label={t('tcp_reuse')} onChange={() => act.toggleTcpReuse(node)} testId={`node-tcp-reuse-${node.id}`} />
            <span className="sb-dim">{!hasPort ? t('group_no_port_badge') : node.tcp_reuse_enabled !== false ? t('enabled') : t('disabled')}</span>
          </span>
        </Field>
        <Field label={t('username')}>
          {node.username ? <span className="sb-inline"><span className="mono">{node.username}</span><CopyButton text={node.username} /></span> : dash}
        </Field>
        <Field label={t('password_auth')} wide><SecretText value={node.password} label={t('password_auth')} /></Field>
      </dl>
    </Section>
  )
}

function PolicyMembersSection({ node, ctl, act, busy = false }) {
  const { t } = useTranslation()
  const cfg = parsedNodeConfig(node)
  const memberIds = Array.isArray(cfg.member_ids) ? cfg.member_ids : []
  const linkedGroupIds = Array.isArray(cfg.member_sources)
    ? cfg.member_sources.filter((entry) => entry?.type === 'group' && Number(entry.id) > 0).map((entry) => Number(entry.id))
    : []
  const nodeMap = new Map((ctl.allNodes || ctl.nodes || []).map((item) => [item.id, item]))
  const groupMap = ctl.groupById || new Map((ctl.groups || []).map((item) => [item.id, item]))
  const healthMap = new Map((node.policy_member_health || []).map((item) => [item.node_id, item]))
  const warnings = Array.isArray(node.policy_warnings) ? node.policy_warnings : []
  return (
    <Section title={t('policy_members_status')}>
      {linkedGroupIds.length > 0 ? (
        <div className="sb-policy-linked-bar" data-testid={`policy-linked-groups-${node.id}`}>
          <span className="sb-policy-linked-bar__label">{t('policy_linked_groups_label')}</span>
          {linkedGroupIds.map((gid) => {
            const group = groupMap.get(gid)
            const name = group ? group.name : t('policy_group_missing', { id: gid })
            return (
              <span key={gid} className="sb-policy-linked-pill" data-testid={`policy-linked-pill-${gid}`}>
                <Link2 size={12} strokeWidth={1.9} aria-hidden="true" />
                <span className="truncate">{name}</span>
                <span className="sb-policy-linked-pill__tag">{t('policy_group_sync_badge')}</span>
                <button
                  type="button"
                  className="sb-policy-linked-pill__unlink"
                  aria-label={t('policy_unlink_group')}
                  data-tip={t('policy_unlink_group_tip')}
                  data-testid={`policy-detail-unlink-group-${gid}`}
                  disabled={busy}
                  onClick={() => void act.unlinkPolicyGroup(node, gid)}
                >
                  <Unlink size={12} strokeWidth={1.9} aria-hidden="true" />
                </button>
              </span>
            )
          })}
        </div>
      ) : null}
      {warnings.map((warn, idx) => (
        <p key={idx} className="sb-callout sb-callout--warn sb-section__note" style={{ marginTop: 0, marginBottom: 8 }}>
          <TriangleAlert size={14} strokeWidth={1.8} />
          <span>{policyWarningLabel(warn, t)}</span>
        </p>
      ))}
      {memberIds.length === 0 ? (
        <p className="sb-dim">{t('policy_no_members')}</p>
      ) : (
        <ul className="sb-policy-status-list" data-testid={`policy-members-status-${node.id}`}>
          {memberIds.map((id, index) => {
            const member = nodeMap.get(id)
            const health = healthMap.get(id)
            const isActive = node.policy_active_member_id === id
            const tone = !member || member.enabled === false
              ? 'muted'
              : health
                ? (health.healthy ? 'ok' : 'bad')
                : 'warn'
            return (
              <li key={id} className={`sb-policy-status-item${isActive ? ' is-active' : ''}`}>
                <span className="mono sb-dim">{index + 1}</span>
                <i className={`dusk-dot dusk-dot--${tone}`} />
                <span className="truncate">{member ? member.name : `#${id}`}</span>
                {isActive ? <span className="sb-tag sb-tag--protocol">{t('policy_active_tag')}</span> : null}
                <span className="sb-policy-status-item__right mono">
                  {!member || member.enabled === false
                    ? t('disabled')
                    : health
                      ? (health.healthy ? (health.latency > 0 ? `${health.latency} ms` : t('status_healthy')) : t('status_failed_short'))
                      : t('status_untested_short')}
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </Section>
  )
}

function OutboundSection({ node }) {
  const { t, i18n } = useTranslation()
  const policy = isPolicyNode(node)
  const cfg = policy ? parsedNodeConfig(node) : null
  const address = serverAddress(node, t)
  const locale = i18n.language?.startsWith('zh') ? 'zh' : 'en'
  return (
    <Section title={t('section_outbound')}>
      <dl className="sb-fields">
        {policy ? (
          <Field label={t('policy_lb_strategy')} wide>
            {node.type === 'failover'
              ? t('policy_mode_failover')
              : (cfg?.strategy === 'consistent-hashing' ? t('policy_strategy_hash') : t('policy_strategy_rr'))}
          </Field>
        ) : (
          <Field label={t('server_address')} wide>
            {address ? <span className="sb-inline"><span className="mono truncate">{address}</span><CopyButton text={address} /></span> : dash}
          </Field>
        )}
        <Field label={t('node_type')}>{protocolLabel(node.type, t)}</Field>
        <Field label={t('updated_at')}>{formatDateTime(node.updated_at, locale)}</Field>
      </dl>
    </Section>
  )
}

function UpstreamSection({ node, ctl, act }) {
  const { t } = useTranslation()
  const group = ctl.groupById?.get(node.group_id) || null
  const effective = resolveEffectiveUpstream(node, group, ctl.settings)
  const mode = effective.mode
  const showEffectiveField = mode === 'group' || mode === 'global' || (mode === 'custom' && effective.disabled)
  const showManagedDetails = mode === 'custom'
    ? Boolean(node.upstream_type && node.upstream_config)
    : effective.active && (mode === 'group' || mode === 'global')
  const displayConfig = mode === 'custom' ? node.upstream_config : effective.config
  const displayIP = mode === 'custom' ? node.upstream_ip : effective.ip
  const displayLocation = mode === 'custom' ? node.upstream_location : effective.location
  const displayCountryCode = mode === 'custom' ? node.upstream_country_code : effective.country_code
  const displayLatency = mode === 'custom' ? node.upstream_latency : effective.latency
  const displayError = mode === 'custom' ? node.upstream_error : effective.error
  const address = showManagedDetails ? configEndpointAddress(displayConfig) : ''
  const effectiveLabel = effective.disabled
    ? t('upstream_effective_disabled')
    : effective.source === 'group'
      ? t('upstream_effective_group', { type: protocolLabel(effective.type, t) })
      : effective.source === 'global'
        ? t('upstream_effective_global', { type: protocolLabel(effective.type, t) })
        : t('upstream_effective_direct')
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
        {showEffectiveField ? (
          <Field label={t('upstream_effective_source')} wide={!showManagedDetails}>
            <span data-testid={`node-upstream-effective-${node.id}`}>{effectiveLabel}</span>
          </Field>
        ) : null}
        {mode === 'custom' ? (
          <Field label={t('node_type')}>{node.upstream_type ? protocolLabel(node.upstream_type, t) : dash}</Field>
        ) : null}
        {showManagedDetails ? (
          <>
            <Field label={t('upstream_address')}>{address ? <span className="mono">{address}</span> : dash}</Field>
            <Field label={t('upstream_ip')}>{displayIP ? <span className="mono">{displayIP}</span> : dash}</Field>
            <Field label={t('upstream_location')}><Region location={displayLocation} code={displayCountryCode} /></Field>
            <Field label={t('upstream_latency')}><Latency value={displayLatency} /></Field>
          </>
        ) : null}
      </dl>
      {showManagedDetails && displayError ? <p className="sb-callout sb-callout--bad sb-section__note">{displayError}</p> : null}
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
        <InboundSection node={node} ctl={ctl} act={act} pinBusy={Boolean(ctl.pinBusy[node.id])} flagBusy={Boolean(ctl.flagBusy[node.id])} preservePorts={ctl.nodePreservesPorts(node)} />
        <ConnectSection node={node} />
      </div>
      <div className="sb-detail-card" data-detail-group="outbound">
        <OutboundSection node={node} />
        <ExitSection node={node} checking={checking} />
        {isPolicyNode(node) ? <PolicyMembersSection node={node} ctl={ctl} act={act} busy={Boolean(ctl.flagBusy[node.id])} /> : <UpstreamSection node={node} ctl={ctl} act={act} />}
      </div>
      <RemarkSection node={node} act={act} />
    </>
  )
}
