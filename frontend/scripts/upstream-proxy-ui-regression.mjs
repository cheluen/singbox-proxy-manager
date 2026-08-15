import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30027)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5190)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const FRONTEND_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const FRONTEND_VERSION = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
).version
const ARTIFACT_DIR = process.env.E2E_ARTIFACT_DIR || ''

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const sendJSON = (response, status, payload) => {
  const body = JSON.stringify(payload)
  response.writeHead(status, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
  })
  response.end(body)
}

const readJSON = (request) =>
  new Promise((resolve, reject) => {
    let body = ''
    request.on('data', (chunk) => {
      body += chunk
    })
    request.on('end', () => {
      try {
        resolve(JSON.parse(body || '{}'))
      } catch (error) {
        reject(error)
      }
    })
    request.on('error', reject)
  })

const initialNode = {
  id: 1,
  name: 'upstream-policy-node',
  remark: '',
  type: 'direct',
  config: '{}',
  inbound_port: 30001,
  inbound_port_pinned: false,
  username: '',
  password: '',
  tcp_reuse_enabled: true,
  upstream_mode: 'global',
  upstream_type: '',
  upstream_config: '',
  upstream_ip: '',
  upstream_location: '',
  upstream_country_code: '',
  upstream_latency: 0,
  upstream_error: '',
  sort_order: 0,
  node_ip: '',
  location: '',
  country_code: '',
  latency: 0,
  enabled: true,
  created_at: '2026-08-14T00:00:00Z',
  updated_at: '2026-08-14T00:00:00Z',
}

const createMockAPI = () => {
  let settings = {
    start_port: 30001,
    preserve_inbound_ports: true,
    global_upstream_enabled: false,
    global_upstream_type: '',
    global_upstream_config: '',
    global_upstream_ip: '',
    global_upstream_location: '',
    global_upstream_country_code: '',
    global_upstream_latency: 0,
    global_upstream_error: '',
    admin_password_locked: false,
  }
  let nodes = [{ ...initialNode }]
  const settingUpdates = []
  const nodeUpdates = []
  const nodeUpstreamUpdates = []
  const parsedLinks = []
  let globalUpstreamChecks = 0
  let nodeIPChecks = 0

  const server = http.createServer(async (request, response) => {
    try {
      if (request.method === 'GET' && request.url === '/api/version') {
        sendJSON(response, 200, { version: FRONTEND_VERSION, update: { available: false } })
        return
      }
      if (request.method === 'GET' && request.url === '/api/settings') {
        sendJSON(response, 200, settings)
        return
      }
      if (request.method === 'PUT' && request.url === '/api/settings') {
        const payload = await readJSON(request)
        settingUpdates.push(payload)
        settings = { ...settings, ...payload }
        sendJSON(response, 200, { changed: true, password_changed: false })
        return
      }
      if (request.method === 'POST' && request.url === '/api/settings/upstream/check-ip') {
        globalUpstreamChecks += 1
        const result = {
          ip: '203.0.113.30',
          location: 'Tokyo, Japan',
          country_code: 'JP',
          latency: 31,
        }
        settings = {
          ...settings,
          global_upstream_ip: result.ip,
          global_upstream_location: result.location,
          global_upstream_country_code: result.country_code,
          global_upstream_latency: result.latency,
          global_upstream_error: '',
        }
        sendJSON(response, 200, result)
        return
      }
      if (request.method === 'POST' && request.url === '/api/parse-link') {
        const payload = await readJSON(request)
        parsedLinks.push(payload.link)
        if (String(payload.link || '').startsWith('socks5://')) {
          sendJSON(response, 200, {
            type: 'socks5',
            config: JSON.stringify({
              server: '192.0.2.10',
              server_port: 1080,
              username: 'global-user',
              password: 'global-pass',
            }),
          })
          return
        }
        if (String(payload.link || '').startsWith('http://')) {
          sendJSON(response, 200, {
            type: 'http',
            config: JSON.stringify({
              server: '192.0.2.11',
              server_port: 8080,
              username: 'node-user',
              password: 'node-pass',
            }),
          })
          return
        }
        sendJSON(response, 400, { error: 'unsupported test link' })
        return
      }
      if (request.method === 'GET' && request.url === '/api/nodes') {
        sendJSON(response, 200, nodes)
        return
      }
      if (request.method === 'PUT' && request.url === '/api/nodes/1') {
        const payload = await readJSON(request)
        nodeUpdates.push(payload)
        nodes = nodes.map((node) =>
          node.id === 1 ? { ...node, ...payload, id: 1 } : node
        )
        sendJSON(response, 200, nodes[0])
        return
      }
      if (request.method === 'PUT' && request.url === '/api/nodes/1/upstream') {
        const payload = await readJSON(request)
        nodeUpstreamUpdates.push(payload)
        nodes = nodes.map((node) =>
          node.id === 1
            ? {
                ...node,
                ...payload,
                upstream_ip: '',
                upstream_location: '',
                upstream_country_code: '',
                upstream_latency: 0,
                upstream_error: '',
                updated_at: new Date().toISOString(),
              }
            : node
        )
        sendJSON(response, 200, nodes[0])
        return
      }
      if (request.method === 'POST' && request.url === '/api/nodes/1/check-ip') {
        nodeIPChecks += 1
        const result = {
          ip: '203.0.113.20',
          location: 'Final City, Final Country',
          country_code: 'FC',
          latency: 74,
          upstream: {
            ip: '198.51.100.11',
            location: 'Upstream City, Upstream Country',
            country_code: 'UC',
            latency: 28,
          },
        }
        nodes = nodes.map((node) =>
          node.id === 1
            ? {
                ...node,
                node_ip: result.ip,
                location: result.location,
                country_code: result.country_code,
                latency: result.latency,
                upstream_ip: result.upstream.ip,
                upstream_location: result.upstream.location,
                upstream_country_code: result.upstream.country_code,
                upstream_latency: result.upstream.latency,
                upstream_error: '',
              }
            : node
        )
        sendJSON(response, 200, result)
        return
      }
      if (request.method === 'GET' && request.url === '/api/runtime/status') {
        sendJSON(response, 200, { degraded: false, running: true })
        return
      }
      if (request.method === 'POST' && request.url === '/api/logout') {
        sendJSON(response, 200, { message: 'logged out' })
        return
      }
      if (request.method === 'GET' && request.url === '/favicon.ico') {
        response.writeHead(204)
        response.end()
        return
      }
      sendJSON(response, 404, {
        error: 'not found',
        method: request.method,
        url: request.url,
      })
    } catch (error) {
      sendJSON(response, 500, { error: error.message })
    }
  })

  return {
    server,
    state: () => ({
      settings,
      nodes,
      settingUpdates,
      nodeUpdates,
      nodeUpstreamUpdates,
      parsedLinks,
      globalUpstreamChecks,
      nodeIPChecks,
    }),
  }
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
      // Continue looking for an installed browser.
    }
  }
  return undefined
}

