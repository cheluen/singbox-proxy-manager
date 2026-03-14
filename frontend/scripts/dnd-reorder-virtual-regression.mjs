import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30020)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5183)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const SCRIPTS_DIR = path.dirname(SCRIPT_PATH)
const FRONTEND_ROOT = path.resolve(SCRIPTS_DIR, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const FRONTEND_BUILD_VERSION = FRONTEND_PACKAGE.version

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

const createMockNodes = (count = 400) =>
  Array.from({ length: count }).map((_, index) => {
    const id = index + 1
    return {
      id,
      name: `node-${id}`,
      type: 'direct',
      config: '{}',
      inbound_port: 30000 + id,
      username: `u${id}`,
      password: `p${id}`,
      sort_order: index,
      node_ip: '',
      location: '',
      country_code: '',
      latency: 0,
      enabled: true,
      remark: '',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    }
  })

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
      sendJson(res, 200, { version: FRONTEND_BUILD_VERSION })
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
  const child = spawn(
    process.execPath,
    [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)],
    {
      cwd: frontendRoot,
      env: { ...process.env, E2E_API_PORT: String(API_PORT) },
      stdio: ['ignore', 'pipe', 'pipe'],
    }
  )

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

const getVisibleRowNames = async (page) =>
  page.evaluate(() => {
    const tdCells = document.querySelectorAll(
      'tbody.ant-table-tbody tr[data-row-key] td:nth-child(4)'
    )
    if (tdCells.length > 0) {
      return Array.from(tdCells).map((cell) => cell.textContent?.trim() || '')
    }

    const rows = Array.from(document.querySelectorAll('.ant-table-row[data-row-key]'))
    return rows.map((row) => {
      const cells = row.querySelectorAll('.ant-table-cell')
      if (cells.length >= 4) {
        return (cells[3].textContent || '').trim()
      }
      return ''
    })
  })

const countVisibleRows = async (page) =>
  page.evaluate(() => {
    const trRows = document.querySelectorAll('tbody.ant-table-tbody tr[data-row-key]')
    if (trRows.length > 0) return trRows.length
    return document.querySelectorAll('.ant-table-row[data-row-key]').length
  })

const getExpandIconColumnMetrics = async (page) =>
  page.evaluate(() => {
    const root = document.querySelector('[data-testid="nodes-table-container"]')
    if (!root) return null

    const container =
      root.querySelector('.ant-table-container') ||
      root.querySelector('.ant-table-content') ||
      root
    const containerWidth = container.getBoundingClientRect().width

    const expandCell =
      root.querySelector('tbody.ant-table-tbody tr[data-row-key] td.ant-table-row-expand-icon-cell') ||
      root.querySelector('.ant-table-row[data-row-key] .ant-table-row-expand-icon-cell') ||
      root.querySelector('.ant-table-row-expand-icon-cell')
    if (!expandCell) {
      return {
        containerWidth,
        expandCellWidth: 0,
        ratio: 0,
      }
    }

    const expandCellWidth = expandCell.getBoundingClientRect().width
    const ratio = containerWidth > 0 ? expandCellWidth / containerWidth : 0
    return {
      containerWidth,
      expandCellWidth,
      ratio,
    }
  })

