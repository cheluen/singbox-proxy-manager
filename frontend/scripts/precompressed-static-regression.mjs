import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { accessSync, constants, copyFileSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { brotliDecompressSync, gunzipSync } from 'node:zlib'
import puppeteer from 'puppeteer-core'
import { findBrowserExecutable, stopProcess } from './test-harness.mjs'

const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const binary = path.resolve(process.env.SBPM_TEST_BINARY || path.join(frontend, '..', 'sb-proxy'))
const version = readFileSync(path.join(frontend, '..', 'internal/version/version.txt'), 'utf8').trim()
const temporary = mkdtempSync(path.join(process.platform === 'linux' ? '/tmp' : os.tmpdir(), 'sbpm-pc-'))
const password = randomUUID()
const results = { httpChecks: 0, modes: [], browser: null }
const delay = () => new Promise((resolve) => setTimeout(resolve, 100))
const freePort = () => new Promise((resolve, reject) => {
  const server = net.createServer().on('error', reject)
  server.listen(0, '127.0.0.1', () => { const port = server.address().port; server.close(() => resolve(port)) })
})
const request = (origin, endpoint, headers = {}, method = 'GET') => new Promise((resolve, reject) => {
  const outgoing = http.request(origin + endpoint, { method, headers }, (response) => {
    const chunks = []
    response.on('data', (chunk) => chunks.push(chunk)).on('error', reject)
    response.on('end', () => { results.httpChecks += 1; resolve({ status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks) }) })
  })
  outgoing.on('error', reject).setTimeout(15000, () => outgoing.destroy(new Error('HTTP 请求超时')))
  outgoing.end()
})
const decoded = ({ headers, body }) => headers['content-encoding'] === 'br' ? brotliDecompressSync(body) : headers['content-encoding'] === 'gzip' ? gunzipSync(body) : body

function startProcess(command, environment) {
  const child = spawn(command, [], { cwd: temporary, env: environment, stdio: ['ignore', 'pipe', 'pipe'] })
  const managed = { child, log: '', finished: false }
  managed.started = new Promise((resolve, reject) => { child.once('spawn', resolve); child.on('error', reject) })
  managed.closed = new Promise((resolve) => child.once('close', () => { managed.finished = true; resolve() }))
  child.stdout.on('data', (chunk) => { managed.log += chunk })
  child.stderr.on('data', (chunk) => { managed.log += chunk })
  return managed
}
async function checkBrowser(origin) {
  let browser, devtoolsEnvironment
  try {
    browser = await puppeteer.launch({ executablePath: findBrowserExecutable(), headless: true, userDataDir: path.join(temporary, 'chromium'), env: { ...process.env, TMPDIR: temporary }, args: ['--no-sandbox', '--disable-dev-shm-usage', '--no-proxy-server'] })
    const page = (await browser.pages())[0]
    const errors = [], responses = []
    page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()) })
    page.on('pageerror', (error) => errors.push(error.message))
    const session = await page.createCDPSession()
    await session.send('Network.enable')
    await session.send('Network.setCacheDisabled', { cacheDisabled: true })
    session.on('Network.responseReceived', ({ response }) => responses.push(response))
    await page.goto(origin, { waitUntil: 'networkidle0' })
    await page.waitForSelector('[data-testid="login-form"]', { visible: true })
    await page.type('#sb-login-secret', password)
    await Promise.all([page.waitForResponse((response) => response.url().endsWith('/api/nodes') && response.status() === 200), page.click('[data-testid="login-submit"]')])
    await page.waitForFunction(() => !document.querySelector('[data-testid="login-form"]'))
    await page.waitForNetworkIdle()
    const assets = responses.filter((response) => /\.(js|css|svg)$/.test(response.url))
    for (const extension of ['js', 'css', 'svg']) assert.ok(assets.some((response) => response.url.endsWith(`.${extension}`)))
    for (const response of assets) {
      const headers = Object.fromEntries(Object.entries(response.headers).map(([name, value]) => [name.toLowerCase(), value]))
      assert.equal(response.status, 200)
      assert.equal(headers['content-encoding'], 'br', response.url)
    }
    if (process.env.SBPM_DEVTOOLS_CLI) {
      devtoolsEnvironment = { ...process.env, HOME: temporary, XDG_CONFIG_HOME: temporary, XDG_CACHE_HOME: temporary }
      execFileSync(process.env.SBPM_DEVTOOLS_CLI, ['start', '--browserUrl', `http://127.0.0.1:${new URL(browser.wsEndpoint()).port}`, '--no-performance-crux', '--no-usage-statistics'], { env: devtoolsEnvironment, timeout: 30000 })
      assert.match(execFileSync(process.env.SBPM_DEVTOOLS_CLI, ['take_snapshot', '1'], { env: devtoolsEnvironment, encoding: 'utf8', timeout: 30000 }), /SingBox/)
    }
    const token = await page.evaluate(() => localStorage.getItem('token'))
    assert.ok(token)
    const headers = { Authorization: `Bearer ${token}` }
    assert.equal((await request(origin, '/api/nodes', headers)).status, 200)
    assert.equal((await request(origin, '/api/logout', headers, 'POST')).status, 200)
    assert.equal((await request(origin, '/api/nodes', headers)).status, 401)
    assert.deepEqual(errors, [])
    results.browser = { compressedAssets: assets.length, errors: errors.length, loginNodesLogout: true, devtoolsSnapshot: Boolean(devtoolsEnvironment) }
  } finally {
    try { if (devtoolsEnvironment) execFileSync(process.env.SBPM_DEVTOOLS_CLI, ['stop'], { env: devtoolsEnvironment, timeout: 15000 }) }
    finally { if (browser) await browser.close() }
  }
}

