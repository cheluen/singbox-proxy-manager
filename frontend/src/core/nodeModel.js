export const FILTER_KEY_LABEL_MAP = {
  status: 'status',
  enabled: 'state_controls',
  type: 'node_type',
  inbound_port: 'inbound_port',
  name: 'node_name',
  remark: 'remark',
  node_ip: 'node_ip',
  location: 'location',
  country_code: 'country_code',
  username: 'username',
  tcp_reuse_enabled: 'tcp_reuse',
}

export const FILTER_KEY_ORDER = [
  'status',
  'enabled',
  'type',
  'inbound_port',
  'name',
  'remark',
  'node_ip',
  'location',
  'country_code',
  'username',
  'tcp_reuse_enabled',
]

export const FILTER_SUGGESTION_KEYS = new Set(['type', 'location', 'country_code', 'username'])
export const PORT_FILTER_TOKEN_PATTERN = /^(\d+)(?:\s*-\s*(\d+))?$/

export const getFilterKeyLabel = (key, t) => {
  const labelKey = FILTER_KEY_LABEL_MAP[key]
  return labelKey ? t(labelKey) : key
}

export const getFilterKeyOptions = (t) =>
  FILTER_KEY_ORDER.map((key) => ({
    value: key,
    label: getFilterKeyLabel(key, t),
  }))

export const getFilterSelectOptions = (key, t) => {
  if (key === 'status') {
    return [
      { value: 'healthy', label: t('status_healthy') },
      { value: 'unverified', label: t('status_unverified') },
    ]
  }

  if (key === 'enabled' || key === 'tcp_reuse_enabled') {
    return [
      { value: 'true', label: t('enabled') },
      { value: 'false', label: t('disabled') },
    ]
  }

  return []
}

export const getFilterValuePlaceholder = (key, t) =>
  key === 'inbound_port' ? t('filter_inbound_port_placeholder') : t('filter_value_placeholder')

export const getFilterValueLabel = (key, value, t) => {
  if (key === 'status') {
    return value === 'healthy'
      ? t('status_healthy')
      : value === 'unverified'
        ? t('status_unverified')
        : value
  }

  if (key === 'enabled' || key === 'tcp_reuse_enabled') {
    return value === 'true' ? t('enabled') : value === 'false' ? t('disabled') : value
  }

  return value
}

export const buildFilterSuggestionOptions = (nodes, key) => {
  if (!FILTER_SUGGESTION_KEYS.has(key) || !Array.isArray(nodes)) {
    return []
  }

  const values = new Set()
  for (const node of nodes) {
    const raw = String(node?.[key] ?? '').trim()
    if (raw) {
      values.add(raw)
    }
  }

  return Array.from(values)
    .sort((left, right) => left.localeCompare(right, undefined, { numeric: true, sensitivity: 'base' }))
    .map((value) => ({
      value,
      label: value,
    }))
}
export const parseCountryName = (location) => {
  const raw = String(location || '').trim()
  if (!raw) {
    return ''
  }
  const parts = raw.split(',').map((part) => part.trim()).filter(Boolean)
  return parts.length > 0 ? parts[parts.length - 1] : raw
}

export const normalizeCountryCode = (countryCode) => {
  const raw = String(countryCode || '').trim()
  if (!raw) {
    return ''
  }
  return raw.toLowerCase()
}

export const formatCountryWithCode = (record) => {
  const code = normalizeCountryCode(record?.country_code)
  const countryName = parseCountryName(record?.location)
  if (!code && !countryName) {
    return '-'
  }
  if (!countryName) {
    return code
  }
  if (!code) {
    return countryName
  }
  return `${code} ${countryName}`
}

export const sortObjectKeysDeep = (value) => {
  if (Array.isArray(value)) {
    return value.map(sortObjectKeysDeep)
  }
  if (!value || typeof value !== 'object') {
    return value
  }
  if (value.constructor !== Object) {
    return value
  }

  return Object.keys(value)
    .sort()
    .reduce((acc, key) => {
      acc[key] = sortObjectKeysDeep(value[key])
      return acc
    }, {})
}

