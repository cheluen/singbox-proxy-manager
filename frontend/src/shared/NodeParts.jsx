// 节点信息的可复用展示块：端口、认证、前置代理、备注、连接方式。
import { memo, useEffect, useState } from 'react'
import { Button, Form, Input, Switch } from 'antd'
import { Dices, Eye, EyeOff, Route, ShieldCheck, ShieldOff, StickyNote, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { randomCredentials } from '../core/credentials'
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

export const AuthMark = memo(function AuthMark() {
  const { t } = useTranslation()
  const label = t('auth_open_warning')
  return (
    <span className="sb-auth-mark is-open" role="img" aria-label={label} data-tip={label}>
      <ShieldOff size={14} strokeWidth={1.75} />
    </span>
  )
})

// 入站认证开关 + 随机凭据 + 用户名/密码表单块（节点编辑与批量设密共用）。
export function InboundAuthBox({ form, authEnabled, onHint, offHint, autoFocusUsername = false, usernameTestId, passwordTestId }) {
  const { t } = useTranslation()
  return (
    <div className={`sb-auth-box${authEnabled ? '' : ' is-open'}`}>
      <div className="sb-auth-box__head">
        <span className="sb-auth-box__icon">{authEnabled ? <ShieldCheck size={18} strokeWidth={1.75} /> : <ShieldOff size={18} strokeWidth={1.75} />}</span>
        <div className="sb-auth-box__text">
          <strong>{t('inbound_authentication')}</strong>
          <span>{authEnabled ? onHint : offHint}</span>
        </div>
        {authEnabled ? (
          <Button size="small" type="link" icon={<Dices size={14} strokeWidth={1.75} />} data-testid="auth-generate" onClick={() => {
            const next = randomCredentials()
            form.setFieldsValue({ username: next.username, password: next.secret })
            form.validateFields(['username', 'password']).catch(() => {})
          }}>
            {t('auth_generate')}
          </Button>
        ) : null}
        <Form.Item name="auth_enabled" valuePropName="checked" noStyle>
          <Switch
            aria-label={t('inbound_authentication')}
            onChange={(on) => {
              if (!on) form.setFieldsValue({ username: '', password: '' })
            }}
          />
        </Form.Item>
      </div>
      {authEnabled ? (
        <div className="sb-form-grid sb-auth-box__fields">
          <Form.Item
            name="username"
            label={t('username')}
            required
            dependencies={['auth_enabled']}
            rules={[{
              validator: (_, value) => {
                if (!form.getFieldValue('auth_enabled')) return Promise.resolve()
                if (!value) return Promise.reject(new Error(t('enter_username')))
                return String(value).includes('+') ? Promise.reject(new Error(t('username_plus_not_allowed'))) : Promise.resolve()
              },
            }]}
          >
            <Input autoFocus={autoFocusUsername} autoComplete="off" spellCheck={false} data-testid={usernameTestId} />
          </Form.Item>
          <Form.Item
            name="password"
            label={t('password_auth')}
            required
            dependencies={['auth_enabled']}
            rules={[{ validator: (_, value) => (!form.getFieldValue('auth_enabled') || value ? Promise.resolve() : Promise.reject(new Error(t('enter_password')))) }]}
          >
            <Input.Password autoComplete="new-password" data-testid={passwordTestId} />
          </Form.Item>
        </div>
      ) : null}
    </div>
  )
}

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
export function RemarkEditor({ node, act }) {
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
        autoSize={{ minRows: 3, maxRows: 10 }}
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
