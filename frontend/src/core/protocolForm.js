import { isPolicyType } from './protocolSchema.js'
export { localizedProxyTypes, isPolicyType } from './protocolSchema.js'

const isPolicyTypeName = (type) => isPolicyType(type)

// 清洗策略成员来源（支持单个实体节点与整组自动同步），去重并保留顺序。
export function normalizePolicyMemberSources(sources, fallbackMemberIds = []) {
  const normalized = []
  const seen = new Set()
  const raw = Array.isArray(sources) && sources.length > 0
    ? sources
    : (Array.isArray(fallbackMemberIds) ? fallbackMemberIds.map((id) => ({ type: 'node', id })) : [])
  for (const entry of raw) {
    const type = entry?.type === 'group' ? 'group' : entry?.type === 'node' ? 'node' : ''
    const id = Number(entry?.id)
    if (!type || !Number.isInteger(id) || id <= 0) continue
    const key = `${type}:${id}`
    if (seen.has(key)) continue
    seen.add(key)
    normalized.push({ type, id })
  }
  return normalized
}

// 将分组内物理节点按组内排序 (sort_order ASC, id ASC) 排列（排除策略节点与自身）。
export function groupPhysicalNodesInOrder(allNodes, groupId, excludeNodeId = null) {
  return (Array.isArray(allNodes) ? allNodes : [])
    .filter((item) => item && item.group_id === groupId && !isPolicyTypeName(item.type) && item.id !== excludeNodeId)
    .sort((left, right) => (Number(left.sort_order ?? 0) - Number(right.sort_order ?? 0)) || (left.id - right.id))
}

// 断开指定分组的关联：按首次出现顺序原地展开该分组当前物理节点（跟随组内排序）为独立成员并去重，保持原有优先级不变。
export function unlinkPolicyGroupInSources(sources, groupId, allNodes, excludeNodeId = null) {
  const normalized = normalizePolicyMemberSources(sources)
  const seenNodeIds = new Set()
  const expanded = []
  for (const entry of normalized) {
    if (entry.type === 'group' && entry.id === groupId) {
      for (const member of groupPhysicalNodesInOrder(allNodes, groupId, excludeNodeId)) {
        if (seenNodeIds.has(member.id)) continue
        seenNodeIds.add(member.id)
        expanded.push({ type: 'node', id: member.id })
      }
      continue
    }
    if (entry.type === 'node') {
      if (seenNodeIds.has(entry.id)) continue
      seenNodeIds.add(entry.id)
    }
    expanded.push(entry)
  }
  return normalizePolicyMemberSources(expanded)
}

// 将策略来源列表展开为去重后的成员节点 ID 列表（供预览或保存前计算）。
export function resolvePolicyMemberIds(sources, allNodes, excludeNodeId = null) {
  const result = []
  const seen = new Set()
  for (const entry of normalizePolicyMemberSources(sources)) {
    if (entry.type === 'node') {
      if (!seen.has(entry.id)) {
        seen.add(entry.id)
        result.push(entry.id)
      }
      continue
    }
    for (const member of groupPhysicalNodesInOrder(allNodes, entry.id, excludeNodeId)) {
      if (!seen.has(member.id)) {
        seen.add(member.id)
        result.push(member.id)
      }
    }
  }
  return result
}

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

const parseReservedByte = (value) => {
  const normalized = String(value).trim()
  if (!/^\d+$/.test(normalized)) {
    throw new Error('Reserved bytes must be integers between 0 and 255')
  }
  const numeric = Number(normalized)
  if (!Number.isInteger(numeric) || numeric < 0 || numeric > 255) {
    throw new Error('Reserved bytes must be integers between 0 and 255')
  }
  return numeric
}

const decodeReservedBase64 = (rawValue) => {
  const normalized = rawValue
    .trim()
    .replace(/-/g, '+')
    .replace(/_/g, '/')
    .replace(/=+$/, '')
  if (!normalized || !/^[A-Za-z0-9+/]+$/.test(normalized) || normalized.length % 4 === 1) {
    return null
  }

  try {
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=')
    const decoded = globalThis.atob(padded)
    return Array.from(decoded, (character) => character.charCodeAt(0))
  } catch {
    return null
  }
}

