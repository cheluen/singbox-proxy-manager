import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30012)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5175)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const SCRIPTS_DIR = path.dirname(SCRIPT_PATH)
const FRONTEND_ROOT = path.resolve(SCRIPTS_DIR, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const FRONTEND_BUILD_VERSION = FRONTEND_PACKAGE.version

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
  let lastBatchImportPayload = null
  let nodes = []

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJson(res, 200, { version: FRONTEND_BUILD_VERSION })
      return
    }

    if (req.method === 'GET' && req.url === '/api/nodes') {
      sendJson(res, 200, nodes)
      return
    }

    if (req.method === 'POST' && req.url === '/api/nodes/batch-import') {
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        lastBatchImportPayload = payload

        if (typeof payload.content !== 'string' || payload.content.trim() === '') {
          sendJson(res, 400, { error: 'missing content' })
          return
        }

        nodes = [
          {
            id: 1,
            name: 'imported-1',
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
            enabled: Boolean(payload.enabled),
            remark: '',
            created_at: '2026-01-01T00:00:00Z',
            updated_at: '2026-01-01T00:00:00Z',
          },
        ]

        sendJson(res, 200, {
          total: 1,
          success: 1,
          failed: 0,
          results: [{ success: true, id: 1, name: 'imported-1' }],
        })
      })
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { lastBatchImportPayload, nodes })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ lastBatchImportPayload, nodes })
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
    if (clicked) {
      return
    }
    await sleep(200)
  }
  throw new Error(`Button not found: ${text}`)
}

const ensureCheckboxUnchecked = async (
  page,
  { inputSelector, clickableSelector },
  timeoutMs = 10000
) => {
  const checked = await page.evaluate(
    (sel) => Boolean(document.querySelector(sel)?.checked),
    inputSelector
  )
  if (!checked) return

  await page.locator(clickableSelector).click({ timeout: timeoutMs })
  await page.waitForFunction(
    (sel) => {
      const el = document.querySelector(sel)
      return el ? !el.checked : false
    },
    { timeout: timeoutMs },
    inputSelector
  )
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
    await page.setViewport({ width: 1366, height: 900 })
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
      localStorage.setItem('token', 'e2e-token')
      localStorage.setItem('language', 'en')
    })
    await page.reload({ waitUntil: 'networkidle2' })

    await clickButtonByText(page, 'Batch Import', 15000)
    await page.waitForSelector('.ant-modal textarea', { timeout: 15000 })

    const content = 'https://example.com/subscription\nss://YWVzLTEyOC1nY206dGVzdA==@example.com:443#SS'
    await page.type('.ant-modal textarea', content)
    await sleep(300)

    // Avoid triggering IP check (mock server does not implement it).
    await ensureCheckboxUnchecked(
      page,
      {
        inputSelector: '.ant-modal .ant-checkbox-input',
        clickableSelector: '.ant-modal .ant-checkbox',
      },
      10000
    )

    await clickButtonByText(page, 'Confirm', 5000)
    await sleep(800)

    const state = mockApi.getState()
    assert(state.lastBatchImportPayload, 'missing batch import payload')
    assert(
      typeof state.lastBatchImportPayload.content === 'string' &&
        state.lastBatchImportPayload.content.trim() === content,
      `unexpected content payload: ${JSON.stringify(state.lastBatchImportPayload)}`
    )
    assert(
      !Array.isArray(state.lastBatchImportPayload.links),
      `expected links to be absent, got: ${JSON.stringify(state.lastBatchImportPayload.links)}`
    )

    const filteredConsoleErrors = consoleErrors.filter((line) => !isIgnorableConsoleError(line))
    assert(filteredConsoleErrors.length === 0, `Unexpected console errors: ${filteredConsoleErrors.join('\\n')}`)
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
