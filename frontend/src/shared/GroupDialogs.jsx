// 代理节点分组相关弹窗：新建/编辑分组、删除非空分组策略、跨分组移动节点。
import { useState } from 'react'
import { Alert, Button, Form, Input, InputNumber, Radio, Select, Switch } from 'antd'
import { RefreshCw, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { formatDateTime } from '../core/nodeView'

export function GroupFormDialogBody({ group, ctl, onDone }) {
  const { t, i18n } = useTranslation()
  const [form] = Form.useForm()
  const editing = Boolean(group?.id)
  const [saving, setSaving] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const allocatePort = Form.useWatch('allocate_port', form)
  const subUrl = Form.useWatch('subscription_url', form)
  const autoUpdate = Form.useWatch('subscription_auto_update', form)
  const locale = i18n.language?.startsWith('zh') ? 'zh' : 'en'
  const hasSavedSub = Boolean(editing && String(group?.subscription_url || '').trim())
  const subMatchesSaved = hasSavedSub && String(subUrl || '').trim() === String(group?.subscription_url || '').trim()

  const submit = async (values) => {
    const payload = {
      name: String(values.name || '').trim(),
      allocate_port: Boolean(values.allocate_port),
      start_port: values.allocate_port ? Number(values.start_port || 30001) : Number(group?.start_port || values.start_port || 30001),
      subscription_url: String(values.subscription_url || '').trim(),
      subscription_auto_update: Boolean(values.subscription_url && String(values.subscription_url).trim() && values.subscription_auto_update),
      subscription_interval_minutes: Number(values.subscription_interval_minutes || 60),
    }
    setSaving(true)
    const ok = editing
      ? await ctl.updateGroup(group, payload)
      : await ctl.createGroup(payload)
    setSaving(false)
    if (ok) onDone()
  }

  const refreshSub = async () => {
    if (!group?.id) return
    setSyncing(true)
    await ctl.refreshGroupSubscription(group)
    setSyncing(false)
  }

  return (
    <Form
      form={form}
      layout="vertical"
      requiredMark={false}
      className="sb-form"
      data-testid="group-form"
      initialValues={{
        name: group?.name || '',
        allocate_port: editing ? Boolean(group?.allocate_port) : true,
        start_port: group?.start_port || 30001,
        subscription_url: group?.subscription_url || '',
        subscription_auto_update: Boolean(group?.subscription_auto_update),
        subscription_interval_minutes: group?.subscription_interval_minutes || 60,
      }}
      onFinish={submit}
    >
      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('form_group_basic')}</h3>
        <Form.Item
          name="name"
          label={t('group_name')}
          rules={[{ required: true, whitespace: true, message: t('group_name_required') }]}
        >
          <Input autoFocus maxLength={80} placeholder={t('group_name_placeholder')} data-testid="group-name-input" />
        </Form.Item>
      </section>

      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('group_port_policy')}</h3>
        <div className="sb-setting-row">
          <div className="sb-setting-row__text">
            <strong>{t('group_allocate_port')}</strong>
            <span>{t('group_allocate_port_desc')}</span>
          </div>
          <Form.Item name="allocate_port" valuePropName="checked" noStyle>
            <Switch aria-label={t('group_allocate_port')} data-testid="group-allocate-port" />
          </Form.Item>
        </div>
        {allocatePort === false ? (
          <Alert type="info" showIcon className="sb-gap" message={t('group_no_port_notice')} />
        ) : null}
        <Form.Item
          name="start_port"
          label={t('start_port')}
          extra={t('group_start_port_desc')}
          className="sb-gap"
          rules={allocatePort ? [
            { required: true, message: t('start_port_required') },
            { type: 'number', min: 1024, max: 65535, message: t('start_port_range') },
          ] : []}
        >
          <InputNumber
            min={1024}
            max={65535}
            disabled={!allocatePort}
            style={{ width: 200 }}
            data-testid="group-start-port"
          />
        </Form.Item>
      </section>

      <section className="sb-form-section">
        <h3 className="sb-form-section__title">{t('group_subscription')}</h3>
        <Form.Item
          name="subscription_url"
          label={t('group_subscription_url')}
          extra={t('group_subscription_url_desc')}
          rules={[{
            validator: (_, value) => {
              const trimmed = String(value || '').trim()
              if (!trimmed) return Promise.resolve()
              if (/^https?:\/\/\S+$/i.test(trimmed)) return Promise.resolve()
              return Promise.reject(new Error(t('group_subscription_url_invalid')))
            },
          }]}
        >
          <Input allowClear spellCheck={false} placeholder="https://example.com/subscription" data-testid="group-sub-url" />
        </Form.Item>
        <div className="sb-setting-row">
          <div className="sb-setting-row__text">
            <strong>{t('group_subscription_auto')}</strong>
            <span>{t('group_subscription_auto_desc')}</span>
          </div>
          <Form.Item name="subscription_auto_update" valuePropName="checked" noStyle>
            <Switch disabled={!String(subUrl || '').trim()} aria-label={t('group_subscription_auto')} data-testid="group-sub-auto" />
          </Form.Item>
        </div>
        <Form.Item
          name="subscription_interval_minutes"
          label={t('group_subscription_interval')}
          rules={autoUpdate ? [{ required: true, type: 'integer', min: 1, max: 525600, message: t('group_subscription_interval_range') }] : []}
        >
          <InputNumber min={1} max={525600} precision={0} step={1} disabled={!String(subUrl || '').trim() || !autoUpdate} style={{ width: 200 }} suffix={t('minutes')} data-testid="group-sub-interval" />
        </Form.Item>
        {editing && hasSavedSub ? (
          <div className="sb-inline-card">
            <div className="sb-inline-card__text">
              <strong>{t('group_subscription_status')}</strong>
              <span>
                {group.subscription_last_updated_at
                  ? `${t('updated_at')}: ${formatDateTime(group.subscription_last_updated_at, locale)}`
                  : t('group_subscription_never')}
              </span>
            </div>
            <Button
              icon={<RefreshCw size={14} strokeWidth={1.75} />}
              loading={syncing}
              disabled={!subMatchesSaved}
              data-tip={subMatchesSaved ? undefined : t('group_sub_save_first')}
              onClick={refreshSub}
              data-testid="group-sub-refresh-now"
            >
              {t('group_subscription_refresh')}
            </Button>
          </div>
        ) : null}
        {editing && group?.subscription_last_error ? (
          <Alert type="error" showIcon className="sb-gap" message={t('group_subscription_last_error')} description={group.subscription_last_error} />
        ) : null}
      </section>

      <div className="sb-form-footer">
        {editing && (ctl.groups?.length || 0) > 1 ? (
          <Button
            danger
            icon={<Trash2 size={14} strokeWidth={1.75} />}
            style={{ marginRight: 'auto' }}
            disabled={saving}
            onClick={() => ctl.open({ kind: 'group-delete', group })}
            data-testid="group-delete-open"
          >
            {t('group_delete')}
          </Button>
        ) : null}
        <Button onClick={onDone} disabled={saving}>{t('cancel')}</Button>
        <Button type="primary" htmlType="submit" loading={saving} data-testid="group-form-submit">
          {editing ? t('save') : t('group_create')}
        </Button>
      </div>
    </Form>
  )
}

