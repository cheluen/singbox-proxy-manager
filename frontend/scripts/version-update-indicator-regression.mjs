import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30012)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5176)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const FRONTEND_PACKAGE = JSON.parse(fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8'))
const LATEST_VERSION = process.env.E2E_LATEST_VERSION || '9.9.9'
const RELEASE_URL = 'https://github.com/cheluen/singbox-proxy-manager/releases/tag/v9.9.9'
const loginAttempts = []

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
  if (process.env.PUPPETEER_EXECUTABLE_PATH) return process.env.PUPPETEER_EXECUTABLE_PATH
  for (const candidate of ['/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/usr/bin/chromium-browser', '/usr/bin/chromium']) {
    try {
      fs.accessSync(candidate, fs.constants.X_OK)
      return candidate
    } catch {
      // continue
    }
  }
  return undefined
}

const createMockApiServer = () => http.createServer((req, res) => {
  if (req.method === 'GET' && req.url === '/api/version') {
    sendJson(res, 200, {
      version: FRONTEND_PACKAGE.version,
      update: {
        current_version: FRONTEND_PACKAGE.version,
        latest_version: LATEST_VERSION,
        available: true,
        release_url: RELEASE_URL,
        checked_at: '2026-05-15T00:00:00Z',
      },
    })
    return
  }
  if (req.method === 'GET' && req.url === '/api/nodes') {
    sendJson(res, 200, [])
    return
  }
  if (req.method === 'GET' && req.url === '/api/settings') {
    sendJson(res, 200, { start_port: 30001, preserve_inbound_ports: false, admin_password_locked: false })
    return
  }
  if (req.method === 'GET' && req.url === '/api/runtime/status') {
    sendJson(res, 200, {
      degraded: true,
      running: false,
      message: 'sing-box exited unexpectedly',
    })
    return
  }
  if (req.method === 'GET' && req.url === '/api/auth/status') {
    sendJson(res, 200, { setup_required: false, admin_password_locked: false })
    return
  }
  if (req.method === 'POST' && req.url === '/api/login') {
    let body = ''
    req.on('data', (chunk) => {
      body += chunk
    })
    req.on('end', () => {
      const payload = JSON.parse(body || '{}')
      loginAttempts.push(payload.password || '')
      if (payload.password === 'correct-password') {
        sendJson(res, 200, { token: 'version-update-token' })
        return
      }
      sendJson(res, 401, { error: 'invalid password' })
    })
    return
  }
  sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
})

const listen = (server, options) => new Promise((resolve, reject) => {
  server.once('error', reject)
  server.listen(options, resolve)
})

const waitForHttpReady = async (url, timeoutMs = 60000) => {
  const deadline = Date.now() + timeoutMs
  let lastError
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url, { redirect: 'manual' })
      if (response.status >= 200 && response.status < 500) return
      lastError = new Error(`unexpected status ${response.status}`)
    } catch (error) {
      lastError = error
    }
    await sleep(200)
  }
  throw new Error(`HTTP readiness failed for ${url}: ${lastError?.message || 'timeout'}`)
}

const waitForProcessExit = (child, timeoutMs = 10000) => new Promise((resolve) => {
  let done = false
  const finish = () => {
    if (done) return
    done = true
    resolve()
  }
  child.once('exit', finish)
  setTimeout(() => {
    if (!done) {
      try { child.kill('SIGKILL') } catch { /* ignore */ }
    }
    finish()
  }, timeoutMs)
})

const stopChild = async (child) => {
  if (!child || child.killed) return
  try { child.kill('SIGTERM') } catch { /* ignore */ }
  await waitForProcessExit(child)
}

