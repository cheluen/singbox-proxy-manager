import React, { useState } from 'react'
import { Alert, Button, Form, Modal, Segmented, Space, message } from 'antd'
import { EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import UpstreamEditor from './UpstreamEditor'

const initialModeForNode = (node) => {
  if (node?.upstream_mode) return node.upstream_mode
  try {
    const config = JSON.parse(node?.config || '{}')
    return config.detour ? 'legacy' : 'none'
  } catch {
    return 'none'
  }
}

function NodeUpstreamForm({ node, onSave, onCancel }) {
  const { t } = useTranslation()
  const initialMode = initialModeForNode(node)
  const [mode, setMode] = useState(initialMode)
  const [definition, setDefinition] = useState(() => ({
    type: node?.upstream_type || '',
    config: node?.upstream_config || '',
  }))
  const [editorOpen, setEditorOpen] = useState(false)
  const [saving, setSaving] = useState(false)

  const options = [
    { label: t('upstream_direct'), value: 'none' },
    { label: t('upstream_follow_global'), value: 'global' },
    { label: t('upstream_custom'), value: 'custom' },
  ]
  if (initialMode === 'legacy') {
    options.push({ label: t('upstream_legacy'), value: 'legacy' })
  }

  const configured = Boolean(definition.type && definition.config)

  const handleDefinitionSave = async (nextDefinition) => {
    setDefinition(nextDefinition)
    setEditorOpen(false)
  }

  const handleSubmit = async () => {
    if (mode === 'custom' && !configured) {
      message.error(t('upstream_custom_required'))
      return
    }
    setSaving(true)
    try {
      await onSave({
        upstream_mode: mode,
        upstream_type: definition.type,
        upstream_config: definition.config,
      })
    } catch (error) {
      message.error(error.response?.data?.error || error.message || t('server_error'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Form layout="vertical" onFinish={handleSubmit} data-testid="node-upstream-form">
      <Form.Item label={t('upstream_mode')} extra={t('upstream_mode_desc')}>
        <Segmented
          block
          value={mode}
          options={options}
          onChange={setMode}
          data-testid="node-upstream-mode"
        />
      </Form.Item>

      {mode === 'legacy' ? (
        <Alert
          type="warning"
          showIcon
          message={t('upstream_legacy_warning')}
          style={{ marginBottom: 16 }}
        />
      ) : null}

      {mode === 'custom' ? (
        <Form.Item
          label={t('upstream_custom_proxy')}
          required
          validateStatus={configured ? undefined : 'error'}
          help={configured ? undefined : t('upstream_custom_required')}
        >
          <Button
            icon={configured ? <EditOutlined /> : <PlusOutlined />}
            onClick={() => setEditorOpen(true)}
            data-testid="node-upstream-configure"
          >
            {configured
              ? `${t('upstream_edit')} · ${definition.type.toUpperCase()}`
              : t('upstream_configure')}
          </Button>
        </Form.Item>
      ) : null}

      <Space>
        <Button type="primary" htmlType="submit" loading={saving}>
          {t('save')}
        </Button>
        <Button onClick={onCancel}>{t('cancel')}</Button>
      </Space>

      <Modal
        title={t('upstream_custom_proxy')}
        open={editorOpen}
        footer={null}
        width={760}
        destroyOnHidden
        onCancel={() => setEditorOpen(false)}
      >
        <UpstreamEditor
          value={configured ? definition : null}
          formName="node_upstream"
          onSave={handleDefinitionSave}
          onCancel={() => setEditorOpen(false)}
        />
      </Modal>
    </Form>
  )
}

export default NodeUpstreamForm
