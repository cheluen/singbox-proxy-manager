import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { findTestPortRange } from './test-ports.mjs'
import { launchBrowser, stopProcess } from './test-harness.mjs'
import { isExpectedSystemBrowserError } from './system-browser-errors.mjs'
import { PROJECT_MARK_FILE, PROJECT_NAME } from '../src/core/project.js'
import { assertProjectBrand, clickControl, clickText, confirmDialog, openBatchImport, openNodeMenu, saveNodeForm, selectOption, setInput, sleep, toggle, waitOverlaysClosed } from './ui-driver.mjs'
import { assertDetailLayoutOrderAndBalance, assertFullNodeName, assertInboundUsername, assertMaskedInboundPassword, assertRevealedInboundPassword, assertRouteId } from './node-display-assertions.mjs'

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const VERSION = process.env.APP_VERSION || JSON.parse(fs.readFileSync(path.join(FRONTEND, 'package.json'), 'utf8')).version
const BINARY = process.env.SBPM_E2E_BACKEND_BINARY
const MODE = process.env.SBPM_E2E_DATABASE_MODE || 'sqlite'
const DATABASE_KEYS = ['DATABASE_URL', 'POSTGRES_DATABASE_URL', 'POSTGRES_URL', 'PGSQL_DATABASE_URL', 'PGSQL', 'MYSQL_DATABASE_URL', 'MYSQL', 'TURSO_DATABASE_URL', 'TURSO_AUTH_TOKEN']
assert.ok(BINARY && path.isAbsolute(BINARY), '需要统一发行文件的绝对路径')
assert.ok(['sqlite', 'postgres', 'mysql', 'turso'].includes(MODE), '不支持的数据库类型')
if (MODE === 'turso') assert.ok(process.env.TURSO_DATABASE_URL && process.env.TURSO_AUTH_TOKEN, 'Turso 配置不完整')
if (MODE === 'postgres' || MODE === 'mysql') assert.ok(process.env.DATABASE_URL?.startsWith(`${MODE === 'postgres' ? 'postgres' : 'mysql'}:`), '远程数据库配置不匹配')

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
const wait = async (predicate, description, timeout = 40000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (await predicate()) return
    await sleep(100)
  }
  throw new Error(description)
}
const stop = (child) => stopProcess(child, { graceMs: 15000, requireGraceful: true })
const curl = (args) => new Promise((resolve, reject) => {
  const child = spawn('curl', ['--silent', '--show-error', '--fail', '--max-time', '12', '--noproxy', '', ...args], { stdio: ['ignore', 'pipe', 'pipe'] })
  let stdout = ''
  let stderr = ''
  child.stdout.on('data', (chunk) => { stdout += chunk })
  child.stderr.on('data', (chunk) => { stderr += chunk })
  child.once('error', reject)
  child.once('exit', (code) => resolve({ code, stdout, stderr }))
})

const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'sbpm-system-'))
const configDir = path.join(scratch, 'config')
const SECRET = `System-${randomBytes(18).toString('hex')}`
const NEXT_SECRET = `Rotated-${randomBytes(18).toString('hex')}`
const INBOUND_SECRET = 'e2e-inbound-secret'
const ROTATED_INBOUND_SECRET = `Rotated-${'regression-'.repeat(8)}`
const RENAMED_NODE = `System direct renamed ${'FullNodeName_'.repeat(10)}`
const logs = []
const browserErrors = []
const steps = []
let backend
let browser
let page
let token = ''
let secret = SECRET
let targetHits = 0
let expectAuthRevocation = false
let expectRestart = false
const target = http.createServer((request, response) => {
  targetHits += 1
  if (request.url === '/ip') {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ ip: '203.0.113.20', country: 'Test', cc: 'TT' }))
  } else {
    response.writeHead(200, { 'Content-Type': 'text/plain' })
    response.end('sbpm-system-forwarding-ok')
  }
})
const started = Date.now()

