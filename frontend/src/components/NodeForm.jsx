import React, { useEffect, useState } from 'react'
import { Form, Input, Select, Button, Switch, Space, InputNumber } from 'antd'
import { useTranslation } from 'react-i18next'
import api from '../utils/api'

const { TextArea } = Input
const { Option } = Select

const proxyTypes = [
  { value: 'direct', label: 'Direct (Local)' },
  { value: 'ss', label: 'Shadowsocks' },
  { value: 'vless', label: 'VLESS' },
  { value: 'vmess', label: 'VMess' },
  { value: 'hy2', label: 'Hysteria2' },
  { value: 'tuic', label: 'TUIC' },
  { value: 'trojan', label: 'Trojan' },
  { value: 'anytls', label: 'AnyTLS' },
  { value: 'socks5', label: 'SOCKS5' },
  { value: 'http', label: 'HTTP Proxy' },
]

function NodeForm({ node, onSave, onCancel }) {
  const { i18n } = useTranslation()
  const [form] = Form.useForm()
  const [proxyType, setProxyType] = useState(node?.type || 'ss')
  const [loading, setLoading] = useState(false)
  const [preserveInboundPorts, setPreserveInboundPorts] = useState(false)
  const isChineseMode = i18n.language?.startsWith('zh')

  useEffect(() => {
    let cancelled = false

    const loadSettings = async () => {
      try {
        const response = await api.get('/settings')
        if (cancelled) return
        setPreserveInboundPorts(Boolean(response.data?.preserve_inbound_ports))
      } catch {
        // If settings cannot be loaded, default to safe mode (disallow edits).
        if (cancelled) return
        setPreserveInboundPorts(false)
      }
    }

    loadSettings()
    return () => {
      cancelled = true
    }
  }, [])

  const withHint = (label, zhHint) => {
    if (!isChineseMode || !zhHint) {
      return label
    }
    return `${label}（${zhHint}）`
  }

  const withExtraHint = (message, zhHint) => {
    if (!isChineseMode || !zhHint) {
      return message
    }
    return `${message}（${zhHint}）`
  }

  const handleSubmit = async (values) => {
    setLoading(true)
    try {
      // Build config object based on proxy type
      const config = buildConfig(proxyType, values)
      
      await onSave({
        name: values.name,
        remark: node?.remark || '',
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
    if (type === 'direct') {
      return {}
    }

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

      case 'anytls':
        return {
          ...config,
          password: values.anytls_password,
          sni: values.sni,
          alpn: values.alpn ? values.alpn.split(',').map(s => s.trim()).filter(Boolean) : [],
          fingerprint: values.fingerprint,
          insecure: values.insecure || false,
          idle_session_check_interval: values.idle_session_check_interval || '',
          idle_session_timeout: values.idle_session_timeout || '',
          min_idle_session: values.min_idle_session || 0,
        }

      case 'socks5':
        return {
          ...config,
          username: values.proxy_username || '',
          password: values.proxy_password || '',
        }

      case 'http':
        return {
          ...config,
          username: values.proxy_username || '',
          password: values.proxy_password || '',
          tls: values.proxy_tls || false,
          sni: values.proxy_sni || '',
          insecure: values.proxy_insecure || false,
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

  const protocolUsername = normalizedConfig.username
  const protocolPassword = normalizedConfig.password
  delete normalizedConfig.username
  delete normalizedConfig.password

  const proxyTLS = node?.type === 'http' ? normalizedConfig.tls : false
  const proxySNI = node?.type === 'http' ? normalizedConfig.sni : ''
  const proxyInsecure = node?.type === 'http' ? normalizedConfig.insecure : false
  if (node?.type === 'http') {
    delete normalizedConfig.tls
    delete normalizedConfig.sni
    delete normalizedConfig.insecure
  }

  const initialValues = {
    name: node?.name || '',
    remark: node?.remark || '',
    enabled: node?.enabled !== false,
    inbound_port: node?.inbound_port || 0,
    username: node?.username || '',
    password: node?.password || '',
    server: normalizedConfig.server || '',
    server_port: normalizedConfig.server_port || 443,
    ...normalizedConfig,
    ss_password: node?.type === 'ss' ? protocolPassword : '',
    hy2_password: node?.type === 'hy2' ? protocolPassword : '',
    tuic_password: node?.type === 'tuic' ? protocolPassword : '',
    trojan_password: node?.type === 'trojan' ? protocolPassword : '',
    anytls_password: node?.type === 'anytls' ? protocolPassword : '',
    proxy_username: node?.type === 'socks5' || node?.type === 'http' ? protocolUsername : '',
    proxy_password: node?.type === 'socks5' || node?.type === 'http' ? protocolPassword : '',
    proxy_tls: proxyTLS,
    proxy_sni: proxySNI,
    proxy_insecure: proxyInsecure,
  }

  const renderSSFields = () => (
    <>
      <Form.Item
        label={withHint('Method', '加密方式')}
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
        label={withHint('Password', '协议密码')}
        name="ss_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('Plugin', '插件类型')} name="plugin">
        <Input placeholder="e.g., obfs-local, v2ray-plugin" />
      </Form.Item>
      <Form.Item label={withHint('Plugin Options', '插件参数')} name="plugin_opts">
        <Input placeholder="e.g., obfs=http;obfs-host=www.bing.com" />
      </Form.Item>
    </>
  )

  const renderVLESSFields = () => (
    <>
      <Form.Item
        label={withHint('UUID', '节点唯一标识')}
        name="uuid"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input />
      </Form.Item>
      <Form.Item label={withHint('Flow', 'VLESS 流控')} name="flow">
        <Select allowClear>
          <Option value="">None</Option>
          <Option value="xtls-rprx-vision">xtls-rprx-vision</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('Network', '传输层协议')} name="network">
        <Select>
          <Option value="tcp">TCP</Option>
          <Option value="ws">WebSocket</Option>
          <Option value="grpc">gRPC</Option>
          <Option value="quic">QUIC</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('Security', '安全层类型')} name="security">
        <Select>
          <Option value="none">None</Option>
          <Option value="tls">TLS</Option>
          <Option value="reality">Reality</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('SNI', 'TLS 服务器名称')} name="sni">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('ALPN', 'TLS 协议协商列表')} name="alpn">
        <Input placeholder="e.g., h2, http/1.1" />
      </Form.Item>
      <Form.Item label={withHint('Fingerprint', '客户端指纹')} name="fingerprint">
        <Input placeholder="e.g., chrome, firefox, safari" />
      </Form.Item>
      <Form.Item label={withHint('Public Key (Reality)', 'Reality 公钥')} name="public_key">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('Short ID (Reality)', 'Reality 短 ID')} name="short_id">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('Path (WS)', 'WebSocket 路径')} name="path">
        <Input placeholder="e.g., /path" />
      </Form.Item>
      <Form.Item label={withHint('Service Name (gRPC)', 'gRPC 服务名')} name="service_name">
        <Input />
      </Form.Item>
    </>
  )

  const renderVMESSFields = () => (
    <>
      <Form.Item
        label={withHint('UUID', '节点唯一标识')}
        name="uuid"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input />
      </Form.Item>
      <Form.Item label={withHint('Alter ID', '额外 ID')} name="alter_id">
        <InputNumber min={0} max={65535} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label={withHint('Security', '加密策略')} name="vmess_security">
        <Select>
          <Option value="auto">Auto</Option>
          <Option value="aes-128-gcm">aes-128-gcm</Option>
          <Option value="chacha20-poly1305">chacha20-poly1305</Option>
          <Option value="none">None</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('Network', '传输层协议')} name="network">
        <Select>
          <Option value="tcp">TCP</Option>
          <Option value="ws">WebSocket</Option>
          <Option value="grpc">gRPC</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('TLS', '传输安全层')} name="tls">
        <Select>
          <Option value="none">None</Option>
          <Option value="tls">TLS</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('SNI', 'TLS 服务器名称')} name="sni">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('ALPN', 'TLS 协议协商列表')} name="alpn">
        <Input placeholder="e.g., h2, http/1.1" />
      </Form.Item>
      <Form.Item label={withHint('Path (WS)', 'WebSocket 路径')} name="path">
        <Input placeholder="e.g., /path" />
      </Form.Item>
      <Form.Item label={withHint('Service Name (gRPC)', 'gRPC 服务名')} name="service_name">
        <Input />
      </Form.Item>
    </>
  )

  const renderHy2Fields = () => (
    <>
      <Form.Item
        label={withHint('Password', '协议密码')}
        name="hy2_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('Upload Speed (Mbps)', '上行带宽限制')} name="up_mbps">
        <InputNumber min={0} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label={withHint('Download Speed (Mbps)', '下行带宽限制')} name="down_mbps">
        <InputNumber min={0} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label={withHint('Obfuscation', '混淆方式')} name="obfs">
        <Input placeholder="e.g., salamander" />
      </Form.Item>
      <Form.Item label={withHint('Obfs Password', '混淆密码')} name="obfs_password">
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('SNI', 'TLS 服务器名称')} name="sni">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('ALPN (comma-separated)', 'ALPN 列表，英文逗号分隔')} name="alpn">
        <Input placeholder="e.g., h3" />
      </Form.Item>
      <Form.Item label={withHint('Fingerprint', '客户端指纹')} name="fingerprint">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('TLS Verify', '证书校验开关')} name="insecure_skip_verify" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
    </>
  )

  const renderTUICFields = () => (
    <>
      <Form.Item
        label={withHint('UUID', '节点唯一标识')}
        name="uuid"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input />
      </Form.Item>
      <Form.Item
        label={withHint('Password', '协议密码')}
        name="tuic_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('Congestion Control', '拥塞控制算法')} name="congestion_control">
        <Select>
          <Option value="cubic">Cubic</Option>
          <Option value="new_reno">New Reno</Option>
          <Option value="bbr">BBR</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('UDP Relay Mode', 'UDP 转发模式')} name="udp_relay_mode">
        <Select>
          <Option value="native">Native</Option>
          <Option value="quic">QUIC</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('SNI', 'TLS 服务器名称')} name="sni">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('ALPN (comma-separated)', 'ALPN 列表，英文逗号分隔')} name="alpn">
        <Input placeholder="e.g., h3" />
      </Form.Item>
      <Form.Item label={withHint('Fingerprint', '客户端指纹')} name="fingerprint">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('TLS Verify', '证书校验开关')} name="insecure_skip_verify" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
      <Form.Item label={withHint('0-RTT Handshake', '是否启用 0-RTT')} name="zero_rtt_handshake" valuePropName="checked">
        <Switch checkedChildren="0-RTT" unCheckedChildren="Normal" />
      </Form.Item>
    </>
  )

  const renderAnyTLSFields = () => (
    <>
      <Form.Item
        label={withHint('Password', '协议密码')}
        name="anytls_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('SNI', 'TLS 服务器名称')} name="sni">
        <Input placeholder="example.com" />
      </Form.Item>
      <Form.Item label={withHint('ALPN (comma-separated)', 'ALPN 列表，英文逗号分隔')} name="alpn">
        <Input placeholder="h2,http/1.1" />
      </Form.Item>
      <Form.Item label={withHint('Fingerprint', '客户端指纹')} name="fingerprint">
        <Select allowClear>
          <Option value="chrome">Chrome</Option>
          <Option value="firefox">Firefox</Option>
          <Option value="safari">Safari</Option>
          <Option value="edge">Edge</Option>
          <Option value="random">Random</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('TLS Verify', '证书校验开关')} name="insecure" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
      <Form.Item label={withHint('Idle Check Interval', '空闲会话检查间隔')} name="idle_session_check_interval">
        <Input placeholder="30s" />
      </Form.Item>
      <Form.Item label={withHint('Idle Timeout', '空闲会话超时')} name="idle_session_timeout">
        <Input placeholder="10m" />
      </Form.Item>
      <Form.Item label={withHint('Min Idle Sessions', '最小空闲会话数')} name="min_idle_session">
        <InputNumber min={0} style={{ width: '100%' }} />
      </Form.Item>
    </>
  )

  const renderTrojanFields = () => (
    <>
      <Form.Item
        label={withHint('Password', '协议密码')}
        name="trojan_password"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('Network', '传输层协议')} name="network">
        <Select>
          <Option value="tcp">TCP</Option>
          <Option value="ws">WebSocket</Option>
          <Option value="grpc">gRPC</Option>
          <Option value="http">HTTP</Option>
          <Option value="httpupgrade">HTTPUpgrade</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('SNI', 'TLS 服务器名称')} name="sni">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('ALPN (comma-separated)', 'ALPN 列表，英文逗号分隔')} name="alpn">
        <Input placeholder="e.g., h2, http/1.1" />
      </Form.Item>
      <Form.Item label={withHint('Fingerprint', '客户端指纹')} name="fingerprint">
        <Input placeholder="e.g., chrome, firefox" />
      </Form.Item>
      <Form.Item label={withHint('Host Header', 'HTTP Host 头')} name="host">
        <Input placeholder="e.g., example.com" />
      </Form.Item>
      <Form.Item label={withHint('Path', '请求路径')} name="path">
        <Input placeholder="e.g., /ws" />
      </Form.Item>
      <Form.Item label={withHint('HTTP Method (for http/h2)', 'HTTP 请求方法')} name="http_method">
        <Input placeholder="e.g., GET" />
      </Form.Item>
      <Form.Item label={withHint('Service Name (gRPC)', 'gRPC 服务名')} name="service_name">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('TLS Verify', '证书校验开关')} name="insecure" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
    </>
  )

  const renderSOCKS5Fields = () => (
    <>
      <Form.Item label={withHint('Proxy Username', '出站用户名')} name="proxy_username">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('Proxy Password', '出站密码')} name="proxy_password">
        <Input.Password />
      </Form.Item>
    </>
  )

  const renderHTTPProxyFields = () => (
    <>
      <Form.Item label={withHint('Proxy Username', '出站用户名')} name="proxy_username">
        <Input />
      </Form.Item>
      <Form.Item label={withHint('Proxy Password', '出站密码')} name="proxy_password">
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('Transport Type', 'HTTP/HTTPS 开关')} name="proxy_tls" valuePropName="checked">
        <Switch checkedChildren="HTTPS" unCheckedChildren="HTTP" />
      </Form.Item>
      <Form.Item label={withHint('SNI (for HTTPS)', 'HTTPS 的 SNI')} name="proxy_sni">
        <Input placeholder="example.com" />
      </Form.Item>
      <Form.Item label={withHint('TLS Verify', '证书校验开关')} name="proxy_insecure" valuePropName="checked">
        <Switch checkedChildren="Skip Verify" unCheckedChildren="Verify" />
      </Form.Item>
    </>
  )

  const renderConfigFields = () => {
    switch (proxyType) {
      case 'direct':
        return null
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
      case 'anytls':
        return renderAnyTLSFields()
      case 'socks5':
        return renderSOCKS5Fields()
      case 'http':
        return renderHTTPProxyFields()
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
        label={withHint('Node Name', '节点名称')}
        name="name"
        rules={[{ required: true, message: 'Please enter node name' }]}
      >
        <Input />
      </Form.Item>

      <Form.Item label={withHint('Proxy Type', '代理协议类型')} required>
        <Select value={proxyType} onChange={setProxyType}>
          {proxyTypes.map((type) => (
            <Option key={type.value} value={type.value}>
              {type.label}
            </Option>
          ))}
        </Select>
      </Form.Item>

      <Form.Item
        label={withHint('Server Address', '远端服务器地址')}
        name="server"
        rules={proxyType === 'direct' ? [] : [{ required: true, message: 'Required' }]}
        hidden={proxyType === 'direct'}
      >
        <Input />
      </Form.Item>

      <Form.Item
        label={withHint('Server Port', '远端服务器端口')}
        name="server_port"
        rules={proxyType === 'direct' ? [] : [{ required: true, message: 'Required' }]}
        hidden={proxyType === 'direct'}
      >
        <InputNumber min={1} max={65535} style={{ width: '100%' }} />
      </Form.Item>

      <Form.Item
        label={withHint('Inbound Port', '本地监听端口')}
        name="inbound_port"
        extra={
          preserveInboundPorts
            ? withExtraHint(
                'Leave as 0 for auto-assignment based on node order',
                '填 0 按节点顺序自动分配'
              )
            : withExtraHint(
                'Managed by system order (enable Preserve Inbound Ports in Settings to edit)',
                '当前未开启保留入站端口：端口由系统按顺序分配，无法手动修改；如需修改请先在系统设置开启“保留入站端口”'
              )
        }
      >
        <InputNumber
          min={0}
          max={65535}
          style={{ width: '100%' }}
          placeholder="0 (auto)"
          disabled={!preserveInboundPorts}
        />
      </Form.Item>

      {renderConfigFields()}

      <Form.Item label={withHint('Inbound Authentication (Optional)', '入站认证（可选）')}>
        <Space.Compact style={{ width: '100%' }}>
          <Form.Item
            name="username"
            noStyle
            rules={[
              {
                validator: (_, value) => {
                  if (!value || !String(value).includes('+')) {
                    return Promise.resolve()
                  }
                  return Promise.reject(
                    new Error(withHint('Username cannot contain +', '用户名不能包含 +'))
                  )
                },
              },
            ]}
          >
            <Input placeholder={withHint('Username', '用户名')} style={{ width: '50%' }} />
          </Form.Item>
          <Form.Item name="password" noStyle>
            <Input.Password placeholder={withHint('Password', '密码')} style={{ width: '50%' }} />
          </Form.Item>
        </Space.Compact>
      </Form.Item>

      <Form.Item label={withHint('Node Status', '节点启用状态')} name="enabled" valuePropName="checked">
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
