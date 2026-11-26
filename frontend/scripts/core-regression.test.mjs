import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { protocolFixtures } from './fixtures/protocol-configs.mjs'
import {
  createProtocolFormModel,
  groupPhysicalNodesInOrder,
  normalizePolicyMemberSources,
  parsePeersJSON,
  parseReservedInput,
  resolvePolicyMemberIds,
  unlinkPolicyGroupInSources,
} from '../src/core/protocolForm.js'
import { EMPTY_CRITERIA, collectFacets, filterNodes } from '../src/core/nodeFilters.js'
import { stableConfigSignature } from '../src/core/nodeModel.js'
import { parseNodesVirtualThreshold, readNodesVirtualThreshold } from '../src/core/nodeList.js'
import { buildColumnWidthStyle, calculateTableMinWidth, clampColumnWidth, columnWidthsEqual, normalizeColumnWidths } from '../src/core/columnWidths.js'
import { buildProxyUrl, matchesSearch, protocolLabel, resolveEffectiveUpstream, serverAddress, summarizeNodes } from '../src/core/nodeView.js'
import { buildGroupFormPlan } from '../src/core/groupForm.js'
import { localizedProxyTypes } from '../src/core/protocolSchema.js'
import { formatUpstreamLabel, policyWarningLabel } from '../src/shared/labels.js'
import { randomCredentials } from '../src/core/credentials.js'
import { ensureLatestFrontendBuild, frontendBuildVersion } from '../src/core/version.js'
import api from '../src/core/api.js'
import { createLatestRequestCoordinator } from '../src/core/latestRequest.js'
import { PROJECT_BRAND, PROJECT_DESCRIPTOR, PROJECT_MARK_FILE, PROJECT_NAME } from '../src/core/project.js'
import baseZh from '../src/core/i18n/base-zh.js'
import baseEn from '../src/core/i18n/base-en.js'
import extraZh from '../src/core/i18n/extra-zh.js'
import extraEn from '../src/core/i18n/extra-en.js'
import { isExpectedSystemBrowserError } from './system-browser-errors.mjs'

test('系统测试只允许同源预期停机与撤销认证错误，不屏蔽正常运行故障', () => {
  const origin = 'http://127.0.0.1:38080'
  const refused = 'Failed to load resource: net::ERR_CONNECTION_REFUSED'
  const error = (endpoint, text = refused) => ({ text, url: origin + endpoint })
  for (const endpoint of ['/api/nodes', '/api/node-groups', '/api/settings', '/api/runtime/status', '/api/version']) {
    assert.equal(isExpectedSystemBrowserError(error(endpoint), { origin, restarting: true }), true)
    assert.equal(isExpectedSystemBrowserError(error(endpoint), { origin }), false)
    assert.equal(isExpectedSystemBrowserError(error(endpoint, 'net::ERR_FAILED'), { origin, restarting: true }), false)
    assert.equal(isExpectedSystemBrowserError({ text: refused, url: 'https://example.com' + endpoint }, { origin, restarting: true }), false)
  }
  assert.equal(isExpectedSystemBrowserError(error('/assets/app.js'), { origin, restarting: true }), false)
  assert.equal(isExpectedSystemBrowserError(error('/api/nodes/1'), { origin, restarting: true }), false)
  assert.equal(isExpectedSystemBrowserError({ text: refused, url: '' }, { origin, restarting: true }), false)
  assert.equal(isExpectedSystemBrowserError(error('/api/nodes', 'status of 401 (Unauthorized)'), { origin, revoking: true }), true)
  assert.equal(isExpectedSystemBrowserError(error('/api/nodes', 'status of 401 (Unauthorized)'), { origin }), false)
  assert.equal(isExpectedSystemBrowserError(error('/api/nodes/batch-import', 'status of 400 (Bad Request)'), { origin }), true)
  assert.equal(isExpectedSystemBrowserError(error('/api/nodes/batch-import', 'status of 500 (Server Error)'), { origin }), false)
})

test('项目品牌使用共享常量，不再依赖旧翻译词条', () => {
  assert.equal(PROJECT_NAME, 'SingBox Proxy Manager')
  assert.equal(PROJECT_NAME, `${PROJECT_BRAND} ${PROJECT_DESCRIPTOR}`)
  assert.equal(Object.hasOwn(baseZh, 'app_title'), false)
  assert.equal(Object.hasOwn(baseEn, 'app_title'), false)
  const brand = readFileSync(new URL('../src/shared/Brand.jsx', import.meta.url), 'utf8')
  assert.ok(brand.includes('{PROJECT_BRAND}') && brand.includes('{PROJECT_DESCRIPTOR}'))
  assert.equal(brand.includes('useTranslation'), false)
})

