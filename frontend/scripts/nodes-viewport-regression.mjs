import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30000)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5173)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const SCRIPTS_DIR = path.dirname(SCRIPT_PATH)
const FRONTEND_ROOT = path.resolve(SCRIPTS_DIR, '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)
const FRONTEND_BUILD_VERSION = FRONTEND_PACKAGE.version

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

const createMockNodes = (count) =>
  Array.from({ length: count }, (_, index) => {
    const id = index + 1
    return {
      id,
      name: `node-${id}`,
      type: 'direct',
      config: '{}',
      inbound_port: 30000 + id,
      username: `u${id}`,
      password: `p${id}`,
      sort_order: index,
      node_ip: '',
      location: '',
      country_code: '',
      latency: 0,
      enabled: true,
      remark: '',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
    }
  })

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
  const nodes = createMockNodes(220)

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJson(res, 200, { version: FRONTEND_BUILD_VERSION })
      return
    }

    if (req.method === 'GET' && req.url === '/api/nodes') {
      sendJson(res, 200, nodes)
      return
    }

    sendJson(res, 404, { error: 'not_found' })
  })

  return { server }
}

const tryListen = (server, { port, host, ipv6Only }) =>
  new Promise((resolve, reject) => {
    const cleanup = () => {
      server.off('listening', onListening)
      server.off('error', onError)
    }
    const onListening = () => {
      cleanup()
      resolve()
    }
    const onError = (err) => {
      cleanup()
      reject(err)
    }
    server.once('listening', onListening)
    server.once('error', onError)
    server.listen({ port, host, ipv6Only })
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

const startVite = (frontendRoot) => {
  const viteBin = path.join(frontendRoot, 'node_modules', 'vite', 'bin', 'vite.js')
  const logs = []
  const child = spawn(process.execPath, [viteBin, '--host', '127.0.0.1', '--port', String(FRONTEND_PORT)], {
    cwd: frontendRoot,
    env: { ...process.env },
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

const stopProcess = async (child) => {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    sleep(3000),
  ])
  if (child.exitCode === null) {
    child.kill('SIGKILL')
    await new Promise((resolve) => child.once('exit', resolve))
  }
}

const assert = (condition, message) => {
  if (!condition) throw new Error(message)
}

const run = async () => {
  const mockApi = await startMockApi()
  await waitForHttpReady(`http://localhost:${API_PORT}/api/version`, 10000)
  const vite = startVite(FRONTEND_ROOT)
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
	    })
	    await page.reload({ waitUntil: 'networkidle2' })
	    await page.waitForSelector('tbody.ant-table-tbody tr[data-row-key="1"], .ant-table-row[data-row-key="1"]', { timeout: 30000 })
	    await page.waitForSelector('.dashboard-toolbar', { timeout: 30000 })
	    await page.waitForFunction(() => {
	      const root = document.querySelector('[data-testid="nodes-table-container"]')
	      if (!root) return false
	      const resolve = () => {
	        const virtualHolder = root.querySelector('.ant-table-tbody-virtual-holder')
	        if (virtualHolder) return virtualHolder
	        const body = root.querySelector('.ant-table-body')
	        if (body) return body
	        const content = root.querySelector('.ant-table-content')
	        if (content && content.scrollHeight > content.clientHeight) return content
	        const container = root.querySelector('.ant-table-container')
        if (container && container.scrollHeight > container.clientHeight) return container
        const divs = root.querySelectorAll('div')
        for (const el of divs) {
          if (!el || el.scrollHeight <= el.clientHeight) continue
          try {
            const style = window.getComputedStyle(el)
            const overflowY = style?.overflowY
            if (overflowY === 'auto' || overflowY === 'scroll' || overflowY === 'overlay') {
              return el
            }
          } catch {
            // ignore and continue
          }
        }
        return container || content || null
      }
      return Boolean(resolve())
    }, { timeout: 30000 })

	    const before = await page.evaluate(() => {
	      const toolbar = document.querySelector('.dashboard-toolbar')
	      const root = document.querySelector('[data-testid="nodes-table-container"]')
	      const resolve = () => {
	        const virtualHolder = root?.querySelector?.('.ant-table-tbody-virtual-holder')
	        if (virtualHolder) return virtualHolder
	        const body = root?.querySelector?.('.ant-table-body')
	        if (body) return body
	        const content = root?.querySelector?.('.ant-table-content')
	        if (content && content.scrollHeight > content.clientHeight) return content
	        const container = root?.querySelector?.('.ant-table-container')
        if (container && container.scrollHeight > container.clientHeight) return container
        const divs = root?.querySelectorAll?.('div') || []
        for (const el of divs) {
          if (!el || el.scrollHeight <= el.clientHeight) continue
          try {
            const style = window.getComputedStyle(el)
            const overflowY = style?.overflowY
            if (overflowY === 'auto' || overflowY === 'scroll' || overflowY === 'overlay') {
              return el
            }
          } catch {
            // ignore and continue
          }
        }
        return container || content || null
      }
      const tableBody = resolve()
      const toolbarRect = toolbar?.getBoundingClientRect?.()
      return {
        windowScrollY: window.scrollY,
        toolbarTop: toolbarRect?.top ?? null,
        toolbarBottom: toolbarRect?.bottom ?? null,
        tableBodyClientHeight: tableBody?.clientHeight ?? null,
        tableBodyScrollHeight: tableBody?.scrollHeight ?? null,
        tableBodyScrollTop: tableBody?.scrollTop ?? null,
      }
    })

    const windowScrollAfterAttempt = await page.evaluate(() => {
      window.scrollTo(0, 999999)
      return window.scrollY
    })

	    let scrollProbe = null
	    for (let attempt = 0; attempt < 25; attempt += 1) {
	      await page.evaluate(() => {
	        const root = document.querySelector('[data-testid="nodes-table-container"]')
	        const resolve = () => {
	          const virtualHolder = root?.querySelector?.('.ant-table-tbody-virtual-holder')
	          if (virtualHolder) return virtualHolder
	          const body = root?.querySelector?.('.ant-table-body')
	          if (body) return body
	          const content = root?.querySelector?.('.ant-table-content')
	          if (content && content.scrollHeight > content.clientHeight) return content
	          const container = root?.querySelector?.('.ant-table-container')
          if (container && container.scrollHeight > container.clientHeight) return container
          const divs = root?.querySelectorAll?.('div') || []
          for (const el of divs) {
            if (!el || el.scrollHeight <= el.clientHeight) continue
            try {
              const style = window.getComputedStyle(el)
              const overflowY = style?.overflowY
              if (
                overflowY === 'auto' ||
                overflowY === 'scroll' ||
                overflowY === 'overlay'
              ) {
                return el
              }
            } catch {
              // ignore and continue
            }
          }
          return container || content || null
        }
        const tableBody = resolve()
        if (!tableBody) return
        tableBody.scrollTop = Math.max(0, tableBody.scrollHeight - tableBody.clientHeight)
        tableBody.dispatchEvent(new Event('scroll', { bubbles: true }))
      })
      await sleep(250)

      scrollProbe = await page.evaluate(() => {
        const renderedRows = Array.from(
          document.querySelectorAll('tbody.ant-table-tbody tr[data-row-key], .ant-table-row[data-row-key]')
        )
        const renderedKeys = renderedRows
          .map((row) => Number(row.getAttribute('data-row-key')))
          .filter((n) => Number.isFinite(n))
        const maxRenderedRowKey = renderedKeys.length > 0 ? Math.max(...renderedKeys) : null
        const lastKeys = renderedKeys
          .slice()
          .sort((a, b) => a - b)
          .slice(-6)
        return { maxRenderedRowKey, lastKeys }
      })

      if (scrollProbe?.maxRenderedRowKey === 220) {
        break
      }
    }
    await sleep(600)

	    const after = await page.evaluate(() => {
	      const toolbar = document.querySelector('.dashboard-toolbar')
	      const root = document.querySelector('[data-testid="nodes-table-container"]')
	      const resolve = () => {
	        const virtualHolder = root?.querySelector?.('.ant-table-tbody-virtual-holder')
	        if (virtualHolder) return virtualHolder
	        const body = root?.querySelector?.('.ant-table-body')
	        if (body) return body
	        const content = root?.querySelector?.('.ant-table-content')
	        if (content && content.scrollHeight > content.clientHeight) return content
	        const container = root?.querySelector?.('.ant-table-container')
        if (container && container.scrollHeight > container.clientHeight) return container
        const divs = root?.querySelectorAll?.('div') || []
        for (const el of divs) {
          if (!el || el.scrollHeight <= el.clientHeight) continue
          try {
            const style = window.getComputedStyle(el)
            const overflowY = style?.overflowY
            if (overflowY === 'auto' || overflowY === 'scroll' || overflowY === 'overlay') {
              return el
            }
          } catch {
            // ignore and continue
          }
        }
        return container || content || null
      }
      const tableBody = resolve()
      const renderedRows = Array.from(
        document.querySelectorAll('tbody.ant-table-tbody tr[data-row-key], .ant-table-row[data-row-key]')
      )
      const renderedKeys = renderedRows
        .map((row) => Number(row.getAttribute('data-row-key')))
        .filter((n) => Number.isFinite(n))
      const maxRenderedRowKey = renderedKeys.length > 0 ? Math.max(...renderedKeys) : null
      const row220 = document.querySelector('tbody.ant-table-tbody tr[data-row-key="220"], .ant-table-row[data-row-key="220"]')
      const lastKeys = renderedKeys
        .slice()
        .sort((a, b) => a - b)
        .slice(-6)
      const toolbarRect = toolbar?.getBoundingClientRect?.()
      return {
        windowScrollY: window.scrollY,
        toolbarTop: toolbarRect?.top ?? null,
        toolbarBottom: toolbarRect?.bottom ?? null,
        tableBodyScrollTop: tableBody?.scrollTop ?? null,
        tableBodyScrollHeight: tableBody?.scrollHeight ?? null,
        tableBodyClientHeight: tableBody?.clientHeight ?? null,
        maxRenderedRowKey,
        hasRow220: Boolean(row220),
        lastKeys,
      }
    })

    const dragHandleErrors = consoleErrors.filter((line) => line.includes('Unable to find drag handle'))

    assert(before.tableBodyScrollHeight > before.tableBodyClientHeight, 'Expected nodes table body to be scrollable')
    assert(windowScrollAfterAttempt === 0, `Expected window scroll to stay at 0, got ${windowScrollAfterAttempt}`)
    assert(after.windowScrollY === 0, `Expected window scroll to stay at 0 after scrolling nodes, got ${after.windowScrollY}`)
    assert(after.tableBodyScrollTop > 0, `Expected table body scrollTop > 0, got ${after.tableBodyScrollTop}`)
    assert(
      after.hasRow220,
      `Expected row 220 to be rendered after scrolling, lastKeys=${JSON.stringify(after.lastKeys)} probe=${JSON.stringify(scrollProbe)}`
    )
    assert(
      before.toolbarTop !== null && after.toolbarTop !== null && Math.abs(before.toolbarTop - after.toolbarTop) <= 1,
      'Expected toolbar position to remain stable while scrolling nodes'
    )
    assert(dragHandleErrors.length === 0, `Drag handle error detected: ${dragHandleErrors.join(' | ')}`)

    console.log(
      JSON.stringify(
        {
          success: true,
          before,
          windowScrollAfterAttempt,
          after,
        },
        null,
        2
      )
    )
  } catch (error) {
    console.error(
      JSON.stringify(
        {
          success: false,
          error: error?.message || String(error),
          logs: vite?.getLogs?.() || '',
        },
        null,
        2
      )
    )
    process.exitCode = 1
  } finally {
    await stopProcess(vite?.child)
    try {
      mockApi?.server?.close?.()
    } catch {
      // ignore
    }
    if (browser) {
      try {
        await browser.close()
      } catch {
        // ignore
      }
    }
  }
}

run()
