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
    node_ip: '1.1.1.1',
    location: 'Test, CN',
    country_code: 'CN',
    latency: 80,
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
        // keep searching
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
  let lastBatchDeletePayload = null

  const reorderPortsIfNeeded = (list) =>
    list.map((node) => {
      if (settings.preserve_inbound_ports) {
        return node
      }
      return {
        ...node,
        inbound_port: settings.start_port + (node.sort_order || 0),
        updated_at: new Date().toISOString(),
      }
    })

  const normalizeOrder = () => {
    nodes = [...nodes]
      .sort((a, b) => a.sort_order - b.sort_order)
      .map((node, index) => ({
        ...node,
        sort_order: index,
        updated_at: new Date().toISOString(),
      }))
    nodes = reorderPortsIfNeeded(nodes)
  }

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
          normalizeOrder()
        }
        lastReorder = payload
        sendJson(res, 200, { message: 'nodes reordered' })
      })
      return
    }

    if (req.method === 'POST' && req.url === '/api/nodes/batch-delete') {
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        lastBatchDeletePayload = payload
        const ids = Array.isArray(payload.ids) ? payload.ids.map((id) => Number(id)) : []
        nodes = nodes.filter((node) => !ids.includes(node.id))
        normalizeOrder()
        sendJson(res, 200, { message: 'nodes deleted', deleted_count: ids.length })
      })
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { nodes, settings, lastReorder, lastBatchDeletePayload })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  return {
    server,
    getState: () => ({ nodes, settings, lastReorder, lastBatchDeletePayload }),
    setPreserveInboundPorts: (enabled) => {
      settings = {
        ...settings,
        preserve_inbound_ports: Boolean(enabled),
      }
    },
  }
}