try {
  const targetPort = await listen(target)
  const port = await freePort()
  const base = `http://127.0.0.1:${port}`
  const environment = { ...process.env }
  if (MODE === 'sqlite') for (const key of DATABASE_KEYS) delete environment[key]
  const start = async () => {
    backend = spawn(BINARY, [], {
      cwd: scratch,
      env: { ...environment, ACCEPT_USER_AGREEMENT: 'true', ADMIN_PASSWORD: secret, CONFIG_DIR: configDir, BIND_ADDRESS: '127.0.0.1', PORT: String(port), SBPM_UPDATE_CHECK_DISABLED: '1', SBPM_IPCHECK_URLS: `http://127.0.0.1:${targetPort}/ip`, TZ: 'UTC' },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    backend.stdout.on('data', (chunk) => logs.push(chunk.toString()))
    backend.stderr.on('data', (chunk) => logs.push(chunk.toString()))
    await wait(async () => {
      assert.equal(backend.exitCode, null, '后端在就绪前退出')
      try { return (await fetch(`${base}/api/version`)).status === 200 } catch { return false }
    }, '后端未就绪')
  }
  const request = async (method, endpoint, body, authorization = token) => {
    const response = await fetch(`${base}/api${endpoint}`, {
      method, headers: { 'Content-Type': 'application/json', Authorization: authorization },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    return { status: response.status, body: await response.json() }
  }
  const api = async (method, endpoint, body) => {
    const response = await request(method, endpoint, body)
    assert.ok(response.status < 400, `${method} ${endpoint} 失败：${response.status}`)
    return response.body
  }
  const submit = async (method, endpoint, action) => {
    const [response] = await Promise.all([
      page.waitForResponse((response) => response.request().method() === method && new URL(response.url()).pathname === `/api${endpoint}`, { timeout: 90000 }),
      action(),
    ])
    const body = await response.json()
    assert.ok(response.status() < 400, `界面提交 ${method} ${endpoint} 失败：${response.status()}`)
    return body
  }
  await start()
  assert.equal((await request('GET', '/nodes', undefined, '')).status, 401)
  assert.equal((await request('POST', '/login', { password: 'incorrect' }, '')).status, 401)
  const htmlResponse = await fetch(base)
  const html = await htmlResponse.text()
  assert.equal(htmlResponse.headers.get('x-app-version'), VERSION)
  assert.ok(html.includes(`name="sbpm-build-version" content="${VERSION}"`))
  const markResponse = await fetch(`${base}/${PROJECT_MARK_FILE}`)
  assert.equal(markResponse.status, 200)
  assert.ok(markResponse.headers.get('content-type')?.startsWith('image/svg+xml'), '品牌图标不可返回 SPA 的 HTML')
  assert.ok((await markResponse.text()).includes(`>${PROJECT_NAME}</title>`))
  assert.equal(htmlResponse.headers.get('cache-control'), 'no-cache, max-age=0, must-revalidate')
  assert.equal((await api('GET', '/version')).version, VERSION)
  const health = await api('GET', '/health')
  assert.equal(health.status, 'ok')
  steps.push('version-health-assets-auth')

  browser = await launchBrowser({ userDataDir: path.join(scratch, 'browser') })
  page = await browser.newPage()
  page.setDefaultTimeout(30000)
  await page.setViewport({ width: 1440, height: 1000 })
  await page.evaluateOnNewDocument(() => localStorage.setItem('language', 'en'))
  page.on('pageerror', (error) => browserErrors.push(error.message))
  page.on('console', (message) => {
    if (message.type() !== 'error') return
    let endpoint = ''
    try { endpoint = new URL(message.location().url).pathname } catch { /* 保留非网络错误 */ }
    if (isExpectedSystemBrowserError({ text: message.text(), url: message.location().url }, {
      origin: base, restarting: expectRestart, revoking: expectAuthRevocation,
    })) return
    browserErrors.push({ text: message.text(), endpoint, restarting: expectRestart, revoking: expectAuthRevocation })
  })
  await page.goto(base, { waitUntil: 'networkidle2' })
  assert.equal(await page.$('.demo-bar, [data-testid="login-fill-demo"]'), null)
  for (const width of [320, 390, 1440]) {
    await page.setViewport({ width, height: 900 })
    await assertProjectBrand(page, { login: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${width}px 登录页溢出`)
    assert.equal(await page.$$eval('input[type="password"]', (inputs) => inputs.length), 1)
  }
  await setInput(page, '[data-testid="login-secret"]', SECRET)
  await clickControl(page, '[data-testid="login-submit"]')
  await page.waitForSelector('[data-testid="nodes-add-node"]', { visible: true })
  token = await page.evaluate(() => localStorage.getItem('token'))
  assert.ok(token)
  assert.deepEqual(await api('GET', '/nodes'), [], '测试数据库包含旧节点')
  for (const width of [320, 390, 900, 1440]) {
    await page.setViewport({ width, height: 900 })
    await assertProjectBrand(page)
  }
  steps.push('real-login-empty-database')

  const inboundStart = await findTestPortRange()
  const initialGroups = await api('GET', '/node-groups')
  assert.ok(Array.isArray(initialGroups) && initialGroups.length >= 1, '未返回默认分组')
  await clickControl(page, '[data-testid="group-settings-button"]')
  await clickText(page, 'Group Settings', { root: '.ant-dropdown:not(.ant-dropdown-hidden)' })
  await setInput(page, '[data-testid="group-start-port"]', inboundStart)
  await submit('PATCH', `/node-groups/${initialGroups[0].id}`, () => clickControl(page, '[data-testid="group-form-submit"]'))
  await waitOverlaysClosed(page)
  assert.equal((await api('GET', '/node-groups'))[0].start_port, inboundStart)

  await clickControl(page, '[data-testid="nodes-add-node"]')
  await selectOption(page, 'proxy_type', 'Direct (Local)')
  await setInput(page, '#name', 'System direct')
  await setInput(page, '#username', 'system-user')
  await setInput(page, '#password', INBOUND_SECRET)
  const direct = await submit('POST', '/nodes', () => saveNodeForm(page))
  await waitOverlaysClosed(page)
  assert.equal(direct.type, 'direct')
  assert.equal(direct.username, 'system-user')
  const openDirect = `[data-testid="node-open-${direct.id}"]`
  const revealDirect = `[data-testid="node-password-mask-${direct.id}"]`
  await assertInboundUsername(page, direct.id, 'system-user')
  const originalMaskWidth = await assertMaskedInboundPassword(page, direct.id, INBOUND_SECRET)
  await clickControl(page, revealDirect)
  await assertRevealedInboundPassword(page, direct.id, INBOUND_SECRET)
  assert.equal(await page.$('[data-testid="node-inspector"]'), null)
  const forward = async (node, username, password) => {
    for (const protocol of ['http', 'socks5']) {
      const transport = protocol === 'http' ? ['--proxy', `http://127.0.0.1:${node.inbound_port}`] : ['--socks5-hostname', `127.0.0.1:${node.inbound_port}`]
      const result = await curl([...transport, '--proxy-user', `${username}:${password}`, `http://127.0.0.1:${targetPort}/`])
      assert.equal(result.code, 0, `${protocol} 真实转发失败：${result.stderr}`)
      assert.equal(result.stdout, 'sbpm-system-forwarding-ok')
    }
  }
  await forward(direct, 'system-user', INBOUND_SECRET)
  assert.ok(targetHits >= 2)
  const checked = await submit('POST', `/nodes/${direct.id}/check-ip`, async () => {
    await clickControl(page, openDirect)
    await clickControl(page, `[data-testid="node-check-${direct.id}"]`)
  })
  assert.equal(checked.ip, '203.0.113.20')
  assert.equal(checked.transport, 'http')
  await assertFullNodeName(page, direct.id, 'System direct')
  await assertRouteId(page, direct.id, `system-user+${direct.inbound_port}`)
  await assertDetailLayoutOrderAndBalance(page, direct.id, 1440)
  steps.push('ui-create-http-socks-ipcheck')

  const remark = 'system first line\nsecond line  with spaces'
  await setInput(page, '.sb-remark textarea', remark)
  await submit('PUT', `/nodes/${direct.id}/remark`, () => clickText(page, 'Save', { root: '.sb-remark' }))
  await clickControl(page, `[data-testid="node-edit-${direct.id}"]`)
  await setInput(page, '#name', RENAMED_NODE)
  await submit('PUT', `/nodes/${direct.id}`, () => saveNodeForm(page))
  await waitOverlaysClosed(page)
  const stored = await api('GET', `/nodes/${direct.id}`)
  assert.equal(stored.remark, remark)
  assert.equal(stored.name, RENAMED_NODE)
  assert.equal(stored.username, 'system-user')
  await assertFullNodeName(page, direct.id, RENAMED_NODE)
  const flag = await submit('PATCH', `/nodes/${direct.id}`, () => clickControl(page, `[data-testid="node-tcp-reuse-${direct.id}"]`))
  assert.equal(flag.tcp_reuse_enabled, false)
  await assertRouteId(page, direct.id, '')
  await forward(stored, 'system-user', INBOUND_SECRET)
  steps.push('ui-edit-remark-flags')

  await openBatchImport(page)
  await page.locator('.sb-overlay .ant-checkbox-input').click()
  await setInput(page, '[data-testid="batch-import-content"]', 'socks5://127.0.0.1:9#System imported\nvless://00000000-0000-0000-0000-000000000000@127.0.0.1:443?flow=invalid-flow#invalid')
  const summary = await submit('POST', '/nodes/batch-import', () => clickControl(page, '[data-testid="batch-import-submit"]'))
  assert.equal(summary.success, 1)
  assert.equal(summary.failed, 1)
  await page.waitForSelector('[data-testid="batch-import-errors"]', { visible: true })
  assert.equal(await page.$eval('[data-testid="batch-import-submit"]', (element) => element.disabled), true)
  for (const width of [390, 1440]) {
    await page.setViewport({ width, height: 900 })
    assert.equal(await page.$eval('[data-testid="batch-import-errors"]', (element) => element.scrollWidth > element.clientWidth), false)
  }
  await clickText(page, 'Close')
  await waitOverlaysClosed(page)
  const imported = (await api('GET', '/nodes')).find((node) => node.name === 'System imported')
  assert.ok(imported)
  await assertInboundUsername(page, imported.id, imported.username)
  await assertMaskedInboundPassword(page, imported.id, imported.password)
  await openNodeMenu(page, imported.id, 'Export share link')
  await page.waitForSelector('.sb-overlay textarea', { visible: true })
  assert.ok((await page.$eval('.sb-overlay textarea', (element) => element.value)).startsWith('socks5://'))
  await clickText(page, 'Close')
  await waitOverlaysClosed(page)
  steps.push('batch-partial-export-responsive')

  await clickControl(page, '[data-testid="open-settings"]')
  await toggle(page, '#preserve_inbound_ports', true)
  await submit('PUT', '/settings', () => clickText(page, 'Save Settings'))
  await waitOverlaysClosed(page)
  const ports = new Map((await api('GET', '/nodes')).map((node) => [node.id, node.inbound_port]))
  await openNodeMenu(page, imported.id, 'Move to top')
  await wait(async () => (await api('GET', '/nodes'))[0]?.id === imported.id, '界面排序未保存')
  for (const node of await api('GET', '/nodes')) assert.equal(node.inbound_port, ports.get(node.id))
  steps.push('settings-sort-preserve-ports')

  // 真实前后端 + 数据库列宽持久化验收：拖拽列宽落库、不重启 sing-box、跨进程重启恢复并可一键复位。
  await page.setViewport({ width: 1440, height: 1000 })
  const runtimeBeforeResize = await api('GET', '/runtime/status')
  const nameColBefore = await page.$eval('.dusk-head [data-col-key="name"]', (el) => Math.round(el.getBoundingClientRect().width))
  const nameHandle = await page.$('[data-testid="col-resizer-name"]')
  assert.ok(nameHandle, '缺少名称列宽调节手柄')
  const nameHandleBox = await nameHandle.boundingBox()
  await submit('PUT', '/settings', async () => {
    const startX = nameHandleBox.x + nameHandleBox.width / 2
    const y = nameHandleBox.y + nameHandleBox.height / 2
    await page.mouse.move(startX, y)
    await page.mouse.down()
    await page.mouse.move(startX + 80, y, { steps: 10 })
    await page.mouse.up()
  })
  const savedColumnWidths = (await api('GET', '/settings')).node_list_column_widths
  assert.ok(Math.abs(savedColumnWidths.name - (nameColBefore + 80)) <= 4, `持久化列宽异常：${JSON.stringify(savedColumnWidths)}`)
  assert.equal((await api('GET', '/runtime/status')).pid, runtimeBeforeResize.pid, '仅调整列表列宽不应重启 sing-box 内核')

  await page.waitForNetworkIdle({ idleTime: 500 })
  expectRestart = true
  await stop(backend)
  await start()
  await page.reload({ waitUntil: 'networkidle0' })
  await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
  expectRestart = false
  assert.equal(await page.evaluate(() => localStorage.getItem('token')), token)
  const nameColAfterRestart = await page.$eval('.dusk-head [data-col-key="name"]', (el) => Math.round(el.getBoundingClientRect().width))
  assert.ok(Math.abs(nameColAfterRestart - savedColumnWidths.name) <= 2, `重启后端后列宽未从数据库恢复：${nameColAfterRestart} vs ${savedColumnWidths.name}`)
  await submit('PUT', '/settings', () => clickControl(page, '[data-testid="reset-column-widths"]'))
  assert.deepEqual((await api('GET', '/settings')).node_list_column_widths, {})
  assert.equal((await api('GET', `/nodes/${direct.id}`)).remark, remark)
  await assertMaskedInboundPassword(page, direct.id, INBOUND_SECRET)
  await forward(await api('GET', `/nodes/${direct.id}`), 'system-user', INBOUND_SECRET)
  steps.push('restart-database-column-widths-session-persistence')

  await setInput(page, '[data-testid="nodes-search"]', 'System direct renamed')
  await page.waitForFunction((id) => {
    const rows = Array.from(document.querySelectorAll('[data-row-id]'))
    return rows.length === 1 && Number(rows[0].dataset.rowId) === id
  }, {}, direct.id)
  await clickControl(page, revealDirect)
  await assertRevealedInboundPassword(page, direct.id, INBOUND_SECRET)
  await clickControl(page, `[data-testid="node-select-${direct.id}"]`)
  await clickControl(page, '[data-testid="nodes-batch-auth"]')
  await setInput(page, '[data-testid="batch-auth-username"]', 'rotated-user')
  await setInput(page, '[data-testid="batch-auth-secret"]', ROTATED_INBOUND_SECRET)
  const auth = await submit('POST', '/nodes/batch-auth', () => clickControl(page, '[data-testid="batch-auth-form"] button[type="submit"]'))
  assert.ok(auth)
  await waitOverlaysClosed(page)
  const rotated = await api('GET', `/nodes/${direct.id}`)
  assert.equal(rotated.username, 'rotated-user')
  assert.notEqual((await api('GET', `/nodes/${imported.id}`)).username, 'rotated-user')
  await assertInboundUsername(page, direct.id, 'rotated-user')
  const rotatedMaskWidth = await assertMaskedInboundPassword(page, direct.id, ROTATED_INBOUND_SECRET)
  assert.ok(Math.abs(originalMaskWidth - rotatedMaskWidth) <= 1, '真实密码长度变化改变了遮蔽宽度')
  await clickControl(page, revealDirect)
  await assertRevealedInboundPassword(page, direct.id, ROTATED_INBOUND_SECRET)
  await forward(rotated, 'rotated-user', ROTATED_INBOUND_SECRET)
  steps.push('filtered-auth-live-forwarding')

  await setInput(page, '[data-testid="nodes-search"]', '')
  await clickControl(page, '[data-testid="nodes-clear-selection"]')
  await clickControl(page, '[data-testid="open-settings"]')
  await setInput(page, '#admin_secret', NEXT_SECRET)
  await setInput(page, '#admin_secret_confirm', NEXT_SECRET)
  expectAuthRevocation = true
  const changed = await submit('PUT', '/settings', () => clickText(page, 'Save Settings'))
  assert.equal(changed.password_changed, true)
  await page.waitForSelector('[data-testid="login-form"]', { visible: true })
  assert.equal(await page.evaluate(() => localStorage.getItem('token')), null)
  assert.equal((await request('GET', '/nodes')).status, 401)
  assert.equal((await request('POST', '/login', { password: SECRET }, '')).status, 401)
  secret = NEXT_SECRET
  await setInput(page, '[data-testid="login-secret"]', NEXT_SECRET)
  await clickControl(page, '[data-testid="login-submit"]')
  await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
  token = await page.evaluate(() => localStorage.getItem('token'))
  expectAuthRevocation = false
  assert.equal((await request('GET', '/nodes')).status, 200)
  steps.push('admin-rotation-revocation-relogin')

  const ids = (await api('GET', '/nodes')).map((node) => node.id)
  for (const id of ids) await clickControl(page, `[data-testid="node-select-${id}"]`)
  await clickControl(page, '[data-testid="nodes-batch-delete"]')
  await confirmDialog(page)
  await wait(async () => (await api('GET', '/nodes')).length === 0, '批量删除未完成')
  expectAuthRevocation = true
  await clickControl(page, '[data-testid="logout"]')
  await page.waitForSelector('[data-testid="login-form"]', { visible: true })
  assert.equal((await request('GET', '/nodes')).status, 401)
  assert.deepEqual(browserErrors, [], '真实浏览器出现未预期错误')
  steps.push('batch-delete-logout-cleanup')
  console.log(JSON.stringify({ success: true, database: MODE, version: VERSION, steps, targetHits, durationMs: Date.now() - started }))
} catch (error) {
  if (process.env.E2E_ARTIFACT_DIR && page) {
    fs.mkdirSync(process.env.E2E_ARTIFACT_DIR, { recursive: true })
    await page.screenshot({ path: path.join(process.env.E2E_ARTIFACT_DIR, `system-${MODE}-failure.png`), fullPage: true }).catch(() => {})
  }
  const diagnostics = page ? await page.evaluate(() => ({
    forms: Array.from(document.querySelectorAll('.ant-form-item-explain-error'), (element) => element.textContent),
    titles: Array.from(document.querySelectorAll('.ant-modal-title'), (element) => element.textContent),
  })).catch(() => ({})) : {}
  throw new Error(`${MODE} 系统回归失败：${error.stack}\n已完成：${steps.join(', ')}\n诊断：${JSON.stringify(diagnostics)}\n后端：${logs.join('').slice(-9000)}`)
} finally {
  await browser?.close().catch(() => {})
  await stop(backend)
  target.closeAllConnections?.()
  await new Promise((resolve) => target.close(resolve))
  fs.rmSync(scratch, { recursive: true, force: true })
}
