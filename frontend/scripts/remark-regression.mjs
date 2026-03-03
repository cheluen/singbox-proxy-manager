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
    name: 'node-1',
    type: 'direct',
    config: '{}',
    inbound_port: 30001,
    username: 'u1',
    password: 'p1',
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
    name: 'node-2',
    type: 'direct',
    config: '{}',
    inbound_port: 30002,
    username: 'u2',
    password: 'p2',
    sort_order: 1,
    node_ip: '',
    location: '',
    country_code: '',
    latency: 0,
    enabled: true,
    remark: 'existing remark',
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
  let lastRemarkUpdate = null

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

    if (req.method === 'PUT' && req.url?.startsWith('/api/nodes/') && req.url.endsWith('/remark')) {
      const match = req.url.match(/^\/api\/nodes\/(\d+)\/remark$/)
      if (!match) {
        sendJson(res, 404, { error: 'not found' })
        return
      }

      const id = Number(match[1])
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        const remark = String(payload.remark ?? '')
        const index = nodes.findIndex((n) => n.id === id)
        if (index === -1) {
          sendJson(res, 404, { error: 'node not found' })
          return
        }
        nodes = nodes.map((node) =>
          node.id === id
            ? { ...node, remark, updated_at: new Date().toISOString() }
            : node
        )
        lastRemarkUpdate = { id, remark }
        sendJson(res, 200, { success: true })
      })
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { nodes, lastRemarkUpdate })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ nodes, lastRemarkUpdate })
  return { server, getState }
}

const tryListen = (server, options) =>
  new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(options, resolve)
  })

const startMockApi = async () => {
  // Prefer binding to IPv6 unspecified (dual-stack) so Vite's `localhost` proxy
  // (which may resolve to ::1 on CI) can reach the mock server.
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

const getRowRemarkIndicatorText = async (page, rowKey) =>
  page.$eval(
    `tbody.ant-table-tbody tr[data-row-key="${rowKey}"] td:nth-child(5)`,
    (cell) => (cell.textContent || '').trim()
  )

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
    const consoleErrors = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        consoleErrors.push(msg.text())
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
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="2"]', { timeout: 30000 })

    const remarkBefore1 = await getRowRemarkIndicatorText(page, 1)
    const remarkBefore2 = await getRowRemarkIndicatorText(page, 2)
    assert(remarkBefore1 === '', `Row 1 should not have remark indicator, got: ${remarkBefore1}`)
    assert(remarkBefore2 !== '', 'Row 2 should have remark indicator but is empty')

    await page.click('tbody.ant-table-tbody tr[data-row-key="1"] .ant-table-row-expand-icon')
    await page.waitForSelector('tr.ant-table-expanded-row', { timeout: 10000 })

    // Open remark panel (2nd panel: record + remark).
    await page.click('tr.ant-table-expanded-row .ant-collapse-item:nth-child(2) .ant-collapse-header')
    await page.waitForSelector('tr.ant-table-expanded-row .ant-collapse-item-active textarea', { timeout: 10000 })

    await page.type('tr.ant-table-expanded-row textarea', 'a')
    await sleep(300)

    // Regression: typing should NOT auto-collapse the remark panel.
    const remarkPanelActive = await page.$eval(
      'tr.ant-table-expanded-row .ant-collapse-item:nth-child(2)',
      (node) => node.classList.contains('ant-collapse-item-active')
    )
    assert(remarkPanelActive, 'Remark panel collapsed unexpectedly after typing')

    const textareaValue = await page.$eval('tr.ant-table-expanded-row textarea', (node) => node.value)
    assert(textareaValue === 'a', `Unexpected textarea value: ${JSON.stringify(textareaValue)}`)

    await page.click('tr.ant-table-expanded-row .ant-collapse-item:nth-child(2) button.ant-btn-primary')
    await sleep(800)

    const stateAfterSave = mockApi.getState()
    assert(
      stateAfterSave.lastRemarkUpdate?.id === 1 && stateAfterSave.lastRemarkUpdate?.remark === 'a',
      `Unexpected mock remark update: ${JSON.stringify(stateAfterSave.lastRemarkUpdate)}`
    )

    const remarkAfter1 = await getRowRemarkIndicatorText(page, 1)
    assert(remarkAfter1 !== '', 'Row 1 should have remark indicator after saving remark')

    const dragHandleErrors = consoleErrors.filter((line) => line.includes('Unable to find drag handle'))
    assert(dragHandleErrors.length === 0, `Drag handle error detected: ${dragHandleErrors.join(' | ')}`)

    console.log(
      JSON.stringify(
        {
          success: true,
          remarkBefore1,
          remarkBefore2,
          remarkAfter1,
          lastRemarkUpdate: stateAfterSave.lastRemarkUpdate,
        },
        null,
        2
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E remark regression failed.')
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
