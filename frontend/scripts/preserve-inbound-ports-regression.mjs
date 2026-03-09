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
    inbound_port: 30005,
    username: 'u2',
    password: 'p2',
    sort_order: 1,
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
    id: 3,
    name: 'node-3',
    type: 'direct',
    config: '{}',
    inbound_port: 30009,
    username: 'u3',
    password: 'p3',
    sort_order: 2,
    node_ip: '',
    location: '',
    country_code: '',
    latency: 0,
    enabled: true,
    remark: '',
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
      }
    }
  }
  return undefined
}

const createMockApiServer = () => {
  let nodes = createMockNodes()
  let settings = {
    start_port: 30001,
    preserve_inbound_ports: false,
    admin_password_locked: false,
  }
  let lastReorder = null
  let lastSettingsUpdate = null

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

    if (req.method === 'GET' && req.url === '/api/settings') {
      sendJson(res, 200, settings)
      return
    }

    if (req.method === 'PUT' && req.url === '/api/settings') {
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        settings = {
          ...settings,
          ...payload,
        }
        lastSettingsUpdate = payload
        sendJson(res, 200, { message: 'settings updated' })
      })
      return
    }

    if (req.method === 'POST' && req.url === '/api/nodes/reorder') {
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        const rows = payload.nodes || []
        const nodesMap = new Map(nodes.map((item) => [item.id, item]))
        const nextNodes = []
        for (const row of rows) {
          const existing = nodesMap.get(row.id)
          if (!existing) continue
          const nextNode = {
            ...existing,
            sort_order: row.sort_order,
          }
          if (!settings.preserve_inbound_ports) {
            nextNode.inbound_port = settings.start_port + row.sort_order
          }
          nextNodes.push(nextNode)
        }
        if (nextNodes.length === nodes.length) {
          nodes = nextNodes
        }
        lastReorder = payload
        sendJson(res, 200, { message: 'nodes reordered' })
      })
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { nodes, settings, lastReorder, lastSettingsUpdate })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ nodes, settings, lastReorder, lastSettingsUpdate })
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

const getRowNames = async (page) =>
  page.$$eval('tbody.ant-table-tbody tr[data-row-key] td:nth-child(4)', (cells) =>
    cells.map((cell) => cell.textContent?.trim() || '')
  )

const getRowPorts = async (page) =>
  page.$$eval('tbody.ant-table-tbody tr[data-row-key] td:nth-child(7)', (cells) =>
    cells.map((cell) => Number(cell.textContent?.trim() || '0'))
  )

const getCellCenter = async (page, selector) =>
  page.$eval(selector, (element) => {
    const rect = element.getBoundingClientRect()
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 }
  })

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const isIgnorableConsoleError = (line) => {
  const patterns = [
    'Failed to load resource: the server responded with a status of 404',
    'Support for defaultProps will be removed from memo components',
    '[antd: Modal] `destroyOnClose` is deprecated',
    '[antd: message] Static function can not consume context',
  ]
  return patterns.some((pattern) => line.includes(pattern))
}

const clickSettingsButton = async (page) => {
  await page.$eval('span.anticon-setting', (element) => {
    const button = element.closest('button')
    if (!button) {
      throw new Error('settings button not found')
    }
    button.click()
  })
}

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
      localStorage.setItem('token', 'preserve-ports-token')
      localStorage.setItem('i18nextLng', 'en')
    })
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 30000 })

    const portsBefore = await getRowPorts(page)
    const orderBefore = await getRowNames(page)

    await clickSettingsButton(page)
    await page.waitForSelector('.ant-modal .ant-switch', { timeout: 10000 })
    const switchSelector = '.ant-modal .ant-switch'
    const wasChecked = await page.$eval(switchSelector, (element) => element.getAttribute('aria-checked') === 'true')
    if (wasChecked) {
      throw new Error('preserve switch should be disabled initially')
    }
    await page.click(switchSelector)
    await page.click('.ant-modal button[type="submit"]')
    await page.waitForFunction(() => {
      const wraps = Array.from(document.querySelectorAll('.ant-modal-wrap'))
      return !wraps.some((element) => getComputedStyle(element).display !== 'none')
    })
    await sleep(800)

    const stateAfterSettings = mockApi.getState()
    assert(
      stateAfterSettings.lastSettingsUpdate?.preserve_inbound_ports === true,
      `expected settings update payload to enable preserve mode, got ${JSON.stringify(stateAfterSettings.lastSettingsUpdate)}`
    )

    const dragFrom = await getCellCenter(page, 'tbody.ant-table-tbody tr[data-row-key="1"] td:nth-child(2)')
    const dragTo = await getCellCenter(page, 'tbody.ant-table-tbody tr[data-row-key="3"] td:nth-child(2)')
    await page.mouse.move(dragFrom.x, dragFrom.y)
    await page.mouse.down()
    await page.mouse.move(dragTo.x, dragTo.y, { steps: 18 })
    await sleep(200)
    await page.mouse.up()
    await sleep(1200)

    const orderAfterDrag = await getRowNames(page)
    const portsAfterDrag = await getRowPorts(page)
    const stateAfterDrag = mockApi.getState()

    assert(
      JSON.stringify(orderBefore) === JSON.stringify(['node-1', 'node-2', 'node-3']),
      `unexpected initial order: ${JSON.stringify(orderBefore)}`
    )
    assert(
      JSON.stringify(portsBefore) === JSON.stringify([30001, 30005, 30009]),
      `unexpected initial ports: ${JSON.stringify(portsBefore)}`
    )
    assert(Boolean(stateAfterDrag.lastReorder), 'drag should trigger reorder request')
    assert(
      JSON.stringify(orderAfterDrag) === JSON.stringify(['node-2', 'node-3', 'node-1']),
      `unexpected order after drag: ${JSON.stringify(orderAfterDrag)}`
    )
    assert(
      JSON.stringify(portsAfterDrag) === JSON.stringify([30005, 30009, 30001]),
      `expected ports to stay attached to nodes after drag, got ${JSON.stringify(portsAfterDrag)}`
    )
    const blockingConsoleErrors = consoleErrors.filter((line) => !isIgnorableConsoleError(line))
    assert(
      blockingConsoleErrors.length === 0,
      `unexpected console errors: ${blockingConsoleErrors.join(' | ')}`
    )

    console.log(
      JSON.stringify(
        {
          success: true,
          orderBefore,
          portsBefore,
          orderAfterDrag,
          portsAfterDrag,
          lastSettingsUpdate: stateAfterSettings.lastSettingsUpdate,
          reorderPayload: stateAfterDrag.lastReorder,
        },
        null,
        2,
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E preserve inbound ports regression failed.')
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
