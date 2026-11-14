import { useRef, useState } from 'react'
import { Button, Input } from 'antd'
import { ArrowRight, KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'

export function LoginForm({ session, size = 'large' }) {
  const { t } = useTranslation()
  const [secret, setSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const inputRef = useRef(null)

  const submit = async (event) => {
    event?.preventDefault()
    if (busy) return
    if (!secret) {
      setError(t('enter_password'))
      inputRef.current?.focus()
      return
    }
    setBusy(true)
    setError('')
    try {
      await session.login(secret)
    } catch (failure) {
      const status = failure?.response?.status
      setError(status === 401 ? t('login_wrong_secret') : failure?.response?.data?.error || t('login_failed'))
      setBusy(false)
      inputRef.current?.focus({ cursor: 'all' })
    }
  }

  return (
    <form className="sb-login-form" onSubmit={submit} noValidate data-testid="login-form">
      <label className="sb-login-form__label" htmlFor="sb-login-secret">{t('admin_password')}</label>
      <Input.Password
        id="sb-login-secret"
        ref={inputRef}
        size={size}
        value={secret}
        autoFocus
        autoComplete="current-password"
        status={error ? 'error' : undefined}
        prefix={<KeyRound size={16} strokeWidth={1.75} />}
        placeholder={t('enter_password')}
        aria-invalid={Boolean(error)}
        aria-describedby={error ? 'sb-login-error' : undefined}
        data-testid="login-secret"
        onChange={(event) => {
          setSecret(event.target.value)
          setError('')
        }}
      />
      <p id="sb-login-error" className="sb-login-form__error" role="alert" aria-live="polite">{error}</p>
      <Button type="primary" htmlType="submit" size={size} block loading={busy} icon={<ArrowRight size={16} strokeWidth={2} />} iconPosition="end" data-testid="login-submit">
        {t('login')}
      </Button>
    </form>
  )
}
