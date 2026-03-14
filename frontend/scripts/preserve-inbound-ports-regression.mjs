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
        const prevSettings = settings
        const payload = JSON.parse(body || '{}')
        settings = {
          ...settings,
          ...payload,
        }
        lastSettingsUpdate = payload

        const nextPreserve = Boolean(settings.preserve_inbound_ports)
        const prevPreserve = Boolean(prevSettings?.preserve_inbound_ports)
        const shouldReassignInboundPorts =
          !nextPreserve &&
          (payload.start_port !== undefined || prevPreserve !== nextPreserve)

        if (shouldReassignInboundPorts) {
          nodes = nodes.map((node) => ({
            ...node,
            inbound_port: settings.start_port + (node.sort_order || 0),
            updated_at: new Date().toISOString(),
          }))
        }

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
    '[antd: Modal] Static function can not consume context',
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

const waitForModalClosed = async (page) => {
  await page.waitForFunction(() => {
    const modals = Array.from(document.querySelectorAll('.ant-modal'))
    const isHidden = (element) => {
      if (!element) return true
      if (element.getAttribute?.('aria-hidden') === 'true') return true
      const style = getComputedStyle(element)
      if (style.display === 'none' || style.visibility === 'hidden') {
        return true
      }
      const opacity = Number(style.opacity || '1')
      if (!Number.isNaN(opacity) && opacity === 0) {
        return true
      }
      const rect = element.getBoundingClientRect()
      return rect.width === 0 || rect.height === 0
    }
    const isVisible = (modal) => {
      const wrap = modal.closest?.('.ant-modal-wrap') || null
      return !(isHidden(wrap) || isHidden(modal))
    }
    return !modals.some(isVisible)
  })
}

const openSettingsModal = async (page) => {
  await clickSettingsButton(page)
  await page.waitForSelector('.ant-modal input#start_port', { timeout: 10000 })
}

const closeSettingsModal = async (page) => {
  const clicked = await page.evaluate(() => {
    const isVisible = (element) => {
      const style = getComputedStyle(element)
      if (style.display === 'none' || style.visibility === 'hidden') {
        return false
      }
      const opacity = Number(style.opacity || '1')
      if (!Number.isNaN(opacity) && opacity === 0) {
        return false
      }
      const rect = element.getBoundingClientRect()
      return rect.width > 0 && rect.height > 0
    }

    const buttons = Array.from(document.querySelectorAll('.ant-modal-close'))
    const visibleButtons = buttons.filter(isVisible)
    const button = (visibleButtons.length > 0 ? visibleButtons[visibleButtons.length - 1] : buttons[buttons.length - 1]) || null
    if (!button) return false
    button.click()
    return true
  })
  if (!clicked) {
    throw new Error('modal close button not found')
  }
  await waitForModalClosed(page)
}

const openNodeEditModal = async (page, nodeId) => {
  const selector = `tbody.ant-table-tbody tr[data-row-key="${String(nodeId)}"] span.anticon-edit`
  await page.$eval(selector, (element) => {
    const button = element.closest('button')
    if (!button) {
      throw new Error('edit button not found')
    }
    button.click()
  })
  await page.waitForSelector('.ant-modal input[placeholder="0 (auto)"]', { timeout: 10000 })
}

const assertNodeInboundPortEditable = async (page, { expectedDisabled }) => {
  await page.waitForFunction(
    (expected) => {
      const input = document.querySelector('.ant-modal input[placeholder="0 (auto)"]')
      if (!input) return false
      return input.disabled === expected
    },
    { timeout: 15000 },
    expectedDisabled
  )
}

