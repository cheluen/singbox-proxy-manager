import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30013)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5176)
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
  const checkCalls = []
  const checkResponses = []
  const nodes = [
    {
      id: 1,
      name: 'node-1',
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
      enabled: true,
      remark: '',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    },
    {
      id: 2,
      name: 'node-2',
      type: 'direct',
      config: '{}',
      inbound_port: 30002,
      username: 'u2',
      password: 'p2',
      sort_order: 1,
      node_ip: '',
      location: '',
      country_code: '',
      latency: 0,
      enabled: true,
      remark: '',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    },
  ]

  const updateNode = (id, updates) => {
    const index = nodes.findIndex((node) => node.id === id)
    if (index === -1) return
    nodes[index] = {
      ...nodes[index],
      ...updates,
      updated_at: new Date().toISOString(),
    }
  }

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJson(res, 200, { version: FRONTEND_BUILD_VERSION })
      return
    }

    if (req.method === 'GET' && req.url === '/api/nodes') {
      const sorted = [...nodes].sort((a, b) => a.sort_order - b.sort_order)
      sendJson(res, 200, sorted)
      return
    }

    const matchCheckIP = req.url?.match(/^\/api\/nodes\/(\d+)\/check-ip$/)
    if (req.method === 'GET' && matchCheckIP) {
      const id = Number(matchCheckIP[1] || 0)
      checkCalls.push({ id, at: Date.now() })

      const delayMs = id === 1 ? 200 : 1800
      const ipInfo =
        id === 1
          ? {
              ip: '1.1.1.1',
              location: 'Los Angeles, United States',
              country_code: 'US',
              latency: 123,
              transport: 'http',
            }
          : {
              ip: '2.2.2.2',
              location: 'Tokyo, Japan',
              country_code: 'JP',
              latency: 456,
              transport: 'http',
            }

      setTimeout(() => {
        updateNode(id, {
          node_ip: ipInfo.ip,
          location: ipInfo.location,
          country_code: ipInfo.country_code,
          latency: ipInfo.latency,
        })
        checkResponses.push({ id, at: Date.now() })
        sendJson(res, 200, ipInfo)
      }, delayMs)
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { nodes, checkCalls, checkResponses })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ nodes, checkCalls, checkResponses })
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
  const child = spawn(
    process.execPath,
    [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)],
    {
      cwd: FRONTEND_ROOT,
      env: { ...process.env, E2E_API_PORT: String(API_PORT) },
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
  text.includes("[antd: notification] Static function can not consume context like dynamic theme") ||
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
    await sleep(250)
  }
  throw new Error(`Button not found: ${text}`)
}

const getCellText = async (page, selector) =>
  page.evaluate((sel) => {
    const el = document.querySelector(sel)
    return (el?.textContent || '').trim()
  }, selector)

const run = async () => {
  const mockApi = await startMockApi()
  const vite = startVite()
  let browser

  try {
    await waitForHttpReady(FRONTEND_URL, 30000)
    const browserPath = getBrowserExecutablePath()
    assert(browserPath, 'No Chrome/Chromium executable available (set PUPPETEER_EXECUTABLE_PATH)')

    browser = await puppeteer.launch({
      executablePath: browserPath,
      headless: 'new',
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })

    const page = await browser.newPage()
    const consoleErrors = []
    page.on('console', (msg) => {
      const type = msg.type()
      if (type === 'error') {
        const text = msg.text()
        if (!isIgnorableConsoleError(text)) {
          consoleErrors.push(text)
        }
      }
    })
    page.on('pageerror', (err) => {
      consoleErrors.push(`pageerror:${err.message}`)
    })

    await page.goto(FRONTEND_URL, { waitUntil: 'networkidle2' })
    await page.evaluate(() => {
      localStorage.setItem('token', 'e2e-token')
    })
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 30000 })

    const node1Checkbox = 'tbody.ant-table-tbody tr[data-row-key="1"] td:nth-child(3) input[type="checkbox"]'
    const node2Checkbox = 'tbody.ant-table-tbody tr[data-row-key="2"] td:nth-child(3) input[type="checkbox"]'
    await page.click(node1Checkbox)
    await page.click(node2Checkbox)
    await sleep(300)

    await clickButtonByText(page, '批量测IP')

    const node1IPCell = 'tbody.ant-table-tbody tr[data-row-key="1"] td:nth-child(12)'
    const node2IPCell = 'tbody.ant-table-tbody tr[data-row-key="2"] td:nth-child(12)'

    await page.waitForFunction(
      (sel, expected) => (document.querySelector(sel)?.textContent || '').includes(expected),
      { timeout: 5000 },
      node1IPCell,
      '1.1.1.1'
    )

    const node2Before = await getCellText(page, node2IPCell)
    assert(node2Before === '-', `Expected node 2 to be pending when node 1 completes, got: ${node2Before}`)

    await page.waitForFunction(
      (sel, expected) => (document.querySelector(sel)?.textContent || '').includes(expected),
      { timeout: 8000 },
      node2IPCell,
      '2.2.2.2'
    )

    const node1After = await getCellText(page, node1IPCell)
    const node2After = await getCellText(page, node2IPCell)

    assert(node1After === '1.1.1.1', `Unexpected node 1 IP cell text: ${node1After}`)
    assert(node2After === '2.2.2.2', `Unexpected node 2 IP cell text: ${node2After}`)
    assert(consoleErrors.length === 0, `Console errors: ${consoleErrors.join(' | ')}`)

    const state = mockApi.getState()
    const responseOrder = state.checkResponses.map((item) => item.id)
    assert(
      JSON.stringify(responseOrder) === JSON.stringify([1, 2]),
      `Unexpected response order: ${JSON.stringify(responseOrder)}`
    )

    console.log(
      JSON.stringify(
        {
          success: true,
          node1IP: node1After,
          node2IP: node2After,
          responseOrder,
        },
        null,
        2
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E batch check IP live update regression failed.')
    console.error(error)
    if (viteLogs) {
      console.error('Vite logs:')
      console.error(viteLogs)
    }
    process.exitCode = 1
  } finally {
    if (browser) {
      await browser.close()
    }
    await stopProcess(vite.child)
    await new Promise((resolve) => mockApi.server.close(resolve))
  }
}

await run()
