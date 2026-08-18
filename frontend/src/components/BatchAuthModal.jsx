import { useEffect, useState } from 'react'
import { Modal, Form, Input, Switch } from 'antd'
import { useTranslation } from 'react-i18next'

function BatchAuthModal({ visible, onClose, onSave }) {
  const { t } = useTranslation()
  const [form] = Form.useForm()
  const [submitting, setSubmitting] = useState(false)
  const authEnabled = Form.useWatch('auth_enabled', form)

  useEffect(() => {
    if (visible) {
      form.setFieldsValue({ auth_enabled: true, username: '', password: '' })
    }
  }, [form, visible])

  const handleOk = async () => {
    if (submitting) return

    let values
    try {
      values = await form.validateFields()
    } catch {
      return
    }

    setSubmitting(true)
    try {
      const saved = await onSave({
        auth_enabled: Boolean(values.auth_enabled),
        username: values.auth_enabled ? values.username || '' : '',
        password: values.auth_enabled ? values.password || '' : '',
      })
      if (saved !== false) {
        form.resetFields()
      }
    } finally {
      setSubmitting(false)
    }
  }

  const handleCancel = () => {
    if (submitting) return
    form.resetFields()
    onClose()
  }

  return (
    <Modal
      title={t('batch_auth_title')}
      open={visible}
      onOk={handleOk}
      onCancel={handleCancel}
      confirmLoading={submitting}
      cancelButtonProps={{ disabled: submitting }}
      closable={!submitting}
      keyboard={!submitting}
      maskClosable={!submitting}
      okText={t('apply_auth')}
      cancelText={t('cancel')}
    >
      <div style={{ marginBottom: 16 }}>
        {t('batch_auth_desc')}
      </div>
      <Form form={form} layout="vertical" initialValues={{ auth_enabled: true }}>
        <Form.Item
          label={t('inbound_authentication')}
          name="auth_enabled"
          valuePropName="checked"
          extra={t('batch_auth_mode_desc')}
        >
          <Switch
            checkedChildren={t('enabled')}
            unCheckedChildren={t('disabled')}
            onChange={(enabled) => {
              if (!enabled) {
                form.setFieldsValue({ username: '', password: '' })
              }
            }}
          />
        </Form.Item>
        <Form.Item
          label={t('username')}
          name="username"
          hidden={!authEnabled}
          dependencies={['auth_enabled']}
          rules={[
            {
              validator: (_, value) => {
                if (!form.getFieldValue('auth_enabled')) {
                  return Promise.resolve()
                }
                if (!value) {
                  return Promise.reject(new Error(t('enter_username')))
                }
                if (!String(value).includes('+')) {
                  return Promise.resolve()
                }
                return Promise.reject(new Error(t('username_plus_not_allowed')))
              },
            },
          ]}
        >
          <Input data-testid="batch-auth-username" placeholder={t('enter_username')} />
        </Form.Item>
        <Form.Item
          label={t('password')}
          name="password"
          hidden={!authEnabled}
          dependencies={['auth_enabled']}
          rules={[
            {
              validator: (_, value) => {
                if (!form.getFieldValue('auth_enabled') || value) {
                  return Promise.resolve()
                }
                return Promise.reject(new Error(t('enter_password')))
              },
            },
          ]}
        >
          <Input.Password data-testid="batch-auth-password" placeholder={t('enter_password')} />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export default BatchAuthModal
