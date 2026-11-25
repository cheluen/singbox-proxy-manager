import { useEffect, useState } from 'react'
import { Alert, Button, Form, Input, Modal, Select, Switch } from 'antd'
import { Lock, Pencil, Plus, RefreshCw, ShieldCheck, ShieldOff, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { formatDateTime, protocolLabel } from '../core/nodeView'
import { UpstreamDefinitionEditor } from './NodeDialogs'

const utf8Length = (text) => new TextEncoder().encode(text).length
const CERT_PAIR_SEP = '\u0000'
const certPairKey = (certPath, keyPath) => (certPath && keyPath ? `${certPath}${CERT_PAIR_SEP}${keyPath}` : '')
const splitCertPairKey = (key) => {
  if (!key || !key.includes(CERT_PAIR_SEP)) return ['', '']
  const [certPath, keyPath] = key.split(CERT_PAIR_SEP)
  return [certPath || '', keyPath || '']
}
const certDomainLabel = (cert) => {
  if (Array.isArray(cert?.domains) && cert.domains.length > 0) return cert.domains.join(', ')
  return cert?.common_name || cert?.cert_path || ''
}

export function SettingsDialogBody({ ctl, onDone }) {
  const { t } = useTranslation()
  if (!ctl.settings) {
    return <Alert type="error" showIcon message={t('settings_load_failed')} action={<Button size="small" onClick={ctl.loadSettings}>{t('retry')}</Button>} />
  }
  return <SettingsFormBody ctl={ctl} onDone={onDone} />
}

function SettingsFormBody({ ctl, onDone }) {
  const { t, i18n } = useTranslation()
  const [form] = Form.useForm()
  const saved = ctl.settings
  const locale = i18n.language?.startsWith('zh') ? 'zh' : 'en'
  const [upstream, setUpstream] = useState({ type: saved.global_upstream_type || '', config: saved.global_upstream_config || '' })
  const [editorOpen, setEditorOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [checking, setChecking] = useState(false)
  const [rescanning, setRescanning] = useState(false)
  const upstreamEnabled = Form.useWatch('global_upstream_enabled', form)
  const newSecret = Form.useWatch('admin_secret', form)
  const selectedCertPair = Form.useWatch('inbound_tls_pair', form)

  const tlsStatus = saved.inbound_tls || { ready: false, selection_mode: 'none', certificates: [] }
  const tlsCerts = Array.isArray(tlsStatus.certificates) ? tlsStatus.certificates : []
  const validTlsCerts = tlsCerts.filter((cert) => !cert.expired)
  const autoSingleCert = tlsStatus.ready && tlsStatus.selection_mode === 'auto' && tlsStatus.active ? tlsStatus.active : null
  const showMultiSelect = !autoSingleCert && (tlsStatus.selection_mode === 'selected' || tlsStatus.selection_mode === 'unselected' || validTlsCerts.length > 1)
  const savedCertKey = certPairKey(saved.inbound_tls_cert_path || '', saved.inbound_tls_key_path || '')
  const matchedSavedCertKey = validTlsCerts.some((cert) => certPairKey(cert.cert_path, cert.key_path) === savedCertKey)
    ? savedCertKey
    : ''
  const previewCert = autoSingleCert
    || tlsCerts.find((cert) => certPairKey(cert.cert_path, cert.key_path) === (selectedCertPair || matchedSavedCertKey))
    || null

  useEffect(() => {
    if (!form.isFieldTouched('inbound_tls_pair')) {
      form.setFieldValue('inbound_tls_pair', matchedSavedCertKey || undefined)
    }
  }, [form, matchedSavedCertKey])

  const configured = Boolean(upstream.type && upstream.config)
  const matchesSaved = configured && upstream.type === (saved.global_upstream_type || '') && upstream.config === (saved.global_upstream_config || '')
  const hasCheckResult = Boolean(saved.global_upstream_ip || saved.global_upstream_latency || saved.global_upstream_error)

  const rescanTLS = async () => {
    setRescanning(true)
    await ctl.loadSettings()
    setRescanning(false)
  }

  const submit = async (values) => {
    const update = {}
    if (Boolean(values.preserve_inbound_ports) !== Boolean(saved.preserve_inbound_ports)) update.preserve_inbound_ports = Boolean(values.preserve_inbound_ports)
    if (Boolean(values.global_upstream_enabled) !== Boolean(saved.global_upstream_enabled)) update.global_upstream_enabled = Boolean(values.global_upstream_enabled)
    if (upstream.type !== (saved.global_upstream_type || '')) update.global_upstream_type = upstream.type
    if (upstream.config !== (saved.global_upstream_config || '')) update.global_upstream_config = upstream.config
    if (values.admin_secret) update.admin_password = values.admin_secret
    if (showMultiSelect) {
      const nextPair = values.inbound_tls_pair || ''
      if (nextPair && nextPair !== savedCertKey) {
        const [certPath, keyPath] = splitCertPairKey(nextPair)
        if (certPath && keyPath) {
          update.inbound_tls_cert_path = certPath
          update.inbound_tls_key_path = keyPath
        }
      }
    }

    if (values.global_upstream_enabled && !configured) {
      ctl.helpers.notify.error(t('upstream_global_required'))
      return
    }
    if (Object.keys(update).length === 0) {
      ctl.helpers.notify.info(t('settings_unchanged'))
      onDone()
      return
    }
    // 关闭「保留入站端口」会按各分组起始端口重排未固定节点端口，固定端口不变；可能影响客户端连接，需二次确认。
    if (saved.preserve_inbound_ports && update.preserve_inbound_ports === false) {
      const confirmed = await ctl.helpers.confirm({ title: t('warning'), content: t('preserve_inbound_ports_disable_warning'), okText: t('confirm'), danger: true })
      if (!confirmed) return
    }
    setSaving(true)
    const ok = await ctl.saveSettings(update)
    setSaving(false)
    if (ok) onDone()
  }

  const checkUpstream = async () => {
    setChecking(true)
    await ctl.checkGlobalUpstream()
    setChecking(false)
  }

  const tlsOptions = tlsCerts.map((cert) => {
    const domains = certDomainLabel(cert)
    const exp = cert.not_after ? formatDateTime(cert.not_after, locale) : '—'
    const expSuffix = cert.expired ? ` [${t('inbound_tls_expired_badge')}]` : ''
    return {
      value: certPairKey(cert.cert_path, cert.key_path),
      label: `${domains} · ${exp}${expSuffix}`,
      disabled: Boolean(cert.expired),
    }
  })

  return (
    <Form
      form={form}
      layout="vertical"
      requiredMark={false}
      className="sb-form"
      data-testid="settings-form"
      initialValues={{
        preserve_inbound_ports: Boolean(saved.preserve_inbound_ports),
        global_upstream_enabled: Boolean(saved.global_upstream_enabled),
        inbound_tls_pair: matchedSavedCertKey || undefined,
        admin_secret: '',
        admin_secret_confirm: '',
      }}
      onFinish={submit}
    >
      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('settings_ports')}</h3>
        <div className="sb-setting-row">
          <div className="sb-setting-row__text">
            <strong>{t('preserve_inbound_ports')}</strong>
            <span>{t('preserve_inbound_ports_desc')}</span>
          </div>
          <Form.Item name="preserve_inbound_ports" valuePropName="checked" noStyle>
            <Switch aria-label={t('preserve_inbound_ports')} />
          </Form.Item>
        </div>
        <p className="sb-form-note">{t('settings_group_start_port_hint')}</p>
      </section>

      <section className="sb-form-section" data-testid="settings-inbound-tls">
        <div className="sb-form-section__head">
          <h3 className="sb-form-section__title">{t('settings_inbound_tls')}</h3>
          <Button
            size="small"
            icon={<RefreshCw size={13} strokeWidth={1.8} className={rescanning ? 'sb-spin' : undefined} />}
            loading={rescanning}
            onClick={rescanTLS}
            data-testid="inbound-tls-rescan"
          >
            {t('inbound_tls_rescan')}
          </Button>
        </div>
        {!autoSingleCert && !showMultiSelect ? (
          <div className="sb-inline-card sb-tls-card is-empty" data-testid="inbound-tls-empty">
            <span className="sb-tls-card__icon"><ShieldOff size={18} strokeWidth={1.75} /></span>
            <div className="sb-inline-card__text">
              <strong>{t('inbound_tls_none_title')}</strong>
              <span>{t('inbound_tls_none_desc')}</span>
            </div>
          </div>
        ) : autoSingleCert ? (
          <div className="sb-tls-panel" data-testid="inbound-tls-single">
            <p className="sb-form-note">{t('inbound_tls_single_hint')}</p>
            <div className="sb-inline-card sb-tls-card is-ready">
              <span className="sb-tls-card__icon"><ShieldCheck size={18} strokeWidth={1.75} /></span>
              <div className="sb-inline-card__text">
                <strong className="sb-inline">
                  <span className="truncate">{certDomainLabel(autoSingleCert)}</span>
                  <span className="sb-tls-badge is-ok">{t('inbound_tls_auto_badge')}</span>
                  {autoSingleCert.expired ? <span className="sb-tls-badge is-bad">{t('inbound_tls_expired_badge')}</span> : null}
                </strong>
                <span>
                  {`${t('inbound_tls_expires')}: ${formatDateTime(autoSingleCert.not_after, locale)}`}
                </span>
                <span className="mono sb-tls-card__path">{autoSingleCert.cert_path}</span>
              </div>
            </div>
          </div>
        ) : (
          <div className="sb-tls-panel" data-testid="inbound-tls-multi">
            <p className="sb-form-note">{t('inbound_tls_multi_hint')}</p>
            <Form.Item name="inbound_tls_pair" label={t('inbound_tls_select_label')}>
              <Select
                options={tlsOptions}
                placeholder={t('inbound_tls_select_placeholder')}
                virtual={false}
                data-testid="inbound-tls-cert-select"
              />
            </Form.Item>
            {previewCert ? (
              <div className="sb-inline-card sb-tls-card is-ready" data-testid="inbound-tls-selected-info">
                <span className="sb-tls-card__icon"><Lock size={16} strokeWidth={1.8} /></span>
                <div className="sb-inline-card__text">
                  <strong className="sb-inline">
                    <span className="truncate">{certDomainLabel(previewCert)}</span>
                    {certPairKey(previewCert.cert_path, previewCert.key_path) === matchedSavedCertKey ? (
                      <span className="sb-tls-badge is-ok">{t('inbound_tls_selected_badge')}</span>
                    ) : null}
                    {previewCert.expired ? <span className="sb-tls-badge is-bad">{t('inbound_tls_expired_badge')}</span> : null}
                  </strong>
                  <span>{`${t('inbound_tls_expires')}: ${formatDateTime(previewCert.not_after, locale)}`}</span>
                  <span className="mono sb-tls-card__path">{`${previewCert.cert_path} · ${previewCert.key_path}`}</span>
                </div>
              </div>
            ) : null}
          </div>
        )}
      </section>

      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('upstream_global')}</h3>
        <div className="sb-setting-row">
          <div className="sb-setting-row__text">
            <strong>{t('upstream_global_enable')}</strong>
            <span>{t('upstream_global_desc')}</span>
          </div>
          <Form.Item name="global_upstream_enabled" valuePropName="checked" noStyle>
            <Switch aria-label={t('upstream_global')} data-testid="global-upstream-enabled" />
          </Form.Item>
        </div>
        <div className={`sb-inline-card${upstreamEnabled && !configured ? ' is-error' : ''}`}>
          <div className="sb-inline-card__text">
            <strong>{t('upstream_global_proxy')}</strong>
            <span>
              {configured ? `${protocolLabel(upstream.type, t)} · ${matchesSaved ? t('configured') : t('configured_unsaved')}` : upstreamEnabled ? t('upstream_global_required') : t('not_configured')}
            </span>
          </div>
          <Button icon={<Zap size={14} strokeWidth={1.75} />} loading={checking} disabled={!matchesSaved} data-tip={matchesSaved ? undefined : t('upstream_check_save_first')} onClick={checkUpstream} data-testid="global-upstream-check-ip">
            {t('check_ip')}
          </Button>
          <Button icon={configured ? <Pencil size={14} strokeWidth={1.75} /> : <Plus size={14} strokeWidth={1.75} />} onClick={() => setEditorOpen(true)} data-testid="global-upstream-configure">
            {configured ? t('upstream_edit') : t('upstream_configure')}
          </Button>
        </div>
        {matchesSaved && hasCheckResult ? (
          saved.global_upstream_error ? (
            <Alert type="error" showIcon className="sb-gap" message={t('upstream_check_error')} description={saved.global_upstream_error} data-testid="global-upstream-check-result" />
          ) : (
            <dl className="sb-fields sb-fields--3 sb-gap" data-testid="global-upstream-check-result">
              <div className="sb-field"><dt>{t('upstream_ip')}</dt><dd className="mono">{saved.global_upstream_ip || '—'}</dd></div>
              <div className="sb-field"><dt>{t('upstream_location')}</dt><dd>{[saved.global_upstream_country_code, saved.global_upstream_location].filter(Boolean).join(' ') || '—'}</dd></div>
              <div className="sb-field"><dt>{t('upstream_latency')}</dt><dd className="mono">{saved.global_upstream_latency > 0 ? `${saved.global_upstream_latency} ms` : '—'}</dd></div>
            </dl>
          )
        ) : null}
      </section>

      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('admin_password')}</h3>
        <div className="sb-form-grid">
          <Form.Item
            name="admin_secret"
            label={t('new_admin_password')}
            extra={t('admin_password_leave_empty')}
            rules={[{
              validator: (_, value) => {
                if (!value) return Promise.resolve()
                if ([...value].length < 8) return Promise.reject(new Error(t('password_min_8')))
                return utf8Length(value) > 72 ? Promise.reject(new Error(t('password_max_72_bytes'))) : Promise.resolve()
              },
            }]}
          >
            <Input.Password autoComplete="new-password" placeholder={t('admin_password_placeholder_optional')} />
          </Form.Item>
          <Form.Item
            name="admin_secret_confirm"
            label={t('confirm_new_secret')}
            dependencies={['admin_secret']}
            rules={[{ validator: (_, value) => ((form.getFieldValue('admin_secret') || '') === (value || '') ? Promise.resolve() : Promise.reject(new Error(t('password_mismatch')))) }]}
          >
            <Input.Password autoComplete="new-password" disabled={!newSecret} />
          </Form.Item>
        </div>
        {newSecret ? <p className="sb-form-note">{t('relogin_notice')}</p> : null}
      </section>

      <div className="sb-form-footer">
        <Button onClick={onDone} disabled={saving}>{t('cancel')}</Button>
        <Button type="primary" htmlType="submit" loading={saving}>{t('save_settings')}</Button>
      </div>

      <Modal title={t('upstream_global_proxy')} open={editorOpen} footer={null} width={720} destroyOnHidden centered maskClosable={false} rootClassName="sb-overlay" onCancel={() => setEditorOpen(false)}>
        <UpstreamDefinitionEditor
          value={configured ? upstream : null}
          parse={ctl.parseLink}
          formName="global_upstream"
          onCancel={() => setEditorOpen(false)}
          onSave={async (next) => {
            setUpstream(next)
            setEditorOpen(false)
          }}
        />
      </Modal>
    </Form>
  )
}
