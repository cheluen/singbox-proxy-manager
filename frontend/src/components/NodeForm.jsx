import React, { useEffect, useState } from 'react'
import { Form, Input, Select, Button, Switch, Space, InputNumber, message } from 'antd'
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
  { value: 'wireguard', label: 'Cloudflare WireGuard' },
]

const splitMultilineList = (rawValue) => {
  if (typeof rawValue !== 'string') {
    return []
  }
  return rawValue
    .split(/[\n,;]+/)
    .map((item) => item.trim())
    .filter(Boolean)
}

const formatMultilineList = (items) =>
  Array.isArray(items) && items.length > 0 ? items.join('\n') : ''

const parseReservedInput = (rawValue) => {
  if (typeof rawValue !== 'string' || rawValue.trim() === '') {
    return []
  }

  const trimmed = rawValue.trim()
  if (trimmed.startsWith('[')) {
    const parsed = JSON.parse(trimmed)
    if (!Array.isArray(parsed)) {
      throw new Error('Reserved bytes must be an array')
    }
    return parsed.map((value) => {
      const numeric = Number.parseInt(String(value), 10)
      if (!Number.isInteger(numeric) || numeric < 0 || numeric > 255) {
        throw new Error('Reserved bytes must be integers between 0 and 255')
      }
      return numeric
    })
  }

  return splitMultilineList(trimmed).map((value) => {
    const numeric = Number.parseInt(String(value), 10)
    if (!Number.isInteger(numeric) || numeric < 0 || numeric > 255) {
      throw new Error('Reserved bytes must be integers between 0 and 255')
    }
    return numeric
  })
}

const formatReservedInput = (items) =>
  Array.isArray(items) && items.length > 0 ? items.join(',') : ''

const parsePeersJSON = (rawValue) => {
  if (typeof rawValue !== 'string' || rawValue.trim() === '') {
    return []
  }
  const parsed = JSON.parse(rawValue)
  if (!Array.isArray(parsed)) {
    throw new Error('Peers JSON must be an array')
  }
  return parsed
}

