import { memo, useEffect, useState } from 'react'
import { Checkbox, Input, Modal, Space } from 'antd'
import { useTranslation } from 'react-i18next'

function ReplaceNodeModal({ open, loading, nodeId, onCancel, onConfirm }) {
  const { t } = useTranslation()
  const [link, setLink] = useState('')
  const [updateName, setUpdateName] = useState(true)

  useEffect(() => {
    if (!open) return
    setLink('')
    setUpdateName(true)
  }, [open, nodeId])

  const handleConfirm = () => {
    onConfirm?.(link, updateName)
  }

  return (
    <Modal
      title={t('replace')}
      open={open}
      onCancel={onCancel}
      onOk={handleConfirm}
      okText={t('confirm')}
      cancelText={t('cancel')}
      confirmLoading={loading}
      width={700}
      destroyOnHidden
    >
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <div>{t('replace_desc')}</div>
        <Input
          data-testid="replace-node-link-input"
          placeholder={t('enter_share_link')}
          value={link}
          onChange={(event) => setLink(event.target.value)}
          allowClear
        />
        <Checkbox
          checked={updateName}
          onChange={(event) => setUpdateName(event.target.checked)}
        >
          {t('replace_update_name')}
        </Checkbox>
      </Space>
    </Modal>
  )
}

export default memo(ReplaceNodeModal)