const waitForHTTP = async (url, timeoutMilliseconds) => {
  const deadline = Date.now() + timeoutMilliseconds
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.status < 500) return
    } catch {
      // Retry until Vite is ready.
    }
    await sleep(200)
  }
  throw new Error(`Timed out waiting for ${url}`)
}

const startVite = () => {
  const viteBinary = path.join(FRONTEND_ROOT, 'node_modules', 'vite', 'bin', 'vite.js')
  const logs = []
  const child = spawn(
    process.execPath,
    [viteBinary, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)],
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

const stopProcess = async (child) => {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    sleep(5000).then(() => {
      if (child.exitCode === null) child.kill('SIGKILL')
    }),
  ])
}

const clickButton = async (page, text, timeoutMilliseconds = 10000) => {
  const deadline = Date.now() + timeoutMilliseconds
  while (Date.now() < deadline) {
    const clicked = await page.evaluate((label) => {
      const button = Array.from(document.querySelectorAll('button')).find((candidate) => {
        const rectangle = candidate.getBoundingClientRect()
        return (
          rectangle.width > 0 &&
          rectangle.height > 0 &&
          !candidate.disabled &&
          (candidate.textContent || '').trim() === label
        )
      })
      if (!button) return false
      button.click()
      return true
    }, text)
    if (clicked) return
    await sleep(100)
  }
  throw new Error(`Visible button not found: ${text}`)
}

const clickSelector = async (page, selector, timeoutMilliseconds = 10000) => {
  const deadline = Date.now() + timeoutMilliseconds
  while (Date.now() < deadline) {
    const clicked = await page.evaluate((target) => {
      const element = document.querySelector(target)
      if (!element) return false
      const rectangle = element.getBoundingClientRect()
      if (rectangle.width <= 0 || rectangle.height <= 0) return false
      element.click()
      return true
    }, selector)
    if (clicked) return
    await sleep(100)
  }
  throw new Error(`Visible selector not found: ${selector}`)
}

