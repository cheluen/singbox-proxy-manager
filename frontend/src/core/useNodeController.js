// 面板控制器：把数据层（useNodeManager）与操作层（useNodeActions）合并成一个对象交给界面使用。
import { useNodeActions } from './useNodeActions'
import { useNodeManager } from './useNodeManager'

export function useNodeController(options) {
  const manager = useNodeManager(options)
  const actions = useNodeActions(manager)
  return {
    ...manager,
    ...actions,
    open: manager.setOverlay,
    close: () => manager.setOverlay(null),
  }
}