export const parseReservedInput = (rawValue) => {
  if (Array.isArray(rawValue)) {
    return rawValue.map(parseReservedByte)
  }
  if (typeof rawValue !== 'string' || rawValue.trim() === '') {
    return []
  }

  const trimmed = rawValue.trim()
  if (trimmed.startsWith('[')) {
    const parsed = JSON.parse(trimmed)
    if (!Array.isArray(parsed)) {
      throw new Error('Reserved bytes must be an array')
    }
    return parsed.map(parseReservedByte)
  }

  const numericValues = trimmed
    .split(/[\s,;]+/)
    .map((value) => value.trim())
    .filter(Boolean)
  if (numericValues.length > 1 || /^\d+$/.test(trimmed)) {
    return numericValues.map(parseReservedByte)
  }

  const decoded = decodeReservedBase64(trimmed)
  if (decoded) {
    return decoded
  }
  throw new Error('Reserved bytes must be a numeric array, list, or base64 string')
}

const formatReservedInput = (items) => {
  if (items == null || items === '') {
    return ''
  }
  try {
    return parseReservedInput(items).join(',')
  } catch {
    // Keep malformed historical data visible so saving surfaces a validation
    // error instead of silently replacing it with an empty byte array.
    return typeof items === 'string' ? items : ''
  }
}

export const parsePeersJSON = (rawValue) => {
  if (typeof rawValue !== 'string' || rawValue.trim() === '') {
    return []
  }
  const parsed = JSON.parse(rawValue)
  if (!Array.isArray(parsed)) {
    throw new Error('Peers JSON must be an array')
  }
  return parsed.map((peer) => {
    if (
      !peer ||
      typeof peer !== 'object' ||
      Array.isArray(peer) ||
      !Object.prototype.hasOwnProperty.call(peer, 'reserved')
    ) {
      return peer
    }
    return { ...peer, reserved: parseReservedInput(peer.reserved) }
  })
}

const isPlainObject = (value) =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value)

const hasOwn = (value, key) =>
  Boolean(value) && Object.prototype.hasOwnProperty.call(value, key)

export const normalizeClearedSelect = (value) => value ?? ''

const nativeStringList = (value) => {
  if (Array.isArray(value)) {
    return value.filter((item) => typeof item === 'string')
  }
  if (typeof value === 'string' && value.trim()) {
    return [value]
  }
  return []
}

const nativeHeaderHost = (headers) => {
  if (!isPlainObject(headers)) return ''
  const key = Object.keys(headers).find((name) => name.toLowerCase() === 'host')
  return key && typeof headers[key] === 'string' ? headers[key] : ''
}

const hydrateNativeCompatibilityForForm = (type, config) => {
  const normalized = { ...config }
  const tls = isPlainObject(config.tls_options) ? config.tls_options : null
  if (tls) {
    if (!hasOwn(config, 'sni') && typeof tls.server_name === 'string') {
      normalized.sni = tls.server_name
    }
    if (!hasOwn(config, 'alpn')) {
      const alpn = nativeStringList(tls.alpn)
      if (alpn.length > 0) normalized.alpn = alpn
    }
    if (
      !hasOwn(config, 'fingerprint') &&
      isPlainObject(tls.utls) &&
      tls.utls.enabled !== false &&
      typeof tls.utls.fingerprint === 'string'
    ) {
      normalized.fingerprint = tls.utls.fingerprint
    }
    const insecureKey = type === 'hy2' || type === 'tuic' ? 'insecure_skip_verify' : 'insecure'
    if (!hasOwn(config, insecureKey) && typeof tls.insecure === 'boolean') {
      normalized[insecureKey] = tls.insecure
    }
    const reality = isPlainObject(tls.reality) ? tls.reality : null
    const realityEnabled = reality?.enabled === true
    if (type === 'vless') {
      if (!hasOwn(config, 'security')) {
        normalized.security = realityEnabled ? 'reality' : tls.enabled ? 'tls' : 'none'
      }
      if (
        realityEnabled &&
        !hasOwn(config, 'public_key') &&
        typeof reality?.public_key === 'string'
      ) {
        normalized.public_key = reality.public_key
      }
      if (
        realityEnabled &&
        !hasOwn(config, 'short_id') &&
        typeof reality?.short_id === 'string'
      ) {
        normalized.short_id = reality.short_id
      }
    } else if (type === 'vmess' && !hasOwn(config, 'tls')) {
      normalized.tls = realityEnabled ? 'reality' : tls.enabled ? 'tls' : 'none'
    } else if (type === 'trojan' && !hasOwn(config, 'security') && reality?.enabled) {
      normalized.security = 'reality'
    } else if (type === 'http' && !hasOwn(config, 'tls') && typeof tls.enabled === 'boolean') {
      normalized.tls = tls.enabled
    }
  }

  if (type === 'vless' || type === 'vmess' || type === 'trojan') {
    const transport = isPlainObject(config.transport_options) ? config.transport_options : null
    if (transport) {
      const transportType =
        typeof transport.type === 'string'
          ? transport.type === 'h2'
            ? 'http'
            : transport.type
          : ''
      if (!hasOwn(config, 'network') && transportType) {
        normalized.network = transportType
      }
      const network = normalized.network || transportType
      if ((network === 'ws' || network === 'http') && !hasOwn(config, 'path') && typeof transport.path === 'string') {
        normalized.path = transport.path
      }
      if (network === 'ws') {
        if (!hasOwn(config, 'host')) {
          const host = nativeHeaderHost(transport.headers)
          if (host) normalized.host = host
        }
      } else if (network === 'grpc' && !hasOwn(config, 'service_name') && typeof transport.service_name === 'string') {
        normalized.service_name = transport.service_name
      } else if (network === 'http') {
        if (!hasOwn(config, 'host')) {
          const hosts = nativeStringList(transport.host)
          if (hosts.length > 0) normalized.host = hosts.join(',')
        }
        if (type === 'vmess' && !hasOwn(config, 'http_path') && typeof transport.path === 'string') {
          normalized.http_path = [transport.path]
        }
        if (!hasOwn(config, 'method') && typeof transport.method === 'string') {
          normalized.method = transport.method
        }
      } else if (network === 'httpupgrade') {
        if (!hasOwn(config, 'path') && typeof transport.path === 'string') {
          normalized.path = transport.path
        }
        if (!hasOwn(config, 'host') && typeof transport.host === 'string') {
          normalized.host = transport.host
        }
      }
    }
  }
  return normalized
}