const clickSettings = async (page) => {
  const clicked = await page.evaluate(() => {
    const button = document.querySelector('.anticon-setting')?.closest('button')
    if (!button) return false
    button.click()
    return true
  })
  assert(clicked, 'Settings button was not found')
  await page.waitForSelector('[data-testid="settings-form"]', { visible: true, timeout: 10000 })
}

const openNodeUpstream = async (page) => {
  await page.click('[data-testid="node-upstream-action-1"]')
  await page.waitForSelector('[data-testid="node-upstream-form"]', {
    visible: true,
    timeout: 10000,
  })
}

const setSwitch = async (page, selector, enabled) => {
  await page.$eval(
    selector,
    (element, nextEnabled) => {
      if (element.getAttribute('aria-checked') !== String(nextEnabled)) element.click()
    },
    enabled
  )
  await page.waitForFunction(
    (target, nextEnabled) =>
      document.querySelector(target)?.getAttribute('aria-checked') === String(nextEnabled),
    { timeout: 5000 },
    selector,
    enabled
  )
}

const setInput = async (page, selector, value) => {
  await page.waitForSelector(selector, { visible: true, timeout: 10000 })
  await page.$eval(
    selector,
    (input, nextValue) => {
      const prototype =
        input instanceof window.HTMLTextAreaElement
          ? window.HTMLTextAreaElement.prototype
          : window.HTMLInputElement.prototype
      const setter = Object.getOwnPropertyDescriptor(prototype, 'value')?.set
      setter?.call(input, String(nextValue))
      input.dispatchEvent(new Event('input', { bubbles: true }))
      input.dispatchEvent(new Event('change', { bubbles: true }))
    },
    value
  )
}

const clickSegment = async (page, label) => {
  const clicked = await page.$eval(
    '[data-testid="node-upstream-mode"]',
    (root, targetLabel) => {
      const item = Array.from(root.querySelectorAll('.ant-segmented-item')).find(
        (candidate) => (candidate.textContent || '').trim() === targetLabel
      )
      if (!item) return false
      item.click()
      return true
    },
    label
  )
  assert(clicked, `Upstream mode not found: ${label}`)
  await page.waitForFunction(
    (targetLabel) =>
      Array.from(document.querySelectorAll('[data-testid="node-upstream-mode"] .ant-segmented-item-selected'))
        .some((item) => (item.textContent || '').trim() === targetLabel),
    { timeout: 5000 },
    label
  )
}

const openUpstreamEditor = async (page, triggerSelector) => {
  await page.waitForSelector(triggerSelector, { visible: true, timeout: 10000 })
  for (let attempt = 0; attempt < 3; attempt += 1) {
    await page.click(triggerSelector)
    try {
      await page.waitForSelector('[data-testid="upstream-editor"]', {
        visible: true,
        timeout: 3000,
      })
      return
    } catch {
      await sleep(200)
    }
  }
  throw new Error(`Upstream editor did not open from ${triggerSelector}`)
}

const saveUpstreamEditor = async (page) => {
  await page.$eval('[data-testid="upstream-editor"] button[type="submit"]', (button) => button.click())
  await page.waitForSelector('[data-testid="upstream-editor"]', { hidden: true, timeout: 10000 })
}

const parseUpstreamLink = async (page, link, expectedType) => {
  await setInput(page, '[data-testid="upstream-link-input"]', link)
  await page.click('[data-testid="upstream-link-parse"]')
  await page.waitForFunction(
    (type) => {
      const value = document.querySelector('[data-testid="upstream-editor"] .ant-select-selection-item')
        ?.textContent || ''
      return value.toLowerCase().includes(type.toLowerCase())
    },
    { timeout: 10000 },
    expectedType
  )
}

const saveNodeUpstream = async (page) => {
  await page.$eval('[data-testid="node-upstream-form"] button[type="submit"]', (button) => button.click())
  await page.waitForSelector('[data-testid="node-upstream-form"]', {
    hidden: true,
    timeout: 10000,
  })
}

const waitForUpdates = async (state, key, count) => {
  const deadline = Date.now() + 10000
  while (state()[key].length < count && Date.now() < deadline) await sleep(100)
  assert(state()[key].length >= count, `Timed out waiting for ${key} update ${count}`)
}