const confirmModalOk = async (page, { expectedIncludes }) => {
  await page.waitForSelector('.ant-modal-confirm', { timeout: 10000 })
  const confirmText = await page.$eval('.ant-modal-confirm', (element) => element.innerText || '')
  assert(
    confirmText.includes(expectedIncludes),
    `expected confirm modal to include ${JSON.stringify(expectedIncludes)}, got: ${JSON.stringify(confirmText)}`
  )
  await page.click('.ant-modal-confirm .ant-btn-primary')
  await page.waitForFunction(() => {
    const modal = document.querySelector('.ant-modal-confirm')
    if (!modal) return true

    const isHidden = (element) => {
      if (!element) return true
      if (element.getAttribute?.('aria-hidden') === 'true') return true
      const style = getComputedStyle(element)
      if (style.display === 'none' || style.visibility === 'hidden') {
        return true
      }
      const opacity = Number(style.opacity || '1')
      if (!Number.isNaN(opacity) && opacity === 0) {
        return true
      }
      const rect = element.getBoundingClientRect()
      return rect.width === 0 || rect.height === 0
    }

    const wrap = modal.closest?.('.ant-modal-wrap') || null
    return isHidden(wrap) || isHidden(modal)
  }, { timeout: 10000 })
}

const assertSettingsI18n = async (page, { include = [], exclude = [] }) => {
  const modalText = await page.$eval(
    'input#start_port',
    (element) => element.closest('.ant-modal')?.innerText || ''
  )
  for (const fragment of include) {
    assert(
      modalText.includes(fragment),
      `expected modal to include ${JSON.stringify(fragment)}, got: ${JSON.stringify(modalText)}`
    )
  }
  for (const fragment of exclude) {
    assert(
      !modalText.includes(fragment),
      `expected modal to exclude ${JSON.stringify(fragment)}, got: ${JSON.stringify(modalText)}`
    )
  }
}

