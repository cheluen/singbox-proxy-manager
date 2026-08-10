import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30017)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5181)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

const createNodes = (firstName) =>
  Array.from({ length: 800 }, (_, index) => ({
    id: index + 1,
    name: index === 0 ? firstName : `node-${index + 1}`,
    remark: '',
    type: 'direct',
    config: '{}',
    inbound_port: 31001 + index,
    inbound_port_pinned: false,
    username: '',
    password: '',
    tcp_reuse_enabled: true,
    sort_order: index,
    node_ip: '',
    location: '',
    country_code: '',
    latency: 0,
    enabled: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  }))

const sendJson = (response, statusCode, payload) => {
  if (response.destroyed || response.writableEnded) return
  const body = JSON.stringify(payload)
  response.writeHead(statusCode, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
  })
  response.end(body)
}

const createMockApi = () => {
  let raceMode = false
  let raceRequests = 0
  let runtimeDegraded = true
  const initialNodes = createNodes('initial-node')
  const staleNodes = createNodes('stale-node')
  const latestNodes = createNodes('latest-node')

  const server = http.createServer((request, response) => {
    if (request.method === 'GET' && request.url === '/api/version') {
      sendJson(response, 200, { version: FRONTEND_PACKAGE.version })
      return
    }
    if (request.method === 'GET' && request.url === '/api/settings') {
      sendJson(response, 200, { start_port: 30001, preserve_inbound_ports: false })
      return
    }
    if (request.method === 'GET' && request.url === '/api/runtime/status') {
      sendJson(response, 200, runtimeDegraded
        ? { state: 'degraded', running: false, degraded: true, message: 'test runtime stopped' }
        : { state: 'running', running: true, degraded: false })
      return
    }
    if (request.method === 'POST' && request.url === '/api/runtime/restart') {
      runtimeDegraded = false
      sendJson(response, 200, {
        message: 'restarted',
        status: { state: 'running', running: true, degraded: false },
      })
      return
    }
    if (request.method === 'GET' && request.url === '/api/nodes') {
      if (!raceMode) {
        sendJson(response, 200, initialNodes)
        return
      }
      raceRequests += 1
      const requestNumber = raceRequests
      const delay = requestNumber === 1 ? 1200 : 20
      const payload = requestNumber === 1 ? staleNodes : latestNodes
      setTimeout(() => sendJson(response, 200, payload), delay)
      return
    }
    if (request.method === 'PUT' && request.url === '/api/nodes/1/replace') {
      request.resume()
      request.on('end', () => sendJson(response, 200, { message: 'replaced' }))
      return
    }
    sendJson(response, 404, { error: 'not_found', method: request.method, url: request.url })
  })

  return {
    server,
    beginRace: () => {
      raceMode = true
      raceRequests = 0
    },
    raceRequestCount: () => raceRequests,
  }
}

const listen = (server, options) =>
  new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(options, resolve)
  })

const waitForHttp = async (url, timeout = 60000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.status < 500) return
    } catch {
      // retry
    }
    await sleep(200)
  }
  throw new Error(`timed out waiting for ${url}`)
}

const browserExecutable = () => {
  if (process.env.PUPPETEER_EXECUTABLE_PATH) return process.env.PUPPETEER_EXECUTABLE_PATH
  for (const candidate of [
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/usr/bin/chromium-browser',
    '/usr/bin/chromium',
  ]) {
    try {
      fs.accessSync(candidate, fs.constants.X_OK)
      return candidate
    } catch {
      // continue
    }
  }
  return undefined
}

const startVite = () => {
  const viteBin = path.join(FRONTEND_ROOT, 'node_modules', 'vite', 'bin', 'vite.js')
  const logs = []
  const child = spawn(
    process.execPath,
    [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)],
    {
      cwd: FRONTEND_ROOT,
      env: {
        ...process.env,
        E2E_API_PORT: String(API_PORT),
        VITE_API_TARGET: `http://127.0.0.1:${API_PORT}`,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    }
  )
  child.stdout.on('data', (chunk) => logs.push(chunk.toString()))
  child.stderr.on('data', (chunk) => logs.push(chunk.toString()))
  return { child, logs: () => logs.join('') }
}

const stopChild = async (child) => {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([new Promise((resolve) => child.once('exit', resolve)), sleep(3000)])
  if (child.exitCode === null) {
    child.kill('SIGKILL')
    await new Promise((resolve) => child.once('exit', resolve))
  }
}

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const isIgnorableConsoleError = (text) =>
  text.includes('Support for defaultProps will be removed from memo components') ||
  text.includes('[antd: message] Static function can not consume context') ||
  text.includes('Failed to load resource: the server responded with a status of 404')

