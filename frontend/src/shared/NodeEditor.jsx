// 节点 / 前置代理编辑表单。字段由 core/protocolSchema.js 声明，保存逻辑由 core/protocolForm.js 提供
import { useMemo, useState } from 'react'
import { App, Button, Collapse, Form, Input, InputNumber, Radio, Select, Switch } from 'antd'
import { ArrowDown, ArrowUp, Braces, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { createProtocolFormModel, isPolicyType, normalizeClearedSelect, proxyTypes, upstreamProxyTypes } from '../core/protocolForm'
import { groupFields, protocolLabel } from '../core/protocolSchema'
import { isPolicyNode } from '../core/nodeModel'
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

function PolicyMemberSelector({ value = [], onChange, allNodes = [], groups = [], excludeNodeId }) {
  const { t } = useTranslation()
  const entityNodes = useMemo(
    () => allNodes.filter((item) => !isPolicyNode(item) && item.id !== excludeNodeId),
    [allNodes, excludeNodeId]
  )
  const nodeById = useMemo(() => new Map(entityNodes.map((item) => [item.id, item])), [entityNodes])
  const groupById = useMemo(() => new Map(groups.map((item) => [item.id, item])), [groups])

  const groupedOptions = useMemo(() => {
    const byGroup = new Map()
    for (const item of entityNodes) {
      const gid = item.group_id ?? 0
      if (!byGroup.has(gid)) byGroup.set(gid, [])
      byGroup.get(gid).push(item)
    }
    const result = []
    for (const group of groups) {
      const list = byGroup.get(group.id)
      if (!list || list.length === 0) continue
      result.push({
        label: group.allocate_port === false ? `${group.name} (${t('group_no_port_badge')})` : group.name,
        options: list.map((item) => ({
          value: item.id,
          label: `${item.name} · ${protocolLabel(item.type)}${item.enabled === false ? ` [${t('disabled')}]` : ''}`,
        })),
      })
      byGroup.delete(group.id)
    }
    for (const [, list] of byGroup) {
      result.push({
        label: t('nodes'),
        options: list.map((item) => ({
          value: item.id,
          label: `${item.name} · ${protocolLabel(item.type)}${item.enabled === false ? ` [${t('disabled')}]` : ''}`,
        })),
      })
    }
    return result
  }, [entityNodes, groups, t])

  const selectedIds = Array.isArray(value) ? value : []
  const move = (index, delta) => {
    const target = index + delta
    if (target < 0 || target >= selectedIds.length) return
    const next = [...selectedIds]
    ;[next[index], next[target]] = [next[target], next[index]]
    onChange?.(next)
  }
  const remove = (id) => onChange?.(selectedIds.filter((item) => item !== id))

  return (
    <div className="sb-policy-picker" data-testid="policy-member-picker">
      <Select
        mode="multiple"
        showSearch
        optionFilterProp="label"
        placeholder={t('policy_members_placeholder')}
        value={selectedIds}
        options={groupedOptions}
        onChange={(next) => onChange?.(next)}
        style={{ width: '100%' }}
        data-testid="policy-members-select"
      />
      {selectedIds.length > 0 ? (
        <ul className="sb-policy-order" data-testid="policy-member-order">
          {selectedIds.map((id, index) => {
            const member = nodeById.get(id)
            const groupName = member ? groupById.get(member.group_id)?.name : ''
            return (
              <li key={id} className="sb-policy-order__item" data-member-id={id}>
                <span className="sb-policy-order__idx mono">{index + 1}</span>
                <span className="sb-policy-order__name truncate">{member ? member.name : `#${id} (${t('policy_member_missing')})`}</span>
                {groupName ? <span className="sb-policy-order__meta">{groupName}</span> : null}
                {member ? <span className="sb-tag sb-tag--protocol">{protocolLabel(member.type)}</span> : null}
                {member?.enabled === false ? <span className="sb-policy-order__warn">{t('disabled')}</span> : null}
                <span className="sb-policy-order__ops">
                  <button type="button" className="sb-icon-btn sb-icon-btn--sm" aria-label={t('move_up')} disabled={index === 0} onClick={() => move(index, -1)}>
                    <ArrowUp size={13} strokeWidth={1.8} />
                  </button>
                  <button type="button" className="sb-icon-btn sb-icon-btn--sm" aria-label={t('move_down')} disabled={index === selectedIds.length - 1} onClick={() => move(index, 1)}>
                    <ArrowDown size={13} strokeWidth={1.8} />
                  </button>
                  <button type="button" className="sb-icon-btn sb-icon-btn--sm" aria-label={t('delete')} onClick={() => remove(id)}>
                    <X size={13} strokeWidth={1.8} />
                  </button>
                </span>
              </li>
            )
          })}
        </ul>
      ) : null}
    </div>
  )
}

export default function NodeEditor({ node, variant = 'node', formName, preservePorts = false, allocatePort = true, allNodes = [], groups = [], onSave, onCancel, submitLabel }) {
  const { t, i18n } = useTranslation()
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const isUpstream = variant === 'upstream'
  const zh = Boolean(i18n.language?.startsWith('zh'))
  const [proxyType, setProxyType] = useState(node?.type || 'ss')
  const [saving, setSaving] = useState(false)
  const watched = Form.useWatch([], form)
  const authEnabled = Form.useWatch('auth_enabled', form)
  const isPolicy = !isUpstream && isPolicyType(proxyType)
  const typeOptions = isUpstream ? upstreamProxyTypes : proxyTypes

  // 仅在被编辑的对象变化时重建模型；isFieldTouched 走实时的 form 实例。
  const model = useMemo(
    () => createProtocolFormModel(node, { isUpstreamEditor: isUpstream, isFieldTouched: (name) => form.isFieldTouched(name) }),
    [node, isUpstream, form]
  )
  const fieldGroups = useMemo(() => groupFields(proxyType), [proxyType])
  const values = watched || model.initialValues
  const portEditable = allocatePort && (preservePorts || Boolean(node?.inbound_port_pinned))
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

  const advancedGroup = fieldGroups.find((entry) => entry.group === 'advanced')
  const visibleGroups = fieldGroups.filter((entry) => entry.group !== 'advanced')
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
              options={typeOptions}
              virtual={false}
              showSearch
              optionFilterProp="label"
            />
          </Form.Item>
          <Form.Item name="server" label={t('server_address')} hidden={proxyType === 'direct' || isPolicy} rules={proxyType === 'direct' || isPolicy ? [] : [{ required: true, message: hint('Required', '必填') }]}>
            <Input spellCheck={false} autoComplete="off" placeholder="example.com" />
          </Form.Item>
          <Form.Item name="server_port" label={t('server_port')} hidden={proxyType === 'direct' || isPolicy} rules={proxyType === 'direct' || isPolicy ? [] : [{ required: true, message: hint('Required', '必填') }]}>
            <InputNumber min={1} max={65535} style={{ width: '100%' }} />
          </Form.Item>
        </div>
        {proxyType === 'direct' ? <p className="sb-form-note">{t('direct_hint')}</p> : null}
        {isPolicy ? <p className="sb-form-note">{proxyType === 'failover' ? t('policy_failover_hint') : t('policy_loadbalance_hint')}</p> : null}
      </section>

      {isPolicy ? (
        <section className="sb-form-section">
          <h3 className="sb-form-section__title">{t('policy_config_title')}</h3>
          {proxyType === 'loadbalance' ? (
            <Form.Item name="policy_strategy" label={t('policy_lb_strategy')}>
              <Radio.Group data-testid="policy-lb-strategy">
                <Radio value="round-robin">{t('policy_strategy_rr')}</Radio>
                <Radio value="consistent-hashing">{t('policy_strategy_hash')}</Radio>
              </Radio.Group>
            </Form.Item>
          ) : null}
          <Form.Item
            name="policy_member_ids"
            label={proxyType === 'failover' ? t('policy_members_priority') : t('policy_members')}
            extra={t('policy_members_desc')}
            rules={[{
              validator: (_, value) => (Array.isArray(value) && value.length > 0 ? Promise.resolve() : Promise.reject(new Error(t('policy_members_required')))),
            }]}
          >
            <PolicyMemberSelector allNodes={allNodes} groups={groups} excludeNodeId={node?.id} />
          </Form.Item>
          <Collapse
            ghost
            className="sb-form-collapse"
            items={[{
              key: 'policy-health',
              forceRender: true,
              label: t('policy_health_advanced'),
              children: (
                <div className="sb-form-grid">
                  <Form.Item name="policy_health_check_url" label={t('policy_health_url')} extra={t('policy_health_url_desc')}>
                    <Input spellCheck={false} placeholder="https://www.gstatic.com/generate_204" />
                  </Form.Item>
                  <Form.Item name="policy_health_check_interval" label={t('policy_health_interval')}>
                    <InputNumber min={10} max={3600} style={{ width: '100%' }} suffix="s" />
                  </Form.Item>
                </div>
              ),
            }]}
          />
        </section>
      ) : null}

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
              extra={!allocatePort ? t('port_hint_group_disabled') : preservePorts ? t('port_hint_preserve') : node?.inbound_port_pinned ? t('port_hint_pinned') : t('port_hint_managed')}
            >
              <InputNumber min={0} max={65535} style={{ width: '100%' }} placeholder={allocatePort ? t('auto_assign') : t('group_no_port_badge')} disabled={!portEditable} />
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