async function verifyMode(threshold) {
  const origin = `http://127.0.0.1:${await freePort()}`
  const environment = { ...process.env, CONFIG_DIR: path.join(temporary, `config-${threshold}`), PORT: new URL(origin).port, BIND_ADDRESS: '127.0.0.1', ADMIN_PASSWORD: password, SBPM_UPDATE_CHECK_DISABLED: 'true', GOMAXPROCS: '2' }
  for (const name of ['DATABASE_URL', 'POSTGRES_DATABASE_URL', 'POSTGRES_URL', 'PGSQL_DATABASE_URL', 'PGSQL', 'MYSQL_DATABASE_URL', 'MYSQL', 'TURSO_DATABASE_URL', 'TURSO_AUTH_TOKEN', 'MYSQL_DATABASE_NAME', 'SBPM_NODES_VIRTUAL_THRESHOLD', 'SBPM_BATCH_CHECK_IP_CONCURRENCY']) environment[name] = ''
  if (threshold === 42) environment.SBPM_NODES_VIRTUAL_THRESHOLD = '42'
  let server
  try {
    server = startProcess(path.join(temporary, 'sbpm'), environment)
    await server.started
    let ready = false
    for (let attempt = 0; attempt < 100 && !ready; attempt += 1) {
      assert.ok(!server.finished, `服务提前退出：${server.log}`)
      try { ready = (await request(origin, '/api/version')).status === 200 } catch { await delay() }
      if (!ready) await delay()
    }
    assert.ok(ready, `服务未就绪：${server.log}`)
    assert.ok(statSync(path.join(environment.CONFIG_DIR, 'proxy.db')).isFile())
    assert.match(server.log, /Serving frontend assets from embedded:frontend\/dist/)
    if (threshold === 120) {
      const files = readdirSync(path.join(frontend, 'dist/assets'))
      const endpoints = ['js', 'css'].map((extension) => `/assets/${files.find((file) => file.endsWith(`.${extension}`))}`).concat(['/logo.svg', '/brand-mark.svg'])
      for (const endpoint of endpoints) {
        const original = readFileSync(path.join(frontend, 'dist', endpoint))
        for (const encoding of ['br', 'gzip', '']) {
          const response = await request(origin, endpoint, { 'Accept-Encoding': encoding })
          assert.equal(response.status, 200)
          assert.equal(response.headers['content-encoding'] || '', encoding)
          assert.match(response.headers['content-type'], endpoint.endsWith('.js') ? /javascript/ : endpoint.endsWith('.css') ? /text\/css/ : /image\/svg\+xml/)
          assert.match(response.headers.vary, /Accept-Encoding/)
          assert.equal(response.headers['x-app-version'], version)
          assert.equal(Number(response.headers['content-length']), response.body.length)
          assert.deepEqual(decoded(response), original)
        }
      }
      const head = await request(origin, endpoints[0], { 'Accept-Encoding': 'br', Range: 'bytes=0-9' }, 'HEAD')
      assert.equal(head.status, 200)
      assert.equal(head.headers['content-encoding'], 'br')
      assert.equal(Number(head.headers['content-length']), readFileSync(path.join(frontend, 'dist', `${endpoints[0]}.br`)).length)
      assert.equal(head.body.length, 0)
      const range = await request(origin, endpoints[0], { 'Accept-Encoding': 'br', Range: 'bytes=0-9' })
      assert.equal(range.status, 206)
      assert.equal(range.headers['content-encoding'], undefined)
      assert.deepEqual(range.body, readFileSync(path.join(frontend, 'dist', endpoints[0])).subarray(0, 10))
      const font = await request(origin, `/assets/${files.find((file) => file.endsWith('.woff2'))}`, { 'Accept-Encoding': 'br, gzip' })
      assert.equal(font.status, 200)
      assert.equal(font.headers['content-encoding'], undefined)
      await checkBrowser(origin)
    }
    const plain = await request(origin, '/')
    for (const encoding of ['br', 'gzip', '']) {
      const response = await request(origin, '/', { 'Accept-Encoding': encoding })
      assert.equal(response.status, 200)
      assert.deepEqual(decoded(response), plain.body)
      assert.match(decoded(response).toString(), new RegExp(`<meta name="sbpm-nodes-virtual-threshold" content="${threshold}" />`))
      assert.match(decoded(response).toString(), /<meta name="sbpm-batch-check-ip-concurrency" content="3" \/>/)
      assert.equal((await request(origin, '/', { 'Accept-Encoding': encoding, 'If-None-Match': response.headers.etag })).status, 304)
    }
    results.modes.push({ threshold, embedded: true, isolatedSQLite: true })
  } finally {
    await stopProcess(server?.child, { graceMs: 12000 })
    await server?.closed
  }
}
try {
  assert.ok(statSync(binary).isFile(), '测试二进制必须是普通文件')
  accessSync(binary, constants.R_OK | constants.X_OK)
  copyFileSync(binary, path.join(temporary, 'sbpm'))
  await verifyMode(120)
  await verifyMode(42)
  process.stdout.write(`${JSON.stringify({ success: true, ...results })}\n`)
} finally { rmSync(temporary, { recursive: true, force: true }) }
