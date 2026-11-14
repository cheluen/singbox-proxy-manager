// 节点信息的可复用展示块：端口、认证、前置代理、备注、连接方式。
import { memo, useEffect, useState } from 'react'
import { Button, Input } from 'antd'
import { Eye, EyeOff, Route, ShieldCheck, ShieldOff, StickyNote, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { buildHostPort, buildProxyUrl, hasInboundAuth, initialUpstreamMode, panelHost, reuseUsername, shellQuote } from '../core/nodeView'
import { PinGlyph } from './icons'
import { upstreamModeLabel } from './labels'
import { CopyButton, IconButton, Spinner } from './primitives'

// 入站端口 + 固定开关。开启「保留入站端口」后端口本身不会被重排，因此不再显示固定开关。
export const PortChip = memo(function PortChip({ node, act, pinBusy = false, preservePorts = false, copyable = false }) {
  const { t } = useTranslation()
  const pinned = Boolean(node.inbound_port_pinned)
  const busy = Boolean(pinBusy)
  return (
    <span className={`sb-port${pinned ? ' is-pinned' : ''}`} onClick={(event) => event.stopPropagation()}>
      <span className="mono sb-port__value">{node.inbound_port || '—'}</span>
      {!preservePorts && (
        <button
          type="button"
          className="sb-port__pin"
          aria-label={t(pinned ? 'port_pin_remove_hint' : 'port_pin_add_hint')}
          data-tip={t(pinned ? 'port_pin_remove_hint' : 'port_pin_add_hint')}
          aria-pressed={pinned}
          disabled={busy}
          data-testid={`node-port-pin-${node.id}`}
          onClick={() => act.togglePortPin(node)}
        >
          {busy ? <Spinner size={12} /> : <PinGlyph pinned={pinned} size={13} />}
        </button>
      )}
      {copyable && node.inbound_port ? <CopyButton text={buildHostPort(node)} label={`${t('copy')} ${buildHostPort(node)}`} /> : null}
    </span>
  )
})

export const AuthMark = memo(function AuthMark({ node, withText = false }) {
  const { t } = useTranslation()
  const on = hasInboundAuth(node)
  const label = on ? `${t('inbound_authentication')}: ${node.username}` : t('auth_open_warning')
  return (
    <span className={`sb-auth-mark${on ? '' : ' is-open'}`} role="img" aria-label={label} data-tip={label}>
      {on ? <ShieldCheck size={14} strokeWidth={1.75} /> : <ShieldOff size={14} strokeWidth={1.75} />}
      {withText ? <span className="truncate">{on ? node.username : t('auth_none')}</span> : null}
    </span>
  )
})

export const UpstreamMark = memo(function UpstreamMark({ node }) {
  const { t } = useTranslation()
  const mode = initialUpstreamMode(node)
  if (mode === 'none') return null
  const label = `${t('upstream_mode')}: ${upstreamModeLabel(mode, t)}`
  return (
    <span className={`sb-mini-mark${node.upstream_error ? ' is-bad' : ''}`} role="img" aria-label={label} data-tip={label}>
      <Route size={13} strokeWidth={1.75} />
    </span>
  )
})

export const RemarkMark = memo(function RemarkMark({ node }) {
  const remark = String(node.remark || '').trim()
  if (!remark) return null
  const preview = remark.length > 200 ? `${remark.slice(0, 200)}…` : remark
  return (
    <span className="sb-mini-mark" role="img" aria-label={preview} data-tip={preview}><StickyNote size={13} strokeWidth={1.75} /></span>
  )
})

// 备注：就地编辑，有改动时才出现保存 / 还原。
export function RemarkEditor({ node, act, rows = 3 }) {
  const { t } = useTranslation()
  const original = node.remark ?? ''
  const [draft, setDraft] = useState(original)
  const [saving, setSaving] = useState(false)
  useEffect(() => setDraft(original), [node.id, original])
  const dirty = draft !== original
  const save = async () => {
    setSaving(true)
    await act.saveRemark(node, draft)
    setSaving(false)
  }
  return (
    <div className="sb-remark">
      <Input.TextArea
        value={draft}
        autoSize={{ minRows: rows, maxRows: 10 }}
        placeholder={t('remark_empty_hint')}
        aria-label={t('remark')}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if ((event.metaKey || event.ctrlKey) && event.key === 'Enter' && dirty && !saving) save()
        }}
      />
      {dirty ? (
        <div className="sb-remark__actions">
          <span className="sb-dim">{t('unsaved_changes')}</span>
          <Button size="small" onClick={() => setDraft(original)} disabled={saving}>{t('reset')}</Button>
          <Button size="small" type="primary" onClick={save} loading={saving}>{t('save')}</Button>
        </div>
      ) : null}
    </div>
  )
}

function UrlRow({ label, url, masked }) {
  const { t } = useTranslation()
  return (
    <div className="sb-url">
      <span className="sb-url__label">{label}</span>
      <code className="sb-url__value mono" title={masked}>{masked}</code>
      <CopyButton text={url} label={`${t('copy')} ${label}`} />
    </div>
  )
}

const maskSecret = (url, node, visible) =>
  visible || !hasInboundAuth(node) ? url : url.replace(`:${encodeURIComponent(node.password)}@`, ':••••••@')

// 「怎么连这个节点」：给出可直接复制的代理地址。旧面板里用户需要自己拼接。
export function ConnectInfo({ node }) {
  const { t } = useTranslation()
  const [visible, setVisible] = useState(false)
  const auth = hasInboundAuth(node)
  const httpUrl = buildProxyUrl(node, 'http')
  const socksUrl = buildProxyUrl(node, 'socks5')
  const curl = `curl -x ${shellQuote(httpUrl)} http://httpbin.org/ip`
  const reuse = node.tcp_reuse_enabled !== false ? reuseUsername(node) : ''
  if (!node.inbound_port) return <p className="sb-dim">{t('connect_no_port')}</p>
  return (
    <div className="sb-connect">
      {node.enabled === false ? (
        <p className="sb-callout sb-callout--warn"><TriangleAlert size={15} strokeWidth={1.75} />{t('connect_disabled_hint')}</p>
      ) : null}
      {!auth ? (
        <p className="sb-callout sb-callout--bad"><ShieldOff size={15} strokeWidth={1.75} />{t('auth_open_warning')}</p>
      ) : null}
      <div className="sb-connect__head">
        <span className="sb-dim">{t('connect_host_hint', { host: panelHost() })}</span>
        {auth ? (
          <IconButton label={visible ? t('hide') : t('show')} icon={visible ? EyeOff : Eye} size="sm" onClick={() => setVisible((prev) => !prev)} />
        ) : null}
      </div>
      <UrlRow label="HTTP" url={httpUrl} masked={maskSecret(httpUrl, node, visible)} />
      <UrlRow label="SOCKS5" url={socksUrl} masked={maskSecret(socksUrl, node, visible)} />
      <UrlRow label="curl" url={curl} masked={maskSecret(curl, node, visible)} />
      {reuse ? (
        <div className="sb-connect__reuse">
          <div className="sb-url">
            <span className="sb-url__label">{t('tcp_reuse_short')}</span>
            <code className="sb-url__value mono">{reuse}</code>
            <CopyButton text={reuse} label={`${t('copy')} ${reuse}`} />
          </div>
          <p className="sb-dim">{t('tcp_reuse_explain')}</p>
        </div>
      ) : null}
    </div>
  )
}
