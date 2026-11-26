import { clickControl, clickText, editNode, saveNodeForm, selectOption, setInput, sleep, toggle, waitOverlaysClosed } from './ui-driver.mjs'
import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { isDeepStrictEqual } from 'node:util'
import { fileURLToPath } from 'node:url'
import { launchBrowser, stopProcess, waitForHTTP } from './test-harness.mjs'

const API_PORT = Number(process.env.E2E_API_PORT || 30023)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5186)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)

import { protocolFixtures as fixtures } from './fixtures/protocol-configs.mjs'

const sendJSON = (res, statusCode, payload) => {
  const body = JSON.stringify(payload)
  res.writeHead(statusCode, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(body),
  })
  res.end(body)
}

const readJSONBody = (req) =>
  new Promise((resolve, reject) => {
    let body = ''
    req.on('data', (chunk) => {
      body += chunk
    })
    req.on('end', () => {
      try {
        resolve(JSON.parse(body || '{}'))
      } catch (error) {
        reject(error)
      }
    })
    req.on('error', reject)
  })

const createMockAPI = () => {
  const createdPayloads = []
  const updatedPayloads = []
  const existingNodes = [
    {
      id: 9901,
      name: 'Existing empty Trojan',
      remark: '',
      type: 'trojan',
      config: JSON.stringify({
        server: 'trojan-existing.example.com',
        server_port: 443,
        password: '',
        security: 'tls',
      }),
      inbound_port: 30991,
      username: 'local-inbound-user',
      password: 'local-inbound-secret',
      tcp_reuse_enabled: true,
      sort_order: 0,
      latency: 0,
      enabled: false,
      node_ip: '',
      location: '',
      country_code: '',
      created_at: '2026-08-08T00:00:00Z',
      updated_at: '2026-08-08T00:00:00Z',
    },
  ]
  const fixtureByLink = new Map(fixtures.map((fixture) => [`audit://${fixture.key}`, fixture]))

  const server = http.createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJSON(res, 200, { version: FRONTEND_PACKAGE.version, update: { available: false } })
      return
    }
    if (req.method === 'GET' && req.url === '/api/node-groups') {
      sendJSON(res, 200, [{ id: 1, name: 'Default', sort_order: 0, allocate_port: true, preserve_inbound_ports: true, start_port: 30001 }])
      return
    }
    if (req.method === 'GET' && req.url === '/api/settings') {
      sendJSON(res, 200, {})
      return
    }
    if (req.method === 'GET' && req.url === '/api/nodes') {
      sendJSON(res, 200, existingNodes)
      return
    }
    if (req.method === 'GET' && req.url === '/api/runtime/status') {
      sendJSON(res, 200, { degraded: false, running: true })
      return
    }
    if (req.method === 'POST' && req.url === '/api/parse-link') {
      const payload = await readJSONBody(req)
      const fixture = fixtureByLink.get(payload.link)
      if (!fixture) {
        sendJSON(res, 400, { error: 'unknown audit fixture' })
        return
      }
      sendJSON(res, 200, {
        type: fixture.type,
        name: `Imported ${fixture.key}`,
        config: JSON.stringify(fixture.config),
      })
      return
    }
    if (req.method === 'POST' && req.url === '/api/nodes') {
      const payload = await readJSONBody(req)
      createdPayloads.push(payload)
      // Deliberately omit an id so Dashboard does not start an unrelated IP
      // check; this regression is scoped to import/edit/save preservation.
      sendJSON(res, 201, {})
      return
    }
    if (req.method === 'PUT' && req.url === '/api/nodes/9901') {
      const payload = await readJSONBody(req)
      updatedPayloads.push(payload)
      sendJSON(res, 200, payload)
      return
    }
    if (req.method === 'POST' && req.url === '/api/logout') {
      sendJSON(res, 200, { message: 'logged out' })
      return
    }
    if (req.method === 'GET' && req.url === '/favicon.ico') {
      res.writeHead(204)
      res.end()
      return
    }
    sendJSON(res, 404, { error: 'not found', method: req.method, url: req.url })
  })

  return { server, createdPayloads, updatedPayloads }
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
  return { child, getLogs: () => logs.join('') }
}

