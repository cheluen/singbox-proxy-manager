import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import puppeteer from 'puppeteer-core'
import { findTestPortRange } from './test-ports.mjs'
import { findBrowserExecutable, stopProcess } from './test-harness.mjs'
import { clickControl, setInput, sleep } from './ui-driver.mjs'

const binary = process.env.SBPM_E2E_BACKEND_BINARY
assert.ok(binary && path.isAbsolute(binary), '需要统一发行文件的绝对路径')
const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'sbpm-node-list-'))
const environment = { ...process.env }
for (const key of ['DATABASE_URL', 'POSTGRES_DATABASE_URL', 'POSTGRES_URL', 'PGSQL_DATABASE_URL', 'PGSQL', 'MYSQL_DATABASE_URL', 'MYSQL', 'TURSO_DATABASE_URL', 'TURSO_AUTH_TOKEN', 'SBPM_NODES_VIRTUAL_THRESHOLD']) delete environment[key]
const secret = randomBytes(24).toString('hex')
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
    links: Array.from({ length: 202 }, (_, index) => `socks5://127.0.0.1:9#ThresholdCase_${String(index + 1).padStart(3, '0')}`), enabled: false,
  })
  assert.equal(imported.success, 202)
  assert.equal(imported.failed, 0)
  const expectedNodes = await api('GET', '/nodes')
  assert.equal(expectedNodes.length, 202)
  await stopBackend()
  browser = await puppeteer.launch({
    headless: true, executablePath: findBrowserExecutable(),
    userDataDir: path.join(scratch, 'browser'), args: ['--no-sandbox', '--disable-setuid-sandbox'],
  })

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
      await setInput(page, '[data-testid="nodes-search"]', 'ThresholdCase_202')
      await page.waitForFunction(() => document.querySelectorAll('[data-row-id]').length === 1)
      await clickControl(page, '[data-testid="open-palette"]')
      await setInput(page, '[data-testid="palette-input"]', 'ThresholdCase_202')
      await page.keyboard.press('Enter')
      await page.waitForSelector('[data-testid="node-inspector"]', { visible: true })
      assert.equal(await page.$eval('[data-testid="nodes-search"]', (input) => input.value), '')
      assert.equal(await page.$eval('[data-testid="node-inspector"]', (element) => Number(element.dataset.nodeId)), expectedNodes.at(-1).id)
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
