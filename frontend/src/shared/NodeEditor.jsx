// 节点 / 前置代理编辑表单。字段由 core/protocolSchema.js 声明，保存逻辑由 core/protocolForm.js 提供
import { useMemo, useState } from 'react'
import { App, Button, Collapse, Form, Input, InputNumber, Select, Switch } from 'antd'
import { Braces } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { createProtocolFormModel, normalizeClearedSelect, proxyTypes } from '../core/protocolForm'
import { groupFields } from '../core/protocolSchema'
import { InboundAuthBox } from './NodeParts'

const GROUP_TITLE = { main: 'form_group_protocol', transport: 'form_group_transport', tls: 'form_group_tls', advanced: 'form_group_advanced' }

function ProtocolField({ field, form, zh }) {
  const required = [{ required: true, message: zh ? '必填' : 'Required' }]
  const rules = field.rules ? field.rules({ form }) : field.required ? required : undefined
  const label = (
    <span className="sb-form-label">
      {field.en}
      {zh && field.zh ? <span className="sb-form-label__hint">{field.zh}</span> : null}
    </span>
  )
  const common = {
    name: field.name,
    label,
    rules,
    dependencies: field.dependencies,
    className: field.span === 2 || field.kind === 'textarea' ? 'sb-col-2' : undefined,
    extra: field.extra ? (zh ? field.extra.zh : field.extra.en) : undefined,
  }
  switch (field.kind) {
    case 'secret':
      return <Form.Item {...common}><Input.Password autoComplete="new-password" placeholder={field.placeholder} /></Form.Item>
    case 'number':
      return <Form.Item {...common}><InputNumber min={field.min} max={field.max} style={{ width: '100%' }} /></Form.Item>
    case 'select':
      return (
        <Form.Item {...common} normalize={field.clearable ? normalizeClearedSelect : undefined}>
          <Select allowClear={field.clearable} options={field.options} virtual={false} placeholder={field.required ? undefined : zh ? '默认' : 'Default'} />
        </Form.Item>
      )
    case 'switch':
      return (
        <Form.Item {...common} valuePropName="checked">
          <Switch checkedChildren={field.on} unCheckedChildren={field.off} />
        </Form.Item>
      )
    case 'textarea':
      return (
        <Form.Item {...common}>
          <Input.TextArea rows={field.rows || 3} placeholder={field.placeholder} className={field.mono ? 'mono' : undefined} spellCheck={false} />
        </Form.Item>
      )
    default:
      return <Form.Item {...common}><Input placeholder={field.placeholder} spellCheck={false} autoComplete="off" /></Form.Item>
  }
}

