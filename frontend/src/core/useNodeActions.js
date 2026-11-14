// 节点的批量操作、排序、导入导出与系统级操作。接收 useNodeManager 的结果并在其上扩展。
import { useCallback, useEffect, useRef } from 'react'
import api from './api'

const BATCH_IMPORT_TIMEOUT_MS = 120000

export function useNodeActions(manager) {
  const {
    helpers, nodesRef, visibleRef, loadNodes, loadSettings, setNodes, selectedIds, clearSelection,
    checks, checkNodes, filtersActive, preservePorts, setWorking, setRuntime, setRuntimeRestarting, setOverlay,
  } = manager
  const { t, notify, confirm, onPasswordChanged, errorText, outboundSignature, isNodeExportable } = helpers
  const importBusyRef = useRef(false)
  const importRequestRef = useRef(null)
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

  const selectedInOrder = useCallback(
    () => nodesRef.current.filter((node) => selectedIds.has(node.id)),
    [nodesRef, selectedIds]
  )

  /* ---------- 排序 ---------- */

  const commitOrder = useCallback(async (ordered) => {
    setNodes(ordered)
    try {
      await api.post('/nodes/reorder', { nodes: ordered.map((node, index) => ({ id: node.id, sort_order: index })) })
      notify.success(t('nodes_reordered'))
    } catch {
      notify.error(t('server_error'))
    }
    await loadNodes({ silent: true })
  }, [loadNodes, notify, setNodes, t])

  // 把 activeId 移到 overId 所在位置。筛选状态下只调整可见子集在完整列表中的相对顺序。
  const reorder = useCallback(async (activeId, overId) => {
    const visible = visibleRef.current
    const from = visible.findIndex((node) => node.id === activeId)
    const to = visible.findIndex((node) => node.id === overId)
    if (from < 0 || to < 0 || from === to) return
    if (filtersActive && !preservePorts) {
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
  }, [commitOrder, filtersActive, nodesRef, notify, preservePorts, t, visibleRef])

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

  const deleteSelected = useCallback(async () => {
    const ids = selectedInOrder().map((node) => node.id)
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
      await loadNodes({ silent: true })
    })
  }, [clearSelection, confirm, errorText, loadNodes, notify, selectedInOrder, t, track])

  const dedupeSelected = useCallback(async () => {
    const seen = new Set()
    const duplicates = []
    for (const node of selectedInOrder()) {
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
      await loadNodes({ silent: true })
    })
  }, [confirm, errorText, loadNodes, notify, outboundSignature, selectedInOrder, t, track])

  const applyBatchAuth = useCallback(async (auth) => {
    if (auth.auth_enabled === false) {
      const confirmed = await confirm({ title: t('batch_auth_disable_action'), content: t('auth_open_warning'), okText: t('confirm'), danger: true })
      if (!confirmed) return false
    }
    try {
      await api.post('/nodes/batch-auth', { node_ids: selectedInOrder().map((node) => node.id), ...auth })
      notify.success(t('auth_updated'))
      await loadNodes({ silent: true })
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [confirm, errorText, loadNodes, notify, selectedInOrder, t])

  const checkSelected = useCallback(() => checkNodes(selectedInOrder().map((node) => node.id)), [checkNodes, selectedInOrder])
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

  const exportSelected = useCallback(() => track('export', async () => {
    const ids = selectedInOrder().filter(isNodeExportable).map((node) => node.id)
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
  }), [errorText, isNodeExportable, notify, selectedInOrder, setOverlay, t, track])

  /* ---------- 导入 ---------- */

  // 解析单条分享链接，返回可直接交给节点编辑器的草稿；失败时抛出可展示的错误文案。
  const parseLink = useCallback(async (link) => {
    try {
      const response = await api.post('/parse-link', { link: String(link || '').trim() })
      const { type, name, config } = response.data || {}
      return { type, name, config: typeof config === 'string' ? config : JSON.stringify(config || {}), enabled: true }
    } catch (error) {
      throw new Error(errorText(error, t('invalid_link')))
    }
  }, [errorText, t])

  const importBatch = useCallback(async ({ content, sourceType, enabled }) => {
    if (importBusyRef.current || !mountedRef.current) return null
    importBusyRef.current = true
    const controller = new AbortController()
    importRequestRef.current = controller
    try {
      const response = await api.post(
        '/nodes/batch-import',
        { content, source_type: sourceType, enabled },
        { timeout: BATCH_IMPORT_TIMEOUT_MS, signal: controller.signal }
      )
      if (!mountedRef.current) return null
      const { success = 0, failed = 0, results = [] } = response.data || {}
      const importedIds = results.filter((item) => item?.success && item?.id).map((item) => Number(item.id)).filter(Number.isFinite)
      await loadNodes({ silent: true })
      if (mountedRef.current && enabled && importedIds.length > 0) checks.runChecks(importedIds)
      return { success, failed, errors: results.filter((item) => item?.success === false) }
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
  }, [checks, errorText, loadNodes, t])

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
      const response = await api.post('/settings/upstream/check-ip')
      await loadSettings()
      notify.success(t('upstream_check_success'))
      return { ok: true, data: response.data }
    } catch (error) {
      const text = errorText(error, t('server_error'))
      notify.error(text)
      return { ok: false, error: text }
    }
  }, [errorText, loadSettings, notify, t])

  return {
    reorder, moveNode, deleteSelected, dedupeSelected, applyBatchAuth, checkSelected, checkVisible,
    exportNode, exportSelected, parseLink, importBatch, restartRuntime, saveSettings, checkGlobalUpstream,
  }
}
