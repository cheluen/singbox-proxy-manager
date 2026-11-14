import { Button, Dropdown } from 'antd'
import { ChevronDown, Ellipsis, FileStack, Link2, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { IconButton } from './primitives'
import { useNodeMenu } from './useNodeMenu'

const icon = (Component) => <Component size={15} strokeWidth={1.75} aria-hidden="true" />

export function NodeMenuButton({ node, act, canReorder, size = 'md' }) {
  const { t } = useTranslation()
  const menu = useNodeMenu(node, { act, canReorder })
  return (
    <Dropdown menu={menu} trigger={['click']} placement="bottomRight" destroyOnHidden>
      <IconButton label={t('more_actions')} icon={Ellipsis} size={size} tip={false} onClick={(event) => event.stopPropagation()} data-testid={`node-menu-${node.id}`} />
    </Dropdown>
  )
}

// 「添加节点」主按钮 + 下拉：手动添加 / 粘贴分享链接 / 批量导入。
export function AddMenu({ act, size = 'middle', compact = false }) {
  const { t } = useTranslation()
  const items = [
    { key: 'manual', icon: icon(Plus), label: t('add_manual') },
    { key: 'link', icon: icon(Link2), label: t('add_from_link') },
    { key: 'batch', icon: icon(FileStack), label: t('batch_import') },
  ]
  const onClick = ({ key }) => {
    if (key === 'batch') act.open({ kind: 'import' })
    else act.open({ kind: 'node', node: null, focusLink: key === 'link' })
  }
  return (
    <span className="sb-split-btn">
      <Button type="primary" size={size} icon={icon(Plus)} aria-label={t('add_node')} onClick={() => act.open({ kind: 'node', node: null })} data-testid="nodes-add-node">
        {compact ? null : t('add_node')}
      </Button>
      <Dropdown menu={{ items, onClick }} trigger={['click']} placement="bottomRight">
        <Button type="primary" size={size} icon={icon(ChevronDown)} aria-label={t('more_add_options')} data-testid="nodes-add-menu" />
      </Dropdown>
    </span>
  )
}
