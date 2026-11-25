import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { launchBrowser, stopProcess } from './test-harness.mjs'
import { assertProjectBrand, clickControl, clickText, closeOverlay, confirmDialog, editNode, openBatchImport, openNodeMenu, saveNodeForm, selectOption, setInput, sleep, toggle, waitOverlaysClosed } from './ui-driver.mjs'
import { assertColumnOrder, assertCopiedText, assertDetailLayoutOrderAndBalance, assertEmptyInboundPassword, assertFullNodeName, assertInboundUsername, assertMaskedInboundPassword, assertRevealedInboundPassword, assertRouteId, assertUnclippedTableHeaders } from './node-display-assertions.mjs'

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const VERSION = JSON.parse(fs.readFileSync(path.join(FRONTEND, 'package.json'), 'utf8')).version
const TOKEN = 'dusk-regression-session'

const makeNode = (id, patch = {}) => ({
  id, group_id: 1, name: `Node ${String(id).padStart(3, '0')}`, remark: '', type: 'ss',
  config: JSON.stringify({ server: `n${id}.example.com`, server_port: 443, method: 'aes-128-gcm', password: 'protocol-secret' }),
  inbound_port: 30000 + id, inbound_port_pinned: false, sort_order: id - 1,
  username: 'alice', password: 'inbound-secret', enabled: true, tcp_reuse_enabled: true,
  upstream_mode: 'none', upstream_type: '', upstream_config: '',
  check_status: 'ok', check_error: '',
  node_ip: '203.0.113.10', location: 'Tokyo, Japan', country_code: 'JP', latency: 40,
  ...patch,
})

const listen = (server) => new Promise((resolve, reject) => {
  server.once('error', reject)
  server.listen(0, '127.0.0.1', () => resolve(server.address().port))
})

const freePort = async () => {
  const server = net.createServer()
  const port = await listen(server)
  await new Promise((resolve) => server.close(resolve))
  return port
}

const eventually = async (predicate, message, timeout = 15000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (await predicate()) return
    await sleep(40)
  }
  throw new Error(message)
}

const readBody = async (request) => {
  const chunks = []
  for await (const chunk of request) chunks.push(chunk)
  const body = Buffer.concat(chunks).toString()
  return body ? JSON.parse(body) : null
}

