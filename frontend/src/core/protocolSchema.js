// kind: text | secret | number | select | switch | textarea
// group: main（协议参数）| transport（传输）| tls（TLS）| advanced（高级，默认折叠）
// showWhen(values): 只控制字段可见性，实际提交的字段由 protocolForm.js 决定。

const opts = (...values) => values.map((value) => (Array.isArray(value) ? { value: value[0], label: value[1] } : { value, label: value }))

const NETWORK_WITH_PATH = new Set(['ws', 'http', 'httpupgrade'])
const tlsOn = (key) => (v) => v[key] !== 'none'
const isNetwork = (...names) => (v) => names.includes(v.network)
const hasPath = (v) => NETWORK_WITH_PATH.has(v.network)

const SNI = { name: 'sni', en: 'SNI', zh: 'TLS 服务器名称', kind: 'text', group: 'tls' }
const FINGERPRINT = { name: 'fingerprint', en: 'Fingerprint', zh: '客户端指纹', kind: 'text', group: 'tls' }
const ALPN_LIST = { name: 'alpn', en: 'ALPN', zh: '英文逗号分隔', kind: 'text', group: 'tls' }
const SKIP_VERIFY = { en: 'TLS Verify', zh: '证书校验', kind: 'switch', group: 'tls', on: 'Skip Verify', off: 'Verify' }
const UUID = { name: 'uuid', en: 'UUID', zh: '节点唯一标识', kind: 'text', required: true, span: 2 }
const PROXY_AUTH_FIELDS = [
  { name: 'proxy_username', en: 'Proxy Username', zh: '出站用户名', kind: 'text' },
  { name: 'proxy_password', en: 'Proxy Password', zh: '出站密码', kind: 'secret' },
]

const protocol = (label, shortLabel, fields = []) => ({ label, shortLabel, fields })

const PROTOCOL_SHORT_KEYS = {
  direct: 'protocol_direct_short',
  failover: 'protocol_failover_short',
  loadbalance: 'protocol_loadbalance_short',
  urltest: 'protocol_urltest_short',
}

const PROTOCOL_LABEL_KEYS = {
  direct: 'protocol_direct_label',
  failover: 'protocol_failover_label',
  loadbalance: 'protocol_loadbalance_label',
  urltest: 'protocol_urltest_label',
  http: 'protocol_http_label',
}

