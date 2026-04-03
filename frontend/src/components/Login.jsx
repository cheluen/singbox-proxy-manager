import React, { useEffect, useState } from 'react'
import { Form, Input, Button, Typography, message, Select } from 'antd'
import { GithubOutlined, LockOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'
import { OFFICIAL_GITHUB_URL } from '../constants/project'

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
    <div className="login-shell">
      <div className="login-aura login-aura-left" />
      <div className="login-aura login-aura-right" />
      <div className="login-card">
        <div className="login-brand">
          <img src="/logo.svg" alt="SingBox Proxy Manager" className="login-brand-logo" />
          <Title level={2} className="login-brand-title">
            {t('app_title')}
          </Title>
          <p className="login-brand-subtitle">{t('login_subtitle')}</p>
          <div className="login-brand-tagline">
            <span>{t('nodes')}</span>
            <span>•</span>
            <span>{t('settings')}</span>
            <span>•</span>
            <span>{t('batch_check_ip')}</span>
          </div>
          <div className="login-brand-links">
            <Typography.Link
              href={OFFICIAL_GITHUB_URL}
              target="_blank"
              rel="noreferrer"
              className="login-brand-link"
            >
              <GithubOutlined />
              <span>{t('official_repository')}</span>
            </Typography.Link>
          </div>
        </div>
        <div className="login-panel">
          <div className="login-toolbar">
            <span className="login-panel-title">{t('login_title')}</span>
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
          {loadingStatus ? (
            <div className="login-loading">{t('loading')}</div>
          ) : setupRequired && !adminPasswordLocked ? (
            <Form onFinish={handleSetup} size="large" layout="vertical">
              <div className="login-hint">{t('setup_required')}</div>
              <Form.Item
                name="password"
                label={t('new_password')}
                rules={[
                  { required: true, message: t('enter_password') },
                  { min: 8, message: t('password_min_8') },
                ]}
              >
                <Input.Password prefix={<LockOutlined />} placeholder={t('new_password')} />
              </Form.Item>
              <Form.Item
                name="confirm_password"
                label={t('confirm_password')}
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
              <Form.Item style={{ marginBottom: 0 }}>
                <Button type="primary" htmlType="submit" loading={loading} block>
                  {t('setup')}
                </Button>
              </Form.Item>
            </Form>
          ) : (
            <Form onFinish={handleLogin} size="large" layout="vertical">
              {adminPasswordLocked ? (
                <div className="login-hint">{t('admin_password_locked_hint')}</div>
              ) : null}
              <Form.Item
                name="password"
                label={t('password')}
                rules={[{ required: true, message: t('enter_password') }]}
              >
                <Input.Password prefix={<LockOutlined />} placeholder={t('password')} />
              </Form.Item>
              <Form.Item style={{ marginBottom: 0 }}>
                <Button type="primary" htmlType="submit" loading={loading} block>
                  {t('login')}
                </Button>
              </Form.Item>
            </Form>
          )}
        </div>
      </div>
    </div>
  )
}

export default Login
