// 节点的批量操作、排序、导入导出与系统级操作。接收 useNodeManager 的结果并在其上扩展。
import { useCallback, useEffect, useRef } from 'react'
import api from './api'

const BATCH_IMPORT_TIMEOUT_MS = 120000

export function useNodeActions(manager) {
  const {
    helpers, allNodesRef, nodesRef, visibleRef, activeGroupIdRef, loadGroups, loadNodes, loadSettings, setNodes, selectedNodesRef, clearSelection,
    checks, checkNodes, filtersActive, preservePorts, activeGroup, setWorking, setRuntime, setRuntimeRestarting, setOverlay,
  } = manager
  const { t, notify, confirm, onPasswordChanged, errorText, outboundSignature, isNodeExportable } = helpers
  const importBusyRef = useRef(false)
  const importRequestRef = useRef(null)
  const batchEnabledBusyRef = useRef(false)
  const mountedRef = useRef(false)
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      importRequestRef.current?.abort()
    }
  }, [])

  const track = useCallback(async (key, task) => {
    setWorking((prev) => ({ ...prev, [key]: true }))
    try {
      return await task()
    } finally {
      setWorking((prev) => ({ ...prev, [key]: false }))
    }
  }, [setWorking])

  /* ---------- 排序 ---------- */

  const commitOrder = useCallback(async (ordered) => {
    setNodes(ordered)
    const groupId = activeGroupIdRef.current
    const payload = {
      nodes: ordered.map((node, index) => ({ id: node.id, sort_order: index })),
      ...(groupId != null ? { group_id: groupId } : {}),
    }
    try {
      await api.post('/nodes/reorder', payload)
      notify.success(t('nodes_reordered'))
    } catch {
      notify.error(t('server_error'))
    }
    await loadNodes({ silent: true })
  }, [activeGroupIdRef, loadNodes, notify, setNodes, t])

  // 把 activeId 移到 overId 所在位置。筛选状态下只调整可见子集在当前分组列表中的相对顺序。
  const reorder = useCallback(async (activeId, overId) => {
    const visible = visibleRef.current
    const from = visible.findIndex((node) => node.id === activeId)
    const to = visible.findIndex((node) => node.id === overId)
    if (from < 0 || to < 0 || from === to) return
    if (filtersActive && !preservePorts && activeGroup?.allocate_port !== false) {
      notify.warning(t('drag_sort_disabled_filtered'))
      return
    }
    const moved = [...visible]
    const [item] = moved.splice(from, 1)
    moved.splice(to, 0, item)
    const subset = new Set(visible.map((node) => node.id))
    let cursor = 0
    const merged = nodesRef.current.map((node) => {
      if (!subset.has(node.id)) return node
      cursor += 1
      return moved[cursor - 1]
    })
    await commitOrder(merged)
  }, [activeGroup?.allocate_port, commitOrder, filtersActive, nodesRef, notify, preservePorts, t, visibleRef])

  // 不依赖拖拽的排序方式（键盘、触屏更友好）：where = 'up' | 'down' | 'top' | 'bottom'
  const moveNode = useCallback((id, where) => {
    const visible = visibleRef.current
    const index = visible.findIndex((node) => node.id === id)
    if (index < 0) return Promise.resolve()
    const target = { up: index - 1, down: index + 1, top: 0, bottom: visible.length - 1 }[where]
    if (target === undefined || target < 0 || target >= visible.length || target === index) return Promise.resolve()
    return reorder(id, visible[target].id)
  }, [reorder, visibleRef])

  /* ---------- 批量操作 ---------- */

  const setSelectedEnabled = useCallback(async (enabled) => {
    if (batchEnabledBusyRef.current) return false
    const ids = selectedNodesRef.current.map((node) => node.id)
    if (ids.length === 0) return false
    batchEnabledBusyRef.current = true
    return track(enabled ? 'batchEnable' : 'batchDisable', async () => {
      try {
        const response = await api.post('/nodes/batch-enabled', { ids, enabled })
        const updated = Number(response.data?.updated_count ?? ids.length)
        if (updated === 0) {
          notify.info(t(enabled ? 'batch_enable_unchanged' : 'batch_disable_unchanged'))
        } else {
          notify.success(t(enabled ? 'batch_enable_success' : 'batch_disable_success', { count: updated }))
        }
        await Promise.all([loadNodes({ silent: true }), loadGroups()])
        return true
      } catch (error) {
        notify.error(errorText(error, t('server_error')))
        await Promise.all([loadNodes({ silent: true }), loadGroups()])
        return false
      } finally {
        batchEnabledBusyRef.current = false
      }
    })
  }, [errorText, loadGroups, loadNodes, notify, selectedNodesRef, t, track])

  const enableSelected = useCallback(() => setSelectedEnabled(true), [setSelectedEnabled])
  const disableSelected = useCallback(() => setSelectedEnabled(false), [setSelectedEnabled])

  const deleteSelected = useCallback(async () => {
    const ids = selectedNodesRef.current.map((node) => node.id)
    if (ids.length === 0) return
    const confirmed = await confirm({ title: t('batch_delete_confirm', { count: ids.length }), content: t('delete_node_desc'), okText: t('delete'), danger: true })
    if (!confirmed) return
    await track('delete', async () => {
      try {
        await api.post('/nodes/batch-delete', { ids })
        notify.success(t('batch_delete_success', { count: ids.length }))
        clearSelection()
      } catch (error) {
        notify.error(errorText(error, t('server_error')))
      }
      await Promise.all([loadNodes({ silent: true }), loadGroups()])
    })
  }, [clearSelection, confirm, errorText, loadGroups, loadNodes, notify, selectedNodesRef, t, track])

  const dedupeSelected = useCallback(async () => {
    const seen = new Set()
    const duplicates = []
    for (const node of selectedNodesRef.current) {
      const signature = outboundSignature(node)
      if (seen.has(signature)) duplicates.push(node.id)
      else seen.add(signature)
    }
    if (duplicates.length === 0) {
      notify.info(t('outbound_dedup_no_duplicates'))
      return
    }
    const confirmed = await confirm({ title: t('outbound_dedup'), content: t('outbound_dedup_confirm', { count: duplicates.length }), okText: t('delete'), danger: true })
    if (!confirmed) return
    await track('dedupe', async () => {
      try {
        await api.post('/nodes/batch-delete', { ids: duplicates })
        notify.success(t('outbound_dedup_success', { count: duplicates.length }))
      } catch (error) {
        notify.error(errorText(error, t('server_error')))
      }
      await Promise.all([loadNodes({ silent: true }), loadGroups()])
    })
  }, [confirm, errorText, loadGroups, loadNodes, notify, outboundSignature, selectedNodesRef, t, track])

  const applyBatchAuth = useCallback(async (auth) => {
    if (auth.auth_enabled === false) {
      const confirmed = await confirm({ title: t('batch_auth_disable_action'), content: t('auth_open_warning'), okText: t('confirm'), danger: true })
      if (!confirmed) return false
    }
    const ids = selectedNodesRef.current.map((node) => node.id)
    if (ids.length === 0) return false
    try {
      await api.post('/nodes/batch-auth', { node_ids: ids, ...auth })
      notify.success(t('auth_updated'))
      await loadNodes({ silent: true })
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [confirm, errorText, loadNodes, notify, selectedNodesRef, t])

  const checkSelected = useCallback(() => checkNodes(selectedNodesRef.current.map((node) => node.id)), [checkNodes, selectedNodesRef])
  const checkVisible = useCallback(() => checkNodes(visibleRef.current.map((node) => node.id)), [checkNodes, visibleRef])

  /* ---------- 导出 ---------- */

  const exportNode = useCallback((node) => track('export', async () => {
    if (!isNodeExportable(node)) {
      notify.warning(t('no_exportable_nodes'))
      return
    }
    try {
      const response = await api.get(`/nodes/${node.id}/export`)
      setOverlay({ kind: 'export', text: response.data?.link || '', count: 1 })
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
    }
  }), [errorText, isNodeExportable, notify, setOverlay, t, track])

  const exportIds = useCallback((ids) => track('export', async () => {
    if (ids.length === 0) {
      notify.warning(t('no_exportable_nodes'))
      return
    }
    try {
      const response = await api.post('/nodes/batch-export', { ids })
      const links = (response.data?.results || []).filter((item) => item.success && item.link).map((item) => item.link)
      const failed = response.data?.failed ?? 0
      if (failed > 0) notify.warning(t('export_partial', { count: failed }))
      setOverlay({ kind: 'export', text: links.join('\n'), count: links.length })
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
    }
  }), [errorText, notify, setOverlay, t, track])

  const exportSelected = useCallback(() => {
    const ids = selectedNodesRef.current.filter(isNodeExportable).map((node) => node.id)
    return exportIds(ids)
  }, [exportIds, isNodeExportable, selectedNodesRef])

  const exportScope = useCallback((scope) => {
    const pool = scope === 'all' ? allNodesRef.current : nodesRef.current
    const ids = pool.filter(isNodeExportable).map((node) => node.id)
    return exportIds(ids)
  }, [allNodesRef, exportIds, isNodeExportable, nodesRef])

  /* ---------- 导入 ---------- */

  // 解析单条分享链接，返回可直接交给节点编辑器的草稿；失败时抛出可展示的错误文案。
  const parseLink = useCallback(async (link) => {
    try {
      const response = await api.post('/parse-link', { link: String(link || '').trim() })
      const { type, name, config } = response.data
      return { type, name, config, enabled: true }
    } catch (error) {
      throw new Error(errorText(error, t('invalid_link')))
    }
  }, [errorText, t])

  const importBatch = useCallback(async ({ content, sourceType, enabled, groupId }) => {
    if (importBusyRef.current || !mountedRef.current) return null
    importBusyRef.current = true
    const controller = new AbortController()
    importRequestRef.current = controller
    const targetGroupId = groupId ?? activeGroupIdRef.current
    try {
      const response = await api.post(
        '/nodes/batch-import',
        { content, source_type: sourceType, enabled, ...(targetGroupId != null ? { group_id: targetGroupId } : {}) },
        { timeout: BATCH_IMPORT_TIMEOUT_MS, signal: controller.signal }
      )
      if (!mountedRef.current) return null
      const { success, failed, results } = response.data
      const importedIds = results.filter((item) => item.success).map((item) => item.id)
      await Promise.all([loadNodes({ silent: true }), loadGroups()])
      if (mountedRef.current && enabled && importedIds.length > 0) checks.runChecks(importedIds)
      return { success, failed, errors: results.filter((item) => item.success === false) }
    } catch (error) {
      if (!mountedRef.current || error?.code === 'ERR_CANCELED') return null
      const data = error.response?.data
      const errors = Array.isArray(data?.results)
        ? data.results.filter((item) => item?.success === false)
        : [{ error: errorText(error, t('server_error')) }]
      return { success: 0, failed: errors.length, errors }
    } finally {
      importRequestRef.current = null
      importBusyRef.current = false
    }
  }, [activeGroupIdRef, checks, errorText, loadGroups, loadNodes, t])

  /* ---------- 系统 ---------- */

  const restartRuntime = useCallback(async () => {
    setRuntimeRestarting(true)
    try {
      const response = await api.post('/runtime/restart')
      setRuntime(response.data?.status || null)
      notify.success(t('runtime_restarted'))
    } catch (error) {
      if (error.response?.data?.status) setRuntime(error.response.data.status)
      notify.error(errorText(error, t('server_error')))
    } finally {
      setRuntimeRestarting(false)
    }
  }, [errorText, notify, setRuntime, setRuntimeRestarting, t])

  const saveSettings = useCallback(async (updateData) => {
    try {
      const response = await api.put('/settings', updateData)
      if (response.data?.password_changed) {
        notify.success(t('admin_password_changed_relogin'))
        onPasswordChanged?.()
        return true
      }
      await Promise.all([loadSettings(), loadNodes({ silent: true })])
      notify.success(t('settings_updated'))
      return true
    } catch (error) {
      notify.error(errorText(error, t('settings_update_failed')))
      return false
    }
  }, [errorText, loadNodes, loadSettings, notify, onPasswordChanged, t])

  const checkGlobalUpstream = useCallback(async () => {
    try {
      await api.post('/settings/upstream/check-ip', null, { timeout: 310000 })
      await loadSettings()
      notify.success(t('upstream_check_success'))
    } catch (error) {
      await loadSettings()
      notify.error(errorText(error, t('server_error')))
    }
  }, [errorText, loadSettings, notify, t])

  return {
    reorder, moveNode, enableSelected, disableSelected, deleteSelected, dedupeSelected, applyBatchAuth, checkSelected, checkVisible,
    exportNode, exportSelected, exportScope, parseLink, importBatch, restartRuntime, saveSettings, checkGlobalUpstream,
  }
}