const PROTOCOLS = {
  direct: protocol('Direct (Local)', 'Direct'),
  failover: protocol('Failover (Policy)', 'Failover'),
  loadbalance: protocol('Load Balance (Policy)', 'LoadBalance'),
  urltest: protocol('Lowest Latency (Policy)', 'Lowest Latency'),

  ss: protocol('Shadowsocks', 'SS', [
    { name: 'method', en: 'Method', zh: '加密方式', kind: 'select', required: true, options: opts('aes-128-gcm', 'aes-256-gcm', 'chacha20-ietf-poly1305', '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm') },
    { name: 'ss_password', en: 'Password', zh: '协议密码', kind: 'secret', required: true },
    { name: 'plugin', en: 'Plugin', zh: '插件类型', kind: 'text', group: 'advanced', placeholder: 'obfs-local, v2ray-plugin' },
    { name: 'plugin_opts', en: 'Plugin Options', zh: '插件参数', kind: 'text', group: 'advanced', placeholder: 'obfs=http;obfs-host=www.bing.com' },
  ]),

  vless: protocol('VLESS', 'VLESS', [
    UUID,
    { name: 'flow', en: 'Flow', zh: '流控', kind: 'select', clearable: true, options: opts(['', 'None'], 'xtls-rprx-vision') },
    { name: 'network', en: 'Network', zh: '传输层协议', kind: 'select', group: 'transport', options: opts(['tcp', 'TCP'], ['ws', 'WebSocket'], ['grpc', 'gRPC'], ['quic', 'QUIC']) },
    { name: 'path', en: 'Path', zh: '请求路径', kind: 'text', group: 'transport', placeholder: '/path', showWhen: hasPath },
    { name: 'service_name', en: 'Service Name', zh: 'gRPC 服务名', kind: 'text', group: 'transport', showWhen: isNetwork('grpc') },
    { name: 'security', en: 'Security', zh: '安全层类型', kind: 'select', group: 'tls', options: opts(['none', 'None'], ['tls', 'TLS'], ['reality', 'Reality']) },
    { ...SNI, showWhen: tlsOn('security') },
    { ...ALPN_LIST, placeholder: 'h2, http/1.1', showWhen: tlsOn('security') },
    { ...FINGERPRINT, placeholder: 'chrome, firefox, safari', showWhen: tlsOn('security') },
    { name: 'public_key', en: 'Public Key', zh: 'Reality 公钥', kind: 'text', group: 'tls', span: 2, showWhen: (v) => v.security === 'reality' },
    { name: 'short_id', en: 'Short ID', zh: 'Reality 短 ID', kind: 'text', group: 'tls', showWhen: (v) => v.security === 'reality' },
  ]),

  vmess: protocol('VMess', 'VMess', [
    UUID,
    { name: 'alter_id', en: 'Alter ID', zh: '额外 ID', kind: 'number', min: 0, max: 65535 },
    { name: 'vmess_security', en: 'Security', zh: '加密策略', kind: 'select', options: opts(['auto', 'Auto'], 'aes-128-gcm', 'aes-128-cfb', 'chacha20-poly1305', ['none', 'None']) },
    { name: 'network', en: 'Network', zh: '传输层协议', kind: 'select', group: 'transport', options: opts(['tcp', 'TCP'], ['ws', 'WebSocket'], ['http', 'HTTP'], ['grpc', 'gRPC']) },
    { name: 'path', en: 'Path', zh: '请求路径', kind: 'text', group: 'transport', placeholder: '/path', showWhen: hasPath },
    { name: 'service_name', en: 'Service Name', zh: 'gRPC 服务名', kind: 'text', group: 'transport', showWhen: isNetwork('grpc') },
    { name: 'tls', en: 'TLS', zh: '传输安全层', kind: 'select', group: 'tls', options: opts(['none', 'None'], ['tls', 'TLS']) },
    { ...SNI, showWhen: tlsOn('tls') },
    { ...ALPN_LIST, placeholder: 'h2, http/1.1', showWhen: tlsOn('tls') },
  ]),

  hy2: protocol('Hysteria2', 'HY2', [
    { name: 'hy2_password', en: 'Password', zh: '协议密码', kind: 'secret', span: 2 },
    { name: 'up_mbps', en: 'Upload (Mbps)', zh: '上行带宽限制', kind: 'number', min: 0 },
    { name: 'down_mbps', en: 'Download (Mbps)', zh: '下行带宽限制', kind: 'number', min: 0 },
    { name: 'obfs', en: 'Obfuscation', zh: '混淆方式', kind: 'text', placeholder: 'salamander' },
    {
      name: 'obfs_password', en: 'Obfs Password', zh: '混淆密码', kind: 'secret',
      showWhen: (v) => Boolean(String(v.obfs || '').trim()) || Boolean(v.obfs_password),
      dependencies: ['obfs'],
      rules: ({ form }) => [{
        validator: (_, value) => {
          const obfs = String(form.getFieldValue('obfs') || '').trim().toLowerCase()
          return obfs === 'salamander' && String(value ?? '') === ''
            ? Promise.reject(new Error('Required for Salamander'))
            : Promise.resolve()
        },
      }],
    },
    SNI,
    { ...ALPN_LIST, placeholder: 'h3' },
    FINGERPRINT,
    { ...SKIP_VERIFY, name: 'insecure_skip_verify' },
  ]),

  tuic: protocol('TUIC', 'TUIC', [
    UUID,
    { name: 'tuic_password', en: 'Password', zh: '协议密码', kind: 'secret', span: 2 },
    { name: 'congestion_control', en: 'Congestion Control', zh: '拥塞控制算法', kind: 'select', options: opts(['cubic', 'Cubic'], ['new_reno', 'New Reno'], ['bbr', 'BBR']) },
    { name: 'udp_relay_mode', en: 'UDP Relay Mode', zh: 'UDP 转发模式', kind: 'select', options: opts(['native', 'Native'], ['quic', 'QUIC']) },
    SNI,
    { ...ALPN_LIST, placeholder: 'h3' },
    FINGERPRINT,
    { ...SKIP_VERIFY, name: 'insecure_skip_verify' },
    { name: 'zero_rtt_handshake', en: '0-RTT Handshake', zh: '启用 0-RTT', kind: 'switch', group: 'advanced', on: '0-RTT', off: 'Normal' },
  ]),

  trojan: protocol('Trojan', 'Trojan', [
    { name: 'trojan_password', en: 'Password', zh: '协议密码', kind: 'secret', span: 2 },
    { name: 'network', en: 'Network', zh: '传输层协议', kind: 'select', group: 'transport', options: opts(['tcp', 'TCP'], ['ws', 'WebSocket'], ['grpc', 'gRPC'], ['http', 'HTTP'], ['httpupgrade', 'HTTPUpgrade']) },
    { name: 'host', en: 'Host Header', zh: 'HTTP Host 头', kind: 'text', group: 'transport', placeholder: 'example.com', showWhen: hasPath },
    { name: 'path', en: 'Path', zh: '请求路径', kind: 'text', group: 'transport', placeholder: '/ws', showWhen: hasPath },
    { name: 'http_method', en: 'HTTP Method', zh: 'HTTP 请求方法', kind: 'text', group: 'transport', placeholder: 'GET', showWhen: isNetwork('http') },
    { name: 'service_name', en: 'Service Name', zh: 'gRPC 服务名', kind: 'text', group: 'transport', showWhen: isNetwork('grpc') },
    SNI,
    { ...ALPN_LIST, placeholder: 'h2, http/1.1' },
    { ...FINGERPRINT, placeholder: 'chrome, firefox' },
    { ...SKIP_VERIFY, name: 'insecure' },
  ]),

  anytls: protocol('AnyTLS', 'AnyTLS', [
    { name: 'anytls_password', en: 'Password', zh: '协议密码', kind: 'secret', span: 2 },
    { ...SNI, placeholder: 'example.com' },
    { ...ALPN_LIST, placeholder: 'h2,http/1.1' },
    { ...FINGERPRINT, kind: 'select', clearable: true, options: opts(['chrome', 'Chrome'], ['firefox', 'Firefox'], ['safari', 'Safari'], ['edge', 'Edge'], ['random', 'Random']) },
    { ...SKIP_VERIFY, name: 'insecure' },
    { name: 'idle_session_check_interval', en: 'Idle Check Interval', zh: '空闲会话检查间隔', kind: 'text', group: 'advanced', placeholder: '30s' },
    { name: 'idle_session_timeout', en: 'Idle Timeout', zh: '空闲会话超时', kind: 'text', group: 'advanced', placeholder: '10m' },
    { name: 'min_idle_session', en: 'Min Idle Sessions', zh: '最小空闲会话数', kind: 'number', group: 'advanced', min: 0 },
  ]),

  socks5: protocol('SOCKS5', 'SOCKS5', PROXY_AUTH_FIELDS),
  socks5h: protocol('SOCKS5H', 'SOCKS5H', PROXY_AUTH_FIELDS),

  http: protocol('HTTP Proxy', 'HTTP', [
    ...PROXY_AUTH_FIELDS,
    { name: 'proxy_tls', en: 'Transport Type', zh: 'HTTP / HTTPS', kind: 'switch', group: 'tls', on: 'HTTPS', off: 'HTTP' },
    { name: 'proxy_sni', en: 'SNI', zh: 'HTTPS 的 SNI', kind: 'text', group: 'tls', placeholder: 'example.com', showWhen: (v) => Boolean(v.proxy_tls) },
    { ...SKIP_VERIFY, name: 'proxy_insecure', showWhen: (v) => Boolean(v.proxy_tls) },
  ]),

  ssh: protocol('SSH', 'SSH', [
    { name: 'ssh_user', en: 'User', zh: 'SSH 用户名（留空默认 root）', kind: 'text', placeholder: 'root' },
    { name: 'ssh_password', en: 'Password', zh: 'SSH 密码', kind: 'secret' },
    {
      name: 'ssh_private_key', en: 'Private Key', zh: '私钥内容 (PEM)', kind: 'textarea', rows: 4, span: 2, mono: true,
      placeholder: '-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----',
    },
    { name: 'ssh_private_key_passphrase', en: 'Private Key Passphrase', zh: '私钥口令', kind: 'secret', span: 2 },
    { name: 'ssh_private_key_path', en: 'Private Key Path', zh: '私钥文件路径', kind: 'text', group: 'advanced', span: 2, placeholder: '/path/to/id_ed25519' },
    {
      name: 'ssh_host_key', en: 'Host Key', zh: '主机公钥（每行一条，留空接受任意）', kind: 'textarea', rows: 3, group: 'advanced', span: 2, mono: true,
      placeholder: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA...',
    },
    { name: 'ssh_host_key_algorithms', en: 'Host Key Algorithms', zh: '主机密钥算法（英文逗号分隔）', kind: 'text', group: 'advanced', placeholder: 'ssh-ed25519, rsa-sha2-512' },
    { name: 'ssh_client_version', en: 'Client Version', zh: '客户端版本标识', kind: 'text', group: 'advanced', placeholder: 'SSH-2.0-OpenSSH_8.9' },
  ]),

  wireguard: protocol('Cloudflare WireGuard', 'WireGuard', [
    { name: 'wireguard_local_address', en: 'Local Address', zh: '每行一个 CIDR', kind: 'textarea', rows: 3, required: true, span: 2, placeholder: '172.16.0.2/32\n2606:4700:110:8765::2/128' },
    { name: 'wireguard_private_key', en: 'Private Key', zh: '本地私钥', kind: 'secret', required: true, span: 2 },
    {
      name: 'wireguard_peer_public_key', en: 'Peer Public Key', zh: '对端公钥', kind: 'text', span: 2,
      dependencies: ['wireguard_peers_json'],
      rules: ({ form }) => [{
        validator: (_, value) =>
          String(form.getFieldValue('wireguard_peers_json') || '').trim() || String(value || '').trim()
            ? Promise.resolve()
            : Promise.reject(new Error('Required')),
      }],
    },
    { name: 'wireguard_pre_shared_key', en: 'Pre-shared Key', zh: '预共享密钥', kind: 'secret', span: 2 },
    { name: 'wireguard_allowed_ips', en: 'Allowed IPs', zh: '每行一个 CIDR', kind: 'textarea', rows: 3, placeholder: '0.0.0.0/0\n::/0' },
    { name: 'wireguard_reserved', en: 'Reserved Bytes', zh: 'Cloudflare 保留字节，逗号分隔', kind: 'text', placeholder: '162,104,222' },
    { name: 'wireguard_system_interface', en: 'System Interface', zh: '使用系统网卡', kind: 'switch', group: 'advanced', on: 'System', off: 'Userspace' },
    { name: 'wireguard_interface_name', en: 'Interface Name', zh: '系统网卡名称', kind: 'text', group: 'advanced', placeholder: 'wgcf' },
    { name: 'wireguard_mtu', en: 'MTU', zh: '', kind: 'number', group: 'advanced', min: 0, max: 65535 },
    { name: 'wireguard_workers', en: 'Workers', zh: '工作线程数', kind: 'number', group: 'advanced', min: 0, max: 128 },
    { name: 'wireguard_domain_resolver', en: 'Domain Resolver', zh: '域名解析器标签', kind: 'text', group: 'advanced', placeholder: 'local' },
    { name: 'wireguard_domain_resolver_strategy', en: 'Resolver Strategy', zh: '域名解析策略', kind: 'select', group: 'advanced', clearable: true, options: opts('prefer_ipv4', 'prefer_ipv6', 'ipv4_only', 'ipv6_only') },
    { name: 'wireguard_routing_mark', en: 'Routing Mark', zh: '路由标记', kind: 'text', group: 'advanced', placeholder: '0x10' },
    { name: 'wireguard_connect_timeout', en: 'Connect Timeout', zh: '连接超时', kind: 'text', group: 'advanced', placeholder: '5s' },
    { name: 'wireguard_udp_fragment', en: 'UDP Fragment', zh: '启用 UDP 分片', kind: 'switch', group: 'advanced', on: 'On', off: 'Off' },
    {
      name: 'wireguard_peers_json', en: 'Peers JSON', zh: '多 Peer 高级配置', kind: 'textarea', rows: 6, group: 'advanced', span: 2, mono: true,
      extra: { en: 'When provided, peers JSON takes precedence over the single-peer fields above', zh: '填写后将优先使用该 JSON 中的 peers 配置' },
      placeholder: '[\n  {\n    "server": "engage.cloudflareclient.com",\n    "server_port": 2408,\n    "public_key": "peer-public-key",\n    "allowed_ips": ["0.0.0.0/0", "::/0"],\n    "reserved": [162, 104, 222]\n  }\n]',
    },
  ]),
}

