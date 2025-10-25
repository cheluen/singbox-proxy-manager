import React, { useState } from 'react'
import { Form, Input, Button, Typography, message, Select } from 'antd'
import { LockOutlined, GlobalOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'

const { Title } = Typography

function Login({ onLogin }) {
  const { t, i18n } = useTranslation()
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (values) => {
    setLoading(true)
    try {
      const response = await api.post('/login', {
        password: values.password,
      })
      onLogin(response.data.token)
      message.success(t('login_success'))
    } catch (error) {
      message.error(error.response?.data?.error || t('login_failed'))
    } finally {
      setLoading(false)
    }
  }

  const handleLanguageChange = (lang) => {
    i18n.changeLanguage(lang)
    localStorage.setItem('language', lang)
  }

  return (
    <div className="login-container">
      <div className="login-box">
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
          <Select
            value={i18n.language}
            onChange={handleLanguageChange}
            style={{ width: 120 }}
            options={[
              { value: 'zh', label: '中文' },
              { value: 'en', label: 'English' },
            ]}
          />
        </div>
        <Title level={2} style={{ textAlign: 'center', marginBottom: 30 }}>
          {t('app_title')}
        </Title>
        <Form onFinish={handleSubmit} size="large">
          <Form.Item
            name="password"
            rules={[{ required: true, message: t('enter_password') }]}
          >
            <Input.Password
              prefix={<LockOutlined />}
              placeholder={t('password')}
            />
          </Form.Item>
          <Form.Item>
            <Button
              type="primary"
              htmlType="submit"
              loading={loading}
              block
            >
              {t('login')}
            </Button>
          </Form.Item>
        </Form>
      </div>
    </div>
  )
}

export default Login
