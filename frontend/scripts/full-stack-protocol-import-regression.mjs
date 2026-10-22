import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const PROJECT_ROOT = path.resolve(FRONTEND_ROOT, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const BACKEND_BINARY = process.env.SBPM_E2E_BACKEND_BINARY
const SINGBOX_BINARY = process.env.SINGBOX_TEST_BINARY
const HTTP_PORT = Number(process.env.E2E_FULL_STACK_PORT || 30126)
const BASE_URL = `http://127.0.0.1:${HTTP_PORT}`
const ADMIN_PASSWORD = 'full-stack-e2e-password'
const ARTIFACT_DIR = process.env.E2E_ARTIFACT_DIR || ''

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

const listen = (server) => new Promise((resolve, reject) => {
  server.once('error', reject)
  server.listen(0, '127.0.0.1', () => {
    server.off('error', reject)
    resolve(server.address().port)
  })
})

const closeServer = (server) => new Promise((resolve) => {
  if (!server.listening) return resolve()
  server.close(resolve)
  server.closeAllConnections?.()
})

const reservePort = async () => {
  const server = net.createServer()
  const port = await listen(server)
  await closeServer(server)
  return port
}

const setInput = async (page, selector, value) => {
  await page.$eval(selector, (input, nextValue) => {
    const prototype = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(input, String(nextValue))
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
  }, value)
}

const getBrowserExecutablePath = () => {
  if (process.env.PUPPETEER_EXECUTABLE_PATH) {
    return process.env.PUPPETEER_EXECUTABLE_PATH
  }
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

const waitForHTTP = async (url, timeoutMs) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.status < 500) return
    } catch {
      // Retry until the real backend is listening.
    }
    await sleep(200)
  }
  throw new Error(`Timed out waiting for ${url}`)
}

const stopProcess = async (child) => {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    sleep(8000).then(() => {
      if (child.exitCode === null) child.kill('SIGKILL')
    }),
  ])
}

const clickVisibleButton = async (page, text, timeoutMs = 15000) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const clicked = await page.evaluate((needle) => {
      const button = Array.from(document.querySelectorAll('button')).find((candidate) => {
        const rect = candidate.getBoundingClientRect()
        return rect.width > 0 && rect.height > 0 && (candidate.textContent || '').trim() === needle
      })
      if (!button || button.disabled) return false
      button.click()
      return true
    }, text)
    if (clicked) return
    await sleep(100)
  }
  throw new Error(`Visible button not found: ${text}`)
}

if (!BACKEND_BINARY || !SINGBOX_BINARY) {
  throw new Error('SBPM_E2E_BACKEND_BINARY and SINGBOX_TEST_BINARY are required')
}

