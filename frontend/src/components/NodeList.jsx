import React from 'react'
import { DragDropContext, Droppable, Draggable } from 'react-beautiful-dnd'
import {
  List,
  Space,
  Button,
  Tag,
  Checkbox,
  Typography,
  Popconfirm,
  Empty,
} from 'antd'
import {
  EditOutlined,
  DeleteOutlined,
  GlobalOutlined,
  DragOutlined,
} from '@ant-design/icons'

const { Text } = Typography

function NodeList({
  nodes,
  loading,
  onEdit,
  onDelete,
  onCheckIP,
  onReorder,
  selectedNodes,
  onSelectNodes,
}) {
  const handleDragEnd = (result) => {
    if (!result.destination) return

    const items = Array.from(nodes)
    const [reorderedItem] = items.splice(result.source.index, 1)
    items.splice(result.destination.index, 0, reorderedItem)

    onReorder(items)
  }

  const handleSelectNode = (nodeId, checked) => {
    if (checked) {
      onSelectNodes([...selectedNodes, nodeId])
    } else {
      onSelectNodes(selectedNodes.filter((id) => id !== nodeId))
    }
  }

  const getTypeColor = (type) => {
    const colors = {
      ss: 'blue',
      vless: 'green',
      vmess: 'orange',
      hy2: 'purple',
      tuic: 'cyan',
    }
    return colors[type] || 'default'
  }

  if (nodes.length === 0) {
    return (
      <Empty
        description="No nodes yet"
        style={{ padding: '40px 0' }}
      />
    )
  }

  return (
    <DragDropContext onDragEnd={handleDragEnd}>
      <Droppable droppableId="nodes">
        {(provided) => (
          <div {...provided.droppableProps} ref={provided.innerRef}>
            {nodes.map((node, index) => (
              <Draggable
                key={node.id}
                draggableId={String(node.id)}
                index={index}
              >
                {(provided, snapshot) => (
                  <div
                    ref={provided.innerRef}
                    {...provided.draggableProps}
                    className={`node-item ${snapshot.isDragging ? 'dragging' : ''}`}
                  >
                    <div className="node-header">
                      <Space>
                        <div {...provided.dragHandleProps}>
                          <DragOutlined style={{ cursor: 'move', fontSize: 16 }} />
                        </div>
                        <Checkbox
                          checked={selectedNodes.includes(node.id)}
                          onChange={(e) =>
                            handleSelectNode(node.id, e.target.checked)
                          }
                        />
                        <Text strong style={{ fontSize: 16 }}>
                          {node.name}
                        </Text>
                        <Tag color={getTypeColor(node.type)}>
                          {node.type.toUpperCase()}
                        </Tag>
                        <Tag color={node.enabled ? 'success' : 'default'}>
                          {node.enabled ? 'Enabled' : 'Disabled'}
                        </Tag>
                      </Space>
                      <Space>
                        <Button
                          icon={<GlobalOutlined />}
                          onClick={() => onCheckIP(node.id)}
                          size="small"
                        >
                          Check IP
                        </Button>
                        <Button
                          icon={<EditOutlined />}
                          onClick={() => onEdit(node)}
                          size="small"
                        >
                          Edit
                        </Button>
                        <Popconfirm
                          title="Delete this node?"
                          onConfirm={() => onDelete(node.id)}
                          okText="Yes"
                          cancelText="No"
                        >
                          <Button
                            icon={<DeleteOutlined />}
                            danger
                            size="small"
                          >
                            Delete
                          </Button>
                        </Popconfirm>
                      </Space>
                    </div>
                    <div className="node-info">
                      <Text type="secondary">Port: {node.inbound_port}</Text>
                      {node.username && (
                        <Text type="secondary">
                          Auth: {node.username}:{node.password}
                        </Text>
                      )}
                      {node.node_ip && (
                        <Text type="secondary">IP: {node.node_ip}</Text>
                      )}
                      {node.location && (
                        <Text type="secondary">Location: {node.location}</Text>
                      )}
                    </div>
                  </div>
                )}
              </Draggable>
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </DragDropContext>
  )
}

export default NodeList
