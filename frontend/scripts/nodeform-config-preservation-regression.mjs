import { legacyClick as clickVisibleButton, editNode, setInput, waitOverlaysClosed } from './ui-driver.mjs'
import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { isDeepStrictEqual } from 'node:util'
import { fileURLToPath } from 'node:url'
import { launchBrowser, stopProcess } from './test-harness.mjs'

const API_PORT = Number(process.env.E2E_API_PORT || 30023)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5186)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)

import { protocolFixtures as fixtures } from './fixtures/protocol-configs.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

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
      sendJSON(res, 200, [{ id: 1, name: 'Default', sort_order: 0, allocate_port: true, start_port: 30001 }])
      return
    }
    if (req.method === 'GET' && req.url === '/api/settings') {
      sendJSON(res, 200, { preserve_inbound_ports: true })
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

const waitForHTTP = async (url, timeoutMs) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.status < 500) return
    } catch {
      // Retry until the server is ready.
    }
    await sleep(250)
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

const selectFormOption = async (page, fieldID, optionText, timeoutMs = 10000) => {
  const opened = await page.evaluate((id) => {
    const input = document.getElementById(id)
    const selector = input?.closest('.ant-select')?.querySelector('.ant-select-selector')
    if (!selector) return false

    selector.scrollIntoView({ block: 'center', inline: 'nearest' })
    const rect = selector.getBoundingClientRect()
    const eventInit = {
      bubbles: true,
      cancelable: true,
      clientX: rect.left + rect.width / 2,
      clientY: rect.top + rect.height / 2,
      buttons: 1,
    }
    selector.dispatchEvent(
      new PointerEvent('pointerdown', { ...eventInit, pointerType: 'mouse' })
    )
    selector.dispatchEvent(new MouseEvent('mousedown', eventInit))
    selector.dispatchEvent(new MouseEvent('mouseup', eventInit))
    selector.click()
    return true
  }, fieldID)

  if (!opened) {
    throw new Error(`Select field not found: ${fieldID}`)
  }

  await page.waitForFunction(
    (text) =>
      Array.from(
        document.querySelectorAll(
          '.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option'
        )
      ).some((option) => (option.textContent || '').trim() === text),
    { timeout: timeoutMs },
    optionText
  )

  const selected = await page.evaluate((text) => {
    const option = Array.from(
      document.querySelectorAll(
        '.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option'
      )
    ).find((candidate) => (candidate.textContent || '').trim() === text)
    if (!option) return false

    const rect = option.getBoundingClientRect()
    const eventInit = {
      bubbles: true,
      cancelable: true,
      clientX: rect.left + rect.width / 2,
      clientY: rect.top + rect.height / 2,
      buttons: 1,
    }
    option.dispatchEvent(
      new PointerEvent('pointerdown', { ...eventInit, pointerType: 'mouse' })
    )
    option.dispatchEvent(new MouseEvent('mousedown', eventInit))
    option.dispatchEvent(new MouseEvent('mouseup', eventInit))
    option.click()
    return true
  }, optionText)

  if (!selected) {
    throw new Error(`Select option not found: ${optionText}`)
  }

  await page.waitForFunction(
    (id, text) => {
      const input = document.getElementById(id)
      const selectedValue = input
        ?.closest('.ant-select')
        ?.querySelector('.ant-select-selection-item')?.textContent
      return (selectedValue || '').trim() === text
    },
    { timeout: timeoutMs },
    fieldID,
    optionText
  )
}

const setImportLink = async (page, link) => {
  await page.waitForSelector('#sb-link-input', { visible: true, timeout: 10000 })
  await page.$eval(
    '#sb-link-input',
    (input, value) => {
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        'value'
      )?.set
      setter?.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
      input.dispatchEvent(new Event('change', { bubbles: true }))
    },
    link
  )
}

const setPasswordInput = setInput

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

const setInboundAuthentication = async (page, enabled) => {
  await page.waitForSelector('#auth_enabled', { visible: true, timeout: 10000 })
  await page.$eval(
    '#auth_enabled',
    (button, nextEnabled) => {
      if (button.getAttribute('aria-checked') !== String(nextEnabled)) {
        button.click()
      }
    },
    enabled
  )
  await page.waitForFunction(
    (nextEnabled) =>
      document.querySelector('#auth_enabled')?.getAttribute('aria-checked') === String(nextEnabled),
    { timeout: 5000 },
    enabled
  )
}

