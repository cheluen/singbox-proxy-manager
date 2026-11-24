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
import { assertColumnOrder, assertCopiedText, assertEmptyInboundPassword, assertFullNodeName, assertInboundUsername, assertMaskedInboundPassword, assertRevealedInboundPassword, assertRouteId, assertUnclippedTableHeaders } from './node-display-assertions.mjs'

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const VERSION = JSON.parse(fs.readFileSync(path.join(FRONTEND, 'package.json'), 'utf8')).version
const TOKEN = 'dusk-regression-session'

const makeNode = (id, patch = {}) => ({
  id, name: `Node ${String(id).padStart(3, '0')}`, remark: '', type: 'ss',
  config: JSON.stringify({ server: `n${id}.example.com`, server_port: 443, method: 'aes-128-gcm', password: 'protocol-secret' }),
  inbound_port: 30000 + id, inbound_port_pinned: false, sort_order: id - 1,
  username: 'alice', password: 'inbound-secret', enabled: true, tcp_reuse_enabled: true,
  upstream_mode: 'none', upstream_type: '', upstream_config: '',
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
    nodes: [makeNode(1), makeNode(2, { type: 'vless', country_code: '', location: '', node_ip: '', latency: 0, tcp_reuse_enabled: false }), makeNode(3, { type: 'vmess', enabled: false, country_code: 'US', location: 'Boston, United States' }), makeNode(4)],
    settings: { start_port: 30001, preserve_inbound_ports: false, global_upstream_enabled: false, global_upstream_type: '', global_upstream_config: '' },
    runtime: { running: true, degraded: false, pid: 1234 }, version: VERSION, update: { available: false },
    requests: [], loads: [], flags: [], orders: [], deletes: [], auth: [], imports: [], saves: [], upstreams: [], settingsSaves: [], checks: [],
    expectedFailures: new Set(), activeChecks: 0, peakChecks: 0, checkDelay: 20, checkStatuses: {}, importDelay: 0,
    importResult: null, flagFailure: false, nextLoads: [], loginSecret: 'dusk-regression-admin', revoked: false,
  }
  const reorderPorts = () => {
    if (state.settings.preserve_inbound_ports) return
    const pinned = new Set(state.nodes.filter((node) => node.inbound_port_pinned).map((node) => node.inbound_port))
    let port = state.settings.start_port
    state.nodes.forEach((node, index) => {
      node.sort_order = index
      if (node.inbound_port_pinned) return
      while (pinned.has(port)) port += 1
      node.inbound_port = port++
    })
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
      reorderPorts()
      if (body.admin_password) {
        state.loginSecret = body.admin_password
        state.revoked = true
        return send(200, { password_changed: true })
      }
      return send(200, {})
    }
    if (route === '/api/nodes/reorder') {
      state.orders.push(body)
      const byID = new Map(state.nodes.map((node) => [node.id, node]))
      state.nodes = body.nodes.map((item) => byID.get(item.id))
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
      const node = makeNode(id, body)
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
          return send(status, { error: `check failed ${status}`, upstream: { error: 'upstream failed' } })
        }
        Object.assign(node, { node_ip: '198.51.100.8', country_code: 'US', location: 'Boston, United States', latency: 50 })
        return send(200, { ip: node.node_ip, country_code: node.country_code, location: node.location, latency: node.latency })
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
    state.update = { available: true, latest_version: '9.0.2', release_url: 'https://github.com/cheluen/singbox-proxy-manager/releases/tag/v9.0.2' }
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('a[data-testid="version-badge"].is-update', { visible: true })
    assert.equal(await page.$eval('[data-testid="version-badge"]', (element) => element.href), state.update.release_url)
    assert.ok((await page.$eval('[data-testid="version-badge"]', (element) => element.textContent)).includes('9.0.2'))
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