const waitForNodeForm = async (page, expectedName) => {
  await page.waitForFunction(
    (name) => {
      const forms = Array.from(document.querySelectorAll('.ant-modal .ant-form'))
      return forms.some((form) => {
        const rect = form.getBoundingClientRect()
        const nameInput = form.querySelector('input#name')
        return rect.width > 0 && rect.height > 0 && nameInput?.value === name
      })
    },
    { timeout: 10000 },
    expectedName
  )
}

const assertOriginalConfigPreserved = (fixture, actual) => {
  const expectedAbsentFields = new Set(fixture.expectedAbsentFields || [])
  for (const [key, expectedValue] of Object.entries(fixture.config)) {
    if (expectedAbsentFields.has(key)) {
      continue
    }
    const expectedSavedValue = Object.prototype.hasOwnProperty.call(
      fixture.expectedOverrides || {},
      key
    )
      ? fixture.expectedOverrides[key]
      : expectedValue
    if (!Object.prototype.hasOwnProperty.call(actual, key)) {
      throw new Error(`${fixture.key}: config key ${key} was dropped`)
    }
    if (!isDeepStrictEqual(actual[key], expectedSavedValue)) {
      throw new Error(
        `${fixture.key}: config key ${key} changed from ${JSON.stringify(expectedValue)} to ${JSON.stringify(actual[key])}; expected saved value ${JSON.stringify(expectedSavedValue)}`
      )
    }
  }
}

const assertExpectedSavedFields = (fixture, actual) => {
  for (const [key, expectedValue] of Object.entries(fixture.expectedSavedFields || {})) {
    if (!Object.prototype.hasOwnProperty.call(actual, key)) {
      throw new Error(`${fixture.key}: expected saved config key ${key} is missing`)
    }
    if (!isDeepStrictEqual(actual[key], expectedValue)) {
      throw new Error(
        `${fixture.key}: saved config key ${key} is ${JSON.stringify(actual[key])}; expected ${JSON.stringify(expectedValue)}`
      )
    }
  }

  for (const key of fixture.expectedAbsentFields || []) {
    if (Object.prototype.hasOwnProperty.call(actual, key)) {
      throw new Error(
        `${fixture.key}: saved config unexpectedly contains ${key}=${JSON.stringify(actual[key])}`
      )
    }
  }
}

const isIgnorableConsoleError = (text) =>
  text.includes('Support for defaultProps will be removed from memo components') ||
  text.includes('[antd: message] Static function can not consume context')

