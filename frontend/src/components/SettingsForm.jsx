import React, { useState, useEffect } from 'react'
import { Form, Input, InputNumber, Button, message, Divider } from 'antd'
import api from '../utils/api'

function SettingsForm({ onClose }) {
  const [form] = Form.useForm()
  const [loading, setLoading] = useState(false)
  const [loadingData, setLoadingData] = useState(true)

  useEffect(() => {
    loadSettings()
  }, [])

  const loadSettings = async () => {
    try {
      const response = await api.get('/settings')
      form.setFieldsValue(response.data)
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
      if (values.admin_password) {
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

      <Form.Item
        label="New Admin Password"
        name="admin_password"
        extra="Leave empty to keep current password"
      >
        <Input.Password placeholder="Enter new password (optional)" />
      </Form.Item>

      <Form.Item>
        <Button type="primary" htmlType="submit" loading={loading} block>
          Save Settings
        </Button>
      </Form.Item>
    </Form>
  )
}

export default SettingsForm