const run = async () => {
  const mockAPI = createMockAPI()
  await new Promise((resolve, reject) => {
    mockAPI.server.once('error', reject)
    mockAPI.server.listen(API_PORT, '127.0.0.1', resolve)
  })
  const vite = startVite()
  let browser
  let page
  let stage = 'launch'

  try {
    await waitForHTTP(FRONTEND_URL, 60000)
    browser = await puppeteer.launch({
      headless: true,
      executablePath: browserExecutable(),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })
    page = await browser.newPage()
    await page.setViewport({ width: 1440, height: 1000 })
    await page.evaluateOnNewDocument(() => {
      localStorage.setItem('token', 'upstream-ui-regression-token')
      localStorage.setItem('language', 'en')
    })

    const consoleErrors = []
    page.on('console', (message) => {
      if (message.type() === 'error') consoleErrors.push(message.text())
    })
    page.on('pageerror', (error) => consoleErrors.push(`pageerror:${error.message}`))

    stage = 'dashboard'
    await page.goto(FRONTEND_URL, { waitUntil: 'networkidle2' })
    await page.waitForSelector('tr[data-row-key="1"]', { visible: true, timeout: 30000 })

    const initialUpstreamButtonClass = await page.$eval(
      '[data-testid="node-upstream-action-1"]',
      (button) => button.className
    )
    assert(
      !initialUpstreamButtonClass.includes('ant-btn-primary'),
      `Non-custom upstream button should be gray: ${initialUpstreamButtonClass}`
    )

    stage = 'settings'
    await clickSettings(page)
    await openUpstreamEditor(page, '[data-testid="global-upstream-configure"]')
    stage = 'parse global upstream link'
    await parseUpstreamLink(
      page,
      'socks5://global-user:global-pass@192.0.2.10:1080#global',
      'SOCKS5'
    )
    stage = 'edit parsed global proxy'
    await setInput(page, '#global_upstream_server', '198.51.100.10')
    stage = 'save global proxy editor'
    await saveUpstreamEditor(page)
    await setSwitch(page, '[data-testid="global-upstream-enabled"]', true)
    const checkDisabledBeforeSave = await page.$eval(
      '[data-testid="global-upstream-check-ip"]',
      (button) => button.disabled
    )
    assert(checkDisabledBeforeSave, 'Unsaved global upstream was allowed to run an IP check')
    if (ARTIFACT_DIR) {
      await sleep(3500)
      fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
      await page.screenshot({ path: path.join(ARTIFACT_DIR, 'upstream-settings-desktop.png'), fullPage: true })
    }
    stage = 'save settings'
    await clickButton(page, 'Save Settings')
    await waitForUpdates(mockAPI.state, 'settingUpdates', 1)

    const settingPayload = mockAPI.state().settingUpdates[0]
    assert(settingPayload.global_upstream_enabled === true, `Global enable flag missing: ${JSON.stringify(settingPayload)}`)
    assert(settingPayload.global_upstream_type === 'socks5', `Global type mismatch: ${JSON.stringify(settingPayload)}`)
    assert(JSON.parse(settingPayload.global_upstream_config).server === '198.51.100.10', 'Global config was not serialized')

    stage = 'check saved global upstream'
    await page.waitForSelector('[data-testid="settings-form"]', { hidden: true, timeout: 10000 })
    await clickSettings(page)
    const checkDisabledAfterSave = await page.$eval(
      '[data-testid="global-upstream-check-ip"]',
      (button) => button.disabled
    )
    assert(!checkDisabledAfterSave, 'Saved global upstream IP check is disabled')
    await setInput(page, '#admin_password', '\u{1F600}'.repeat(4))
    await clickButton(page, 'Save Settings')
    await page.waitForFunction(
      () => Array.from(document.querySelectorAll('.ant-form-item-explain-error'))
        .some((element) => (element.textContent || '').includes('at least 8 characters')),
      { timeout: 5000 }
    )
    assert(mockAPI.state().settingUpdates.length === 1, 'Short Unicode password reached the API')
    await setInput(page, '#admin_password', '\u{1F600}'.repeat(19))
    await clickButton(page, 'Save Settings')
    await page.waitForFunction(
      () => Array.from(document.querySelectorAll('.ant-form-item-explain-error'))
        .some((element) => (element.textContent || '').includes('72 bytes')),
      { timeout: 5000 }
    )
    assert(mockAPI.state().settingUpdates.length === 1, 'Oversized Unicode password reached the API')
    await setInput(page, '#admin_password', '')
    await setInput(page, '#start_port', 31001)
    await page.click('[data-testid="global-upstream-check-ip"]')
    await page.waitForSelector('[data-testid="global-upstream-check-result"]', {
      visible: true,
      timeout: 10000,
    })
    const globalCheckText = await page.$eval(
      '[data-testid="global-upstream-check-result"]',
      (element) => element.textContent || ''
    )
    assert(globalCheckText.includes('203.0.113.30'), `Global check IP missing: ${globalCheckText}`)
    assert(globalCheckText.includes('Tokyo, Japan'), `Global check location missing: ${globalCheckText}`)
    assert(globalCheckText.includes('31ms'), `Global check latency missing: ${globalCheckText}`)
    const startPortAfterCheck = await page.$eval('#start_port', (input) => input.value)
    assert(startPortAfterCheck === '31001', `Global check reset unsaved settings: ${startPortAfterCheck}`)
    await setInput(page, '#start_port', 30001)
    await clickButton(page, 'Save Settings')
    await page.waitForSelector('[data-testid="settings-form"]', { hidden: true, timeout: 10000 })

    stage = 'open dedicated node upstream editor'
    await openNodeUpstream(page)
    const mainNodeFormVisible = await page.$('[data-testid="node-form"]')
    assert(!mainNodeFormVisible, 'Main node form opened with the dedicated upstream action')
    const defaultMode = await page.$eval(
      '[data-testid="node-upstream-mode"] .ant-segmented-item-selected',
      (element) => (element.textContent || '').trim()
    )
    assert(defaultMode === 'Follow Global', `Existing global mode was not selected: ${defaultMode}`)
    stage = 'select custom mode'
    await clickSegment(page, 'Custom')
    await openUpstreamEditor(page, '[data-testid="node-upstream-configure"]')
    stage = 'parse custom upstream link'
    await parseUpstreamLink(
      page,
      'http://node-user:node-pass@192.0.2.11:8080#node',
      'HTTP Proxy'
    )
    stage = 'edit parsed custom proxy'
    await setInput(page, '#node_upstream_server', '198.51.100.11')
    await setInput(page, '#node_upstream_server_port', 8081)
    stage = 'save custom proxy editor'
    await saveUpstreamEditor(page)

    await page.setViewport({ width: 390, height: 844, deviceScaleFactor: 1 })
    const mobileLayout = await page.evaluate(() => {
      const form = document.querySelector('[data-testid="node-upstream-form"]')
      const rectangle = form?.getBoundingClientRect()
      return {
        bodyOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
        formLeft: rectangle?.left ?? -1,
        formRight: rectangle?.right ?? -1,
        viewportWidth: window.innerWidth,
      }
    })
    assert(!mobileLayout.bodyOverflow, `Mobile editor overflows horizontally: ${JSON.stringify(mobileLayout)}`)
    assert(mobileLayout.formLeft >= 0 && mobileLayout.formRight <= mobileLayout.viewportWidth, `Mobile form is clipped: ${JSON.stringify(mobileLayout)}`)
    if (ARTIFACT_DIR) {
      await sleep(3500)
      await page.screenshot({ path: path.join(ARTIFACT_DIR, 'upstream-node-mobile.png'), fullPage: true })
    }
    await page.setViewport({ width: 1440, height: 1000 })

    stage = 'save custom node upstream'
    await saveNodeUpstream(page)
    await waitForUpdates(mockAPI.state, 'nodeUpstreamUpdates', 1)
    const customPayload = mockAPI.state().nodeUpstreamUpdates[0]
    assert(customPayload.upstream_mode === 'custom', `Custom mode missing: ${JSON.stringify(customPayload)}`)
    assert(customPayload.upstream_type === 'http', `Custom type mismatch: ${JSON.stringify(customPayload)}`)
    assert(JSON.parse(customPayload.upstream_config).server === '198.51.100.11', 'Custom config was not serialized')
    assert(JSON.parse(customPayload.upstream_config).server_port === 8081, 'Parsed custom config was not editable')

    const customUpstreamButtonClass = await page.$eval(
      '[data-testid="node-upstream-action-1"]',
      (button) => button.className
    )
    assert(
      customUpstreamButtonClass.includes('ant-btn-primary'),
      `Custom upstream button was not highlighted: ${customUpstreamButtonClass}`
    )

    stage = 'check final and custom upstream IPs'
    await clickSelector(page, '[data-testid="node-select-1"]')
    await clickButton(page, 'Batch Check IP')
    const checkDeadline = Date.now() + 10000
    while (mockAPI.state().nodeIPChecks < 1 && Date.now() < checkDeadline) await sleep(100)
    assert(mockAPI.state().nodeIPChecks === 1, 'Node IP check request was not sent')
    await clickSelector(page, 'tr[data-row-key="1"] .ant-table-row-expand-icon')
    await page.waitForSelector('.ant-table-expanded-row', { visible: true, timeout: 10000 })
    const upstreamPanelOpened = await page.evaluate(() => {
      const header = Array.from(
        document.querySelectorAll('.sbpm-node-record-collapse .ant-collapse-header')
      ).find((candidate) => (candidate.textContent || '').includes('Upstream Proxy'))
      if (!header) return false
      header.click()
      return true
    })
    assert(upstreamPanelOpened, 'Expanded upstream information panel was not found')
    await page.waitForFunction(
      () => {
        const row = document.querySelector('.ant-table-expanded-row')
        const text = row?.textContent || ''
        return text.includes('198.51.100.11') && text.includes('Upstream Country') && text.includes('28ms')
      },
      { timeout: 10000 }
    )

    await sleep(500)
    stage = 'open direct node upstream editor'
    await openNodeUpstream(page)
    await clickSegment(page, 'Direct')
    stage = 'save direct node upstream'
    await saveNodeUpstream(page)
    await waitForUpdates(mockAPI.state, 'nodeUpstreamUpdates', 2)
    const directPayload = mockAPI.state().nodeUpstreamUpdates[1]
    assert(directPayload.upstream_mode === 'none', `Direct bypass mode missing: ${JSON.stringify(directPayload)}`)
    assert(
      directPayload.upstream_type === 'http',
      `Inactive custom upstream should remain available for later reuse: ${JSON.stringify(directPayload)}`
    )
    const directUpstreamButtonClass = await page.$eval(
      '[data-testid="node-upstream-action-1"]',
      (button) => button.className
    )
    assert(!directUpstreamButtonClass.includes('ant-btn-primary'), 'Direct upstream button stayed highlighted')

    await sleep(500)
    stage = 'open global node upstream editor'
    await openNodeUpstream(page)
    await clickSegment(page, 'Follow Global')
    stage = 'save global node upstream'
    await saveNodeUpstream(page)
    await waitForUpdates(mockAPI.state, 'nodeUpstreamUpdates', 3)
    const globalPayload = mockAPI.state().nodeUpstreamUpdates[2]
    assert(globalPayload.upstream_mode === 'global', `Global mode was not restored: ${JSON.stringify(globalPayload)}`)
    assert(mockAPI.state().nodeUpdates.length === 0, 'Dedicated upstream changes used the main node update endpoint')
    assert(mockAPI.state().parsedLinks.length === 2, 'Upstream links were not parsed through the shared endpoint')
    assert(mockAPI.state().globalUpstreamChecks === 1, 'Global upstream IP check count mismatch')

    const unexpectedConsoleErrors = consoleErrors.filter(
      (line) =>
        !line.includes('Support for defaultProps will be removed from memo components') &&
        !line.includes('Static function can not consume context')
    )
    assert(unexpectedConsoleErrors.length === 0, `Browser errors: ${unexpectedConsoleErrors.join('\n')}`)

    process.stdout.write(
      `${JSON.stringify({ success: true, settingUpdates: 1, nodeModes: ['custom', 'none', 'global'], parsedLinks: 2, globalUpstreamChecks: 1, nodeIPChecks: 1, mobileLayout })}\n`
    )
  } catch (error) {
    let browserState = ''
    try {
      browserState = JSON.stringify(await page?.evaluate(() => ({
        visibleForms: Array.from(document.querySelectorAll('[data-testid]'))
          .filter((element) => {
            const rectangle = element.getBoundingClientRect()
            return rectangle.width > 0 && rectangle.height > 0
          })
          .map((element) => element.getAttribute('data-testid')),
        errors: Array.from(document.querySelectorAll('.ant-form-item-explain-error'))
          .map((element) => (element.textContent || '').trim()),
      })))
      if (ARTIFACT_DIR && page) {
        fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
        await page.screenshot({ path: path.join(ARTIFACT_DIR, 'upstream-ui-failure.png'), fullPage: true })
      }
    } catch {
      // Preserve the original failure.
    }
    throw new Error(`stage=${stage}: ${error.message}\nBrowser state: ${browserState}\nVite logs:\n${vite.logs()}`)
  } finally {
    try {
      await browser?.close()
    } catch {
      // Ignore browser cleanup failures.
    }
    await stopProcess(vite.child)
    await new Promise((resolve) => mockAPI.server.close(resolve))
  }
}

run().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
