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
    env: { ...process.env, E2E_API_PORT: String(API_PORT) },
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
    await page.evaluateOnNewDocument(() => localStorage.setItem('token', 'version-update-token'))
    await page.goto(FRONTEND_URL, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.dashboard-version-update-link', { timeout: 15000 })

    const result = await page.evaluate(() => {
      const tag = document.querySelector('.dashboard-version-tag')
      const link = document.querySelector('.dashboard-version-update-link')
      return {
        tagText: tag?.textContent || '',
        href: link?.getAttribute('href') || '',
        ariaLabel: link?.getAttribute('aria-label') || '',
      }
    })

    assert(result.tagText.includes(FRONTEND_PACKAGE.version), `version tag missing current version: ${JSON.stringify(result)}`)
    assert(result.href === RELEASE_URL, `unexpected release URL: ${JSON.stringify(result)}`)
    assert(result.ariaLabel.includes(LATEST_VERSION), `aria-label missing latest version: ${JSON.stringify(result)}`)
    assert(consoleErrors.length === 0, `browser console errors: ${consoleErrors.join(' | ')}`)
  } finally {
    if (browser) await browser.close()
    await stopChild(vite.child)
    await new Promise((resolve) => apiServer.close(resolve))
  }
}

run().catch((error) => {
  console.error(error)
  process.exit(1)
})
