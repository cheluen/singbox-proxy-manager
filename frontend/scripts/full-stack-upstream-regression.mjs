import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const FRONTEND_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const PROJECT_ROOT = path.resolve(FRONTEND_ROOT, '..')
const BACKEND_BINARY = process.env.SBPM_E2E_BACKEND_BINARY
const SINGBOX_BINARY = process.env.SINGBOX_TEST_BINARY
const HTTP_PORT = Number(process.env.E2E_FULL_STACK_UPSTREAM_PORT || 30129)
const BASE_URL = `http://127.0.0.1:${HTTP_PORT}`
const ADMIN_PASSWORD = 'full-stack-upstream-password'
const ARTIFACT_DIR = process.env.E2E_ARTIFACT_DIR || ''

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

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
      // Retry while the backend starts.
    }
    await sleep(200)
  }
  throw new Error(`Timed out waiting for ${url}`)
}

const listen = (server) =>
  new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolve(server.address().port)
    })
  })

const closeServer = (server) =>
  new Promise((resolve) => {
    if (!server?.listening) {
      resolve()
      return
    }
    server.close(() => resolve())
  })

const reserveTCPPort = async () => {
  const server = net.createServer()
  const port = await listen(server)
  await closeServer(server)
  return port
}

const waitForTCP = async (port, timeoutMilliseconds) => {
  const deadline = Date.now() + timeoutMilliseconds
  while (Date.now() < deadline) {
    const connected = await new Promise((resolve) => {
      const socket = net.createConnection({ host: '127.0.0.1', port })
      socket.setTimeout(500)
      socket.once('connect', () => {
        socket.destroy()
        resolve(true)
      })
      socket.once('timeout', () => {
        socket.destroy()
        resolve(false)
      })
      socket.once('error', () => resolve(false))
    })
    if (connected) return
    await sleep(100)
  }
  throw new Error(`Timed out waiting for TCP port ${port}`)
}

const runCommand = (command, args) =>
  new Promise((resolve) => {
    const child = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'] })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (chunk) => { stdout += chunk.toString() })
    child.stderr.on('data', (chunk) => { stderr += chunk.toString() })
    child.on('error', (error) => resolve({ code: -1, stdout, stderr: `${stderr}${error.message}` }))
    child.on('exit', (code) => resolve({ code, stdout, stderr }))
  })

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

