// 批量操作弹窗内容：批量导入、批量设置认证。
import { useState } from 'react'
import { Alert, Button, Checkbox, Form, Input, Switch } from 'antd'
import { Dices, ShieldCheck, ShieldOff } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { randomCredentials } from '../core/credentials'
import { Segmented } from './primitives'

const SOURCE_TYPES = ['auto', 'subscription', 'http_proxy']

export function ImportDialogBody({ ctl, onDone, onBusyChange }) {
  const { t } = useTranslation()
  const [content, setContent] = useState('')
  const [sourceType, setSourceType] = useState('auto')
  const [enabled, setEnabled] = useState(true)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState(null)
  const [error, setError] = useState('')
  const lineCount = content.split('\n').filter((line) => line.trim()).length
  // 已有部分成功时，同一份内容不能原样再提交一次，否则成功的那部分会被重复导入。
  const retryBlocked = Boolean(result?.success > 0 && result.content === content.trim() && result.sourceType === sourceType)

  const submit = async () => {
    const trimmed = content.trim()
    if (!trimmed) {
      setError(t('import_empty'))
      return
    }
    if (busy || retryBlocked) return
    setBusy(true)
    onBusyChange?.(true)
    setResult(null)
    const outcome = await ctl.importBatch({ content: trimmed, sourceType, enabled })
    setBusy(false)
    onBusyChange?.(false)
    if (!outcome) return
    if (outcome.failed === 0) {
      ctl.helpers.notify.success(t('import_success', { count: outcome.success }))
      onDone()
      return
    }
    setResult({ ...outcome, content: trimmed, sourceType })
  }

  return (
    <>
      <p className="sb-dialog-lead">{t('batch_import_desc')}</p>

      {result ? (
        <Alert
          type={result.success > 0 ? 'warning' : 'error'}
          showIcon
          className="sb-gap"
          data-testid="batch-import-errors"
          message={t('import_result', { success: result.success, failed: result.failed })}
          description={
            <ul className="sb-error-list">
              {result.errors.map((item, index) => (
                <li key={index}>
                  <code className="mono">{item.source || item.link || `#${index + 1}`}</code>
                  <span>{item.error || t('server_error')}</span>
                </li>
              ))}
            </ul>
          }
        />
      ) : null}

      <div className="sb-labeled">
        <span className="sb-labeled__label" id="sb-import-source">{t('batch_import_source_type')}</span>
        <Segmented
          label={t('batch_import_source_type')}
          value={sourceType}
          onChange={setSourceType}
          testId="import-source"
          options={SOURCE_TYPES.map((value) => ({ value, label: t(`import_source_${value}`) }))}
        />
        <p className="sb-dim">{t(`import_source_${sourceType}_hint`)}</p>
      </div>

      <Input.TextArea
        autoFocus
        rows={9}
        value={content}
        disabled={busy}
        spellCheck={false}
        className="mono sb-gap"
        status={error ? 'error' : undefined}
        placeholder={t('import_placeholder')}
        aria-label={t('paste_links')}
        data-testid="batch-import-content"
        onChange={(event) => {
          setContent(event.target.value)
          setError('')
        }}
      />
      <div className="sb-between">
        {error ? <span className="sb-linkbar__error" role="alert">{error}</span> : <span className="sb-dim">{t('import_line_count', { count: lineCount })}</span>}
        <Checkbox checked={enabled} disabled={busy} onChange={(event) => setEnabled(event.target.checked)}>{t('enable_after_import')}</Checkbox>
      </div>
      {retryBlocked ? <p className="sb-dim sb-gap">{t('import_retry_blocked')}</p> : null}

      <div className="sb-form-footer">
        <Button onClick={onDone} disabled={busy}>{result ? t('close') : t('cancel')}</Button>
        <Button type="primary" onClick={submit} loading={busy} disabled={retryBlocked} data-testid="batch-import-submit">{t('import_action')}</Button>
      </div>
    </>
  )
}

export function BatchAuthDialogBody({ ctl, count, onDone }) {
  const { t } = useTranslation()
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)
  const authEnabled = Form.useWatch('auth_enabled', form) ?? true

  const submit = async (values) => {
    setSaving(true)
    const ok = await ctl.applyBatchAuth({
      auth_enabled: Boolean(values.auth_enabled),
      username: values.auth_enabled ? values.username || '' : '',
      password: values.auth_enabled ? values.password || '' : '',
    })
    setSaving(false)
    if (ok) onDone()
  }

  return (
    <Form form={form} layout="vertical" initialValues={{ auth_enabled: true, username: '', password: '' }} onFinish={submit} className="sb-form" data-testid="batch-auth-form">
      <p className="sb-dialog-lead">{t('batch_auth_scope', { count })}</p>
      <div className={`sb-auth-box${authEnabled ? '' : ' is-open'}`}>
        <div className="sb-auth-box__head">
          <span className="sb-auth-box__icon">{authEnabled ? <ShieldCheck size={18} strokeWidth={1.75} /> : <ShieldOff size={18} strokeWidth={1.75} />}</span>
          <div className="sb-auth-box__text">
            <strong>{t('inbound_authentication')}</strong>
            <span>{authEnabled ? t('batch_auth_on_hint') : t('batch_auth_mode_desc')}</span>
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
              rules={[{
                validator: (_, value) => {
                  if (!value) return Promise.reject(new Error(t('enter_username')))
                  return String(value).includes('+') ? Promise.reject(new Error(t('username_plus_not_allowed'))) : Promise.resolve()
                },
              }]}
            >
              <Input autoFocus autoComplete="off" spellCheck={false} data-testid="batch-auth-username" />
            </Form.Item>
            <Form.Item name="password" label={t('password_auth')} rules={[{ required: true, message: t('enter_password') }]}>
              <Input.Password autoComplete="new-password" data-testid="batch-auth-secret" />
            </Form.Item>
          </div>
        ) : null}
      </div>
      <div className="sb-form-footer">
        <Button onClick={onDone} disabled={saving}>{t('cancel')}</Button>
        <Button type="primary" htmlType="submit" loading={saving} danger={!authEnabled}>{authEnabled ? t('apply_auth') : t('batch_auth_disable_action')}</Button>
      </div>
    </Form>
  )
}