function NodeForm({ node, onSave, onCancel }) {
  const { t, i18n } = useTranslation()
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
      message.error(error?.message || t('invalid_config'))
    } finally {
      setLoading(false)
    }
  }

  const handleProxyTypeChange = (nextType) => {
    const currentPort = Number(form.getFieldValue('server_port') || 0)
    if (nextType === 'wireguard' && (!currentPort || currentPort === 443)) {
      form.setFieldsValue({ server_port: 2408 })
    } else if (proxyType === 'wireguard' && nextType !== 'wireguard' && currentPort === 2408) {
      form.setFieldsValue({ server_port: 443 })
    }
    setProxyType(nextType)
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

      case 'wireguard': {
        const peers = parsePeersJSON(values.wireguard_peers_json)
        const reserved = parseReservedInput(values.wireguard_reserved)
        return {
          ...config,
          local_address: splitMultilineList(values.wireguard_local_address),
          private_key: values.wireguard_private_key,
          peer_public_key: values.wireguard_peer_public_key || '',
          pre_shared_key: values.wireguard_pre_shared_key || '',
          allowed_ips: splitMultilineList(values.wireguard_allowed_ips),
          reserved,
          system_interface: values.wireguard_system_interface || false,
          interface_name: values.wireguard_interface_name || '',
          mtu: values.wireguard_mtu || 0,
          workers: values.wireguard_workers || 0,
          network:
            values.wireguard_network && values.wireguard_network !== 'both'
              ? values.wireguard_network
              : '',
          detour: values.wireguard_detour || '',
          domain_resolver: values.wireguard_domain_resolver || '',
          domain_resolver_strategy: values.wireguard_domain_resolver_strategy || '',
          routing_mark: values.wireguard_routing_mark || '',
          udp_fragment:
            typeof values.wireguard_udp_fragment === 'boolean'
              ? values.wireguard_udp_fragment
              : undefined,
          connect_timeout: values.wireguard_connect_timeout || '',
          peers,
        }
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
  if (node?.type === 'wireguard') {
    const wireguardPeers = Array.isArray(initialConfig.peers) ? initialConfig.peers : []
    const primaryPeer = wireguardPeers.length > 0 ? wireguardPeers[0] : null

    if (!normalizedConfig.server && primaryPeer?.server) {
      normalizedConfig.server = primaryPeer.server
    }
    if (!normalizedConfig.server_port && primaryPeer?.server_port) {
      normalizedConfig.server_port = primaryPeer.server_port
    }
    if (!normalizedConfig.peer_public_key && primaryPeer?.public_key) {
      normalizedConfig.peer_public_key = primaryPeer.public_key
    }
    if (!normalizedConfig.pre_shared_key && primaryPeer?.pre_shared_key) {
      normalizedConfig.pre_shared_key = primaryPeer.pre_shared_key
    }
    if ((!Array.isArray(normalizedConfig.allowed_ips) || normalizedConfig.allowed_ips.length === 0) && Array.isArray(primaryPeer?.allowed_ips)) {
      normalizedConfig.allowed_ips = primaryPeer.allowed_ips
    }
    if ((!Array.isArray(normalizedConfig.reserved) || normalizedConfig.reserved.length === 0) && Array.isArray(primaryPeer?.reserved)) {
      normalizedConfig.reserved = primaryPeer.reserved
    }
    normalizedConfig.wireguard_local_address = formatMultilineList(initialConfig.local_address)
    normalizedConfig.wireguard_allowed_ips = formatMultilineList(normalizedConfig.allowed_ips)
    normalizedConfig.wireguard_reserved = formatReservedInput(normalizedConfig.reserved)
    normalizedConfig.wireguard_peers_json =
      wireguardPeers.length > 1 ? JSON.stringify(wireguardPeers, null, 2) : ''
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
    wireguard_private_key: node?.type === 'wireguard' ? normalizedConfig.private_key || '' : '',
    wireguard_peer_public_key: node?.type === 'wireguard' ? normalizedConfig.peer_public_key || '' : '',
    wireguard_pre_shared_key: node?.type === 'wireguard' ? normalizedConfig.pre_shared_key || '' : '',
    wireguard_local_address: node?.type === 'wireguard' ? normalizedConfig.wireguard_local_address || '' : '',
    wireguard_allowed_ips: node?.type === 'wireguard' ? normalizedConfig.wireguard_allowed_ips || '' : '',
    wireguard_reserved: node?.type === 'wireguard' ? normalizedConfig.wireguard_reserved || '' : '',
    wireguard_system_interface: node?.type === 'wireguard' ? Boolean(normalizedConfig.system_interface) : false,
    wireguard_interface_name: node?.type === 'wireguard' ? normalizedConfig.interface_name || '' : '',
    wireguard_mtu: node?.type === 'wireguard' ? normalizedConfig.mtu || 0 : 0,
    wireguard_workers: node?.type === 'wireguard' ? normalizedConfig.workers || 0 : 0,
    wireguard_network:
      node?.type === 'wireguard'
        ? normalizedConfig.network || 'both'
        : 'both',
    wireguard_detour: node?.type === 'wireguard' ? normalizedConfig.detour || '' : '',
    wireguard_domain_resolver: node?.type === 'wireguard' ? normalizedConfig.domain_resolver || '' : '',
    wireguard_domain_resolver_strategy:
      node?.type === 'wireguard' ? normalizedConfig.domain_resolver_strategy || '' : '',
    wireguard_routing_mark: node?.type === 'wireguard' ? normalizedConfig.routing_mark || '' : '',
    wireguard_udp_fragment:
      node?.type === 'wireguard'
        ? Boolean(normalizedConfig.udp_fragment)
        : false,
    wireguard_connect_timeout: node?.type === 'wireguard' ? normalizedConfig.connect_timeout || '' : '',
    wireguard_peers_json: node?.type === 'wireguard' ? normalizedConfig.wireguard_peers_json || '' : '',
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

  const renderWireGuardFields = () => (
    <>
      <Form.Item
        label={withHint('Local Address (one per line)', '本地 WireGuard 地址，每行一个 CIDR')}
        name="wireguard_local_address"
        rules={[{ required: true, message: 'Required' }]}
      >
        <TextArea rows={3} placeholder={'172.16.0.2/32\n2606:4700:110:8765::2/128'} />
      </Form.Item>
      <Form.Item
        label={withHint('Private Key', '本地私钥')}
        name="wireguard_private_key"
        rules={[{ required: true, message: 'Required' }]}
      >
        <Input.Password />
      </Form.Item>
      <Form.Item
        label={withHint('Peer Public Key', '对端公钥')}
        name="wireguard_peer_public_key"
        rules={[
          {
            validator: (_, value) => {
              if (String(form.getFieldValue('wireguard_peers_json') || '').trim() || String(value || '').trim()) {
                return Promise.resolve()
              }
              return Promise.reject(new Error('Required'))
            },
          },
        ]}
      >
        <Input />
      </Form.Item>
      <Form.Item label={withHint('Pre-shared Key', '预共享密钥')} name="wireguard_pre_shared_key">
        <Input.Password />
      </Form.Item>
      <Form.Item label={withHint('Allowed IPs (one per line)', 'Allowed IPs，每行一个 CIDR')} name="wireguard_allowed_ips">
        <TextArea rows={3} placeholder={'0.0.0.0/0\n::/0'} />
      </Form.Item>
      <Form.Item label={withHint('Reserved Bytes', 'Cloudflare 保留字节，逗号分隔')} name="wireguard_reserved">
        <Input placeholder="162,104,222" />
      </Form.Item>
      <Form.Item label={withHint('System Interface', '是否使用系统网卡')} name="wireguard_system_interface" valuePropName="checked">
        <Switch checkedChildren="System" unCheckedChildren="Userspace" />
      </Form.Item>
      <Form.Item label={withHint('Interface Name', '系统网卡名称')} name="wireguard_interface_name">
        <Input placeholder="wgcf" />
      </Form.Item>
      <Form.Item label={withHint('MTU', 'MTU')} name="wireguard_mtu">
        <InputNumber min={0} max={65535} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label={withHint('Workers', '工作线程数')} name="wireguard_workers">
        <InputNumber min={0} max={128} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item label={withHint('Network', '启用的底层网络')} name="wireguard_network">
        <Select>
          <Option value="both">Both (Default)</Option>
          <Option value="udp">UDP</Option>
          <Option value="tcp">TCP</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('Detour', '拨号绕行出站标签')} name="wireguard_detour">
        <Input placeholder="selector" />
      </Form.Item>
      <Form.Item label={withHint('Domain Resolver', '域名解析器标签')} name="wireguard_domain_resolver">
        <Input placeholder="local" />
      </Form.Item>
      <Form.Item label={withHint('Resolver Strategy', '域名解析策略')} name="wireguard_domain_resolver_strategy">
        <Select allowClear>
          <Option value="prefer_ipv4">prefer_ipv4</Option>
          <Option value="prefer_ipv6">prefer_ipv6</Option>
          <Option value="ipv4_only">ipv4_only</Option>
          <Option value="ipv6_only">ipv6_only</Option>
        </Select>
      </Form.Item>
      <Form.Item label={withHint('Routing Mark', '路由标记')} name="wireguard_routing_mark">
        <Input placeholder="0x10" />
      </Form.Item>
      <Form.Item label={withHint('UDP Fragment', '是否启用 UDP 分片')} name="wireguard_udp_fragment" valuePropName="checked">
        <Switch checkedChildren="On" unCheckedChildren="Off" />
      </Form.Item>
      <Form.Item label={withHint('Connect Timeout', '连接超时')} name="wireguard_connect_timeout">
        <Input placeholder="5s" />
      </Form.Item>
      <Form.Item
        label={withHint('Peers JSON (Advanced)', '多 Peer 高级 JSON 配置')}
        name="wireguard_peers_json"
        extra={withExtraHint('When provided, peers JSON takes precedence over the single-peer fields above', '填写后将优先使用该 JSON 中的 peers 配置')}
      >
        <TextArea
          rows={6}
          placeholder={`[
  {
    "server": "engage.cloudflareclient.com",
    "server_port": 2408,
    "public_key": "peer-public-key",
    "allowed_ips": ["0.0.0.0/0", "::/0"],
    "reserved": [162, 104, 222]
  }
]`}
        />
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
      case 'wireguard':
        return renderWireGuardFields()
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
        <Select value={proxyType} onChange={handleProxyTypeChange}>
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
