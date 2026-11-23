// 节点管理控制器（无 UI）：数据加载、筛选、选择、排序与全部节点操作。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import api, { EXTERNAL_CHANGE_EVENT } from './api'
import { isNodeExportable, stableConfigSignature, stableJSONStringify } from './nodeModel'
import { EMPTY_CRITERIA, collectFacets, filterNodes, hasActiveCriteria } from './nodeFilters'
import { summarizeNodes } from './nodeView'
import { useNodeChecks } from './useNodeChecks'
import { ensureLatestFrontendBuild } from './version'
import { createLatestRequestCoordinator } from './latestRequest'

const RUNTIME_POLL_MS = 5000
const errorText = (error, fallback) => error?.response?.data?.error || fallback

const outboundSignature = (node) =>
  stableJSONStringify({
    type: String(node?.type || ''),
    config: stableConfigSignature(node?.config),
    upstream_mode: String(node?.upstream_mode || 'none'),
    upstream_type: String(node?.upstream_type || ''),
    upstream_config: stableConfigSignature(node?.upstream_config),
  })

// 未变化的节点沿用旧对象引用，配合 memo 让列表只重绘真正变化的行。
const reconcile = (previous, next) => {
  const known = new Map(previous.map((node) => [node.id, node]))
  let changed = previous.length !== next.length
  const merged = next.map((node, index) => {
    const old = known.get(node.id)
    if (old && JSON.stringify(old) === JSON.stringify(node)) {
      if (previous[index] !== old) changed = true
      return old
    }
    changed = true
    return node
  })
  return changed ? merged : previous
}

const restrictSelection = (selected, visibleIds) => {
  if (selected.size === 0) return selected
  const scoped = new Set([...selected].filter((id) => visibleIds.has(id)))
  return scoped.size === selected.size ? selected : scoped
}

