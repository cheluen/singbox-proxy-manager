import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30014)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5177)
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
  let lastBatchDeletePayload = null
  let nodes = [
    {
      id: 1,
      name: 'node-1',
      type: 'vmess',
      config: '{"server":"example.com","tls":true,"server_port":443}',
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
      name: 'node-2-dup-different-json-order',
      type: 'vmess',
      config: '{"tls":true,"server_port":443,"server":"example.com"}',
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
    {
      id: 3,
      name: 'node-3',
      type: 'vmess',
      config: '{"server":"example.com","tls":false,"server_port":443}',
      inbound_port: 30003,
      username: 'u3',
      password: 'p3',
      sort_order: 2,
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

  const reorder = () => {
    nodes = [...nodes]
      .sort((a, b) => a.sort_order - b.sort_order)
      .map((node, index) => ({
        ...node,
        sort_order: index,
        updated_at: new Date().toISOString(),
      }))
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

    if (req.method === 'POST' && req.url === '/api/nodes/batch-delete') {
      let body = ''
      req.on('data', (chunk) => {
        body += chunk
      })
      req.on('end', () => {
        const payload = JSON.parse(body || '{}')
        lastBatchDeletePayload = payload
        const ids = Array.isArray(payload.ids) ? payload.ids.map((id) => Number(id)) : []
        nodes = nodes.filter((node) => !ids.includes(node.id))
        reorder()
        sendJson(res, 200, { message: 'nodes deleted', deleted_count: ids.length })
      })
      return
    }

    if (req.method === 'GET' && req.url === '/api/__state') {
      sendJson(res, 200, { nodes, lastBatchDeletePayload })
      return
    }

    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }

    sendJson(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  const getState = () => ({ nodes, lastBatchDeletePayload })
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
  text.includes("[antd: Modal] Static function can not consume context like dynamic theme") ||
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

const getRowNames = async (page) =>
  page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll('tbody.ant-table-tbody tr[data-row-key]'))
    return rows.map((row) => {
      const cells = Array.from(row.querySelectorAll('td'))
      const cell = cells[3]
      return (cell?.textContent || '').trim()
    })
  })

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
    const node3Checkbox = 'tbody.ant-table-tbody tr[data-row-key="3"] td:nth-child(3) input[type="checkbox"]'
    await page.click(node1Checkbox)
    await page.click(node2Checkbox)
    await page.click(node3Checkbox)
    await sleep(300)

    const orderBefore = await getRowNames(page)
    await clickButtonByText(page, '出站去重')
    await page.waitForSelector('.ant-modal-confirm', { timeout: 5000 })
    await page.waitForSelector('.ant-modal-confirm .ant-btn-primary', { timeout: 5000 })
    await page.click('.ant-modal-confirm .ant-btn-primary')

    await page.waitForFunction(
      () => !document.querySelector('tbody.ant-table-tbody tr[data-row-key="2"]'),
      { timeout: 10000 }
    )

    const state = mockApi.getState()
    const orderAfter = await getRowNames(page)

    assert(
      JSON.stringify(orderBefore) === JSON.stringify(['node-1', 'node-2-dup-different-json-order', 'node-3']),
      `Unexpected initial order: ${JSON.stringify(orderBefore)}`
    )
    assert(Boolean(state.lastBatchDeletePayload), 'Expected batch-delete request to be sent')
    assert(
      JSON.stringify(state.lastBatchDeletePayload.ids) === JSON.stringify([2]),
      `Unexpected batch-delete payload: ${JSON.stringify(state.lastBatchDeletePayload)}`
    )
    assert(
      JSON.stringify(orderAfter) === JSON.stringify(['node-1', 'node-3']),
      `Unexpected order after dedup: ${JSON.stringify(orderAfter)}`
    )
    assert(consoleErrors.length === 0, `Console errors: ${consoleErrors.join(' | ')}`)

    console.log(
      JSON.stringify(
        {
          success: true,
          orderBefore,
          orderAfter,
          batchDeletePayload: state.lastBatchDeletePayload,
        },
        null,
        2
      )
    )
  } catch (error) {
    const viteLogs = vite.getLogs()
    console.error('E2E outbound dedup regression failed.')
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
