// 节点信息的可复用展示块：端口、入站模式、认证、前置代理、备注、连接方式。
import { memo, useEffect, useRef, useState } from 'react'
import { Button, Form, Input, Switch } from 'antd'
import { Dices, Eye, EyeOff, Lock, Route, ShieldCheck, ShieldOff, StickyNote, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { randomCredentials } from '../core/credentials'
import { buildHostPort, buildProxyUrl, hasInboundAuth, initialUpstreamMode, panelHost, reuseUsername, shellQuote } from '../core/nodeView'
import { PinGlyph } from './icons'
import { upstreamModeLabel } from './labels'
import { CopyButton, IconButton, Spinner } from './primitives'

// 入站端口 + 固定开关。开启「保留入站端口」后或无端口分组节点不显示固定开关。
export const PortChip = memo(function PortChip({ node, act, pinBusy = false, preservePorts = false, copyable = false, compact = false }) {
  const { t } = useTranslation()
  const hasPort = Number(node.inbound_port) > 0
  const pinned = hasPort && Boolean(node.inbound_port_pinned)
  const busy = Boolean(pinBusy)
  if (!hasPort) {
    return (
      <span className="sb-port sb-dim" onClick={(event) => event.stopPropagation()} data-tip={t('group_no_port_node_tip')}>
        <span className="mono sb-port__value">—</span>
      </span>
    )
  }
  return (
    <span className={`sb-port${pinned ? ' is-pinned' : ''}`} onClick={(event) => event.stopPropagation()}>
      <span className="mono sb-port__value">{node.inbound_port}</span>
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
          {busy ? <Spinner size={compact ? 11 : 12} /> : <PinGlyph pinned={pinned} size={compact ? 12 : 13} />}
        </button>
      )}
      {copyable ? <CopyButton text={buildHostPort(node)} label={`${t('copy')} ${buildHostPort(node)}`} /> : null}
    </span>
  )
})

export const InboundTLSBadge = memo(function InboundTLSBadge({ node }) {
  const { t } = useTranslation()
  if (Number(node?.inbound_port) <= 0 || node?.inbound_mode !== 'https') return null
  const label = t('inbound_mode_https_mark')
  return (
    <span className="sb-mini-mark is-tls" role="img" aria-label={label} data-tip={label} data-testid={`node-https-mark-${node.id}`}>
      <Lock size={12} strokeWidth={2} />
    </span>
  )
})

