import { useMemo } from 'react'
import { App } from 'antd'
import {
  ArrowDown, ArrowDownToLine, ArrowLeftRight, ArrowUp, ArrowUpToLine, Copy, Download, Pencil, Route, Trash2, Zap,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { isNodeExportable } from '../core/nodeModel'
import { buildProxyUrl, copyText } from '../core/nodeView'

const icon = (Component) => <Component size={15} strokeWidth={1.75} aria-hidden="true" />

export function useNodeMenu(node, { act, canReorder }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  return useMemo(() => {
    const copy = async (text) => ((await copyText(text)) ? message.success(t('copied')) : message.error(t('copy_failed')))
    const handlers = {
      'copy-http': () => copy(buildProxyUrl(node, 'http')),
      'copy-socks': () => copy(buildProxyUrl(node, 'socks5')),
      check: () => act.checkNodes([node.id]),
      edit: () => act.open({ kind: 'node', node }),
      upstream: () => act.open({ kind: 'upstream', node }),
      replace: () => act.open({ kind: 'replace', node }),
      export: () => act.exportNode(node),
      top: () => act.moveNode(node.id, 'top'),
      up: () => act.moveNode(node.id, 'up'),
      down: () => act.moveNode(node.id, 'down'),
      bottom: () => act.moveNode(node.id, 'bottom'),
      delete: () => act.removeNode(node),
    }
    const hasPort = Boolean(node.inbound_port)
    return {
      items: [
        { key: 'copy-http', icon: icon(Copy), label: t('copy_proxy_http'), disabled: !hasPort },
        { key: 'copy-socks', icon: icon(Copy), label: t('copy_proxy_socks'), disabled: !hasPort },
        { type: 'divider' },
        { key: 'check', icon: icon(Zap), label: t('check_ip'), disabled: node.enabled === false },
        { key: 'edit', icon: icon(Pencil), label: t('edit') },
        { key: 'upstream', icon: icon(Route), label: t('upstream_mode') },
        { key: 'replace', icon: icon(ArrowLeftRight), label: t('replace_with_link') },
        { key: 'export', icon: icon(Download), label: t('export_share_link'), disabled: !isNodeExportable(node) },
        { type: 'divider' },
        { key: 'top', icon: icon(ArrowUpToLine), label: t('move_top'), disabled: !canReorder },
        { key: 'up', icon: icon(ArrowUp), label: t('move_up'), disabled: !canReorder },
        { key: 'down', icon: icon(ArrowDown), label: t('move_down'), disabled: !canReorder },
        { key: 'bottom', icon: icon(ArrowDownToLine), label: t('move_bottom'), disabled: !canReorder },
        { type: 'divider' },
        { key: 'delete', icon: icon(Trash2), label: t('delete'), danger: true },
      ],
      onClick: ({ key, domEvent }) => {
        domEvent?.stopPropagation()
        handlers[key]?.()
      },
    }
  }, [act, canReorder, message, node, t])
}
