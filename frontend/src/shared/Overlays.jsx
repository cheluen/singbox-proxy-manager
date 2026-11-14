import { useEffect, useState } from 'react'
import { Drawer, Modal } from 'antd'
import { useTranslation } from 'react-i18next'
import { BatchAuthDialogBody, ImportDialogBody } from './BatchDialogs'
import { ExportDialogBody, NodeDialogBody, ReplaceDialogBody, UpstreamDialogBody } from './NodeDialogs'
import { SettingsDialogBody } from './SettingsDialog'

const WIDTH = { node: 720, upstream: 620, replace: 560, export: 640, import: 680, auth: 520, settings: 640 }

export function Overlays({ ctl, presentation = 'modal' }) {
  const { t } = useTranslation()
  const [locked, setLocked] = useState(false)
  const overlay = ctl.overlay
  const [activeView, setActiveView] = useState({ overlay, presentation })
  // 打开期间不更换容器类型，旋转屏幕或调整窗口不会丢失表单与导入结果。
  if (activeView.overlay !== overlay) {
    setActiveView({ overlay, presentation })
    if (locked) setLocked(false)
  }
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
  else if (kind === 'import') body = <ImportDialogBody ctl={ctl} onDone={onDone} onBusyChange={setLocked} />
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

  if ((activeView.overlay === overlay ? activeView.presentation : presentation) === 'drawer') {
    return (
      <Drawer title={title} open={open} onClose={close} width={Math.min(WIDTH[kind] || 640, 720)} destroyOnHidden maskClosable={!locked} keyboard={!locked} closable={!locked} rootClassName="sb-overlay">
        {body}
      </Drawer>
    )
  }
  return (
    <Modal title={title} open={open} onCancel={close} footer={null} width={WIDTH[kind] || 640} destroyOnHidden centered maskClosable={kind === 'export'} keyboard={!locked} closable={!locked} rootClassName="sb-overlay">
      {body}
    </Modal>
  )
}