test('静态浏览器标题、应用名称与紧凑品牌标识对齐', () => {
  const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
  assert.ok(html.includes(`<title>${PROJECT_NAME}</title>`))
  assert.ok(html.includes(`<meta name="application-name" content="${PROJECT_NAME}"`))
  assert.ok(html.includes(`href="/${PROJECT_MARK_FILE}"`))
  const mark = readFileSync(new URL(`../public/${PROJECT_MARK_FILE}`, import.meta.url), 'utf8')
  assert.ok(mark.includes('viewBox="0 0 64 64"'))
  assert.ok(mark.includes(`>${PROJECT_NAME}</title>`))
  assert.equal(/<script\b|(?:href|src)="https?:/i.test(mark), false)
})

test('登录页移除终端装饰与功能说明，不保留废弃样式和词条', () => {
  const login = readFileSync(new URL('../src/panel/Login.jsx', import.meta.url), 'utf8')
  const styles = readFileSync(new URL('../src/panel/dusk.css', import.meta.url), 'utf8')
  assert.equal(/dusk-login__(?:prompt|sub)|login_subtitle|PROJECT_SLUG/.test(login + styles), false)
  assert.equal(Object.hasOwn(baseZh, 'login_subtitle'), false)
  assert.equal(Object.hasOwn(baseEn, 'login_subtitle'), false)
  assert.ok(login.includes('<LoginForm session={session} />'))
  assert.ok(login.includes('<h1 lang="en" data-testid="project-brand">'))
})

test('列表、设置、内核与版本只接收最新请求结果，卸载后不更新状态', async () => {
  const coordinator = createLatestRequestCoordinator()
  const values = []
  const settled = []
  for (const key of ['nodes', 'settings', 'runtime', 'version']) {
    let finish
    let signal
    const pending = coordinator.run(key, (next) => {
      signal = next
      return new Promise((resolve) => { finish = resolve })
    }, (value) => values.push(value), { onSettled: () => settled.push(`${key}:old`) })
    await coordinator.run(key, async () => `${key}:new`, (value) => values.push(value), {
      onSettled: () => settled.push(`${key}:new`),
    })
    assert.equal(signal.aborted, true)
    finish(`${key}:old`)
    await pending
  }
  assert.deepEqual(values, ['nodes:new', 'settings:new', 'runtime:new', 'version:new'])
  assert.deepEqual(settled, values)
  let finish
  const pending = coordinator.run('runtime', () => new Promise((resolve) => { finish = resolve }), (value) => values.push(value))
  coordinator.abortAll()
  finish('after-unmount')
  await pending
  assert.equal(values.includes('after-unmount'), false)
})

for (const fixture of protocolFixtures) {
  test(`协议配置无损保存：${fixture.key}`, () => {
    const touched = new Set([
      ...Object.keys(fixture.editInputs || {}),
      ...(fixture.clearSelections || []),
    ])
    const node = { type: fixture.type, config: JSON.stringify(fixture.config) }
    const model = createProtocolFormModel(node, { isFieldTouched: (name) => touched.has(name) })
    const values = { ...model.initialValues }
    for (const [name, value] of Object.entries(fixture.editInputs || {})) {
      values[name] = typeof values[name] === 'number' && value !== '' ? Number(value) : value
    }
    for (const name of fixture.clearSelections || []) values[name] = ''
    if (fixture.clearPeerArray) {
      values.wireguard_peers_json = ''
      touched.add('wireguard_peers_json')
    }
    if (fixture.switchSecurityToTLS) {
      values.security = 'tls'
      touched.add('security')
    }
    if (fixture.selectVMessCFB) {
      values.vmess_security = 'aes-128-cfb'
      touched.add('vmess_security')
    }
    const actual = JSON.parse(JSON.stringify(model.buildConfig(fixture.type, values)))
    const absent = new Set(fixture.expectedAbsentFields || [])
    for (const [name, original] of Object.entries(fixture.config)) {
      if (absent.has(name)) continue
      const expected = Object.hasOwn(fixture.expectedOverrides || {}, name) ? fixture.expectedOverrides[name] : original
      assert.deepEqual(actual[name], expected, `${fixture.key}.${name}`)
    }
    for (const name of absent) assert.equal(Object.hasOwn(actual, name), false, `${fixture.key}.${name} 应删除`)
    for (const [name, expected] of Object.entries(fixture.expectedSavedFields || {})) {
      assert.deepEqual(actual[name], expected, `${fixture.key}.${name}`)
    }
    if (fixture.peerArray && !fixture.clearPeerArray) {
      for (const name of ['server', 'server_port', 'peer_public_key', 'pre_shared_key', 'allowed_ips', 'reserved']) {
        assert.equal(Object.hasOwn(actual, name), false, `${fixture.key} 不应生成冲突字段 ${name}`)
      }
    }
    if (fixture.flatWireGuard) assert.equal(Object.hasOwn(actual, 'peers'), false)
    if (fixture.nativeResolverObject) {
      assert.deepEqual(actual.domain_resolver_options, { disable_cache: true, rewrite_ttl: 60, client_subnet: '192.0.2.0/24' })
      assert.equal(actual.domain_resolver_strategy, 'prefer_ipv4')
    }
  })
}

test('协议密码与入站密码相互独立', () => {
  for (const type of ['trojan', 'hy2', 'anytls']) {
    const model = createProtocolFormModel({
      id: 1, type, username: 'inbound-user', password: 'inbound-secret',
      config: JSON.stringify({ server: 'example.com', server_port: 443, password: '' }),
    })
    assert.equal(model.buildConfig(type, model.initialValues).password, '')
    assert.equal(model.initialValues.password, 'inbound-secret')
  }
})

test('前置代理编辑器移除旧 detour，节点编辑器保留旧链路', () => {
  const node = { type: 'socks5', upstream_mode: 'legacy', config: '{"server":"example.com","server_port":1080,"detour":"outbound-target"}' }
  const regular = createProtocolFormModel(node)
  const upstream = createProtocolFormModel(node, { isUpstreamEditor: true })
  assert.equal(regular.buildConfig(node.type, regular.initialValues).detour, 'outbound-target')
  assert.equal(upstream.buildConfig(node.type, upstream.initialValues).detour, '')
})

test('WireGuard 支持历史 Base64，拒绝越界、非法 peer 和保留字节', () => {
  assert.deepEqual(parseReservedInput('omje'), [162, 104, 222])
  assert.deepEqual(parseReservedInput('162,104,222'), [162, 104, 222])
  for (const value of ['256,1,2', '-1,1,2', 'not base64']) assert.throws(() => parseReservedInput(value))
  assert.throws(() => parsePeersJSON('{"server":"example.com"}'))
  assert.throws(() => parsePeersJSON('[{"reserved":[256,0,0]}]'))
})

test('WireGuard 多 Peer 只校验实际提交的保留字节', () => {
  const model = createProtocolFormModel(null)
  const peers = [{ server: 'example.com', server_port: 2408, reserved: [162, 104, 222] }]
  const values = { ...model.initialValues, wireguard_peers_json: JSON.stringify(peers), wireguard_reserved: '256' }
  const config = JSON.parse(JSON.stringify(model.buildConfig('wireguard', values)))
  assert.deepEqual(config.peers, peers)
  assert.equal(Object.hasOwn(config, 'reserved'), false)
  assert.throws(() => model.buildConfig('wireguard', { ...values, wireguard_peers_json: '' }), /Reserved bytes/)
  assert.throws(() => model.buildConfig('wireguard', {
    ...values, wireguard_peers_json: JSON.stringify([{ ...peers[0], reserved: [256] }]),
  }), /Reserved bytes/)
})

test('节点列表列宽清洗、范围约束与 CSS 变量映射准确', () => {
  assert.deepEqual(normalizeColumnWidths(null), {})
  assert.deepEqual(normalizeColumnWidths([]), {})
  assert.deepEqual(normalizeColumnWidths({ name: 260.4, port: 12, server: 9999, unknown: 200, ip: 'bad' }), {
    name: 260,
    port: 64,
    server: 560,
  })
  assert.equal(clampColumnWidth('latency', 30), 58)
  assert.equal(clampColumnWidth('latency', 120), 120)
  assert.equal(clampColumnWidth('unknown', 100), null)
  assert.equal(columnWidthsEqual({ name: 200, user: 150 }, { user: 150, name: 200 }), true)
  assert.equal(columnWidthsEqual({ name: 200 }, { name: 201 }), false)
  assert.equal(buildColumnWidthStyle({}, true, 1440), undefined)
  assert.equal(buildColumnWidthStyle({ name: 260 }, false, 390), undefined)
  const style = buildColumnWidthStyle({ name: 260, server: 320 }, true, 1440)
  assert.equal(style['--dusk-col-name'], '260px')
  assert.equal(style['--dusk-col-server'], '320px')
  assert.equal(calculateTableMinWidth({ name: 260, server: 320 }, 1440), 1486)
  assert.equal(calculateTableMinWidth({ name: 280 }, 1080), 1146)
  assert.equal(calculateTableMinWidth({ name: 280 }, 1000), 1003)
  assert.equal(style['--dusk-table-min-width'], '1486px')
})

const nodes = [
  { id: 1, name: 'Tokyo alpha', type: 'ss', remark: 'primary', inbound_port: 30001, username: 'alice', password: 'secret', enabled: true, check_status: 'ok', node_ip: '203.0.113.1', country_code: 'JP', location: 'Tokyo, Japan', latency: 30 },
  { id: 2, name: 'US beta', type: 'vless', inbound_port: 30002, enabled: true, tcp_reuse_enabled: false, check_status: 'untested', node_ip: '', latency: 0 },
  { id: 3, name: 'Tokyo off', type: 'vmess', inbound_port: 30003, enabled: false, node_ip: '203.0.113.3', country_code: 'JP', latency: 50 },
  { id: 4, name: 'Failed delta', type: 'trojan', inbound_port: 30004, enabled: true, check_status: 'failed', check_error: 'dial timeout', node_ip: '', latency: 0 },
]

test('节点渲染阈值默认 120，0 全量，正整数决定批次大小', () => {
  for (const raw of [undefined, null, '', 'invalid', '-1', '1.5', '1e3', '9007199254740992']) {
    assert.equal(parseNodesVirtualThreshold(raw), 120)
  }
  for (const [raw, expected] of [['0', 0], ['1', 1], ['120', 120], ['9999', 9999], [' +12 ', 12]]) {
    assert.equal(parseNodesVirtualThreshold(raw), expected)
    assert.equal(readNodesVirtualThreshold(), 120)
    globalThis.document = { querySelector: (selector) => {
      assert.equal(selector, 'meta[name="sbpm-nodes-virtual-threshold"]')
      return { getAttribute: () => raw }
    } }
    assert.equal(readNodesVirtualThreshold(), expected)
    delete globalThis.document
  }
})

test('全部筛选项准确匹配，不把 SS 混为 VLESS 或 VMess', () => {
  const ids = (criteria) => filterNodes(nodes, { ...EMPTY_CRITERIA, ...criteria }).map((node) => node.id)
  assert.deepEqual(ids({ types: ['ss'] }), [1])
  assert.deepEqual(ids({ status: 'healthy' }), [1])
  assert.deepEqual(ids({ status: 'untested' }), [2])
  assert.deepEqual(ids({ status: 'failed' }), [4])
  assert.deepEqual(ids({ status: 'disabled' }), [3])
  assert.deepEqual(ids({ ports: '30001-30002,30009' }), [1, 2])
  assert.deepEqual(ids({ ports: '30001-30002，30009', search: 'TOKYO primary' }), [1])
  assert.deepEqual(ids({ ports: '30001-30000' }), [])
  assert.deepEqual(ids({ regions: ['jp'], username: 'alice', auth: 'on' }), [1])
  assert.deepEqual(ids({ regions: ['-'], tcpReuse: 'off', auth: 'off' }), [2])
  assert.deepEqual(ids({ search: 'primary' }), [1])
  assert.deepEqual(ids({ search: '203.0.113.1' }), [1])
  assert.deepEqual(ids({ search: 'not-found' }), [])
  const facets = collectFacets(nodes)
  assert.deepEqual(facets.usernames, ['alice'])
  assert.equal(facets.regions.find((region) => region.value === 'jp').count, 2)
  assert.deepEqual(summarizeNodes(nodes), { total: 4, healthy: 1, untested: 1, failed: 1, disabled: 1 })
})

test('去重基于配置语义，连接串安全编码认证字段', () => {
  assert.equal(stableConfigSignature('{"b":2,"a":{"z":1,"x":2}}'), stableConfigSignature('{"a":{"x":2,"z":1},"b":2}'))
  assert.notEqual(stableConfigSignature('{"server":"one"}'), stableConfigSignature('{"server":"two"}'))
  globalThis.window = { location: { hostname: 'panel.example.com' } }
  const link = buildProxyUrl({ inbound_port: 30001, username: 'a@b', password: 'x:# y' }, 'http')
  assert.equal(new URL(link).username, 'a%40b')
  assert.equal(decodeURIComponent(new URL(link).password), 'x:# y')
  assert.equal(new URL(link).hostname, 'panel.example.com')
})

test('生成认证凭据使用安全随机数且无歧义字符', () => {
  const credentials = Array.from({ length: 100 }, randomCredentials)
  assert.equal(new Set(credentials.map((item) => item.secret)).size, 100)
  for (const item of credentials) {
    assert.equal(item.username.length, 8)
    assert.equal(item.secret.length, 16)
    assert.match(item.secret, /^[abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789]+$/)
  }
})

test('版本不匹配仅刷新一次，禁用存储时不陷入刷新循环', () => {
  const calls = []
  const storage = new Map()
  globalThis.window = {
    location: { href: 'https://panel.example.com/', replace: (url) => calls.push(url) },
    sessionStorage: { getItem: (key) => storage.get(key), setItem: (key, value) => storage.set(key, value) },
  }
  assert.equal(ensureLatestFrontendBuild(frontendBuildVersion).refreshed, false)
  assert.equal(ensureLatestFrontendBuild('8.0.1').refreshed, true)
  assert.equal(ensureLatestFrontendBuild('8.0.1').refreshed, false)
  window.sessionStorage = { getItem: () => { throw new Error('disabled') }, setItem: () => { throw new Error('disabled') } }
  assert.equal(ensureLatestFrontendBuild('8.0.2').refreshed, true)
  window.location.href = 'https://panel.example.com/?v=8.0.2'
  assert.equal(ensureLatestFrontendBuild('8.0.2').refreshed, false)
  assert.equal(calls.length, 2)
})

test('过期请求的 401 不清除新登录，当前会话的 401 触发退出', async () => {
  const events = []
  globalThis.window = { dispatchEvent: (event) => events.push(event.type) }
  const adapter = api.defaults.adapter
  let rejectOld
  api.setToken('old-session')
  api.defaults.adapter = (config) => new Promise((_resolve, reject) => {
    rejectOld = () => reject({ config, response: { status: 401 } })
  })
  const oldRequest = api.get('/nodes').catch(() => {})
  api.setToken('new-session')
  rejectOld()
  await oldRequest
  assert.equal(api.defaults.headers.common.Authorization, 'new-session')
  assert.equal(events.length, 0)
  api.defaults.adapter = (config) => Promise.reject({ config, response: { status: 401 } })
  await api.post('/login', { password: 'wrong' }).catch(() => {})
  assert.equal(api.defaults.headers.common.Authorization, 'new-session')
  await api.get('/nodes').catch(() => {})
  assert.equal(api.defaults.headers.common.Authorization, undefined)
  assert.deepEqual(events, ['sbpm-dusk:unauthorized'])
  api.defaults.adapter = adapter
})

test('旧 TUIC 节点开启 reduce_rtt 时初始化为开启 0-RTT，关闭保存后移除旧别名', () => {
  const model = createProtocolFormModel({
    id: 99,
    name: 'legacy-tuic',
    type: 'tuic',
    config: JSON.stringify({
      server: 'tuic.example.com',
      server_port: 443,
      uuid: '2dd61d93-75d8-4da4-ac0e-6aece7eac365',
      password: 'secret',
      reduce_rtt: true,
    }),
  })
  assert.equal(model.initialValues.zero_rtt_handshake, true)
  const saved = model.buildConfig('tuic', { ...model.initialValues, zero_rtt_handshake: false })
  assert.equal(saved.zero_rtt_handshake, false)
  assert.equal(Object.hasOwn(saved, 'reduce_rtt'), false)
})

test('中文与英文环境下策略节点名称、摘要、告警与连接前置标签完整本地化', () => {
  const dictZh = { ...baseZh, ...extraZh }
  const dictEn = { ...baseEn, ...extraEn }
  const tZh = (key) => dictZh[key] ?? key
  const tEn = (key) => dictEn[key] ?? key

  assert.equal(protocolLabel('failover', tZh), '故障转移')
  assert.equal(protocolLabel('loadbalance', tZh), '负载均衡')
  assert.equal(protocolLabel('direct', tZh), '直连')
  assert.equal(protocolLabel('failover', tEn), 'Failover')
  assert.equal(protocolLabel('loadbalance', tEn), 'LoadBalance')
  assert.equal(protocolLabel('direct', tEn), 'Direct')

  const zhTypes = new Map(localizedProxyTypes(tZh).map((item) => [item.value, item.label]))
  const enTypes = new Map(localizedProxyTypes(tEn).map((item) => [item.value, item.label]))
  assert.equal(zhTypes.get('failover'), '故障转移（策略）')
  assert.equal(zhTypes.get('loadbalance'), '负载均衡（策略）')
  assert.equal(zhTypes.get('direct'), '本机直连')
  assert.equal(enTypes.get('failover'), 'Failover (Policy)')
  assert.equal(enTypes.get('loadbalance'), 'Load Balance (Policy)')
  assert.equal(enTypes.get('direct'), 'Direct (Local)')

  const failover = { id: 10, name: 'Policy A', type: 'failover', config: JSON.stringify({ member_ids: [1, 2] }) }
  const lbRR = { id: 11, name: 'Policy B', type: 'loadbalance', config: JSON.stringify({ member_ids: [1, 2, 3], strategy: 'round-robin' }) }
  const lbHash = { id: 12, name: 'Policy C', type: 'loadbalance', config: JSON.stringify({ member_ids: [1, 2], strategy: 'consistent-hashing' }) }
  assert.equal(serverAddress(failover, tZh), '2 · 故障转移')
  assert.equal(serverAddress(lbRR, tZh), '3 · 轮询')
  assert.equal(serverAddress(lbHash, tZh), '2 · 目标粘性')
  assert.equal(serverAddress(failover, tEn), '2 · failover')
  assert.equal(serverAddress(lbRR, tEn), '3 · rr')
  assert.equal(serverAddress(lbHash, tEn), '2 · sticky')
  assert.equal(matchesSearch(failover, '故障转移'), true)
  assert.equal(matchesSearch(lbRR, '负载均衡'), true)
  assert.equal(matchesSearch(lbRR, '轮询'), true)
  assert.equal(matchesSearch(lbRR, '目标粘性'), false)
  assert.equal(matchesSearch(lbHash, '目标粘性'), true)
  assert.equal(matchesSearch(lbHash, 'target sticky'), true)
  assert.equal(matchesSearch(lbHash, '轮询'), false)

  assert.equal(policyWarningLabel('no healthy enabled members', tZh), '没有健康的已启用成员节点')
  assert.equal(policyWarningLabel('disabled members are skipped', tZh), '已自动跳过禁用的成员节点')
  assert.equal(formatUpstreamLabel('group: socks5', tZh, { zh: true, source: 'group' }), '分组前置 · SOCKS5')
  assert.equal(formatUpstreamLabel('global: http', tZh, { zh: true, source: 'global' }), '全局前置 · HTTP')
  assert.equal(formatUpstreamLabel('global: http', tZh, { zh: true, source: 'legacy' }), 'global: http')
  assert.equal(formatUpstreamLabel('global: my-relay', tZh, { zh: true }), 'global: my-relay')
  assert.equal(formatUpstreamLabel('global: http', tEn, { zh: false, source: 'global' }), 'global: http')
  assert.equal(formatUpstreamLabel('custom: vless', tEn, { zh: false, source: 'custom' }), 'custom: vless')
})

test('前置代理前端解析严格遵循：节点自定义 > 分组前置 > 全局前置，且禁用节点不生效', () => {
  const settings = {
    global_upstream_enabled: true,
    global_upstream_type: 'http',
    global_upstream_config: '{"server":"global.example.com","server_port":8080}',
  }
  const groupOn = {
    id: 1,
    upstream_enabled: true,
    upstream_type: 'socks5',
    upstream_config: '{"server":"group.example.com","server_port":1080}',
  }
  const groupOff = {
    id: 2,
    upstream_enabled: false,
    upstream_type: 'socks5',
    upstream_config: '{"server":"group.example.com","server_port":1080}',
  }
  const customNode = {
    enabled: true,
    upstream_mode: 'custom',
    upstream_type: 'trojan',
    upstream_config: '{"server":"node.example.com","server_port":443}',
  }

  assert.equal(resolveEffectiveUpstream(customNode, groupOn, settings).source, 'custom')
  assert.equal(resolveEffectiveUpstream({ enabled: true, upstream_mode: 'group' }, groupOn, settings).source, 'group')
  assert.equal(resolveEffectiveUpstream({ enabled: true, upstream_mode: 'group' }, groupOff, settings).source, 'none')
  assert.equal(resolveEffectiveUpstream({ enabled: true, upstream_mode: 'global' }, groupOn, settings).source, 'group')
  assert.equal(resolveEffectiveUpstream({ enabled: true, upstream_mode: 'global' }, groupOff, settings).source, 'global')
  assert.equal(resolveEffectiveUpstream({ enabled: true, upstream_mode: 'none' }, groupOn, settings).source, 'none')
  const disabledNode = resolveEffectiveUpstream({ enabled: false, upstream_mode: 'global' }, groupOn, settings)
  assert.equal(disabledNode.active, false)
  assert.equal(disabledNode.disabled, true)
  assert.equal(disabledNode.source, 'none')
})

test('分组编辑弹窗仅提交实际修改的字段，避免覆盖并发保存的前置配置并识别同字段冲突', () => {
  const openedSnapshot = {
    upstream_enabled: false,
    upstream_type: 'socks5',
    upstream_config: '{"server":"old.example.com","server_port":1080}',
    upstream_revision: 1,
  }
  const refreshedServerGroup = {
    id: 1,
    name: 'Default',
    allocate_port: true,
    start_port: 30001,
    subscription_url: 'https://sub.example.com/a',
    subscription_auto_update: true,
    subscription_interval_minutes: 60,
    upstream_enabled: true,
    upstream_type: 'http',
    upstream_config: '{"server":"new-by-other-admin.example.com","server_port":8080}',
    upstream_revision: 2,
  }

  // 用户只修改分组名，未触碰前置代理 -> PATCH 不包含任何 upstream_* 或 expected_upstream_* 字段，不覆盖服务端新配置
  const renameOnly = buildGroupFormPlan({
    editing: true,
    values: { name: 'Renamed', allocate_port: true, start_port: 30001, upstream_enabled: false, subscription_url: 'https://sub.example.com/a', subscription_auto_update: false },
    savedGroup: refreshedServerGroup,
    openedSnapshot,
    touchedFields: { name: true },
    upstreamDraft: null,
    upstreamEnabledTouched: false,
  })
  assert.equal(renameOnly.missingUpstream, false)
  assert.equal(renameOnly.upstreamConflict, false)
  assert.deepEqual(renameOnly.payload, { name: 'Renamed' })

  // 仅修改非空订阅地址为另一非空地址，不得覆盖另一会话开启的 subscription_auto_update
  const subUrlReplace = buildGroupFormPlan({
    editing: true,
    values: { name: 'Default', allocate_port: true, start_port: 30001, upstream_enabled: false, subscription_url: 'https://sub.example.com/b', subscription_auto_update: false },
    savedGroup: refreshedServerGroup,
    openedSnapshot,
    touchedFields: { subscription_url: true },
    upstreamDraft: null,
    upstreamEnabledTouched: false,
  })
  assert.deepEqual(subUrlReplace.payload, { subscription_url: 'https://sub.example.com/b' })

  // 清空订阅地址时，应同步关闭 subscription_auto_update
  const subUrlClear = buildGroupFormPlan({
    editing: true,
    values: { name: 'Default', allocate_port: true, start_port: 30001, upstream_enabled: false, subscription_url: '', subscription_auto_update: false },
    savedGroup: refreshedServerGroup,
    openedSnapshot,
    touchedFields: { subscription_url: true },
    upstreamDraft: null,
    upstreamEnabledTouched: false,
  })
  assert.deepEqual(subUrlClear.payload, { subscription_url: '', subscription_auto_update: false })

  // 用户也编辑了前置代理 -> 识别到与服务端并发修改的同字段冲突，并携带服务端基线字段供后端 409 乐观锁校验
  const conflictEdit = buildGroupFormPlan({
    editing: true,
    values: { name: 'Default', allocate_port: true, start_port: 30001, upstream_enabled: true },
    savedGroup: refreshedServerGroup,
    openedSnapshot,
    touchedFields: {},
    upstreamDraft: { type: 'socks5', config: '{"server":"mine.example.com","server_port":1080}' },
    upstreamEnabledTouched: true,
  })
  assert.equal(conflictEdit.missingUpstream, false)
  assert.equal(conflictEdit.upstreamConflict, true)
  assert.deepEqual(conflictEdit.payload, {
    upstream_type: 'socks5',
    upstream_config: '{"server":"mine.example.com","server_port":1080}',
    expected_upstream_revision: 2,
    expected_upstream_enabled: true,
  })

  // 同协议仅改配置时仍成对提交 upstream_type + upstream_config 与乐观锁基线；开启自动分配端口且旧 start_port 为 0 时补带 start_port
  const atomicPair = buildGroupFormPlan({
    editing: true,
    values: { name: 'Default', allocate_port: true, start_port: 30001, upstream_enabled: true },
    savedGroup: { ...refreshedServerGroup, allocate_port: false, start_port: 0 },
    openedSnapshot: {
      upstream_enabled: refreshedServerGroup.upstream_enabled,
      upstream_type: refreshedServerGroup.upstream_type,
      upstream_config: refreshedServerGroup.upstream_config,
      upstream_revision: refreshedServerGroup.upstream_revision,
    },
    touchedFields: { allocate_port: true },
    upstreamDraft: { type: 'http', config: '{"server":"updated-http.example.com","server_port":8080}' },
    upstreamEnabledTouched: false,
  })
  assert.equal(atomicPair.upstreamConflict, false)
  assert.deepEqual(atomicPair.payload, {
    allocate_port: true,
    start_port: 30001,
    upstream_type: 'http',
    upstream_config: '{"server":"updated-http.example.com","server_port":8080}',
    expected_upstream_revision: 2,
    expected_upstream_enabled: true,
  })

  // A -> B -> A 修订号递增即使内容改回原值也能在本地检测冲突
  const abaConflict = buildGroupFormPlan({
    editing: true,
    values: { name: 'Default', allocate_port: true, start_port: 30001, upstream_enabled: false },
    savedGroup: { ...refreshedServerGroup, upstream_enabled: false, upstream_type: 'socks5', upstream_config: '{"server":"old.example.com","server_port":1080}', upstream_revision: 3 },
    openedSnapshot,
    touchedFields: {},
    upstreamDraft: { type: 'socks5', config: '{"server":"mine.example.com","server_port":1080}' },
    upstreamEnabledTouched: false,
  })
  assert.equal(abaConflict.upstreamConflict, true)

  // 分组级「保留入站端口」：新建随 allocate_port 归一化，编辑仅在触碰时提交并正确触发关闭二次确认
  const createdNoPort = buildGroupFormPlan({
    editing: false,
    values: { name: 'NoPort', allocate_port: false, preserve_inbound_ports: true, start_port: 30001 },
    savedGroup: null,
    openedSnapshot: null,
    touchedFields: {},
    upstreamDraft: null,
    upstreamEnabledTouched: false,
  })
  assert.equal(createdNoPort.payload.allocate_port, false)
  assert.equal(createdNoPort.payload.preserve_inbound_ports, false)

  const preserveGroup = { ...refreshedServerGroup, preserve_inbound_ports: true }
  const renamePreserveGroup = buildGroupFormPlan({
    editing: true,
    values: { name: 'Renamed', allocate_port: true, preserve_inbound_ports: true, start_port: 30001 },
    savedGroup: preserveGroup,
    openedSnapshot,
    touchedFields: { name: true },
    upstreamDraft: null,
    upstreamEnabledTouched: false,
  })
  assert.deepEqual(renamePreserveGroup.payload, { name: 'Renamed' })
  assert.equal(renamePreserveGroup.disablingPreserve, false)

  const disablePreserve = buildGroupFormPlan({
    editing: true,
    values: { name: 'Default', allocate_port: true, preserve_inbound_ports: false, start_port: 30001 },
    savedGroup: preserveGroup,
    openedSnapshot,
    touchedFields: { preserve_inbound_ports: true },
    upstreamDraft: null,
    upstreamEnabledTouched: false,
  })
  assert.deepEqual(disablePreserve.payload, { preserve_inbound_ports: false })
  assert.equal(disablePreserve.disablingPreserve, true)

  const disableAllocateInstead = buildGroupFormPlan({
    editing: true,
    values: { name: 'Default', allocate_port: false, preserve_inbound_ports: false, start_port: 30001 },
    savedGroup: preserveGroup,
    openedSnapshot,
    touchedFields: { allocate_port: true, preserve_inbound_ports: true },
    upstreamDraft: null,
    upstreamEnabledTouched: false,
  })
  assert.deepEqual(disableAllocateInstead.payload, { allocate_port: false })
  assert.equal(disableAllocateInstead.disablingPreserve, false)
})

test('策略整组关联跟随组内 sort_order 排序，断链原地展开去重并在保存时彻底移除 member_sources', () => {
  const all = [
    { id: 30, group_id: 2, sort_order: 0, type: 'ss', name: 'G2-30' },
    { id: 10, group_id: 2, sort_order: 1, type: 'vless', name: 'G2-10' },
    { id: 25, group_id: 2, sort_order: 1, type: 'vmess', name: 'G2-25' },
    { id: 20, group_id: 2, sort_order: 0, type: 'failover', name: 'Nested Policy' },
    { id: 5, group_id: 1, sort_order: 0, type: 'vmess', name: 'G1-5' },
    { id: 40, group_id: 3, sort_order: 0, type: 'trojan', name: 'G3-40' },
  ]
  // sort_order 优先，同 sort_order 时按 id 兜底升序
  assert.deepEqual(groupPhysicalNodesInOrder(all, 2, 99).map((n) => n.id), [30, 10, 25])
  assert.deepEqual(normalizePolicyMemberSources(undefined, [5, 10, 5]), [
    { type: 'node', id: 5 },
    { type: 'node', id: 10 },
  ])
  const linked = [{ type: 'node', id: 10 }, { type: 'group', id: 2 }, { type: 'group', id: 3 }]
  assert.deepEqual(resolvePolicyMemberIds(linked, all, 99), [10, 30, 25, 40])
  const unlinkedG2 = unlinkPolicyGroupInSources(linked, 2, all, 99)
  assert.deepEqual(unlinkedG2, [
    { type: 'node', id: 10 },
    { type: 'node', id: 30 },
    { type: 'node', id: 25 },
    { type: 'group', id: 3 },
  ])
  // 分组在前、重复手工节点在后：断链必须保留首次出现位置 [30, 10, 25]，跟随组内排序
  const groupBeforeDup = [{ type: 'group', id: 2 }, { type: 'node', id: 10 }]
  assert.deepEqual(resolvePolicyMemberIds(groupBeforeDup, all, 99), [30, 10, 25])
  assert.deepEqual(unlinkPolicyGroupInSources(groupBeforeDup, 2, all, 99), [
    { type: 'node', id: 30 },
    { type: 'node', id: 10 },
    { type: 'node', id: 25 },
  ])
  const unlinkedAll = unlinkPolicyGroupInSources(unlinkedG2, 3, all, 99)
  assert.deepEqual(unlinkedAll, [
    { type: 'node', id: 10 },
    { type: 'node', id: 30 },
    { type: 'node', id: 25 },
    { type: 'node', id: 40 },
  ])

  const existingPolicy = {
    id: 99,
    type: 'failover',
    inbound_mode: 'https',
    config: JSON.stringify({
      member_ids: [30, 10, 25],
      member_sources: [{ type: 'group', id: 2 }],
    }),
  }
  const model = createProtocolFormModel(existingPolicy)
  assert.equal(model.initialValues.inbound_mode, 'https')
  assert.deepEqual(model.initialValues.policy_member_sources, [{ type: 'group', id: 2 }])

  // 保持整组关联时保留 member_sources
  const kept = model.buildConfig('failover', model.initialValues)
  assert.deepEqual(kept.member_sources, [{ type: 'group', id: 2 }])

  // 断链后变为纯手工节点，buildConfig 必须移除 member_sources，避免回退旧关联
  const detached = model.buildConfig('failover', {
    ...model.initialValues,
    policy_member_sources: [{ type: 'node', id: 30 }, { type: 'node', id: 10 }],
  })
  assert.equal(Object.hasOwn(detached, 'member_sources'), false)
  assert.deepEqual(detached.member_ids, [30, 10])
})
