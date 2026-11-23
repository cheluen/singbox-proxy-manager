import { getNodeStatusCode } from './nodeModel.js'
import { protocolLabel } from './protocolSchema.js'

export { protocolLabel }

// 面板里的三态：已禁用 / 可用 / 未检测或失效。三者互斥且相加等于总数。
export const nodeHealth = (node) => {
  if (node?.enabled === false) return 'disabled'
  return getNodeStatusCode(node) === 'healthy' ? 'healthy' : 'unverified'
}

export const latencyTone = (latency) => {
  const value = Number(latency)
  if (!Number.isFinite(value) || value <= 0) return 'none'
  if (value < 120) return 'fast'
  if (value < 260) return 'ok'
  return 'slow'
}

export const summarizeNodes = (nodes) => {
  const summary = { total: nodes.length, healthy: 0, unverified: 0, disabled: 0 }
  for (const node of nodes) {
    const health = nodeHealth(node)
    summary[health] += 1
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

export const serverAddress = (node) => node?.type === 'direct' ? '' : configEndpoint(parsedConfig(node))

export const upstreamAddress = (node) => {
  if (node?.upstream_mode !== 'custom' || !node.upstream_config) return ''
  try {
    return configEndpoint(JSON.parse(node.upstream_config))
  } catch {
    return ''
  }
}

export const initialUpstreamMode = (node) => {
  if (node?.upstream_mode) return node.upstream_mode
  return parsedConfig(node).detour ? 'legacy' : 'none'
}

export const hasInboundAuth = (node) => Boolean(node?.username && node?.password)

// 后端规则（services/singbox.go）：开启 TCP 复用且配置了入站认证的节点，
// 会以「用户名+入站端口」作为附加用户注册到所有启用认证的入站上。
export const reuseUsername = (node) => (hasInboundAuth(node) ? `${node.username}+${node.inbound_port}` : '')

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

const nodeHaystack = (node) => {
  if (haystackCache.has(node)) return haystackCache.get(node)
  const text = [
    node.name, node.remark, node.type, protocolLabel(node.type), node.node_ip, node.location, node.country_code,
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