const tryListen = (server, options) =>
  new Promise((resolve, reject) => {
    const onError = (error) => {
      server.off('listening', onListening)
      reject(error)
    }
    const onListening = () => {
      server.off('error', onError)
      resolve()
    }
    server.once('error', onError)
    server.once('listening', onListening)
    server.listen(options)
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

const getRect = async (page, selector) =>
  page.$eval(selector, (element) => {
    const rect = element.getBoundingClientRect()
    return {
      left: rect.left,
      top: rect.top,
      width: rect.width,
      height: rect.height,
    }
  })

const getVisibleRowKeys = async (page) =>
  page.$$eval('tbody.ant-table-tbody tr[data-row-key]', (rows) =>
    rows.map((row) => row.getAttribute('data-row-key') || '')
  )

const getRowNames = async (page) =>
  page.$$eval('tbody.ant-table-tbody tr[data-row-key] td:nth-child(4)', (cells) =>
    cells.map((cell) => cell.textContent?.trim() || '')
  )

const getCellCenter = async (page, selector) =>
  page.$eval(selector, (element) => {
    const rect = element.getBoundingClientRect()
    return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 }
  })

const selectAntdOptionByText = async (page, triggerSelector, optionText) => {
  await page.click(triggerSelector)
  await page.waitForSelector('.ant-select-dropdown:not(.ant-select-dropdown-hidden)', {
    timeout: 10000,
  })
  await page.evaluate((text) => {
    const dropdowns = Array.from(document.querySelectorAll('.ant-select-dropdown'))
      .filter((dropdown) => !dropdown.classList.contains('ant-select-dropdown-hidden'))
    const dropdown = dropdowns[dropdowns.length - 1]
    if (!dropdown) {
      throw new Error('select dropdown not found')
    }
    const options = Array.from(dropdown.querySelectorAll('.ant-select-item-option')).filter(
      (option) => !option.classList.contains('ant-select-item-option-disabled')
    )
    const match = options.find((option) => (option.textContent || '').includes(text))
    if (!match) {
      throw new Error(`option not found: ${text}`)
    }
    match.click()
  }, optionText)
}

const attemptDrag = async (page, fromRowKey, toRowKey) => {
  const dragFrom = await getCellCenter(
    page,
    `tbody.ant-table-tbody tr[data-row-key="${fromRowKey}"] td:nth-child(2)`
  )
  const dragTo = await getCellCenter(
    page,
    `tbody.ant-table-tbody tr[data-row-key="${toRowKey}"] td:nth-child(2)`
  )
  await page.mouse.move(dragFrom.x, dragFrom.y)
  await page.mouse.down()
  await page.mouse.move(dragTo.x, dragTo.y, { steps: 18 })
  await sleep(200)
  await page.mouse.up()
}

const waitFor = async (fn, timeoutMs) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const value = await fn()
    if (value) return value
    await sleep(200)
  }
  return null
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
    await page.setViewport({ width: 1440, height: 960 })
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
      localStorage.setItem('language', 'zh')
    })
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', {
      timeout: 30000,
    })

    // Filter to "unverified/invalid" nodes.
    await selectAntdOptionByText(
      page,
      '[data-testid="nodes-filter-value"] .ant-select-selector',
      '未检测/失效'
    )
    await page.click('[data-testid="nodes-filter-add"]')
    await page.waitForSelector('[data-testid="nodes-filter-tag-status"]', { timeout: 10000 })
    await sleep(600)

    const filteredRowKeys = await getVisibleRowKeys(page)
    assert(
      JSON.stringify(filteredRowKeys) === JSON.stringify(['1', '3']),
      `Unexpected filtered rows: ${JSON.stringify(filteredRowKeys)}`
    )

    // Drag should be disabled while filtering when preserve ports is off.
    await attemptDrag(page, '1', '3')
    await sleep(1000)
    const stateAfterDisabledDrag = mockApi.getState()
    assert(
      !stateAfterDisabledDrag.lastReorder,
      'Expected no reorder request when drag is disabled under filters'
    )

    // Enable preserve ports and reload so dashboard picks it up.
    mockApi.setPreserveInboundPorts(true)
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', {
      timeout: 30000,
    })

    await selectAntdOptionByText(
      page,
      '[data-testid="nodes-filter-value"] .ant-select-selector',
      '未检测/失效'
    )
    await page.click('[data-testid="nodes-filter-add"]')
    await page.waitForSelector('[data-testid="nodes-filter-tag-status"]', { timeout: 10000 })
    await sleep(600)

    await attemptDrag(page, '1', '3')
    const reorderState = await waitFor(() => mockApi.getState().lastReorder, 5000)
    assert(Boolean(reorderState), 'Expected reorder request when drag is enabled under filters')

    const stateAfterEnabledDrag = mockApi.getState()
    assert(
      Array.isArray(stateAfterEnabledDrag.lastReorder?.nodes) &&
        stateAfterEnabledDrag.lastReorder.nodes.length === 3,
      `Unexpected reorder payload length: ${stateAfterEnabledDrag.lastReorder?.nodes?.length}`
    )

    const filteredOrderAfterDrag = await getRowNames(page)
    assert(
      JSON.stringify(filteredOrderAfterDrag) === JSON.stringify(['node-3', 'node-1']),
      `Unexpected filtered order after drag: ${JSON.stringify(filteredOrderAfterDrag)}`
    )

    // Select-all should only affect filtered rows, and batch delete should only delete within scope.
    await page.click('[data-testid="nodes-select-all"]')
    await page.waitForSelector('[data-testid="nodes-batch-delete"]', { timeout: 10000 })
    await page.waitForSelector('[data-testid="dashboard-toolbar-selection"]', { timeout: 10000 })
    await sleep(300)

    const primaryToolbarRect = await getRect(page, '[data-testid="dashboard-toolbar-primary"]')
    const selectionToolbarRect = await getRect(page, '[data-testid="dashboard-toolbar-selection"]')
    const addButtonRect = await getRect(page, '[data-testid="nodes-add-node"]')
    const batchExportRect = await getRect(page, '[data-testid="nodes-batch-export"]')
    assert(
      selectionToolbarRect.top > primaryToolbarRect.top + 4,
      `Expected batch toolbar to render below primary toolbar, got primary=${JSON.stringify(primaryToolbarRect)} selection=${JSON.stringify(selectionToolbarRect)}`
    )
    assert(
      Math.abs(selectionToolbarRect.left - primaryToolbarRect.left) <= 1.5,
      `Expected batch toolbar to align left with primary toolbar, got primary=${JSON.stringify(primaryToolbarRect)} selection=${JSON.stringify(selectionToolbarRect)}`
    )
    assert(
      Math.abs(addButtonRect.left - batchExportRect.left) <= 1.5,
      `Expected batch export button to align with add button, got add=${JSON.stringify(addButtonRect)} export=${JSON.stringify(batchExportRect)}`
    )

    await page.click('[data-testid="nodes-batch-delete"]')
    await page.waitForSelector('.ant-popconfirm', { timeout: 10000 })
    await page.waitForSelector('.ant-popconfirm-buttons button.ant-btn-primary:not([disabled])', {
      timeout: 10000,
    })
    await page.evaluate(() => {
      const confirmButtons = Array.from(
        document.querySelectorAll('.ant-popconfirm-buttons button.ant-btn-primary:not([disabled])')
      ).filter((button) => {
        if (!(button instanceof HTMLElement)) {
          return false
        }
        return button.offsetParent !== null
      })
      const target = confirmButtons[confirmButtons.length - 1]
      if (!target) {
        throw new Error('popconfirm primary button not found')
      }
      target.click()
    })
    await sleep(1200)

    const stateAfterBatchDelete = mockApi.getState()
    const deletedIds = Array.isArray(stateAfterBatchDelete.lastBatchDeletePayload?.ids)
      ? stateAfterBatchDelete.lastBatchDeletePayload.ids.map((id) => Number(id)).sort((a, b) => a - b)
      : []
    assert(
      JSON.stringify(deletedIds) === JSON.stringify([1, 3]),
      `Unexpected batch delete ids: ${JSON.stringify(deletedIds)}`
    )

    await page.click('[data-testid="nodes-filter-clear"]')
    await sleep(800)
    const remainingRowKeys = await getVisibleRowKeys(page)
    assert(
      JSON.stringify(remainingRowKeys) === JSON.stringify(['2']),
      `Unexpected remaining rows after delete: ${JSON.stringify(remainingRowKeys)}`
    )

    const ignoredConsolePatterns = [
      'Support for defaultProps will be removed from memo components',
      '[antd: message] Static function can not consume context',
      'Failed to load resource: the server responded with a status of 404',
    ]
    const relevantConsoleErrors = consoleErrors.filter(
      (line) => !ignoredConsolePatterns.some((pattern) => line.includes(pattern))
    )
    assert(
      relevantConsoleErrors.length === 0,
      `Console errors: ${relevantConsoleErrors.join('\\n')}`
    )
  } catch (error) {
    const logs = vite?.getLogs?.() || ''
    throw new Error(`${error?.stack || error}\n\nVite logs:\n${logs}`)
  } finally {
    await stopProcess(vite?.child)
    try {
      await browser?.close()
    } catch {
      // ignore
    }
    try {
      mockApi.server.close()
    } catch {
      // ignore
    }
  }
}

run().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