const waitForReplaceInput = async (page) => {
  try {
    await page.waitForSelector('[data-testid="replace-node-link-input"]', {
      visible: true,
      timeout: 5000,
    })
  } catch (error) {
    const diagnostics = await page.evaluate(() => ({
      modalCount: document.querySelectorAll('.ant-modal').length,
      openModalCount: document.querySelectorAll('.ant-modal-wrap:not([style*="display: none"])').length,
      replaceButtonCount: document.querySelectorAll('[data-testid^="replace-node-"]').length,
      activeElement: document.activeElement?.outerHTML?.slice(0, 500) || '',
      bodyText: document.body.textContent?.slice(0, 1000) || '',
    }))
    throw new Error(`replace modal did not open: ${error.message}; diagnostics=${JSON.stringify(diagnostics)}`)
  }
}

const run = async () => {
  const mock = createMockApi()
  await listen(mock.server, { host: '127.0.0.1', port: API_PORT })
  const vite = startVite()
  let browser

  try {
    await waitForHttp(FRONTEND_URL)
    browser = await puppeteer.launch({
      headless: true,
      executablePath: browserExecutable(),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })
    const page = await browser.newPage()
    const consoleErrors = []
    page.on('pageerror', (error) => consoleErrors.push(`pageerror:${error.message}`))
    page.on('console', (message) => {
      if (message.type() === 'error' && !isIgnorableConsoleError(message.text())) {
        consoleErrors.push(message.text())
      }
    })

    await page.goto(FRONTEND_URL, { waitUntil: 'networkidle2' })
    await page.evaluate(() => localStorage.setItem('token', 'e2e-token'))
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('[data-testid="replace-node-1"]', { timeout: 30000 })
    await page.waitForSelector('[data-testid="runtime-restart"]', { visible: true })
    await page.click('[data-testid="runtime-restart"]')
    await page.waitForSelector('[data-testid="runtime-status-degraded"]', { hidden: true })

    const cdp = await page.target().createCDPSession()
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 })
    await page.click('[data-testid="replace-node-1"]')
    await waitForReplaceInput(page)
    const inputText = `vless://${'a'.repeat(160)}@example.com:443?security=tls#latency-check`
    const inputStarted = Date.now()
    await page.type('[data-testid="replace-node-link-input"]', inputText, { delay: 0 })
    const inputElapsed = Date.now() - inputStarted
    const inputValue = await page.$eval('[data-testid="replace-node-link-input"]', (element) => element.value)
    assert(inputValue === inputText, `replace input lost characters: ${inputValue.length}/${inputText.length}`)
    assert(inputElapsed / inputText.length < 80, `replace input remained sluggish: ${inputElapsed}ms for ${inputText.length} characters`)
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 1 })
    await page.click('.ant-modal .ant-btn-default')
    await page.waitForSelector('[data-testid="replace-node-link-input"]', { hidden: true })

    mock.beginRace()
    await page.click('[data-testid="nodes-refresh"]')
    const requestDeadline = Date.now() + 3000
    while (mock.raceRequestCount() < 1 && Date.now() < requestDeadline) {
      await sleep(10)
    }
    assert(mock.raceRequestCount() === 1, 'slow refresh request did not start')

    // The explicit refresh displays Ant Design's loading mask. Trigger the
    // underlying action directly so this test can intentionally create the
    // otherwise narrow overlap between an old visible refresh and the silent
    // refresh performed after a mutation.
    await page.$eval('[data-testid="replace-node-1"]', (element) => element.click())
    await waitForReplaceInput(page)
    await page.type('[data-testid="replace-node-link-input"]', 'direct://replacement')
    await page.click('.ant-modal .ant-btn-primary')
    await page.waitForFunction(
      () => document.body.textContent?.includes('latest-node'),
      { timeout: 5000 }
    )
    await sleep(1400)
    const bodyText = await page.evaluate(() => document.body.textContent || '')
    assert(bodyText.includes('latest-node'), 'latest node response was not rendered')
    assert(!bodyText.includes('stale-node'), 'older node response overwrote the latest state')
    assert(mock.raceRequestCount() >= 2, `expected two overlapping node loads, got ${mock.raceRequestCount()}`)
    assert(consoleErrors.length === 0, `browser errors: ${consoleErrors.join(' | ')}`)

    console.log(JSON.stringify({ success: true, inputElapsed, characters: inputText.length }))
  } catch (error) {
    throw new Error(`${error.message}\nVite logs:\n${vite.logs()}`)
  } finally {
    if (browser) await browser.close()
    await stopChild(vite.child)
    await new Promise((resolve) => mock.server.close(resolve))
  }
}

await run()