const waitForNodeFormClosed = waitOverlaysClosed

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

  try {
    await waitForHTTP(FRONTEND_URL, 60000)
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

    await page.goto(FRONTEND_URL, { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('.sb-repo', { timeout: 15000 })

    for (const fixture of fixtures) {
      const captureIndex = mockAPI.createdPayloads.length
      await clickVisibleButton(page, 'Import Node')
      await setImportLink(page, `audit://${fixture.key}`)
      await clickVisibleButton(page, 'Parse Link')
      await waitForNodeForm(page, `Imported ${fixture.key}`)
      await setInboundAuthentication(page, false)

      for (const [id, expected] of Object.entries(fixture.initialInputs || {})) {
        const actual = await page.$eval(`#${id}`, (input) => input.value)
        if (actual !== expected) throw new Error(`${fixture.key}: ${id}=${actual}, expected ${expected}`)
      }
      for (const [id, expected] of Object.entries(fixture.initialSelections || {})) {
        const actual = await page.$eval(`#${id}`, (input) =>
          input.closest('.ant-select')?.querySelector('.ant-select-selection-item')?.textContent
        )
        if (actual !== expected) throw new Error(`${fixture.key}: ${id}=${actual}, expected ${expected}`)
      }
      for (const [id, value] of Object.entries(fixture.editInputs || {})) {
        await setPasswordInput(page, `#${id}`, value)
      }
      for (const id of fixture.clearSelections || []) {
        const select = `.ant-select:has(#${id})`
        await page.locator(select).hover()
        await page.locator(`${select} .ant-select-clear`).click()
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
        await page.$eval('#wireguard_peers_json', (textarea) => {
          const setter = Object.getOwnPropertyDescriptor(
            window.HTMLTextAreaElement.prototype,
            'value'
          )?.set
          setter?.call(textarea, '')
          textarea.dispatchEvent(new Event('input', { bubbles: true }))
          textarea.dispatchEvent(new Event('change', { bubbles: true }))
        })
      }

      if (fixture.switchSecurityToTLS) {
        await selectFormOption(page, 'security', 'TLS')
      }

      if (fixture.selectVMessCFB) {
        await selectFormOption(page, 'vmess_security', 'aes-128-cfb')
      }

      if (fixture.verifySalamanderRequired) {
        await setPasswordInput(page, '#obfs_password', '')
        await clickVisibleButton(page, 'Save')
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
        await setPasswordInput(page, '#obfs_password', 'obfs-secret')
      }

      await clickVisibleButton(page, 'Save')
      const deadline = Date.now() + 10000
      while (mockAPI.createdPayloads.length === captureIndex && Date.now() < deadline) {
        await sleep(100)
      }
      const payload = mockAPI.createdPayloads[captureIndex]
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
      await waitForNodeFormClosed(page)
    }

    await editNode(page, 9901)
    await waitForNodeForm(page, 'Existing empty Trojan')
    await clickVisibleButton(page, 'Save')
    const updateDeadline = Date.now() + 10000
    while (mockAPI.updatedPayloads.length === 0 && Date.now() < updateDeadline) {
      await sleep(100)
    }
    const updatedTrojan = mockAPI.updatedPayloads[0]
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
    await waitForNodeFormClosed(page)

    const unexpectedErrors = consoleErrors.filter((line) => !isIgnorableConsoleError(line))
    if (unexpectedErrors.length > 0) {
      throw new Error(`Unexpected browser errors: ${unexpectedErrors.join('\n')}`)
    }

    process.stdout.write(
      `${JSON.stringify({ success: true, fixtures: fixtures.map((fixture) => fixture.key) })}\n`
    )
  } catch (error) {
    const diagnostic = page ? await page.evaluate(() => ({
      errors: Array.from(document.querySelectorAll('.ant-form-item-explain-error'), (element) => element.textContent),
      buttons: Array.from(document.querySelectorAll('.ant-modal button'), (element) => ({ text: element.textContent, disabled: element.disabled })),
    })).catch(() => ({})) : {}
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
