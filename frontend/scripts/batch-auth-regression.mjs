import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30021)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5185)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

const initialNode = {
  id: 1,
  name: 'batch-auth-node',
  remark: '',
  type: 'direct',
  config: '{}',
  inbound_port: 30001,
  inbound_port_pinned: false,
  username: 'old-user',
  password: 'old-password',
  tcp_reuse_enabled: true,
  upstream_mode: 'none',
  upstream_type: '',
  upstream_config: '',
  sort_order: 0,
  node_ip: '',
  location: '',
  country_code: '',
  latency: 0,
  enabled: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

const sendJson = (response, statusCode, payload) => {
  if (response.destroyed || response.writableEnded) return
  const body = JSON.stringify(payload)
  response.writeHead(statusCode, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
  })
  response.end(body)
}

const readJsonBody = (request) =>
  new Promise((resolve, reject) => {
    let body = ''
    request.on('data', (chunk) => {
      body += chunk
    })
    request.on('end', () => {
      try {
        resolve(JSON.parse(body || '{}'))
      } catch (error) {
        reject(error)
      }
    })
    request.on('error', reject)
  })

const createMockApi = () => {
  let node = { ...initialNode }
  let batchAuthAttempts = 0
  const batchAuthPayloads = []

  const server = http.createServer(async (request, response) => {
    if (request.method === 'GET' && request.url === '/api/version') {
      sendJson(response, 200, { version: FRONTEND_PACKAGE.version })
      return
    }
    if (request.method === 'GET' && request.url === '/api/settings') {
      sendJson(response, 200, { start_port: 30001, preserve_inbound_ports: false })
      return
    }
    if (request.method === 'GET' && request.url === '/api/runtime/status') {
      sendJson(response, 200, { state: 'running', running: true, degraded: false })
      return
    }
    if (request.method === 'GET' && request.url === '/api/nodes') {
      sendJson(response, 200, [node])
      return
    }
    if (request.method === 'POST' && request.url === '/api/nodes/batch-auth') {
      try {
        const payload = await readJsonBody(request)
        batchAuthAttempts += 1
        batchAuthPayloads.push(payload)
        await sleep(250)

        if (batchAuthAttempts === 1) {
          sendJson(response, 400, { error: 'credentials rejected by policy' })
          return
        }
        if (batchAuthAttempts === 2) {
          sendJson(response, 404, { error: 'node 1 not found' })
          return
        }

        node = {
          ...node,
          username: String(payload.username || ''),
          password: String(payload.password || ''),
          updated_at: new Date().toISOString(),
        }
        sendJson(response, 200, { message: 'authentication updated' })
      } catch {
        sendJson(response, 400, { error: 'invalid request' })
      }
      return
    }
    sendJson(response, 404, { error: 'not found', method: request.method, url: request.url })
  })

  return {
    server,
    getState: () => ({ node, batchAuthAttempts, batchAuthPayloads }),
  }
}

const listen = (server, options) =>
  new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(options, resolve)
  })

const waitForHttp = async (url, timeout = 60000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.status < 500) return
    } catch {
      // Retry until the child process is accepting requests.
    }
    await sleep(200)
  }
  throw new Error(`timed out waiting for ${url}`)
}

const waitFor = async (predicate, description, timeout = 10000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (predicate()) return
    await sleep(50)
  }
  throw new Error(`timed out waiting for ${description}`)
}

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
      // Continue searching known Chromium paths.
    }
  }
  return undefined
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
        VITE_API_TARGET: `http://127.0.0.1:${API_PORT}`,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    }
  )
  child.stdout.on('data', (chunk) => logs.push(chunk.toString()))
  child.stderr.on('data', (chunk) => logs.push(chunk.toString()))
  return { child, logs: () => logs.join('') }
}

const stopChild = async (child) => {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([new Promise((resolve) => child.once('exit', resolve)), sleep(3000)])
  if (child.exitCode === null) {
    child.kill('SIGKILL')
    await new Promise((resolve) => child.once('exit', resolve))
  }
}

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const isIgnorableConsoleError = (text) =>
  text.includes('Support for defaultProps will be removed from memo components') ||
  text.includes('[antd: message] Static function can not consume context') ||
  (text.includes('Failed to load resource') &&
    (text.includes('status of 400') || text.includes('status of 404')))

const openModalSelector = '.ant-modal-wrap:not([style*="display: none"])'

const getOpenModalValues = (page) =>
  page.evaluate((modalSelector) => {
    const modal = document.querySelector(modalSelector)
    return {
      username: modal?.querySelector('[data-testid="batch-auth-username"]')?.value || '',
      password: modal?.querySelector('[data-testid="batch-auth-password"]')?.value || '',
    }
  }, openModalSelector)

