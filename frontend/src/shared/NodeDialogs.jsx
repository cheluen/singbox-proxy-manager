// 节点相关的弹窗内容：新增 / 编辑、前置代理、替换、导出。
import { useState } from 'react'
import { Alert, Button, Checkbox, Input, Modal } from 'antd'
import { ClipboardPaste, Pencil, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { copyText, initialUpstreamMode, protocolLabel } from '../core/nodeView'
import NodeEditor from './NodeEditor'
import { upstreamModeLabel } from './labels'

// 粘贴分享链接后自动回填表单。回填即「用解析结果重新打开表单」，所以放在表单外层。
function LinkImportBar({ parse, onParsed, autoFocus }) {
  const { t } = useTranslation()
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = async () => {
    if (!link.trim()) {
      setError(t('enter_share_link'))
      return
    }
    setBusy(true)
    setError('')
    try {
      onParsed(await parse(link))
      setLink('')
    } catch (failure) {
      setError(failure.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="sb-linkbar">
      <label className="sb-linkbar__label" htmlFor="sb-link-input">{t('link_import_title')}</label>
      <div className="sb-linkbar__row">
        <Input
          id="sb-link-input"
          value={link}
          autoFocus={autoFocus}
          allowClear
          spellCheck={false}
          status={error ? 'error' : undefined}
          prefix={<ClipboardPaste size={15} strokeWidth={1.75} />}
          placeholder={t('link_import_placeholder')}
          data-testid="link-import-input"
          onChange={(event) => {
            setLink(event.target.value)
            setError('')
          }}
          onPressEnter={(event) => {
            event.preventDefault()
            run()
          }}
        />
        <Button onClick={run} loading={busy} data-testid="link-import-parse">{t('parse_link')}</Button>
      </div>
      {error ? <p className="sb-linkbar__error" role="alert">{error}</p> : <p className="sb-dim">{t('link_import_hint')}</p>}
    </div>
  )
}

export function NodeDialogBody({ node, focusLink, ctl, onDone }) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(node)
  const [revision, setRevision] = useState(0)
  const [fromLink, setFromLink] = useState(false)
  const editing = Boolean(node?.id)
  const targetGroup = editing ? ctl.groupById?.get(node.group_id) : ctl.activeGroup
  const allocatePort = targetGroup ? targetGroup.allocate_port !== false : true
  const preservePorts = Boolean(targetGroup && targetGroup.allocate_port !== false && targetGroup.preserve_inbound_ports)
  const save = async (values) => {
    const ok = await ctl.saveNode(values, editing ? node : null, { autoCheck: fromLink })
    if (ok) onDone()
  }
  return (
    <>
      {!editing && (
        <LinkImportBar
          parse={ctl.parseLink}
          autoFocus={Boolean(focusLink)}
          onParsed={(parsed) => {
            setDraft(parsed)
            setFromLink(true)
            setRevision((value) => value + 1)
          }}
        />
      )}
      {fromLink ? <Alert type="success" showIcon className="sb-gap" message={t('link_import_filled', { type: protocolLabel(draft?.type, t) })} /> : null}
      <NodeEditor
        key={`${node?.id || 'new'}:${revision}`}
        node={draft}
        preservePorts={preservePorts}
        allocatePort={allocatePort}
        allNodes={ctl.allNodes || ctl.nodes}
        groups={ctl.groups || []}
        tlsStatus={ctl.settings?.inbound_tls}
        onSave={save}
        onCancel={onDone}
        submitLabel={editing ? t('save') : t('create_node')}
      />
    </>
  )
}

// 前置代理定义编辑器：可粘贴连接串解析，也可手动填写。节点级与全局设置共用。
export function UpstreamDefinitionEditor({ value, parse, formName, onSave, onCancel }) {
  const { t } = useTranslation()
  const [definition, setDefinition] = useState(value)
  const [revision, setRevision] = useState(0)
  return (
    <div data-testid="upstream-definition-editor">
      <LinkImportBar
        parse={parse}
        onParsed={(parsed) => {
          setDefinition({ type: parsed.type, config: parsed.config })
          setRevision((current) => current + 1)
        }}
      />
      <NodeEditor
        key={`${revision}:${definition?.type || ''}`}
        variant="upstream"
        formName={formName}
        node={definition?.type && definition?.config ? definition : null}
        onSave={onSave}
        onCancel={onCancel}
        submitLabel={t('confirm')}
      />
    </div>
  )
}

export function UpstreamDialogBody({ node, ctl, onDone }) {
  const { t } = useTranslation()
  const initialMode = initialUpstreamMode(node)
  const [mode, setMode] = useState(initialMode)
  const [definition, setDefinition] = useState({ type: node.upstream_type || '', config: node.upstream_config || '' })
  const [editorOpen, setEditorOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [touched, setTouched] = useState(false)
  const configured = Boolean(definition.type && definition.config)
  const modes = ['none', 'group', 'global', 'custom', ...(initialMode === 'legacy' ? ['legacy'] : [])]
  const nodeGroup = ctl.groupById?.get(node.group_id) || ctl.activeGroup
  const groupUpstreamOn = Boolean(nodeGroup?.upstream_enabled)
  const globalUpstreamOn = Boolean(ctl.settings?.global_upstream_enabled)
  const groupOff = mode === 'group' && !groupUpstreamOn
  const globalOverriddenByGroup = mode === 'global' && groupUpstreamOn
  const globalOff = mode === 'global' && !groupUpstreamOn && !globalUpstreamOn

  const submit = async () => {
    setTouched(true)
    if (mode === 'custom' && !configured) return
    setSaving(true)
    const ok = await ctl.saveUpstream(node, { upstream_mode: mode, upstream_type: definition.type, upstream_config: definition.config })
    setSaving(false)
    if (ok) onDone()
  }

  return (
    <div data-testid="node-upstream-form">
      <p className="sb-dialog-lead">{t('upstream_mode_desc')}</p>
      <div className="sb-choice" role="radiogroup" aria-label={t('upstream_mode')} data-testid="node-upstream-mode">
        {modes.map((value) => (
          <button key={value} type="button" role="radio" data-mode={value} aria-checked={mode === value} className={`sb-choice__item${mode === value ? ' is-active' : ''}`} onClick={() => setMode(value)}>
            <strong>{upstreamModeLabel(value, t)}</strong>
            <span>{t(`upstream_mode_hint_${value}`)}</span>
          </button>
        ))}
      </div>

      {mode === 'legacy' ? <Alert type="warning" showIcon className="sb-gap" message={t('upstream_legacy_warning')} /> : null}
      {groupOff ? <Alert type="info" showIcon className="sb-gap" message={t('upstream_group_off_hint')} /> : null}
      {globalOverriddenByGroup ? (
        <Alert
          type="info"
          showIcon
          className="sb-gap"
          message={t('upstream_global_overridden_by_group_hint', { type: protocolLabel(nodeGroup?.upstream_type, t) })}
          data-testid="node-upstream-group-override-hint"
        />
      ) : null}
      {globalOff ? <Alert type="info" showIcon className="sb-gap" message={t('upstream_global_off_hint')} /> : null}

      {mode === 'custom' ? (
        <div className={`sb-inline-card sb-gap${touched && !configured ? ' is-error' : ''}`}>
          <div className="sb-inline-card__text">
            <strong>{t('upstream_custom_proxy')}</strong>
            <span>{configured ? `${protocolLabel(definition.type, t)} · ${t('configured')}` : t('upstream_custom_required')}</span>
          </div>
          <Button icon={configured ? <Pencil size={14} strokeWidth={1.75} /> : <Plus size={14} strokeWidth={1.75} />} onClick={() => setEditorOpen(true)} data-testid="node-upstream-configure">
            {configured ? t('upstream_edit') : t('upstream_configure')}
          </Button>
        </div>
      ) : null}

      <div className="sb-form-footer">
        <Button onClick={onDone} disabled={saving}>{t('cancel')}</Button>
        <Button type="primary" onClick={submit} loading={saving} data-testid="node-upstream-save">{t('save')}</Button>
      </div>

      <Modal title={t('upstream_custom_proxy')} open={editorOpen} footer={null} width={720} destroyOnHidden centered maskClosable={false} rootClassName="sb-overlay" onCancel={() => setEditorOpen(false)}>
        <UpstreamDefinitionEditor
          value={configured ? definition : null}
          parse={ctl.parseLink}
          formName="node_upstream"
          onCancel={() => setEditorOpen(false)}
          onSave={async (next) => {
            setDefinition(next)
            setEditorOpen(false)
          }}
        />
      </Modal>
    </div>
  )
}

export function ReplaceDialogBody({ node, ctl, onDone }) {
  const { t } = useTranslation()
  const [link, setLink] = useState('')
  const [updateName, setUpdateName] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const submit = async () => {
    if (!link.trim()) {
      setError(t('enter_share_link'))
      return
    }
    setSaving(true)
    const ok = await ctl.replaceNode(node, link, updateName)
    setSaving(false)
    if (ok) onDone()
  }
  return (
    <>
      <p className="sb-dialog-lead">{t('replace_desc')}</p>
      <Input.TextArea
        autoFocus
        value={link}
        autoSize={{ minRows: 3, maxRows: 6 }}
        spellCheck={false}
        status={error ? 'error' : undefined}
        placeholder={t('enter_share_link')}
        data-testid="replace-node-link-input"
        onChange={(event) => {
          setLink(event.target.value)
          setError('')
        }}
      />
      {error ? <p className="sb-linkbar__error" role="alert">{error}</p> : null}
      <Checkbox className="sb-gap" checked={updateName} onChange={(event) => setUpdateName(event.target.checked)}>{t('replace_update_name')}</Checkbox>
      <div className="sb-form-footer">
        <Button onClick={onDone} disabled={saving}>{t('cancel')}</Button>
        <Button type="primary" onClick={submit} loading={saving}>{t('replace')}</Button>
      </div>
    </>
  )
}

export function ExportDialogBody({ text, count, onDone }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const download = () => {
    const url = URL.createObjectURL(new Blob([`${text}\n`], { type: 'text/plain;charset=utf-8' }))
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = 'sbpm-nodes.txt'
    anchor.click()
    URL.revokeObjectURL(url)
  }
  return (
    <>
      <p className="sb-dialog-lead">{t('export_desc', { count })}</p>
      <Input.TextArea value={text} readOnly autoSize={{ minRows: 4, maxRows: 14 }} className="mono" spellCheck={false} onFocus={(event) => event.target.select()} />
      <div className="sb-form-footer">
        <Button onClick={onDone}>{t('close')}</Button>
        <Button onClick={download} disabled={!text}>{t('download_txt')}</Button>
        <Button type="primary" disabled={!text} onClick={async () => setCopied(await copyText(text))}>{copied ? t('copied') : t('copy')}</Button>
      </div>
    </>
  )
}