// 入站模式滑动切换控件（Mixed ↔ HTTPS）：无有效 TLS 证书时点击 HTTPS 播放滑过去再弹回 Mixed 的微动效并提示引导。
export function InboundModeSwitch({ value = 'mixed', tlsStatus, disabled = false, busy = false, onChange, onRejected, testId }) {
  const { t } = useTranslation()
  const [bounceSeq, setBounceSeq] = useState(0)
  const [bouncing, setBouncing] = useState(false)
  const timerRef = useRef(null)
  const rafRef = useRef(null)
  const isHttps = value === 'https'
  const tlsReady = Boolean(tlsStatus?.ready)
  const rejectReason = tlsStatus?.selection_mode === 'unselected'
    ? t('inbound_tls_select_in_settings')
    : t('inbound_tls_missing_guide')

  useEffect(() => () => {
    window.clearTimeout(timerRef.current)
    window.cancelAnimationFrame(rafRef.current)
  }, [])

  const triggerBounce = () => {
    window.clearTimeout(timerRef.current)
    window.cancelAnimationFrame(rafRef.current)
    setBouncing(false)
    rafRef.current = window.requestAnimationFrame(() => {
      setBounceSeq((seq) => seq + 1)
      setBouncing(true)
      timerRef.current = window.setTimeout(() => setBouncing(false), 540)
    })
  }

  const allowMixedReset = disabled && isHttps
  const select = (target) => {
    if (busy) return
    if (disabled && !(target === 'mixed' && allowMixedReset)) return
    if (target === (isHttps ? 'https' : 'mixed')) return
    if (target === 'https' && !tlsReady) {
      triggerBounce()
      onRejected?.(rejectReason)
      return
    }
    onChange?.(target)
  }

  return (
    <div
      className={`sb-mode-switch${isHttps ? ' is-https' : ''}${bouncing ? ' is-bouncing' : ''}${!tlsReady ? ' is-tls-missing' : ''}${disabled && !allowMixedReset ? ' is-disabled' : ''}${busy ? ' is-busy' : ''}`}
      role="radiogroup"
      aria-label={t('inbound_mode')}
      data-testid={testId}
      data-mode={isHttps ? 'https' : 'mixed'}
      data-bouncing={bouncing ? 'true' : 'false'}
      data-bounce-seq={bounceSeq}
      data-tls-ready={tlsReady ? 'true' : 'false'}
      onClick={(event) => event.stopPropagation()}
    >
      <span key={bounceSeq} className="sb-mode-switch__thumb" aria-hidden="true" />
      <button
        type="button"
        role="radio"
        aria-checked={!isHttps}
        disabled={busy || (disabled && !allowMixedReset)}
        className={`sb-mode-switch__opt${!isHttps ? ' is-active' : ''}`}
        data-value="mixed"
        onClick={() => select('mixed')}
      >
        {t('inbound_mode_mixed')}
      </button>
      <button
        type="button"
        role="radio"
        aria-checked={isHttps}
        disabled={disabled || busy}
        aria-disabled={disabled || busy || !tlsReady}
        className={`sb-mode-switch__opt${isHttps ? ' is-active' : ''}${!tlsReady ? ' is-locked' : ''}`}
        data-value="https"
        data-tip={!tlsReady ? rejectReason : undefined}
        onClick={() => select('https')}
      >
        <Lock size={11} strokeWidth={2} aria-hidden="true" />
        <span>{t('inbound_mode_https')}</span>
      </button>
    </div>
  )
}

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
        autoSize={{ minRows: 4, maxRows: 16 }}
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
  const isHttps = node.inbound_mode === 'https'
  const httpUrl = buildProxyUrl(node, isHttps ? 'https' : 'http')
  const socksUrl = isHttps ? '' : buildProxyUrl(node, 'socks5')
  const curl = `curl -x ${shellQuote(httpUrl)} http://httpbin.org/ip`
  const reuse = node.tcp_reuse_enabled !== false ? reuseUsername(node) : ''
  if (!node.inbound_port) return <p className="sb-dim">{t('connect_no_port')}</p>
  return (
    <div className="sb-connect" data-inbound-mode={isHttps ? 'https' : 'mixed'}>
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
      {isHttps ? (
        <>
          <UrlRow label="HTTPS" url={httpUrl} masked={maskSecret(httpUrl, node, visible)} />
          <UrlRow label="curl" url={curl} masked={maskSecret(curl, node, visible)} />
        </>
      ) : (
        <>
          <UrlRow label="HTTP" url={httpUrl} masked={maskSecret(httpUrl, node, visible)} />
          <UrlRow label="SOCKS5" url={socksUrl} masked={maskSecret(socksUrl, node, visible)} />
          <UrlRow label="curl" url={curl} masked={maskSecret(curl, node, visible)} />
        </>
      )}
      {reuse ? (
        <div className="sb-connect__reuse" data-testid={`node-route-id-section-${node.id}`}>
          <div className="sb-url">
            <span className="sb-url__label" data-testid={`node-route-id-label-${node.id}`}>{t('tcp_reuse_short')}</span>
            <code className="sb-url__value mono" data-testid={`node-route-id-value-${node.id}`}>{reuse}</code>
            <CopyButton text={reuse} label={`${t('copy')} ${reuse}`} />
          </div>
          <p className="sb-dim">{t('tcp_reuse_explain')}</p>
        </div>
      ) : null}
    </div>
  )
}
