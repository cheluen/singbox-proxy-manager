import React from 'react'
import { Modal, Form, Input } from 'antd'
import { useTranslation } from 'react-i18next'

function BatchAuthModal({ visible, selectedNodes, onClose, onSave }) {
  const { t } = useTranslation()
  const [form] = Form.useForm()

  const handleOk = () => {
    form.validateFields().then((values) => {
      onSave(values)
      form.resetFields()
    })
  }

  const handleCancel = () => {
    form.resetFields()
    onClose()
  }

  return (
    <Modal
      title={t('batch_auth_title')}
      open={visible}
      onOk={handleOk}
      onCancel={handleCancel}
      okText={t('apply_auth')}
      cancelText={t('cancel')}
    >
      <div style={{ marginBottom: 16 }}>
        {t('batch_auth_desc')}
      </div>
      <Form form={form} layout="vertical">
        <Form.Item
          label={t('username')}
          name="username"
          rules={[{ required: true, message: t('enter_username') }]}
        >
          <Input placeholder={t('enter_username')} />
        </Form.Item>
        <Form.Item
          label={t('password')}
          name="password"
          rules={[{ required: true, message: t('enter_password') }]}
        >
          <Input.Password placeholder={t('enter_password')} />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export default BatchAuthModal