const assertSettingsPasswordPlaceholder = async (page, { expected, unexpected }) => {
  const placeholder = await page.$eval(
    'input#admin_password',
    (element) => element.getAttribute('placeholder') || ''
  )
  assert(
    placeholder === expected,
    `expected password placeholder ${JSON.stringify(expected)}, got ${JSON.stringify(placeholder)}`
  )
  if (unexpected) {
    assert(
      placeholder !== unexpected,
      `expected password placeholder to not be ${JSON.stringify(unexpected)}, got ${JSON.stringify(placeholder)}`
    )
  }
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
    })

	    await page.evaluate(() => {
	      localStorage.setItem('language', 'zh')
	    })
	    await page.reload({ waitUntil: 'networkidle2' })
	    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 30000 })

	    await openNodeEditModal(page, 1)
	    await assertNodeInboundPortEditable(page, { expectedDisabled: true })
	    await closeSettingsModal(page)

	    await openSettingsModal(page)
	    await assertSettingsI18n(page, {
	      include: [
	        '系统设置',
        '起始端口',
        '入站连接使用的起始端口号',
        '保留入站端口',
        '开启后，拖拽排序',
        '新的管理员密码',
        '留空表示保持当前密码',
        '保存设置',
      ],
      exclude: [
        'Settings',
        'Start Port',
        'The starting port number for inbound connections.',
        'Preserve Inbound Ports',
        'When enabled, drag sorting',
        'New Admin Password',
        'Leave empty to keep current password',
        'Save Settings',
      ],
    })
    await assertSettingsPasswordPlaceholder(page, {
      expected: '输入新密码（可选）',
      unexpected: 'Enter new password (optional)',
    })
    await closeSettingsModal(page)

    await page.evaluate(() => {
      localStorage.setItem('language', 'en')
    })
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 30000 })

    const portsBefore = await getRowPorts(page)
    const orderBefore = await getRowNames(page)

    await openSettingsModal(page)
    await assertSettingsI18n(page, {
      include: [
        'Settings',
        'Start Port',
        'The starting port number for inbound connections.',
        'Preserve Inbound Ports',
        'When enabled, drag sorting',
        'New Admin Password',
        'Leave empty to keep current password',
        'Save Settings',
      ],
      exclude: [
        '系统设置',
        '起始端口',
        '入站连接使用的起始端口号',
        '保留入站端口',
        '开启后，拖拽排序',
        '新的管理员密码',
        '留空表示保持当前密码',
        '保存设置',
      ],
    })
    await assertSettingsPasswordPlaceholder(page, {
      expected: 'Enter new password (optional)',
      unexpected: '输入新密码（可选）',
    })

    const wasChecked = await page.$eval('input#start_port', (input) => {
      const modal = input.closest('.ant-modal')
      const element = modal?.querySelector?.('.ant-switch')
      return element?.getAttribute?.('aria-checked') === 'true'
    })
    if (wasChecked) {
      throw new Error('preserve switch should be disabled initially')
    }
	    await page.$eval('input#start_port', (input) => {
	      const modal = input.closest('.ant-modal')
	      const element = modal?.querySelector?.('.ant-switch')
	      if (!element) {
	        throw new Error('preserve switch not found')
	      }
	      element.click()
	    })
	    await page.$eval('input#start_port', (input) => {
	      const modal = input.closest('.ant-modal')
	      const button = modal?.querySelector?.('button[type="submit"]')
	      if (!button) {
	        throw new Error('settings submit button not found')
	      }
	      button.click()
	    })
	    await waitForModalClosed(page)
	    await sleep(800)

	    await openNodeEditModal(page, 1)
	    await assertNodeInboundPortEditable(page, { expectedDisabled: false })
	    await closeSettingsModal(page)

	    const stateAfterSettings = mockApi.getState()
	    assert(
	      stateAfterSettings.lastSettingsUpdate?.preserve_inbound_ports === true,
	      `expected settings update payload to enable preserve mode, got ${JSON.stringify(stateAfterSettings.lastSettingsUpdate)}`
	    )

    const dragFrom = await getCellCenter(page, 'tbody.ant-table-tbody tr[data-row-key="1"] td:nth-child(2)')
    const dragTo = await getCellCenter(page, 'tbody.ant-table-tbody tr[data-row-key="3"] td:nth-child(2)')
    for (let attempt = 1; attempt <= 3; attempt += 1) {
      await page.mouse.move(dragFrom.x, dragFrom.y)
      await page.mouse.down()
      await page.mouse.move(dragTo.x, dragTo.y, { steps: 18 })
      await sleep(200)
      await page.mouse.up()
      await sleep(1200)

      if (mockApi.getState().lastReorder) {
        break
      }
      await sleep(500)
    }

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

	    await openSettingsModal(page)
	    const preserveEnabled = await page.$eval('input#start_port', (input) => {
	      const modal = input.closest('.ant-modal')
	      const element = modal?.querySelector?.('.ant-switch')
	      return element?.getAttribute?.('aria-checked') === 'true'
	    })
	    assert(preserveEnabled, 'preserve switch should be enabled before disabling')
	    await page.$eval('input#start_port', (input) => {
	      const modal = input.closest('.ant-modal')
	      const element = modal?.querySelector?.('.ant-switch')
	      if (!element) {
	        throw new Error('preserve switch not found')
	      }
	      element.click()
	    })
	    await page.$eval('input#start_port', (input) => {
	      const modal = input.closest('.ant-modal')
	      const button = modal?.querySelector?.('button[type="submit"]')
	      if (!button) {
	        throw new Error('settings submit button not found')
	      }
	      button.click()
	    })
	    await confirmModalOk(page, { expectedIncludes: 'reassign inbound ports' })
	    await waitForModalClosed(page)
	    await sleep(800)

	    const stateAfterDisable = mockApi.getState()
	    assert(
	      stateAfterDisable.lastSettingsUpdate?.preserve_inbound_ports === false,
	      `expected settings update payload to disable preserve mode, got ${JSON.stringify(stateAfterDisable.lastSettingsUpdate)}`
	    )

	    const orderAfterDisable = await getRowNames(page)
	    const portsAfterDisable = await getRowPorts(page)
	    assert(
	      JSON.stringify(orderAfterDisable) === JSON.stringify(orderAfterDrag),
	      `expected order to stay the same after disabling preserve mode, got ${JSON.stringify(orderAfterDisable)}`
	    )
	    assert(
	      JSON.stringify(portsAfterDisable) === JSON.stringify([30001, 30002, 30003]),
	      `expected ports to be reassigned sequentially after disabling preserve mode, got ${JSON.stringify(portsAfterDisable)}`
	    )

	    await openNodeEditModal(page, 1)
	    await assertNodeInboundPortEditable(page, { expectedDisabled: true })
	    await closeSettingsModal(page)
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
	          orderAfterDisable,
	          portsAfterDisable,
	          lastSettingsUpdate: stateAfterSettings.lastSettingsUpdate,
	          lastSettingsDisableUpdate: stateAfterDisable.lastSettingsUpdate,
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