function createFixture() {
  const state = {
    groups: [{
      id: 1, name: 'Default', sort_order: 0, allocate_port: true, start_port: 30001,
      upstream_enabled: false, upstream_type: '', upstream_config: '', upstream_revision: 0,
      upstream_ip: '', upstream_location: '', upstream_country_code: '', upstream_latency: 0, upstream_error: '',
      subscription_url: '', subscription_auto_update: false, subscription_interval_minutes: 60, subscription_last_error: '',
    }],
    nodes: [makeNode(1), makeNode(2, { type: 'vless', check_status: 'untested', country_code: '', location: '', node_ip: '', latency: 0, tcp_reuse_enabled: false }), makeNode(3, { type: 'vmess', enabled: false, country_code: 'US', location: 'Boston, United States' }), makeNode(4)],
    settings: { preserve_inbound_ports: false, global_upstream_enabled: false, global_upstream_type: '', global_upstream_config: '', node_list_column_widths: {} },
    runtime: { running: true, degraded: false, pid: 1234 }, version: VERSION, update: { available: false },
    connectionsSnapshot: {
      runtime_epoch: '1234-1', sampled_at: new Date().toISOString(), active_connections: 3, client_count: 1, route_count: 2,
      total_upload_bps: 18400, total_download_bps: 262144,
      routes: [
        { key: '10.0.0.5|30001|node:1', client_ip: '10.0.0.5', inbound_port: 30001, inbound_node_id: 1, inbound_node_name: 'Node 001', route_id: '', outbound_node_id: 1, outbound_node_name: 'Node 001', active_count: 2, upload_bps: 12000, download_bps: 200000 },
        { key: '10.0.0.5|30002|node:4|member:1', client_ip: '10.0.0.5', inbound_port: 30002, inbound_node_id: 2, inbound_node_name: 'Node 002', route_id: 'alice+30004', outbound_node_id: 4, outbound_node_name: 'JP Failover', policy_member_id: 1, policy_member_name: 'Node 001', upstream_source: 'custom', upstream_label: 'custom: socks5', active_count: 1, upload_bps: 6400, download_bps: 62144 },
      ],
    },
    sseActive: 0, sseOpened: 0, sseClosed: 0,
    logsSettings: { enabled: false, min_level: 'INFO', store_mode: 'local_sqlite', remote_driver: 'postgres', local_sqlite_path: '', remote_dsn_masked: '', remote_url_masked: '', has_remote_dsn: false, has_remote_token: false, retention_days: 7, max_size_mb: 200, status: { active: false, driver: 'memory', size_bytes: 0, dropped_events: 0, last_error: '' } },
    logsEvents: [
      { id: 'e2', ts: '2026-11-25T01:02:00Z', source: 'singbox', level: 'WARN', client_ip: '10.0.0.5', inbound_port: 30001, outbound_name: 'Node 001', message: 'connection reset by peer' },
      { id: 'e1', ts: '2026-11-25T01:01:00Z', source: 'panel', level: 'INFO', client_ip: '', message: 'sing-box started successfully' },
    ],
    logsTraffic: { persisted: true, upload_bytes: 2048000, download_bytes: 10485760, total_bytes: 12533760, items: [{ client_ip: '10.0.0.5', inbound_port: 30001, route_id: '', outbound_node_id: 1, outbound_node_name: 'Node 001', policy_member_name: '', upload_bytes: 2048000, download_bytes: 10485760, total_bytes: 12533760 }] },
    logSettingsFailMessage: '',
    requests: [], loads: [], flags: [], orders: [], deletes: [], auth: [], imports: [], saves: [], upstreams: [], settingsSaves: [], checks: [], groupMutations: [], moves: [],
    expectedFailures: new Set(), activeChecks: 0, peakChecks: 0, checkDelay: 20, checkStatuses: {}, importDelay: 0,
    importResult: null, flagFailure: false, nextLoads: [], loginSecret: 'dusk-regression-admin', revoked: false,
  }
  const reorderPorts = () => {
    if (state.settings.preserve_inbound_ports) return
    const used = new Set(state.nodes.filter((node) => node.inbound_port_pinned && node.inbound_port > 0).map((node) => node.inbound_port))
    for (const group of state.groups) {
      const groupNodes = state.nodes.filter((node) => (node.group_id ?? state.groups[0]?.id) === group.id)
      let port = group.start_port || 30001
      groupNodes.forEach((node, index) => {
        node.sort_order = index
        if (group.allocate_port === false) {
          node.inbound_port = 0
          node.inbound_port_pinned = false
          return
        }
        if (node.inbound_port_pinned && node.inbound_port > 0) return
        while (used.has(port)) port += 1
        node.inbound_port = port
        used.add(port)
        port += 1
      })
    }
  }
  const server = http.createServer(async (request, response) => {
    const url = new URL(request.url, 'http://127.0.0.1')
    const route = url.pathname
    const body = await readBody(request)
    const method = request.method
    state.requests.push({ method, route, body, authorization: request.headers.authorization || '' })
    const send = (status, payload) => {
      if (response.destroyed || response.writableEnded) return
      response.writeHead(status, { 'Content-Type': 'application/json' })
      response.end(JSON.stringify(payload))
    }
    if (route === '/api/version') return send(200, { version: state.version, update: state.update })
    if (route === '/api/login') {
      if (body?.password !== state.loginSecret) {
        state.expectedFailures.add(route)
        return send(401, { error: 'invalid password' })
      }
      state.revoked = false
      return send(200, { token: TOKEN, expiry: Math.floor(Date.now() / 1000) + 3600 })
    }
    if (request.headers.authorization?.replace(/^Bearer /, '') !== TOKEN || state.revoked) {
      state.expectedFailures.add(route)
      return send(401, { error: 'unauthorized' })
    }
    if (route === '/api/logout') { state.revoked = true; return send(200, {}) }
    if (route === '/api/node-groups' && method === 'GET') {
      const enriched = state.groups.map((group) => ({
        ...group,
        node_count: state.nodes.filter((node) => (node.group_id ?? state.groups[0]?.id) === group.id).length,
        enabled_node_count: state.nodes.filter((node) => (node.group_id ?? state.groups[0]?.id) === group.id && node.enabled !== false).length,
      }))
      return send(200, enriched)
    }
    if (route === '/api/node-groups' && method === 'POST') {
      const id = Math.max(0, ...state.groups.map((g) => g.id)) + 1
      const created = { id, sort_order: state.groups.length, subscription_last_error: '', ...body }
      state.groups.push(created)
      state.groupMutations.push({ op: 'create', body })
      return send(201, created)
    }
    if (route === '/api/node-groups/reorder' && method === 'POST') {
      const byId = new Map(state.groups.map((g) => [g.id, g]))
      state.groups = body.ids.map((id, idx) => ({ ...byId.get(id), sort_order: idx }))
      return send(200, {})
    }
    const groupMatch = route.match(/^\/api\/node-groups\/(\d+)(?:\/(.*))?$/)
    if (groupMatch) {
      const gid = Number(groupMatch[1])
      const action = groupMatch[2]
      const group = state.groups.find((g) => g.id === gid)
      if (!group) return send(404, { error: 'group not found' })
      if (action === 'subscription/refresh' && method === 'POST') {
        group.subscription_last_updated_at = new Date().toISOString()
        group.subscription_last_error = ''
        return send(200, { added: 1, updated: 0, removed: 0, unchanged: 0, warnings: [] })
      }
      if (action === 'upstream/check-ip' && method === 'POST') {
        Object.assign(group, {
          upstream_ip: '198.51.100.31',
          upstream_latency: 18,
          upstream_location: 'Tokyo, Japan',
          upstream_country_code: 'JP',
          upstream_error: '',
        })
        return send(200, { ip: '198.51.100.31', latency: 18, location: 'Tokyo, Japan', country_code: 'JP' })
      }
      if (method === 'PATCH') {
        const revMismatch = Object.hasOwn(body || {}, 'expected_upstream_revision') && body.expected_upstream_revision !== group.upstream_revision
        const enabledMismatch = Object.hasOwn(body || {}, 'expected_upstream_enabled') && body.expected_upstream_enabled !== group.upstream_enabled
        if (revMismatch || enabledMismatch) {
          state.expectedFailures.add(route)
          return send(409, { code: 'upstream_conflict', error: 'upstream_conflict', upstream_revision: group.upstream_revision, upstream_enabled: group.upstream_enabled })
        }
        const defChanged = (
          (Object.hasOwn(body || {}, 'upstream_type') && body.upstream_type !== group.upstream_type) ||
          (Object.hasOwn(body || {}, 'upstream_config') && body.upstream_config !== group.upstream_config)
        )
        const patchFields = { ...(body || {}) }
        delete patchFields.expected_upstream_revision
        delete patchFields.expected_upstream_enabled
        if (defChanged) {
          Object.assign(group, { upstream_revision: (group.upstream_revision || 0) + 1, upstream_ip: '', upstream_location: '', upstream_country_code: '', upstream_latency: 0, upstream_error: '' })
        }
        Object.assign(group, patchFields)
        reorderPorts()
        state.groupMutations.push({ op: 'update', id: gid, body })
        return send(200, group)
      }
      if (method === 'DELETE') {
        if (state.groups.length <= 1) return send(409, { error: 'last group', code: 'last_group' })
        const mode = url.searchParams.get('mode')
        const targetId = Number(url.searchParams.get('target_group_id') || 0)
        const members = state.nodes.filter((n) => n.group_id === gid)
        if (members.length > 0 && !mode) return send(409, { error: 'group not empty', code: 'group_not_empty', node_count: members.length })
        if (mode === 'move' && targetId) {
          for (const node of members) node.group_id = targetId
        } else if (mode === 'delete_nodes') {
          state.nodes = state.nodes.filter((n) => n.group_id !== gid)
        }
        state.groups = state.groups.filter((g) => g.id !== gid)
        reorderPorts()
        state.groupMutations.push({ op: 'delete', id: gid, mode, targetId })
        return send(200, {})
      }
    }
    if (route === '/api/nodes/batch-move' && method === 'POST') {
      state.moves.push(body)
      for (const node of state.nodes) {
        if (body.ids.includes(node.id)) node.group_id = body.group_id
      }
      reorderPorts()
      return send(200, {})
    }
    if (route === '/api/connections' && method === 'GET') {
      return send(200, state.connectionsSnapshot)
    }
    if (route === '/api/connections/stream' && method === 'GET') {
      state.sseActive += 1
      state.sseOpened += 1
      response.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' })
      response.write(`event: snapshot\ndata: ${JSON.stringify(state.connectionsSnapshot)}\n\n`)
      const timer = setInterval(() => {
        if (!response.writableEnded) response.write(`event: snapshot\ndata: ${JSON.stringify(state.connectionsSnapshot)}\n\n`)
      }, 600)
      response.once('close', () => {
        clearInterval(timer)
        state.sseActive = Math.max(0, state.sseActive - 1)
        state.sseClosed += 1
      })
      return
    }
    if (route === '/api/logs/settings' && method === 'GET') return send(200, state.logsSettings)
    if (route === '/api/logs/settings' && method === 'PUT') {
      if (state.logSettingsFailMessage) {
        state.expectedFailures.add(route)
        return send(400, { error: state.logSettingsFailMessage })
      }
      const next = { ...state.logsSettings, enabled: Boolean(body.enabled), min_level: body.min_level || 'INFO', store_mode: body.store_mode || 'local_sqlite', remote_driver: body.remote_driver || 'postgres', retention_days: body.retention_days ?? 7, max_size_mb: body.max_size_mb ?? 200 }
      if (body.store_mode === 'independent' && body.remote_dsn) {
        next.remote_dsn_masked = body.remote_dsn.replace(/:\/\/([^:@]+):([^@]+)@/, '://$1:***@')
        next.has_remote_dsn = true
      }
      next.status = { active: next.enabled, driver: next.enabled ? next.store_mode : 'memory', size_bytes: next.enabled ? 4096 : 0, dropped_events: 0, last_error: '' }
      state.logsSettings = next
      return send(200, next)
    }
    if (route === '/api/logs/traffic' && method === 'GET') {
      return send(200, { ...state.logsTraffic, persisted: state.logsSettings.enabled })
    }
    if (route === '/api/logs' && method === 'GET') {
      const src = url.searchParams.get('source') || ''
      const lvl = url.searchParams.get('level') || ''
      const q = (url.searchParams.get('q') || '').toLowerCase()
      const ip = url.searchParams.get('client_ip') || ''
      const items = state.logsEvents.filter((item) =>
        (!src || item.source === src) &&
        (!lvl || item.level === lvl) &&
        (!ip || item.client_ip === ip) &&
        (!q || item.message.toLowerCase().includes(q))
      )
      return send(200, { items, next_cursor: '', storage_mode: state.logsSettings.enabled ? state.logsSettings.store_mode : 'memory', persisted: state.logsSettings.enabled, dropped_events: 0 })
    }
    if (route === '/api/nodes' && method === 'GET') {
      const plan = state.nextLoads.shift()
      const snapshot = structuredClone(plan?.nodes || state.nodes)
      state.loads.push(snapshot)
      if (plan?.delay) await sleep(plan.delay)
      return send(200, snapshot)
    }
    if (route === '/api/settings' && method === 'GET') return send(200, state.settings)
    if (route === '/api/runtime/status') return send(200, state.runtime)
    if (route === '/api/runtime/restart') { state.runtime = { running: true, degraded: false }; return send(200, { status: state.runtime }) }
    if (route === '/api/settings/upstream/check-ip') {
      Object.assign(state.settings, { global_upstream_ip: '198.51.100.30', global_upstream_latency: 20, global_upstream_location: 'Test', global_upstream_country_code: 'US' })
      return send(200, { ip: '198.51.100.30', latency: 20 })
    }
    if (route === '/api/settings' && method === 'PUT') {
      state.settingsSaves.push(body)
      Object.assign(state.settings, body)
      const runtimeFields = ['start_port', 'preserve_inbound_ports', 'global_upstream_enabled', 'global_upstream_type', 'global_upstream_config']
      if (runtimeFields.some((key) => Object.hasOwn(body || {}, key))) reorderPorts()
      if (body.admin_password) {
        state.loginSecret = body.admin_password
        state.revoked = true
        return send(200, { password_changed: true })
      }
      return send(200, { node_list_column_widths: state.settings.node_list_column_widths || {} })
    }
    if (route === '/api/nodes/reorder') {
      state.orders.push(body)
      const byID = new Map(state.nodes.map((node) => [node.id, node]))
      if (body.group_id != null) {
        const reordered = body.nodes.map((item) => byID.get(item.id)).filter(Boolean)
        const ids = new Set(reordered.map((node) => node.id))
        let cursor = 0
        state.nodes = state.nodes.map((node) => (ids.has(node.id) ? reordered[cursor++] : node))
      } else {
        state.nodes = body.nodes.map((item) => byID.get(item.id))
      }
      reorderPorts()
      return send(200, {})
    }
    if (route === '/api/nodes/batch-delete') {
      state.deletes.push(body.ids)
      state.nodes = state.nodes.filter((node) => !body.ids.includes(node.id))
      reorderPorts()
      return send(200, {})
    }
    if (route === '/api/nodes/batch-auth') {
      state.auth.push(body)
      for (const node of state.nodes) if (body.node_ids.includes(node.id)) {
        node.username = body.auth_enabled ? body.username : ''
        node.password = body.auth_enabled ? body.password : ''
      }
      return send(200, {})
    }
    if (route === '/api/nodes/batch-export') {
      return send(200, { failed: 0, results: body.ids.map((id) => ({ id, success: true, link: `socks5://127.0.0.1:1080#${id}` })) })
    }
    if (route === '/api/nodes/batch-import') {
      state.imports.push(body)
      if (state.importDelay) await sleep(state.importDelay)
      const result = state.importResult || { total: 1, success: 1, failed: 0, results: [{ id: 10, success: true, name: 'imported' }] }
      for (const item of result.results || []) if (item.success && !state.nodes.some((node) => node.id === item.id)) {
        state.nodes.push(makeNode(item.id, { name: item.name || 'imported', enabled: body.enabled }))
      }
      if (result.status >= 400) state.expectedFailures.add(route)
      return send(result.status || 200, result)
    }
    if (route === '/api/parse-link') return send(200, { type: 'socks5', name: 'Parsed link', config: '{"server":"127.0.0.1","server_port":1080}' })
    if (route === '/api/nodes' && method === 'POST') {
      const id = Math.max(0, ...state.nodes.map((node) => node.id)) + 1
      const groupId = body.group_id || state.groups[0]?.id || 1
      const node = makeNode(id, { ...body, group_id: groupId })
      state.saves.push(body)
      state.nodes.push(node)
      reorderPorts()
      return send(201, node)
    }
    const match = route.match(/^\/api\/nodes\/(\d+)(?:\/(.*))?$/)
    if (match) {
      const id = Number(match[1])
      const action = match[2]
      const node = state.nodes.find((item) => item.id === id)
      if (!node) return send(404, { error: 'node not found' })
      if (action === 'check-ip') {
        state.checks.push(id)
        state.activeChecks += 1
        state.peakChecks = Math.max(state.peakChecks, state.activeChecks)
        let counted = true
        response.once('close', () => { if (counted) { counted = false; state.activeChecks -= 1 } })
        await sleep(state.checkDelay)
        const status = state.checkStatuses[id] || 200
        if (status >= 400) {
          state.expectedFailures.add(route)
          const failedPayload = status === 429 || status >= 500
            ? { error: `check failed ${status}`, check_status: 'failed', check_error: `check failed ${status}`, upstream: { error: 'upstream failed' } }
            : { error: `check failed ${status}`, upstream: { error: 'upstream failed' } }
          if (status === 429 || status >= 500) Object.assign(node, { check_status: 'failed', check_error: `check failed ${status}`, node_ip: '', location: '', country_code: '', latency: 0 })
          return send(status, failedPayload)
        }
        Object.assign(node, { check_status: 'ok', check_error: '', node_ip: '198.51.100.8', country_code: 'US', location: 'Boston, United States', latency: 50 })
        return send(200, { ip: node.node_ip, country_code: node.country_code, location: node.location, latency: node.latency, check_status: 'ok', check_error: '' })
      }
      if (action === 'remark') { node.remark = body.remark; return send(200, {}) }
      if (action === 'port-pin') { node.inbound_port_pinned = body.pinned; return send(200, { pinned: body.pinned }) }
      if (action === 'upstream') { state.upstreams.push(body); Object.assign(node, body); return send(200, node) }
      if (action === 'replace') { state.saves.push(body); node.config = '{"server":"replacement.example.com","server_port":1080}'; if (body.update_name) node.name = 'Replaced'; return send(200, node) }
      if (action === 'export') return send(200, { link: `socks5://127.0.0.1:1080#${id}` })
      if (method === 'PATCH') {
        state.flags.push(body)
        if (state.flagFailure) { state.expectedFailures.add(route); return send(500, { error: 'runtime apply failed' }) }
        Object.assign(node, body)
        return send(200, node)
      }
      if (method === 'PUT') { state.saves.push(body); Object.assign(node, body); reorderPorts(); return send(200, node) }
      if (method === 'DELETE') { state.nodes = state.nodes.filter((item) => item.id !== id); reorderPorts(); return send(200, {}) }
      return send(200, node)
    }
    send(404, { error: 'fixture route not implemented', route, method })
  })
  return { server, state }
}