const run = async () => {
  const mockAPI = createMockAPI()
  await new Promise((resolve, reject) => {
    mockAPI.server.once('error', reject)
    mockAPI.server.listen(API_PORT, '127.0.0.1', resolve)
  })
  const vite = startVite()
  let browser
  let page
  let currentFixture = null
  let currentStep = '等待前端服务'

  try {
    await waitForHTTP(FRONTEND_URL, { timeoutMs: 60000, intervalMs: 250 })
    currentStep = '启动浏览器'
    browser = await launchBrowser()
    page = await browser.newPage()
    page.setDefaultNavigationTimeout(120000)
    await page.setViewport({ width: 1440, height: 1100 })
    await page.evaluateOnNewDocument(() => {
      localStorage.setItem('token', 'nodeform-config-preservation-token')
      localStorage.setItem('language', 'en')
    })

    const consoleErrors = []
    page.on('console', (message) => {
      if (message.type() === 'error') consoleErrors.push(message.text())
    })
    page.on('pageerror', (error) => consoleErrors.push(`pageerror:${error.message}`))

    currentStep = '打开管理面板'
    await page.goto(FRONTEND_URL, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.sb-repo', { timeout: 15000 })

    for (const fixture of fixtures) {
      currentFixture = fixture.key
      const captureIndex = mockAPI.createdPayloads.length
      currentStep = '打开节点创建弹窗'
      await clickControl(page, '[data-testid="nodes-add-node"]')
      currentStep = '填写导入链接'
      await setInput(page, '#sb-link-input', `audit://${fixture.key}`)
      currentStep = '解析导入链接'
      await clickText(page, 'Parse Link')
      currentStep = '等待协议表单回填'
      await waitForNodeForm(page, `Imported ${fixture.key}`)
      currentStep = '关闭入站认证'
      await toggle(page, '#auth_enabled', false)

      for (const [id, expected] of Object.entries(fixture.initialInputs || {})) {
        currentStep = `核对初始输入：${id}`
        const actual = await page.$eval(`#${id}`, (input) => input.value)
        if (actual !== expected) throw new Error(`${fixture.key}: ${id}=${actual}, expected ${expected}`)
      }
      for (const [id, expected] of Object.entries(fixture.initialSelections || {})) {
        currentStep = `核对初始选项：${id}`
        const actual = await page.$eval(`#${id}`, (input) =>
          input.closest('.ant-select')?.querySelector('.ant-select-selection-item')?.textContent
        )
        if (actual !== expected) throw new Error(`${fixture.key}: ${id}=${actual}, expected ${expected}`)
      }
      for (const [id, value] of Object.entries(fixture.editInputs || {})) {
        currentStep = `修改输入：${id}`
        await setInput(page, `#${id}`, value)
      }
      for (const id of fixture.clearSelections || []) {
        const select = `.ant-select:has(#${id})`
        currentStep = `清空选项：${id}`
        await clickControl(page, `${select} .ant-select-clear`)
        currentStep = `等待选项清空：${id}`
        await page.waitForFunction(
          (fieldID) => {
            const text = document.getElementById(fieldID)?.closest('.ant-select')
              ?.querySelector('.ant-select-selection-item')?.textContent?.trim() || ''
            return text === '' || text === 'None'
          },
          { timeout: 5000 },
          id
        )
      }

      if (fixture.peerArray) {
        currentStep = '核对 WireGuard peer-array 表单'
        const hasNetworkField = await page.evaluate(() =>
          Array.from(document.querySelectorAll('.ant-modal .ant-form-item-label label')).some(
            (label) => (label.textContent || '').trim() === 'Network'
          )
        )
        if (hasNetworkField) {
          throw new Error('WireGuard form still exposes the unsupported Network field')
        }
      }

      if (fixture.clearPeerArray) {
        const panel = '[data-testid="node-form"] .ant-collapse-item:has(#wireguard_peers_json)'
        const header = `${panel} > .ant-collapse-header`
        currentStep = '检查 WireGuard 高级区展开状态'
        const expanded = await page.$eval(header, (element) => element.getAttribute('aria-expanded') === 'true')
        if (!expanded) {
          currentStep = '展开 WireGuard 高级区'
          await clickControl(page, header)
        }
        currentStep = '等待 WireGuard 高级区动画结束'
        await page.waitForSelector(`${panel}.ant-collapse-item-active > .ant-collapse-content-active:not(.ant-collapse-content-hidden):not(.ant-motion-collapse)`, { visible: true })
        currentStep = '清空 WireGuard peers'
        await setInput(page, '[data-testid="node-form"] #wireguard_peers_json', '')
      }

      if (fixture.switchSecurityToTLS) {
        currentStep = '将安全层切换到 TLS'
        await selectOption(page, 'security', 'TLS', { waitForSelection: true })
      }

      if (fixture.selectVMessCFB) {
        currentStep = '选择 VMess aes-128-cfb'
        await selectOption(page, 'vmess_security', 'aes-128-cfb', { waitForSelection: true })
      }

      if (fixture.verifySalamanderRequired) {
        currentStep = '清空 Salamander 混淆密码'
        await setInput(page, '#obfs_password', '')
        currentStep = '提交无混淆密码的 Salamander 表单'
        await saveNodeForm(page)
        currentStep = '核对 Salamander 必填校验'
        await page.waitForFunction(
          () =>
            Array.from(document.querySelectorAll('.ant-form-item-explain-error')).some(
              (element) => (element.textContent || '').includes('Required for Salamander')
            ),
          { timeout: 15000 }
        )
        if (mockAPI.createdPayloads.length !== captureIndex) {
          throw new Error('Hysteria2 Salamander was saved with an empty obfs password')
        }
        currentStep = '恢复 Salamander 混淆密码'
        await setInput(page, '#obfs_password', 'obfs-secret')
      }

      currentStep = '保存新节点并确认关闭认证'
      await saveNodeForm(page, { confirmOpenAuth: true })
      currentStep = '等待节点创建请求'
      const deadline = Date.now() + 10000
      while (mockAPI.createdPayloads.length === captureIndex && Date.now() < deadline) {
        await sleep(100)
      }
      const payload = mockAPI.createdPayloads[captureIndex]
      currentStep = '核对创建类型与认证配置'
      if (!payload) throw new Error(`${fixture.key}: create payload was not captured`)
      if (payload.type !== fixture.type) {
        throw new Error(`${fixture.key}: expected type ${fixture.type}, got ${payload.type}`)
      }
      if (payload.auth_enabled !== false || payload.username !== '' || payload.password !== '') {
        throw new Error(
          `${fixture.key}: explicitly disabled inbound authentication was not preserved: ${JSON.stringify(payload)}`
        )
      }

      const actualConfig = JSON.parse(payload.config || '{}')
      currentStep = '核对协议原始配置与兼容字段'
      assertOriginalConfigPreserved(fixture, actualConfig)
      assertExpectedSavedFields(fixture, actualConfig)
      if (fixture.peerArray && !fixture.clearPeerArray) {
        for (const conflictingKey of [
          'server',
          'server_port',
          'peer_public_key',
          'pre_shared_key',
          'allowed_ips',
          'reserved',
        ]) {
          if (conflictingKey in actualConfig) {
            throw new Error(
              `Peer-array WireGuard config must not gain derived flat field ${conflictingKey}`
            )
          }
        }
      }
      if (fixture.clearPeerArray) {
        for (const requiredFlatKey of [
          'server',
          'server_port',
          'peer_public_key',
          'allowed_ips',
          'reserved',
        ]) {
          if (!(requiredFlatKey in actualConfig)) {
            throw new Error(
              `Clearing WireGuard peers must expose flat compatibility field ${requiredFlatKey}`
            )
          }
        }
      }
      if (fixture.nativeResolverObject) {
        const resolverOptions = actualConfig.domain_resolver_options
        const expectedResolverOptions = {
          disable_cache: true,
          rewrite_ttl: 60,
          client_subnet: '192.0.2.0/24',
        }
        if (!isDeepStrictEqual(resolverOptions, expectedResolverOptions)) {
          throw new Error(
            `Native WireGuard resolver options were not preserved: ${JSON.stringify(resolverOptions)}`
          )
        }
        if (actualConfig.domain_resolver_strategy !== 'prefer_ipv4') {
          throw new Error('Native WireGuard resolver strategy was not preserved')
        }
      }
      if (fixture.flatWireGuard && 'peers' in actualConfig) {
        throw new Error('Flat WireGuard config must not gain an empty peers array')
      }
      currentStep = '等待节点创建弹窗关闭'
      await waitOverlaysClosed(page)
    }

    currentFixture = 'Existing empty Trojan'
    currentStep = '打开已有 Trojan 编辑弹窗'
    await editNode(page, 9901)
    currentStep = '等待已有 Trojan 表单回填'
    await waitForNodeForm(page, 'Existing empty Trojan')
    currentStep = '保存已有 Trojan 节点'
    await saveNodeForm(page)
    currentStep = '等待已有 Trojan 更新请求'
    const updateDeadline = Date.now() + 10000
    while (mockAPI.updatedPayloads.length === 0 && Date.now() < updateDeadline) {
      await sleep(100)
    }
    const updatedTrojan = mockAPI.updatedPayloads[0]
    currentStep = '核对已有 Trojan 协议密码与入站认证'
    if (!updatedTrojan) {
      throw new Error('Existing empty Trojan update payload was not captured')
    }
    const updatedTrojanConfig = JSON.parse(updatedTrojan.config || '{}')
    if (updatedTrojanConfig.password !== '') {
      throw new Error(
        `Trojan protocol password fell back to local inbound password: ${JSON.stringify(updatedTrojanConfig)}`
      )
    }
    if (updatedTrojan.password !== 'local-inbound-secret') {
      throw new Error('Existing local inbound password was not preserved independently')
    }
    currentStep = '等待已有 Trojan 编辑弹窗关闭'
    await waitOverlaysClosed(page)

    currentFixture = null
    currentStep = '核对浏览器错误'
    const unexpectedErrors = consoleErrors.filter((line) => !isIgnorableConsoleError(line))
    if (unexpectedErrors.length > 0) {
      throw new Error(`Unexpected browser errors: ${unexpectedErrors.join('\n')}`)
    }

    process.stdout.write(
      `${JSON.stringify({ success: true, fixtures: fixtures.map((fixture) => fixture.key) })}\n`
    )
  } catch (error) {
    const state = page ? await page.evaluate(() => {
      const rect = (element) => {
        if (!element) return null
        const { x, y, width, height } = element.getBoundingClientRect()
        return { x, y, width, height }
      }
      const peers = document.querySelector('[data-testid="node-form"] #wireguard_peers_json')
      const focused = document.activeElement
      return {
        errors: Array.from(document.querySelectorAll('.ant-form-item-explain-error'), (element) => element.textContent),
        buttons: Array.from(document.querySelectorAll('.ant-modal button'), (element) => ({ text: element.textContent, disabled: element.disabled })),
        focus: { tag: focused?.tagName, id: focused?.id, testId: focused?.getAttribute('data-testid') },
        peers: peers ? { bounds: rect(peers), disabled: peers.disabled, visibility: getComputedStyle(peers).visibility } : null,
        collapse: Array.from(document.querySelectorAll('.ant-modal .ant-collapse-item'), (element) => {
          const header = element.querySelector(':scope > .ant-collapse-header')
          const content = element.querySelector(':scope > .ant-collapse-content')
          return { itemClass: element.className, expanded: header?.getAttribute('aria-expanded'), contentClass: content?.className, contentBounds: rect(content) }
        }),
        scroll: Array.from(document.querySelectorAll('.ant-modal-body'), (element) => ({
          bounds: rect(element), scrollTop: element.scrollTop, clientHeight: element.clientHeight, scrollHeight: element.scrollHeight,
        })),
        footers: Array.from(document.querySelectorAll('.ant-modal .sb-form-footer'), rect),
      }
    }).catch((failure) => ({ snapshotError: failure.message })) : {}
    const diagnostic = { fixture: currentFixture, step: currentStep, ...state }
    throw new Error(`${error.stack}\n诊断：${JSON.stringify(diagnostic)}\nVite logs:\n${vite.getLogs()}`)
  } finally {
    try {
      await browser?.close()
    } catch {
      // Ignore browser cleanup failures.
    }
    await stopProcess(vite.child, { graceMs: 5000 })
    await new Promise((resolve) => mockAPI.server.close(resolve))
  }
}

run().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
