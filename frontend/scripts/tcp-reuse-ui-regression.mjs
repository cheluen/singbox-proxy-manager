import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30000)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5173)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const SCRIPTS_DIR = path.dirname(SCRIPT_PATH)
const FRONTEND_ROOT = path.resolve(SCRIPTS_DIR, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const FRONTEND_BUILD_VERSION = FRONTEND_PACKAGE.version

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

const createMockNodes = () => [
  {
    id: 1,
    name: 'node-no-auth',
    type: 'direct',
    config: '{}',
    inbound_port: 30001,
    username: '',
    password: '',
    tcp_reuse_enabled: false,
    sort_order: 0,
    node_ip: '',
    location: '',
    country_code: '',
    latency: 0,
    enabled: true,
    remark: '',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 2,
    name: 'node-with-auth',
    type: 'direct',
    config: '{}',
    inbound_port: 30005,
    username: 'user2',
    password: 'pass2',
    tcp_reuse_enabled: true,
    sort_order: 1,
    node_ip: '1.1.1.1',
    location: 'Los Angeles, United States',
    country_code: 'US',
    latency: 60,
    enabled: true,
    remark: 'hello',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
]

const sendJson = (res, statusCode, payload) => {
  const body = JSON.stringify(payload)
  res.writeHead(statusCode, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
  })
  res.end(body)
}

const getBrowserExecutablePath = () => {
  const fromEnv = process.env.PUPPETEER_EXECUTABLE_PATH
  if (fromEnv) return fromEnv
  if (process.platform === 'linux') {
    const candidates = [
      '/usr/bin/google-chrome',
      '/usr/bin/google-chrome-stable',
      '/usr/bin/chromium-browser',
      '/usr/bin/chromium',
    ]
    for (const candidate of candidates) {
      try {
        fs.accessSync(candidate, fs.constants.X_OK)
        return candidate
      } catch {
        // keep searching
      }
    }
  }
  return undefined
}

const createMockApiServer = () => {
  let nodes = createMockNodes()
  let lastNodeUpdate = null

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJson(res, 200, { version: FRONTEND_BUILD_VERSION })
      return
    }

    if (req.method === 'GET' && req.url === '/api/nodes') {
      const sorted = [...nodes].sort((a, b) => a.sort_order - b.sort_order)
      sendJson(res, 200, sorted)
      return
    }

    if (req.method === 'PUT' && /^\/api\/nodes\/\d+$/.test(req.url || '')) {
      const match = req.url.match(/^\/api\/nodes\/(\d+)$/)
      const id = Number(match?.[1] || 0)
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        const index = nodes.findIndex((node) => node.id === id)
        if (index === -1) {
          sendJson(res, 404, { error: 'node not found' })
          return
        }
        nodes = nodes.map((node) =>
          node.id === id
            ? {
                ...node,
                ...payload,
                id: node.id,
                updated_at: new Date().toISOString(),
              }
            : node
        )
        lastNodeUpdate = { id, payload }
        sendJson(res, 200, nodes[index])
      })
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { nodes, lastNodeUpdate })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ nodes, lastNodeUpdate })
  return { server, getState }
}

const tryListen = (server, options) =>
  new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(options, resolve)
  })

const startMockApi = async () => {
  const primary = createMockApiServer()
  try {
    await tryListen(primary.server, { port: API_PORT, host: '::', ipv6Only: false })
    return primary
  } catch {
    try {
      primary.server.close()
    } catch {
      // ignore
    }
  }

  const fallback = createMockApiServer()
  await tryListen(fallback.server, { port: API_PORT, host: '127.0.0.1' })
  return fallback
}

const waitForHttpReady = async (url, timeoutMs) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.status < 500) return
    } catch {
      // ignore retry
    }
    await sleep(500)
  }
  throw new Error(`Timed out waiting for ${url}`)
}

const startVite = (frontendRoot) => {
  const viteBin = path.join(frontendRoot, 'node_modules', 'vite', 'bin', 'vite.js')
  const logs = []
  const child = spawn(process.execPath, [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)], {
    cwd: frontendRoot,
    env: { ...process.env },
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  child.stdout.on('data', (chunk) => {
    logs.push(chunk.toString())
  })
  child.stderr.on('data', (chunk) => {
    logs.push(chunk.toString())
  })

  return {
    child,
    getLogs: () => logs.join(''),
  }
}

const stopProcess = async (child) => {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    sleep(3000),
  ])
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
  text.includes('[antd: Modal] `destroyOnClose` is deprecated') ||
  text.includes("[antd: message] Static function can not consume context like dynamic theme") ||
  text.includes('Failed to load resource: the server responded with a status of 404 (Not Found)')

