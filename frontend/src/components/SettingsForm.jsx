import React, { useState, useEffect } from 'react'
import {
  Alert,
  Button,
  Descriptions,
  Divider,
  Form,
  Input,
  InputNumber,
  message,
  Modal,
  Space,
  Switch,
  Tooltip,
} from 'antd'
import { EditOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'
import UpstreamEditor from './UpstreamEditor'

function SettingsForm({ onClose, onUpdated, onPasswordChanged }) {
  const { t } = useTranslation()
  const [form] = Form.useForm()
  const [loading, setLoading] = useState(false)
  const [loadingData, setLoadingData] = useState(true)
  const [settingsData, setSettingsData] = useState(null)
  const [initialPreserveInboundPorts, setInitialPreserveInboundPorts] = useState(false)
  const [upstreamEditorOpen, setUpstreamEditorOpen] = useState(false)
  const [globalUpstream, setGlobalUpstream] = useState({ type: '', config: '' })
  const [globalUpstreamChecking, setGlobalUpstreamChecking] = useState(false)
  const globalUpstreamEnabled = Form.useWatch('global_upstream_enabled', form)

  useEffect(() => {
    let cancelled = false

    const loadSettings = async () => {
      try {
        const response = await api.get('/settings')
        if (cancelled) return
        setSettingsData(response.data)
        form.setFieldsValue(response.data)
        setInitialPreserveInboundPorts(Boolean(response.data?.preserve_inbound_ports))
        setGlobalUpstream({
          type: response.data?.global_upstream_type || '',
          config: response.data?.global_upstream_config || '',
        })
      } catch (error) {
        if (cancelled) return
        message.error(t('settings_load_failed'))
      } finally {
        if (cancelled) return
        setLoadingData(false)
      }
    }

    loadSettings()

    return () => {
      cancelled = true
    }
  }, [form, t])

  const confirmDisablePreserveInboundPorts = () =>
    new Promise((resolve) => {
      Modal.confirm({
        title: t('warning'),
        content: t('preserve_inbound_ports_disable_warning'),
        okText: t('confirm'),
        cancelText: t('cancel'),
        onOk: (close) => {
          resolve(true)
          close()
        },
        onCancel: (close) => {
          resolve(false)
          close()
        },
      })
    })

  const handleSubmit = async (values) => {
    setLoading(true)
    try {
      const updateData = {}
      if (
        values.start_port !== undefined &&
        Number(values.start_port) !== Number(settingsData?.start_port)
      ) {
        updateData.start_port = values.start_port
      }
      if (
        values.preserve_inbound_ports !== undefined &&
        Boolean(values.preserve_inbound_ports) !== Boolean(settingsData?.preserve_inbound_ports)
      ) {
        updateData.preserve_inbound_ports = Boolean(values.preserve_inbound_ports)
      }
      if (values.admin_password) {
        updateData.admin_password = values.admin_password
      }
      if (
        values.global_upstream_enabled !== undefined &&
        Boolean(values.global_upstream_enabled) !== Boolean(settingsData?.global_upstream_enabled)
      ) {
        updateData.global_upstream_enabled = Boolean(values.global_upstream_enabled)
      }
      if (globalUpstream.type !== (settingsData?.global_upstream_type || '')) {
        updateData.global_upstream_type = globalUpstream.type
      }
      if (globalUpstream.config !== (settingsData?.global_upstream_config || '')) {
        updateData.global_upstream_config = globalUpstream.config
      }

      if (values.global_upstream_enabled && (!globalUpstream.type || !globalUpstream.config)) {
        message.error(t('upstream_global_required'))
        return
      }

      if (
        initialPreserveInboundPorts &&
        updateData.preserve_inbound_ports === false
      ) {
        const confirmed = await confirmDisablePreserveInboundPorts()
        if (!confirmed) {
          return
        }
      }

      if (Object.keys(updateData).length === 0) {
        message.info(t('settings_unchanged'))
        onClose()
        return
      }

      const response = await api.put('/settings', updateData)
      const passwordChanged = Boolean(response.data?.password_changed)
      if (updateData.preserve_inbound_ports !== undefined) {
        setInitialPreserveInboundPorts(Boolean(updateData.preserve_inbound_ports))
      }
      if (passwordChanged) {
        message.success(t('admin_password_changed_relogin'))
        onPasswordChanged?.()
        return
      }

      await onUpdated?.()
      message.success(t('settings_updated'))
      onClose()
    } catch (error) {
      message.error(error.response?.data?.error || t('settings_update_failed'))
    } finally {
      setLoading(false)
    }
  }

  if (loadingData) {
    return <div style={{ padding: 16, textAlign: 'center' }}>{t('loading')}</div>
  }

  const globalUpstreamConfigured = Boolean(globalUpstream.type && globalUpstream.config)
  const globalUpstreamMatchesSaved = Boolean(
    globalUpstreamConfigured &&
      globalUpstream.type === (settingsData?.global_upstream_type || '') &&
      globalUpstream.config === (settingsData?.global_upstream_config || '')
  )
  const hasGlobalUpstreamCheckResult = Boolean(
    settingsData?.global_upstream_ip ||
      settingsData?.global_upstream_location ||
      settingsData?.global_upstream_country_code ||
      settingsData?.global_upstream_latency ||
      settingsData?.global_upstream_error
  )
  const handleGlobalUpstreamSave = async (definition) => {
    setGlobalUpstream(definition)
    setUpstreamEditorOpen(false)
  }
  const handleGlobalUpstreamCheck = async () => {
    if (!globalUpstreamMatchesSaved) return

    setGlobalUpstreamChecking(true)
    try {
      const response = await api.post('/settings/upstream/check-ip')
      setSettingsData((previous) => ({
        ...previous,
        global_upstream_ip: response.data?.ip || '',
        global_upstream_location: response.data?.location || '',
        global_upstream_country_code: response.data?.country_code || '',
        global_upstream_latency: Number(response.data?.latency) || 0,
        global_upstream_error: '',
      }))
      message.success(t('upstream_check_success'))
    } catch (error) {
      const errorMessage = error.response?.data?.error || t('server_error')
      setSettingsData((previous) => ({
        ...previous,
        global_upstream_ip: '',
        global_upstream_location: '',
        global_upstream_country_code: '',
        global_upstream_latency: 0,
        global_upstream_error: errorMessage,
      }))
      message.error(errorMessage)
    } finally {
      setGlobalUpstreamChecking(false)
    }
  }

  return (
    <Form
      form={form}
      layout="vertical"
      onFinish={handleSubmit}
      data-testid="settings-form"
    >
      <Form.Item
        label={t('start_port')}
        name="start_port"
        rules={[
          { required: true, message: t('start_port_required') },
          { type: 'number', min: 1024, max: 65535, message: t('start_port_range') },
        ]}
        extra={t('start_port_desc')}
      >
        <InputNumber style={{ width: '100%' }} />
      </Form.Item>

      <Form.Item
        label={t('preserve_inbound_ports')}
        name="preserve_inbound_ports"
        valuePropName="checked"
        extra={t('preserve_inbound_ports_desc')}
      >
        <Switch checkedChildren={t('enabled')} unCheckedChildren={t('default')} />
      </Form.Item>

      <Divider />

      <Form.Item
        label={t('upstream_global')}
        name="global_upstream_enabled"
        valuePropName="checked"
        extra={t('upstream_global_desc')}
      >
        <Switch
          checkedChildren={t('enabled')}
          unCheckedChildren={t('disabled')}
          data-testid="global-upstream-enabled"
        />
      </Form.Item>

      <Form.Item
        label={t('upstream_global_proxy')}
        required={Boolean(globalUpstreamEnabled)}
        validateStatus={globalUpstreamEnabled && !globalUpstreamConfigured ? 'error' : undefined}
        help={globalUpstreamEnabled && !globalUpstreamConfigured ? t('upstream_global_required') : undefined}
      >
        <Space wrap>
          <Button
            icon={globalUpstreamConfigured ? <EditOutlined /> : <PlusOutlined />}
            onClick={() => setUpstreamEditorOpen(true)}
            data-testid="global-upstream-configure"
          >
            {globalUpstreamConfigured
              ? `${t('upstream_edit')} · ${globalUpstream.type}`
              : t('upstream_configure')}
          </Button>
          <Tooltip
            title={globalUpstreamMatchesSaved ? t('upstream_check_ip') : t('upstream_check_save_first')}
          >
            <span>
              <Button
                icon={<ThunderboltOutlined />}
                loading={globalUpstreamChecking}
                disabled={!globalUpstreamMatchesSaved}
                onClick={handleGlobalUpstreamCheck}
                data-testid="global-upstream-check-ip"
              >
                {t('check_ip')}
              </Button>
            </span>
          </Tooltip>
        </Space>
      </Form.Item>

      {globalUpstreamMatchesSaved && hasGlobalUpstreamCheckResult ? (
        <div data-testid="global-upstream-check-result" style={{ marginBottom: 16 }}>
          {settingsData?.global_upstream_error ? (
            <Alert
              type="error"
              showIcon
              message={t('upstream_check_error')}
              description={settingsData.global_upstream_error}
            />
          ) : (
            <Descriptions
              size="small"
              column={1}
              items={[
                {
                  key: 'ip',
                  label: t('upstream_ip'),
                  children: settingsData?.global_upstream_ip || '-',
                },
                {
                  key: 'location',
                  label: t('upstream_location'),
                  children: [
                    settingsData?.global_upstream_country_code,
                    settingsData?.global_upstream_location,
                  ]
                    .filter(Boolean)
                    .join(' ') || '-',
                },
                {
                  key: 'latency',
                  label: t('upstream_latency'),
                  children:
                    settingsData?.global_upstream_latency > 0
                      ? `${settingsData.global_upstream_latency}ms`
                      : '-',
                },
              ]}
            />
          )}
        </div>
      ) : null}

      <Modal
        title={t('upstream_global_proxy')}
        open={upstreamEditorOpen}
        footer={null}
        width={720}
        destroyOnHidden
        onCancel={() => setUpstreamEditorOpen(false)}
      >
        <UpstreamEditor
          value={globalUpstreamConfigured ? globalUpstream : null}
          formName="global_upstream"
          onSave={handleGlobalUpstreamSave}
          onCancel={() => setUpstreamEditorOpen(false)}
        />
      </Modal>

      <Divider />

      <Form.Item
        label={t('new_admin_password')}
        name="admin_password"
        extra={t('admin_password_leave_empty')}
        rules={[
          {
            validator: (_, value) => {
              if (!value) return Promise.resolve()
              if ([...value].length < 8) {
                return Promise.reject(new Error(t('password_min_8')))
              }
              if (new TextEncoder().encode(value).length > 72) {
                return Promise.reject(new Error(t('password_max_72_bytes')))
              }
              return Promise.resolve()
            },
          },
        ]}
      >
        <Input.Password placeholder={t('admin_password_placeholder_optional')} />
      </Form.Item>

      <Form.Item>
        <Button type="primary" htmlType="submit" loading={loading} block>
          {t('save_settings')}
        </Button>
      </Form.Item>
    </Form>
  )
}

export default SettingsForm
