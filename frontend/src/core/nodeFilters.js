// 节点筛选条件模型（纯函数）。覆盖线上面板的全部筛选能力（状态 / 开关 / 类型 / 端口范围 / 名称 /
// 备注 / IP / 位置 / 地区 / 用户名 / TCP 复用），但改成「搜索框 + 状态分段 + 结构化条件」的组合。
import { matchInboundPortFilter, normalizeCountryCode, parseCountryName, parseInboundPortFilterExpression } from './nodeModel.js'
import { compileSearch, hasInboundAuth, nodeHealth, protocolLabel } from './nodeView.js'

export const EMPTY_CRITERIA = Object.freeze({
  search: '',
  status: 'all', // all | healthy | unverified | disabled
  types: [], // 协议精确匹配（修正了旧面板里 ss 会连带匹配 vless/vmess 的子串问题）
  regions: [], // 小写地区码；'-' 表示尚未检测出地区
  ports: '', // 单个端口或范围表达式：30001-30005, 30008
  username: '',
  tcpReuse: 'any', // any | on | off
  auth: 'any', // any | on | off
})

export const UNKNOWN_REGION = '-'
const regionOf = (node) => normalizeCountryCode(node?.country_code) || UNKNOWN_REGION

const triState = (wanted, actual) => wanted === 'any' || (wanted === 'on') === actual

export const filterNodes = (nodes, criteria) => {
  const active = { ...EMPTY_CRITERIA, ...criteria }
  if (!hasActiveCriteria(active)) return nodes
  const types = new Set(active.types)
  const regions = new Set(active.regions)
  const ports = String(active.ports || '').trim()
  const portRanges = ports ? parseInboundPortFilterExpression(ports) : null
  if (ports && !portRanges) return []
  const matchesSearch = compileSearch(active.search)
  return nodes.filter((node) => {
    if (active.status !== 'all' && nodeHealth(node) !== active.status) return false
    if (types.size > 0 && !types.has(node.type)) return false
    if (regions.size > 0 && !regions.has(regionOf(node))) return false
    if (portRanges && !matchInboundPortFilter(node.inbound_port, portRanges)) return false
    if (active.username && node.username !== active.username) return false
    if (!triState(active.tcpReuse, node.tcp_reuse_enabled !== false)) return false
    if (!triState(active.auth, hasInboundAuth(node))) return false
    return matchesSearch(node)
  })
}

// 结构化条件（不含搜索词与状态分段）的数量，用于「筛选」按钮上的角标。
export const countAdvancedCriteria = (criteria) =>
  criteria.types.length +
  criteria.regions.length +
  (String(criteria.ports || '').trim() ? 1 : 0) +
  (criteria.username ? 1 : 0) +
  (criteria.tcpReuse !== 'any' ? 1 : 0) +
  (criteria.auth !== 'any' ? 1 : 0)

export const hasActiveCriteria = (criteria) =>
  Boolean(String(criteria.search || '').trim()) || criteria.status !== 'all' || countAdvancedCriteria(criteria) > 0

const onOff = (value, t) => (value === 'on' ? t('enabled') : t('disabled'))

// 把当前条件展开成可逐个移除的标签：{ id, label, clear(criteria) => criteria }
export const describeCriteria = (criteria, t) => {
  const chips = []
  const search = String(criteria.search || '').trim()
  if (search) chips.push({ id: 'search', label: `${t('search')}: ${search}`, clear: (c) => ({ ...c, search: '' }) })
  if (criteria.status !== 'all') {
    const label = { healthy: t('status_healthy'), unverified: t('status_unverified_short'), disabled: t('disabled') }[criteria.status]
    chips.push({ id: 'status', label: `${t('status')}: ${label}`, clear: (c) => ({ ...c, status: 'all' }) })
  }
  for (const type of criteria.types) {
    chips.push({ id: `type:${type}`, label: protocolLabel(type), clear: (c) => ({ ...c, types: c.types.filter((item) => item !== type) }) })
  }
  for (const region of criteria.regions) {
    const label = region === UNKNOWN_REGION ? t('region_unknown') : region.toUpperCase()
    chips.push({ id: `region:${region}`, label: `${t('location')}: ${label}`, clear: (c) => ({ ...c, regions: c.regions.filter((item) => item !== region) }) })
  }
  const ports = String(criteria.ports || '').trim()
  if (ports) chips.push({ id: 'ports', label: `${t('inbound_port')}: ${ports}`, clear: (c) => ({ ...c, ports: '' }) })
  if (criteria.username) chips.push({ id: 'username', label: `${t('username')}: ${criteria.username}`, clear: (c) => ({ ...c, username: '' }) })
  if (criteria.tcpReuse !== 'any') chips.push({ id: 'tcpReuse', label: `${t('tcp_reuse')}: ${onOff(criteria.tcpReuse, t)}`, clear: (c) => ({ ...c, tcpReuse: 'any' }) })
  if (criteria.auth !== 'any') chips.push({ id: 'auth', label: `${t('inbound_authentication')}: ${onOff(criteria.auth, t)}`, clear: (c) => ({ ...c, auth: 'any' }) })
  return chips
}

// 供筛选面板生成可选项：只列出当前节点里真实存在的值。
export const collectFacets = (nodes) => {
  const types = new Map()
  const regions = new Map()
  const usernames = new Set()
  for (const node of nodes) {
    types.set(node.type, (types.get(node.type) || 0) + 1)
    const region = regionOf(node)
    const entry = regions.get(region) || { count: 0, name: '' }
    entry.count += 1
    if (!entry.name) entry.name = parseCountryName(node.location)
    regions.set(region, entry)
    if (node.username) usernames.add(node.username)
  }
  const byCount = (left, right) => right.count - left.count || String(left.value).localeCompare(String(right.value))
  return {
    types: Array.from(types, ([value, count]) => ({ value, count })).sort(byCount),
    regions: Array.from(regions, ([value, entry]) => ({ value, count: entry.count, name: entry.name })).sort(byCount),
    usernames: Array.from(usernames).sort((left, right) => left.localeCompare(right)),
  }
}
