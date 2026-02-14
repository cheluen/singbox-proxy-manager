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
    remark: '',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 3,
    name: 'node-3',
    type: 'direct',
    config: '{}',
    inbound_port: 30003,
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
        // keep searching
      }
    }
  }
  return undefined
}

const createMockApiServer = () => {
  let nodes = createMockNodes()
  let lastReorder = null

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJson(res, 200, { version: 'test-version' })
      return
    }

    if (req.method === 'GET' && req.url === '/api/nodes') {
      const sorted = [...nodes].sort((a, b) => a.sort_order - b.sort_order)
      sendJson(res, 200, sorted)
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
          nextNodes.push({
            ...existing,
            sort_order: row.sort_order,
          })
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
      sendJson(res, 200, { nodes, lastReorder })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ nodes, lastReorder })
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

const getRowNames = async (page) =>
  page.$$eval('tbody.ant-table-tbody tr[data-row-key] td:nth-child(4)', (cells) =>
    cells.map((cell) => cell.textContent?.trim() || '')
  )

const getCellCenter = async (page, selector) =>
  page.$eval(selector, (element) => {
    const rect = element.getBoundingClientRect()
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 }
  })

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const run = async () => {
  const scriptFile = fileURLToPath(import.meta.url)
  const scriptsDir = path.dirname(scriptFile)
  const frontendRoot = path.resolve(scriptsDir, '..')
  const mockApi = await startMockApi()
  // Ensure `localhost` can reach the mock API (CI runners often resolve localhost -> ::1).
  await waitForHttpReady(`http://localhost:${API_PORT}/api/version`, 10000)
  const vite = startVite(frontendRoot)
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

    const orderBefore = await getRowNames(page)
    await page.click('tbody.ant-table-tbody tr[data-row-key="1"] td:nth-child(3) input[type="checkbox"]')
    await sleep(600)
    const orderAfterCheckboxClick = await getRowNames(page)
    const stateAfterCheckboxClick = mockApi.getState()

    const dragFrom = await getCellCenter(page, 'tbody.ant-table-tbody tr[data-row-key="1"] td:nth-child(2)')
    const dragTo = await getCellCenter(page, 'tbody.ant-table-tbody tr[data-row-key="3"] td:nth-child(2)')
    await page.mouse.move(dragFrom.x, dragFrom.y)
    await page.mouse.down()
    await page.mouse.move(dragTo.x, dragTo.y, { steps: 18 })
    await sleep(200)
    await page.mouse.up()
    await sleep(1200)

    const orderAfterDrag = await getRowNames(page)
    const stateAfterDrag = mockApi.getState()
    const dragHandleErrors = consoleErrors.filter((line) => line.includes('Unable to find drag handle'))

    assert(
      JSON.stringify(orderBefore) === JSON.stringify(['node-1', 'node-2', 'node-3']),
      `Unexpected initial node order: ${JSON.stringify(orderBefore)}`
    )
    assert(
      JSON.stringify(orderAfterCheckboxClick) === JSON.stringify(orderBefore),
      'Checkbox click unexpectedly changed row order'
    )
    assert(!stateAfterCheckboxClick.lastReorder, 'Checkbox click should not trigger reorder request')
    assert(Boolean(stateAfterDrag.lastReorder), 'Drag should trigger reorder request')
    assert(
      JSON.stringify(orderAfterDrag) === JSON.stringify(['node-2', 'node-3', 'node-1']),
      `Unexpected row order after drag: ${JSON.stringify(orderAfterDrag)}`
    )
    assert(dragHandleErrors.length === 0, `Drag handle error detected: ${dragHandleErrors.join(' | ')}`)

    console.log(
      JSON.stringify(
        {
          success: true,
          orderBefore,
          orderAfterCheckboxClick,
          orderAfterDrag,
          reorderPayload: stateAfterDrag.lastReorder,
        },
        null,
        2
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E drag reorder regression failed.')
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