const run = async () => {
  const mockApi = await startMockApi()
  await waitForHttpReady(`http://localhost:${API_PORT}/api/version`, 10000)
  const vite = startVite(FRONTEND_ROOT)
  let browser

  try {
    await waitForHttpReady(FRONTEND_URL, 60000)

    const executablePath = getBrowserExecutablePath()
    browser = await puppeteer.launch({
      headless: true,
      executablePath,
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })

    const page = await browser.newPage()
    await page.setViewport({ width: 1366, height: 900 })

    const consoleErrors = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        const text = msg.text()
        if (!isIgnorableConsoleError(text)) {
          consoleErrors.push(text)
        }
      }
    })
    page.on('pageerror', (err) => {
      consoleErrors.push(`pageerror:${err.message}`)
    })

    await page.goto(FRONTEND_URL, { waitUntil: 'networkidle2' })
    await page.evaluate(() => {
      localStorage.setItem('token', 'e2e-token')
    })
    await page.reload({ waitUntil: 'networkidle2' })

    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 30000 })

    const switchCount = await page.$$eval(
      'tbody.ant-table-tbody tr[data-row-key="1"] .ant-switch',
      (switches) => switches.length
    )
    assert(switchCount === 2, `Expected 2 state switches on row 1, got ${switchCount}`)

    const hasHorizontalOverflow = await page.$eval('.ant-table-content', (node) => node.scrollWidth > node.clientWidth)
    assert(!hasHorizontalOverflow, 'Table has horizontal overflow at 1366px viewport')

    await page.click('tbody.ant-table-tbody tr[data-row-key="1"] .anticon-edit')
    await page.waitForSelector('.ant-modal .ant-modal-content', { timeout: 10000 })
    const modalContainsTCPReuseField = await page.$eval('.ant-modal', (modal) => {
      const text = modal.textContent || ''
      return text.includes('TCP Inbound Port Reuse') || text.includes('TCP 入站端口复用')
    })
    assert(!modalContainsTCPReuseField, 'TCP reuse switch should not appear in edit modal')
    await page.click('.ant-modal .ant-modal-close')
    await page.waitForSelector('.ant-modal', { hidden: true, timeout: 10000 })

    await page.$$eval('tbody.ant-table-tbody tr[data-row-key="1"] .ant-switch', (switches) => {
      switches[1]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await sleep(900)

    const messageTexts = await page.$$eval('.ant-message .ant-message-notice-content', (nodes) =>
      nodes.map((node) => (node.textContent || '').trim())
    )
    const warningShown = messageTexts.some((text) =>
      text.includes('未配置入站用户名/密码') ||
      text.includes('TCP reuse cannot work until auth is configured')
    )
    assert(warningShown, `Expected tcp reuse auth warning, got: ${JSON.stringify(messageTexts)}`)

    const state = mockApi.getState()
    assert(Boolean(state.lastNodeUpdate), 'Expected node update request after toggling tcp reuse')
    assert(state.lastNodeUpdate.id === 1, `Expected update on node 1, got ${JSON.stringify(state.lastNodeUpdate)}`)
    assert(
      state.lastNodeUpdate.payload?.tcp_reuse_enabled === true,
      `Expected tcp_reuse_enabled=true payload, got ${JSON.stringify(state.lastNodeUpdate.payload)}`
    )

    assert(consoleErrors.length === 0, `Unexpected console errors: ${consoleErrors.join(' | ')}`)

    console.log(
      JSON.stringify(
        {
          success: true,
          switchCount,
          hasHorizontalOverflow,
          warningShown,
          lastNodeUpdate: state.lastNodeUpdate,
        },
        null,
        2
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E TCP reuse UI regression failed.')
    console.error(error)
    if (viteLogs) {
      console.error('Vite logs:')
      console.error(viteLogs)
    }
    process.exitCode = 1
  } finally {
    if (browser) {
      await browser.close()
    }
    await stopProcess(vite.child)
    await new Promise((resolve) => mockApi.server.close(resolve))
  }
}

await run()
