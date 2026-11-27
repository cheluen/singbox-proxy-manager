const PORT_FILTER_TOKEN_PATTERN = /^(\d+)(?:\s*-\s*(\d+))?$/

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

const sortObjectKeysDeep = (value) => {
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

export const isPolicyNode = (node) => {
  const type = String(node?.type || '').toLowerCase()
  return type === 'failover' || type === 'loadbalance' || type === 'urltest'
}

export const isNodeExportable = (node) => {
  const type = String(node?.type || '').toLowerCase()
  return type !== 'direct' && !isPolicyNode({ type })
}

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

export const matchInboundPortFilter = (port, ranges) =>
  ranges.some(({ start, end }) => port >= start && port <= end)

export const getNodeStatusCode = (node) => {
  const status = String(node?.check_status || '').trim().toLowerCase()
  if (status === 'failed') return 'failed'
  if (status === 'ok') return 'healthy'
  if (status === 'untested') return 'untested'
  const hasIP = Boolean(String(node?.node_ip || '').trim())
  const latencyRaw = Number(node?.latency)
  const latency = Number.isFinite(latencyRaw) ? latencyRaw : 0
  return hasIP && latency > 0 ? 'healthy' : 'untested'
}
