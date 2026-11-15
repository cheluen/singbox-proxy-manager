import { useEffect, useState } from 'react'
import { Modal } from 'antd'
import { useTranslation } from 'react-i18next'
import { BatchAuthDialogBody, ImportDialogBody } from './BatchDialogs'
import { ExportDialogBody, NodeDialogBody, ReplaceDialogBody, UpstreamDialogBody } from './NodeDialogs'
import { SettingsDialogBody } from './SettingsDialog'

const WIDTH = { node: 720, upstream: 620, replace: 560, export: 640, import: 680, auth: 520, settings: 640 }

export function Overlays({ ctl }) {
  const { t } = useTranslation()
  const [busyOverlay, setBusyOverlay] = useState(null)
  const overlay = ctl.overlay
  const locked = Boolean(overlay) && busyOverlay === overlay
  // 忙碌状态绑定弹窗实例，旧导入结束不会解除新弹窗的关闭保护。
  const onBusyChange = (busy) => setBusyOverlay((current) => busy ? overlay : current === overlay ? null : current)
  const kind = overlay?.kind
  const onDone = () => ctl.setOverlay((current) => current === overlay ? null : current)
  const close = () => {
    if (!locked) ctl.close()
  }
  // 被编辑的节点始终取最新数据；节点在别处被删除时自动关闭弹窗。
  const node = overlay?.node?.id ? ctl.nodes.find((item) => item.id === overlay.node.id) : overlay?.node
  const orphaned = Boolean(overlay?.node?.id) && !node
  const closeOverlay = ctl.close
  useEffect(() => {
    if (orphaned) closeOverlay()
  }, [orphaned, closeOverlay])

  const titles = {
    node: overlay?.node?.id ? t('edit_node') : t('add_node'),
    upstream: t('upstream_mode'),
    replace: t('replace_with_link'),
    export: t('export_share_link'),
    import: t('batch_import_title'),
    auth: t('batch_auth_title'),
    settings: t('settings'),
  }

  let body = null
  if (orphaned) body = null
  else if (kind === 'node') body = <NodeDialogBody node={node || null} focusLink={overlay.focusLink} ctl={ctl} onDone={onDone} />
  else if (kind === 'upstream' && node) body = <UpstreamDialogBody key={node.id} node={node} ctl={ctl} onDone={onDone} />
  else if (kind === 'replace' && node) body = <ReplaceDialogBody node={node} ctl={ctl} onDone={onDone} />
  else if (kind === 'export') body = <ExportDialogBody text={overlay.text} count={overlay.count} onDone={onDone} />
  else if (kind === 'import') body = <ImportDialogBody ctl={ctl} onDone={onDone} onBusyChange={onBusyChange} />
  else if (kind === 'auth') body = <BatchAuthDialogBody ctl={ctl} count={ctl.selectedIds.size} onDone={onDone} />
  else if (kind === 'settings') body = <SettingsDialogBody ctl={ctl} onDone={onDone} />

  const open = Boolean(body)
  const subtitle = node?.name && kind !== 'node' ? node.name : null
  const title = (
    <span className="sb-dialog-title">
      {titles[kind]}
      {subtitle ? <span className="sb-dialog-title__sub truncate">{subtitle}</span> : null}
    </span>
  )

  return (
    <Modal title={title} open={open} onCancel={close} footer={null} width={WIDTH[kind] || 640} destroyOnHidden centered maskClosable={kind === 'export'} keyboard={!locked} closable={!locked} rootClassName="sb-overlay">
      {body}
    </Modal>
  )
}
