import React, { useState } from 'react'
import { Form, Input, Select, Button, Switch, Space, Tabs, InputNumber } from 'antd'

const { TextArea } = Input
const { Option } = Select

const proxyTypes = [
  { value: 'ss', label: 'Shadowsocks' },
  { value: 'vless', label: 'VLESS' },
  { value: 'vmess', label: 'VMess' },
  { value: 'hy2', label: 'Hysteria2' },
  { value: 'tuic', label: 'TUIC' },
  { value: 'trojan', label: 'Trojan' },
]

function NodeForm({ node, onSave, onCancel }) {
  const [form] = Form.useForm()
  const [proxyType, setProxyType] = useState(node?.type || 'ss')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (values) => {
    setLoading(true)
    try {
      // Build config object based on proxy type
      const config = buildConfig(proxyType, values)
      
      await onSave({
        name: values.name,
        type: proxyType,
        config: JSON.stringify(config),
        inbound_port: values.inbound_port || 0, // 0 means auto-assign
        username: values.username || '',
        password: values.password || '',
        enabled: values.enabled !== false,
      })
    } catch (error) {
      console.error(error)
    } finally {
      setLoading(false)
    }
  }

  const buildConfig = (type, values) => {
    const config = {
      server: values.server,
      server_port: values.server_port,
    }

    switch (type) {
      case 'ss':
        return {
          ...config,
          method: values.method,
          password: values.ss_password,
          plugin: values.plugin,
          plugin_opts: values.plugin_opts,
        }
      
      case 'vless':
        return {
          ...config,
          uuid: values.uuid,
          flow: values.flow,
          network: values.network,
          security: values.security,
          sni: values.sni,
          alpn: values.alpn,
          fingerprint: values.fingerprint,
          public_key: values.public_key,
          short_id: values.short_id,
          path: values.path,
          service_name: values.service_name,
        }
      
      case 'vmess':
        return {
          ...config,
          uuid: values.uuid,
          alter_id: values.alter_id || 0,
          security: values.vmess_security,
          network: values.network,
          tls: values.tls,
          sni: values.sni,
          alpn: values.alpn,
          path: values.path,
          service_name: values.service_name,
        }
      
      case 'hy2':
        return {
          ...config,
          password: values.hy2_password,
          up_mbps: values.up_mbps,
          down_mbps: values.down_mbps,
          obfs: values.obfs,
          obfs_password: values.obfs_password,
          sni: values.sni,
          alpn: values.alpn ? values.alpn.split(',').map(s => s.trim()) : [],
          fingerprint: values.fingerprint,
          insecure_skip_verify: values.insecure_skip_verify || false,
        }
      
      case 'tuic':
        return {
          ...config,
          uuid: values.uuid,
          password: values.tuic_password,
          congestion_control: values.congestion_control,
          udp_relay_mode: values.udp_relay_mode,
          sni: values.sni,
          alpn: values.alpn ? values.alpn.split(',').map(s => s.trim()) : [],
          fingerprint: values.fingerprint,
          insecure_skip_verify: values.insecure_skip_verify || false,
          zero_rtt_handshake: values.zero_rtt_handshake || false,
        }

      case 'trojan':
        return {
          ...config,
          password: values.trojan_password || values.password,
          network: values.network,
          sni: values.sni,
          alpn: values.alpn ? values.alpn.split(',').map(s => s.trim()).filter(Boolean) : [],
          fingerprint: values.fingerprint,
          insecure: values.insecure || false,
          host: values.host,
          path: values.path,
          service_name: values.service_name,
          method: values.http_method,
        }
      
      default:
        return config
    }
  }

  const parseConfig = (configStr) => {
    if (!configStr) return {}
    try {
      return JSON.parse(configStr)
    } catch {
      return {}
    }
  }

  const initialConfig = node ? parseConfig(node.config) : {}
  const normalizedConfig = { ...initialConfig }
  if (Array.isArray(initialConfig.alpn)) {
    normalizedConfig.alpn = initialConfig.alpn.join(',')
  }

  const initialValues = {
    name: node?.name || '',
    enabled: node?.enabled !== false,
    inbound_port: node?.inbound_port || 0,
    username: node?.username || '',
    password: node?.password || '',
    server: normalizedConfig.server || '',
    server_port: normalizedConfig.server_port || 443,
    ...normalizedConfig,
  }

  const renderSSFields = () => (
    <>
      <Form.Item
        label="Method"
        name="method"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Select>
          <Option value="aes-128-gcm">aes-128-gcm</Option>
          <Option value="aes-256-gcm">aes-256-gcm</Option>
          <Option value="chacha20-ietf-poly1305">chacha20-ietf-poly1305</Option>
          <Option value="2022-blake3-aes-128-gcm">2022-blake3-aes-128-gcm</Option>
          <Option value="2022-blake3-aes-256-gcm">2022-blake3-aes-256-gcm</Option>
        </Select>
      </Form.Item>
      <Form.Item
        label="Password"
        name="ss_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label="Plugin" name="plugin">
        <Input placeholder="e.g., obfs-local, v2ray-plugin" />
      </Form.Item>
      <Form.Item label="Plugin Options" name="plugin_opts">
        <Input placeholder="e.g., obfs=http;obfs-host=www.bing.com" />
      </Form.Item>
    </>
  )

  const renderVLESSFields = () => (
    <>
      <Form.Item
        label="UUID"
        name="uuid"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input />
      </Form.Item>
      <Form.Item label="Flow" name="flow">
        <Select allowClear>
          <Option value="">None</Option>
          <Option value="xtls-rprx-vision">xtls-rprx-vision</Option>
        </Select>
      </Form.Item>
      <Form.Item label="Network" name="network">
        <Select>
          <Option value="tcp">TCP</Option>
          <Option value="ws">WebSocket</Option>
          <Option value="grpc">gRPC</Option>
          <Option value="quic">QUIC</Option>
        </Select>
      </Form.Item>
      <Form.Item label="Security" name="security">
        <Select>
          <Option value="none">None</Option>
          <Option value="tls">TLS</Option>
          <Option value="reality">Reality</Option>
        </Select>
      </Form.Item>
      <Form.Item label="SNI" name="sni">
        <Input />
      </Form.Item>
      <Form.Item label="ALPN" name="alpn">
        <Input placeholder="e.g., h2, http/1.1" />
      </Form.Item>
      <Form.Item label="Fingerprint" name="fingerprint">
        <Input placeholder="e.g., chrome, firefox, safari" />
      </Form.Item>
      <Form.Item label="Public Key (Reality)" name="public_key">
        <Input />
      </Form.Item>
      <Form.Item label="Short ID (Reality)" name="short_id">
        <Input />
      </Form.Item>
      <Form.Item label="Path (WS)" name="path">
        <Input placeholder="e.g., /path" />
      </Form.Item>
      <Form.Item label="Service Name (gRPC)" name="service_name">
        <Input />
      </Form.Item>
    </>
  )

  const renderVMESSFields = () => (
    <>
      <Form.Item
        label="UUID"
        name="uuid"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input />
      </Form.Item>
      <Form.Item label="Alter ID" name="alter_id">
        <InputNumber min={0} max={65535} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label="Security" name="vmess_security">
        <Select>
          <Option value="auto">Auto</Option>
          <Option value="aes-128-gcm">aes-128-gcm</Option>
          <Option value="chacha20-poly1305">chacha20-poly1305</Option>
          <Option value="none">None</Option>
        </Select>
      </Form.Item>
      <Form.Item label="Network" name="network">
        <Select>
          <Option value="tcp">TCP</Option>
          <Option value="ws">WebSocket</Option>
          <Option value="grpc">gRPC</Option>
        </Select>
      </Form.Item>
      <Form.Item label="TLS" name="tls">
        <Select>
          <Option value="none">None</Option>
          <Option value="tls">TLS</Option>
        </Select>
      </Form.Item>
      <Form.Item label="SNI" name="sni">
        <Input />
      </Form.Item>
      <Form.Item label="ALPN" name="alpn">
        <Input placeholder="e.g., h2, http/1.1" />
      </Form.Item>
      <Form.Item label="Path (WS)" name="path">
        <Input placeholder="e.g., /path" />
      </Form.Item>
      <Form.Item label="Service Name (gRPC)" name="service_name">
        <Input />
      </Form.Item>
    </>
  )

  const renderHy2Fields = () => (
    <>
      <Form.Item
        label="Password"
        name="hy2_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label="Upload Speed (Mbps)" name="up_mbps">
        <InputNumber min={0} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label="Download Speed (Mbps)" name="down_mbps">
        <InputNumber min={0} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label="Obfuscation" name="obfs">
        <Input placeholder="e.g., salamander" />
      </Form.Item>
      <Form.Item label="Obfs Password" name="obfs_password">
        <Input.Password />
      </Form.Item>
      <Form.Item label="SNI" name="sni">
        <Input />
      </Form.Item>
      <Form.Item label="ALPN (comma-separated)" name="alpn">
        <Input placeholder="e.g., h3" />
      </Form.Item>
      <Form.Item label="Fingerprint" name="fingerprint">
        <Input />
      </Form.Item>
      <Form.Item name="insecure_skip_verify" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
    </>
  )

  const renderTUICFields = () => (
    <>
      <Form.Item
        label="UUID"
        name="uuid"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input />
      </Form.Item>
      <Form.Item
        label="Password"
        name="tuic_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label="Congestion Control" name="congestion_control">
        <Select>
          <Option value="cubic">Cubic</Option>
          <Option value="new_reno">New Reno</Option>
          <Option value="bbr">BBR</Option>
        </Select>
      </Form.Item>
      <Form.Item label="UDP Relay Mode" name="udp_relay_mode">
        <Select>
          <Option value="native">Native</Option>
          <Option value="quic">QUIC</Option>
        </Select>
      </Form.Item>
      <Form.Item label="SNI" name="sni">
        <Input />
      </Form.Item>
      <Form.Item label="ALPN (comma-separated)" name="alpn">
        <Input placeholder="e.g., h3" />
      </Form.Item>
      <Form.Item label="Fingerprint" name="fingerprint">
        <Input />
      </Form.Item>
      <Form.Item name="insecure_skip_verify" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
      <Form.Item name="zero_rtt_handshake" valuePropName="checked">
        <Switch checkedChildren="0-RTT" unCheckedChildren="Normal" />
      </Form.Item>
    </>
  )

  const renderTrojanFields = () => (
    <>
      <Form.Item
        label="Password"
        name="trojan_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label="Network" name="network">
        <Select>
          <Option value="tcp">TCP</Option>
          <Option value="ws">WebSocket</Option>
          <Option value="grpc">gRPC</Option>
          <Option value="http">HTTP</Option>
          <Option value="httpupgrade">HTTPUpgrade</Option>
        </Select>
      </Form.Item>
      <Form.Item label="SNI" name="sni">
        <Input />
      </Form.Item>
      <Form.Item label="ALPN (comma-separated)" name="alpn">
        <Input placeholder="e.g., h2, http/1.1" />
      </Form.Item>
      <Form.Item label="Fingerprint" name="fingerprint">
        <Input placeholder="e.g., chrome, firefox" />
      </Form.Item>
      <Form.Item label="Host Header" name="host">
        <Input placeholder="e.g., example.com" />
      </Form.Item>
      <Form.Item label="Path" name="path">
        <Input placeholder="e.g., /ws" />
      </Form.Item>
      <Form.Item label="HTTP Method (for http/h2)" name="http_method">
        <Input placeholder="e.g., GET" />
      </Form.Item>
      <Form.Item label="Service Name (gRPC)" name="service_name">
        <Input />
      </Form.Item>
      <Form.Item name="insecure" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
    </>
  )

  const renderConfigFields = () => {
    switch (proxyType) {
      case 'ss':
        return renderSSFields()
      case 'vless':
        return renderVLESSFields()
      case 'vmess':
        return renderVMESSFields()
      case 'hy2':
        return renderHy2Fields()
      case 'tuic':
        return renderTUICFields()
      case 'trojan':
        return renderTrojanFields()
      default:
        return null
    }
  }

  return (
    <Form
      form={form}
      layout="vertical"
      initialValues={initialValues}
      onFinish={handleSubmit}
    >
      <Form.Item
        label="Node Name"
        name="name"
        rules={[{ required: true, message: 'Please enter node name' }]}
      >
        <Input />
      </Form.Item>

      <Form.Item label="Proxy Type" required>
        <Select value={proxyType} onChange={setProxyType}>
          {proxyTypes.map((type) => (
            <Option key={type.value} value={type.value}>
              {type.label}
            </Option>
          ))}
        </Select>
      </Form.Item>

      <Form.Item
        label="Server Address"
        name="server"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input />
      </Form.Item>

      <Form.Item
        label="Server Port"
        name="server_port"
        rules={[{ required: true, message: 'Required' }]}
      >
        <InputNumber min={1} max={65535} style={{ width: '100%' }} />
      </Form.Item>

      <Form.Item
        label="Inbound Port"
        name="inbound_port"
        extra="Leave as 0 for auto-assignment based on node order"
      >
        <InputNumber min={0} max={65535} style={{ width: '100%' }} placeholder="0 (auto)" />
      </Form.Item>

      {renderConfigFields()}

      <Form.Item label="Inbound Authentication (Optional)">
        <Space.Compact style={{ width: '100%' }}>
          <Form.Item name="username" noStyle>
            <Input placeholder="Username" style={{ width: '50%' }} />
          </Form.Item>
          <Form.Item name="password" noStyle>
            <Input.Password placeholder="Password" style={{ width: '50%' }} />
          </Form.Item>
        </Space.Compact>
      </Form.Item>

      <Form.Item name="enabled" valuePropName="checked">
        <Switch checkedChildren="Enabled" unCheckedChildren="Disabled" />
      </Form.Item>

      <Form.Item>
        <Space>
          <Button type="primary" htmlType="submit" loading={loading}>
            Save
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </Space>
      </Form.Item>
    </Form>
  )
}

export default NodeForm