export const POLICY_NODE_TYPES = new Set(['failover', 'loadbalance', 'urltest'])
export const isPolicyType = (type) => POLICY_NODE_TYPES.has(String(type || '').toLowerCase())
export const localizedProxyTypes = (t, { includePolicy = true } = {}) =>
  Object.entries(PROTOCOLS)
    .filter(([value]) => includePolicy || !POLICY_NODE_TYPES.has(value))
    .map(([value, entry]) => ({
      value,
      label: typeof t === 'function' && Object.hasOwn(PROTOCOL_LABEL_KEYS, value) ? t(PROTOCOL_LABEL_KEYS[value]) : entry.label,
    }))
export const protocolLabel = (type, t) => {
  const entry = PROTOCOLS[type]
  if (!entry) return String(type || '-').toUpperCase()
  if (typeof t === 'function' && Object.hasOwn(PROTOCOL_SHORT_KEYS, type)) return t(PROTOCOL_SHORT_KEYS[type])
  return entry.shortLabel
}

const FIELD_GROUPS = ['main', 'transport', 'tls', 'advanced']

// 按分组整理字段，保持声明顺序。
export const groupFields = (type) => {
  const groups = new Map(FIELD_GROUPS.map((group) => [group, []]))
  for (const field of PROTOCOLS[type]?.fields || []) {
    groups.get(field.group || 'main').push(field)
  }
  return FIELD_GROUPS.map((group) => ({ group, fields: groups.get(group) })).filter((entry) => entry.fields.length > 0)
}