const startVite = () => {
  const viteBin = path.join(FRONTEND_ROOT, 'node_modules', 'vite', 'bin', 'vite.js')
  const child = spawn(process.execPath, [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)], {
    cwd: FRONTEND_ROOT,
    env: {
      ...process.env,
      E2E_API_PORT: String(API_PORT),
      VITE_API_TARGET: `http://127.0.0.1:${API_PORT}`,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const logs = []
  child.stdout.on('data', (chunk) => logs.push(chunk.toString()))
  child.stderr.on('data', (chunk) => logs.push(chunk.toString()))
  return { child, logs: () => logs.join('') }
}

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const run = async () => {
  const apiServer = createMockApiServer()
  await listen(apiServer, { port: API_PORT, host: '127.0.0.1' })
  const vite = startVite()
  let browser
  try {
    await waitForHttpReady(FRONTEND_URL)
    browser = await puppeteer.launch({
      headless: true,
      executablePath: getBrowserExecutablePath(),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })
    const page = await browser.newPage()
    const consoleErrors = []
    page.on('console', (msg) => {
      if (msg.type() === 'error' && !msg.text().includes('defaultProps will be removed')) {
        consoleErrors.push(msg.text())
      }
    })
    page.on('pageerror', (err) => consoleErrors.push(`pageerror:${err.message}`))
    await page.evaluateOnNewDocument(() => localStorage.setItem('language', 'en'))
    await page.goto(FRONTEND_URL, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('input#password', { visible: true, timeout: 15000 })

    let navigationCount = 0
    const countNavigation = (frame) => {
      if (frame === page.mainFrame()) navigationCount += 1
    }
    page.on('framenavigated', countNavigation)
    await page.type('input#password', 'wrong-password')
    await page.$eval('.login-panel button[type="submit"]', (button) => button.click())
    await page.waitForFunction(
      () =>
        Array.from(document.querySelectorAll('.ant-message-notice')).some(
          (element) => (element.textContent || '').includes('invalid password')
        ),
      { timeout: 10000 }
    )
    const failedLogin = await page.evaluate(() => ({
      value: document.querySelector('input#password')?.value || '',
      loginVisible: Boolean(document.querySelector('.login-card')),
    }))
    assert(
      failedLogin.value === 'wrong-password' && failedLogin.loginVisible,
      `failed login lost input or left the login page: ${JSON.stringify(failedLogin)}`
    )
    assert(navigationCount === 0, `failed login refreshed the page ${navigationCount} time(s)`)

    await page.click('input#password', { clickCount: 3 })
    await page.keyboard.press('Backspace')
    await page.type('input#password', 'correct-password')
    await page.$eval('.login-panel button[type="submit"]', (button) => button.click())
    await page.waitForFunction(
      () =>
        Array.from(document.querySelectorAll('.ant-message-notice')).some(
          (element) => (element.textContent || '').includes('Login successful')
        ),
      { timeout: 10000 }
    )
    const loginSuccessNoticeCount = await page.$$eval(
      '.ant-message-notice',
      (elements) => elements.filter(
        (element) => (element.textContent || '').includes('Login successful')
      ).length
    )
    await page.waitForSelector('.dashboard-version-update-link', { timeout: 15000 })
    await page.waitForSelector('[data-testid="runtime-status-degraded"]', { timeout: 15000 })

    const result = await page.evaluate(() => {
      const tag = document.querySelector('.dashboard-version-tag')
      const link = document.querySelector('.dashboard-version-update-link')
      return {
        tagText: tag?.textContent || '',
        href: link?.getAttribute('href') || '',
        ariaLabel: link?.getAttribute('aria-label') || '',
        degradedText: document.querySelector('[data-testid="runtime-status-degraded"]')?.textContent || '',
      }
    })
    page.off('framenavigated', countNavigation)

    assert(result.tagText.includes(FRONTEND_PACKAGE.version), `version tag missing current version: ${JSON.stringify(result)}`)
    assert(result.href === RELEASE_URL, `unexpected release URL: ${JSON.stringify(result)}`)
    assert(result.ariaLabel.includes(LATEST_VERSION), `aria-label missing latest version: ${JSON.stringify(result)}`)
    assert(result.degradedText.includes('sing-box exited unexpectedly'), `runtime degraded reason missing: ${JSON.stringify(result)}`)
    assert(loginSuccessNoticeCount === 1, `login success notification duplicated: ${loginSuccessNoticeCount}`)
    assert(navigationCount === 0, `login flow unexpectedly navigated ${navigationCount} time(s)`)
    assert(
      JSON.stringify(loginAttempts) === JSON.stringify(['wrong-password', 'correct-password']),
      `unexpected login attempts: ${JSON.stringify(loginAttempts)}`
    )
    const unexpectedConsoleErrors = consoleErrors.filter(
      (line) =>
        !line.includes('401 (Unauthorized)') &&
        !line.includes('status of 401') &&
        !line.includes('[antd: message] Static function can not consume context')
    )
    assert(unexpectedConsoleErrors.length === 0, `browser console errors: ${unexpectedConsoleErrors.join(' | ')}`)
  } finally {
    if (browser) await browser.close()
    await stopChild(vite.child)
    await new Promise((resolve) => apiServer.close(resolve))
  }
}

run().catch((error) => {
  console.error(error)
  console.error(`login attempts: ${JSON.stringify(loginAttempts)}`)
  process.exit(1)
})
