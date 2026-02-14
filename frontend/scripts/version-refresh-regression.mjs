import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30010)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5174)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const SCRIPTS_DIR = path.dirname(SCRIPT_PATH)
const FRONTEND_ROOT = path.resolve(SCRIPTS_DIR, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const SERVER_VERSION = process.env.E2E_SERVER_VERSION || `${FRONTEND_PACKAGE.version}-server`

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
  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJson(res, 200, { version: SERVER_VERSION })
      return
    }

    if (req.method === 'GET' && req.url === '/api/nodes') {
      sendJson(res, 200, [])
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  return { server }
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
      // ignore close error
    }
  }

  const fallback = createMockApiServer()
  await tryListen(fallback.server, { port: API_PORT, host: '127.0.0.1' })
  return fallback
}

const waitForHttpReady = async (url, timeoutMs = 60000) => {
  const deadline = Date.now() + timeoutMs
  let lastError

  while (Date.now() < deadline) {
    try {
      const response = await fetch(url, { redirect: 'manual' })
      if (response.status >= 200 && response.status < 500) {
        return
      }
      lastError = new Error(`unexpected status ${response.status}`)
    } catch (error) {
      lastError = error
    }
    await sleep(200)
  }

  throw new Error(`HTTP readiness check failed for ${url}: ${lastError?.message || 'timeout'}`)
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

const stopChild = async (child) => {
  if (!child || child.killed) return
  try {
    child.kill('SIGTERM')
  } catch {
    // ignore kill error
  }
  await waitForProcessExit(child)
}

const startVite = () => {
  const viteBin = path.join(FRONTEND_ROOT, 'node_modules', 'vite', 'bin', 'vite.js')
  const logs = []
  const child = spawn(
    process.execPath,
    [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)],
    {
      cwd: FRONTEND_ROOT,
      env: {
        ...process.env,
        E2E_API_PORT: String(API_PORT),
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    }
  )

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

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const isIgnorableConsoleError = (text) =>
  text.includes('Support for defaultProps will be removed from memo components') ||
  text.includes('[antd: Modal] `destroyOnClose` is deprecated') ||
  text.includes("[antd: message] Static function can not consume context like dynamic theme") ||
  text.includes('Failed to load resource: the server responded with a status of 404 (Not Found)')

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
    const navigations = []
    const consoleErrors = []

    page.on('framenavigated', (frame) => {
      if (frame === page.mainFrame()) {
        navigations.push(frame.url())
      }
    })
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        const text = msg.text()
        if (!isIgnorableConsoleError(text)) {
          consoleErrors.push(text)
        }
      }
    })
    page.on('pageerror', (err) => {
      consoleErrors.push(`pageerror:${err.message}`)
    })

    await page.evaluateOnNewDocument(() => {
      localStorage.setItem('token', 'version-refresh-token')
    })

    await page.goto(FRONTEND_URL, { waitUntil: 'domcontentloaded' })

    const waitForVersionParam = async () => {
      const deadline = Date.now() + 15000
      while (Date.now() < deadline) {
        const url = page.url()
        try {
          const params = new URLSearchParams(new URL(url).search)
          if (params.get('v') === SERVER_VERSION) {
            return url
          }
        } catch {
          // ignore parse errors during navigation
        }
        await sleep(200)
      }
      throw new Error(`timeout waiting for v=${SERVER_VERSION}, current=${page.url()}`)
    }

    const finalUrl = await waitForVersionParam()
    const finalParams = new URLSearchParams(new URL(finalUrl).search)
    assert(finalParams.get('v') === SERVER_VERSION, `expected version query param in URL, got ${finalUrl}`)
    assert(
      navigations.filter((url) => url.includes(`v=${encodeURIComponent(SERVER_VERSION)}`)).length >= 1,
      `expected at least one navigation to cache-busted URL, got ${JSON.stringify(navigations)}`
    )
    assert(consoleErrors.length === 0, `browser console errors: ${consoleErrors.join(' | ')}`)

    console.log(
      JSON.stringify({
        success: true,
        navigations,
        finalUrl,
      })
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    throw new Error(`Version refresh regression failed: ${error.message}\nVite logs:\n${viteLogs}`)
  } finally {
    if (browser) {
      try {
        await browser.close()
      } catch {
        // ignore browser close errors
      }
    }
    try {
      mockApi.server.close()
    } catch {
      // ignore close errors
    }
    await stopChild(vite.child)
  }
}

run().catch((error) => {
  console.error(error.stack || error.message)
  process.exit(1)
})
