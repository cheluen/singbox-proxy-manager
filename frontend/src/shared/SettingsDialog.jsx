import { useState } from 'react'
import { Alert, Button, Form, Input, InputNumber, Modal, Switch } from 'antd'
import { Pencil, Plus, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { protocolLabel } from '../core/nodeView'
import { UpstreamDefinitionEditor } from './NodeDialogs'

const utf8Length = (text) => new TextEncoder().encode(text).length

export function SettingsDialogBody({ ctl, onDone }) {
  const { t } = useTranslation()
  if (!ctl.settings) {
    return <Alert type="error" showIcon message={t('settings_load_failed')} action={<Button size="small" onClick={ctl.loadSettings}>{t('retry')}</Button>} />
  }
  return <SettingsFormBody ctl={ctl} onDone={onDone} />
}

function SettingsFormBody({ ctl, onDone }) {
  const { t } = useTranslation()
  const [form] = Form.useForm()
  const saved = ctl.settings
  const [upstream, setUpstream] = useState({ type: saved.global_upstream_type || '', config: saved.global_upstream_config || '' })
  const [editorOpen, setEditorOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [checking, setChecking] = useState(false)
  const upstreamEnabled = Form.useWatch('global_upstream_enabled', form)
  const newSecret = Form.useWatch('admin_secret', form)

  const configured = Boolean(upstream.type && upstream.config)
  const matchesSaved = configured && upstream.type === (saved.global_upstream_type || '') && upstream.config === (saved.global_upstream_config || '')
  const hasCheckResult = Boolean(saved.global_upstream_ip || saved.global_upstream_latency || saved.global_upstream_error)

  const submit = async (values) => {
    const update = {}
    if (Number(values.start_port) !== Number(saved.start_port)) update.start_port = values.start_port
    if (Boolean(values.preserve_inbound_ports) !== Boolean(saved.preserve_inbound_ports)) update.preserve_inbound_ports = Boolean(values.preserve_inbound_ports)
    if (Boolean(values.global_upstream_enabled) !== Boolean(saved.global_upstream_enabled)) update.global_upstream_enabled = Boolean(values.global_upstream_enabled)
    if (upstream.type !== (saved.global_upstream_type || '')) update.global_upstream_type = upstream.type
    if (upstream.config !== (saved.global_upstream_config || '')) update.global_upstream_config = upstream.config
    if (values.admin_secret) update.admin_password = values.admin_secret

    if (values.global_upstream_enabled && !configured) {
      ctl.helpers.notify.error(t('upstream_global_required'))
      return
    }
    if (Object.keys(update).length === 0) {
      ctl.helpers.notify.info(t('settings_unchanged'))
      onDone()
      return
    }
    // 关闭「保留入站端口」会重分配未固定节点的端口，固定端口不变；可能影响客户端连接，需二次确认。
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

  return (
    <Form
      form={form}
      layout="vertical"
      requiredMark={false}
      className="sb-form"
      data-testid="settings-form"
      initialValues={{ start_port: saved.start_port, preserve_inbound_ports: Boolean(saved.preserve_inbound_ports), global_upstream_enabled: Boolean(saved.global_upstream_enabled), admin_secret: '', admin_secret_confirm: '' }}
      onFinish={submit}
    >
      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('settings_ports')}</h3>
        <Form.Item
          name="start_port"
          label={t('start_port')}
          extra={t('start_port_desc')}
          rules={[{ required: true, message: t('start_port_required') }, { type: 'number', min: 1024, max: 65535, message: t('start_port_range') }]}
        >
          <InputNumber style={{ width: 200 }} />
        </Form.Item>
        <div className="sb-setting-row">
          <div className="sb-setting-row__text">
            <strong>{t('preserve_inbound_ports')}</strong>
            <span>{t('preserve_inbound_ports_desc')}</span>
          </div>
          <Form.Item name="preserve_inbound_ports" valuePropName="checked" noStyle>
            <Switch aria-label={t('preserve_inbound_ports')} />
          </Form.Item>
        </div>
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
              {configured ? `${protocolLabel(upstream.type)} · ${matchesSaved ? t('configured') : t('configured_unsaved')}` : upstreamEnabled ? t('upstream_global_required') : t('not_configured')}
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
