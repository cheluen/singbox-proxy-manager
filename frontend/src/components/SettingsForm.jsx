import React, { useState, useEffect } from 'react'
import { Form, Input, InputNumber, Button, message, Divider, Alert } from 'antd'
import api from '../utils/api'

function SettingsForm({ onClose }) {
  const [form] = Form.useForm()
  const [loading, setLoading] = useState(false)
  const [loadingData, setLoadingData] = useState(true)
  const [adminPasswordLocked, setAdminPasswordLocked] = useState(false)

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
        label="Start Port"
        name="start_port"
        rules={[
          { required: true, message: 'Please enter start port' },
          { type: 'number', min: 1024, max: 65535, message: 'Port must be between 1024 and 65535' },
        ]}
        extra="The starting port number for inbound connections. Each node will use sequential ports."
      >
        <InputNumber style={{ width: '100%' }} />
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
          label="New Admin Password"
          name="admin_password"
          extra="Leave empty to keep current password"
          rules={[{ min: 8, message: 'Password must be at least 8 characters' }]}
        >
          <Input.Password placeholder="Enter new password (optional)" />
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
