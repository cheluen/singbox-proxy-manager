import { useMemo } from 'react'
import { App } from 'antd'
import {
  ArrowDown, ArrowDownToLine, ArrowLeftRight, ArrowUp, ArrowUpToLine, Copy, Download, FolderInput, Pencil, Route, Trash2, Zap,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { isNodeExportable, isPolicyNode } from '../core/nodeModel'
import { buildProxyUrl, copyText } from '../core/nodeView'
import { MenuIcon } from './icons'

export function useNodeMenu(node, { act, canReorder }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  return useMemo(() => {
    const copy = async (text) => ((await copyText(text)) ? message.success(t('copied')) : message.error(t('copy_failed')))
    const isHttps = node.inbound_mode === 'https'
    const handlers = {
      'copy-http': () => copy(buildProxyUrl(node, 'http')),
      'copy-https': () => copy(buildProxyUrl(node, 'https')),
      'copy-socks': () => copy(buildProxyUrl(node, 'socks5')),
      check: () => act.checkNodes([node.id]),
      edit: () => act.open({ kind: 'node', node }),
      upstream: () => act.open({ kind: 'upstream', node }),
      replace: () => act.open({ kind: 'replace', node }),
      'move-group': () => act.open({ kind: 'move-group', nodeIds: [node.id] }),
      export: () => act.exportNode(node),
      top: () => act.moveNode(node.id, 'top'),
      up: () => act.moveNode(node.id, 'up'),
      down: () => act.moveNode(node.id, 'down'),
      bottom: () => act.moveNode(node.id, 'bottom'),
      delete: () => act.removeNode(node),
    }
    const hasPort = Boolean(node.inbound_port)
    const policy = isPolicyNode(node)
    return {
      items: [
        ...(isHttps
          ? [{ key: 'copy-https', icon: <MenuIcon icon={Copy} />, label: t('copy_proxy_https'), disabled: !hasPort }]
          : [
              { key: 'copy-http', icon: <MenuIcon icon={Copy} />, label: t('copy_proxy_http'), disabled: !hasPort },
              { key: 'copy-socks', icon: <MenuIcon icon={Copy} />, label: t('copy_proxy_socks'), disabled: !hasPort },
            ]),
        { type: 'divider' },
        { key: 'check', icon: <MenuIcon icon={Zap} />, label: t('check_ip'), disabled: node.enabled === false },
        { key: 'edit', icon: <MenuIcon icon={Pencil} />, label: t('edit') },
        ...(!policy ? [
          { key: 'upstream', icon: <MenuIcon icon={Route} />, label: t('upstream_mode') },
          { key: 'replace', icon: <MenuIcon icon={ArrowLeftRight} />, label: t('replace_with_link') },
        ] : []),
        { key: 'move-group', icon: <MenuIcon icon={FolderInput} />, label: t('move_to_group') },
        { key: 'export', icon: <MenuIcon icon={Download} />, label: t('export_share_link'), disabled: !isNodeExportable(node) },
        { type: 'divider' },
        { key: 'top', icon: <MenuIcon icon={ArrowUpToLine} />, label: t('move_top'), disabled: !canReorder },
        { key: 'up', icon: <MenuIcon icon={ArrowUp} />, label: t('move_up'), disabled: !canReorder },
        { key: 'down', icon: <MenuIcon icon={ArrowDown} />, label: t('move_down'), disabled: !canReorder },
        { key: 'bottom', icon: <MenuIcon icon={ArrowDownToLine} />, label: t('move_bottom'), disabled: !canReorder },
        { type: 'divider' },
        { key: 'delete', icon: <MenuIcon icon={Trash2} />, label: t('delete'), danger: true },
      ],
      onClick: ({ key, domEvent }) => {
        domEvent?.stopPropagation()
        handlers[key]?.()
      },
    }
  }, [act, canReorder, message, node, t])
}