const getCenter = async (page, selector) =>
  page.$eval(selector, (element) => {
    const rect = element.getBoundingClientRect()
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 }
  })

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const isIgnorableConsoleError = (text) => {
  const line = String(text || '')
  if (!line) return true
  if (line.includes('Failed to load resource') && line.includes('404')) return true
  if (line.includes('ResizeObserver loop limit exceeded')) return true
  if (line.includes('ResizeObserver loop completed')) return true
  if (line.includes('Support for defaultProps will be removed from memo components')) return true
  if (line.includes('[antd: message] Static function can not consume context')) return true
  return false
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
      if (msg.type() === 'error' && !isIgnorableConsoleError(msg.text())) {
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
    await page.waitForSelector('[data-testid="node-drag-handle-1"]', { timeout: 30000 })

    const expandMetrics = await getExpandIconColumnMetrics(page)
    assert(expandMetrics, 'Failed to locate nodes table container for expand metrics')
    assert(
      expandMetrics.containerWidth > 0,
      `Invalid nodes table container width: ${expandMetrics.containerWidth}`
    )
    assert(
      expandMetrics.expandCellWidth > 0,
      `Expand icon column not found or has invalid width: ${expandMetrics.expandCellWidth}`
    )
    assert(
      expandMetrics.ratio < 0.2,
      `Expand icon column is too wide: ${JSON.stringify(expandMetrics)}`
    )

    const insideContainer = await page.evaluate(() => {
      const handle = document.querySelector('[data-testid="node-drag-handle-1"]')
      return Boolean(handle?.closest?.('[data-testid="nodes-table-container"]'))
    })
    assert(insideContainer, 'Drag handle is not inside nodes-table-container')
    const scrollMeta = await page.evaluate(() => {
      const root = document.querySelector('[data-testid="nodes-table-container"]')
      const body = root?.querySelector?.('.ant-table-body')
      const content = root?.querySelector?.('.ant-table-content')
      const container = root?.querySelector?.('.ant-table-container')
      return {
        hasBody: Boolean(body),
        hasContent: Boolean(content),
        hasContainer: Boolean(container),
        bodyTag: body?.tagName || '',
        contentTag: content?.tagName || '',
        containerTag: container?.tagName || '',
      }
    })

    const visibleBefore = await getVisibleRowNames(page)
    const visibleCount = await countVisibleRows(page)
    const stateBefore = mockApi.getState()

    await page.evaluate(() => {
      window.__sbpm_e2e_mouse = { down: 0, pointerDown: 0, lastPointer: null }
      document.addEventListener(
        'mousedown',
        () => {
          window.__sbpm_e2e_mouse.down += 1
        },
        true
      )
      document.addEventListener(
        'pointerdown',
        (event) => {
          window.__sbpm_e2e_mouse.pointerDown += 1
          const target = event?.target
          const handle = target?.closest?.('[data-node-drag-id]')
          window.__sbpm_e2e_mouse.lastPointer = {
            tag: target?.tagName || '',
            testid: (target?.closest?.('[data-testid]') || target)?.getAttribute?.('data-testid') || '',
            dragId: handle?.getAttribute?.('data-node-drag-id') || '',
          }
        },
        true
      )
    })

    const checkboxClicked = await page.evaluate(() => {
      const row =
        document.querySelector('tr[data-row-key="1"]') ||
        document.querySelector('.ant-table-row[data-row-key="1"]')
      if (!row) return false
      const checkbox = row.querySelector('input[type="checkbox"]')
      if (!checkbox) return false
      checkbox.click()
      return true
    })
    assert(checkboxClicked, 'Row checkbox not found/clickable')
    await sleep(600)
    const stateAfterCheckboxClick = mockApi.getState()

    const dragFrom = await getCenter(page, '[data-testid="node-drag-handle-1"]')
    const dragTo = await getCenter(page, '[data-testid="node-drag-handle-3"]')
    const hitFrom = await page.evaluate(({ x, y }) => {
      const target = document.elementFromPoint(x, y)
      const handle = target?.closest?.('[data-testid]') || target
      return handle?.getAttribute?.('data-testid') || ''
    }, dragFrom)
    const hitTo = await page.evaluate(({ x, y }) => {
      const target = document.elementFromPoint(x, y)
      const handle = target?.closest?.('[data-testid]') || target
      return handle?.getAttribute?.('data-testid') || ''
    }, dragTo)
    assert(
      hitFrom === 'node-drag-handle-1',
      `Hit test mismatch for dragFrom: ${hitFrom || '(empty)'}`
    )
    assert(
      hitTo === 'node-drag-handle-3',
      `Hit test mismatch for dragTo: ${hitTo || '(empty)'}`
    )

    await page.mouse.move(dragFrom.x, dragFrom.y)
    await page.mouse.down()
    await sleep(150)
    const mouseCounts = await page.evaluate(() => window.__sbpm_e2e_mouse)
    const dragActivated = await page.evaluate(() => ({
      hasActiveRow: Boolean(document.querySelector('.sbpm-virtual-drag-active')),
      bodyCursor: document.body.style.cursor || '',
    }))
    assert(
      dragActivated.hasActiveRow || dragActivated.bodyCursor === 'grabbing',
      `Drag did not activate (activeRow=${dragActivated.hasActiveRow}, cursor="${dragActivated.bodyCursor}", mousedown=${mouseCounts?.down}, pointerdown=${mouseCounts?.pointerDown}, lastPointer=${JSON.stringify(mouseCounts?.lastPointer)}, scrollMeta=${JSON.stringify(scrollMeta)})`
    )
    await page.mouse.move(dragTo.x, dragTo.y, { steps: 18 })
    await sleep(200)
    await page.mouse.up()
    await sleep(1200)

    const visibleAfterDrag = await getVisibleRowNames(page)
    const stateAfterDrag = mockApi.getState()

    assert(stateBefore.nodes.length === 400, `Expected 400 nodes but got ${stateBefore.nodes.length}`)
    assert(visibleCount > 0, 'Expected at least one visible row')
    assert(
      visibleCount < 200,
      `Virtual rendering expected (<200 visible rows), got ${visibleCount}`
    )

    assert(!stateAfterCheckboxClick.lastReorder, 'Checkbox click should not trigger reorder request')
    assert(Boolean(stateAfterDrag.lastReorder), 'Drag should trigger reorder request')
    assert(
      Array.isArray(stateAfterDrag.lastReorder?.nodes) &&
        stateAfterDrag.lastReorder.nodes.length === 400,
      `Unexpected reorder payload length: ${stateAfterDrag.lastReorder?.nodes?.length}`
    )

    assert(
      JSON.stringify(visibleBefore.slice(0, 3)) === JSON.stringify(['node-1', 'node-2', 'node-3']),
      `Unexpected initial visible order: ${JSON.stringify(visibleBefore.slice(0, 3))}`
    )
    assert(
      JSON.stringify(visibleAfterDrag.slice(0, 3)) === JSON.stringify(['node-2', 'node-3', 'node-1']),
      `Unexpected visible order after drag: ${JSON.stringify(visibleAfterDrag.slice(0, 3))}`
    )

    assert(
      consoleErrors.length === 0,
      `Console errors detected: ${consoleErrors.join(' | ')}`
    )

    console.log(
      JSON.stringify(
        {
          success: true,
          visibleCount,
          visibleBefore: visibleBefore.slice(0, 8),
          visibleAfterDrag: visibleAfterDrag.slice(0, 8),
          reorderPayloadLength: stateAfterDrag.lastReorder?.nodes?.length,
        },
        null,
        2
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E virtual table drag reorder regression failed.')
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
