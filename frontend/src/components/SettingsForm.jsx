import React, { useState, useEffect } from 'react'
import { Form, Input, InputNumber, Button, message, Divider, Alert, Switch } from 'antd'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'

function SettingsForm({ onClose }) {
  const { i18n } = useTranslation()
  const [form] = Form.useForm()
  const [loading, setLoading] = useState(false)
  const [loadingData, setLoadingData] = useState(true)
  const [adminPasswordLocked, setAdminPasswordLocked] = useState(false)
  const isChineseMode = i18n.language?.startsWith('zh')

  const withHint = (label, zhHint) => {
    if (!isChineseMode || !zhHint) {
      return label
    }
    return `${label}（${zhHint}）`
  }

  const withExtraHint = (message, zhHint) => {
    if (!isChineseMode || !zhHint) {
      return message
    }
    return `${message}（${zhHint}）`
  }

  useEffect(() => {
    loadSettings()
  }, [])

  const loadSettings = async () => {
    try {
      const response = await api.get('/settings')
      form.setFieldsValue(response.data)
      setAdminPasswordLocked(Boolean(response.data?.admin_password_locked))
    } catch (error) {
      message.error('Failed to load settings')
    } finally {
      setLoadingData(false)
    }
  }

  const handleSubmit = async (values) => {
    setLoading(true)
    try {
      const updateData = {}
      if (values.start_port !== undefined) {
        updateData.start_port = values.start_port
      }
      if (values.preserve_inbound_ports !== undefined) {
        updateData.preserve_inbound_ports = Boolean(values.preserve_inbound_ports)
      }
      if (!adminPasswordLocked && values.admin_password) {
        updateData.admin_password = values.admin_password
      }

      await api.put('/settings', updateData)
      message.success('Settings updated successfully')
      onClose()
    } catch (error) {
      message.error('Failed to update settings')
    } finally {
      setLoading(false)
    }
  }

  if (loadingData) {
    return <div style={{ padding: 16, textAlign: 'center' }}>Loading...</div>
  }

  return (
    <Form
      form={form}
      layout="vertical"
      onFinish={handleSubmit}
    >
      <Form.Item
        label={withHint('Start Port', '节点起始端口')}
        name="start_port"
        rules={[
          { required: true, message: 'Please enter start port' },
          { type: 'number', min: 1024, max: 65535, message: 'Port must be between 1024 and 65535' },
        ]}
        extra={withExtraHint('The starting port number for inbound connections. Auto-assigned ports will skip occupied ports.', '自动分配时会从该端口开始，并自动跳过已占用端口')}
      >
        <InputNumber style={{ width: '100%' }} />
      </Form.Item>

      <Form.Item
        label={withHint('Preserve Inbound Ports', '保留入站端口')}
        name="preserve_inbound_ports"
        valuePropName="checked"
        extra={withExtraHint('When enabled, drag sorting, delete reindexing, and start-port changes will not rewrite existing node ports. Only manual edits change a node port.', '开启后，拖拽排序、删除补位、修改起始端口都不会改写现有节点端口，只有手动编辑节点端口时才会变更')}
      >
        <Switch checkedChildren={withHint('Enabled', '开启')} unCheckedChildren={withHint('Default', '默认')} />
      </Form.Item>

      <Divider />

      {adminPasswordLocked ? (
        <Alert
          type="info"
          showIcon
          message="管理员密码由 ADMIN_PASSWORD 环境变量管理"
          description="当前部署已设置 ADMIN_PASSWORD，面板内无法修改管理员密码。如需修改，请更新部署环境变量后重启服务。"
          style={{ marginBottom: 16 }}
        />
      ) : (
        <Form.Item
          label={withHint('New Admin Password', '新的管理员密码')}
          name="admin_password"
          extra={withExtraHint('Leave empty to keep current password', '留空表示保持当前密码')}
          rules={[{ min: 8, message: 'Password must be at least 8 characters' }]}
        >
          <Input.Password placeholder={withHint('Enter new password (optional)', '输入新密码（可选）')} />
        </Form.Item>
      )}

      <Form.Item>
        <Button type="primary" htmlType="submit" loading={loading} block>
          Save Settings
        </Button>
      </Form.Item>
    </Form>
  )
}

export default SettingsForm