export function DeleteGroupDialogBody({ group, ctl, onDone }) {
  const { t } = useTranslation()
  const otherGroups = (ctl.groups || []).filter((item) => item.id !== group.id)
  const nodeCount = ctl.allNodes.filter((node) => node.group_id === group.id).length
  const [mode, setMode] = useState(otherGroups.length > 0 ? 'move' : 'delete_nodes')
  const [targetGroupId, setTargetGroupId] = useState(otherGroups[0]?.id ?? null)
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    const ok = await ctl.deleteGroup(group, nodeCount > 0 ? { mode, targetGroupId } : {})
    setBusy(false)
    if (ok) onDone()
  }

  return (
    <div data-testid="group-delete-dialog">
      {nodeCount === 0 ? (
        <p className="sb-dialog-lead">{t('group_delete_empty_confirm', { name: group.name })}</p>
      ) : (
        <>
          <Alert
            type="warning"
            showIcon
            message={t('group_delete_nonempty_title', { name: group.name, count: nodeCount })}
            description={t('group_delete_nonempty_desc')}
          />
          <Radio.Group
            value={mode}
            onChange={(event) => setMode(event.target.value)}
            className="sb-gap"
            style={{ display: 'flex', flexDirection: 'column', gap: 12 }}
          >
            <Radio value="move" disabled={otherGroups.length === 0} data-testid="group-delete-mode-move">
              {t('group_delete_mode_move')}
            </Radio>
            {mode === 'move' && otherGroups.length > 0 ? (
              <div style={{ paddingLeft: 24 }}>
                <Select
                  style={{ width: '100%' }}
                  value={targetGroupId}
                  onChange={setTargetGroupId}
                  options={otherGroups.map((item) => ({
                    value: item.id,
                    label: `${item.name}${item.allocate_port ? '' : ` · ${t('group_no_port_badge')}`}`,
                  }))}
                  data-testid="group-delete-target"
                />
              </div>
            ) : null}
            <Radio value="delete_nodes" data-testid="group-delete-mode-delete">
              {t('group_delete_mode_delete', { count: nodeCount })}
            </Radio>
          </Radio.Group>
        </>
      )}
      <div className="sb-form-footer">
        <Button onClick={onDone} disabled={busy}>{t('cancel')}</Button>
        <Button
          type="primary"
          danger
          loading={busy}
          disabled={nodeCount > 0 && mode === 'move' && !targetGroupId}
          onClick={submit}
          data-testid="group-delete-confirm"
        >
          {t('group_delete')}
        </Button>
      </div>
    </div>
  )
}