export const stableJSONStringify = (value) => {
  try {
    const sorted = sortObjectKeysDeep(value)
    const str = JSON.stringify(sorted)
    return typeof str === 'string' ? str : ''
  } catch {
    return ''
  }
}

export const stableConfigSignature = (rawConfig) => {
  if (rawConfig === null || rawConfig === undefined) {
    return 'null'
  }

  if (typeof rawConfig !== 'string') {
    const normalized = stableJSONStringify(rawConfig)
    return normalized ? `json:${normalized}` : `raw:${String(rawConfig)}`
  }

  const trimmed = rawConfig.trim()
  if (!trimmed) {
    return 'json:{}'
  }

  try {
    return `json:${stableJSONStringify(JSON.parse(trimmed))}`
  } catch {
    return `raw:${trimmed}`
  }
}

export const isNodeExportable = (node) => String(node?.type || '').toLowerCase() !== 'direct'

export const normalizeFilterText = (value) => String(value ?? '').trim().toLowerCase()

export const parseInboundPortFilterExpression = (value) => {
  const tokens = String(value ?? '')
    .split(/[，,]/)
    .map((token) => token.trim())
    .filter(Boolean)

  if (tokens.length === 0) {
    return null
  }

  const ranges = []
  for (const token of tokens) {
    const matched = token.match(PORT_FILTER_TOKEN_PATTERN)
    if (!matched) {
      return null
    }

    const start = Number.parseInt(matched[1], 10)
    const end = Number.parseInt(matched[2] || matched[1], 10)
    if (!Number.isInteger(start) || !Number.isInteger(end) || start > end) {
      return null
    }

    ranges.push({ start, end })
  }

  return ranges
}

export const matchInboundPortFilter = (port, rawFilterValue) => {
  const parsedRanges = parseInboundPortFilterExpression(rawFilterValue)
  if (!parsedRanges) {
    return normalizeFilterText(port).includes(normalizeFilterText(rawFilterValue))
  }

  const normalizedPort = Number.parseInt(String(port ?? '').trim(), 10)
  if (!Number.isInteger(normalizedPort)) {
    return false
  }

  return parsedRanges.some(({ start, end }) => normalizedPort >= start && normalizedPort <= end)
}

export const getNodeStatusCode = (node) => {
  const hasIP = Boolean(String(node?.node_ip || '').trim())
  const latencyRaw = Number(node?.latency)
  const latency = Number.isFinite(latencyRaw) ? latencyRaw : 0
  return hasIP && latency > 0 ? 'healthy' : 'unverified'
}

export const matchNodeFilter = (node, filter) => {
  const key = String(filter?.key || '').trim()
  const valueText = normalizeFilterText(filter?.value)
  if (!key || !valueText) return true

  if (key === 'status') {
    return getNodeStatusCode(node) === valueText
  }

  if (key === 'enabled') {
    const enabled = node?.enabled !== false
    return (enabled ? 'true' : 'false') === valueText
  }

  if (key === 'tcp_reuse_enabled') {
    const enabled = node?.tcp_reuse_enabled !== false
    return (enabled ? 'true' : 'false') === valueText
  }

  if (key === 'inbound_port') {
    return matchInboundPortFilter(node?.inbound_port, filter?.value)
  }

  const candidate = normalizeFilterText(node?.[key])
  return candidate.includes(valueText)
}

export const applyNodeFilters = (nodes, filters) => {
  if (!Array.isArray(nodes)) return []
  if (!Array.isArray(filters) || filters.length === 0) return nodes
  const normalized = filters
    .map((filter) => ({
      key: String(filter?.key || '').trim(),
      value: String(filter?.value ?? '').trim(),
    }))
    .filter((filter) => filter.key && filter.value)
  if (normalized.length === 0) return nodes
  return nodes.filter((node) => normalized.every((filter) => matchNodeFilter(node, filter)))
}
