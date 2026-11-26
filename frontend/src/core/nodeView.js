import { getNodeStatusCode, isPolicyNode } from './nodeModel.js'
import { protocolLabel } from './protocolSchema.js'

export { protocolLabel }

// 面板里的四态：已禁用 / 可用 / 测速失败 / 未测。四者互斥且相加等于总数。
export const nodeHealth = (node) => {
  if (node?.enabled === false) return 'disabled'
  return getNodeStatusCode(node)
}

export const latencyTone = (latency) => {
  const value = Number(latency)
  if (!Number.isFinite(value) || value <= 0) return 'none'
  if (value < 120) return 'fast'
  if (value < 260) return 'ok'
  return 'slow'
}

export const summarizeNodes = (nodes) => {
  const summary = { total: nodes.length, healthy: 0, untested: 0, failed: 0, disabled: 0 }
  for (const node of nodes) {
    const health = nodeHealth(node)
    summary[health] = (summary[health] || 0) + 1
  }
  return summary
}

const configCache = new WeakMap()

// 节点对象在未变化时引用稳定，因此可以用 WeakMap 缓存解析结果，避免每次渲染都 JSON.parse。
const parsedConfig = (node) => {
  if (!node || typeof node !== 'object') return {}
  if (configCache.has(node)) return configCache.get(node)
  let parsed = {}
  try {
    const value = JSON.parse(node.config || '{}')
    if (value && typeof value === 'object' && !Array.isArray(value)) parsed = value
  } catch {
    parsed = {}
  }
  configCache.set(node, parsed)
  return parsed
}

const formatEndpoint = (server, port) => (server ? `${server}${port ? `:${port}` : ''}` : '')

const configEndpoint = (config) => {
  const peer = Array.isArray(config.peers) ? config.peers[0] : null
  return formatEndpoint(config.server || peer?.server, config.server_port || peer?.server_port)
}

export const serverAddress = (node, t) => {
  if (!node || node.type === 'direct') return ''
  if (node.type === 'failover' || node.type === 'loadbalance') {
    const cfg = parsedConfig(node)
    const count = Array.isArray(cfg.member_ids) ? cfg.member_ids.length : 0
    if (node.type === 'loadbalance') {
      const algo = cfg.strategy === 'consistent-hashing'
        ? (typeof t === 'function' ? t('policy_summary_hash') : 'sticky')
        : (typeof t === 'function' ? t('policy_summary_rr') : 'rr')
      return `${count} · ${algo}`
    }
    const label = typeof t === 'function' ? t('policy_summary_failover') : 'failover'
    return `${count} · ${label}`
  }
  return configEndpoint(parsedConfig(node))
}

export const parsedNodeConfig = (node) => parsedConfig(node)

export const configEndpointAddress = (rawConfig) => {
  if (!rawConfig) return ''
  try {
    return configEndpoint(JSON.parse(rawConfig))
  } catch {
    return ''
  }
}

export const initialUpstreamMode = (node) => {
  if (node?.upstream_mode) return node.upstream_mode
  return parsedConfig(node).detour ? 'legacy' : 'none'
}

// 节点实际生效的前置代理（与后端三层优先级完全一致：节点自定义 > 分组前置代理 > 全局前置代理；禁用节点与策略节点不生效）。
export const resolveEffectiveUpstream = (node, group, settings) => {
  const mode = initialUpstreamMode(node)
  if (node?.enabled === false || isPolicyNode(node)) {
    return {
      mode,
      source: 'none',
      active: false,
      disabled: node?.enabled === false,
      type: '',
      config: '',
      ip: '',
      location: '',
      country_code: '',
      latency: 0,
      error: '',
    }
  }
  if (mode === 'custom') {
    return {
      mode,
      source: 'custom',
      active: Boolean(node?.upstream_type && node?.upstream_config),
      disabled: false,
      type: node?.upstream_type || '',
      config: node?.upstream_config || '',
      ip: node?.upstream_ip || '',
      location: node?.upstream_location || '',
      country_code: node?.upstream_country_code || '',
      latency: Number(node?.upstream_latency) || 0,
      error: node?.upstream_error || '',
    }
  }
  if (mode === 'group') {
    const active = Boolean(group?.upstream_enabled && group?.upstream_type && group?.upstream_config)
    return {
      mode,
      source: active ? 'group' : 'none',
      active,
      disabled: false,
      type: active ? group.upstream_type : '',
      config: active ? group.upstream_config : '',
      ip: active ? (group.upstream_ip || '') : '',
      location: active ? (group.upstream_location || '') : '',
      country_code: active ? (group.upstream_country_code || '') : '',
      latency: active ? (Number(group.upstream_latency) || 0) : 0,
      error: active ? (group.upstream_error || '') : '',
    }
  }
  if (mode === 'global') {
    if (group?.upstream_enabled && group?.upstream_type && group?.upstream_config) {
      return {
        mode,
        source: 'group',
        active: true,
        disabled: false,
        type: group.upstream_type,
        config: group.upstream_config,
        ip: group.upstream_ip || '',
        location: group.upstream_location || '',
        country_code: group.upstream_country_code || '',
        latency: Number(group.upstream_latency) || 0,
        error: group.upstream_error || '',
      }
    }
    if (settings?.global_upstream_enabled && settings?.global_upstream_type && settings?.global_upstream_config) {
      return {
        mode,
        source: 'global',
        active: true,
        disabled: false,
        type: settings.global_upstream_type,
        config: settings.global_upstream_config,
        ip: settings.global_upstream_ip || '',
        location: settings.global_upstream_location || '',
        country_code: settings.global_upstream_country_code || '',
        latency: Number(settings.global_upstream_latency) || 0,
        error: settings.global_upstream_error || '',
      }
    }
    return { mode, source: 'none', active: false, disabled: false, type: '', config: '', ip: '', location: '', country_code: '', latency: 0, error: '' }
  }
  return { mode, source: mode, active: mode === 'legacy', disabled: false, type: '', config: '', ip: '', location: '', country_code: '', latency: 0, error: '' }
}

