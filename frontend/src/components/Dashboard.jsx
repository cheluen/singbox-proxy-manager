import React, { useState, useEffect } from 'react'
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
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { DragDropContext, Droppable, Draggable } from 'react-beautiful-dnd'
import api from '../utils/api'
import NodeForm from './NodeForm'
import SettingsForm from './SettingsForm'
import BatchAuthModal from './BatchAuthModal'

const { Header, Content } = Layout
const { Title } = Typography
const { TextArea } = Input

// Get country flag emoji from country name or code
const getCountryFlag = (location) => {
  if (!location) return ''
  
  const countryFlags = {
    'Japan': '🇯🇵',
    'United States': '🇺🇸',
    'USA': '🇺🇸',
    'China': '🇨🇳',
    'Hong Kong': '🇭🇰',
    'Taiwan': '🇨🇳',
    'Singapore': '🇸🇬',
    'Korea': '🇰🇷',
    'South Korea': '🇰🇷',
    'Germany': '🇩🇪',
    'United Kingdom': '🇬🇧',
    'UK': '🇬🇧',
    'France': '🇫🇷',
    'Canada': '🇨🇦',
    'Australia': '🇦🇺',
    'Russia': '🇷🇺',
    'Netherlands': '🇳🇱',
    'Brazil': '🇧🇷',
    'India': '🇮🇳',
  }

  for (const [country, flag] of Object.entries(countryFlags)) {
    if (location.includes(country)) {
      return flag
    }
  }
  
  return '🌐'
}

