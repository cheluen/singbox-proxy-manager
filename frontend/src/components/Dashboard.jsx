import React, { useMemo, useState, useEffect, useRef, useCallback, useLayoutEffect } from 'react'
import {
    Layout,
    Typography,
    Button,
    Space,
    message,
    notification,
    Modal,
    Input,
    Switch,
    Checkbox,
    Table,
    Tag,
    Collapse,
    Descriptions,
    Popconfirm,
    Tooltip,
    Select,
  } from 'antd'
import {
  LogoutOutlined,
  PlusOutlined,
  SettingOutlined,
  ReloadOutlined,
  ImportOutlined,
  DeleteOutlined,
  EditOutlined,
  ExportOutlined,
  CopyOutlined,
  SwapOutlined,
  CheckCircleOutlined,
  CloseCircleOutlined,
  ThunderboltOutlined,
  HolderOutlined,
  FilterOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { DragDropContext, Droppable, Draggable } from 'react-beautiful-dnd'
import api from '../utils/api'
import {
  ensureLatestFrontendBuild,
  frontendBuildFingerprint,
  frontendBuildVersion,
} from '../utils/version'
import NodeForm from './NodeForm'
import SettingsForm from './SettingsForm'
import BatchAuthModal from './BatchAuthModal'

const { Header, Content } = Layout
const { Title, Text } = Typography
const { TextArea } = Input

function NodeRemarkEditor({ record, saving, onSave, t }) {
  const [draft, setDraft] = useState(record.remark ?? '')

  useEffect(() => {
    setDraft(record.remark ?? '')
  }, [record.id, record.remark])

  const original = record.remark ?? ''
  const hasChanged = draft !== original

  return (
    <Space direction="vertical" style={{ width: '100%' }} size="middle">
      <TextArea
        value={draft}
        rows={3}
        placeholder={t('remark_placeholder')}
        onChange={(e) => setDraft(e.target.value)}
      />
      <Space>
        <Button
          type="primary"
          onClick={() => onSave(record, draft)}
          loading={saving}
          disabled={saving || !hasChanged}
        >
          {t('save')}
        </Button>
        <Button
          onClick={() => setDraft(original)}
          disabled={saving || !hasChanged}
        >
          {t('reset')}
        </Button>
      </Space>
    </Space>
  )
}

const parseCountryName = (location) => {
  const raw = String(location || '').trim()
  if (!raw) {
    return ''
  }
  const parts = raw.split(',').map((part) => part.trim()).filter(Boolean)
  return parts.length > 0 ? parts[parts.length - 1] : raw
}

const normalizeCountryCode = (countryCode) => {
  const raw = String(countryCode || '').trim()
  if (!raw) {
    return ''
  }
  return raw.toLowerCase()
}

const formatCountryWithCode = (record) => {
  const code = normalizeCountryCode(record?.country_code)
  const countryName = parseCountryName(record?.location)
  if (!code && !countryName) {
    return '-'
  }
  if (!countryName) {
    return code
  }
  if (!code) {
    return countryName
  }
  return `${code} ${countryName}`
}

const sortObjectKeysDeep = (value) => {
  if (Array.isArray(value)) {
    return value.map(sortObjectKeysDeep)
  }
  if (!value || typeof value !== 'object') {
    return value
  }
  if (value.constructor !== Object) {
    return value
  }

  return Object.keys(value)
    .sort()
    .reduce((acc, key) => {
      acc[key] = sortObjectKeysDeep(value[key])
      return acc
    }, {})
}

const stableJSONStringify = (value) => {
  try {
    const sorted = sortObjectKeysDeep(value)
    const str = JSON.stringify(sorted)
    return typeof str === 'string' ? str : ''
  } catch {
    return ''
  }
}

function Dashboard({ onLogout }) {
  const { t, i18n } = useTranslation()
  const [nodes, setNodes] = useState([])
  const nodesRef = useRef(nodes)
  const [loading, setLoading] = useState(false)
  const [appVersion, setAppVersion] = useState('')
  const [modalVisible, setModalVisible] = useState(false)
  const [settingsVisible, setSettingsVisible] = useState(false)
  const [batchAuthVisible, setBatchAuthVisible] = useState(false)
  const [batchImportVisible, setBatchImportVisible] = useState(false)
  const [importLinkVisible, setImportLinkVisible] = useState(false)
  const [editingNode, setEditingNode] = useState(null)
  const [selectedNodeIds, setSelectedNodeIds] = useState([])
  const [batchImportLinks, setBatchImportLinks] = useState('')
  const [enableAfterImport, setEnableAfterImport] = useState(true)
  const [checkingIP, setCheckingIP] = useState(false)
  const [exportVisible, setExportVisible] = useState(false)
  const [exportText, setExportText] = useState('')
  const [exportLoading, setExportLoading] = useState(false)
  const [replaceVisible, setReplaceVisible] = useState(false)
  const [replaceNode, setReplaceNode] = useState(null)
  const [replaceLink, setReplaceLink] = useState('')
  const [replaceUpdateName, setReplaceUpdateName] = useState(true)
  const [replaceLoading, setReplaceLoading] = useState(false)
  const [dedupLoading, setDedupLoading] = useState(false)
  const [autoCheckAfterCreate, setAutoCheckAfterCreate] = useState(false)
  const [expandedRowKeys, setExpandedRowKeys] = useState([])
  const [remarkSaving, setRemarkSaving] = useState({})
  const [remarkPanelKeys, setRemarkPanelKeys] = useState({})
  const nodesViewportRef = useRef(null)
  const tableContainerRef = useRef(null)
  const virtualDragMetaRef = useRef(null)
  const [virtualDragState, setVirtualDragState] = useState(null)
  const [tableBodyScrollY, setTableBodyScrollY] = useState(0)

  const selectedNodeIdSet = useMemo(() => new Set(selectedNodeIds), [selectedNodeIds])

  const dragSortInlineLimit = 300
  const virtualDragEnabled = nodes.length > dragSortInlineLimit
  const dragSortInTableEnabled = !virtualDragEnabled
  const virtualTableEnabled = virtualDragEnabled

  const nodeIndexMap = useMemo(() => {
    const map = new Map()
    for (let index = 0; index < nodes.length; index += 1) {
      const node = nodes[index]
      map.set(String(node.id), index)
    }
    return map
  }, [nodes])

  useEffect(() => {
    nodesRef.current = nodes
  }, [nodes])

  const cleanupVirtualDrag = useCallback(() => {
    const meta = virtualDragMetaRef.current
    if (!meta) {
      setVirtualDragState(null)
      return
    }

    if (meta.raf) {
      cancelAnimationFrame(meta.raf)
    }

    window.removeEventListener('pointermove', meta.onMove)
    window.removeEventListener('pointerup', meta.onUp)

    document.body.style.cursor = meta.prevCursor
    document.body.style.userSelect = meta.prevUserSelect

    virtualDragMetaRef.current = null
    setVirtualDragState(null)
  }, [])

  useEffect(() => cleanupVirtualDrag, [cleanupVirtualDrag])

  const reorderNodesByIndex = useCallback(
    async (sourceIndex, destinationIndex) => {
      const currentNodes = Array.from(nodesRef.current || [])
      if (
        sourceIndex === destinationIndex ||
        sourceIndex < 0 ||
        destinationIndex < 0 ||
        sourceIndex >= currentNodes.length ||
        destinationIndex >= currentNodes.length
      ) {
        return
      }

      const items = Array.from(currentNodes)
      const [reordered] = items.splice(sourceIndex, 1)
      items.splice(destinationIndex, 0, reordered)

      setNodes(items)

      try {
        const orderMap = items.map((node, index) => ({
          id: node.id,
          sort_order: index,
        }))
        await api.post('/nodes/reorder', { nodes: orderMap })
        message.success(t('nodes_reordered'))
        loadNodes()
      } catch (error) {
        message.error(t('server_error'))
        loadNodes()
      }
    },
    [t]
  )

  const reorderNodesById = useCallback(
    async (nodeId, destinationIndex) => {
      const currentNodes = Array.from(nodesRef.current || [])
      const sourceIndex = currentNodes.findIndex(
        (node) => String(node?.id) === String(nodeId)
      )
      await reorderNodesByIndex(sourceIndex, destinationIndex)
    },
    [reorderNodesByIndex]
  )

  const resolveTableScrollContainer = useCallback(() => {
    const root = tableContainerRef.current
    if (!root) return null
    const body = root.querySelector('.ant-table-body')
    if (body) return body

    const content = root.querySelector('.ant-table-content')
    if (content && content.scrollHeight > content.clientHeight) return content

    const container = root.querySelector('.ant-table-container')
    if (container && container.scrollHeight > container.clientHeight) return container

    const divs = root.querySelectorAll('div')
    for (const el of divs) {
      if (!el || el.scrollHeight <= el.clientHeight) continue
      try {
        const style = window.getComputedStyle(el)
        const overflowY = style?.overflowY
        if (overflowY === 'auto' || overflowY === 'scroll' || overflowY === 'overlay') {
          return el
        }
      } catch {
        // ignore and continue
      }
    }

    return container || content || null
  }, [])

  const recalcTableBodyScrollY = useCallback(() => {
    const viewport = nodesViewportRef.current
    const root = tableContainerRef.current
    if (!viewport || !root) return

    const viewportHeight = viewport.getBoundingClientRect().height
    if (!viewportHeight || viewportHeight <= 0) return

    const header =
      root.querySelector('.ant-table-header') ||
      root.querySelector('.ant-table-thead') ||
      root.querySelector('thead')
    const headerHeight = header ? header.getBoundingClientRect().height : 0

    const nextY = Math.max(0, Math.floor(viewportHeight - headerHeight - 2))
    setTableBodyScrollY((prev) => {
      if (Math.abs((prev || 0) - nextY) <= 1) return prev
      return nextY
    })
  }, [])

  useLayoutEffect(() => {
    const viewport = nodesViewportRef.current
    if (!viewport) return undefined

    let raf = null
    const schedule = () => {
      if (raf) cancelAnimationFrame(raf)
      raf = requestAnimationFrame(() => {
        raf = null
        recalcTableBodyScrollY()
      })
    }

    recalcTableBodyScrollY()

    let ro = null
    if (typeof ResizeObserver !== 'undefined') {
      ro = new ResizeObserver(schedule)
      ro.observe(viewport)

      const root = tableContainerRef.current
      const header =
        root?.querySelector?.('.ant-table-header') ||
        root?.querySelector?.('.ant-table-thead') ||
        root?.querySelector?.('thead')
      if (header) {
        ro.observe(header)
      }
    }

    window.addEventListener('resize', schedule, { passive: true })
    return () => {
      window.removeEventListener('resize', schedule)
      if (raf) cancelAnimationFrame(raf)
      ro?.disconnect?.()
    }
  }, [recalcTableBodyScrollY])

  const measureRowHeight = (scrollContainer) => {
    if (!scrollContainer) return 40
    const candidate =
      scrollContainer.querySelector('tr.ant-table-row[data-row-key]') ||
      scrollContainer.querySelector('.ant-table-row[data-row-key]')
    if (!candidate?.getBoundingClientRect) return 40
    const rect = candidate.getBoundingClientRect()
    const height = rect?.height
    return Number.isFinite(height) && height > 10 ? height : 40
  }

  const handleVirtualDragStart = useCallback(
    (event, recordId) => {
      if (!virtualDragEnabled) return
      if (nodesRef.current.length <= 1) return
      if (typeof event?.button === 'number' && event.button !== 0) return

      event.preventDefault?.()
      event.stopPropagation?.()

      cleanupVirtualDrag()

      const activeId = String(recordId)
      const sourceIndex = nodeIndexMap.get(activeId)
      if (sourceIndex === undefined) return

      const scrollContainer = resolveTableScrollContainer()
      if (!scrollContainer) {
        message.warning(t('server_error'))
        return
      }

      const rowHeight = measureRowHeight(scrollContainer)
      const itemCount = nodesRef.current.length
      const pointerId = typeof event.pointerId === 'number' ? event.pointerId : null
      const prevCursor = document.body.style.cursor
      const prevUserSelect = document.body.style.userSelect

      document.body.style.cursor = 'grabbing'
      document.body.style.userSelect = 'none'

      const meta = {
        activeId,
        sourceIndex,
        overIndex: sourceIndex,
        pointerId,
        scrollContainer,
        rowHeight,
        itemCount,
        prevCursor,
        prevUserSelect,
        raf: null,
        lastClientY: event.clientY,
        onMove: null,
        onUp: null,
      }

      const updateOverIndex = () => {
        const rect = meta.scrollContainer.getBoundingClientRect()
        const rawY = meta.lastClientY - rect.top
        const clampedY = Math.min(Math.max(rawY, 0), rect.height)

        const threshold = 44
        const scrollStep = 18
        if (clampedY < threshold) {
          meta.scrollContainer.scrollTop = Math.max(0, meta.scrollContainer.scrollTop - scrollStep)
        } else if (clampedY > rect.height - threshold) {
          meta.scrollContainer.scrollTop = Math.min(
            meta.scrollContainer.scrollHeight,
            meta.scrollContainer.scrollTop + scrollStep
          )
        }

        let nextIndex

        const root = tableContainerRef.current
        if (root) {
          const rowNodes = Array.from(
            root.querySelectorAll('tr.ant-table-row[data-row-key], .ant-table-row[data-row-key]')
          ).filter(
            (row) => !String(row?.className || '').includes('ant-table-expanded-row')
          )
          let bestIndex = null
          let bestDistance = Number.POSITIVE_INFINITY

          for (const row of rowNodes) {
            const rowKey = row?.getAttribute?.('data-row-key')
            if (!rowKey) continue
            const index = nodeIndexMap.get(String(rowKey))
            if (index === undefined) continue
            const rowRect = row.getBoundingClientRect()
            const centerY = rowRect.top + rowRect.height / 2
            const distance = Math.abs(meta.lastClientY - centerY)
            if (distance < bestDistance) {
              bestDistance = distance
              bestIndex = index
            }
          }

          if (bestIndex !== null) {
            nextIndex = bestIndex
          }
        }

        if (nextIndex === undefined) {
          nextIndex = Math.min(
            meta.itemCount - 1,
            Math.max(0, Math.floor((meta.scrollContainer.scrollTop + clampedY) / meta.rowHeight))
          )
        }

        if (nextIndex === meta.overIndex) return
        meta.overIndex = nextIndex
        setVirtualDragState((prev) => {
          if (!prev || prev.activeId !== meta.activeId) return prev
          return { ...prev, overIndex: nextIndex }
        })
      }

      meta.onMove = (moveEvent) => {
        if (meta.pointerId !== null && moveEvent.pointerId !== meta.pointerId) return
        moveEvent.preventDefault?.()
        meta.lastClientY = moveEvent.clientY
        if (meta.raf) return
        meta.raf = requestAnimationFrame(() => {
          meta.raf = null
          updateOverIndex()
        })
      }

      meta.onUp = async (upEvent) => {
        if (meta.pointerId !== null && upEvent.pointerId !== meta.pointerId) return
        upEvent.preventDefault?.()
        const destinationIndex = meta.overIndex
        const sourceIndexSnapshot = meta.sourceIndex
        cleanupVirtualDrag()
        if (destinationIndex === sourceIndexSnapshot) return
        await reorderNodesById(meta.activeId, destinationIndex)
      }

      virtualDragMetaRef.current = meta
      setVirtualDragState({
        activeId,
        sourceIndex,
        overIndex: sourceIndex,
      })

      window.addEventListener('pointermove', meta.onMove, { passive: false })
      window.addEventListener('pointerup', meta.onUp, { passive: false })
    },
    [cleanupVirtualDrag, nodeIndexMap, reorderNodesById, resolveTableScrollContainer, t, virtualDragEnabled]
  )

  useEffect(() => {
    const root = tableContainerRef.current
    if (!virtualDragEnabled || !root) return undefined

    const onPointerDown = (event) => {
      if (virtualDragMetaRef.current) return
      const target = event?.target
      const handle = target?.closest?.('[data-node-drag-id]')
      const nodeId = handle?.getAttribute?.('data-node-drag-id')
      if (!nodeId) return
      handleVirtualDragStart(event, nodeId)
    }

    root.addEventListener('pointerdown', onPointerDown, true)
    return () => {
      root.removeEventListener('pointerdown', onPointerDown, true)
    }
  }, [handleVirtualDragStart, virtualDragEnabled])

  useEffect(() => {
    loadNodes()
    loadVersion()
  }, [])

  const loadVersion = async () => {
    try {
      const response = await api.get('/version')
      const serverVersion = response.data?.version || ''
      const versionCheck = ensureLatestFrontendBuild(serverVersion)
      if (versionCheck.refreshed) {
        return
      }
      setAppVersion(serverVersion)
      if (versionCheck.mismatch) {
        message.warning(
          t('frontend_version_mismatch')
            .replace('{{server}}', versionCheck.serverVersion || '-')
            .replace('{{client}}', versionCheck.frontendVersion || '-')
        )
      }
    } catch {
      setAppVersion('')
    }
  }

  const loadNodes = async () => {
    setLoading(true)
    try {
      const response = await api.get('/nodes')
      const nextNodes = response.data || []
      setNodes(nextNodes)
      setExpandedRowKeys((prev) =>
        prev.filter((key) => nextNodes.some((n) => String(n.id) === String(key)))
      )
    } catch (error) {
      message.error(t('network_error'))
    } finally {
      setLoading(false)
    }
  }

  const handleLanguageChange = (lang) => {
    i18n.changeLanguage(lang)
    localStorage.setItem('language', lang)
    message.success(t('success'))
  }

  const handleCreateNode = () => {
    setAutoCheckAfterCreate(false)
    setEditingNode(null)
    setModalVisible(true)
  }

  const getOutboundSignature = (node) => {
    const proxyType = String(node?.type || '')
    const rawConfig = node?.config

    if (rawConfig === null || rawConfig === undefined) {
      return `${proxyType}|null`
    }

    if (typeof rawConfig !== 'string') {
      return `${proxyType}|${stableJSONStringify(rawConfig)}`
    }

    const trimmed = rawConfig.trim()
    if (!trimmed) {
      return `${proxyType}|{}`
    }

    try {
      const parsed = JSON.parse(trimmed)
      return `${proxyType}|${stableJSONStringify(parsed)}`
    } catch {
      return `${proxyType}|raw:${trimmed}`
    }
  }

  const computeDuplicateOutboundNodeIds = (scopeNodeIds) => {
    const scopeSet = new Set((scopeNodeIds || []).map((id) => String(id)))
    if (scopeSet.size === 0) {
      return []
    }

    const scopeNodes = nodes.filter((node) => scopeSet.has(String(node.id)))
    const seen = new Map()
    const duplicates = []

    for (const node of scopeNodes) {
      const signature = getOutboundSignature(node)
      if (seen.has(signature)) {
        duplicates.push(node.id)
        continue
      }
      seen.set(signature, node.id)
    }

    return duplicates
  }

  const handleDeduplicateOutbounds = () => {
    const duplicateIds = computeDuplicateOutboundNodeIds(selectedNodeIds)
    if (duplicateIds.length === 0) {
      message.info(t('outbound_dedup_no_duplicates'))
      return
    }

    Modal.confirm({
      title: t('outbound_dedup'),
      content: t('outbound_dedup_confirm').replace(
        '{{count}}',
        duplicateIds.length.toString()
      ),
      okText: t('confirm'),
      cancelText: t('cancel'),
      okButtonProps: { danger: true },
      onOk: async () => {
        setDedupLoading(true)
        try {
          await api.post('/nodes/batch-delete', { ids: duplicateIds })
          message.success(
            t('outbound_dedup_success').replace(
              '{{count}}',
              duplicateIds.length.toString()
            )
          )
          setSelectedNodeIds((prev) =>
            prev.filter((id) => !duplicateIds.includes(id))
          )
          await loadNodes()
        } catch (error) {
          message.error(error.response?.data?.error || t('server_error'))
        } finally {
          setDedupLoading(false)
        }
      },
    })
  }

  const applyNodeIPInfo = (id, ipInfo) => {
    if (id === null || id === undefined) return

    const nodeIP = typeof ipInfo?.ip === 'string' ? ipInfo.ip : ''
    const location = typeof ipInfo?.location === 'string' ? ipInfo.location : ''
    const countryCode =
      typeof ipInfo?.country_code === 'string' ? ipInfo.country_code : ''
    const latencyRaw = Number(ipInfo?.latency)
    const latency = Number.isFinite(latencyRaw) ? latencyRaw : 0

    setNodes((prev) =>
      prev.map((node) =>
        String(node.id) === String(id)
          ? {
              ...node,
              node_ip: nodeIP,
              location,
              country_code: countryCode,
              latency,
            }
          : node
      )
    )
  }

  const clearNodeIPInfo = (id) => {
    if (id === null || id === undefined) return
    setNodes((prev) =>
      prev.map((node) =>
        String(node.id) === String(id)
          ? {
              ...node,
              node_ip: '',
              location: '',
              country_code: '',
              latency: 0,
            }
          : node
      )
    )
  }

  const runNodeIPChecks = async (nodeIds, notificationKey) => {
    const ids = Array.from(new Set(nodeIds)).filter(
      (id) => id !== null && id !== undefined
    )
    if (ids.length === 0) return

    let completed = 0
    let failed = 0
    const total = ids.length

    const key = notificationKey
    notification.info({
      key,
      message: t('batch_check_ip_running')
        .replace('{{current}}', '0')
        .replace('{{total}}', total.toString()),
      duration: 0,
      icon: <ThunderboltOutlined style={{ color: '#1890ff' }} />,
    })

    const concurrency = Math.min(5, total)
    let nextIndex = 0

    const runWorker = async () => {
      while (true) {
        const currentIndex = nextIndex
        nextIndex += 1
        if (currentIndex >= ids.length) return

        const id = ids[currentIndex]
        try {
          const response = await api.get(`/nodes/${id}/check-ip`)
          applyNodeIPInfo(id, response.data)
          completed += 1
        } catch (error) {
          failed += 1
          const statusCode = error?.response?.status
          if (typeof statusCode === 'number' && statusCode >= 500) {
            clearNodeIPInfo(id)
          }
          const msg = error.response?.data?.error || t('server_error')
          message.error(`ID ${id}: ${msg}`)
        } finally {
          const done = completed + failed
          if (done < total) {
            notification.info({
              key,
              message: t('batch_check_ip_running')
                .replace('{{current}}', done.toString())
                .replace('{{total}}', total.toString()),
              duration: 0,
              icon: <ThunderboltOutlined style={{ color: '#1890ff' }} />,
            })
          }
        }
      }
    }

    await Promise.all(Array.from({ length: concurrency }, () => runWorker()))

    notification.destroy(key)
    if (completed > 0) {
      notification.success({
        message: t('batch_check_ip_success').replace('{{count}}', completed.toString()),
        duration: 3,
      })
    }
    if (failed > 0) {
      notification.warning({
        message: `${failed} ${t('status_unverified')}`,
        duration: 4,
      })
    }

    loadNodes()
  }

  const handleImportLink = async (link) => {
    try {
      message.loading({ content: t('loading'), key: 'parselink' })
      const response = await api.post('/parse-link', { link })
      const { type, name, config } = response.data

      const parsedConfig = typeof config === 'string' ? JSON.parse(config) : config

      message.success({ content: t('success'), key: 'parselink' })
      setAutoCheckAfterCreate(true)

      setEditingNode({
        name,
        type,
        config: JSON.stringify(parsedConfig),
        enabled: true,
      })
      setImportLinkVisible(false)
      setModalVisible(true)
    } catch (error) {
      message.error({
        content: error.response?.data?.error || t('invalid_link'),
        key: 'parselink',
      })
    }
  }

  const handleBatchImport = async () => {
    const content = (batchImportLinks || '').trim()

    if (!content) {
      message.error(t('invalid_request'))
      return
    }

    try {
      setLoading(true)
      message.loading({ content: t('loading'), key: 'batchimport' })

      const response = await api.post('/nodes/batch-import', {
        content,
        enabled: enableAfterImport,
      })

      const { success = 0, failed = 0, results = [] } = response.data || {}
      const importedIds = results
        .filter((r) => r?.success && r?.id)
        .map((r) => Number(r.id))
        .filter((id) => Number.isFinite(id))

      if (success > 0) {
        message.success({
          content: `${t('import_success').replace('{{count}}', success)}`,
          key: 'batchimport',
          duration: 3,
        })
      }

      if (failed > 0) {
        message.warning({
          content: `${t('import_failed').replace('{{count}}', failed)}`,
          duration: 5,
        })
      }

      setBatchImportVisible(false)
      setBatchImportLinks('')
      loadNodes()

      if (enableAfterImport && importedIds.length > 0) {
        setCheckingIP(true)
        runNodeIPChecks(importedIds, 'import-check-ip').finally(() => {
          setCheckingIP(false)
        })
      }
    } catch (error) {
      message.error({
        content: error.response?.data?.error || t('server_error'),
        key: 'batchimport',
      })
    } finally {
      setLoading(false)
    }
  }

  const handleEditNode = (node) => {
    setAutoCheckAfterCreate(false)
    setEditingNode({
      ...node,
      config: node.config
    })
    setModalVisible(true)
  }

  const handleDeleteNode = async (id) => {
    try {
      await api.delete(`/nodes/${id}`)
      message.success(t('node_deleted'))
      loadNodes()
    } catch (error) {
      message.error(t('server_error'))
    }
  }

  const handleBatchDelete = async () => {
    if (selectedNodeIds.length === 0) {
      message.warning(t('select_nodes'))
      return
    }

    try {
      setLoading(true)
      // Use batch delete API - only restarts sing-box once
      await api.post('/nodes/batch-delete', { ids: selectedNodeIds })
      message.success(t('batch_delete_success').replace('{{count}}', selectedNodeIds.length))
      setSelectedNodeIds([])
      loadNodes()
    } catch (error) {
      message.error(t('server_error'))
    } finally {
      setLoading(false)
    }
  }

    const handleBatchCheckIP = async () => {
      if (selectedNodeIds.length === 0) {
        message.warning(t('select_nodes'))
        return
      }

      setCheckingIP(true)
      try {
        await runNodeIPChecks(selectedNodeIds, 'batch-check-ip')
      } finally {
        setCheckingIP(false)
      }
    }

  const copyToClipboard = async (text) => {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      message.success(t('copied'))
    } catch {
      try {
        const textarea = document.createElement('textarea')
        textarea.value = text
        textarea.style.position = 'fixed'
        textarea.style.opacity = '0'
        document.body.appendChild(textarea)
        textarea.focus()
        textarea.select()
        document.execCommand('copy')
        document.body.removeChild(textarea)
        message.success(t('copied'))
      } catch {
        message.error(t('copy_failed'))
      }
    }
  }

  const handleExportNode = async (node) => {
    try {
      setExportLoading(true)
      const response = await api.get(`/nodes/${node.id}/export`)
      const link = response.data?.link || ''
      setExportText(link)
      setExportVisible(true)
    } catch (error) {
      message.error(error.response?.data?.error || t('server_error'))
    } finally {
      setExportLoading(false)
    }
  }

  const handleBatchExport = async () => {
    if (selectedNodeIds.length === 0) {
      message.warning(t('select_nodes'))
      return
    }

    try {
      setExportLoading(true)
      const response = await api.post('/nodes/batch-export', { ids: selectedNodeIds })
      const results = response.data?.results || []
      const links = results
        .filter((r) => r.success && r.link)
        .map((r) => r.link)
      const failedCount = (response.data?.failed ?? 0)
      if (failedCount > 0) {
        message.warning(t('export_partial').replace('{{count}}', failedCount.toString()))
      }
      setExportText(links.join('\n'))
      setExportVisible(true)
    } catch (error) {
      message.error(error.response?.data?.error || t('server_error'))
    } finally {
      setExportLoading(false)
    }
  }

  const openReplaceModal = (node) => {
    setReplaceNode(node)
    setReplaceLink('')
    setReplaceUpdateName(true)
    setReplaceVisible(true)
  }

  const handleConfirmReplace = async () => {
    if (!replaceNode?.id) return
    if (!replaceLink.trim()) {
      message.warning(t('enter_share_link'))
      return
    }

    try {
      setReplaceLoading(true)
      await api.put(`/nodes/${replaceNode.id}/replace`, {
        link: replaceLink.trim(),
        update_name: replaceUpdateName,
      })
      message.success(t('node_replaced'))
      setReplaceVisible(false)
      setReplaceNode(null)
      setReplaceLink('')
      loadNodes()
    } catch (error) {
      message.error(error.response?.data?.error || t('server_error'))
    } finally {
      setReplaceLoading(false)
    }
  }

  const handleSaveNode = async (values) => {
    const isEditing = !!editingNode?.id
    const shouldAutoCheck =
      !isEditing && autoCheckAfterCreate && values.enabled !== false

    try {
      let createdId

      if (isEditing) {
        await api.put(`/nodes/${editingNode.id}`, values)
        message.success(t('node_updated'))
      } else {
        const response = await api.post('/nodes', values)
        createdId = response.data?.id
        message.success(t('node_created'))
      }
      setModalVisible(false)
      setEditingNode(null)
      setAutoCheckAfterCreate(false)
      loadNodes()

      if (shouldAutoCheck && createdId) {
        setCheckingIP(true)
        runNodeIPChecks([createdId], `import-check-ip-${createdId}`).finally(() => {
          setCheckingIP(false)
        })
      }
    } catch (error) {
      message.error(error.response?.data?.error || t('server_error'))
    }
  }

  const handleToggleNode = async (node) => {
    try {
      await api.put(`/nodes/${node.id}`, {
        ...node,
        enabled: !node.enabled,
      })
      message.success(t('success'))
      loadNodes()
    } catch (error) {
      message.error(t('server_error'))
    }
  }

  const handleToggleTCPReuse = async (node) => {
    const nextTCPReuseEnabled = node?.tcp_reuse_enabled === false

    if (nextTCPReuseEnabled && (!node?.username || !node?.password)) {
      message.warning(t('tcp_reuse_auth_required_hint'))
    }

    try {
      await api.put(`/nodes/${node.id}`, {
        ...node,
        tcp_reuse_enabled: nextTCPReuseEnabled,
      })
      message.success(t('success'))
      loadNodes()
    } catch (error) {
      message.error(error.response?.data?.error || t('server_error'))
      loadNodes()
    }
  }

  const handleDragEnd = async (result) => {
    if (!result.destination) return
    await reorderNodesByIndex(result.source.index, result.destination.index)
  }

    const handleBatchSetAuth = async (auth) => {
      try {
        await api.post('/nodes/batch-auth', {
          node_ids: selectedNodeIds,
          ...auth,
        })
        message.success(t('auth_updated'))
        setBatchAuthVisible(false)
        loadNodes()
      } catch (error) {
        message.error(t('server_error'))
      }
    }

    const handleSaveRemark = async (record, draft) => {
      if (!record?.id) return

      const original = record.remark ?? ''
      if (draft === original) return

      setRemarkSaving((prev) => ({ ...prev, [record.id]: true }))
      try {
        await api.put(`/nodes/${record.id}/remark`, { remark: draft })
        message.success(t('success'))
        setNodes((prev) =>
          prev.map((n) => (n.id === record.id ? { ...n, remark: draft } : n))
        )
      } catch (error) {
        message.error(error.response?.data?.error || t('server_error'))
      } finally {
        setRemarkSaving((prev) => ({ ...prev, [record.id]: false }))
      }
    }

    const expandedRowRender = (record) => {
      const saving = !!remarkSaving[record.id]
      const activeKeys = remarkPanelKeys[record.id] ?? []

      const statusText =
        record.node_ip && record.latency > 0
          ? t('status_healthy')
          : t('status_unverified')

      return (
        <Collapse
          size="small"
          activeKey={activeKeys}
          onChange={(keys) => {
            const nextKeys = Array.isArray(keys) ? keys : keys ? [keys] : []
            setRemarkPanelKeys((prev) => ({
              ...prev,
              [record.id]: nextKeys,
            }))
          }}
          items={[
            {
              key: 'record',
              label: t('node_record'),
              children: (
                <Descriptions
                  size="small"
                  column={2}
                  items={[
                    {
                      key: 'port',
                      label: t('inbound_port'),
                      children: record.inbound_port ?? '-',
                    },
                    {
                      key: 'ip',
                      label: t('node_ip'),
                      children: record.node_ip || '-',
                    },
                    {
                      key: 'location',
                      label: t('location'),
                      children: record.location || '-',
                    },
                    {
                      key: 'latency',
                      label: t('latency'),
                      children: record.latency > 0 ? `${record.latency}ms` : '-',
                    },
                    {
                      key: 'username',
                      label: t('username'),
                      children: record.username || '-',
                    },
                    {
                      key: 'password',
                      label: t('password_auth'),
                      children: record.password || '-',
                    },
                    {
                      key: 'tcp_reuse',
                      label: t('tcp_reuse'),
                      children:
                        record.tcp_reuse_enabled === false
                          ? t('disabled')
                          : t('enabled'),
                    },
                    {
                      key: 'status',
                      label: t('status'),
                      children: statusText,
                    },
                  ]}
                />
              ),
            },
            {
              key: 'remark',
              label: t('remark'),
              children: (
                <NodeRemarkEditor
                  record={record}
                  saving={saving}
                  onSave={handleSaveRemark}
                  t={t}
                />
              ),
            },
          ]}
        />
      )
    }

    const columns = [
      {
        title: '',
        key: 'drag',
        width: 34,
        render: (_, record) => {
          const disabled = nodes.length <= 1
          const isDragging = virtualDragState?.activeId === String(record?.id)
          const cursor = disabled
            ? 'not-allowed'
            : isDragging
              ? 'grabbing'
              : 'grab'
          const color = disabled ? '#ccc' : '#999'

          const handle = (
            <span
              data-testid={`node-drag-handle-${String(record?.id ?? '')}`}
              data-node-drag-id={String(record?.id ?? '')}
              title={t('drag_sort_hint')}
              style={{
                display: 'inline-flex',
                width: '100%',
                height: '100%',
                alignItems: 'center',
                justifyContent: 'center',
                cursor,
                color,
              }}
            >
              <HolderOutlined />
            </span>
          )

          return handle
        },
      },
      {
        title: <Checkbox
          checked={selectedNodeIds.length === nodes.length && nodes.length > 0}
          indeterminate={selectedNodeIds.length > 0 && selectedNodeIds.length < nodes.length}
          onChange={(e) => {
            if (e.target.checked) {
              setSelectedNodeIds(nodes.map((node) => node.id))
            } else {
              setSelectedNodeIds([])
            }
          }}
        />,
        key: 'checkbox',
        width: 42,
        render: (_, record) => (
          <Checkbox
            checked={selectedNodeIdSet.has(record.id)}
            onChange={(e) => {
              const checked = e.target.checked
              setSelectedNodeIds((prev) => {
                if (checked) {
                  if (prev.includes(record.id)) return prev
                  return [...prev, record.id]
                }
                return prev.filter((id) => id !== record.id)
              })
            }}
          />
        ),
      },
      {
        title: t('node_name'),
        dataIndex: 'name',
        key: 'name',
        ellipsis: true,
        width: 110,
      },
      {
        title: t('remark'),
        key: 'remark_indicator',
        width: 56,
        render: (_, record) => {
          const remark = (record?.remark ?? '').trim()
          if (!remark) return null
          const preview = remark.length > 200 ? `${remark.slice(0, 200)}...` : remark
          return (
            <Tooltip title={preview}>
              <Tag color="gold">{t('remark')}</Tag>
            </Tooltip>
          )
        },
      },
      {
        title: t('node_type'),
        dataIndex: 'type',
        key: 'type',
        width: 70,
        render: (type) => <Tag color="blue">{type.toUpperCase()}</Tag>,
      },
      {
        title: t('inbound_port'),
        dataIndex: 'inbound_port',
        key: 'inbound_port',
        width: 72,
      },
      {
        title: t('username'),
        dataIndex: 'username',
        key: 'username',
        width: 76,
        render: (username) => username || '-',
      },
      {
        title: t('password_auth'),
        dataIndex: 'password',
        key: 'password',
        width: 76,
        render: (password) => password || '-',
      },
      {
        title: t('status'),
        key: 'status',
        width: 84,
        render: (_, record) => {
          const healthy = record.node_ip && record.latency > 0
          return (
            <Tag color={healthy ? 'green' : 'red'}>
              {healthy ? t('status_healthy') : t('status_unverified')}
            </Tag>
          )
        },
      },
      {
        title: t('latency'),
        dataIndex: 'latency',
        key: 'latency',
        width: 72,
        render: (latency) => latency > 0 ? `${latency}ms` : '-',
      },
      {
        title: t('node_ip'),
        dataIndex: 'node_ip',
        key: 'node_ip',
        width: 86,
        render: (nodeIP) => nodeIP || '-',
      },
      {
        title: t('country_code'),
        dataIndex: 'country_code',
        key: 'country_code',
        width: 60,
        render: (countryCode) => normalizeCountryCode(countryCode) || '-',
      },
      {
        title: t('location'),
        dataIndex: 'location',
        key: 'location',
        width: 96,
        render: (_, record) => {
          const label = formatCountryWithCode(record)
          return (
            <Tooltip title={label}>
              <Text ellipsis style={{ maxWidth: 90 }}>
                {label}
              </Text>
            </Tooltip>
          )
        },
      },
      {
        title: t('enabled'),
        dataIndex: 'enabled',
        key: 'enabled',
        width: 68,
        render: (_, record) => (
          <Switch
            checked={record.enabled}
            onChange={() => handleToggleNode(record)}
            checkedChildren={<CheckCircleOutlined />}
            unCheckedChildren={<CloseCircleOutlined />}
          />
        ),
      },
      {
        title: t('tcp_reuse'),
        key: 'tcp_reuse',
        width: 68,
        render: (_, record) => (
          <Switch
            checked={record.tcp_reuse_enabled !== false}
            onChange={() => handleToggleTCPReuse(record)}
            checkedChildren={t('tcp_reuse_short')}
            unCheckedChildren={t('tcp_reuse_short')}
          />
        ),
      },
      {
        title: t('actions'),
        key: 'actions',
        width: 96,
        render: (_, record) => (
          <Space size={[4, 4]} wrap>
            <Tooltip title={t('export')}>
              <Button
                type="link"
                size="small"
                icon={<CopyOutlined />}
                loading={exportLoading}
                onClick={() => handleExportNode(record)}
              />
            </Tooltip>
            <Tooltip title={t('replace')}>
              <Button
                type="link"
                size="small"
                icon={<SwapOutlined />}
                onClick={() => openReplaceModal(record)}
              />
            </Tooltip>
            <Tooltip title={t('edit')}>
              <Button
                type="link"
                size="small"
                icon={<EditOutlined />}
                onClick={() => handleEditNode(record)}
              />
            </Tooltip>
            <Popconfirm
              title={t('confirm')}
              onConfirm={() => handleDeleteNode(record.id)}
              okText={t('confirm')}
              cancelText={t('cancel')}
            >
              <Button type="link" size="small" danger icon={<DeleteOutlined />} />
            </Popconfirm>
          </Space>
        ),
      },
    ]

    const DraggableRow = (props) => {
      const rowKey = props['data-row-key']
      const { className, style, ...restProps } = props

      if (className?.includes('ant-table-expanded-row')) {
        return (
          <tr className={className} style={style} {...restProps}>
            {props.children}
          </tr>
        )
      }

      const index = nodeIndexMap.get(String(rowKey))
      if (index === undefined) {
        return (
          <tr className={className} style={style} {...restProps}>
            {props.children}
          </tr>
        )
      }

      return (
        <Draggable draggableId={`node-${String(rowKey)}`} index={index}>
          {(provided, snapshot) => (
            <tr
              ref={provided.innerRef}
              {...restProps}
              {...provided.draggableProps}
              {...provided.dragHandleProps}
              className={className}
              style={{
                ...style,
                ...provided.draggableProps.style,
                background: snapshot.isDragging ? '#e6f7ff' : style?.background,
                cursor: 'grab',
              }}
            >
              {props.children}
            </tr>
          )}
        </Draggable>
      )
    }

    const DraggableBody = (props) => (
      <Droppable droppableId="table-body">
        {(provided) => (
          <tbody ref={provided.innerRef} {...provided.droppableProps} {...props}>
            {props.children}
            {provided.placeholder}
          </tbody>
        )}
      </Droppable>
    )

    const table = (
      <div
        ref={tableContainerRef}
        data-testid="nodes-table-container"
        className="dashboard-nodes-table"
      >
        <Table
          columns={columns}
          dataSource={nodes}
          rowKey="id"
          size="small"
          virtual={virtualTableEnabled}
          listItemHeight={40}
          scroll={{ y: Math.max(0, tableBodyScrollY || 0) }}
          expandable={{
            expandedRowRender,
            expandedRowKeys,
            onExpandedRowsChange: (keys) => setExpandedRowKeys(keys),
          }}
          rowClassName={(record) => {
            if (!virtualDragEnabled || !virtualDragState) return ''
            const recordId = String(record?.id)
            if (recordId === virtualDragState.activeId) {
              return 'sbpm-virtual-drag-active'
            }
            const index = nodeIndexMap.get(recordId)
            if (index === virtualDragState.overIndex) {
              return 'sbpm-virtual-drag-over'
            }
            return ''
          }}
          loading={loading}
          pagination={false}
          locale={{
            emptyText: t('no_nodes'),
          }}
          components={
            dragSortInTableEnabled
              ? {
                  body: {
                    wrapper: DraggableBody,
                    row: DraggableRow,
                  },
                }
              : undefined
          }
        />
      </div>
    )

    const tableView = dragSortInTableEnabled ? (
      <DragDropContext onDragEnd={handleDragEnd}>
        {table}
      </DragDropContext>
    ) : (
      table
    )

	    return (
	      <Layout className="dashboard-layout">
	        <Header className="dashboard-header">
	          <div className="dashboard-brand">
	            <span className="dashboard-logo-shell">
	              <img
	                src="/logo.svg"
	                alt="SingBox Proxy Manager"
	                className="dashboard-logo"
	              />
	            </span>
	            <Title level={3} className="dashboard-title">
	              {t('app_title')}
	            </Title>
	            <Tooltip
	              title={`${t('frontend_build')}: ${frontendBuildVersion} (${frontendBuildFingerprint})`}
	            >
	              <Tag color="blue">
	                {t('version')} {appVersion || '-'}
	              </Tag>
	            </Tooltip>
	          </div>
	          <Space className="dashboard-actions">
	            <Select
	              value={i18n.language}
	              onChange={handleLanguageChange}
	              style={{ width: 120 }}
	              options={[
	                { value: 'zh', label: '中文' },
	                { value: 'en', label: 'English' },
	              ]}
	            />
	            <Button
	              icon={<SettingOutlined />}
	              onClick={() => setSettingsVisible(true)}
	            >
	              {t('settings')}
	            </Button>
	            <Button
	              icon={<LogoutOutlined />}
	              onClick={onLogout}
	              danger
	            >
	              {t('logout')}
	            </Button>
	          </Space>
	        </Header>

      <Content className="dashboard-content">
        <div className="dashboard-toolbar">
          <Space wrap>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={handleCreateNode}
            >
              {t('add_node')}
            </Button>
            <Button
              icon={<ImportOutlined />}
              onClick={() => setImportLinkVisible(true)}
            >
              {t('import_node')}
            </Button>
            <Button
              icon={<ImportOutlined />}
              onClick={() => setBatchImportVisible(true)}
            >
              {t('batch_import')}
            </Button>
            <Button
              icon={<ReloadOutlined />}
              onClick={loadNodes}
              loading={loading}
            >
              {t('refresh')}
            </Button>
          </Space>

          {selectedNodeIds.length > 0 && (
            <Space wrap>
              <Tag color="blue">{t('selected_count').replace('{{count}}', selectedNodeIds.length)}</Tag>
              <Button
                icon={<ExportOutlined />}
                onClick={handleBatchExport}
                loading={exportLoading}
              >
                {t('batch_export')}
              </Button>
              <Button
                icon={<ThunderboltOutlined />}
                onClick={handleBatchCheckIP}
                loading={checkingIP}
              >
                {t('batch_check_ip')}
              </Button>
              <Button
                onClick={() => setBatchAuthVisible(true)}
              >
                {t('set_auth')}
              </Button>
              <Button
                icon={<FilterOutlined />}
                onClick={handleDeduplicateOutbounds}
                loading={dedupLoading}
                disabled={dedupLoading}
              >
                {t('outbound_dedup')}
              </Button>
              <Popconfirm
                title={t('batch_delete_confirm').replace('{{count}}', selectedNodeIds.length)}
                onConfirm={handleBatchDelete}
                okText={t('confirm')}
                cancelText={t('cancel')}
              >
                <Button danger icon={<DeleteOutlined />}>
                  {t('batch_delete')}
                </Button>
              </Popconfirm>
            </Space>
          )}
        </div>

        <div ref={nodesViewportRef} className="dashboard-nodes-viewport">
          {tableView}
        </div>
      </Content>

      <Modal
        title={editingNode?.id ? t('edit') : t('add_node')}
        open={modalVisible}
        destroyOnHidden
        onCancel={() => {
          setModalVisible(false)
          setEditingNode(null)
          setAutoCheckAfterCreate(false)
        }}
        footer={null}
        width={800}
      >
        <NodeForm
          node={editingNode}
          onSave={handleSaveNode}
          onCancel={() => {
            setModalVisible(false)
            setEditingNode(null)
            setAutoCheckAfterCreate(false)
          }}
        />
      </Modal>

      <Modal
        title={t('import_node')}
        open={importLinkVisible}
        onCancel={() => setImportLinkVisible(false)}
        onOk={() => {
          const link = document.getElementById('import-link-input').value
          handleImportLink(link)
        }}
        okText={t('parse_link')}
        cancelText={t('cancel')}
      >
        <Input
          id="import-link-input"
          placeholder={t('enter_share_link')}
          allowClear
        />
      </Modal>

      <Modal
        title={t('batch_import_title')}
        open={batchImportVisible}
        onCancel={() => setBatchImportVisible(false)}
        onOk={handleBatchImport}
        okText={t('confirm')}
        cancelText={t('cancel')}
        width={700}
      >
        <Space direction="vertical" style={{ width: '100%' }} size="middle">
          <div>{t('batch_import_desc')}</div>
          <TextArea
            rows={10}
            placeholder={t('paste_links')}
            value={batchImportLinks}
            onChange={(e) => setBatchImportLinks(e.target.value)}
          />
          <Checkbox
            checked={enableAfterImport}
            onChange={(e) => setEnableAfterImport(e.target.checked)}
          >
            {t('enable_after_import')}
          </Checkbox>
        </Space>
      </Modal>

      <Modal
        title={t('export')}
        open={exportVisible}
        onCancel={() => setExportVisible(false)}
        onOk={() => copyToClipboard(exportText)}
        okText={t('copy')}
        cancelText={t('cancel')}
        okButtonProps={{ disabled: !exportText }}
        width={700}
      >
        <TextArea
          rows={10}
          value={exportText}
          readOnly
        />
      </Modal>

      <Modal
        title={t('replace')}
        open={replaceVisible}
        onCancel={() => {
          setReplaceVisible(false)
          setReplaceNode(null)
          setReplaceLink('')
        }}
        onOk={handleConfirmReplace}
        okText={t('confirm')}
        cancelText={t('cancel')}
        confirmLoading={replaceLoading}
        width={700}
      >
        <Space direction="vertical" style={{ width: '100%' }} size="middle">
          <div>{t('replace_desc')}</div>
          <Input
            placeholder={t('enter_share_link')}
            value={replaceLink}
            onChange={(e) => setReplaceLink(e.target.value)}
            allowClear
          />
          <Checkbox
            checked={replaceUpdateName}
            onChange={(e) => setReplaceUpdateName(e.target.checked)}
          >
            {t('replace_update_name')}
          </Checkbox>
        </Space>
      </Modal>

      <Modal
        title={t('settings')}
        open={settingsVisible}
        onCancel={() => setSettingsVisible(false)}
        footer={null}
        width={600}
      >
        <SettingsForm onClose={() => setSettingsVisible(false)} />
      </Modal>

      <BatchAuthModal
        visible={batchAuthVisible}
        selectedNodes={nodes.filter((node) => selectedNodeIdSet.has(node.id))}
        onClose={() => setBatchAuthVisible(false)}
        onSave={handleBatchSetAuth}
      />
    </Layout>
  )
}

export default Dashboard