const configDir = fs.mkdtempSync(path.join(os.tmpdir(), 'sbpm-full-stack-'))
const backendLogs = []
const target = http.createServer((_request, response) => {
  response.setHeader('Content-Type', 'application/json')
  response.end(JSON.stringify({ ip: '203.0.113.8', country: 'Test', cc: 'TT' }))
})
const occupiedPortServer = net.createServer((socket) => socket.destroy())
let backend
let upstream
let browser
try {
  const targetPort = await listen(target)
  backend = spawn(BACKEND_BINARY, [], {
    cwd: PROJECT_ROOT,
    env: {
      ...process.env,
      ADMIN_PASSWORD,
      CONFIG_DIR: configDir,
      PORT: String(HTTP_PORT),
      SINGBOX_BINARY,
      SBPM_IPCHECK_URLS: `http://127.0.0.1:${targetPort}/ip`,
      SBPM_SINGBOX_LOG_OUTPUT: 'both',
      SBPM_UPDATE_CHECK_DISABLED: '1',
      TZ: 'UTC',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  backend.stdout.on('data', (chunk) => backendLogs.push(chunk.toString()))
  backend.stderr.on('data', (chunk) => backendLogs.push(chunk.toString()))
  await waitForHTTP(`${BASE_URL}/api/version`, 30000)
  for (const legacyRoute of [
    { method: 'GET', path: '/api/auth/status' },
    { method: 'POST', path: '/api/setup/admin-password' },
  ]) {
    const response = await fetch(`${BASE_URL}${legacyRoute.path}`, { method: legacyRoute.method })
    assert(response.status === 404, `${legacyRoute.method} ${legacyRoute.path} status=${response.status}`)
  }
  browser = await puppeteer.launch({
    headless: true,
    executablePath: getBrowserExecutablePath(),
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  })
  const page = await browser.newPage()
  await page.setViewport({ width: 1440, height: 1000 })
  await page.evaluateOnNewDocument(() => {
    localStorage.setItem('language', 'en')
  })

  const consoleErrors = []
  const legacyAuthRequests = []
  page.on('console', (message) => {
    if (message.location().url === `${BASE_URL}/api/nodes/batch-import` && message.text().includes('400')) return
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(`pageerror:${error.message}`))
  page.on('request', (request) => {
    const pathName = new URL(request.url()).pathname
    if (pathName === '/api/auth/status' || pathName === '/api/setup/admin-password') {
      legacyAuthRequests.push(`${request.method()} ${pathName}`)
    }
  })

  let batchResponse
  page.on('response', async (response) => {
    if (
      response.url() === `${BASE_URL}/api/nodes/batch-import` &&
      response.request().method() === 'POST'
    ) {
      batchResponse = {
        status: response.status(),
        body: await response.json(),
      }
    }
  })

  await page.goto(BASE_URL, { waitUntil: 'networkidle2' })
  await page.waitForSelector('input[type="password"]', { visible: true, timeout: 15000 })
  const inspectLoginLayout = () =>
    page.evaluate(() => {
      const passwordInputs = document.querySelectorAll('input[type="password"]')
      const visibleButtons = Array.from(document.querySelectorAll('button')).filter((button) => {
        const rect = button.getBoundingClientRect()
        return rect.width > 0 && rect.height > 0
      })
      return {
        passwordInputCount: passwordInputs.length,
        visibleButtonLabels: visibleButtons.map((button) => (button.textContent || '').trim()),
        viewportWidth: window.innerWidth,
        documentWidth: document.documentElement.scrollWidth,
      }
    })
  const desktopLogin = await inspectLoginLayout()
  assert(desktopLogin.passwordInputCount === 1, `desktop login inputs: ${JSON.stringify(desktopLogin)}`)
  assert(desktopLogin.visibleButtonLabels.includes('Login'), `desktop login button: ${JSON.stringify(desktopLogin)}`)
  assert(!desktopLogin.visibleButtonLabels.includes('Set password'), `legacy setup button: ${JSON.stringify(desktopLogin)}`)
  if (ARTIFACT_DIR) {
    fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'login-desktop.png'), fullPage: true })
  }
  await page.setViewport({ width: 390, height: 844 })
  const mobileLogin = await inspectLoginLayout()
  assert(mobileLogin.passwordInputCount === 1, `mobile login inputs: ${JSON.stringify(mobileLogin)}`)
  assert(mobileLogin.documentWidth <= mobileLogin.viewportWidth, `mobile login overflow: ${JSON.stringify(mobileLogin)}`)
  if (ARTIFACT_DIR) {
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'login-mobile.png'), fullPage: true })
  }
  await page.setViewport({ width: 1440, height: 1000 })
  await page.type('input[type="password"]', ADMIN_PASSWORD)
  await clickVisibleButton(page, 'Login')
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll('button')).some((button) =>
      (button.textContent || '').includes('Batch Import')
    ),
    { timeout: 15000 }
  )
  assert(legacyAuthRequests.length === 0, `legacy auth requests: ${legacyAuthRequests.join(', ')}`)

  await clickVisibleButton(page, 'Batch Import')
  await page.waitForSelector('.ant-modal textarea', { visible: true, timeout: 10000 })
  const content = [
    'socks5://user:pass@127.0.0.1:1080#system-valid-socks',
    'trojan://@127.0.0.1:443?security=tls&insecure=1#system-empty-trojan',
    'hysteria2://@127.0.0.1:443/?insecure=1#system-empty-hy2',
    'anytls://@127.0.0.1:443/?insecure=1#system-empty-anytls',
    'vless://00000000-0000-0000-0000-000000000000@127.0.0.1:443?security=tls&flow=unsupported-flow&detour=system-valid-socks#system-bad-vless',
  ].join('\n')
  await page.type('.ant-modal textarea', content)

  const enabledCheckbox = await page.$('.ant-modal .ant-checkbox-input')
  if (enabledCheckbox && (await enabledCheckbox.evaluate((input) => input.checked))) {
    await page.click('.ant-modal .ant-checkbox')
  }
  await clickVisibleButton(page, 'Confirm')

  const deadline = Date.now() + 30000
  while (!batchResponse && Date.now() < deadline) {
    await sleep(100)
  }
  assert(batchResponse, 'real batch-import response was not captured')
  assert(batchResponse.status === 200, `real batch-import status=${batchResponse.status}`)
  assert(
    batchResponse.body?.total === 5 &&
      batchResponse.body?.success === 4 &&
      batchResponse.body?.failed === 1,
    `unexpected real batch-import summary: ${JSON.stringify(batchResponse.body)}`
  )

  await page.waitForSelector('[data-testid="batch-import-errors"]', { visible: true })
  const failureText = await page.$eval('[data-testid="batch-import-errors"]', (element) => element.textContent)
  assert(failureText.includes('unsupported-flow') && failureText.includes('sing-box validation failed'), `missing failure reason: ${failureText}`)
  assert(await page.$eval('.ant-modal textarea', (element) => element.value) === content, 'partial import erased input')
  assert(await page.$eval('.ant-modal .ant-btn-primary', (button) => button.disabled), 'unchanged partial batch can be duplicated')
  for (const width of [1440, 390]) {
    await page.setViewport({ width, height: 1000 })
    const overflow = await page.$eval('[data-testid="batch-import-errors"]', (element) => element.scrollWidth > element.clientWidth)
    assert(!overflow, `batch failure details overflow at ${width}px`)
    if (ARTIFACT_DIR) await page.screenshot({ path: path.join(ARTIFACT_DIR, `import-errors-${width}.png`), fullPage: true })
  }
  await page.setViewport({ width: 1440, height: 1000 })
  await clickVisibleButton(page, 'Cancel')

  const state = await page.evaluate(async () => {
    const token = localStorage.getItem('token') || ''
    const [nodesResponse, versionResponse] = await Promise.all([
      fetch('/api/nodes', { headers: { Authorization: token } }),
      fetch('/api/version'),
    ])
    return {
      nodesStatus: nodesResponse.status,
      nodes: await nodesResponse.json(),
      version: (await versionResponse.json()).version,
    }
  })
  assert(state.nodesStatus === 200, `real nodes API status=${state.nodesStatus}`)
  assert(Array.isArray(state.nodes) && state.nodes.length === 4, `unexpected real nodes: ${JSON.stringify(state.nodes)}`)
  assert(
    ['anytls', 'hy2', 'socks5', 'trojan'].every((type) =>
      state.nodes.some((node) => node.type === type)
    ),
    `real nodes are missing expected protocols: ${JSON.stringify(state.nodes)}`
  )
  assert(state.version === FRONTEND_PACKAGE.version, `frontend/backend version skew: ${JSON.stringify(state)}`)

  const apiRequest = async (method, endpoint, body) => {
    const result = await page.evaluate(async (method, endpoint, body) => {
      const response = await fetch(`/api${endpoint}`, {
        method,
        headers: { Authorization: localStorage.getItem('token') || '', 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
      })
      return { status: response.status, body: await response.json() }
    }, method, endpoint, body)
    assert(result.status < 400, `${method} ${endpoint}: ${JSON.stringify(result)}`)
    return result.body
  }
  const runtimeOutbound = (node) => {
    const config = JSON.parse(fs.readFileSync(path.join(configDir, 'config.json'), 'utf8'))
    const outbound = config.outbounds.find((entry) => entry.tag === `node-${node.id}-out`)
    assert(outbound, `missing runtime outbound for ${node.name}`)
    return outbound
  }
  const saveForm = async (method, endpoint) => {
    const responsePromise = page.waitForResponse((response) =>
      response.request().method() === method && response.url() === `${BASE_URL}/api${endpoint}`
    )
    await clickVisibleButton(page, 'Save')
    const response = await responsePromise
    const body = await response.json()
    assert(response.status() < 400, `form save failed: ${JSON.stringify(body)}`)
    await page.waitForFunction(() => !Array.from(document.querySelectorAll('.ant-modal form')).some((form) => form.getBoundingClientRect().height > 0))
    return body
  }
  const editNode = async (node) => {
    await page.reload({ waitUntil: 'networkidle2' })
    const selector = `tr[data-row-key="${node.id}"] .anticon-edit`
    await page.waitForSelector(selector, { visible: true })
    await page.click(selector)
    await page.waitForSelector('#name', { visible: true })
  }

  const securityContent = `proxies:
  - {name: pinned-trojan, type: trojan, server: proxy.example, port: 443, password: test, fingerprint: "${'ab'.repeat(32)}", client-fingerprint: chrome, skip-cert-verify: true}
  - {name: encrypted-socks, type: socks5, server: proxy.example, port: 443, tls: true}
`
  await clickVisibleButton(page, 'Batch Import')
  await page.waitForSelector('.ant-modal textarea', { visible: true })
  await setInput(page, '.ant-modal textarea', securityContent)
  await clickVisibleButton(page, 'Confirm')
  await page.waitForFunction(() => document.querySelector('[data-testid="batch-import-errors"]')?.textContent.includes('SOCKS TLS'))
  const securityErrors = await page.$eval('[data-testid="batch-import-errors"]', (element) => element.textContent)
  assert(securityErrors.includes('certificate fingerprint pinning'), 'certificate pin rejection is missing from the UI')
  assert(await page.$eval('.ant-modal textarea', (element) => element.value) === securityContent, 'fully failed batch erased input')
  assert((await apiRequest('GET', '/nodes')).length === 4, 'unsafe imports were persisted')
  await clickVisibleButton(page, 'Cancel')

  const occupiedPort = await listen(occupiedPortServer)
  const settings = await apiRequest('GET', '/settings')
  await apiRequest('PUT', '/settings', { ...settings, start_port: occupiedPort })
  const portBatch = await apiRequest('POST', '/nodes/batch-import', {
    content: ['one', 'two', 'three'].map((name) => `socks5://127.0.0.1:1080#occupied-${name}`).join('\n'),
    enabled: false,
  })
  assert(portBatch.success === 3 && portBatch.failed === 0, `system occupied port broke batch: ${JSON.stringify(portBatch)}`)
  const occupiedNodes = (await apiRequest('GET', '/nodes')).filter((node) => node.name.startsWith('occupied-'))
  assert(occupiedNodes.every((node) => node.inbound_port > occupiedPort), 'occupied port was assigned')
  await closeServer(occupiedPortServer)

  const socksPort = await reservePort()
  const vmessPort = await reservePort()
  const vmessUUID = '00000000-0000-0000-0000-000000000019'
  const upstreamConfigPath = path.join(configDir, 'upstream-test.json')
  fs.writeFileSync(upstreamConfigPath, JSON.stringify({
    log: { level: 'error' },
    inbounds: [
      { type: 'socks', tag: 'socks', listen: '127.0.0.1', listen_port: socksPort },
      { type: 'vmess', tag: 'vmess', listen: '127.0.0.1', listen_port: vmessPort, users: [{ uuid: vmessUUID }], transport: { type: 'http', path: '/new' } },
    ],
    outbounds: [{ type: 'direct', tag: 'direct' }],
  }), { mode: 0o600 })
  upstream = spawn(SINGBOX_BINARY, ['run', '-c', upstreamConfigPath], { stdio: ['ignore', 'pipe', 'pipe'] })
  upstream.stdout.on('data', (chunk) => backendLogs.push(chunk.toString()))
  upstream.stderr.on('data', (chunk) => backendLogs.push(chunk.toString()))
  const chainTarget = await apiRequest('POST', '/nodes', {
    name: 'chain-target', type: 'socks5', enabled: true, auth_enabled: false,
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPort }),
  })
  await page.reload({ waitUntil: 'networkidle2' })
  await clickVisibleButton(page, 'Import Node')
  await page.waitForSelector('#import-link-input', { visible: true })
  await setInput(page, '#import-link-input', `socks5://127.0.0.1:${socksPort}?detour=chain-target#single-detour`)
  await clickVisibleButton(page, 'Parse Link')
  await page.waitForSelector('#auth_enabled', { visible: true })
  await page.$eval('#auth_enabled', (element) => { if (element.getAttribute('aria-checked') === 'true') element.click() })
  const singleNode = await saveForm('POST', '/nodes')
  const batchChain = await apiRequest('POST', '/nodes/batch-import', {
    content: `socks5://127.0.0.1:${socksPort}?detour=chain-target#batch-detour`, enabled: true,
  })
  assert(batchChain.success === 1, 'batch detour import failed')
  const chainNodes = (await apiRequest('GET', '/nodes')).filter((node) => ['single-detour', 'batch-detour'].includes(node.name))
  for (const node of chainNodes) {
    assert(node.upstream_mode === 'legacy', `${node.name} lost upstream mode`)
    assert(runtimeOutbound(node).detour === `node-${chainTarget.id}-out`, `${node.name} lost runtime detour`)
    const info = await apiRequest('POST', `/nodes/${node.id}/check-ip`)
    assert(info.ip === '203.0.113.8' && info.transport === 'http', `real detour forwarding failed: ${JSON.stringify(info)}`)
  }
  assert(chainNodes.some((node) => node.id === singleNode.id) && chainNodes.length === 2, 'single/batch import mismatch')

  const vmess = await apiRequest('POST', '/nodes', {
    name: 'vmess-path-edit', type: 'vmess', enabled: true, auth_enabled: true,
    username: ' user name ', password: ' p a+@:% ss ',
    config: JSON.stringify({
      server: '127.0.0.1', server_port: vmessPort, uuid: vmessUUID,
      security: 'aes-128-gcm', network: 'http', tls: 'none', path: '/old', http_path: ['/old'],
      transport_options: { type: 'http', path: '/old' },
    }),
  })
  await editNode(vmess)
  assert(await page.$eval('#vmess_security', (input) => input.closest('.ant-select').textContent.includes('aes-128-gcm')), 'VMess encryption is blank')
  await setInput(page, '#path', '/new')
  await saveForm('PUT', `/nodes/${vmess.id}`)
  const savedVMess = await apiRequest('GET', `/nodes/${vmess.id}`)
  assert(JSON.parse(savedVMess.config).http_path[0] === '/new', 'VMess historical path overrides edit in database')
  assert(runtimeOutbound(vmess).transport.path === '/new', 'VMess runtime path was not updated')
  const vmessIP = await apiRequest('POST', `/nodes/${vmess.id}/check-ip`)
  assert(vmessIP.ip === '203.0.113.8' && vmessIP.transport === 'http' && !vmessIP.http_error, `HTTP credentials or VMess path failed: ${JSON.stringify(vmessIP)}`)

  const hy2 = await apiRequest('POST', '/nodes', {
    name: 'hy2-bandwidth-edit', type: 'hy2', enabled: true, auth_enabled: false,
    config: JSON.stringify({ server: '127.0.0.1', server_port: 443, password: 'test', up_mbps: 10, down_mbps: 20, brutal_up_mbps: 40, brutal_down_mbps: 30 }),
  })
  await editNode(hy2)
  assert(await page.$eval('#up_mbps', (input) => input.value) === '40', 'Hysteria2 did not display effective upload bandwidth')
  await setInput(page, '#up_mbps', '90')
  await setInput(page, '#down_mbps', '')
  await saveForm('PUT', `/nodes/${hy2.id}`)
  const savedHy2 = JSON.parse((await apiRequest('GET', `/nodes/${hy2.id}`)).config)
  assert(!('brutal_up_mbps' in savedHy2) && !('brutal_down_mbps' in savedHy2), 'historical bandwidth fields survived editing')
  assert(runtimeOutbound(hy2).up_mbps === 90 && !runtimeOutbound(hy2).down_mbps, 'Hysteria2 runtime ignored bandwidth edits')

  for (const [type, field, config] of [
    ['vless', 'flow', { uuid: vmessUUID, flow: 'xtls-rprx-vision', security: 'tls' }],
    ['anytls', 'fingerprint', { password: 'test', fingerprint: 'chrome', tls_options: { enabled: true, utls: { enabled: true, fingerprint: 'chrome' } } }],
  ]) {
    const node = await apiRequest('POST', '/nodes', {
      name: `clear-${field}`, type, enabled: true, auth_enabled: false,
      config: JSON.stringify({ server: '127.0.0.1', server_port: 443, ...config }),
    })
    await editNode(node)
    const select = `.ant-select:has(#${field})`
    await page.locator(select).hover()
    await page.locator(`${select} .ant-select-clear`).click()
    await page.waitForFunction((fieldID) => {
      const text = document.getElementById(fieldID)?.closest('.ant-select')
        ?.querySelector('.ant-select-selection-item')?.textContent?.trim() || ''
      return text === '' || text === 'None'
    }, { timeout: 5000 }, field)
    await saveForm('PUT', `/nodes/${node.id}`)
    const saved = JSON.parse((await apiRequest('GET', `/nodes/${node.id}`)).config)
    assert(saved[field] === '', `${field} did not clear in database`)
    const outbound = runtimeOutbound(node)
    assert(field === 'flow' ? !outbound.flow : !outbound.tls?.utls, `${field} did not clear in runtime`)
  }

  const unexpectedConsoleErrors = consoleErrors.filter(
    (line) =>
      !line.includes('Support for defaultProps will be removed from memo components') &&
      !line.includes('Static function can not consume context like dynamic theme')
  )
  assert(unexpectedConsoleErrors.length === 0, `browser errors: ${unexpectedConsoleErrors.join('\n')}`)

  process.stdout.write(
    `${JSON.stringify({ success: true, version: state.version, importedTypes: state.nodes.map((node) => node.type).sort(), checked: ['failure-details', 'security-rejections', 'occupied-port', 'single-batch-detour', 'vmess-path', 'http-space-credentials', 'hy2-bandwidth', 'clear-select', 'vmess-security'] })}\n`
  )
} catch (error) {
  throw new Error(`${error.message}\nBackend logs:\n${backendLogs.join('')}`)
} finally {
  try {
    await browser?.close()
  } catch {
    // Ignore browser cleanup failures.
  }
  await stopProcess(backend)
  await stopProcess(upstream)
  await closeServer(occupiedPortServer)
  await closeServer(target)
  fs.rmSync(configDir, { recursive: true, force: true })
}