// Extract country name from location
const getCountryName = (location) => {
  if (!location) return ''
  const parts = location.split(',').map(s => s.trim())
  return parts.length > 1 ? parts[parts.length - 1] : location
}

  function Dashboard({ onLogout }) {
    const { t, i18n } = useTranslation()
    const [nodes, setNodes] = useState([])
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
    const [autoCheckAfterCreate, setAutoCheckAfterCreate] = useState(false)
    const [remarkDrafts, setRemarkDrafts] = useState({})
    const [remarkSaving, setRemarkSaving] = useState({})

  useEffect(() => {
    loadNodes()
    loadVersion()
  }, [])

  const loadVersion = async () => {
    try {
      const response = await api.get('/version')
      setAppVersion(response.data?.version || '')
    } catch {
      setAppVersion('')
    }
  }

  const loadNodes = async () => {
    setLoading(true)
    try {
      const response = await api.get('/nodes')
      setNodes(response.data || [])
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
          await api.get(`/nodes/${id}/check-ip`)
          completed += 1
        } catch (error) {
          failed += 1
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
    const links = batchImportLinks.split('\n').filter((link) => link.trim())

    if (links.length === 0) {
      message.error(t('invalid_request'))
      return
    }

    try {
      setLoading(true)
      message.loading({ content: t('loading'), key: 'batchimport' })

      const response = await api.post('/nodes/batch-import', {
        links,
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

  const handleDragEnd = async (result) => {
    if (!result.destination) return

    const items = Array.from(nodes)
    const [reordered] = items.splice(result.source.index, 1)
    items.splice(result.destination.index, 0, reordered)

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

    const getRemarkDraft = (record) => {
      if (!record?.id) return ''
      return remarkDrafts[record.id] ?? record.remark ?? ''
    }

    const setRemarkDraft = (id, value) => {
      setRemarkDrafts((prev) => ({
        ...prev,
        [id]: value,
      }))
    }

    const resetRemarkDraft = (record) => {
      if (!record?.id) return
      setRemarkDrafts((prev) => ({
        ...prev,
        [record.id]: record.remark ?? '',
      }))
    }

    const handleSaveRemark = async (record) => {
      if (!record?.id) return

      const draft = getRemarkDraft(record)
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
      const draft = getRemarkDraft(record)
      const original = record.remark ?? ''
      const saving = !!remarkSaving[record.id]

      const statusText =
        record.node_ip && record.latency > 0
          ? t('status_healthy')
          : t('status_unverified')

      return (
        <Collapse
          size="small"
          items={[
            {
              key: 'record',
              label: t('node_record'),
              children: (
                <Descriptions
                  size="small"
                  column={3}
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
                <Space direction="vertical" style={{ width: '100%' }} size="middle">
                  <TextArea
                    value={draft}
                    rows={3}
                    placeholder={t('remark_placeholder')}
                    onChange={(e) => setRemarkDraft(record.id, e.target.value)}
                  />
                  <Space>
                    <Button
                      type="primary"
                      onClick={() => handleSaveRemark(record)}
                      loading={saving}
                      disabled={saving || draft === original}
                    >
                      {t('save')}
                    </Button>
                    <Button
                      onClick={() => resetRemarkDraft(record)}
                      disabled={saving || draft === original}
                    >
                      {t('reset')}
                    </Button>
                  </Space>
                </Space>
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
      width: 40,
      render: () => <HolderOutlined style={{ cursor: 'grab', color: '#999' }} />,
    },
    {
      title: <Checkbox
        checked={selectedNodeIds.length === nodes.length && nodes.length > 0}
        indeterminate={selectedNodeIds.length > 0 && selectedNodeIds.length < nodes.length}
        onChange={(e) => {
          if (e.target.checked) {
            setSelectedNodeIds(nodes.map(n => n.id))
          } else {
            setSelectedNodeIds([])
          }
        }}
      />,
      key: 'checkbox',
      width: 50,
      render: (_, record) => (
        <Checkbox
          checked={selectedNodeIds.includes(record.id)}
          onChange={(e) => {
            if (e.target.checked) {
              setSelectedNodeIds([...selectedNodeIds, record.id])
            } else {
              setSelectedNodeIds(selectedNodeIds.filter(id => id !== record.id))
            }
          }}
        />
      ),
    },
      {
        title: t('node_name'),
        dataIndex: 'name',
        key: 'name',
        ellipsis: true,
        width: 200,
      },
      {
        title: t('node_type'),
        dataIndex: 'type',
        key: 'type',
      width: 100,
      render: (type) => <Tag color="blue">{type.toUpperCase()}</Tag>,
    },
    {
      title: t('inbound_port'),
      dataIndex: 'inbound_port',
      key: 'inbound_port',
      width: 100,
    },
    {
      title: t('username'),
      dataIndex: 'username',
      key: 'username',
      width: 120,
      render: (username) => username || '-',
    },
    {
      title: t('password_auth'),
      dataIndex: 'password',
      key: 'password',
      width: 120,
      render: (password) => password || '-',
    },
    {
      title: t('node_ip'),
      dataIndex: 'node_ip',
      key: 'node_ip',
      width: 140,
      render: (ip) => ip || '-',
    },
    {
      title: t('location'),
      dataIndex: 'location',
      key: 'location',
      width: 180,
      render: (location, record) => {
        if (!location) return '-'
        
        // Get country code from API response (stored in country_code field)
        const countryCode = record.country_code || ''
        
        // Extract city and country from location string
        // Format examples: "Tokyo, Japan" or "Japan"
        const parts = location.split(',').map(s => s.trim())
        let city = ''
        let country = ''
        
        if (parts.length > 1) {
          city = parts[0]
          country = parts[parts.length - 1]
        } else {
          country = parts[0]
        }
        
        return (
          <span>
            {countryCode ? (
              // Show country code (abbreviation) prominently, then country name and city normally
              <>
                <span style={{ fontSize: '15px', fontWeight: 600, color: '#1890ff' }}>{countryCode}</span>
                {' '}
                <span style={{ fontSize: '13px', color: '#666' }}>
                  {country}
                  {city && `(${city})`}
                </span>
              </>
            ) : (
              // Fallback: show full location if no code available
              <span style={{ fontSize: '13px', color: '#666' }}>{location}</span>
            )}
          </span>
        )
      },
    },
    {
      title: t('latency'),
      dataIndex: 'latency',
      key: 'latency',
      width: 100,
      render: (latency) => latency > 0 ? `${latency}ms` : '-',
    },
    {
      title: t('status'),
      key: 'status',
      width: 120,
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
      title: t('enabled'),
      dataIndex: 'enabled',
      key: 'enabled',
      width: 80,
      render: (enabled, record) => (
        <Switch
          checked={enabled}
          onChange={() => handleToggleNode(record)}
          checkedChildren={<CheckCircleOutlined />}
          unCheckedChildren={<CloseCircleOutlined />}
        />
      ),
    },
    {
      title: t('actions'),
      key: 'actions',
      width: 140,
      fixed: 'right',
      render: (_, record) => (
        <Space size="small">
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

      const index = nodes.findIndex((item) => String(item.id) === String(rowKey))
      if (index === -1) {
        return (
          <tr className={className} style={style} {...restProps}>
            {props.children}
          </tr>
        )
      }

      return (
        <Draggable draggableId={`node-${String(rowKey)}`} index={index}>
          {(provided, snapshot) => {
            const children = React.Children.toArray(props.children)
            const dragCellIndex = children.findIndex(
              (child) => child?.props?.['data-col-key'] === 'drag'
            )
            if (dragCellIndex >= 0) {
              const dragCell = children[dragCellIndex]
              children[dragCellIndex] = React.cloneElement(dragCell, {
                ...dragCell.props,
                ...provided.dragHandleProps,
                style: {
                  ...dragCell.props?.style,
                  cursor: 'grab',
                },
              })
            }

            return (
              <tr
                ref={provided.innerRef}
                {...restProps}
                {...provided.draggableProps}
                className={className}
                style={{
                  ...style,
                  ...provided.draggableProps.style,
                  background: snapshot.isDragging ? '#e6f7ff' : style?.background,
                }}
              >
                {children}
              </tr>
            )
          }}
        </Draggable>
      )
    }

  const DraggableBody = (props) => (
    <Droppable droppableId="table-body">
      {(provided) => (
        <tbody
          ref={provided.innerRef}
          {...provided.droppableProps}
          {...props}
        >
          {props.children}
          {provided.placeholder}
        </tbody>
      )}
    </Droppable>
  )

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ 
        background: '#001529', 
        padding: '0 24px',
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center'
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '16px' }}>
          <Title level={3} style={{ color: 'white', margin: 0 }}>
            {t('app_title')}
          </Title>
          <Tag color="green">{t('version')} {appVersion || '-'}</Tag>
        </div>
        <Space>
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

      <Content style={{ padding: '24px' }}>
        <Space direction="vertical" style={{ width: '100%' }} size="large">
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <Space>
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
              <Space>
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

            <DragDropContext onDragEnd={handleDragEnd}>
              <Table
                columns={columns}
                dataSource={nodes}
                rowKey="id"
                expandable={{
                  expandedRowRender,
                }}
                loading={loading}
                pagination={false}
                scroll={{ x: 1500 }}
                locale={{
                  emptyText: t('no_nodes')
              }}
              components={{
                body: {
                  wrapper: DraggableBody,
                  row: DraggableRow,
                },
              }}
            />
          </DragDropContext>
        </Space>
      </Content>

      <Modal
        title={editingNode?.id ? t('edit') : t('add_node')}
        open={modalVisible}
        destroyOnClose
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
        selectedNodes={nodes.filter(n => selectedNodeIds.includes(n.id))}
        onClose={() => setBatchAuthVisible(false)}
        onSave={handleBatchSetAuth}
      />
    </Layout>
  )
}

export default Dashboard