const clickButton = async (page, text, timeoutMilliseconds = 15000) => {
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

const openSettings = async (page) => {
  const clicked = await page.evaluate(() => {
    const button = document.querySelector('.anticon-setting')?.closest('button')
    if (!button) return false
    button.click()
    return true
  })
  assert(clicked, 'Settings button was not found')
  await page.waitForSelector('[data-testid="settings-form"]', { visible: true, timeout: 10000 })
}

const openNodeEdit = async (page, nodeName) => {
  const clicked = await page.evaluate((name) => {
    const row = Array.from(document.querySelectorAll('tr[data-row-key]')).find((candidate) =>
      (candidate.textContent || '').includes(name)
    )
    const button = row?.querySelector('.anticon-edit')?.closest('button')
    if (!button) return false
    button.click()
    return true
  }, nodeName)
  assert(clicked, `Edit button not found for ${nodeName}`)
  await page.waitForSelector('[data-testid="node-form"]', { visible: true, timeout: 10000 })
}

const browserAPI = async (page, pathName, options = {}) =>
  page.evaluate(
    async (pathValue, requestOptions) => {
      const token = localStorage.getItem('token') || ''
      const response = await fetch(pathValue, {
        ...requestOptions,
        headers: {
          'Content-Type': 'application/json',
          Authorization: token,
          ...(requestOptions.headers || {}),
        },
      })
      return { status: response.status, body: await response.json() }
    },
    pathName,
    options
  )

const waitForNodeMode = async (page, mode) => {
  const deadline = Date.now() + 15000
  while (Date.now() < deadline) {
    const result = await browserAPI(page, '/api/nodes')
    if (result.status === 200 && result.body?.[0]?.upstream_mode === mode) return result.body[0]
    await sleep(200)
  }
  throw new Error(`Timed out waiting for node upstream mode ${mode}`)
}

const readGeneratedConfig = (configDir) =>
  JSON.parse(fs.readFileSync(path.join(configDir, 'config.json'), 'utf8'))

const entryByTag = (entries, tag) =>
  (Array.isArray(entries) ? entries : []).find((entry) => entry?.tag === tag)

if (!BACKEND_BINARY || !SINGBOX_BINARY) {
  throw new Error('SBPM_E2E_BACKEND_BINARY and SINGBOX_TEST_BINARY are required')
}

const configDir = fs.mkdtempSync(path.join(os.tmpdir(), 'sbpm-upstream-full-stack-'))
const backendLogs = []
const upstreamLogs = []
let backend
let browser
let upstream
let targetServer

const startBackend = () => {
  const child = spawn(BACKEND_BINARY, [], {
    cwd: PROJECT_ROOT,
    env: {
      ...process.env,
      ADMIN_PASSWORD,
      BIND_ADDRESS: '127.0.0.1',
      CONFIG_DIR: configDir,
      PORT: String(HTTP_PORT),
      SINGBOX_BINARY,
      SBPM_UPDATE_CHECK_DISABLED: '1',
      TZ: 'UTC',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  child.stdout.on('data', (chunk) => backendLogs.push(chunk.toString()))
  child.stderr.on('data', (chunk) => backendLogs.push(chunk.toString()))
  return child
}

try {
  targetServer = http.createServer((_, response) => {
    response.writeHead(200, { 'Content-Type': 'text/plain' })
    response.end('sbpm-upstream-live-ok\n')
  })
  const targetPort = await listen(targetServer)
  let upstreamPort = await reserveTCPPort()
  while (upstreamPort === 30001 || upstreamPort === HTTP_PORT || upstreamPort === targetPort) {
    upstreamPort = await reserveTCPPort()
  }
  const upstreamConfigPath = path.join(configDir, 'test-upstream.json')
  fs.writeFileSync(
    upstreamConfigPath,
    JSON.stringify({
      log: { level: 'warn' },
      inbounds: [{ type: 'socks', tag: 'socks-in', listen: '127.0.0.1', listen_port: upstreamPort }],
      outbounds: [{ type: 'direct', tag: 'direct' }],
      route: { final: 'direct' },
    })
  )
  upstream = spawn(SINGBOX_BINARY, ['run', '-c', upstreamConfigPath], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  upstream.stdout.on('data', (chunk) => upstreamLogs.push(chunk.toString()))
  upstream.stderr.on('data', (chunk) => upstreamLogs.push(chunk.toString()))
  await waitForTCP(upstreamPort, 10000)

  backend = startBackend()
  await waitForHTTP(`${BASE_URL}/api/auth/status`, 30000)
  browser = await puppeteer.launch({
    headless: true,
    executablePath: browserExecutable(),
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  })
  const page = await browser.newPage()
  await page.setViewport({ width: 1440, height: 1000 })
  await page.evaluateOnNewDocument(() => localStorage.setItem('language', 'en'))

  const consoleErrors = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(`pageerror:${error.message}`))

  await page.goto(BASE_URL, { waitUntil: 'networkidle2' })
  await page.waitForSelector('input[type="password"]', { visible: true, timeout: 15000 })
  await page.type('input[type="password"]', ADMIN_PASSWORD)
  await clickButton(page, 'Login')
  await page.waitForSelector('[data-testid="nodes-add-node"]', { visible: true, timeout: 15000 })

  await openSettings(page)
  await openUpstreamEditor(page, '[data-testid="global-upstream-configure"]')
  await selectOption(page, 'global_upstream_proxy_type', 'SOCKS5')
  await setInput(page, '#global_upstream_server', '127.0.0.1')
  await setInput(page, '#global_upstream_server_port', upstreamPort)
  await page.$eval('[data-testid="upstream-editor"] button[type="submit"]', (button) => button.click())
  await page.waitForSelector('[data-testid="upstream-editor"]', { hidden: true, timeout: 10000 })
  await setSwitch(page, '[data-testid="global-upstream-enabled"]', true)
  await clickButton(page, 'Save Settings')
  await page.waitForSelector('[data-testid="settings-form"]', { hidden: true, timeout: 15000 })

  const persistedSettings = await browserAPI(page, '/api/settings')
  assert(persistedSettings.status === 200, `Settings API status=${persistedSettings.status}`)
  assert(persistedSettings.body.global_upstream_enabled === true, 'Global upstream was not persisted')
  assert(persistedSettings.body.global_upstream_type === 'socks5', 'Global upstream type was not persisted')

  await page.click('[data-testid="nodes-add-node"]')
  await page.waitForSelector('[data-testid="node-form"]', { visible: true, timeout: 10000 })
  await setInput(page, '#name', 'full-stack-upstream-node')
  await selectOption(page, 'proxy_type', 'Direct (Local)')
  await setSwitch(page, '#auth_enabled', false)
  const initialMode = await page.$eval(
    '[data-testid="node-upstream-mode"] .ant-segmented-item-selected',
    (element) => (element.textContent || '').trim()
  )
  assert(initialMode === 'Follow Global', `New node default mode=${initialMode}`)
  const createResponsePromise = page.waitForResponse(
    (response) => response.url() === `${BASE_URL}/api/nodes` && response.request().method() === 'POST',
    { timeout: 15000 }
  )
  await page.$eval('[data-testid="node-form"] button[type="submit"]', (button) => button.click())
  const createResponse = await createResponsePromise
  const createBody = await createResponse.json()
  assert(createResponse.status() === 201, `Create node failed (${createResponse.status()}): ${JSON.stringify(createBody)}`)
  const inheritedNode = await waitForNodeMode(page, 'global')

  let generated = readGeneratedConfig(configDir)
  assert(entryByTag(generated.outbounds, 'managed-upstream-global')?.type === 'socks', 'Global upstream outbound is missing')
  const inheritedOutbound = entryByTag(generated.outbounds, `node-${inheritedNode.id}-out`)
  assert(
    inheritedOutbound?.type === 'selector' && inheritedOutbound?.default === 'managed-upstream-global',
    `Direct node did not inherit global upstream: ${JSON.stringify(inheritedOutbound)}`
  )
  const inheritedTraffic = await runCommand('curl', [
    '--fail', '--silent', '--show-error', '--noproxy', '',
    '--socks5-hostname', `127.0.0.1:${inheritedNode.inbound_port}`,
    `http://127.0.0.1:${targetPort}/`,
  ])
  assert(
    inheritedTraffic.code === 0 && inheritedTraffic.stdout.trim() === 'sbpm-upstream-live-ok',
    `Live global upstream chain failed: ${inheritedTraffic.stderr || inheritedTraffic.stdout}`
  )

  await sleep(500)
  await openNodeEdit(page, 'full-stack-upstream-node')
  await clickSegment(page, 'Custom')
  await openUpstreamEditor(page, '[data-testid="node-upstream-configure"]')
  await selectOption(page, 'node_upstream_proxy_type', 'HTTP Proxy')
  await setInput(page, '#node_upstream_server', '198.51.100.21')
  await setInput(page, '#node_upstream_server_port', 8080)
  await page.$eval('[data-testid="upstream-editor"] button[type="submit"]', (button) => button.click())
  await page.waitForSelector('[data-testid="upstream-editor"]', { hidden: true, timeout: 10000 })
  await page.$eval('[data-testid="node-form"] button[type="submit"]', (button) => button.click())
  await waitForNodeMode(page, 'custom')

  generated = readGeneratedConfig(configDir)
  assert(entryByTag(generated.outbounds, `node-${inheritedNode.id}-upstream`)?.type === 'http', 'Custom upstream outbound is missing')
  const customOutbound = entryByTag(generated.outbounds, `node-${inheritedNode.id}-out`)
  assert(
    customOutbound?.type === 'selector' && customOutbound?.default === `node-${inheritedNode.id}-upstream`,
    `Custom upstream did not override global: ${JSON.stringify(customOutbound)}`
  )

  await sleep(500)
  await openNodeEdit(page, 'full-stack-upstream-node')
  await clickSegment(page, 'Direct')
  if (ARTIFACT_DIR) {
    fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-stack-upstream-node.png'), fullPage: true })
  }
  await page.$eval('[data-testid="node-form"] button[type="submit"]', (button) => button.click())
  const bypassNode = await waitForNodeMode(page, 'none')

  generated = readGeneratedConfig(configDir)
  assert(!('detour' in entryByTag(generated.outbounds, `node-${bypassNode.id}-out`)), 'Direct mode still has a detour')
  assert(!entryByTag(generated.outbounds, `node-${bypassNode.id}-upstream`), 'Inactive custom upstream was generated')
  await stopProcess(upstream)
  const bypassTraffic = await runCommand('curl', [
    '--fail', '--silent', '--show-error', '--noproxy', '',
    '--socks5-hostname', `127.0.0.1:${bypassNode.inbound_port}`,
    `http://127.0.0.1:${targetPort}/`,
  ])
  assert(
    bypassTraffic.code === 0 && bypassTraffic.stdout.trim() === 'sbpm-upstream-live-ok',
    `Explicit direct mode still depended on global upstream: ${bypassTraffic.stderr || bypassTraffic.stdout}`
  )

  await stopProcess(backend)
  backend = startBackend()
  await waitForHTTP(`${BASE_URL}/api/auth/status`, 30000)
  consoleErrors.length = 0
  const relogin = await fetch(`${BASE_URL}/api/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: ADMIN_PASSWORD }),
  })
  const reloginBody = await relogin.json()
  assert(relogin.status === 200 && reloginBody.token, `Relogin after restart failed: ${JSON.stringify(reloginBody)}`)
  const [settingsAfterRestart, nodesAfterRestart] = await Promise.all([
    fetch(`${BASE_URL}/api/settings`, { headers: { Authorization: `Bearer ${reloginBody.token}` } }),
    fetch(`${BASE_URL}/api/nodes`, { headers: { Authorization: `Bearer ${reloginBody.token}` } }),
  ])
  const persistedAfterRestart = {
    settings: await settingsAfterRestart.json(),
    nodes: await nodesAfterRestart.json(),
  }
  assert(persistedAfterRestart.settings.global_upstream_enabled === true, 'Global upstream did not survive restart')
  assert(persistedAfterRestart.nodes?.[0]?.upstream_mode === 'none', 'Node bypass mode did not survive restart')
  assert(persistedAfterRestart.nodes?.[0]?.upstream_type === 'http', 'Inactive custom definition did not survive restart')

  const check = await new Promise((resolve) => {
    const child = spawn(SINGBOX_BINARY, ['check', '-c', path.join(configDir, 'config.json')], {
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let output = ''
    child.stdout.on('data', (chunk) => { output += chunk.toString() })
    child.stderr.on('data', (chunk) => { output += chunk.toString() })
    child.on('exit', (code) => resolve({ code, output }))
  })
  assert(check.code === 0, `Final sing-box check failed: ${check.output}`)

  const unexpectedConsoleErrors = consoleErrors.filter(
    (line) =>
      !line.includes('Support for defaultProps will be removed from memo components') &&
      !line.includes('Static function can not consume context')
  )
  assert(unexpectedConsoleErrors.length === 0, `Browser errors: ${unexpectedConsoleErrors.join('\n')}`)

  process.stdout.write(
    `${JSON.stringify({ success: true, nodeId: bypassNode.id, persistedMode: 'none', globalType: 'socks5' })}\n`
  )
} catch (error) {
  throw new Error(
    `${error.message}\nBackend logs:\n${backendLogs.join('')}\nUpstream logs:\n${upstreamLogs.join('')}`
  )
} finally {
  try {
    await browser?.close()
  } catch {
    // Ignore browser cleanup failures.
  }
  await stopProcess(backend)
  await stopProcess(upstream)
  await closeServer(targetServer)
  fs.rmSync(configDir, { recursive: true, force: true })
}