export function MoveGroupDialogBody({ nodeIds, ctl, onDone }) {
  const { t } = useTranslation()
  const candidates = (ctl.groups || []).filter((item) => item.id !== ctl.activeGroupId)
  const [targetGroupId, setTargetGroupId] = useState(candidates[0]?.id ?? ctl.groups?.[0]?.id ?? null)
  const [busy, setBusy] = useState(false)
  const targetGroup = (ctl.groups || []).find((item) => item.id === targetGroupId)

  const submit = async () => {
    if (!targetGroupId || !nodeIds?.length) return
    setBusy(true)
    const ok = await ctl.moveNodesToGroup(nodeIds, targetGroupId)
    setBusy(false)
    if (ok) onDone()
  }

  return (
    <div data-testid="move-group-dialog">
      <p className="sb-dialog-lead">{t('move_to_group_desc', { count: nodeIds?.length || 0 })}</p>
      <Select
        style={{ width: '100%' }}
        value={targetGroupId}
        onChange={setTargetGroupId}
        options={(ctl.groups || []).map((item) => ({
          value: item.id,
          disabled: item.id === ctl.activeGroupId,
          label: `${item.name}${item.allocate_port ? '' : ` · ${t('group_no_port_badge')}`}`,
        }))}
        data-testid="move-group-select"
      />
      {targetGroup && !targetGroup.allocate_port ? (
        <Alert type="info" showIcon className="sb-gap" message={t('move_to_noport_group_hint')} />
      ) : null}
      <div className="sb-form-footer">
        <Button onClick={onDone} disabled={busy}>{t('cancel')}</Button>
        <Button type="primary" loading={busy} disabled={!targetGroupId || targetGroupId === ctl.activeGroupId} onClick={submit} data-testid="move-group-confirm">
          {t('confirm')}
        </Button>
      </div>
    </div>
  )
}
