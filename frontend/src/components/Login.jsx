import { useState } from 'react'
import { Form, Input, Button, Typography, message, Select } from 'antd'
import { GithubOutlined, LockOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'
import { OFFICIAL_GITHUB_URL } from '../constants/project'

const { Title } = Typography

function Login({ onLogin }) {
  const { t, i18n } = useTranslation()
  const [loading, setLoading] = useState(false)

  const handleLogin = async (values) => {
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
          <Form onFinish={handleLogin} size="large" layout="vertical">
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
        </div>
      </div>
    </div>
  )
}

export default Login
