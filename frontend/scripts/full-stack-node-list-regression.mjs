import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import { findTestPortRange } from './test-ports.mjs'
import { launchBrowser, stopProcess } from './test-harness.mjs'
import { clickControl, setInput, sleep } from './ui-driver.mjs'
import { assertEmptyInboundPassword, assertFullNodeName, assertInboundUsername, assertMaskedInboundPassword, assertRevealedInboundPassword, assertRouteId } from './node-display-assertions.mjs'

const binary = process.env.SBPM_E2E_BACKEND_BINARY
assert.ok(binary && path.isAbsolute(binary), '需要统一发行文件的绝对路径')
const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'sbpm-node-list-'))
const environment = { ...process.env }
for (const key of ['DATABASE_URL', 'POSTGRES_DATABASE_URL', 'POSTGRES_URL', 'PGSQL_DATABASE_URL', 'PGSQL', 'MYSQL_DATABASE_URL', 'MYSQL', 'TURSO_DATABASE_URL', 'TURSO_AUTH_TOKEN', 'SBPM_NODES_VIRTUAL_THRESHOLD']) delete environment[key]
const secret = randomBytes(24).toString('hex')
const listCredentials = [
  { username: 'first-list-user', password: 'List-A7z!' },
  { username: 'last-list-user', password: `List-${'LongCredential_'.repeat(6)}` },
]
const finalName = `ThresholdCase_202_${'FullNodeName_'.repeat(10)}`
const logs = []
const results = []
let backend
let browser
let token = ''
let base = ''

const stopBackend = () => stopProcess(backend, { graceMs: 15000, requireGraceful: true })
const api = async (method, endpoint, body) => {
  const response = await fetch(`${base}/api${endpoint}`, {
    method, headers: { 'Content-Type': 'application/json', Authorization: token },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(120000),
  })
  assert.ok(response.status < 400, `${method} ${endpoint} 失败：${response.status}`)
  return response.json()
}
const startBackend = async (threshold) => {
  const env = {
    ...environment, ADMIN_PASSWORD: secret, CONFIG_DIR: path.join(scratch, 'config'), BIND_ADDRESS: '127.0.0.1',
    PORT: new URL(base).port, SBPM_UPDATE_CHECK_DISABLED: '1', SBPM_BATCH_IMPORT_VALIDATION_TIMEOUT: '2m', TZ: 'UTC',
  }
  if (threshold !== undefined) env.SBPM_NODES_VIRTUAL_THRESHOLD = String(threshold)
  backend = spawn(binary, [], { cwd: scratch, env, stdio: ['ignore', 'pipe', 'pipe'] })
  backend.stdout.on('data', (chunk) => logs.push(chunk.toString()))
  backend.stderr.on('data', (chunk) => logs.push(chunk.toString()))
  for (let attempt = 0; attempt < 300; attempt += 1) {
    assert.equal(backend.exitCode, null, '后端在就绪前退出')
    assert.equal(backend.signalCode, null, '后端在就绪前被终止')
    try { if ((await fetch(`${base}/api/version`)).ok) return } catch { /* 等待监听端口 */ }
    await sleep(100)
  }
  throw new Error('后端未就绪')
}

