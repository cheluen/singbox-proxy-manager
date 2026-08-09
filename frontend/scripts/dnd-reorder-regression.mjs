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
const ARTIFACT_DIR = String(process.env.E2E_ARTIFACT_DIR || '').trim()
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const SCRIPTS_DIR = path.dirname(SCRIPT_PATH)
const FRONTEND_ROOT = path.resolve(SCRIPTS_DIR, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const FRONTEND_BUILD_VERSION = FRONTEND_PACKAGE.version
const LONG_NODE_NAME = 'node-20-with-an-intentionally-long-display-name-for-ellipsis-regression'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

const createMockNodes = (count = 20) =>
  Array.from({ length: count }, (_, index) => {
    const id = index + 1
    return {
      id,
      name: id === 20 ? LONG_NODE_NAME : `node-${id}`,
      type: 'direct',
      config: '{}',
      inbound_port: 30000 + id,
      inbound_port_pinned: false,
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
  let reorderCount = 0
  const portPinUpdates = []

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

    if (req.method === 'GET' && req.url === '/api/runtime/status') {
      sendJson(res, 200, { degraded: false, running: true })
      return
    }

    if (req.method === 'GET' && req.url === '/api/settings') {
      sendJson(res, 200, {
        start_port: 30001,
        preserve_inbound_ports: false,
        admin_password_locked: false,
      })
      return
    }

    const portPinMatch = req.url?.match(/^\/api\/nodes\/(\d+)\/port-pin$/)
    if (req.method === 'PUT' && portPinMatch) {
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const id = Number(portPinMatch[1])
        const payload = JSON.parse(body || '{}')
        nodes = nodes.map((node) =>
          node.id === id
            ? { ...node, inbound_port_pinned: Boolean(payload.pinned) }
            : node
        )
        portPinUpdates.push({ id, pinned: Boolean(payload.pinned) })
        sendJson(res, 200, { message: 'port pin updated' })
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
          nextNodes.push({
            ...existing,
            sort_order: row.sort_order,
          })
        }
        if (nextNodes.length === nodes.length) {
          nodes = nextNodes
        }
        lastReorder = payload
        reorderCount += 1
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

  const getState = () => ({ nodes, lastReorder, reorderCount, portPinUpdates })
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
    env: {
      ...process.env,
      E2E_API_PORT: String(API_PORT),
      VITE_API_TARGET: `http://127.0.0.1:${API_PORT}`,
    },
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

const dragBetween = async (page, sourceSelector, destinationSelector) => {
  const source = await getCellCenter(page, sourceSelector)
  const destination = await getCellCenter(page, destinationSelector)
  await page.mouse.move(source.x, source.y)
  await page.mouse.down()
  await page.mouse.move(destination.x, destination.y, { steps: 18 })
  await sleep(200)
  await page.mouse.up()
  await sleep(1000)
}

const clickNodeCheckbox = async (page, rowKey, timeoutMs = 10000) => {
  const cellSelector = `tbody.ant-table-tbody tr[data-row-key="${rowKey}"] td:nth-child(3)`
  const clickableSelector = `${cellSelector} .ant-checkbox`
  const inputSelector = `${cellSelector} input[type="checkbox"]`

  await page.locator(clickableSelector).click({ timeout: timeoutMs })
  await page.waitForFunction(
    (sel) => Boolean(document.querySelector(sel)?.checked),
    { timeout: timeoutMs },
    inputSelector
  )
}

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const run = async () => {
  const mockApi = await startMockApi()
  // Ensure `localhost` can reach the mock API (CI runners often resolve localhost -> ::1).
  await waitForHttpReady(`http://localhost:${API_PORT}/api/version`, 10000)
  const vite = startVite(FRONTEND_ROOT)
  let browser
  let page
  const consoleErrors = []

  try {
    await waitForHttpReady(FRONTEND_URL, 60000)

    const executablePath = getBrowserExecutablePath()
    browser = await puppeteer.launch({
      headless: true,
      executablePath,
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })

    page = await browser.newPage()
    page.setDefaultNavigationTimeout(120000)
    await page.setViewport({ width: 1366, height: 900 })
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
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 120000 })
    await page.waitForSelector('[data-testid="node-drag-handle-1"]', { timeout: 120000 })

    const orderBefore = await getRowNames(page)
    const desktopColumnCount = await page.$$eval(
      'tbody.ant-table-tbody tr[data-row-key="1"] td',
      (cells) => cells.length
    )
    const desktopNameHeader = await page.evaluate(() => {
      const header = Array.from(document.querySelectorAll('thead.ant-table-thead th')).find(
        (cell) => cell.textContent?.trim() === '名称'
      )
      return {
        text: header?.textContent?.trim() || '',
        width: header?.getBoundingClientRect().width || 0,
      }
    })
    const longNamePresentation = await page.$eval(
      'tbody.ant-table-tbody tr[data-row-key="20"] td:nth-child(4) .node-name-ellipsis',
      (element) => ({
        text: element.textContent?.trim() || '',
        clientWidth: element.clientWidth,
        scrollWidth: element.scrollWidth,
        overflow: getComputedStyle(element).overflow,
        textOverflow: getComputedStyle(element).textOverflow,
      })
    )
    let longNameTooltipVisible = false
    for (let attempt = 0; attempt < 3 && !longNameTooltipVisible; attempt += 1) {
      await page.$eval(
        'tbody.ant-table-tbody tr[data-row-key="20"] td:nth-child(4) .node-name-ellipsis',
        (element) => {
          element.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
          element.dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }))
        }
      )
      longNameTooltipVisible = await page.waitForFunction(
        (expected) =>
          Array.from(document.querySelectorAll('.ant-tooltip-inner')).some(
            (element) => (element.textContent || '').trim() === expected
          ),
        { timeout: 5000 },
        LONG_NODE_NAME
      ).then(() => true).catch(() => false)
    }
    assert(longNameTooltipVisible, 'Long node name tooltip did not become visible')
    const longNameTooltip = await page.evaluate((expected) => {
      const tooltip = Array.from(document.querySelectorAll('.ant-tooltip-inner')).find(
        (element) => (element.textContent || '').trim() === expected
      )
      return tooltip?.textContent?.trim() || ''
    }, LONG_NODE_NAME)
    const dragHandleScope = await page.evaluate(() => {
      const row = document.querySelector('tbody.ant-table-tbody tr[data-row-key="1"]')
      const libraryHandles = Array.from(
        document.querySelectorAll('[data-rbd-drag-handle-draggable-id]')
      )
      return {
        libraryHandleCount: libraryHandles.length,
        allLibraryHandlesScoped: libraryHandles.every((handle) =>
          Boolean(handle.closest('[data-node-drag-id]'))
        ),
        rowIsLibraryHandle: Boolean(row?.hasAttribute('data-rbd-drag-handle-draggable-id')),
        rowCursor: row ? getComputedStyle(row).cursor : '',
      }
    })

    if (ARTIFACT_DIR) {
      fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
      await page.screenshot({ path: path.join(ARTIFACT_DIR, 'desktop-dashboard.png') })
    }

    const pinnedPort = mockApi.getState().nodes.find((node) => node.id === 1)?.inbound_port
    await page.locator('[data-testid="node-port-pin-1"]').click()
    await page.waitForFunction(
      () => document.querySelector('[data-testid="node-port-pin-1"]')?.getAttribute('aria-pressed') === 'true',
      { timeout: 5000 }
    )
    const stateAfterPin = mockApi.getState()

    await clickNodeCheckbox(page, 1)
    await sleep(600)
    const orderAfterCheckboxClick = await getRowNames(page)
    const stateAfterCheckboxClick = mockApi.getState()

    await dragBetween(
      page,
      'tbody.ant-table-tbody tr[data-row-key="1"] td:nth-child(4)',
      'tbody.ant-table-tbody tr[data-row-key="3"] td:nth-child(4)'
    )
    const orderAfterNonHandleDrag = await getRowNames(page)
    const stateAfterNonHandleDrag = mockApi.getState()

    await dragBetween(
      page,
      '[data-testid="node-drag-handle-1"]',
      '[data-testid="node-drag-handle-3"]'
    )

    const orderAfterDrag = await getRowNames(page)
    const stateAfterDrag = mockApi.getState()
    const dragHandleErrors = consoleErrors.filter((line) => line.includes('Unable to find drag handle'))

    const expectedBefore = Array.from(
      { length: 20 },
      (_, index) => index === 19 ? LONG_NODE_NAME : `node-${index + 1}`
    )
    const expectedAfter = [
      'node-2',
      'node-3',
      'node-1',
      ...Array.from(
        { length: 17 },
        (_, index) => index === 16 ? LONG_NODE_NAME : `node-${index + 4}`
      ),
    ]

    assert(
      JSON.stringify(orderBefore) === JSON.stringify(expectedBefore),
      `Unexpected initial node order: ${JSON.stringify(orderBefore)}`
    )
    assert(desktopColumnCount === 16, `Desktop columns changed unexpectedly: ${desktopColumnCount}`)
    assert(
      desktopNameHeader.text === '名称' && desktopNameHeader.width >= 72 && desktopNameHeader.width <= 105,
      `Desktop node-name header changed unexpectedly: ${JSON.stringify(desktopNameHeader)}`
    )
    assert(
      longNamePresentation.text === LONG_NODE_NAME &&
        longNamePresentation.scrollWidth > longNamePresentation.clientWidth &&
        longNamePresentation.overflow === 'hidden' &&
        longNamePresentation.textOverflow === 'ellipsis',
      `Long node name is not visually truncated: ${JSON.stringify(longNamePresentation)}`
    )
    assert(longNameTooltip === LONG_NODE_NAME, `Long node tooltip lost the full value: ${longNameTooltip}`)
    assert(
      stateAfterPin.portPinUpdates.length === 1 &&
        stateAfterPin.portPinUpdates[0]?.id === 1 &&
        stateAfterPin.portPinUpdates[0]?.pinned === true,
      `Pin click did not persist the pinned state: ${JSON.stringify(stateAfterPin.portPinUpdates)}`
    )
    assert(dragHandleScope.libraryHandleCount === 20, `Unexpected drag handle count: ${JSON.stringify(dragHandleScope)}`)
    assert(dragHandleScope.allLibraryHandlesScoped, `Library drag handles escape the six-dot controls: ${JSON.stringify(dragHandleScope)}`)
    assert(!dragHandleScope.rowIsLibraryHandle, `Table row is still a drag handle: ${JSON.stringify(dragHandleScope)}`)
    assert(!['grab', 'grabbing'].includes(dragHandleScope.rowCursor), `Table row still advertises dragging: ${JSON.stringify(dragHandleScope)}`)
    assert(
      JSON.stringify(orderAfterCheckboxClick) === JSON.stringify(orderBefore),
      'Checkbox click unexpectedly changed row order'
    )
    assert(!stateAfterCheckboxClick.lastReorder, 'Checkbox click should not trigger reorder request')
    assert(
      JSON.stringify(orderAfterNonHandleDrag) === JSON.stringify(orderBefore),
      'Dragging from the node name unexpectedly changed row order'
    )
    assert(stateAfterNonHandleDrag.reorderCount === 0, 'Non-handle drag should not trigger reorder request')
    assert(Boolean(stateAfterDrag.lastReorder), 'Drag should trigger reorder request')
    assert(stateAfterDrag.reorderCount === 1, `Handle drag should issue one reorder request: ${stateAfterDrag.reorderCount}`)
    assert(
      JSON.stringify(orderAfterDrag) === JSON.stringify(expectedAfter),
      `Unexpected row order after drag: ${JSON.stringify(orderAfterDrag)}`
    )
    const pinnedNodeAfterDrag = stateAfterDrag.nodes.find((node) => node.id === 1)
    assert(
      pinnedNodeAfterDrag?.inbound_port_pinned === true && pinnedNodeAfterDrag?.inbound_port === pinnedPort,
      `Pinned node port changed during reorder: ${JSON.stringify(pinnedNodeAfterDrag)}`
    )
    assert(dragHandleErrors.length === 0, `Drag handle error detected: ${dragHandleErrors.join(' | ')}`)

    await page.locator('[data-testid="node-port-pin-1"]').click()
    await page.waitForFunction(
      () => document.querySelector('[data-testid="node-port-pin-1"]')?.getAttribute('aria-pressed') === 'false',
      { timeout: 5000 }
    )
    const stateAfterUnpin = mockApi.getState()
    assert(
      stateAfterUnpin.reorderCount === 1 &&
        stateAfterUnpin.portPinUpdates.length === 2 &&
        stateAfterUnpin.portPinUpdates[1]?.pinned === false,
      `Unpin unexpectedly triggered reorder or was not persisted: ${JSON.stringify(stateAfterUnpin)}`
    )
    assert(
      stateAfterUnpin.nodes.find((node) => node.id === 1)?.inbound_port === pinnedPort,
      'Unpin unexpectedly changed the node port'
    )

    await page.setViewport({
      width: 390,
      height: 700,
      deviceScaleFactor: 1,
      isMobile: true,
      hasTouch: true,
    })
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 30000 })
    await page.waitForFunction(() => {
      const row = document.querySelector('tbody.ant-table-tbody tr[data-row-key="1"]')
      return row?.querySelectorAll('td').length === 5
    }, { timeout: 10000 })

    const mobileLayout = await page.evaluate(() => {
      const viewportWidth = window.innerWidth
      const isInsideViewport = (element) => {
        if (!element) return false
        const rect = element.getBoundingClientRect()
        return rect.left >= -1 && rect.right <= viewportWidth + 1
      }
      const headerActions = document.querySelector('[data-testid="dashboard-header-actions"]')
      const primaryToolbar = document.querySelector('[data-testid="dashboard-toolbar-primary"]')
      const filterControls = document.querySelector('.dashboard-filter-controls')
      const tableContainer = document.querySelector('[data-testid="nodes-table-container"]')
      const row = document.querySelector('tbody.ant-table-tbody tr[data-row-key="1"]')
      const handle = document.querySelector('[data-testid="node-drag-handle-1"]')
      const body = tableContainer?.querySelector('.ant-table-body')
      const filterChildren = Array.from(filterControls?.children || [])
      const nameHeader = Array.from(
        tableContainer?.querySelectorAll('thead.ant-table-thead th') || []
      ).find((cell) => cell.textContent?.trim() === '名称')

      return {
        viewportWidth,
        documentScrollWidth: document.documentElement.scrollWidth,
        headerActionsInside: isInsideViewport(headerActions),
        primaryToolbarInside: isInsideViewport(primaryToolbar),
        filterControlsInside: isInsideViewport(filterControls),
        filterChildrenInside: filterChildren.every(isInsideViewport),
        tableInside: isInsideViewport(tableContainer),
        primaryClientWidth: primaryToolbar?.clientWidth || 0,
        primaryScrollWidth: primaryToolbar?.scrollWidth || 0,
        visibleCellCount: row?.querySelectorAll('td').length || 0,
        rowCursor: row ? getComputedStyle(row).cursor : '',
        rowTouchAction: row ? getComputedStyle(row).touchAction : '',
        handleTouchAction: handle ? getComputedStyle(handle).touchAction : '',
        handleIsLibraryHandle: Boolean(
          handle?.hasAttribute('data-rbd-drag-handle-draggable-id')
        ),
        bodyClientHeight: body?.clientHeight || 0,
        bodyScrollHeight: body?.scrollHeight || 0,
        nameHeaderText: nameHeader?.textContent?.trim() || '',
        nameHeaderWidth: nameHeader?.getBoundingClientRect().width || 0,
      }
    })

    assert(
      mobileLayout.documentScrollWidth <= mobileLayout.viewportWidth + 1,
      `Mobile page has horizontal overflow: ${JSON.stringify(mobileLayout)}`
    )
    assert(mobileLayout.headerActionsInside, `Mobile header actions are clipped: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.primaryToolbarInside, `Mobile toolbar is clipped: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.filterControlsInside, `Mobile filter controls are clipped: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.filterChildrenInside, `Mobile filter child controls are clipped: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.tableInside, `Mobile node table is clipped: ${JSON.stringify(mobileLayout)}`)
    assert(
      mobileLayout.primaryScrollWidth <= mobileLayout.primaryClientWidth + 1,
      `Mobile toolbar overflows internally: ${JSON.stringify(mobileLayout)}`
    )
    assert(mobileLayout.visibleCellCount === 5, `Mobile table did not collapse optional columns: ${JSON.stringify(mobileLayout)}`)
    assert(!['grab', 'grabbing'].includes(mobileLayout.rowCursor), `Mobile row still captures drag: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.rowTouchAction !== 'none', `Mobile row blocks native scrolling: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.handleTouchAction === 'none', `Mobile drag handle does not own touch dragging: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.handleIsLibraryHandle, `Mobile six-dot control is not the library drag handle: ${JSON.stringify(mobileLayout)}`)
    assert(
      mobileLayout.nameHeaderText === '名称' && mobileLayout.nameHeaderWidth <= 116,
      `Chinese mobile node-name header is not compact: ${JSON.stringify(mobileLayout)}`
    )
    assert(
      mobileLayout.bodyScrollHeight > mobileLayout.bodyClientHeight,
      `Mobile node list is not vertically scrollable: ${JSON.stringify(mobileLayout)}`
    )

    if (ARTIFACT_DIR) {
      await page.screenshot({ path: path.join(ARTIFACT_DIR, 'mobile-dashboard.png') })
    }

    const mobileExpandSelector =
      'tbody.ant-table-tbody tr[data-row-key="1"] .ant-table-row-expand-icon'
    await page.$eval(mobileExpandSelector, (button) => button.click())
    await page.waitForSelector('.ant-table-expanded-row .sbpm-node-record-collapse', {
      timeout: 10000,
    })
    await page.$eval(
      '.ant-table-expanded-row .sbpm-node-record-collapse .ant-collapse-header',
      (button) => button.click()
    )
    await page.waitForSelector('[data-testid="node-expanded-enabled-1"]', { timeout: 10000 })
    await page.waitForSelector('[data-testid="node-expanded-tcp-reuse-1"]', { timeout: 10000 })
    const mobileExpandedControls = await page.evaluate(() => ({
      enabledVisible: Boolean(document.querySelector('[data-testid="node-expanded-enabled-1"]')),
      tcpReuseVisible: Boolean(document.querySelector('[data-testid="node-expanded-tcp-reuse-1"]')),
      descriptionColumns: document.querySelectorAll(
        '.ant-table-expanded-row .node-record-descriptions .ant-descriptions-row'
      ).length,
    }))
    assert(
      mobileExpandedControls.enabledVisible && mobileExpandedControls.tcpReuseVisible,
      `Mobile expansion lost node controls: ${JSON.stringify(mobileExpandedControls)}`
    )
    assert(
      mobileExpandedControls.descriptionColumns >= 10,
      `Mobile node details are incomplete: ${JSON.stringify(mobileExpandedControls)}`
    )
    if (ARTIFACT_DIR) {
      await page.screenshot({ path: path.join(ARTIFACT_DIR, 'mobile-node-details.png') })
    }
    await page.$eval(mobileExpandSelector, (button) => button.click())
    await page.waitForFunction(
      (selector) =>
        document
          .querySelector(selector)
          ?.classList.contains('ant-table-row-expand-icon-collapsed'),
      { timeout: 10000 },
      mobileExpandSelector
    )

    const swipeStart = await page.$eval(
      'tbody.ant-table-tbody tr[data-row-key="4"] td:nth-child(4)',
      (cell) => {
        const body = cell.closest('.ant-table-body')
        if (body) body.scrollTop = 0
        const cellRect = cell.getBoundingClientRect()
        const bodyRect = body?.getBoundingClientRect()
        return {
          x: cellRect.left + cellRect.width / 2,
          y: cellRect.top + cellRect.height / 2,
          endY: Math.max((bodyRect?.top || 0) + 16, cellRect.top + cellRect.height / 2 - 150),
        }
      }
    )
    const cdp = await page.createCDPSession()
    await cdp.send('Input.dispatchTouchEvent', {
      type: 'touchStart',
      touchPoints: [{ x: swipeStart.x, y: swipeStart.y }],
    })
    await sleep(220)
    for (let step = 1; step <= 6; step += 1) {
      const y = swipeStart.y + ((swipeStart.endY - swipeStart.y) * step) / 6
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x: swipeStart.x, y }],
      })
      await sleep(35)
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    await sleep(500)

    const mobileScrollTop = await page.$eval(
      '[data-testid="nodes-table-container"] .ant-table-body',
      (body) => body.scrollTop
    )
    const stateAfterMobileScroll = mockApi.getState()
    assert(mobileScrollTop > 20, `Long-press swipe outside the handle did not scroll: ${mobileScrollTop}`)
    assert(
      stateAfterMobileScroll.reorderCount === 1,
      `Mobile non-handle swipe triggered reorder: ${stateAfterMobileScroll.reorderCount}`
    )

    await page.evaluate(() => localStorage.setItem('language', 'en'))
    await page.setViewport({
      width: 320,
      height: 568,
      deviceScaleFactor: 1,
      isMobile: true,
      hasTouch: true,
    })
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForFunction(() => {
      const row = document.querySelector('tbody.ant-table-tbody tr[data-row-key="2"]')
      return row?.querySelectorAll('td').length === 5
    }, { timeout: 30000 })
    await page.$eval(
      'tbody.ant-table-tbody tr[data-row-key="2"] input[type="checkbox"]',
      (input) => input.click()
    )
    await page.waitForSelector('[data-testid="dashboard-toolbar-selection"]', {
      timeout: 10000,
    })
    const compactMobileLayout = await page.evaluate(() => {
      const viewportWidth = window.innerWidth
      const rectInside = (element) => {
        if (!element) return false
        const rect = element.getBoundingClientRect()
        return rect.left >= -1 && rect.right <= viewportWidth + 1
      }
      const headerActions = document.querySelector('[data-testid="dashboard-header-actions"]')
      const primaryToolbar = document.querySelector('[data-testid="dashboard-toolbar-primary"]')
      const selectionToolbar = document.querySelector('[data-testid="dashboard-toolbar-selection"]')
      const table = document.querySelector('[data-testid="nodes-table-container"]')
      const actionButtons = Array.from(
        selectionToolbar?.querySelectorAll('button') || []
      )
      const nameHeader = Array.from(
        table?.querySelectorAll('thead.ant-table-thead th') || []
      ).find((cell) => cell.textContent?.trim() === 'Name')
      return {
        viewportWidth,
        documentScrollWidth: document.documentElement.scrollWidth,
        language: localStorage.getItem('language'),
        headerActionsInside: rectInside(headerActions),
        primaryToolbarInside: rectInside(primaryToolbar),
        selectionToolbarInside: rectInside(selectionToolbar),
        selectionButtonsInside: actionButtons.every(rectInside),
        selectionClientWidth: selectionToolbar?.clientWidth || 0,
        selectionScrollWidth: selectionToolbar?.scrollWidth || 0,
        tableInside: rectInside(table),
        tableHeight: table?.getBoundingClientRect().height || 0,
        nameHeaderText: nameHeader?.textContent?.trim() || '',
        nameHeaderWidth: nameHeader?.getBoundingClientRect().width || 0,
      }
    })
    assert(
      compactMobileLayout.documentScrollWidth <= compactMobileLayout.viewportWidth + 1,
      `Compact English mobile page overflows: ${JSON.stringify(compactMobileLayout)}`
    )
    assert(compactMobileLayout.language === 'en', `Compact mobile language did not switch: ${JSON.stringify(compactMobileLayout)}`)
    assert(compactMobileLayout.headerActionsInside, `Compact mobile header is clipped: ${JSON.stringify(compactMobileLayout)}`)
    assert(compactMobileLayout.primaryToolbarInside, `Compact mobile primary toolbar is clipped: ${JSON.stringify(compactMobileLayout)}`)
    assert(compactMobileLayout.selectionToolbarInside, `Compact mobile selection toolbar is clipped: ${JSON.stringify(compactMobileLayout)}`)
    assert(compactMobileLayout.selectionButtonsInside, `Compact mobile selection buttons are clipped: ${JSON.stringify(compactMobileLayout)}`)
    assert(
      compactMobileLayout.selectionScrollWidth <= compactMobileLayout.selectionClientWidth + 1,
      `Compact mobile selection toolbar overflows: ${JSON.stringify(compactMobileLayout)}`
    )
    assert(compactMobileLayout.tableInside && compactMobileLayout.tableHeight > 80, `Compact mobile table is unusable: ${JSON.stringify(compactMobileLayout)}`)
    assert(
      compactMobileLayout.nameHeaderText === 'Name' && compactMobileLayout.nameHeaderWidth <= 100,
      `English compact-mobile node-name header is not compact: ${JSON.stringify(compactMobileLayout)}`
    )

    await page.evaluate(() => localStorage.removeItem('token'))
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('.login-card', { timeout: 30000 })
    const mobileLoginLayout = await page.evaluate(() => {
      const shell = document.querySelector('.login-shell')
      const card = document.querySelector('.login-card')
      const input = document.querySelector('.login-panel input')
      const cardRect = card?.getBoundingClientRect()
      const inputRect = input?.getBoundingClientRect()
      return {
        viewportWidth: window.innerWidth,
        documentScrollWidth: document.documentElement.scrollWidth,
        shellOverflowY: shell ? getComputedStyle(shell).overflowY : '',
        cardLeft: cardRect?.left || 0,
        cardRight: cardRect?.right || 0,
        inputLeft: inputRect?.left || 0,
        inputRight: inputRect?.right || 0,
      }
    })
    assert(
      mobileLoginLayout.documentScrollWidth <= mobileLoginLayout.viewportWidth + 1,
      `Mobile login has horizontal overflow: ${JSON.stringify(mobileLoginLayout)}`
    )
    assert(
      mobileLoginLayout.cardLeft >= -1 && mobileLoginLayout.cardRight <= mobileLoginLayout.viewportWidth + 1,
      `Mobile login card is clipped: ${JSON.stringify(mobileLoginLayout)}`
    )
    assert(
      mobileLoginLayout.inputLeft >= -1 && mobileLoginLayout.inputRight <= mobileLoginLayout.viewportWidth + 1,
      `Mobile login input is clipped: ${JSON.stringify(mobileLoginLayout)}`
    )
    assert(mobileLoginLayout.shellOverflowY === 'auto', `Mobile login cannot scroll: ${JSON.stringify(mobileLoginLayout)}`)

    if (ARTIFACT_DIR) {
      await page.screenshot({ path: path.join(ARTIFACT_DIR, 'mobile-login.png'), fullPage: true })
    }

    console.log(
      JSON.stringify(
        {
          success: true,
          orderBefore,
          orderAfterCheckboxClick,
          orderAfterNonHandleDrag,
          orderAfterDrag,
          reorderPayload: stateAfterDrag.lastReorder,
          dragHandleScope,
          desktopNameHeader,
          longNamePresentation,
          longNameTooltip,
          portPinUpdates: stateAfterUnpin.portPinUpdates,
          mobileLayout,
          mobileExpandedControls,
          mobileScrollTop,
          compactMobileLayout,
          mobileLoginLayout,
        },
        null,
        2
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E drag reorder regression failed.')
    console.error(error)
    if (consoleErrors.length > 0) {
      console.error('Browser errors:')
      console.error(consoleErrors.join('\n'))
    }
    if (page) {
      try {
        const browserState = await page.evaluate(() => ({
          url: window.location.href,
          token: localStorage.getItem('token'),
          bodyText: (document.body?.innerText || '').slice(0, 1000),
          tableRows: document.querySelectorAll('tbody.ant-table-tbody tr[data-row-key]').length,
          loginVisible: Boolean(document.querySelector('.login-card')),
        }))
        console.error(`Browser state: ${JSON.stringify(browserState)}`)
      } catch {
        // The page may already be gone after a navigation failure.
      }
    }
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
