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
    admin_password_locked: true,
  }
  let nodes = [{ ...initialNode }]
  const settingUpdates = []
  const nodeUpdates = []

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
    state: () => ({ settings, nodes, settingUpdates, nodeUpdates }),
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

const clickNodeEdit = async (page) => {
  const clicked = await page.evaluate(() => {
    const row = Array.from(document.querySelectorAll('tr[data-row-key]')).find((candidate) =>
      (candidate.textContent || '').includes('upstream-policy-node')
    )
    const button = row?.querySelector('.anticon-edit')?.closest('button')
    if (!button) return false
    button.click()
    return true
  })
  assert(clicked, 'Node edit button was not found')
  await page.waitForSelector('[data-testid="node-form"]', { visible: true, timeout: 10000 })
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
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value'
      )?.set
      setter?.call(input, String(nextValue))
      input.dispatchEvent(new Event('input', { bubbles: true }))
      input.dispatchEvent(new Event('change', { bubbles: true }))
    },
    value
  )
}

const selectOption = async (page, fieldID, optionText) => {
  const opened = await page.evaluate((id) => {
    const selector = document.getElementById(id)?.closest('.ant-select')?.querySelector('.ant-select-selector')
    if (!selector) return false
    const rectangle = selector.getBoundingClientRect()
    const eventInit = {
      bubbles: true,
      cancelable: true,
      clientX: rectangle.left + rectangle.width / 2,
      clientY: rectangle.top + rectangle.height / 2,
      buttons: 1,
    }
    selector.dispatchEvent(new PointerEvent('pointerdown', { ...eventInit, pointerType: 'mouse' }))
    selector.dispatchEvent(new MouseEvent('mousedown', eventInit))
    selector.dispatchEvent(new MouseEvent('mouseup', eventInit))
    selector.click()
    return true
  }, fieldID)
  assert(opened, `Select field not found: ${fieldID}`)
  await page.waitForFunction(
    (label) =>
      Array.from(document.querySelectorAll('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option'))
        .some((option) => (option.textContent || '').trim() === label),
    { timeout: 10000 },
    optionText
  )
  const selected = await page.evaluate((label) => {
    const option = Array.from(
      document.querySelectorAll('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option')
    ).find((candidate) => (candidate.textContent || '').trim() === label)
    if (!option) return false
    const rectangle = option.getBoundingClientRect()
    const eventInit = {
      bubbles: true,
      cancelable: true,
      clientX: rectangle.left + rectangle.width / 2,
      clientY: rectangle.top + rectangle.height / 2,
      buttons: 1,
    }
    option.dispatchEvent(new PointerEvent('pointerdown', { ...eventInit, pointerType: 'mouse' }))
    option.dispatchEvent(new MouseEvent('mousedown', eventInit))
    option.dispatchEvent(new MouseEvent('mouseup', eventInit))
    option.click()
    return true
  }, optionText)
  assert(selected, `Select option not found: ${optionText}`)
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

const saveNode = async (page) => {
  await page.$eval('[data-testid="node-form"] button[type="submit"]', (button) => button.click())
  await page.waitForSelector('[data-testid="node-form"]', { hidden: true, timeout: 10000 })
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

    stage = 'settings'
    await clickSettings(page)
    await openUpstreamEditor(page, '[data-testid="global-upstream-configure"]')
    stage = 'global proxy type'
    await selectOption(page, 'global_upstream_proxy_type', 'SOCKS5')
    stage = 'global proxy fields'
    await setInput(page, '#global_upstream_server', '198.51.100.10')
    await setInput(page, '#global_upstream_server_port', 1080)
    stage = 'save global proxy editor'
    await saveUpstreamEditor(page)
    await setSwitch(page, '[data-testid="global-upstream-enabled"]', true)
    if (ARTIFACT_DIR) {
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

    stage = 'open node editor'
    await clickNodeEdit(page)
    const defaultMode = await page.$eval(
      '[data-testid="node-upstream-mode"] .ant-segmented-item-selected',
      (element) => (element.textContent || '').trim()
    )
    assert(defaultMode === 'Follow Global', `Existing global mode was not selected: ${defaultMode}`)
    stage = 'select custom mode'
    await clickSegment(page, 'Custom')
    await openUpstreamEditor(page, '[data-testid="node-upstream-configure"]')
    stage = 'custom proxy type'
    await selectOption(page, 'node_upstream_proxy_type', 'HTTP Proxy')
    await setInput(page, '#node_upstream_server', '198.51.100.11')
    await setInput(page, '#node_upstream_server_port', 8080)
    stage = 'save custom proxy editor'
    await saveUpstreamEditor(page)

    await page.setViewport({ width: 390, height: 844, deviceScaleFactor: 1 })
    const mobileLayout = await page.evaluate(() => {
      const form = document.querySelector('[data-testid="node-form"]')
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
      await page.screenshot({ path: path.join(ARTIFACT_DIR, 'upstream-node-mobile.png'), fullPage: true })
    }
    await page.setViewport({ width: 1440, height: 1000 })

    stage = 'save custom node'
    await saveNode(page)
    await waitForUpdates(mockAPI.state, 'nodeUpdates', 1)
    const customPayload = mockAPI.state().nodeUpdates[0]
    assert(customPayload.upstream_mode === 'custom', `Custom mode missing: ${JSON.stringify(customPayload)}`)
    assert(customPayload.upstream_type === 'http', `Custom type mismatch: ${JSON.stringify(customPayload)}`)
    assert(JSON.parse(customPayload.upstream_config).server === '198.51.100.11', 'Custom config was not serialized')

    await sleep(500)
    stage = 'open direct node editor'
    await clickNodeEdit(page)
    await clickSegment(page, 'Direct')
    stage = 'save direct node'
    await saveNode(page)
    await waitForUpdates(mockAPI.state, 'nodeUpdates', 2)
    const directPayload = mockAPI.state().nodeUpdates[1]
    assert(directPayload.upstream_mode === 'none', `Direct bypass mode missing: ${JSON.stringify(directPayload)}`)
    assert(
      directPayload.upstream_type === 'http',
      `Inactive custom upstream should remain available for later reuse: ${JSON.stringify(directPayload)}`
    )

    await sleep(500)
    stage = 'open global node editor'
    await clickNodeEdit(page)
    await clickSegment(page, 'Follow Global')
    stage = 'save global node'
    await saveNode(page)
    await waitForUpdates(mockAPI.state, 'nodeUpdates', 3)
    const globalPayload = mockAPI.state().nodeUpdates[2]
    assert(globalPayload.upstream_mode === 'global', `Global mode was not restored: ${JSON.stringify(globalPayload)}`)

    const unexpectedConsoleErrors = consoleErrors.filter(
      (line) =>
        !line.includes('Support for defaultProps will be removed from memo components') &&
        !line.includes('Static function can not consume context')
    )
    assert(unexpectedConsoleErrors.length === 0, `Browser errors: ${unexpectedConsoleErrors.join('\n')}`)

    process.stdout.write(
      `${JSON.stringify({ success: true, settingUpdates: 1, nodeModes: ['custom', 'none', 'global'], mobileLayout })}\n`
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
