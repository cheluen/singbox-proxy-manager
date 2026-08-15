import React, { useState } from 'react'
import { Button, Divider, Input, Space, message } from 'antd'
import { ImportOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'
import NodeForm from './NodeForm'

const { TextArea } = Input

function UpstreamEditor({ value, onSave, onCancel, formName = 'upstream' }) {
  const { t } = useTranslation()
  const [definition, setDefinition] = useState(() => ({
    type: value?.type || '',
    config: value?.config || '',
  }))
  const [link, setLink] = useState('')
  const [parsing, setParsing] = useState(false)
  const [revision, setRevision] = useState(0)

  const handleParseLink = async () => {
    const normalizedLink = link.trim()
    if (!normalizedLink) {
      message.warning(t('enter_share_link'))
      return
    }

    setParsing(true)
    try {
      const response = await api.post('/parse-link', { link: normalizedLink })
      const parsed = {
        type: response.data?.type || '',
        config: response.data?.config || '',
      }
      if (!parsed.type || !parsed.config) {
        throw new Error(t('invalid_config'))
      }
      setDefinition(parsed)
      setRevision((current) => current + 1)
      message.success(t('upstream_link_parsed'))
    } catch (error) {
      message.error(error.response?.data?.error || error.message || t('invalid_config'))
    } finally {
      setParsing(false)
    }
  }

  const configured = Boolean(definition.type && definition.config)

  return (
    <div data-testid="upstream-definition-editor">
      <Space.Compact block>
        <TextArea
          value={link}
          onChange={(event) => setLink(event.target.value)}
          placeholder={t('upstream_link_placeholder')}
          autoSize={{ minRows: 2, maxRows: 4 }}
          data-testid="upstream-link-input"
        />
        <Button
          type="primary"
          icon={<ImportOutlined />}
          loading={parsing}
          onClick={handleParseLink}
          data-testid="upstream-link-parse"
          style={{ height: 'auto' }}
        >
          {t('parse_link')}
        </Button>
      </Space.Compact>

      <Divider />

      <NodeForm
        key={`${revision}:${definition.type}:${definition.config}`}
        variant="upstream"
        formName={formName}
        node={configured ? definition : null}
        onSave={onSave}
        onCancel={onCancel}
      />
    </div>
  )
}

export default UpstreamEditor