export default function NodeEditor({ node, variant = 'node', formName, preservePorts = false, onSave, onCancel, submitLabel }) {
  const { t, i18n } = useTranslation()
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const isUpstream = variant === 'upstream'
  const zh = Boolean(i18n.language?.startsWith('zh'))
  const [proxyType, setProxyType] = useState(node?.type || 'ss')
  const [saving, setSaving] = useState(false)
  const watched = Form.useWatch([], form)
  const authEnabled = Form.useWatch('auth_enabled', form)

  // 仅在被编辑的对象变化时重建模型；isFieldTouched 走实时的 form 实例。
  const model = useMemo(
    () => createProtocolFormModel(node, { isUpstreamEditor: isUpstream, isFieldTouched: (name) => form.isFieldTouched(name) }),
    [node, isUpstream, form]
  )
  const groups = useMemo(() => groupFields(proxyType), [proxyType])
  const values = watched || model.initialValues
  const portEditable = preservePorts || Boolean(node?.inbound_port_pinned)
  const hint = (en, zhText) => (zh ? zhText : en)

  const handleTypeChange = (nextType) => {
    const currentPort = Number(form.getFieldValue('server_port') || 0)
    if (nextType === 'wireguard' && (!currentPort || currentPort === 443)) form.setFieldsValue({ server_port: 2408 })
    else if (proxyType === 'wireguard' && nextType !== 'wireguard' && currentPort === 2408) form.setFieldsValue({ server_port: 443 })
    setProxyType(nextType)
  }

  const handleFinish = async () => {
    // 取表单的全量值：条件隐藏或位于折叠面板里的字段同样参与保存，不会被悄悄丢弃。
    const all = form.getFieldsValue(true)
    setSaving(true)
    try {
      const config = JSON.stringify(model.buildConfig(proxyType, all))
      if (isUpstream) {
        await onSave({ type: proxyType, config })
        return
      }
      await onSave({
        name: all.name,
        remark: node?.remark || '',
        type: proxyType,
        config,
        inbound_port: all.inbound_port || 0,
        auth_enabled: Boolean(all.auth_enabled),
        username: all.auth_enabled ? all.username || '' : '',
        password: all.auth_enabled ? all.password || '' : '',
        enabled: all.enabled !== false,
      })
    } catch (error) {
      message.error(error?.message || t('invalid_config'))
    } finally {
      setSaving(false)
    }
  }

  const advancedGroup = groups.find((entry) => entry.group === 'advanced')
  const visibleGroups = groups.filter((entry) => entry.group !== 'advanced')
  const renderFields = (fields) =>
    fields.map((field) => (
      <div key={field.name} className={field.span === 2 || field.kind === 'textarea' ? 'sb-col-2' : undefined} hidden={field.showWhen ? !field.showWhen(values) : false}>
        <ProtocolField field={field} form={form} zh={zh} />
      </div>
    ))

  return (
    <Form
      form={form}
      layout="vertical"
      initialValues={model.initialValues}
      onFinish={handleFinish}
      onFinishFailed={({ errorFields }) => form.scrollToField(errorFields[0]?.name, { block: 'center', behavior: 'smooth' })}
      name={formName}
      className="sb-form"
      data-testid={isUpstream ? 'upstream-editor' : 'node-form'}
    >
      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('form_group_basic')}</h3>
        <div className="sb-form-grid">
          {!isUpstream && (
            <Form.Item name="name" label={t('node_name')} rules={[{ required: true, message: t('enter_node_name') }]}>
              <Input autoFocus={!node?.id} autoComplete="off" maxLength={255} />
            </Form.Item>
          )}
          <Form.Item label={t('node_type')} required>
            <Select
              id={isUpstream ? `${formName || 'upstream'}_proxy_type` : 'proxy_type'}
              value={proxyType}
              onChange={handleTypeChange}
              options={proxyTypes}
              virtual={false}
              showSearch
              optionFilterProp="label"
            />
          </Form.Item>
          <Form.Item name="server" label={t('server_address')} hidden={proxyType === 'direct'} rules={proxyType === 'direct' ? [] : [{ required: true, message: hint('Required', '必填') }]}>
            <Input spellCheck={false} autoComplete="off" placeholder="example.com" />
          </Form.Item>
          <Form.Item name="server_port" label={t('server_port')} hidden={proxyType === 'direct'} rules={proxyType === 'direct' ? [] : [{ required: true, message: hint('Required', '必填') }]}>
            <InputNumber min={1} max={65535} style={{ width: '100%' }} />
          </Form.Item>
        </div>
        {proxyType === 'direct' ? <p className="sb-form-note">{t('direct_hint')}</p> : null}
      </section>

      {visibleGroups.map((entry) => (
        <section key={entry.group} className="sb-form-section">
          <h3 className="sb-form-section__title">{t(GROUP_TITLE[entry.group])}</h3>
          <div className="sb-form-grid">{renderFields(entry.fields)}</div>
        </section>
      ))}

      {advancedGroup ? (
        <Collapse
          ghost
          className="sb-form-collapse"
          items={[{ key: 'advanced', forceRender: true, label: `${t('form_group_advanced')} · ${advancedGroup.fields.length}`, children: <div className="sb-form-grid">{renderFields(advancedGroup.fields)}</div> }]}
        />
      ) : null}

      {!isUpstream && (
        <section className="sb-form-section">
          <h3 className="sb-form-section__title">{t('section_inbound')}</h3>
          <div className="sb-form-grid">
            <Form.Item
              name="inbound_port"
              label={t('inbound_port')}
              extra={preservePorts ? t('port_hint_preserve') : node?.inbound_port_pinned ? t('port_hint_pinned') : t('port_hint_managed')}
            >
              <InputNumber min={0} max={65535} style={{ width: '100%' }} placeholder={t('auto_assign')} disabled={!portEditable} />
            </Form.Item>
            <Form.Item name="enabled" label={t('node_enabled_label')} valuePropName="checked">
              <Switch checkedChildren={t('enabled')} unCheckedChildren={t('disabled')} />
            </Form.Item>
          </div>

          <InboundAuthBox form={form} authEnabled={authEnabled} onHint={t('auth_on_hint')} offHint={t('auth_off_hint')} />
        </section>
      )}

      {node?.config && node.type === proxyType ? (
        <Collapse
          ghost
          className="sb-form-collapse"
          items={[{
            key: 'raw',
            label: <span className="sb-inline"><Braces size={14} strokeWidth={1.75} />{t('raw_config_title')}</span>,
            children: (
              <>
                <p className="sb-form-note">{t('raw_config_hint')}</p>
                <pre className="sb-code">{JSON.stringify(model.initialConfig, null, 2)}</pre>
              </>
            ),
          }]}
        />
      ) : null}

      <div className="sb-form-footer">
        <Button onClick={onCancel} disabled={saving}>{t('cancel')}</Button>
        <Button type="primary" htmlType="submit" loading={saving}>{submitLabel || t('save')}</Button>
      </div>
    </Form>
  )
}
