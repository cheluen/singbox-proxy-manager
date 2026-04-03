import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30021)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5184)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const SCRIPTS_DIR = path.dirname(SCRIPT_PATH)
const FRONTEND_ROOT = path.resolve(SCRIPTS_DIR, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const FRONTEND_BUILD_VERSION = FRONTEND_PACKAGE.version
const OFFICIAL_GITHUB_URL = 'https://github.com/cheluen/singbox-proxy-manager'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

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
  let lastCreatePayload = null
  let nodes = []

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJson(res, 200, { version: FRONTEND_BUILD_VERSION })
      return
    }

    if (req.method === 'GET' && req.url === '/api/auth/status') {
      sendJson(res, 200, { setup_required: false, admin_password_locked: false })
      return
    }

    if (req.method === 'GET' && req.url === '/api/settings') {
      sendJson(res, 200, {
        start_port: 30001,
        preserve_inbound_ports: true,
        admin_password_locked: false,
      })
      return
    }

    if (req.method === 'GET' && req.url === '/api/nodes') {
      sendJson(res, 200, nodes)
      return
    }

    if (req.method === 'POST' && req.url === '/api/nodes') {
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        lastCreatePayload = payload

        const createdNode = {
          id: 1,
          name: payload.name || 'warp',
          remark: payload.remark || '',
          type: payload.type || 'wireguard',
          config: payload.config || '{}',
          inbound_port: Number(payload.inbound_port || 30001),
          username: payload.username || 'u1',
          password: payload.password || 'p1',
          tcp_reuse_enabled: true,
          sort_order: 0,
          node_ip: '',
          location: '',
          country_code: '',
          latency: 0,
          enabled: payload.enabled !== false,
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        }
        nodes = [createdNode]
        sendJson(res, 201, { id: 1 })
      })
      return
    }

    if (req.method === 'POST' && req.url === '/api/logout') {
      sendJson(res, 200, { message: 'logged out' })
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { lastCreatePayload, nodes })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ lastCreatePayload, nodes })
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

const startVite = () => {
  const viteBin = path.join(FRONTEND_ROOT, 'node_modules', 'vite', 'bin', 'vite.js')
  const logs = []
  const child = spawn(process.execPath, [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)], {
    cwd: FRONTEND_ROOT,
    env: { ...process.env, E2E_API_PORT: String(API_PORT) },
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

const waitForProcessExit = (child, timeoutMs = 10000) =>
  new Promise((resolve) => {
    let settled = false
    const finish = () => {
      if (settled) return
      settled = true
      resolve()
    }

    child.once('exit', finish)
    setTimeout(() => {
      if (!settled) {
        try {
          child.kill('SIGKILL')
        } catch {
          // ignore
        }
      }
      finish()
    }, timeoutMs)
  })

const stopProcess = async (child) => {
  if (!child || child.exitCode !== null) return
  try {
    child.kill('SIGTERM')
  } catch {
    // ignore
  }
  await waitForProcessExit(child)
}

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const isIgnorableConsoleError = (text) =>
  text.includes('Support for defaultProps will be removed from memo components') ||
  text.includes('[antd: Modal] `destroyOnClose` is deprecated') ||
  text.includes("[antd: message] Static function can not consume context like dynamic theme") ||
  text.includes('Failed to load resource: the server responded with a status of 404 (Not Found)')

const clickButtonByText = async (page, text, timeoutMs = 10000) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const clicked = await page.evaluate((needle) => {
      const buttons = Array.from(document.querySelectorAll('button'))
      const button = buttons.find((btn) => (btn.textContent || '').trim().includes(needle))
      if (!button) return false
      button.click()
      return true
    }, text)
    if (clicked) return
    await sleep(200)
  }
  throw new Error(`Button not found: ${text}`)
}

const setFieldValueByLabel = async (page, labelText, value) => {
  const ok = await page.evaluate((labelText, value) => {
    const labels = Array.from(document.querySelectorAll('.ant-form-item-label label'))
    const label = labels.find((el) => (el.textContent || '').includes(labelText))
    const item = label?.closest('.ant-form-item')
    const input = item?.querySelector('textarea, input')
    if (!input) return false

    const prototype = Object.getPrototypeOf(input)
    const descriptor = Object.getOwnPropertyDescriptor(prototype, 'value')
    if (descriptor?.set) {
      descriptor.set.call(input, '')
      input.dispatchEvent(new Event('input', { bubbles: true }))
      descriptor.set.call(input, value)
    } else {
      input.value = value
    }
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
    return true
  }, labelText, value)

  if (!ok) {
    throw new Error(`Field not found: ${labelText}`)
  }
}