export function useNodeManager({ t, notify, confirm, onPasswordChanged }) {
  const [nodes, setNodeState] = useState([])
  const [loading, setLoading] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [criteria, setCriteria] = useState(EMPTY_CRITERIA)
  const [selectedIds, setSelectedIds] = useState(() => new Set())
  const [settings, setSettings] = useState(null)
  const [runtime, setRuntimeState] = useState(null)
  const [runtimeRestarting, setRuntimeRestarting] = useState(false)
  const [version, setVersion] = useState({ server: '', update: null })
  const [flagBusy, setFlagBusy] = useState({})
  const [pinBusy, setPinBusy] = useState({})
  const [overlay, setOverlay] = useState(null)
  const [working, setWorking] = useState({})

  const nodesRef = useRef(nodes)
  const runtimeRequest = useRef(null)
  const [resources] = useState(createLatestRequestCoordinator)
  const invalidateRuntime = useCallback(() => {
    resources.invalidate('runtime')
    runtimeRequest.current = null
  }, [resources])
  const setRuntime = useCallback((next) => {
    invalidateRuntime()
    setRuntimeState(next)
  }, [invalidateRuntime])

  const preservePorts = Boolean(settings?.preserve_inbound_ports)
  const visibleNodes = useMemo(() => filterNodes(nodes, criteria), [nodes, criteria])
  const visibleIds = useMemo(() => new Set(visibleNodes.map((node) => node.id)), [visibleNodes])
  const summary = useMemo(() => summarizeNodes(nodes), [nodes])
  const facets = useMemo(() => collectFacets(nodes), [nodes])
  const filtersActive = hasActiveCriteria(criteria)
  const canReorder = (!filtersActive || preservePorts) && visibleNodes.length > 1
  const scopedSelectedIds = useMemo(() => restrictSelection(selectedIds, visibleIds), [selectedIds, visibleIds])
  const selectedNodes = useMemo(() => scopedSelectedIds.size > 0
    ? visibleNodes.filter((node) => scopedSelectedIds.has(node.id))
    : [], [visibleNodes, scopedSelectedIds])
  const visibleRef = useRef(visibleNodes)
  const selectedNodesRef = useRef(selectedNodes)
  useEffect(() => {
    nodesRef.current = nodes
    visibleRef.current = visibleNodes
    selectedNodesRef.current = selectedNodes
  }, [nodes, visibleNodes, selectedNodes])

  // 选择只对当前可见的节点有效：筛选变化后自动丢弃不可见的选中项，避免误操作看不见的节点。
  useEffect(() => {
    if (scopedSelectedIds === selectedIds) return
    setSelectedIds((prev) => prev === selectedIds ? scopedSelectedIds : restrictSelection(prev, visibleIds))
  }, [scopedSelectedIds, selectedIds, visibleIds])

  const setNodes = useCallback((next) => {
    resources.invalidate('nodes')
    setLoading(false)
    setNodeState(next)
  }, [resources])

  const patchNode = useCallback((id, patch) => {
    setNodes((prev) => prev.map((node) => (node.id === id ? { ...node, ...patch } : node)))
  }, [setNodes])

  const loadNodes = useCallback(({ silent = false } = {}) => {
    if (!silent) setLoading(true)
    return resources.run(
      'nodes',
      (signal) => api.get('/nodes', { signal }),
      (response) => {
        setNodeState((prev) => reconcile(prev, response.data || []))
        setLoaded(true)
      },
      {
        onError: (error) => {
          if (error?.code !== 'ERR_CANCELED') notify.error(t('network_error'))
        },
        onSettled: () => setLoading(false),
      }
    )
  }, [notify, resources, t])

  const loadSettings = useCallback(() => resources.run(
    'settings',
    (signal) => api.get('/settings', { signal }),
    (response) => setSettings(response.data || null),
    { onError: () => setSettings(null) }
  ), [resources])

  const loadRuntime = useCallback(() => {
    if (runtimeRequest.current) return runtimeRequest.current
    const request = resources.run(
      'runtime',
      (signal) => api.get('/runtime/status', { signal }),
      (response) => setRuntimeState(response.data || null)
    ).finally(() => {
      if (runtimeRequest.current === request) runtimeRequest.current = null
    })
    runtimeRequest.current = request
    return request
  }, [resources])

  const loadVersion = useCallback(() => resources.run(
    'version',
    (signal) => api.get('/version', { signal }),
    (response) => {
      const server = response.data?.version || ''
      const check = ensureLatestFrontendBuild(server)
      if (check.refreshed) return
      setVersion({ server, update: response.data?.update || null })
      if (check.mismatch) {
        notify.warning(t('frontend_version_mismatch', { server: check.serverVersion || '-', client: check.frontendVersion || '-' }))
      }
    },
    { onError: () => setVersion({ server: '', update: null }) }
  ), [notify, resources, t])

  const refreshAll = useCallback(async () => {
    await Promise.all([loadNodes(), loadSettings(), loadRuntime(), loadVersion()])
  }, [loadNodes, loadRuntime, loadSettings, loadVersion])

  useEffect(() => {
    let timer
    let activePoll
    const pollRuntime = async () => {
      if (document.hidden || activePoll) return
      const request = loadRuntime()
      activePoll = request
      await request
      if (activePoll !== request) return
      activePoll = null
      if (!document.hidden) timer = window.setTimeout(pollRuntime, RUNTIME_POLL_MS)
    }
    const syncVisibility = () => {
      window.clearTimeout(timer)
      if (document.hidden) {
        activePoll = null
        invalidateRuntime()
      } else {
        void pollRuntime()
      }
    }
    loadNodes()
    loadSettings()
    loadVersion()
    syncVisibility()
    document.addEventListener('visibilitychange', syncVisibility)
    window.addEventListener(EXTERNAL_CHANGE_EVENT, refreshAll)
    return () => {
      activePoll = null
      window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', syncVisibility)
      window.removeEventListener(EXTERNAL_CHANGE_EVENT, refreshAll)
      runtimeRequest.current = null
      resources.abortAll()
    }
  }, [invalidateRuntime, loadNodes, loadRuntime, loadSettings, loadVersion, refreshAll, resources])

  /* ---------- 测 IP ---------- */

  const checks = useNodeChecks({
    onResult: (id, data, { updateFinal }) => {
      const hasUpstream = Object.prototype.hasOwnProperty.call(data || {}, 'upstream')
      const upstream = data?.upstream
      patchNode(id, {
        ...(updateFinal
          ? { node_ip: data?.ip || '', location: data?.location || '', country_code: data?.country_code || '', latency: Number(data?.latency) || 0 }
          : {}),
        ...(hasUpstream
          ? {
              upstream_ip: upstream?.ip || '',
              upstream_location: upstream?.location || '',
              upstream_country_code: upstream?.country_code || '',
              upstream_latency: Number(upstream?.latency) || 0,
              upstream_error: upstream?.error || '',
            }
          : {}),
      })
    },
    onClear: (id) => patchNode(id, { node_ip: '', location: '', country_code: '', latency: 0 }),
    onFinished: (outcome, { silent }) => {
      if (silent) return
      if (outcome.stopped) {
        notify.info(t('batch_check_ip_stopped', { current: outcome.done, total: outcome.total }))
      } else if (outcome.total === 1) {
        if (outcome.failures.length > 0) notify.error(outcome.failures[0].error || t('check_failed'))
        else notify.success(t('check_done'))
      } else if (outcome.failures.length > 0) {
        notify.warning(t('check_summary_mixed', { ok: outcome.completed, failed: outcome.failures.length }))
      } else {
        notify.success(t('batch_check_ip_success', { count: outcome.completed }))
      }
    },
  })
  const { runChecks } = checks

  // 已禁用的节点后端会直接拒绝检测，这里提前过滤并给出明确提示。
  const checkNodes = useCallback((ids) => {
    const enabled = new Set(nodesRef.current.filter((node) => node.enabled !== false).map((node) => node.id))
    const runnable = ids.filter((id) => enabled.has(id))
    if (runnable.length === 0) {
      notify.warning(t('check_none_enabled'))
      return Promise.resolve(null)
    }
    if (runnable.length < ids.length) notify.info(t('check_skipped_disabled', { count: ids.length - runnable.length }))
    return runChecks(runnable)
  }, [notify, runChecks, t])

  /* ---------- 选择 ---------- */

  const toggleSelect = useCallback((id, on) => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      const shouldSelect = typeof on === 'boolean' ? on : !next.has(id)
      if (shouldSelect) next.add(id)
      else next.delete(id)
      return next
    })
  }, [])
  const selectAllVisible = useCallback((on) => {
    setSelectedIds(on ? visibleIds : new Set())
  }, [visibleIds])
  const clearSelection = useCallback(() => setSelectedIds(new Set()), [])

  /* ---------- 单节点操作 ---------- */

  const toggleFlag = useCallback(async (node, field) => {
    if (flagBusy[node.id]) return
    const current = field === 'enabled' ? node.enabled !== false : node.tcp_reuse_enabled !== false
    const next = !current
    if (field === 'tcp_reuse_enabled' && next && (!node.username || !node.password)) {
      notify.warning(t('tcp_reuse_auth_required_hint'))
    }
    setFlagBusy((prev) => ({ ...prev, [node.id]: true }))
    patchNode(node.id, { [field]: next })
    try {
      const response = await api.patch(`/nodes/${node.id}`, { [field]: next })
      patchNode(node.id, response.data || {})
    } catch (error) {
      patchNode(node.id, { [field]: current })
      notify.error(errorText(error, t('server_error')))
    } finally {
      setFlagBusy((prev) => ({ ...prev, [node.id]: false }))
    }
  }, [flagBusy, notify, patchNode, t])

  const togglePortPin = useCallback(async (node) => {
    if (preservePorts || pinBusy[node.id]) return
    const pinned = !node.inbound_port_pinned
    setPinBusy((prev) => ({ ...prev, [node.id]: true }))
    try {
      await api.put(`/nodes/${node.id}/port-pin`, { pinned })
      patchNode(node.id, { inbound_port_pinned: pinned })
      notify.success(t(pinned ? 'port_pin_enabled' : 'port_pin_disabled'))
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      await loadNodes({ silent: true })
    } finally {
      setPinBusy((prev) => ({ ...prev, [node.id]: false }))
    }
  }, [loadNodes, notify, patchNode, pinBusy, preservePorts, t])

  const saveRemark = useCallback(async (node, remark) => {
    if (remark === (node.remark ?? '')) return true
    try {
      await api.put(`/nodes/${node.id}/remark`, { remark })
      patchNode(node.id, { remark })
      notify.success(t('remark_saved'))
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [notify, patchNode, t])

  const removeNode = useCallback(async (node) => {
    const confirmed = await confirm({ title: t('delete_node_title', { name: node.name }), content: t('delete_node_desc'), okText: t('delete'), danger: true })
    if (!confirmed) return false
    try {
      await api.delete(`/nodes/${node.id}`)
      setNodes((prev) => prev.filter((item) => item.id !== node.id))
      notify.success(t('node_deleted'))
      await loadNodes({ silent: true })
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [confirm, loadNodes, notify, setNodes, t])

  const saveNode = useCallback(async (values, editing, { autoCheck = false } = {}) => {
    if (values.auth_enabled === false && (!editing || (editing.username && editing.password))) {
      const confirmed = await confirm({ title: t('batch_auth_disable_action'), content: t('auth_open_warning'), okText: t('confirm'), danger: true })
      if (!confirmed) return false
    }
    try {
      if (editing?.id) {
        await api.put(`/nodes/${editing.id}`, values)
        notify.success(t('node_updated'))
        await loadNodes({ silent: true })
      } else {
        const response = await api.post('/nodes', values)
        notify.success(t('node_created'))
        await loadNodes({ silent: true })
        const createdId = response.data?.id
        if (autoCheck && createdId && values.enabled !== false) runChecks([createdId], { silent: true })
      }
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [confirm, loadNodes, notify, runChecks, t])

  const replaceNode = useCallback(async (node, link, updateName) => {
    try {
      await api.put(`/nodes/${node.id}/replace`, { link: link.trim(), update_name: updateName })
      notify.success(t('node_replaced'))
      await loadNodes({ silent: true })
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [loadNodes, notify, t])

  const saveUpstream = useCallback(async (node, values) => {
    try {
      const response = await api.put(`/nodes/${node.id}/upstream`, values)
      patchNode(node.id, response.data || {})
      notify.success(t('upstream_saved'))
      await loadNodes({ silent: true })
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [loadNodes, notify, patchNode, t])

  return {
    nodes, loading, loaded, criteria, setCriteria, visibleNodes, summary, facets, filtersActive,
    selectedIds: scopedSelectedIds, selectedNodes, toggleSelect, selectAllVisible, clearSelection,
    settings, preservePorts, loadSettings, runtime, setRuntime, runtimeRestarting, setRuntimeRestarting, version,
    flagBusy, pinBusy, working, setWorking, overlay, setOverlay, canReorder,
    checks, checkNodes, loadNodes, refreshAll, patchNode, setNodes, nodesRef, visibleRef, selectedNodesRef,
    toggleEnabled: (node) => toggleFlag(node, 'enabled'),
    toggleTcpReuse: (node) => toggleFlag(node, 'tcp_reuse_enabled'),
    togglePortPin, saveRemark, removeNode, saveNode, replaceNode, saveUpstream,
    helpers: { t, notify, confirm, onPasswordChanged, errorText, outboundSignature, isNodeExportable },
  }
}
