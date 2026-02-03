import React, { useEffect, useState } from 'react'
import { Form, Input, Button, Typography, message, Select } from 'antd'
import { LockOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'

const { Title } = Typography

function Login({ onLogin }) {
  const { t, i18n } = useTranslation()
  const [loading, setLoading] = useState(false)
  const [loadingStatus, setLoadingStatus] = useState(true)
  const [setupRequired, setSetupRequired] = useState(false)
  const [adminPasswordLocked, setAdminPasswordLocked] = useState(false)

  useEffect(() => {
    let ignore = false
    async function fetchStatus() {
      try {
        const response = await api.get('/auth/status')
        if (ignore) return
        const { setup_required, admin_password_locked } = response.data || {}
        setSetupRequired(Boolean(setup_required))
        setAdminPasswordLocked(Boolean(admin_password_locked))
      } catch (error) {
        // Fallback to normal login form if status endpoint is unavailable.
      } finally {
        if (!ignore) setLoadingStatus(false)
      }
    }
    fetchStatus()
    return () => {
      ignore = true
    }
  }, [])

  const handleLogin = async (values) => {
    setLoading(true)
    try {
      const response = await api.post('/login', {
        password: values.password,
      })
      onLogin(response.data.token)
      message.success(t('login_success'))
    } catch (error) {
      const data = error.response?.data
      if (error.response?.status === 428 && data?.setup_required) {
        setSetupRequired(true)
        message.warning(t('setup_required'))
      } else {
        message.error(data?.error || t('login_failed'))
      }
    } finally {
      setLoading(false)
    }
  }

  const handleSetup = async (values) => {
    setLoading(true)
    try {
      const response = await api.post('/setup/admin-password', {
        password: values.password,
      })
      onLogin(response.data.token)
      message.success(t('setup_success'))
    } catch (error) {
      message.error(error.response?.data?.error || t('setup_failed'))
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
        {loadingStatus ? (
          <div style={{ textAlign: 'center', padding: 16 }}>{t('loading')}</div>
        ) : setupRequired && !adminPasswordLocked ? (
          <Form onFinish={handleSetup} size="large">
            <Form.Item>
              <div style={{ marginBottom: 12, color: '#666' }}>{t('setup_required')}</div>
            </Form.Item>
            <Form.Item
              name="password"
              rules={[
                { required: true, message: t('enter_password') },
                { min: 8, message: t('password_min_8') },
              ]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder={t('new_password')} />
            </Form.Item>
            <Form.Item
              name="confirm_password"
              dependencies={['password']}
              rules={[
                { required: true, message: t('enter_confirm_password') },
                ({ getFieldValue }) => ({
                  validator(_, value) {
                    if (!value || getFieldValue('password') === value) {
                      return Promise.resolve()
                    }
                    return Promise.reject(new Error(t('password_not_match')))
                  },
                }),
              ]}
            >
              <Input.Password prefix={<LockOutlined />} placeholder={t('confirm_password')} />
            </Form.Item>
            <Form.Item>
              <Button type="primary" htmlType="submit" loading={loading} block>
                {t('setup')}
              </Button>
            </Form.Item>
          </Form>
        ) : (
          <Form onFinish={handleLogin} size="large">
            {adminPasswordLocked ? (
              <Form.Item>
                <div style={{ marginBottom: 12, color: '#666' }}>{t('admin_password_locked_hint')}</div>
              </Form.Item>
            ) : null}
            <Form.Item name="password" rules={[{ required: true, message: t('enter_password') }]}>
              <Input.Password prefix={<LockOutlined />} placeholder={t('password')} />
            </Form.Item>
            <Form.Item>
              <Button type="primary" htmlType="submit" loading={loading} block>
                {t('login')}
              </Button>
            </Form.Item>
          </Form>
        )}
      </div>
    </div>
  )
}

export default Login
