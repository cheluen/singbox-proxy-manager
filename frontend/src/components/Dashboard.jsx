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

const APP_VERSION = '1.0.0'

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

  useEffect(() => {
    loadNodes()
  }, [])

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
    setEditingNode(null)
    setModalVisible(true)
  }

  const handleImportLink = async (link) => {
    try {
      message.loading({ content: t('loading'), key: 'parselink' })
      const response = await api.post('/parse-link', { link })
      const { type, name, config } = response.data
      
      const parsedConfig = typeof config === 'string' ? JSON.parse(config) : config
      
      message.success({ content: t('success'), key: 'parselink' })
      
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
    const links = batchImportLinks.split('\n').filter(link => link.trim())
    
    if (links.length === 0) {
      message.error(t('invalid_request'))
      return
    }

    try {
      setLoading(true)
      message.loading({ content: t('loading'), key: 'batchimport' })
      
      const response = await api.post('/nodes/batch-import', {
        links,
        enabled: enableAfterImport
      })

      const { success, failed } = response.data

      if (success > 0) {
        message.success({
          content: `${t('import_success').replace('{{count}}', success)}`,
          key: 'batchimport',
          duration: 3
        })
      }

      if (failed > 0) {
        message.warning({
          content: `${t('import_failed').replace('{{count}}', failed)}`,
          duration: 5
        })
      }

      setBatchImportVisible(false)
      setBatchImportLinks('')
      loadNodes()
    } catch (error) {
      message.error({
        content: error.response?.data?.error || t('server_error'),
        key: 'batchimport'
      })
    } finally {
      setLoading(false)
    }
  }

  const handleEditNode = (node) => {
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
      await Promise.all(selectedNodeIds.map(id => api.delete(`/nodes/${id}`)))
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
    let completed = 0
    const total = selectedNodeIds.length

    // Open notification with initial progress
    const key = 'batch-check-ip'
    notification.info({
      key,
      message: t('batch_check_ip_running')
        .replace('{{current}}', '1')
        .replace('{{total}}', total.toString()),
      duration: 0, // Don't auto close
      icon: <ThunderboltOutlined style={{ color: '#1890ff' }} />,
    })

    try {
      for (let i = 0; i < selectedNodeIds.length; i++) {
        const id = selectedNodeIds[i]
        
        try {
          await api.get(`/nodes/${id}/check-ip`)
          completed++
        } catch (error) {
          console.error(`Failed to check IP for node ${id}:`, error)
        }

        // Update notification with current progress (only if not the last one)
        if (i < selectedNodeIds.length - 1) {
          notification.info({
            key,
            message: t('batch_check_ip_running')
              .replace('{{current}}', (i + 2).toString())
              .replace('{{total}}', total.toString()),
            duration: 0,
            icon: <ThunderboltOutlined style={{ color: '#1890ff' }} />,
          })
        }
      }

      // Close the progress notification and show success
      notification.destroy(key)
      notification.success({
        message: t('batch_check_ip_success').replace('{{count}}', completed.toString()),
        duration: 3,
      })

      loadNodes()
    } finally {
      setCheckingIP(false)
    }
  }

  const handleSaveNode = async (values) => {
    try {
      if (editingNode?.id) {
        await api.put(`/nodes/${editingNode.id}`, values)
        message.success(t('node_updated'))
      } else {
        await api.post('/nodes', values)
        message.success(t('node_created'))
      }
      setModalVisible(false)
      setEditingNode(null)
      loadNodes()
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
      width: 100,
      fixed: 'right',
      render: (_, record) => (
        <Space size="small">
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
    const index = nodes.findIndex(item => item.id === props['data-row-key'])
    if (index === -1) return <tr {...props} />
    
    return (
      <Draggable draggableId={`node-${props['data-row-key']}`} index={index}>
        {(provided, snapshot) => (
          <tr
            ref={provided.innerRef}
            {...provided.draggableProps}
            style={{
              ...provided.draggableProps.style,
              background: snapshot.isDragging ? '#e6f7ff' : undefined,
            }}
          >
            <td {...provided.dragHandleProps} style={{ cursor: 'grab' }}>
              <HolderOutlined style={{ color: '#999' }} />
            </td>
            {React.Children.toArray(props.children).slice(1)}
          </tr>
        )}
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
          <Tag color="green">{t('version')} {APP_VERSION}</Tag>
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
        onCancel={() => {
          setModalVisible(false)
          setEditingNode(null)
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