const getFieldValueByLabel = async (page, labelText) =>
  page.evaluate((labelText) => {
    const labels = Array.from(document.querySelectorAll('.ant-form-item-label label'))
    const label = labels.find((el) => (el.textContent || '').includes(labelText))
    const item = label?.closest('.ant-form-item')
    const input = item?.querySelector('textarea, input')
    return input ? String(input.value || '') : ''
  }, labelText)

const selectOptionByLabel = async (page, labelText, optionText, timeoutMs = 10000) => {
  const opened = await page.evaluate((labelText) => {
    const labels = Array.from(document.querySelectorAll('.ant-form-item-label label'))
    const label = labels.find((el) => (el.textContent || '').includes(labelText))
    const item = label?.closest('.ant-form-item')
    const select = item?.querySelector('.ant-select')
    const selector = item?.querySelector('.ant-select-selector')
    const searchInput = item?.querySelector('.ant-select-selection-search-input')
    const target = selector || searchInput || select
    if (!target) return false

    target.scrollIntoView({ block: 'center', inline: 'nearest' })
    const rect = target.getBoundingClientRect()
    const eventInit = {
      bubbles: true,
      cancelable: true,
      clientX: rect.left + rect.width / 2,
      clientY: rect.top + rect.height / 2,
      buttons: 1,
    }

    for (const element of [select, selector, searchInput]) {
      if (!element) continue
      element.dispatchEvent(new PointerEvent('pointerdown', { ...eventInit, pointerType: 'mouse' }))
      element.dispatchEvent(new MouseEvent('mousedown', eventInit))
      element.dispatchEvent(new MouseEvent('mouseup', eventInit))
      element.dispatchEvent(new MouseEvent('click', eventInit))
    }

    if (searchInput) {
      searchInput.focus()
      searchInput.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', code: 'ArrowDown', bubbles: true })
      )
      searchInput.dispatchEvent(
        new KeyboardEvent('keyup', { key: 'ArrowDown', code: 'ArrowDown', bubbles: true })
      )
    }

    return true
  }, labelText)

  if (!opened) {
    throw new Error(`Select not found: ${labelText}`)
  }

  await page.keyboard.press('ArrowDown').catch(() => {})

  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const found = await page.evaluate((needle) => {
      const options = Array.from(document.querySelectorAll('.ant-select-item-option'))
      if (options.some((el) => (el.textContent || '').includes(needle))) {
        return true
      }

      const holders = Array.from(
        document.querySelectorAll('.ant-select-dropdown .rc-virtual-list-holder')
      )
      for (const holder of holders) {
        holder.scrollTop = holder.scrollHeight
        holder.dispatchEvent(new Event('scroll', { bubbles: true }))
      }
      return false
    }, optionText)

    if (found) {
      break
    }
    await sleep(200)
  }

  await page.waitForFunction(
    (needle) =>
      Array.from(document.querySelectorAll('.ant-select-item-option')).some((el) =>
        (el.textContent || '').includes(needle)
      ),
    { timeout: timeoutMs },
    optionText
  )

  const selected = await page.evaluate((optionText) => {
    const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find((el) =>
      (el.textContent || '').includes(optionText)
    )
    if (!option) return false
    const rect = option.getBoundingClientRect()
    const eventInit = {
      bubbles: true,
      cancelable: true,
      clientX: rect.left + rect.width / 2,
      clientY: rect.top + rect.height / 2,
      buttons: 1,
    }
    option.dispatchEvent(new PointerEvent('pointerdown', { ...eventInit, pointerType: 'mouse' }))
    option.dispatchEvent(new MouseEvent('mousedown', eventInit))
    option.dispatchEvent(new MouseEvent('mouseup', eventInit))
    option.click()
    return true
  }, optionText)

  if (!selected) {
    throw new Error(`Option not found: ${optionText}`)
  }
}

