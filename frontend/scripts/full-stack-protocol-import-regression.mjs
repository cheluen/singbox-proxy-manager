import fs from 'node:fs'
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

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

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
const backend = spawn(BACKEND_BINARY, [], {
  cwd: PROJECT_ROOT,
  env: {
    ...process.env,
    ADMIN_PASSWORD,
    CONFIG_DIR: configDir,
    PORT: String(HTTP_PORT),
    SINGBOX_BINARY,
    SBPM_UPDATE_CHECK_DISABLED: '1',
    TZ: 'UTC',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})
backend.stdout.on('data', (chunk) => backendLogs.push(chunk.toString()))
backend.stderr.on('data', (chunk) => backendLogs.push(chunk.toString()))

let browser
try {
  await waitForHTTP(`${BASE_URL}/api/auth/status`, 30000)
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
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(`pageerror:${error.message}`))

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
  await page.type('input[type="password"]', ADMIN_PASSWORD)
  await clickVisibleButton(page, 'Login')
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll('button')).some((button) =>
      (button.textContent || '').includes('Batch Import')
    ),
    { timeout: 15000 }
  )

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

  const unexpectedConsoleErrors = consoleErrors.filter(
    (line) =>
      !line.includes('Support for defaultProps will be removed from memo components') &&
      !line.includes('Static function can not consume context like dynamic theme')
  )
  assert(unexpectedConsoleErrors.length === 0, `browser errors: ${unexpectedConsoleErrors.join('\n')}`)

  process.stdout.write(
    `${JSON.stringify({ success: true, version: state.version, importedTypes: state.nodes.map((node) => node.type).sort() })}\n`
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
  fs.rmSync(configDir, { recursive: true, force: true })
}
