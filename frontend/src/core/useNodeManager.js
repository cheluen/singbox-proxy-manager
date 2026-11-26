// 节点与分组管理控制器（无 UI）：分组加载/切换、节点数据加载、筛选、选择、排序与单节点操作。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import api, { EXTERNAL_CHANGE_EVENT } from './api'
import { columnWidthsEqual, normalizeColumnWidths } from './columnWidths'
import { isNodeExportable, isPolicyNode, stableConfigSignature, stableJSONStringify } from './nodeModel'
import { resolvePolicyMemberIds, unlinkPolicyGroupInSources } from './protocolForm'
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

const groupPreservesPorts = (group) => Boolean(group && group.allocate_port !== false && group.preserve_inbound_ports)

export function useNodeManager({ t, notify, confirm, onPasswordChanged }) {
  const [allNodes, setAllNodeState] = useState([])
  const [groups, setGroups] = useState([])
  const [groupsLoaded, setGroupsLoaded] = useState(false)
  const [groupsError, setGroupsError] = useState('')
  const [activeGroupId, setActiveGroupIdState] = useState(null)
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
  const [refreshingGroupId, setRefreshingGroupId] = useState(null)
  const [overlay, setOverlay] = useState(null)
  const [working, setWorking] = useState({})

  const allNodesRef = useRef(allNodes)
  const groupsRef = useRef(groups)
  const activeGroupIdRef = useRef(activeGroupId)
  const flagBusyRef = useRef({})
  const runtimeRequest = useRef(null)
  const columnWidthsRef = useRef({})
  const persistedColumnWidthsRef = useRef({})
  const columnWidthInFlightRef = useRef(false)
  const columnWidthQueuedRef = useRef(null)
  const columnWidthWaitersRef = useRef([])
  const [resources] = useState(createLatestRequestCoordinator)

  const invalidateRuntime = useCallback(() => {
    resources.invalidate('runtime')
    runtimeRequest.current = null
  }, [resources])
  const setRuntime = useCallback((next) => {
    invalidateRuntime()
    setRuntimeState(next)
  }, [invalidateRuntime])

  const effectiveGroupId = useMemo(() => {
    if (groups.length === 0) return null
    if (activeGroupId != null && groups.some((group) => group.id === activeGroupId)) return activeGroupId
    return groups[0].id
  }, [activeGroupId, groups])

  const groupById = useMemo(() => new Map(groups.map((group) => [group.id, group])), [groups])
  const activeGroup = effectiveGroupId != null ? (groupById.get(effectiveGroupId) || null) : null
  const firstGroupId = groups[0]?.id ?? null

  const nodes = useMemo(() => {
    // 后端保证至少存在 1 个分组；分组未确定时绝不回退全量节点，避免全选/批量操作越界跨组。
    if (effectiveGroupId == null) return []
    return allNodes.filter((node) => {
      if (node.group_id == null) return effectiveGroupId === firstGroupId
      return node.group_id === effectiveGroupId
    })
  }, [allNodes, effectiveGroupId, firstGroupId])

  const preservePorts = groupPreservesPorts(activeGroup)
  const nodePreservesPorts = useCallback(
    (node) => groupPreservesPorts(node?.group_id != null ? groupById.get(node.group_id) : activeGroup),
    [activeGroup, groupById]
  )
  const columnWidths = useMemo(
    () => normalizeColumnWidths(settings?.node_list_column_widths),
    [settings?.node_list_column_widths]
  )
  useEffect(() => {
    columnWidthsRef.current = columnWidths
  }, [columnWidths])

  const visibleNodes = useMemo(() => filterNodes(nodes, criteria), [nodes, criteria])
  const visibleIds = useMemo(() => new Set(visibleNodes.map((node) => node.id)), [visibleNodes])
  const summary = useMemo(() => summarizeNodes(nodes), [nodes])
  const facets = useMemo(() => collectFacets(nodes), [nodes])
  const filtersActive = hasActiveCriteria(criteria)
  const canReorder = (!filtersActive || preservePorts || activeGroup?.allocate_port === false) && visibleNodes.length > 1
  const scopedSelectedIds = useMemo(() => restrictSelection(selectedIds, visibleIds), [selectedIds, visibleIds])
  const selectedNodes = useMemo(() => (scopedSelectedIds.size > 0
    ? visibleNodes.filter((node) => scopedSelectedIds.has(node.id))
    : []), [visibleNodes, scopedSelectedIds])

  const nodesRef = useRef(nodes)
  const visibleRef = useRef(visibleNodes)
  const selectedNodesRef = useRef(selectedNodes)
  useEffect(() => {
    allNodesRef.current = allNodes
    groupsRef.current = groups
    activeGroupIdRef.current = effectiveGroupId
    nodesRef.current = nodes
    visibleRef.current = visibleNodes
    selectedNodesRef.current = selectedNodes
  }, [allNodes, effectiveGroupId, groups, nodes, selectedNodes, visibleNodes])

  // 选择只对当前可见的节点有效：筛选或分组变化后自动丢弃不可见的选中项，避免误操作看不见的节点。
  useEffect(() => {
    if (scopedSelectedIds === selectedIds) return
    setSelectedIds((prev) => (prev === selectedIds ? scopedSelectedIds : restrictSelection(prev, visibleIds)))
  }, [scopedSelectedIds, selectedIds, visibleIds])

  const selectGroup = useCallback((nextGroupId) => {
    setActiveGroupIdState(nextGroupId)
    setSelectedIds(new Set())
  }, [])

  const setAllNodes = useCallback((next) => {
    resources.invalidate('nodes')
    setLoading(false)
    setAllNodeState(next)
  }, [resources])

  // 兼容旧调用：只更新当前组节点顺序时合并回全量节点数组。
  const setNodes = useCallback((updater) => {
    resources.invalidate('nodes')
    setLoading(false)
    setAllNodeState((prev) => {
      const currentGroupId = activeGroupIdRef.current
      const fallbackGroupId = groupsRef.current[0]?.id ?? null
      const belongs = (node) => currentGroupId == null || (node.group_id == null ? currentGroupId === fallbackGroupId : node.group_id === currentGroupId)
      const currentSlice = prev.filter(belongs)
      const nextSlice = typeof updater === 'function' ? updater(currentSlice) : updater
      if (currentGroupId == null) return nextSlice
      let cursor = 0
      const replaced = prev.map((node) => (belongs(node) ? (nextSlice[cursor++] ?? null) : node)).filter(Boolean)
      while (cursor < nextSlice.length) replaced.push(nextSlice[cursor++])
      return replaced
    })
  }, [resources])

  const patchNode = useCallback((id, patch) => {
    setAllNodes((prev) => prev.map((node) => (node.id === id ? { ...node, ...patch } : node)))
  }, [setAllNodes])

  const loadGroups = useCallback(() => resources.run(
    'groups',
    (signal) => api.get('/node-groups', { signal }),
    (response) => {
      const list = Array.isArray(response.data) ? response.data : null
      if (!list || list.length === 0) {
        setGroupsError(t('groups_load_failed'))
        return
      }
      setGroups(list)
      setGroupsLoaded(true)
      setGroupsError('')
      setActiveGroupIdState((prev) => (prev != null && list.some((group) => group.id === prev) ? prev : (list[0]?.id ?? null)))
    },
    {
      onError: (error) => {
        if (error?.code === 'ERR_CANCELED') return
        setGroupsError(errorText(error, t('groups_load_failed')))
      },
    }
  ), [resources, t])

  const loadNodes = useCallback(({ silent = false } = {}) => {
    if (!silent) setLoading(true)
    return resources.run(
      'nodes',
      (signal) => api.get('/nodes', { signal }),
      (response) => {
        setAllNodeState((prev) => reconcile(prev, response.data || []))
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
    (response) => {
      const next = response.data || null
      if (columnWidthInFlightRef.current || columnWidthQueuedRef.current !== null) {
        const activeWidths = columnWidthsRef.current
        setSettings(next ? { ...next, node_list_column_widths: activeWidths } : null)
        return
      }
      persistedColumnWidthsRef.current = normalizeColumnWidths(next?.node_list_column_widths)
      setSettings(next)
    },
    { onError: () => setSettings(null) }
  ), [resources])

  const flushColumnWidths = useCallback(async () => {
    if (columnWidthInFlightRef.current) return
    columnWidthInFlightRef.current = true
    let lastOk = true
    try {
      while (columnWidthQueuedRef.current !== null) {
        const target = columnWidthQueuedRef.current
        columnWidthQueuedRef.current = null
        if (columnWidthsEqual(target, persistedColumnWidthsRef.current)) {
          lastOk = true
          continue
        }
        try {
          const response = await api.put('/settings', { node_list_column_widths: target })
          const confirmed = response.data?.node_list_column_widths !== undefined
            ? normalizeColumnWidths(response.data.node_list_column_widths)
            : target
          persistedColumnWidthsRef.current = confirmed
          lastOk = true
          if (columnWidthQueuedRef.current === null) {
            columnWidthsRef.current = confirmed
            setSettings((prev) => (prev ? { ...prev, node_list_column_widths: confirmed } : { node_list_column_widths: confirmed }))
          }
        } catch (error) {
          lastOk = false
          if (columnWidthQueuedRef.current === null) {
            const fallback = persistedColumnWidthsRef.current
            columnWidthsRef.current = fallback
            setSettings((prev) => (prev ? { ...prev, node_list_column_widths: fallback } : prev))
            notify.error(errorText(error, t('settings_update_failed')))
          }
        }
      }
    } finally {
      columnWidthInFlightRef.current = false
      const waiters = columnWidthWaitersRef.current.splice(0)
      for (const resolve of waiters) resolve(lastOk)
    }
  }, [notify, t])

  const saveColumnWidths = useCallback((nextWidths) => {
    const sanitized = normalizeColumnWidths(nextWidths)
    if (
      !columnWidthInFlightRef.current &&
      columnWidthQueuedRef.current === null &&
      columnWidthsEqual(sanitized, columnWidthsRef.current) &&
      columnWidthsEqual(sanitized, persistedColumnWidthsRef.current)
    ) {
      return Promise.resolve(true)
    }
    columnWidthsRef.current = sanitized
    columnWidthQueuedRef.current = sanitized
    resources.invalidate('settings')
    setSettings((prev) => (prev ? { ...prev, node_list_column_widths: sanitized } : { node_list_column_widths: sanitized }))
    return new Promise((resolve) => {
      columnWidthWaitersRef.current.push(resolve)
      void flushColumnWidths()
    })
  }, [flushColumnWidths, resources])

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
    await Promise.all([loadGroups(), loadNodes(), loadSettings(), loadRuntime(), loadVersion()])
  }, [loadGroups, loadNodes, loadRuntime, loadSettings, loadVersion])

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
    loadGroups()
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
  }, [invalidateRuntime, loadGroups, loadNodes, loadRuntime, loadSettings, loadVersion, refreshAll, resources])

  /* ---------- 测 IP ---------- */

  const checks = useNodeChecks({
    onResult: (id, data, { updateFinal }) => {
      const hasUpstream = Object.prototype.hasOwnProperty.call(data || {}, 'upstream')
      const upstream = data?.upstream
      patchNode(id, {
        ...(updateFinal
          ? {
              node_ip: data?.ip || '',
              location: data?.location || '',
              country_code: data?.country_code || '',
              latency: Number(data?.latency) || 0,
              check_status: data?.check_status || 'ok',
              check_error: data?.check_error || '',
              checked_at: data?.checked_at || new Date().toISOString(),
            }
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
    onFail: (id, data) => {
      patchNode(id, {
        node_ip: '',
        location: '',
        country_code: '',
        latency: 0,
        check_status: data?.check_status || 'failed',
        check_error: data?.check_error || data?.error || t('check_failed'),
        checked_at: data?.checked_at || new Date().toISOString(),
      })
    },
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

  const checkNodes = useCallback((ids) => {
    const enabled = new Set(allNodesRef.current.filter((node) => node.enabled !== false).map((node) => node.id))
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

  /* ---------- 分组操作 ---------- */

  const createGroup = useCallback(async (payload) => {
    try {
      const response = await api.post('/node-groups', payload)
      await loadGroups()
      if (response.data?.id) selectGroup(response.data.id)
      notify.success(t('group_created'))
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [loadGroups, notify, selectGroup, t])

  const updateGroup = useCallback(async (group, payload) => {
    const attempt = async (body, retried = new Set()) => {
      try {
        await api.patch(`/node-groups/${group.id}`, body)
        await Promise.all([loadGroups(), loadNodes({ silent: true })])
        notify.success(t('group_updated'))
        return true
      } catch (error) {
        const data = error?.response?.data
        const code = error?.response?.status === 409 ? data?.code : ''
        if (code === 'pinned_ports_exist' && !retried.has(code)) {
          const count = Array.isArray(data.pinned_node_ids) ? data.pinned_node_ids.length : 0
          const confirmed = await confirm({
            title: t('group_release_pins_title'),
            content: t('group_release_pins_desc', { count }),
            okText: t('confirm'),
            danger: true,
          })
          if (!confirmed) return false
          return attempt({ ...body, release_pins: true }, new Set([...retried, code]))
        }
        if (code === 'upstream_conflict' && !retried.has(code)) {
          await loadGroups()
          const confirmed = await confirm({
            title: t('group_upstream_conflict_title'),
            content: t('group_upstream_conflict_desc'),
            okText: t('confirm'),
            danger: true,
          })
          if (!confirmed) return false
          const next = { ...body }
          delete next.expected_upstream_revision
          delete next.expected_upstream_enabled
          return attempt(next, new Set([...retried, code]))
        }
        notify.error(errorText(error, t('server_error')))
        return false
      }
    }
    return attempt(payload)
  }, [confirm, loadGroups, loadNodes, notify, t])

  const deleteGroup = useCallback(async (group, { mode, targetGroupId } = {}) => {
    try {
      const params = {}
      if (mode) params.mode = mode
      if (mode === 'move' && targetGroupId) params.target_group_id = targetGroupId
      await api.delete(`/node-groups/${group.id}`, { params })
      setSelectedIds(new Set())
      if (activeGroupIdRef.current === group.id) {
        const fallback = (mode === 'move' && targetGroupId)
          ? targetGroupId
          : (groupsRef.current.find((item) => item.id !== group.id)?.id ?? null)
        setActiveGroupIdState(fallback)
      }
      await Promise.all([loadGroups(), loadNodes({ silent: true })])
      notify.success(t('group_deleted'))
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [loadGroups, loadNodes, notify, t])

  const reorderGroups = useCallback(async (ids) => {
    const previous = groupsRef.current
    const byId = new Map(previous.map((item) => [item.id, item]))
    const optimistic = ids.map((id) => byId.get(id)).filter(Boolean)
    if (optimistic.length === previous.length) {
      resources.invalidate('groups')
      setGroups(optimistic)
    }
    try {
      await api.post('/node-groups/reorder', { ids })
      await loadGroups()
      return true
    } catch (error) {
      setGroups(previous)
      notify.error(errorText(error, t('server_error')))
      await loadGroups()
      return false
    }
  }, [loadGroups, notify, resources, t])

  const refreshGroupSubscription = useCallback(async (group) => {
    if (!group?.id || refreshingGroupId === group.id) return false
    setRefreshingGroupId(group.id)
    try {
      const response = await api.post(`/node-groups/${group.id}/subscription/refresh`, null, { timeout: 120000 })
      const data = response.data || {}
      await Promise.all([loadGroups(), loadNodes({ silent: true })])
      notify.success(t('group_sub_refreshed', {
        added: data.added ?? 0,
        updated: data.updated ?? 0,
        removed: data.removed ?? 0,
      }))
      return true
    } catch (error) {
      await loadGroups()
      notify.error(errorText(error, t('server_error')))
      return false
    } finally {
      setRefreshingGroupId(null)
    }
  }, [loadGroups, loadNodes, notify, refreshingGroupId, t])

  const checkGroupUpstream = useCallback(async (groupId) => {
    if (!groupId) return false
    try {
      await api.post(`/node-groups/${groupId}/upstream/check-ip`, null, { timeout: 310000 })
      await loadGroups()
      notify.success(t('upstream_check_success'))
      return true
    } catch (error) {
      await loadGroups()
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [loadGroups, notify, t])

  const moveNodesToGroup = useCallback(async (ids, groupId) => {
    if (!Array.isArray(ids) || ids.length === 0 || !groupId) return false
    setWorking((prev) => ({ ...prev, move: true }))
    try {
      await api.post('/nodes/batch-move', { ids, group_id: groupId })
      clearSelection()
      await Promise.all([loadGroups(), loadNodes({ silent: true })])
      notify.success(t('move_to_group_success', { count: ids.length }))
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    } finally {
      setWorking((prev) => ({ ...prev, move: false }))
    }
  }, [clearSelection, loadGroups, loadNodes, notify, t])

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
      if (field === 'enabled') {
        void loadGroups()
        if (allNodesRef.current.some(isPolicyNode)) void loadNodes({ silent: true })
      }
    } catch (error) {
      patchNode(node.id, { [field]: current })
      notify.error(errorText(error, t('server_error')))
    } finally {
      setFlagBusy((prev) => ({ ...prev, [node.id]: false }))
    }
  }, [flagBusy, loadGroups, loadNodes, notify, patchNode, t])

  const setInboundMode = useCallback(async (node, nextMode) => {
    if (!node?.id || flagBusy[node.id]) return false
    const current = node.inbound_mode === 'https' ? 'https' : 'mixed'
    const target = nextMode === 'https' ? 'https' : 'mixed'
    if (current === target) return true
    setFlagBusy((prev) => ({ ...prev, [node.id]: true }))
    patchNode(node.id, { inbound_mode: target })
    try {
      const response = await api.patch(`/nodes/${node.id}`, { inbound_mode: target })
      patchNode(node.id, response.data || { inbound_mode: target })
      notify.success(t('inbound_mode_updated', { mode: target === 'https' ? t('inbound_mode_https') : t('inbound_mode_mixed') }))
      return true
    } catch (error) {
      patchNode(node.id, { inbound_mode: current })
      notify.error(errorText(error, t('server_error')))
      void loadSettings()
      return false
    } finally {
      setFlagBusy((prev) => ({ ...prev, [node.id]: false }))
    }
  }, [flagBusy, loadSettings, notify, patchNode, t])

  const unlinkPolicyGroup = useCallback(async (node, groupId) => {
    if (!node?.id || !isPolicyNode(node) || flagBusyRef.current[node.id]) return false
    const latest = allNodesRef.current.find((item) => item.id === node.id) || node
    let parsed = {}
    try {
      parsed = JSON.parse(latest.config || '{}')
    } catch {
      parsed = {}
    }
    const nextSources = unlinkPolicyGroupInSources(parsed.member_sources, groupId, allNodesRef.current, latest.id)
    const hasGroupSource = nextSources.some((entry) => entry.type === 'group')
    const nextConfig = {
      ...parsed,
      member_ids: resolvePolicyMemberIds(nextSources, allNodesRef.current, latest.id),
    }
    if (hasGroupSource) nextConfig.member_sources = nextSources
    else delete nextConfig.member_sources
    flagBusyRef.current = { ...flagBusyRef.current, [latest.id]: true }
    setFlagBusy((prev) => ({ ...prev, [latest.id]: true }))
    try {
      const response = await api.put(`/nodes/${latest.id}`, {
        group_id: latest.group_id,
        name: latest.name,
        remark: latest.remark || '',
        type: latest.type,
        config: JSON.stringify(nextConfig),
        inbound_port: latest.inbound_port || 0,
        inbound_mode: latest.inbound_mode === 'https' ? 'https' : 'mixed',
        auth_enabled: Boolean(latest.username && latest.password),
        username: latest.username || '',
        password: latest.password || '',
        tcp_reuse_enabled: latest.tcp_reuse_enabled !== false,
        enabled: latest.enabled !== false,
      })
      if (response.data && typeof response.data === 'object') {
        patchNode(latest.id, response.data)
      }
      const groupName = groupById.get(groupId)?.name || `#${groupId}`
      notify.success(t('policy_group_unlinked', { name: groupName }))
      await loadNodes({ silent: true })
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    } finally {
      const nextBusy = { ...flagBusyRef.current }
      delete nextBusy[latest.id]
      flagBusyRef.current = nextBusy
      setFlagBusy((prev) => ({ ...prev, [latest.id]: false }))
    }
  }, [groupById, loadNodes, notify, patchNode, t])

  const togglePortPin = useCallback(async (node) => {
    if (nodePreservesPorts(node) || pinBusy[node.id] || !node.inbound_port) return
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
  }, [loadNodes, nodePreservesPorts, notify, patchNode, pinBusy, t])

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
      setAllNodes((prev) => prev.filter((item) => item.id !== node.id))
      notify.success(t('node_deleted'))
      await Promise.all([loadNodes({ silent: true }), loadGroups()])
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [confirm, loadGroups, loadNodes, notify, setAllNodes, t])

  const saveNode = useCallback(async (values, editing, { autoCheck = false } = {}) => {
    const targetGroup = editing?.id
      ? groupById.get(values.group_id ?? editing.group_id)
      : activeGroup
    const needsPort = targetGroup ? targetGroup.allocate_port !== false : true
    if (needsPort && values.auth_enabled === false && (!editing || (editing.username && editing.password))) {
      const confirmed = await confirm({ title: t('batch_auth_disable_action'), content: t('auth_open_warning'), okText: t('confirm'), danger: true })
      if (!confirmed) return false
    }
    try {
      if (editing?.id) {
        const payload = values.group_id ? values : { ...values, group_id: editing.group_id }
        await api.put(`/nodes/${editing.id}`, payload)
        notify.success(t('node_updated'))
        await Promise.all([loadNodes({ silent: true }), loadGroups()])
      } else {
        const payload = effectiveGroupId != null && !values.group_id
          ? { ...values, group_id: effectiveGroupId }
          : values
        const response = await api.post('/nodes', payload)
        notify.success(t('node_created'))
        await Promise.all([loadNodes({ silent: true }), loadGroups()])
        const createdId = response.data?.id
        if (autoCheck && createdId && values.enabled !== false) runChecks([createdId], { silent: true })
      }
      return true
    } catch (error) {
      notify.error(errorText(error, t('server_error')))
      return false
    }
  }, [activeGroup, confirm, effectiveGroupId, groupById, loadGroups, loadNodes, notify, runChecks, t])

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
    allNodes, nodes, groups, groupsLoaded, groupsError, groupById, activeGroupId: effectiveGroupId, activeGroup, selectGroup,
    loading, loaded, criteria, setCriteria, visibleNodes, summary, facets, filtersActive,
    selectedIds: scopedSelectedIds, selectedNodes, toggleSelect, selectAllVisible, clearSelection,
    settings, preservePorts, nodePreservesPorts, columnWidths, saveColumnWidths, loadSettings, runtime, setRuntime, runtimeRestarting, setRuntimeRestarting, version,
    flagBusy, pinBusy, refreshingGroupId, working, setWorking, overlay, setOverlay, canReorder,
    checks, checkNodes, loadGroups, loadNodes, refreshAll, patchNode, setNodes,
    allNodesRef, nodesRef, visibleRef, selectedNodesRef, activeGroupIdRef,
    createGroup, updateGroup, deleteGroup, reorderGroups, refreshGroupSubscription, checkGroupUpstream, moveNodesToGroup,
    toggleEnabled: (node) => toggleFlag(node, 'enabled'),
    toggleTcpReuse: (node) => toggleFlag(node, 'tcp_reuse_enabled'),
    setInboundMode, unlinkPolicyGroup,
    togglePortPin, saveRemark, removeNode, saveNode, replaceNode, saveUpstream,
    helpers: { t, notify, confirm, onPasswordChanged, errorText, outboundSignature, isNodeExportable },
  }
}