export function createProtocolFormModel(node, { isUpstreamEditor = false, isFieldTouched = () => false } = {}) {
  const parseConfig = (configStr) => {
    if (!configStr) return {}
    try {
      return JSON.parse(configStr)
    } catch {
      return {}
    }
  }

	  const initialConfig = node ? parseConfig(node.config) : {}
	  const normalizedConfig = hydrateNativeCompatibilityForForm(node?.type, initialConfig)
  if (node?.type === 'vmess' && normalizedConfig.network === 'http' && Array.isArray(normalizedConfig.http_path)) {
    normalizedConfig.path = normalizedConfig.http_path[0] || ''
  }
  if (Array.isArray(normalizedConfig.alpn)) {
    normalizedConfig.alpn = normalizedConfig.alpn.join(',')
  }
  if (node?.type === 'hy2') {
    if (initialConfig.brutal_up_mbps > 0 || initialConfig.brutal_down_mbps > 0) {
      normalizedConfig.up_mbps = initialConfig.brutal_up_mbps > 0 ? initialConfig.brutal_up_mbps : 0
      normalizedConfig.down_mbps = initialConfig.brutal_down_mbps > 0 ? initialConfig.brutal_down_mbps : 0
    }
    const nativeObfs = initialConfig.obfs
    if (nativeObfs && typeof nativeObfs === 'object' && !Array.isArray(nativeObfs)) {
      normalizedConfig.obfs = typeof nativeObfs.type === 'string' ? nativeObfs.type : ''
      if (!Object.prototype.hasOwnProperty.call(initialConfig, 'obfs_password')) {
        normalizedConfig.obfs_password =
          typeof nativeObfs.password === 'string' ? nativeObfs.password : ''
      }
    }
    if (
      !Object.prototype.hasOwnProperty.call(initialConfig, 'obfs_password') &&
      !normalizedConfig.obfs_password &&
      typeof initialConfig.salamander_password === 'string'
    ) {
      normalizedConfig.obfs_password = initialConfig.salamander_password
    }
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
    if (
      (!Array.isArray(normalizedConfig.reserved) || normalizedConfig.reserved.length === 0) &&
      (Array.isArray(primaryPeer?.reserved) || typeof primaryPeer?.reserved === 'string')
    ) {
      normalizedConfig.reserved = primaryPeer.reserved
    }
    normalizedConfig.wireguard_local_address = formatMultilineList(initialConfig.local_address)
    normalizedConfig.wireguard_allowed_ips = formatMultilineList(normalizedConfig.allowed_ips)
    normalizedConfig.wireguard_reserved = formatReservedInput(normalizedConfig.reserved)
    normalizedConfig.wireguard_peers_json =
      wireguardPeers.length > 0 ? JSON.stringify(wireguardPeers, null, 2) : ''

	    const nativeResolver = initialConfig.domain_resolver
	    const resolverOptions = initialConfig.domain_resolver_options
	    const nativeResolverObject = isPlainObject(nativeResolver) ? nativeResolver : null
	    const compatibilityResolverObject = isPlainObject(resolverOptions) ? resolverOptions : null
	    const resolverObject =
	      nativeResolverObject || compatibilityResolverObject
	        ? { ...(compatibilityResolverObject || {}), ...(nativeResolverObject || {}) }
	        : null
    if (typeof nativeResolver === 'string') {
      normalizedConfig.domain_resolver = nativeResolver
    } else if (resolverObject) {
      normalizedConfig.domain_resolver =
        typeof resolverObject.server === 'string' ? resolverObject.server : ''
    }
	    if (!Object.prototype.hasOwnProperty.call(initialConfig, 'domain_resolver_strategy')) {
      normalizedConfig.domain_resolver_strategy =
        resolverObject && typeof resolverObject.strategy === 'string'
          ? resolverObject.strategy
	          : ''
	    }
	    if (resolverObject) {
	      const resolverCompatibility = { ...resolverObject }
	      delete resolverCompatibility.server
	      delete resolverCompatibility.strategy
	      normalizedConfig.domain_resolver_options = resolverCompatibility
	    }
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

  if (node?.type === 'tuic') {
    normalizedConfig.zero_rtt_handshake = Boolean(
      normalizedConfig.zero_rtt_handshake ?? normalizedConfig.reduce_rtt
    )
    delete normalizedConfig.reduce_rtt
  }

  const sshUser = node?.type === 'ssh' ? (normalizedConfig.user || '') : ''
  const sshPrivateKey = node?.type === 'ssh'
    ? (Array.isArray(normalizedConfig.private_key)
        ? normalizedConfig.private_key.join('\n')
        : (normalizedConfig.private_key || ''))
    : ''
  const sshPrivateKeyPath = node?.type === 'ssh' ? (normalizedConfig.private_key_path || '') : ''
  const sshPrivateKeyPassphrase = node?.type === 'ssh' ? (normalizedConfig.private_key_passphrase || '') : ''
  const sshHostKey = node?.type === 'ssh'
    ? (Array.isArray(normalizedConfig.host_key)
        ? normalizedConfig.host_key.join('\n')
        : (normalizedConfig.host_key || ''))
    : ''
  const sshHostKeyAlgorithms = node?.type === 'ssh'
    ? (Array.isArray(normalizedConfig.host_key_algorithms)
        ? normalizedConfig.host_key_algorithms.join(', ')
        : (normalizedConfig.host_key_algorithms || ''))
    : ''
  const sshClientVersion = node?.type === 'ssh' ? (normalizedConfig.client_version || '') : ''
  if (node?.type === 'ssh') {
    delete normalizedConfig.user
    delete normalizedConfig.private_key
    delete normalizedConfig.private_key_path
    delete normalizedConfig.private_key_passphrase
    delete normalizedConfig.host_key
    delete normalizedConfig.host_key_algorithms
    delete normalizedConfig.client_version
  }

  const initialPolicyMemberIds = Array.isArray(normalizedConfig.member_ids)
    ? normalizedConfig.member_ids.map(Number).filter((id) => Number.isInteger(id) && id > 0)
    : []
  const initialPolicyMemberSources = normalizePolicyMemberSources(
    normalizedConfig.member_sources,
    initialPolicyMemberIds
  )

  const initialValues = {
    name: node?.name || '',
    remark: node?.remark || '',
    enabled: node?.enabled !== false,
    inbound_port: node?.inbound_port || 0,
    inbound_mode: node?.inbound_mode === 'https' ? 'https' : 'mixed',
    username: node?.username || '',
    password: node?.password || '',
    auth_enabled: node?.id
      ? Boolean(node?.username && node?.password)
      : true,
    server: normalizedConfig.server || '',
    server_port: normalizedConfig.server_port || (node?.type === 'ssh' ? 22 : 443),
    policy_member_ids: initialPolicyMemberIds,
    policy_member_sources: initialPolicyMemberSources,
    policy_strategy: normalizedConfig.strategy || 'round-robin',
    policy_tolerance: normalizedConfig.tolerance ?? 50,
    policy_health_check_url: normalizedConfig.health_check_url || '',
    policy_health_check_interval: normalizedConfig.health_check_interval || 30,
    ...normalizedConfig,
    vmess_security: node?.type === 'vmess' ? normalizedConfig.security || 'auto' : 'auto',
    ss_password: node?.type === 'ss' ? protocolPassword : '',
    hy2_password: node?.type === 'hy2' ? protocolPassword ?? '' : '',
    tuic_password: node?.type === 'tuic' ? protocolPassword : '',
    trojan_password: node?.type === 'trojan' ? protocolPassword ?? '' : '',
    anytls_password: node?.type === 'anytls' ? protocolPassword ?? '' : '',
    proxy_username: node?.type === 'socks5' || node?.type === 'socks5h' || node?.type === 'http' ? protocolUsername : '',
    proxy_password: node?.type === 'socks5' || node?.type === 'socks5h' || node?.type === 'http' ? protocolPassword : '',
    proxy_tls: proxyTLS,
    proxy_sni: proxySNI,
    proxy_insecure: proxyInsecure,
    ssh_user: sshUser,
    ssh_password: node?.type === 'ssh' ? (protocolPassword ?? '') : '',
    ssh_private_key: sshPrivateKey,
    ssh_private_key_path: sshPrivateKeyPath,
    ssh_private_key_passphrase: sshPrivateKeyPassphrase,
    ssh_host_key: sshHostKey,
    ssh_host_key_algorithms: sshHostKeyAlgorithms,
    ssh_client_version: sshClientVersion,
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

  const buildConfig = (type, values) => {
    const built = buildConfigFromForm(type, values)
    if (!node || node.type !== type) {
      return finalizeFormConfig(type, built)
    }
    const overlay = {}
    for (const [key, value] of Object.entries(built)) {
      if (value !== undefined) {
        overlay[key] = value
      }
    }
    return finalizeFormConfig(type, { ...initialConfig, ...overlay })
  }

  const finalizeFormConfig = (type, config) => {
    if (isUpstreamEditor) config = { ...config, detour: '' }
    if (isPolicyType(type)) {
      const finalized = { ...config }
      const sources = Array.isArray(finalized.member_sources) ? finalized.member_sources : []
      if (!sources.some((entry) => entry?.type === 'group')) {
        delete finalized.member_sources
      }
      if (type !== 'loadbalance') delete finalized.strategy
      if (type !== 'urltest') delete finalized.tolerance
      return finalized
    }
    if (type === 'hy2') {
      const finalized = { ...config }
      delete finalized.brutal_up_mbps
      delete finalized.brutal_down_mbps
      return finalized
    }
    if (type === 'tuic' && !config.zero_rtt_handshake) {
      const finalized = { ...config }
      delete finalized.reduce_rtt
      return finalized
    }
    if (type !== 'vless' || config.security === 'reality') {
      return config
    }

    // Reality credentials are only meaningful when Reality is active. Remove
    // stale/derived flat compatibility fields after the merge so switching an
    // imported node to TLS/none cannot produce security=tls with pbk/sid. Keep
    // the native tls_options object untouched; backend reconciliation decides
    // which native TLS branch is active.
    const finalized = { ...config }
    delete finalized.public_key
    delete finalized.short_id
    return finalized
  }

  const buildConfigFromForm = (type, values) => {
    if (type === 'direct') {
      return {}
    }
    if (isPolicyType(type)) {
      const sources = normalizePolicyMemberSources(values.policy_member_sources, values.policy_member_ids)
      const hasGroupSource = sources.some((entry) => entry.type === 'group')
      const memberIds = hasGroupSource
        ? sources.filter((entry) => entry.type === 'node').map((entry) => entry.id)
        : sources.map((entry) => entry.id)
      const base = {
        member_ids: memberIds,
        member_sources: hasGroupSource ? sources : [],
        ...(values.policy_health_check_url ? { health_check_url: String(values.policy_health_check_url).trim() } : {}),
        ...(Number(values.policy_health_check_interval) > 0 ? { health_check_interval: Number(values.policy_health_check_interval) } : {}),
      }
      if (type === 'loadbalance') {
        return {
          ...base,
          strategy: values.policy_strategy === 'consistent-hashing' ? 'consistent-hashing' : 'round-robin',
        }
      }
      if (type === 'urltest') {
        const rawTol = values.policy_tolerance
        const tolerance = rawTol === '' || rawTol === null || rawTol === undefined ? 50 : Number(rawTol)
        return {
          ...base,
          tolerance: Number.isFinite(tolerance) ? Math.round(tolerance) : 50,
        }
      }
      return base
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
          http_path:
            values.network === 'http' && isFieldTouched('path')
              ? values.path ? [values.path] : []
              : undefined,
          service_name: values.service_name,
        }

      case 'hy2':
        return {
          ...config,
          password: values.hy2_password ?? '',
          up_mbps: values.up_mbps === '' ? 0 : values.up_mbps ?? 0,
          down_mbps: values.down_mbps === '' ? 0 : values.down_mbps ?? 0,
          obfs: values.obfs,
          obfs_password: values.obfs_password,
          // obfs_password is the canonical form-managed field. Clear the
          // historical alias so an old value cannot reappear after the user
          // explicitly clears or changes the obfuscation password.
          salamander_password: '',
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
          password: values.trojan_password ?? '',
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
          password: values.anytls_password ?? '',
          sni: values.sni,
          alpn: values.alpn ? values.alpn.split(',').map(s => s.trim()).filter(Boolean) : [],
          fingerprint: values.fingerprint,
          insecure: values.insecure || false,
          idle_session_check_interval: values.idle_session_check_interval || '',
          idle_session_timeout: values.idle_session_timeout || '',
          min_idle_session: values.min_idle_session || 0,
        }

      case 'socks5':
      case 'socks5h':
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

      case 'ssh': {
        const rawPrivateKey = typeof values.ssh_private_key === 'string'
          ? values.ssh_private_key.replace(/\r\n/g, '\n')
          : ''
        const privateKeyLines = rawPrivateKey.trim()
          ? rawPrivateKey.replace(/\n+$/, '').split('\n')
          : []
        const hostKeyLines = typeof values.ssh_host_key === 'string'
          ? values.ssh_host_key.replace(/\r\n/g, '\n').split('\n').map((line) => line.trim()).filter(Boolean)
          : []
        const hostKeyAlgorithms = splitMultilineList(values.ssh_host_key_algorithms)
        return {
          ...config,
          user: values.ssh_user ?? '',
          password: values.ssh_password ?? '',
          private_key: privateKeyLines,
          private_key_path: String(values.ssh_private_key_path || '').trim(),
          private_key_passphrase: values.ssh_private_key_passphrase ?? '',
          host_key: hostKeyLines,
          host_key_algorithms: hostKeyAlgorithms,
          client_version: String(values.ssh_client_version || '').trim(),
        }
      }

      case 'wireguard': {
        const peers = parsePeersJSON(values.wireguard_peers_json)
        const usesPeerArray = peers.length > 0
        const hadPeerArray = Array.isArray(initialConfig.peers)
        return {
          ...config,
          // Endpoint-format peer arrays take precedence over the legacy flat
          // single-peer fields. Avoid adding derived flat values while editing
          // an imported peers array, otherwise an unchanged save is no longer
          // lossless and conflicting compatibility fields can change meaning.
          server: usesPeerArray ? undefined : values.server,
          server_port: usesPeerArray ? undefined : values.server_port,
          local_address: splitMultilineList(values.wireguard_local_address),
          private_key: values.wireguard_private_key,
          peer_public_key: usesPeerArray ? undefined : values.wireguard_peer_public_key || '',
          pre_shared_key: usesPeerArray ? undefined : values.wireguard_pre_shared_key || '',
          allowed_ips: usesPeerArray
            ? undefined
            : splitMultilineList(values.wireguard_allowed_ips),
          reserved: usesPeerArray ? undefined : parseReservedInput(values.wireguard_reserved),
          system_interface: values.wireguard_system_interface || false,
          interface_name: values.wireguard_interface_name || '',
          mtu: values.wireguard_mtu || 0,
          workers: values.wireguard_workers || 0,
	          detour:
	            isUpstreamEditor ||
	            (node?.upstream_mode || (initialConfig.detour ? 'legacy' : 'none')) !== 'legacy'
	              ? ''
	              : initialConfig.detour || '',
	          domain_resolver: values.wireguard_domain_resolver || '',
	          domain_resolver_strategy: values.wireguard_domain_resolver_strategy || '',
	          domain_resolver_options:
	            node?.type === 'wireguard'
	              ? normalizedConfig.domain_resolver_options || {}
	              : undefined,
          routing_mark: values.wireguard_routing_mark || '',
          udp_fragment:
            typeof values.wireguard_udp_fragment === 'boolean'
              ? values.wireguard_udp_fragment
              : undefined,
          connect_timeout: values.wireguard_connect_timeout || '',
	          peers: usesPeerArray ? peers : hadPeerArray ? [] : undefined,
	        }
      }

      default:
        return config
    }
  }

  return { initialConfig, initialValues, buildConfig }
}