const waitForLatestMessage = (page, expected) =>
  page.waitForFunction(
    (message) => {
      const notices = Array.from(document.querySelectorAll('.ant-message-notice-content'))
      return (notices.at(-1)?.textContent || '').includes(message)
    },
    { timeout: 10000 },
    expected
  )

const run = async () => {
  const mockApi = createMockApi()
  await listen(mockApi.server, { host: '127.0.0.1', port: API_PORT })
  const vite = startVite()
  let browser

  try {
    await waitForHttp(FRONTEND_URL)
    browser = await puppeteer.launch({
      headless: true,
      executablePath: browserExecutable(),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })

    const page = await browser.newPage()
    await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 1 })
    const unexpectedConsoleErrors = []
    page.on('console', (message) => {
      if (message.type() !== 'error') return
      const text = message.text()
      if (!isIgnorableConsoleError(text)) {
        unexpectedConsoleErrors.push(text)
      }
    })
    page.on('pageerror', (error) => unexpectedConsoleErrors.push(`pageerror:${error.message}`))

    await page.goto(FRONTEND_URL, { waitUntil: 'networkidle2' })
    await page.evaluate(() => localStorage.setItem('token', 'batch-auth-e2e-token'))
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"]', { timeout: 30000 })

    const selected = await page.evaluate(() => {
      const checkbox = document.querySelector(
        'tbody.ant-table-tbody tr[data-row-key="1"] input[type="checkbox"]'
      )
      if (!checkbox) return false
      checkbox.click()
      return true
    })
    assert(selected, 'node selection checkbox was not found')
    await page.waitForSelector('[data-testid="nodes-batch-auth"]', { timeout: 10000 })
    await page.click('[data-testid="nodes-batch-auth"]')
    await page.waitForSelector(`${openModalSelector} [data-testid="batch-auth-username"]`, {
      visible: true,
      timeout: 10000,
    })

    const username = 'preserved-user'
    const password = 'preserved-password'
    await page.type(`${openModalSelector} [data-testid="batch-auth-username"]`, username)
    await page.type(`${openModalSelector} [data-testid="batch-auth-password"]`, password)

    const submitSelector = `${openModalSelector} .ant-modal-footer .ant-btn-primary`
    await page.click(submitSelector)
    await waitFor(() => mockApi.getState().batchAuthAttempts === 1, 'first batch auth request')
    const loadingAfterSubmit = await page.$eval(submitSelector, (button) =>
      button.classList.contains('ant-btn-loading')
    )
    assert(loadingAfterSubmit, 'batch auth submit button did not enter loading state')
    await waitForLatestMessage(page, 'credentials rejected by policy')

    let values = await getOpenModalValues(page)
    assert(values.username === username, `username was cleared after 400: ${JSON.stringify(values)}`)
    assert(values.password === password, `password was cleared after 400: ${JSON.stringify(values)}`)

    await page.click(submitSelector)
    await waitFor(() => mockApi.getState().batchAuthAttempts === 2, 'second batch auth request')
    await waitForLatestMessage(page, 'node 1 not found')
    values = await getOpenModalValues(page)
    assert(values.username === username, `username was cleared after 404: ${JSON.stringify(values)}`)
    assert(values.password === password, `password was cleared after 404: ${JSON.stringify(values)}`)

    await page.click(submitSelector)
    await waitFor(() => mockApi.getState().batchAuthAttempts === 3, 'successful batch auth request')
    await page.waitForSelector(openModalSelector, { hidden: true, timeout: 10000 })
    await waitFor(
      () => mockApi.getState().node.username === username,
      'updated node credentials'
    )

    const state = mockApi.getState()
    assert(state.batchAuthPayloads.length === 3, 'unexpected batch auth request count')
    assert(
      state.batchAuthPayloads.every(
        (payload) =>
          payload.auth_enabled === true &&
          payload.username === username &&
          payload.password === password &&
          JSON.stringify(payload.node_ids) === '[1]'
      ),
      `batch auth payload changed across retries: ${JSON.stringify(state.batchAuthPayloads)}`
    )
    assert(
      unexpectedConsoleErrors.length === 0,
      `unexpected browser console errors: ${unexpectedConsoleErrors.join(' | ')}`
    )

    console.log(
      JSON.stringify(
        {
          success: true,
          attempts: state.batchAuthAttempts,
          preservedAfter400: true,
          preservedAfter404: true,
          backendErrorsSurfaced: true,
        },
        null,
        2
      )
    )
  } catch (error) {
    console.error('E2E batch authentication regression failed.')
    console.error(error)
    const logs = vite.logs()
    if (logs) {
      console.error('Vite logs:')
      console.error(logs)
    }
    process.exitCode = 1
  } finally {
    if (browser) await browser.close()
    await stopChild(vite.child)
    await new Promise((resolve) => mockApi.server.close(resolve))
  }
}

await run()