try {
  base = `http://127.0.0.1:${await findTestPortRange(1)}`
  await startBackend()
  token = (await api('POST', '/login', { password: secret })).token
  await api('PUT', '/settings', { start_port: await findTestPortRange(202) })
  const imported = await api('POST', '/nodes/batch-import', {
    links: Array.from({ length: 202 }, (_, index) => `socks5://127.0.0.1:9#${index === 201 ? finalName : `ThresholdCase_${String(index + 1).padStart(3, '0')}`}`), enabled: false,
  })
  assert.equal(imported.success, 202)
  assert.equal(imported.failed, 0)
  const importedNodes = await api('GET', '/nodes')
  assert.equal(importedNodes.at(-1).name, finalName)
  for (const [node, credentials] of [[importedNodes[0], listCredentials[0]], [importedNodes.at(-1), listCredentials[1]]]) {
    await api('POST', '/nodes/batch-auth', { node_ids: [node.id], auth_enabled: true, ...credentials })
  }
  await api('POST', '/nodes/batch-auth', { node_ids: [importedNodes[1].id], auth_enabled: false })
  const expectedNodes = await api('GET', '/nodes')
  assert.equal(expectedNodes.length, 202)
  const first = expectedNodes[0]
  const last = expectedNodes.at(-1)
  await stopBackend()
  browser = await launchBrowser({ userDataDir: path.join(scratch, 'browser') })

  for (const [threshold, expected] of [[undefined, 120], [0, 0], [1, 1], [32, 32], [9999, 9999], ['invalid', 120]]) {
    await startBackend(threshold)
    const context = await browser.createBrowserContext()
    try {
      const page = await context.newPage()
      const errors = []
      page.setDefaultTimeout(30000)
      page.on('pageerror', (error) => errors.push(error.message))
      page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()) })
      await page.setViewport({ width: 1440, height: 900 })
      await page.evaluateOnNewDocument((authorization) => {
        localStorage.setItem('token', authorization)
        localStorage.setItem('language', 'en')
        // 只延迟首批真实观察器回调，以精确验证首批行数，恢复后仍使用浏览器原生观察器。
        const NativeObserver = window.IntersectionObserver
        const pending = []
        let paused = true
        window.IntersectionObserver = class extends NativeObserver {
          constructor(callback, options) {
            super((entries, observer) => {
              if (paused) pending.push(() => callback(entries, observer))
              else callback(entries, observer)
            }, options)
          }
        }
        window.resumeNodeObservers = () => {
          paused = false
          pending.splice(0).forEach((callback) => callback())
        }
      }, token)
      await page.goto(base, { waitUntil: 'networkidle0' })
      await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
      const firstCount = await page.$$eval('[data-row-id]', (rows) => rows.length)
      assert.equal(firstCount, expected === 0 ? 202 : Math.min(expected, 202), `阈值 ${threshold} 的首批行数错误`)
      assert.equal(await page.$eval('meta[name="sbpm-nodes-virtual-threshold"]', (meta) => Number(meta.content)), expected)
      if (expected === 0 || expected >= 202) assert.equal(await page.$('.dusk-more'), null)
      await page.evaluate(() => window.resumeNodeObservers())
      if (expected === 1) await page.waitForFunction(() => document.querySelectorAll('[data-row-id]').length > 2)
      for (let attempt = 0; attempt < 300; attempt += 1) {
        if (await page.$$eval('[data-row-id]', (rows) => rows.length) === 202) break
        await page.evaluate(() => {
          const scroll = document.querySelector('.dusk__scroll')
          scroll.scrollTop = scroll.scrollHeight
        })
        await sleep(100)
      }
      assert.equal(await page.$$eval('[data-row-id]', (rows) => rows.length), 202, `阈值 ${threshold} 无法滚动加载全部节点`)
      if (threshold === undefined) {
        await assertInboundUsername(page, first.id, listCredentials[0].username)
        await assertInboundUsername(page, last.id, listCredentials[1].username)
        await assertInboundUsername(page, expectedNodes[1].id, '')
        await assertEmptyInboundPassword(page, expectedNodes[1].id)
        const shortMask = await assertMaskedInboundPassword(page, first.id, listCredentials[0].password)
        const longMask = await assertMaskedInboundPassword(page, last.id, listCredentials[1].password)
        assert.ok(Math.abs(shortMask - longMask) <= 1, '真实列表的密码掩码宽度不一致')
      }
      await setInput(page, '[data-testid="nodes-search"]', 'ThresholdCase_202')
      await page.waitForFunction(() => document.querySelectorAll('[data-row-id]').length === 1)
      if (threshold === undefined) {
        await clickControl(page, `[data-testid="node-password-mask-${last.id}"]`)
        await assertRevealedInboundPassword(page, last.id, listCredentials[1].password)
        assert.equal(await page.$('[data-testid="node-inspector"]'), null)
        await page.reload({ waitUntil: 'networkidle0' })
        await setInput(page, '[data-testid="nodes-search"]', 'ThresholdCase_202')
        await assertMaskedInboundPassword(page, last.id, listCredentials[1].password)
      }
      await clickControl(page, '[data-testid="open-palette"]')
      await setInput(page, '[data-testid="palette-input"]', 'ThresholdCase_202')
      await page.keyboard.press('Enter')
      await page.waitForSelector('[data-testid="node-inspector"]', { visible: true })
      assert.equal(await page.$eval('[data-testid="nodes-search"]', (input) => input.value), '')
      assert.equal(await page.$eval('[data-testid="node-inspector"]', (element) => Number(element.dataset.nodeId)), last.id)
      if (threshold === undefined) {
        await assertFullNodeName(page, last.id, finalName)
        await assertRouteId(page, last.id, `${listCredentials[1].username}+${last.inbound_port}`)
      }
      assert.deepEqual(await api('GET', '/nodes'), expectedNodes, '展示阈值或筛选改变了节点数据')
      assert.deepEqual(errors, [], '浏览器出现未预期错误')
      results.push({ threshold: threshold ?? 'default', firstCount, total: 202 })
    } finally {
      await context.close()
      await stopBackend()
    }
  }
  console.log(JSON.stringify({ success: true, scenario: 'full-stack-node-list', results }))
} catch (error) {
  throw new Error(`节点阈值系统回归失败：${error.stack}\n后端：${logs.join('').slice(-4000)}`)
} finally {
  await browser?.close().catch(() => {})
  try {
    await stopBackend()
  } finally {
    fs.rmSync(scratch, { recursive: true, force: true })
  }
}