const run = async () => {
  const mockApi = await startMockApi()
  await waitForHttpReady(`http://localhost:${API_PORT}/api/version`, 10000)
  const vite = startVite()
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
    await page.waitForSelector('.login-brand-link', { timeout: 15000 })

    const loginRepoHref = await page.$eval('.login-brand-link', (el) => el.href)
    assert(loginRepoHref === OFFICIAL_GITHUB_URL, `unexpected login repo href: ${loginRepoHref}`)
    const loginRepoUrlCount = await page.$$eval('.login-brand-link-url', (nodes) => nodes.length)
    assert(loginRepoUrlCount === 0, `expected login raw repo url text removed, got count=${loginRepoUrlCount}`)

    await page.evaluate(() => {
      localStorage.setItem('token', 'wireguard-ui-token')
      localStorage.setItem('language', 'en')
    })
    await page.reload({ waitUntil: 'networkidle2' })

    await page.waitForSelector('.dashboard-repo-link', { timeout: 15000 })
    const dashboardRepoHref = await page.$eval('.dashboard-repo-link', (el) => el.href)
    assert(
      dashboardRepoHref === OFFICIAL_GITHUB_URL,
      `unexpected dashboard repo href: ${dashboardRepoHref}`
    )
    const dashboardRepoUrlCount = await page.$$eval('.dashboard-repo-url', (nodes) => nodes.length)
    assert(
      dashboardRepoUrlCount === 0,
      `expected dashboard raw repo url text removed, got count=${dashboardRepoUrlCount}`
    )

    await clickButtonByText(page, 'Add Node', 15000)
    await page.waitForSelector('.ant-modal', { timeout: 15000 })

    await selectOptionByLabel(page, 'Proxy Type', 'Cloudflare WireGuard', 10000)

    const serverPortValue = await getFieldValueByLabel(page, 'Server Port')
    assert(serverPortValue === '2408', `expected server port to auto-switch to 2408, got ${serverPortValue}`)

    await setFieldValueByLabel(page, 'Node Name', 'WARP Node')
    await setFieldValueByLabel(page, 'Server Address', 'engage.cloudflareclient.com')
    await setFieldValueByLabel(page, 'Local Address (one per line)', '172.16.0.2/32\n2606:4700:110:8765::2/128')
    await setFieldValueByLabel(page, 'Private Key', 'private-key')
    await setFieldValueByLabel(page, 'Peer Public Key', 'peer-public-key')
    await setFieldValueByLabel(page, 'Allowed IPs (one per line)', '0.0.0.0/0\n::/0')
    await setFieldValueByLabel(page, 'Reserved Bytes', '162,104,222')

    await clickButtonByText(page, 'Save', 5000)
    await sleep(1000)

    const state = mockApi.getState()
    assert(state.lastCreatePayload, 'missing create payload')
    assert(state.lastCreatePayload.type === 'wireguard', `unexpected type: ${JSON.stringify(state.lastCreatePayload)}`)
    assert(state.lastCreatePayload.name === 'WARP Node', `unexpected name payload: ${JSON.stringify(state.lastCreatePayload)}`)

    const config = JSON.parse(state.lastCreatePayload.config || '{}')
    assert(config.server === 'engage.cloudflareclient.com', `unexpected wireguard config: ${JSON.stringify(config)}`)
    assert(config.server_port === 2408, `unexpected server_port: ${JSON.stringify(config)}`)
    assert(Array.isArray(config.local_address) && config.local_address.length === 2, `unexpected local_address: ${JSON.stringify(config)}`)
    assert(config.private_key === 'private-key', `unexpected private key: ${JSON.stringify(config)}`)
    assert(config.peer_public_key === 'peer-public-key', `unexpected peer public key: ${JSON.stringify(config)}`)
    assert(Array.isArray(config.allowed_ips) && config.allowed_ips[1] === '::/0', `unexpected allowed_ips: ${JSON.stringify(config)}`)
    assert(Array.isArray(config.reserved) && config.reserved.join(',') === '162,104,222', `unexpected reserved: ${JSON.stringify(config)}`)

    const filteredConsoleErrors = consoleErrors.filter((line) => !isIgnorableConsoleError(line))
    assert(filteredConsoleErrors.length === 0, `Unexpected console errors: ${filteredConsoleErrors.join('\n')}`)
  } finally {
    try {
      await browser?.close()
    } catch {
      // ignore
    }
    await stopProcess(vite.child)
    try {
      mockApi.server.close()
    } catch {
      // ignore
    }
  }
}

run().catch((err) => {
  console.error(err)
  process.exitCode = 1
})