const selectNodes = async (page, ids) => {
  for (const id of ids) await clickControl(page, `[data-testid="node-select-${id}"]`)
}

const rowIDs = (page) => page.$$eval('[data-row-id]', (rows) => rows.map((row) => Number(row.dataset.rowId)))

const expectRows = async (page, expected) => {
  await page.waitForFunction((ids) => JSON.stringify(Array.from(document.querySelectorAll('[data-row-id]'), (row) => Number(row.dataset.rowId))) === JSON.stringify(ids), {}, expected)
}

const switchMode = async (page, mode) => clickControl(page, `[data-testid="node-upstream-mode"] [data-mode="${mode}"]`)

const scenarios = {
  async brand({ page, state }) {
    for (const language of ['中文', 'English']) {
      await clickControl(page, `button[title="${language}"]`)
      for (const width of [320, 360, 390, 480, 481, 768, 899, 900, 1024, 1200, 1440]) {
        await page.setViewport({ width, height: 900 })
        await assertProjectBrand(page)
        assert.ok(await page.$eval('[data-testid="open-palette"]', (button) => button.getAttribute('aria-label')), '紧凑命令按钮必须有辅助技术名称')
      }
    }
    await clickControl(page, '[data-testid="logout"]')
    await page.waitForSelector('[data-testid="login-form"]', { visible: true })
    for (const language of ['中文', 'English']) {
      await clickControl(page, `button[title="${language}"]`)
      for (const width of [320, 390, 768, 1440]) {
        await page.setViewport({ width, height: 900 })
        await assertProjectBrand(page, { login: true })
      }
    }
    await setInput(page, '[data-testid="login-secret"]', state.loginSecret)
    await clickControl(page, '[data-testid="login-submit"]')
    await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
    await assertProjectBrand(page)
  },

  async dnd({ page, state }) {
    const before = state.nodes.map((node) => node.id)
    const from = await page.$('[data-row-id="1"] .sb-drag')
    const to = await page.$('[data-row-id="4"] .sb-drag')
    const a = await from.boundingBox()
    const b = await to.boundingBox()
    await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2)
    await page.mouse.down()
    await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 20 })
    await page.mouse.up()
    await eventually(() => state.orders.length === 1, '拖拽未提交排序')
    assert.notDeepEqual(state.nodes.map((node) => node.id), before)
    assert.equal(state.orders[0].nodes.length, before.length)
    await expectRows(page, state.nodes.map((node) => node.id))
    await page.locator(`[data-row-id="${state.nodes[0].id}"] .sb-drag`).click()
    await page.keyboard.press('Space')
    await page.waitForSelector('.is-dragging', { visible: true })
    await sleep(160)
    await page.keyboard.press('ArrowDown')
    await sleep(120)
    await page.keyboard.press('Space')
    await eventually(() => state.orders.length === 2, '键盘排序未生效')
    assert.equal(new Set(state.orders[1].nodes.map((node) => node.id)).size, before.length)
  },

  async 'dnd-virtual'({ page, state, refresh }) {
    state.nodes = Array.from({ length: 360 }, (_, index) => makeNode(index + 1))
    await refresh()
    assert.ok((await rowIDs(page)).length < 360, '分批列表一次挂载了全部节点')
    await clickControl(page, '[data-testid="open-palette"]')
    await setInput(page, '.dusk-palette__input input', 'Node 350')
    await clickText(page, 'Node 350', { root: '.dusk-palette', exact: false })
    await page.waitForSelector('[data-testid="node-inspector"][data-node-id="350"]', { visible: true })
    await clickControl(page, '[data-testid="node-menu-350"]')
    await clickText(page, 'Move to top', { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
    await eventually(() => state.nodes[0].id === 350, '命令面板定位后的排序未生效')
    assert.equal(state.orders[0].nodes.length, 360)
    await page.$eval('.dusk__scroll', (element) => { element.scrollTop = element.scrollHeight })
    await eventually(async () => (await rowIDs(page)).length === 360, '滚动未加载剩余节点')
  },

  async 'preserve-ports'({ page, state, refresh }) {
    state.nodes[1].inbound_port_pinned = true
    state.nodes[1].inbound_port = 31002
    await refresh()
    await openNodeMenu(page, 4, 'Move to top')
    await eventually(() => state.orders.length === 1, '端口回归未收到排序请求')
    assert.equal(state.nodes.find((node) => node.id === 2).inbound_port, 31002)
    assert.equal(state.nodes.find((node) => node.id === 4).inbound_port, 30001)
    await clickControl(page, '[data-testid="node-port-pin-1"]')
    await eventually(() => state.nodes.find((node) => node.id === 1).inbound_port_pinned, '固定端口未保存')
    await editNode(page, 1)
    assert.equal(await page.$eval('#inbound_port', (input) => input.disabled), false)
    await setInput(page, '#inbound_port', '32001')
    await saveNodeForm(page)
    await page.waitForSelector('[data-testid="node-form"]', { hidden: true })
    await waitOverlaysClosed(page)
    assert.equal(state.nodes.find((node) => node.id === 1).inbound_port, 32001)
    state.settings.preserve_inbound_ports = true
    await page.reload({ waitUntil: 'networkidle2' })
    const ports = new Map(state.nodes.map((node) => [node.id, node.inbound_port]))
    await openNodeMenu(page, 3, 'Move to top')
    await eventually(() => state.orders.length === 2, '保留端口下的排序未生效')
    for (const node of state.nodes) assert.equal(node.inbound_port, ports.get(node.id))
    await clickControl(page, '[data-testid="open-settings"]')
    await toggle(page, '#preserve_inbound_ports', false)
    await clickText(page, 'Save Settings')
    await confirmDialog(page, false)
    assert.equal(state.settingsSaves.length, 0)
    await clickText(page, 'Save Settings')
    await confirmDialog(page)
    await eventually(() => state.settingsSaves.length === 1, '端口设置确认后未保存')
  },

  async 'batch-import'({ page, state }) {
    await openBatchImport(page)
    await clickControl(page, '[data-testid="batch-import-submit"]')
    assert.equal(state.imports.length, 0)
    state.importResult = { total: 2, success: 1, failed: 1, results: [{ id: 10, success: true, name: 'new node' }, { source: 'bad-source', success: false, error: `validation failed ${'long-detail '.repeat(30)}` }] }
    await setInput(page, '[data-testid="batch-import-content"]', 'socks5://127.0.0.1:1080#valid\ninvalid')
    await clickControl(page, '[data-testid="batch-import-submit"]')
    await page.waitForSelector('[data-testid="batch-import-errors"]', { visible: true })
    assert.equal(state.imports[0].source_type, 'auto')
    assert.equal(state.imports[0].enabled, true)
    assert.equal(await page.$eval('[data-testid="batch-import-submit"]', (button) => button.disabled), true)
    for (const width of [390, 1440]) {
      await page.setViewport({ width, height: 900 })
      assert.ok(await page.$('[data-testid="batch-import-errors"]'), '调整窗口丢失导入结果')
      assert.equal(await page.$eval('[data-testid="batch-import-errors"]', (element) => element.scrollWidth > element.clientWidth), false)
    }
    await clickText(page, 'Close')
    await page.waitForSelector('[data-testid="batch-import-content"]', { hidden: true })
    await waitOverlaysClosed(page)
    await openBatchImport(page)
    await clickText(page, 'HTTP proxy', { root: '[data-testid="import-source"]', exact: false })
    await setInput(page, '[data-testid="batch-import-content"]', 'http://proxy.example.com:8080')
    state.importResult = null
    state.importDelay = 300
    await clickControl(page, '[data-testid="batch-import-submit"]')
    await page.waitForFunction(() => document.querySelector('[data-testid="batch-import-content"]')?.disabled)
    assert.equal(await page.$('.sb-overlay .ant-modal-close'), null)
    await page.keyboard.press('Escape')
    assert.ok(await page.$('[data-testid="batch-import-content"]'), '导入执行期间 Esc 关闭弹窗')
    await page.waitForSelector('[data-testid="batch-import-content"]', { hidden: true })
    assert.equal(state.imports[1].source_type, 'http_proxy')
    assert.equal(Object.hasOwn(state.imports[1], 'links'), false)
  },

  async 'batch-auth'({ page, state, refresh }) {
    const shortSecret = 'E2e!short'
    const longSecret = `E2e!${'long-secret_'.repeat(18)}`
    const sharedSecret = 'shared-secret'
    Object.assign(state.nodes[0], { username: 'first-user', password: shortSecret })
    Object.assign(state.nodes[1], { username: 'second-user', password: longSecret })
    Object.assign(state.nodes[3], { username: '', password: '' })
    await refresh()
    await assertInboundUsername(page, 1, 'first-user')
    await assertInboundUsername(page, 2, 'second-user')
    await assertInboundUsername(page, 4, '')
    await assertEmptyInboundPassword(page, 4)
    const shortMask = await assertMaskedInboundPassword(page, 1, shortSecret)
    const longMask = await assertMaskedInboundPassword(page, 2, longSecret)
    assert.ok(Math.abs(shortMask - longMask) <= 1, '不同长度密码的遮蔽宽度必须一致')
    const firstMask = '[data-testid="node-password-mask-1"]'
    const firstValue = '[data-testid="node-password-value-1"]'
    const secondMask = '[data-testid="node-password-mask-2"]'
    // 先用键盘快捷键定位节点 1，再验证鼠标点击密码掩码后焦点留在密码控件，不因回落 body 导致按 Enter 误展开行详情。
    await page.keyboard.press('j')
    await clickControl(page, firstMask)
    await assertRevealedInboundPassword(page, 1, shortSecret)
    await assertMaskedInboundPassword(page, 2, longSecret)
    assert.equal(await page.$('[data-testid="node-inspector"]'), null, '点击密码掩码区域误打开了节点详情')
    assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('data-testid')), 'node-password-value-1', '鼠标展开密码后焦点不应回落到 body')
    await page.keyboard.press('Enter')
    await assertMaskedInboundPassword(page, 1, shortSecret)
    assert.equal(await page.$('[data-testid="node-inspector"]'), null, '展开密码后按 Enter 误打开了节点详情')
    await clickControl(page, firstMask)
    await assertRevealedInboundPassword(page, 1, shortSecret)
    // 点击密码单元格留白区域也不应展开节点详情。
    const cellBox = await page.$eval('[data-testid="node-password-1"]', (el) => {
      const r = el.getBoundingClientRect()
      return { left: r.left, top: r.top, right: r.right, height: r.height }
    })
    await page.mouse.click(cellBox.right - 2, cellBox.top + cellBox.height / 2)
    assert.equal(await page.$('[data-testid="node-inspector"]'), null, '点击密码单元格留白误打开了节点详情')
    // 真实跨格拖选明文密码：既不能误切回掩码，也不能因 click 派发到共同祖先而误展开节点详情。
    const valueBox = await page.$eval(firstValue, (el) => {
      const r = el.getBoundingClientRect()
      return { left: r.left, top: r.top, right: r.right, height: r.height }
    })
    await page.mouse.move(valueBox.left + 2, valueBox.top + valueBox.height / 2)
    await page.mouse.down()
    await page.mouse.move(cellBox.right + 16, valueBox.top + valueBox.height / 2, { steps: 6 })
    await page.mouse.up()
    assert.ok((await page.evaluate(() => window.getSelection()?.toString() || '')).includes(shortSecret), '拖选未选中密码明文')
    await assertRevealedInboundPassword(page, 1, shortSecret)
    assert.equal(await page.$('[data-testid="node-inspector"]'), null, '跨格拖选明文密码误打开了节点详情')
    await page.evaluate(() => window.getSelection()?.removeAllRanges())
    await page.focus(secondMask)
    await page.keyboard.press('Enter')
    await assertRevealedInboundPassword(page, 2, longSecret)
    await assertRevealedInboundPassword(page, 1, shortSecret)
    assert.equal(await page.$('[data-testid="node-inspector"]'), null, '键盘展开密码误打开了节点详情')
    await page.keyboard.press('Space')
    await assertMaskedInboundPassword(page, 2, longSecret)
    assert.equal(await page.$('[data-testid="node-inspector"]'), null, '键盘收起密码误打开了节点详情')
    await clickControl(page, firstValue)
    await assertMaskedInboundPassword(page, 1, shortSecret)
    assert.equal(await page.$('[data-testid="node-inspector"]'), null, '点击明文隐藏密码误打开了节点详情')
    await clickControl(page, firstMask)

    await selectNodes(page, [1, 2])
    await clickControl(page, '[data-testid="nodes-batch-auth"]')
    await page.locator('[data-testid="batch-auth-form"] button[type="submit"]').click()
    assert.equal(state.auth.length, 0)
    await setInput(page, '[data-testid="batch-auth-username"]', 'a+b')
    await setInput(page, '[data-testid="batch-auth-secret"]', sharedSecret)
    await page.locator('[data-testid="batch-auth-form"] button[type="submit"]').click()
    assert.equal(state.auth.length, 0)
    await setInput(page, '[data-testid="batch-auth-username"]', 'team-user')
    await page.locator('[data-testid="batch-auth-form"] button[type="submit"]').click()
    await eventually(() => state.auth.length === 1, '批量认证未保存')
    assert.deepEqual(state.auth[0], { node_ids: [1, 2], auth_enabled: true, username: 'team-user', password: sharedSecret })
    await waitOverlaysClosed(page)
    for (const id of [1, 2]) {
      await assertInboundUsername(page, id, 'team-user')
      await assertMaskedInboundPassword(page, id, sharedSecret)
    }
    await clickControl(page, firstMask)
    await assertRevealedInboundPassword(page, 1, sharedSecret)
    await clickControl(page, '[data-testid="nodes-batch-auth"]')
    await toggle(page, '[data-testid="batch-auth-form"] [role="switch"]', false)
    await page.locator('[data-testid="batch-auth-form"] button[type="submit"]').click()
    await confirmDialog(page, false)
    assert.equal(state.auth.length, 1)
    await page.locator('[data-testid="batch-auth-form"] button[type="submit"]').click()
    await confirmDialog(page)
    await eventually(() => state.auth.length === 2, '关闭批量认证未提交')
    assert.deepEqual(state.auth[1], { node_ids: [1, 2], auth_enabled: false, username: '', password: '' })
    await waitOverlaysClosed(page)
    for (const id of [1, 2]) {
      await assertInboundUsername(page, id, '')
      await assertEmptyInboundPassword(page, id)
    }
    assert.equal(state.nodes[2].username, 'alice')
    await clickControl(page, '[data-testid="nodes-batch-auth"]')
    await toggle(page, '[data-testid="batch-auth-form"] [role="switch"]', true)
    await clickControl(page, '[data-testid="auth-generate"]')
    assert.equal((await page.$eval('[data-testid="batch-auth-secret"]', (input) => input.value)).length, 16)
    await setInput(page, '[data-testid="batch-auth-username"]', 'restored-user')
    await setInput(page, '[data-testid="batch-auth-secret"]', sharedSecret)
    await clickControl(page, '[data-testid="batch-auth-form"] button[type="submit"]')
    await eventually(() => state.auth.length === 3, '恢复批量认证未提交')
    await waitOverlaysClosed(page)
    await assertMaskedInboundPassword(page, 1, sharedSecret)
    await assertMaskedInboundPassword(page, 2, sharedSecret)
  },

  async 'batch-check-ip-live'({ page, state, refresh }) {
    state.nodes = Array.from({ length: 12 }, (_, index) => makeNode(index + 1, { enabled: index !== 11 }))
    state.checkDelay = 250
    state.checkStatuses = { 2: 500, 3: 429, 4: 400 }
    await refresh()
    await selectNodes(page, [1, 2, 3, 4, 5, 12])
    await clickControl(page, '[data-testid="nodes-batch-check"]')
    await page.waitForSelector('[data-testid="check-progress"]', { visible: true })
    await openNodeMenu(page, 8, 'Check IP')
    await page.waitForSelector('[data-testid="check-progress"]', { hidden: true })
    await eventually(() => state.activeChecks === 0, '检测请求未全部释放')
    assert.ok(state.peakChecks <= 3, `检测并发超过上限：${state.peakChecks}`)
    assert.equal(state.checks.includes(12), false)
    assert.equal(state.checks.filter((id) => id === 8).length, 1)
    const text = await page.$eval('[data-row-id="2"]', (row) => row.textContent)
    assert.equal(text.includes('203.0.113.10'), false)
    assert.ok((await page.$eval('[data-row-id="4"]', (row) => row.textContent)).includes('203.0.113.10'))
    await clickControl(page, '[data-testid="nodes-clear-selection"]')
    state.checkStatuses = {}
    state.checkDelay = 1000
    const previous = state.checks.length
    await clickControl(page, '[data-testid="check-visible"]')
    await page.waitForSelector('[data-testid="check-stop"]', { visible: true })
    await clickControl(page, '[data-testid="check-stop"]')
    await page.waitForSelector('[data-testid="check-progress"]', { hidden: true })
    await eventually(() => state.activeChecks === 0, '停止检测未中止请求')
    assert.ok(state.checks.length - previous < 11, '停止后仍启动全部排队检测')
  },

  async 'outbound-dedup'({ page, state, refresh }) {
    state.nodes = [
      makeNode(1, { config: '{"server":"same.example.com","server_port":443}', username: 'first' }),
      makeNode(2, { config: '{"server_port":443,"server":"same.example.com"}', username: 'second', enabled: false }),
      makeNode(3, { config: '{"server":"same.example.com","server_port":443}', upstream_mode: 'custom', upstream_type: 'socks5', upstream_config: '{"server":"upstream.example.com","server_port":1080}' }),
      makeNode(4, { type: 'vless', config: '{"server":"same.example.com","server_port":443}' }),
    ]
    await refresh()
    await selectNodes(page, [1, 2, 3, 4])
    await clickControl(page, '[data-testid="nodes-batch-dedupe"]')
    await confirmDialog(page)
    await eventually(() => state.deletes.length === 1, '去重未提交')
    assert.deepEqual(state.deletes[0], [2])
    assert.deepEqual(state.nodes.map((node) => node.id), [1, 3, 4])
    await clickControl(page, '[data-testid="nodes-batch-export"]')
    await page.waitForSelector('.sb-overlay textarea', { visible: true })
    assert.equal((await page.$eval('.sb-overlay textarea', (element) => element.value)).split('\n').length, 3)
    await closeOverlay(page)
  },

  async remark({ page, state, refresh }) {
    const name = `Original-${'LongNodeName'.repeat(16)}`
    state.nodes[0].name = name
    await refresh()
    await clickControl(page, '[data-testid="node-open-1"]')
    await assertFullNodeName(page, 1, name)
    await page.browserContext().overridePermissions(new URL(page.url()).origin, ['clipboard-read', 'clipboard-write'])
    await assertCopiedText(page, '.sb-full-name button', name)
    const text = 'first line\nsecond line with  spaces\n'+ '长备注'.repeat(200)
    await setInput(page, '.sb-remark textarea', text)
    assert.equal(state.nodes[0].remark, '')
    await page.focus('.sb-remark textarea')
    await page.keyboard.down('Control')
    await page.keyboard.press('Enter')
    await page.keyboard.up('Control')
    await eventually(() => state.nodes[0].remark === text, '备注未完整保存')
    await editNode(page, 1)
    await setInput(page, '#name', 'Renamed node')
    await saveNodeForm(page)
    await page.waitForSelector('[data-testid="node-form"]', { hidden: true })
    assert.equal(state.nodes[0].remark, text)
    await assertFullNodeName(page, 1, 'Renamed node')
    await setInput(page, '[data-testid="nodes-search"]', 'second line')
    await expectRows(page, [1])
  },

  async filter({ page, state, refresh }) {
    await selectNodes(page, [1, 2])
    await setInput(page, '[data-testid="nodes-search"]', 'Node 001')
    await expectRows(page, [1])
    assert.ok((await page.$eval('[data-testid="nodes-selected-count"]', (element) => element.textContent)).includes('1'))
    await clickControl(page, '[data-testid="nodes-filter-button"]')
    await page.locator('[data-testid="filter-types"] [data-value="ss"]').click()
    await expectRows(page, [1])
    await page.locator('[data-testid="filter-types"] [data-value="ss"]').click()
    await setInput(page, '[data-testid="nodes-filter-ports"]', '30001-30002')
    await clickControl(page, '[data-testid="nodes-filter-button"]')
    await setInput(page, '[data-testid="nodes-search"]', '')
    await expectRows(page, [1, 2])
    assert.equal(await page.$eval('[data-row-id="1"] .sb-drag', (button) => button.disabled), true)
    state.settings.preserve_inbound_ports = true
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('sbpm-dusk:external-change')))
    await page.waitForFunction(() => !document.querySelector('[data-row-id="1"] .sb-drag')?.disabled)
    await openNodeMenu(page, 2, 'Move to top')
    await eventually(() => state.orders.length === 1, '筛选中的子集排序未保存')
    assert.equal(state.orders[0].nodes.length, 4)
    assert.deepEqual(state.nodes.map((node) => node.id), [2, 1, 3, 4])
    await clickControl(page, '[data-testid="nodes-clear-selection"]')
    await selectNodes(page, [1, 2])
    await clickControl(page, '[data-testid="nodes-batch-delete"]')
    await confirmDialog(page)
    await eventually(() => state.deletes.length === 1, '筛选中的删除未提交')
    assert.deepEqual([...state.deletes[0]].sort(), [1, 2])
    assert.deepEqual(state.nodes.map((node) => node.id), [3, 4])
    await refresh()
  },

  async 'tcp-reuse-ui'({ page, state }) {
    await clickControl(page, '[data-testid="node-open-1"]')
    await assertRouteId(page, 1, 'alice+30001')
    await toggle(page, '[data-testid="node-tcp-reuse-1"]', false)
    await eventually(() => state.flags.length === 1, 'TCP 复用未提交')
    assert.deepEqual(state.flags[0], { tcp_reuse_enabled: false })
    await assertRouteId(page, 1, '')
    const config = state.nodes[0].config
    state.flagFailure = true
    await clickControl(page, '[data-testid="node-tcp-reuse-1"]')
    await eventually(() => state.flags.length === 2, 'TCP 复用失败回滚未覆盖')
    await page.waitForFunction(() => document.querySelector('[data-testid="node-tcp-reuse-1"]')?.getAttribute('aria-checked') === 'false')
    await assertRouteId(page, 1, '')
    assert.equal(state.nodes[0].config, config)
    assert.equal(state.nodes[0].enabled, true)
    state.flagFailure = false
    await toggle(page, '[data-testid="node-tcp-reuse-1"]', true)
    await assertRouteId(page, 1, 'alice+30001')
    await editNode(page, 1)
    assert.equal(await page.$('[data-testid="node-form"] [data-testid^="node-tcp-reuse"]'), null)
    await closeOverlay(page)
  },

  async 'runtime-restart'({ page, state }) {
    const before = structuredClone(state.nodes)
    state.runtime = { running: true, degraded: true, message: 'runtime recovery required' }
    await clickControl(page, '[data-testid="nodes-refresh"]')
    await page.waitForSelector('[data-testid="runtime-restart"]', { visible: true })
    const [response] = await Promise.all([
      page.waitForResponse((item) => new URL(item.url()).pathname === '/api/runtime/restart'),
      clickControl(page, '[data-testid="runtime-restart"]'),
    ])
    assert.equal(response.request().method(), 'POST')
    assert.equal(response.status(), 200)
    await page.waitForSelector('[data-testid="runtime-status-degraded"]', { hidden: true })
    assert.deepEqual(state.nodes, before)
    assert.equal(state.requests.filter((item) => item.route === '/api/runtime/restart').length, 1)
    assert.equal(state.requests.filter((item) => item.route.startsWith('/api/nodes') && item.method !== 'GET').length, 0)
  },

  async 'nodes-viewport'({ page, state, refresh }) {
    state.nodes[0].name = '长名称'.repeat(80)
    state.nodes[0].type = 'wireguard'
    const longSecret = `  Viewport  ${'abc123'.repeat(27)}  `
    state.nodes[0].password = longSecret
    state.nodes = [state.nodes[0]]
    await refresh()
    const dragLabel = await page.$eval('[data-testid="node-drag-handle-1"]', (button) => button.getAttribute('aria-label'))
    assert.equal(dragLabel.toLowerCase().includes('filter'), false, '单节点排序提示不应误报为筛选限制')
    const nameSelector = '[data-testid="node-open-1"]'
    const secretMask = '[data-testid="node-password-mask-1"]'
    const secretValue = '[data-testid="node-password-value-1"]'
    for (const width of [320, 390, 768, 900, 1024, 1280, 1440]) {
      await page.setViewport({ width, height: 900 })
      const layout = await page.$eval(nameSelector, (button) => {
        const name = button.querySelector('.truncate')
        const row = button.closest('[data-row-id]')
        const meta = row.querySelector('.dusk-row__meta')
        return {
          nameWidth: button.getBoundingClientRect().width,
          truncated: name.scrollWidth > name.clientWidth && getComputedStyle(name).overflowX === 'hidden',
          overflow: document.documentElement.scrollWidth > innerWidth || row.scrollWidth > row.clientWidth || meta.scrollWidth > meta.clientWidth,
        }
      })
      assert.equal(layout.overflow, false, `${width}px 列表横向溢出`)
      if (width >= 900) {
        assert.ok(layout.nameWidth > 0 && layout.nameWidth <= 156, `${width}px 桌面节点名列宽为 ${layout.nameWidth}px`)
        assert.equal(layout.truncated, true, `${width}px 桌面长节点名未截断`)
        await assertUnclippedTableHeaders(page)
        await assertColumnOrder(page)
      }
      await clickControl(page, secretMask)
      await assertRevealedInboundPassword(page, 1, longSecret)
      assert.equal(await page.$('[data-testid="node-inspector"]'), null, `${width}px 点击密码掩码误展开详情`)
      await clickControl(page, nameSelector)
      await assertFullNodeName(page, 1, state.nodes[0].name)
      await assertDetailLayoutOrderAndBalance(page, 1, width)
      assert.equal(await page.$eval('[data-row-id="1"]', (row) => row.scrollWidth > row.clientWidth || document.documentElement.scrollWidth > innerWidth), false, `${width}px 展开详情后横向溢出`)
      await clickControl(page, nameSelector)
      await clickControl(page, secretValue)
      await clickControl(page, '[data-testid="nodes-add-node"]')
      await page.waitForSelector('[data-testid="node-form"]', { visible: true })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth), false)
      await setInput(page, '#name', 'unsaved draft')
      await page.setViewport({ width: width === 1440 ? 390 : 1440, height: 900 })
      assert.equal(await page.$eval('#name', (input) => input.value), 'unsaved draft')
      await closeOverlay(page)
    }
    await page.setViewport({ width: 1440, height: 900 })
    await page.keyboard.down('Control')
    await page.keyboard.press('k')
    await page.keyboard.up('Control')
    await page.waitForSelector('[data-testid="palette-input"]', { visible: true })
    await page.keyboard.press('Escape')
    await page.waitForSelector('[data-testid="palette-input"]', { hidden: true })
    assert.equal(await page.$('[data-testid="login-fill-demo"], .demo-bar'), null)
  },

  async 'version-refresh'({ page, state }) {
    state.version = '9.0.1'
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForFunction(() => new URL(location.href).searchParams.get('v') === '9.0.1')
    await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
    const loads = state.loads.length
    await sleep(500)
    assert.equal(state.loads.length, loads, '版本不匹配造成反复刷新')
    assert.equal(await page.evaluate(() => localStorage.getItem('token')), TOKEN)
    const context = await page.browser().createBrowserContext()
    try {
      const denied = await context.newPage()
      await denied.evaluateOnNewDocument((token) => {
        localStorage.setItem('token', token)
        localStorage.setItem('language', 'en')
        Object.defineProperty(window, 'sessionStorage', { get() { throw new Error('blocked') } })
      }, TOKEN)
      await denied.goto(page.url().split('?')[0], { waitUntil: 'networkidle2' })
      await denied.waitForSelector('[data-testid="nodes-list"]')
      assert.equal(new URL(denied.url()).searchParams.get('v'), '9.0.1')
      await sleep(200)
    } finally { await context.close() }
  },

  async 'version-update'({ page, state }) {
    state.update = {
      available: true,
      latest_version: '9.0.2',
      release_url: 'https://github.com/cheluen/singbox-proxy-manager/releases/tag/v1.7.29',
      latest_release_url: 'https://github.com/cheluen/singbox-proxy-manager/releases/latest',
    }
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('a[data-testid="version-badge"].is-update', { visible: true })
    assert.equal(await page.$eval('[data-testid="version-badge"]', (element) => element.href), 'https://github.com/cheluen/singbox-proxy-manager/releases/latest')
    assert.ok((await page.$eval('[data-testid="version-badge"]', (element) => element.textContent)).includes('9.0.2'))
    state.update = { available: true, latest_version: '9.0.3', release_url: 'https://github.com/cheluen/singbox-proxy-manager/releases/tag/v1.7.29' }
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('a[data-testid="version-badge"].is-update', { visible: true })
    assert.equal(await page.$eval('[data-testid="version-badge"]', (element) => element.href), 'https://github.com/cheluen/singbox-proxy-manager/releases/latest')
    state.update = { available: false }
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('span[data-testid="version-badge"]', { visible: true })
  },

  async 'wireguard-ui'({ page, state }) {
    await clickControl(page, '[data-testid="nodes-add-node"]')
    await selectOption(page, 'proxy_type', 'Cloudflare WireGuard')
    assert.equal(await page.$eval('#server_port', (input) => input.value), '2408')
    await setInput(page, '#name', 'wireguard node')
    await setInput(page, '#server', 'engage.cloudflareclient.com')
    await setInput(page, '#wireguard_local_address', '172.16.0.2/32\n2606:4700:110:8765::2/128')
    await setInput(page, '#wireguard_private_key', 'test-private-key')
    await setInput(page, '#wireguard_peer_public_key', 'test-public-key')
    await setInput(page, '#wireguard_reserved', 'omje')
    await setInput(page, '#username', 'wg-user')
    await setInput(page, '#password', 'wg-secret')
    await page.focus('#name')
    await page.focus('#password')
    const unobscured = await page.$eval('#password', (input) => {
      const rect = input.getBoundingClientRect()
      return document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2) === input
    })
    assert.equal(unobscured, true, '固定操作栏遮挡键盘聚焦的认证密码输入框')
    await saveNodeForm(page)
    await eventually(() => state.saves.length === 1, 'WireGuard 表单未提交')
    const config = JSON.parse(state.saves[0].config)
    assert.deepEqual(config.reserved, [162, 104, 222])
    assert.deepEqual(config.local_address, ['172.16.0.2/32', '2606:4700:110:8765::2/128'])
    assert.equal(config.server_port, 2408)
    assert.equal(Object.hasOwn(config, 'network'), false)
  },

  async 'dashboard-concurrency-replace'({ page, state }) {
    const stale = structuredClone(state.nodes)
    stale[0].name = 'stale result'
    const current = structuredClone(state.nodes)
    current[0].name = 'latest result'
    state.nodes = current
    state.nextLoads = [{ nodes: stale, delay: 500 }, { nodes: current, delay: 10 }]
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('sbpm-dusk:external-change')))
    await sleep(50)
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('sbpm-dusk:external-change')))
    await page.waitForFunction(() => document.querySelector('[data-row-id="1"]')?.textContent.includes('latest result'))
    await sleep(600)
    assert.equal((await page.$eval('[data-row-id="1"]', (element) => element.textContent)).includes('stale result'), false)
    state.nextLoads = [{ nodes: structuredClone(state.nodes), delay: 500 }]
    const previousLoads = state.loads.length
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('sbpm-dusk:external-change')))
    await eventually(() => state.loads.length > previousLoads, '未发起并发刷新')
    await clickControl(page, '[data-testid="node-enabled-1"]')
    await eventually(() => state.nodes[0].enabled === false, '节点开关未保存')
    await sleep(650)
    assert.equal(await page.$eval('[data-testid="node-enabled-1"]', (element) => element.getAttribute('aria-checked')), 'false', '旧列表响应覆盖已保存的节点开关')
    await openNodeMenu(page, 1, 'Replace with a share link')
    await setInput(page, '[data-testid="replace-node-link-input"]', 'socks5://127.0.0.1:1080#replacement')
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('sbpm-dusk:external-change')))
    assert.equal(await page.$eval('[data-testid="replace-node-link-input"]', (input) => input.value), 'socks5://127.0.0.1:1080#replacement')
    await clickText(page, 'Replace')
    await eventually(() => state.saves.length === 1, '替换节点未提交')
    assert.equal(JSON.parse(state.nodes[0].config).server, 'replacement.example.com')
  },

  async 'upstream-proxy'({ page, state }) {
    await clickControl(page, '[data-testid="node-open-1"]')
    await clickControl(page, '[data-testid="node-upstream-action-1"]')
    await switchMode(page, 'custom')
    await clickControl(page, '[data-testid="node-upstream-save"]')
    assert.equal(state.upstreams.length, 0)
    await clickControl(page, '[data-testid="node-upstream-configure"]')
    await selectOption(page, 'node_upstream_proxy_type', 'SOCKS5')
    await setInput(page, '#node_upstream_server', 'upstream.example.com')
    await setInput(page, '#node_upstream_server_port', '1080')
    await clickControl(page, '[data-testid="upstream-editor"] button[type="submit"]')
    await page.waitForSelector('[data-testid="upstream-editor"]', { hidden: true })
    await clickControl(page, '[data-testid="node-upstream-save"]')
    await eventually(() => state.upstreams.length === 1, '节点前置代理未保存')
    assert.equal(state.upstreams[0].upstream_mode, 'custom')
    assert.equal(JSON.parse(state.upstreams[0].upstream_config).server, 'upstream.example.com')
    await waitOverlaysClosed(page)
    await clickControl(page, '[data-testid="node-upstream-action-1"]')
    await switchMode(page, 'none')
    await clickControl(page, '[data-testid="node-upstream-save"]')
    await eventually(() => state.upstreams.length === 2, '直连模式未保存')
    assert.equal(state.upstreams[1].upstream_type, 'socks5')
    await waitOverlaysClosed(page)
    await clickControl(page, '[data-testid="open-settings"]')
    await clickControl(page, '[data-testid="global-upstream-configure"]')
    await selectOption(page, 'global_upstream_proxy_type', 'HTTP Proxy')
    await setInput(page, '#global_upstream_server', 'global.example.com')
    await setInput(page, '#global_upstream_server_port', '8080')
    await clickControl(page, '[data-testid="upstream-editor"] button[type="submit"]')
    await page.waitForSelector('[data-testid="upstream-editor"]', { hidden: true })
    await toggle(page, '[data-testid="global-upstream-enabled"]', true)
    await clickText(page, 'Save Settings')
    await eventually(() => state.settingsSaves.length === 1, '全局前置代理未保存')
    await waitOverlaysClosed(page)
    await clickControl(page, '[data-testid="open-settings"]')
    await clickControl(page, '[data-testid="global-upstream-check-ip"]')
    await page.waitForSelector('[data-testid="global-upstream-check-result"]', { visible: true })
    assert.equal(state.settings.global_upstream_ip, '198.51.100.30')
    await closeOverlay(page)
    await clickControl(page, '[data-testid="node-upstream-action-1"]')
    await switchMode(page, 'global')
    await clickControl(page, '[data-testid="node-upstream-save"]')
    await eventually(() => state.upstreams.length === 3, '全局模式未保存')
    assert.equal(state.upstreams[2].upstream_mode, 'global')
    await waitOverlaysClosed(page)

    // 分组级前置代理配置、启用、测 IP 与三层优先级提示
    await clickControl(page, '[data-testid="group-settings-button"]')
    await clickText(page, 'Group Settings', { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
    await page.waitForSelector('[data-testid="group-form"]', { visible: true })
    await clickControl(page, '[data-testid="group-upstream-configure"]')
    await selectOption(page, 'group_upstream_proxy_type', 'SOCKS5')
    await setInput(page, '#group_upstream_server', 'group.example.com')
    await setInput(page, '#group_upstream_server_port', '1080')
    await clickControl(page, '[data-testid="upstream-editor"] button[type="submit"]')
    await page.waitForSelector('[data-testid="upstream-editor"]', { hidden: true })
    await toggle(page, '[data-testid="group-upstream-enabled"]', true)
    await clickControl(page, '[data-testid="group-form-submit"]')
    await waitOverlaysClosed(page)
    await page.waitForSelector('[data-testid="active-group-upstream-pill"]', { visible: true })
    assert.equal(state.groups[0].upstream_enabled, true)

    await clickControl(page, '[data-testid="group-settings-button"]')
    await clickText(page, 'Group Settings', { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
    await page.waitForSelector('[data-testid="group-form"]', { visible: true })
    await clickControl(page, '[data-testid="group-upstream-check-ip"]')
    await page.waitForSelector('[data-testid="group-upstream-check-result"]', { visible: true })
    assert.equal(state.groups[0].upstream_ip, '198.51.100.31')
    await closeOverlay(page)

    // 验证只改分组名不会覆盖其他管理员并发更新过的分组前置代理配置
    await clickControl(page, '[data-testid="group-settings-button"]')
    await clickText(page, 'Group Settings', { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
    await page.waitForSelector('[data-testid="group-form"]', { visible: true })
    state.groups[0].upstream_type = 'http'
    state.groups[0].upstream_config = JSON.stringify({ server: 'concurrent-other-admin.example.com', server_port: 8080 })
    state.groups[0].upstream_revision += 1
    await clickControl(page, '[data-testid="group-upstream-check-ip"]')
    await page.waitForFunction(() => document.querySelector('[data-testid="group-form"]')?.textContent?.includes('HTTP'))
    await setInput(page, '[data-testid="group-name-input"]', 'Default Renamed')
    await clickControl(page, '[data-testid="group-form-submit"]')
    await waitOverlaysClosed(page)
    assert.deepEqual(state.groupMutations.at(-1)?.body, { name: 'Default Renamed' }, '只改分组名时 PATCH 不应携带前置代理字段')
    assert.equal(state.groups[0].name, 'Default Renamed')
    assert.equal(state.groups[0].upstream_type, 'http', '只改分组名时覆盖了其他管理员保存的前置代理类型')
    assert.equal(JSON.parse(state.groups[0].upstream_config).server, 'concurrent-other-admin.example.com', '只改分组名时覆盖了其他管理员保存的前置代理配置')

    // 验证未刷新页面时并发修改同一前置配置触发服务端 409 upstream_conflict，经弹窗确认后才覆盖保存
    await clickControl(page, '[data-testid="group-settings-button"]')
    await clickText(page, 'Group Settings', { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
    await page.waitForSelector('[data-testid="group-form"]', { visible: true })
    state.groups[0].upstream_config = JSON.stringify({ server: 'other-session-newer.example.com', server_port: 8080 })
    state.groups[0].upstream_revision += 1
    await clickControl(page, '[data-testid="group-upstream-configure"]')
    await setInput(page, '#group_upstream_server', 'confirmed-override.example.com')
    await clickControl(page, '[data-testid="upstream-editor"] button[type="submit"]')
    await page.waitForSelector('[data-testid="upstream-editor"]', { hidden: true })
    await clickControl(page, '[data-testid="group-form-submit"]')
    await confirmDialog(page)
    await waitOverlaysClosed(page)
    assert.equal(JSON.parse(state.groups[0].upstream_config).server, 'confirmed-override.example.com')

    // 节点已处于 Follow Global 时，应展示「分组前置覆盖全局」提示与生效来源
    const effectiveText = await page.$eval('[data-testid="node-upstream-effective-1"]', (el) => el.textContent)
    assert.ok(effectiveText.includes('Group upstream'), `未显示分组前置生效来源：${effectiveText}`)
    await clickControl(page, '[data-testid="node-upstream-action-1"]')
    await page.waitForSelector('[data-testid="node-upstream-group-override-hint"]', { visible: true })
    await switchMode(page, 'group')
    await clickControl(page, '[data-testid="node-upstream-save"]')
    await eventually(() => state.upstreams.length === 4, '分组模式未保存')
    assert.equal(state.upstreams[3].upstream_mode, 'group')
  },

  async session({ page, state }) {
    await clickControl(page, '[data-testid="logout"]')
    await page.waitForSelector('[data-testid="login-form"]', { visible: true })
    assert.equal(await page.evaluate(() => localStorage.getItem('token')), null)
    await setInput(page, '[data-testid="login-secret"]', 'wrong')
    await clickControl(page, '[data-testid="login-submit"]')
    await page.waitForFunction(() => document.querySelector('#sb-login-error')?.textContent.length > 0)
    await setInput(page, '[data-testid="login-secret"]', state.loginSecret)
    await clickControl(page, '[data-testid="login-submit"]')
    await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
    assert.equal(await page.evaluate(() => localStorage.getItem('token')), TOKEN)
    state.revoked = true
    await clickControl(page, '[data-testid="nodes-refresh"]')
    await page.waitForSelector('[data-testid="login-form"]', { visible: true })
    assert.equal(await page.evaluate(() => localStorage.getItem('token')), null)
  },

  async 'language-copy'({ page, state, refresh }) {
    state.nodes[0].username = 'a@b'
    state.nodes[0].password = 'p:# + value'
    await refresh()
    assert.equal(await page.$eval('.dusk-head .dusk-col--user', (header) => header.textContent), 'Inbound User')
    assert.equal(await page.$eval('.dusk-head .dusk-col--pass', (header) => header.textContent), 'Inbound Pass')
    await assertUnclippedTableHeaders(page)
    await page.browserContext().overridePermissions(new URL(page.url()).origin, ['clipboard-read', 'clipboard-write'])
    await openNodeMenu(page, 1, 'Copy HTTP proxy URL')
    const copied = await page.evaluate(() => navigator.clipboard.readText())
    assert.equal(decodeURIComponent(new URL(copied).username), 'a@b')
    assert.equal(decodeURIComponent(new URL(copied).password), 'p:# + value')
    assert.equal(new URL(copied).port, '30001')
    await selectNodes(page, [1])
    await clickControl(page, '[data-testid="node-open-1"]')
    await assertRouteId(page, 1, 'a@b+30001')
    await assertCopiedText(page, '[data-testid="node-route-id-section-1"] button', 'a@b+30001')
    await clickControl(page, 'button[title="中文"]')
    await page.waitForFunction(() => document.documentElement.lang === 'zh-CN')
    assert.equal(await page.evaluate(() => localStorage.getItem('language')), 'zh')
    assert.equal(await page.$eval('.dusk-head .dusk-col--user', (header) => header.textContent), '入站用户名')
    assert.equal(await page.$eval('.dusk-head .dusk-col--pass', (header) => header.textContent), '入站密码')
    await assertUnclippedTableHeaders(page)
    assert.equal(await page.$eval('[data-testid="node-select-1"]', (input) => input.checked), true)
    await assertRouteId(page, 1, 'a@b+30001', '路由号')
    await assertFullNodeName(page, 1, state.nodes[0].name, '完整节点名称')
    await assertMaskedInboundPassword(page, 1, state.nodes[0].password, '点击显示入站密码')
    await clickControl(page, '[data-testid="node-password-mask-1"]')
    await assertRevealedInboundPassword(page, 1, state.nodes[0].password, '点击隐藏入站密码')
    await clickControl(page, '[data-testid="node-password-value-1"]')
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
    assert.equal(await page.evaluate(() => document.documentElement.lang), 'zh-CN')
    await clickControl(page, 'button[title="English"]')
    await page.waitForFunction(() => document.documentElement.lang === 'en')
    assert.equal(await page.evaluate(() => localStorage.getItem('language')), 'en')
  },

  async 'column-resize'({ page, state, refresh }) {
    state.nodes[1].upstream_mode = 'custom'
    state.nodes[1].upstream_type = 'socks5'
    state.nodes[1].upstream_config = JSON.stringify({ server: 'relay.example.com', server_port: 1080 })
    state.nodes[1].upstream_ip = '198.51.100.20'
    state.nodes[1].upstream_location = 'Singapore'
    state.nodes[1].upstream_country_code = 'SG'
    state.nodes[1].upstream_latency = 28
    await refresh()

    await page.setViewport({ width: 1440, height: 1000 })
    assert.equal(await page.$('[data-testid="reset-column-widths"]'), null, '默认列宽时不应显示重置按钮')

    // 验证所有可见列的静息态列宽分割线均为完整竖条且清晰可辨（不塌陷成点、与表头底色有明显区分）
    const idleDividers = await page.$$eval('.dusk-head .dusk-col-resizer', (els) => {
      const headBg = getComputedStyle(document.querySelector('.dusk-head')).backgroundColor
      return els
        .filter((el) => el.offsetParent !== null)
        .map((el) => {
          const rect = el.getBoundingClientRect()
          const style = getComputedStyle(el, '::after')
          return {
            testId: el.dataset.testid,
            handleHeight: Math.round(rect.height),
            barHeight: parseFloat(style.height),
            width: parseFloat(style.width),
            opacity: parseFloat(style.opacity),
            bg: style.backgroundColor,
            headBg,
          }
        })
    })
    assert.equal(idleDividers.length, 9, `1440px 下可见列宽手柄数量异常：${idleDividers.length}`)
    for (const item of idleDividers) {
      assert.ok(item.handleHeight >= 26, `${item.testId} 高度未撑满表头：${item.handleHeight}px`)
      assert.ok(item.barHeight >= 12, `${item.testId} 分割线高度塌陷：${item.barHeight}px`)
      assert.ok(item.width >= 2, `${item.testId} 分割线过细：${item.width}px`)
      assert.ok(item.opacity >= 0.58, `${item.testId} 静息透明度过低：${item.opacity}`)
      assert.notEqual(item.bg, item.headBg, `${item.testId} 分割线与表头背景色不可相同`)
    }

    const measureCol = (key) => page.$eval(`.dusk-head [data-col-key="${key}"]`, (el) => Math.round(el.getBoundingClientRect().width))
    const dragCol = async (key, deltaX) => {
      const handle = await page.$(`[data-testid="col-resizer-${key}"]`)
      assert.ok(handle, `缺少列宽手柄 ${key}`)
      const box = await handle.boundingBox()
      const startX = box.x + box.width / 2
      const y = box.y + box.height / 2
      await page.mouse.move(startX, y)
      await page.mouse.down()
      await page.mouse.move(startX + deltaX, y, { steps: 10 })
      await page.mouse.up()
    }

    const initialNameWidth = await measureCol('name')
    await dragCol('name', 90)
    await eventually(() => state.settingsSaves.length === 1, '拖动名称列宽未持久化')
    const savedNameWidth = state.settings.node_list_column_widths.name
    assert.ok(Math.abs(savedNameWidth - (initialNameWidth + 90)) <= 4, `名称列宽保存值异常：${savedNameWidth}`)
    assert.ok(Math.abs((await measureCol('name')) - savedNameWidth) <= 2, '名称列宽渲染与保存值不一致')
    await page.waitForSelector('[data-testid="reset-column-widths"]', { visible: true })

    const beforeUserWidth = await measureCol('user')
    await dragCol('user', 60)
    await eventually(() => state.settingsSaves.length === 2, '拖动入站用户名列宽未持久化')
    const savedUserWidth = state.settings.node_list_column_widths.user
    assert.ok(Math.abs(savedUserWidth - (beforeUserWidth + 60)) <= 4, `入站用户名列宽保存值异常：${savedUserWidth}（拖动前 ${beforeUserWidth}）`)

    // 刷新页面验证从后端持久化的列宽完整恢复
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
    assert.ok(Math.abs((await measureCol('name')) - savedNameWidth) <= 2, '刷新后名称列宽未恢复')
    assert.ok(Math.abs((await measureCol('user')) - savedUserWidth) <= 2, '刷新后入站用户名列宽未恢复')
    await assertColumnOrder(page)

    // 双击单列手柄恢复该列默认值
    const userHandle = await page.$('[data-testid="col-resizer-user"]')
    const userBox = await userHandle.boundingBox()
    await page.mouse.click(userBox.x + userBox.width / 2, userBox.y + userBox.height / 2, { clickCount: 2 })
    await eventually(() => state.settingsSaves.length === 3, '双击重置单列宽未持久化')
    assert.deepEqual(state.settings.node_list_column_widths, { name: savedNameWidth })

    // 一键恢复全部默认列宽
    await clickControl(page, '[data-testid="reset-column-widths"]')
    await eventually(() => state.settingsSaves.length === 4, '一键恢复默认列宽未持久化')
    assert.deepEqual(state.settings.node_list_column_widths, {})
    await page.waitForSelector('[data-testid="reset-column-widths"]', { hidden: true })

    // 验证缩窄后的端口与延迟列在三个断点、中英文双语下容纳 5 位固定端口与 4 位延迟不换行且不裁切表头
    state.nodes[0].inbound_port = 65535
    state.nodes[0].inbound_port_pinned = true
    state.nodes[0].latency = 9999
    await refresh()
    for (const lang of ['English', '中文']) {
      await clickControl(page, `button[title="${lang}"]`)
      for (const width of [1440, 1200, 1000]) {
        await page.setViewport({ width, height: 1000 })
        await assertUnclippedTableHeaders(page)
        await assertColumnOrder(page)
        const fit = await page.evaluate(() => {
          const portHead = document.querySelector('.dusk-head [data-col-key="port"]')?.getBoundingClientRect()
          const latHead = document.querySelector('.dusk-head [data-col-key="latency"]')?.getBoundingClientRect()
          const portChip = document.querySelector('[data-row-id="1"] .sb-port')
          const latEl = document.querySelector('[data-row-id="1"] .sb-latency')
          const portRect = portChip?.getBoundingClientRect()
          const latRect = latEl?.getBoundingClientRect()
          return {
            portHeadW: Math.round(portHead?.width || 0),
            latHeadW: Math.round(latHead?.width || 0),
            portOverflow: portRect ? portRect.right > (portHead?.right || 0) + 1 : true,
            portWrapped: portRect ? portRect.height > 28 : true,
            latOverflow: latRect ? latRect.right > (latHead?.right || 0) + 1 : true,
            latWrapped: latRect ? latRect.height > 24 : true,
          }
        })
        assert.ok(fit.portHeadW <= 82, `${lang}/${width} 端口列过宽：${fit.portHeadW}px`)
        assert.ok(fit.latHeadW <= 66, `${lang}/${width} 延迟列过宽：${fit.latHeadW}px`)
        assert.equal(fit.portOverflow, false, `${lang}/${width} 5位固定端口溢出`)
        assert.equal(fit.portWrapped, false, `${lang}/${width} 5位固定端口换行`)
        assert.equal(fit.latOverflow, false, `${lang}/${width} 4位延迟溢出`)
        assert.equal(fit.latWrapped, false, `${lang}/${width} 4位延迟换行`)
      }
    }
    await clickControl(page, 'button[title="English"]')

    // 验证直连与自定义前置代理节点在三种视口下的展开布局对称性与入站优先顺序
    for (const width of [1440, 1080, 390]) {
      await page.setViewport({ width, height: 1000 })
      for (const nodeId of [1, 2]) {
        await clickControl(page, `[data-testid="node-open-${nodeId}"]`)
        await page.waitForSelector(`[data-testid="node-inspector"][data-node-id="${nodeId}"]`, { visible: true })
        await assertDetailLayoutOrderAndBalance(page, nodeId, width)
        await clickControl(page, `[data-testid="node-open-${nodeId}"]`)
      }
    }
  },

  async 'check-failed-ui'({ page, state, refresh }) {
    state.nodes[1].check_status = 'untested'
    state.nodes[3].check_status = 'failed'
    state.nodes[3].check_error = 'dial tcp 198.51.100.4:443: i/o timeout'
    state.nodes[3].node_ip = ''
    state.nodes[3].latency = 0
    await refresh()
    assert.equal(await page.$eval('[data-testid="node-health-1"]', (el) => el.dataset.health), 'healthy')
    assert.equal(await page.$eval('[data-testid="node-health-2"]', (el) => el.dataset.health), 'untested')
    assert.equal(await page.$eval('[data-testid="node-health-4"]', (el) => el.dataset.health), 'failed')
    const failTip = await page.$eval('[data-row-id="4"] [data-testid="latency-failed"]', (el) => el.getAttribute('data-tip'))
    assert.ok(failTip.includes('i/o timeout'), `失败叉号缺少错误原因提示：${failTip}`)
    const failColor = await page.$eval('[data-row-id="4"] [data-testid="latency-failed"]', (el) => getComputedStyle(el).color)
    const badColor = await page.$eval('[data-testid="node-health-4"] svg', (el) => getComputedStyle(el).color)
    assert.notEqual(failColor, badColor, '延迟失败叉号应使用中性色而非红色')
    await clickControl(page, '[data-testid="status-filter"] [data-value="failed"]')
    await expectRows(page, [4])
    await clickControl(page, '[data-testid="status-filter"] [data-value="untested"]')
    await expectRows(page, [2])
  },

  async 'groups-policy-ui'({ page, state }) {
    await selectNodes(page, [1, 2])
    await page.waitForSelector('[data-testid="nodes-selected-count"]', { visible: true })
    await clickControl(page, '[data-testid="group-create-button"]')
    await setInput(page, '[data-testid="group-name-input"]', 'Pool NoPort')
    await toggle(page, '[data-testid="group-allocate-port"]', false)
    assert.equal(await page.$eval('[data-testid="group-start-port"]', (el) => el.disabled), true)
    await clickControl(page, '[data-testid="group-form-submit"]')
    await waitOverlaysClosed(page)
    await page.waitForSelector('[data-testid="active-group-noport-pill"]', { visible: true })
    assert.equal(await page.$('[data-testid="nodes-selected-count"]'), null, '切换分组未重置选中态')

    await clickControl(page, '[data-testid="group-tab-1"]')
    await expectRows(page, [1, 2, 3, 4])
    await selectNodes(page, [2])
    await clickControl(page, '[data-testid="nodes-batch-move"]')
    await clickControl(page, '[data-testid="move-group-confirm"]')
    await waitOverlaysClosed(page)
    await eventually(() => state.moves.length === 1, '跨分组移动未提交')
    assert.equal(state.nodes.find((n) => n.id === 2).inbound_port, 0)

    await clickControl(page, '[data-testid="nodes-add-node"]')
    await selectOption(page, 'proxy_type', 'Failover (Policy)')
    await setInput(page, '#name', 'JP Policy')
    assert.equal(await page.$eval('#server', (el) => el.closest('.ant-form-item')?.hidden || el.offsetParent === null), true, '策略节点不应显示服务器地址输入框')
    await closeOverlay(page)

    // 中文环境下故障转移 / 负载均衡节点类型、服务器摘要与下拉选项应完整中文化
    state.nodes.push(
      makeNode(5, { name: 'JP Policy Failover', type: 'failover', config: JSON.stringify({ member_ids: [1, 4] }) }),
      makeNode(6, { name: 'JP Policy LB', type: 'loadbalance', config: JSON.stringify({ member_ids: [1, 4], strategy: 'round-robin' }) }),
    )
    await clickControl(page, 'button[title="中文"]')
    await page.waitForFunction(() => document.documentElement.lang === 'zh-CN')
    await clickControl(page, '[data-testid="nodes-refresh"]')
    await page.waitForSelector('[data-row-id="5"]', { visible: true })
    assert.equal(await page.$eval('[data-row-id="5"] .dusk-col--type', (el) => el.textContent.trim()), '故障转移')
    assert.equal(await page.$eval('[data-row-id="5"] .dusk-col--server', (el) => el.textContent.trim()), '2 · 故障转移')
    assert.equal(await page.$eval('[data-row-id="6"] .dusk-col--type', (el) => el.textContent.trim()), '负载均衡')
    assert.equal(await page.$eval('[data-row-id="6"] .dusk-col--server', (el) => el.textContent.trim()), '2 · 轮询')
    await clickControl(page, '[data-testid="nodes-add-node"]')
    await selectOption(page, 'proxy_type', '故障转移（策略）')
    await selectOption(page, 'proxy_type', '负载均衡（策略）')
    await closeOverlay(page)
    await clickControl(page, 'button[title="English"]')
    await page.waitForFunction(() => document.documentElement.lang === 'en')
  },

  async 'connections-logs-ui'({ page, state }) {
    assert.equal(state.sseOpened, 0, '未打开当前连接面板不应开启采集')
    await clickControl(page, '[data-testid="nav-tab-connections"]')
    await page.waitForSelector('[data-testid="connections-topology"]', { visible: true })
    await eventually(() => state.sseOpened >= 1 && state.sseActive >= 1, '打开连接面板未启动按需采集')
    assert.equal(await page.$$eval('[data-testid="conn-outbound-row"]', (els) => els.length), 2)
    assert.equal(await page.$$eval('[data-testid^="conn-client-"]', (els) => els.length), 1, '同客户端 IP 应合并为一张卡片')
    await clickControl(page, '[data-testid="nav-tab-logs"]')
    await page.waitForSelector('[data-testid="logs-panel"]', { visible: true })
    await eventually(() => state.sseActive === 0 && state.sseClosed >= 1, '离开当前连接面板后未立即释放采集')

    await page.waitForSelector('[data-testid="logs-events-list"]', { visible: true })
    await clickControl(page, '[data-testid="logs-settings-open"]')
    await toggle(page, '[data-testid="log-persist-switch"]', true)
    assert.equal(state.logsSettings.enabled, false, '未点击保存前不应立即生效')
    state.logSettingsFailMessage = 'connection refused'
    await clickControl(page, '[data-testid="log-settings-save"]')
    await page.waitForSelector('[data-testid="log-settings-error"]', { visible: true })
    assert.equal(state.logsSettings.enabled, false, '校验失败不应影响当前运行状态')
    state.logSettingsFailMessage = ''
    await clickControl(page, '[data-testid="log-settings-save"]')
    await waitOverlaysClosed(page)
    assert.equal(state.logsSettings.enabled, true)
    await clickControl(page, '[data-testid="logs-subtab"] [data-value="traffic"]')
    await page.waitForSelector('[data-testid="traffic-summary"]', { visible: true })
  },
}

async function runRegression(name) {
  assert.ok(scenarios[name], `未知回归场景：${name}`)
  const fixture = createFixture()
  const port = await listen(fixture.server)
  const frontendPort = await freePort()
  const url = `http://127.0.0.1:${frontendPort}`
  const logs = []
  const vite = spawn(process.execPath, [path.join(FRONTEND, 'node_modules/vite/bin/vite.js'), '--host', '127.0.0.1', '--port', String(frontendPort), '--strictPort'], {
    cwd: FRONTEND, env: { ...process.env, E2E_API_PORT: String(port) }, stdio: ['ignore', 'pipe', 'pipe'],
  })
  vite.stdout.on('data', (chunk) => logs.push(chunk.toString()))
  vite.stderr.on('data', (chunk) => logs.push(chunk.toString()))
  let browser
  let page
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'sbpm-dusk-browser-'))
  const errors = []
  const started = Date.now()
  try {
    await eventually(async () => {
      assert.equal(vite.exitCode, null, `Vite 提前退出：${logs.join('')}`)
      try { return (await fetch(url)).status === 200 } catch { return false }
    }, 'Vite 未启动', 60000)
    browser = await launchBrowser({ userDataDir: scratch })
    page = await browser.newPage()
    await page.setViewport({ width: 1440, height: 1000 })
    page.setDefaultTimeout(15000)
    await page.evaluateOnNewDocument((token) => {
      localStorage.setItem('token', token)
      if (!localStorage.getItem('language')) localStorage.setItem('language', 'en')
    }, TOKEN)
    page.on('pageerror', (error) => errors.push(error.message))
    page.on('console', (message) => {
      if (message.type() !== 'error') return
      const source = message.location().url
      let route = ''
      try { route = new URL(source).pathname } catch { /* 非网络错误也应保留 */ }
      if (fixture.state.expectedFailures.has(route) && message.text().includes('Failed to load resource')) return
      errors.push(message.text())
    })
    await page.goto(url, { waitUntil: 'networkidle2' })
    await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
    const refresh = async () => {
      await Promise.all([
        page.waitForResponse((response) => response.request().method() === 'GET' && new URL(response.url()).pathname === '/api/nodes'),
        page.click('[data-testid="nodes-refresh"]'),
      ])
      await sleep(80)
    }
    await scenarios[name]({ page, state: fixture.state, refresh })
    assert.deepEqual(errors, [], '浏览器出现未预期错误')
    console.log(JSON.stringify({ success: true, scenario: name, requests: fixture.state.requests.length, durationMs: Date.now() - started }))
  } catch (error) {
    if (process.env.E2E_ARTIFACT_DIR && page) {
      fs.mkdirSync(process.env.E2E_ARTIFACT_DIR, { recursive: true })
      await page.screenshot({ path: path.join(process.env.E2E_ARTIFACT_DIR, `${name}-failure.png`), fullPage: true }).catch(() => {})
    }
    const diagnostic = page ? await page.evaluate(() => ({
      errors: Array.from(document.querySelectorAll('.ant-form-item-explain-error'), (element) => element.textContent),
      active: document.activeElement?.getAttribute('data-testid'),
      dialogs: Array.from(document.querySelectorAll('.ant-modal-title'), (element) => element.textContent),
      inputs: Array.from(document.querySelectorAll('input:not([type="password"])'), (element) => ({ id: element.id, value: element.value })),
      dropdowns: Array.from(document.querySelectorAll('.ant-select-dropdown'), (element) => ({ class: element.className, display: getComputedStyle(element).display })),
      dragging: Array.from(document.querySelectorAll('.is-dragging'), (element) => ({ id: element.dataset.rowId, transform: element.style.transform })),
    })).catch(() => ({})) : {}
    throw new Error(`${name}：${error.stack}\n诊断：${JSON.stringify(diagnostic)}\n请求：${JSON.stringify(fixture.state.requests.slice(-8).map((request) => ({ method: request.method, route: request.route })))}\n${logs.join('')}`)
  } finally {
    await browser?.close().catch(() => {})
    await stopProcess(vite, { graceMs: 5000 })
    fixture.server.closeAllConnections?.()
    await new Promise((resolve) => fixture.server.close(resolve))
    fs.rmSync(scratch, { recursive: true, force: true })
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const requested = process.argv[2]
  for (const scenario of requested ? [requested] : Object.keys(scenarios)) await runRegression(scenario)
}