export const hasInboundAuth = (node) => Boolean(node?.username && node?.password)

// 后端规则（services/singbox.go）：有入站端口、开启 TCP 复用且配置了入站认证的节点，
// 会以「用户名+入站端口」作为附加用户注册到所有启用认证的入站上。
export const reuseUsername = (node) => (hasInboundAuth(node) && Number(node?.inbound_port) > 0 ? `${node.username}+${node.inbound_port}` : '')

export const panelHost = () => (typeof window === 'undefined' ? 'localhost' : window.location.hostname || 'localhost')

export const buildProxyUrl = (node, scheme, host = panelHost()) => {
  const auth = hasInboundAuth(node)
    ? `${encodeURIComponent(node.username)}:${encodeURIComponent(node.password)}@`
    : ''
  return `${scheme}://${auth}${host}:${node.inbound_port}`
}

export const buildHostPort = (node, host = panelHost()) => `${host}:${node.inbound_port}`

// 单引号不能直接嵌在 shell 单引号字符串中；关闭引号、转义、重新打开，完整保留凭据。
export const shellQuote = (value) => `'${String(value).replace(/'/g, "'\\''")}'`

const haystackCache = new WeakMap()

const searchTypeAliases = (node) => {
  if (node?.type === 'direct') return '直连 本机直连'
  if (node?.type === 'failover') return '故障转移 策略'
  if (node?.type === 'loadbalance') {
    return parsedConfig(node).strategy === 'consistent-hashing'
      ? '负载均衡 策略 目标粘性 粘性 target sticky'
      : '负载均衡 策略 轮询'
  }
  return ''
}

const nodeHaystack = (node) => {
  if (haystackCache.has(node)) return haystackCache.get(node)
  const text = [
    node.name, node.remark, node.type, protocolLabel(node.type), searchTypeAliases(node),
    node.node_ip, node.location, node.country_code,
    node.inbound_port, node.username, serverAddress(node),
  ].map((part) => String(part ?? '')).join('\n').toLowerCase()
  haystackCache.set(node, text)
  return text
}

// 空格分隔多个关键词，全部命中才算匹配。
const searchTokens = (search) => String(search || '').toLowerCase().split(/\s+/).filter(Boolean)

const matchesSearchTokens = (node, tokens) => {
  if (tokens.length === 0) return true
  const haystack = nodeHaystack(node)
  return tokens.every((token) => haystack.includes(token))
}

export const matchesSearch = (node, search) => matchesSearchTokens(node, searchTokens(search))

export const compileSearch = (search) => {
  const tokens = searchTokens(search)
  return (node) => matchesSearchTokens(node, tokens)
}

export const formatDateTime = (value, locale) => {
  const date = new Date(value)
  if (!value || Number.isNaN(date.getTime())) return '-'
  return date.toLocaleString(locale === 'zh' ? 'zh-CN' : 'en-US', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  })
}

// 明文 HTTP 页面没有 navigator.clipboard（非安全上下文），因此必须保留 execCommand 回退。
export const copyText = async (text) => {
  if (!text) return false
  try {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 权限被拒绝时继续走回退方案
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.top = '-1000px'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  try {
    textarea.select()
    textarea.setSelectionRange(0, textarea.value.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(textarea)
  }
}
