// 全栈新增特性端到端回归：失败检测持久化、节点分组/无端口组/订阅增量同步、策略出站（故障转移+负载均衡真实转发）、
// 「当前连接」按需实时路由路径、以及「日志与流量统计」内存缓冲 + 持久化校验/落盘 + 视觉截图采样。
import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import { findTestPortRange } from './test-ports.mjs'
import { launchBrowser, stopProcess } from './test-harness.mjs'
import { clickControl, setInput, sleep, toggle, waitOverlaysClosed } from './ui-driver.mjs'

const BINARY = process.env.SBPM_E2E_BACKEND_BINARY
const SINGBOX_BINARY = process.env.SINGBOX_TEST_BINARY
const ARTIFACT_DIR = process.env.E2E_ARTIFACT_DIR || ''
assert.ok(BINARY && path.isAbsolute(BINARY), '需要 SBPM_E2E_BACKEND_BINARY 绝对路径')
assert.ok(SINGBOX_BINARY && path.isAbsolute(SINGBOX_BINARY), '需要 SINGBOX_TEST_BINARY 绝对路径')

const listen = (server) => new Promise((resolve, reject) => {
  server.once('error', reject)
  server.listen(0, '127.0.0.1', () => {
    server.off('error', reject)
    resolve(server.address().port)
  })
})

const closeServer = (server) => new Promise((resolve) => {
  if (!server?.listening) return resolve()
  server.close(resolve)
  server.closeAllConnections?.()
})

const reservePort = async () => {
  const server = net.createServer()
  const port = await listen(server)
  await closeServer(server)
  return port
}

const wait = async (predicate, label, timeout = 30000) => {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (await predicate()) return
    await sleep(120)
  }
  throw new Error(label)
}

const curl = (args, timeoutSec = 10) => new Promise((resolve, reject) => {
  const child = spawn('curl', ['--silent', '--show-error', '--fail', '--max-time', String(timeoutSec), '--noproxy', '', ...args], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let stdout = ''
  let stderr = ''
  child.stdout.on('data', (chunk) => { stdout += chunk.toString() })
  child.stderr.on('data', (chunk) => { stderr += chunk.toString() })
  child.once('error', reject)
  child.once('exit', (code) => resolve({ code, stdout, stderr }))
})

// 轻量本地 HTTP/SOCKS5 目标服务：支持慢速流式响应以验证「当前连接」实时速率，以及区分不同上游标记。
const createBackendEchoServer = (tag) => http.createServer((req, res) => {
  if (req.url === '/ip') {
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ ip: tag === 'A' ? '203.0.113.101' : '203.0.113.102', country: 'Japan', cc: 'JP' }))
    return
  }
  if (req.url?.startsWith('/stream')) {
    res.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Cache-Control': 'no-store' })
    const chunk = Buffer.alloc(16384, 'x')
    let sent = 0
    const timer = setInterval(() => {
      if (res.writableEnded || res.destroyed || sent >= 25) {
        clearInterval(timer)
        if (!res.writableEnded) res.end()
        return
      }
      sent += 1
      res.write(chunk)
    }, 250)
    res.once('close', () => clearInterval(timer))
    return
  }
  res.writeHead(200, { 'Content-Type': 'text/plain' })
  res.end(`backend-${tag}`)
})

// 自定义极简 SOCKS5 代理（将连接转发到对应成员的回显服务，便于精确识别命中成员 A 还是成员 B）。
const createTaggedSocks5Server = (targetPort) => net.createServer((client) => {
  let stage = 0
  client.on('data', (chunk) => {
    if (stage === 0) {
      // SOCKS5 greeting
      if (chunk.length < 2 || chunk[0] !== 0x05) { client.destroy(); return }
      stage = 1
      client.write(Buffer.from([0x05, 0x00]))
      return
    }
    if (stage === 1) {
      // CONNECT request: 05 01 00 atyp ...
      if (chunk.length < 7 || chunk[0] !== 0x05 || chunk[1] !== 0x01) { client.destroy(); return }
      stage = 2
      // 总是转发到与当前 tag 绑定的本地目标端口，便于断言流量实际命中的上游成员
      const upstream = net.createConnection({ host: '127.0.0.1', port: targetPort })
      upstream.once('connect', () => {
        client.write(Buffer.from([0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0]))
        client.pipe(upstream)
        upstream.pipe(client)
      })
      upstream.on('error', () => client.destroy())
      client.on('error', () => upstream.destroy())
    }
  })
  client.on('error', () => {})
})

const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'sbpm-features-e2e-'))
const configDir = path.join(scratch, 'config')
const SECRET = `Features-${randomBytes(16).toString('hex')}`
const backendLogs = []
const browserErrors = []
const steps = []

let backend
let browser
let page
let token = ''
let targetA
let targetB
let socksA
let socksB
let subServer

try {
  targetA = createBackendEchoServer('A')
  targetB = createBackendEchoServer('B')
  const targetPortA = await listen(targetA)
  const targetPortB = await listen(targetB)

  socksA = createTaggedSocks5Server(targetPortA)
  socksB = createTaggedSocks5Server(targetPortB)
  const socksPortA = await listen(socksA)
  const socksPortB = await listen(socksB)

  const unusedPort = await reservePort()
  let subHits = 0
  subServer = http.createServer((_req, res) => {
    subHits += 1
    res.writeHead(200, { 'Content-Type': 'text/plain' })
    res.end(Buffer.from(`socks5://127.0.0.1:${socksPortA}#SubNodeA`).toString('base64'))
  })
  const subPort = await listen(subServer)

  const httpPort = await reservePort()
  const base = `http://127.0.0.1:${httpPort}`
  const env = { ...process.env }
  for (const key of ['DATABASE_URL', 'POSTGRES_DATABASE_URL', 'POSTGRES_URL', 'PGSQL_DATABASE_URL', 'PGSQL', 'MYSQL_DATABASE_URL', 'MYSQL', 'TURSO_DATABASE_URL', 'TURSO_AUTH_TOKEN']) {
    delete env[key]
  }

  const startBackend = async () => {
    backend = spawn(BINARY, [], {
      cwd: scratch,
      env: {
        ...env,
        ACCEPT_USER_AGREEMENT: 'true',
        ADMIN_PASSWORD: SECRET,
        BIND_ADDRESS: '127.0.0.1',
        CONFIG_DIR: configDir,
        PORT: String(httpPort),
        SINGBOX_BINARY,
        SBPM_UPDATE_CHECK_DISABLED: '1',
        SBPM_IPCHECK_URLS: `http://127.0.0.1:${targetPortA}/ip`,
        TZ: 'UTC',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    backend.stdout.on('data', (chunk) => backendLogs.push(chunk.toString()))
    backend.stderr.on('data', (chunk) => backendLogs.push(chunk.toString()))
    await wait(async () => {
      assert.equal(backend.exitCode, null, `后端提前退出：${backendLogs.join('').slice(-3000)}`)
      try { return (await fetch(`${base}/api/version`)).status === 200 } catch { return false }
    }, '后端未就绪', 40000)
  }

  const reqAPI = async (method, endpoint, body) => {
    const response = await fetch(`${base}/api${endpoint}`, {
      method,
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(45000),
    })
    let data = null
    try { data = await response.json() } catch { /* ignore non-json */ }
    return { status: response.status, data }
  }

  const api = async (method, endpoint, body) => {
    const res = await reqAPI(method, endpoint, body)
    assert.ok(res.status < 400, `${method} ${endpoint} 失败 (${res.status}): ${JSON.stringify(res.data)}`)
    return res.data
  }

  await startBackend()
  token = (await api('POST', '/login', { password: SECRET })).token

  // 设置默认分组起始端口，避免端口冲突
  const groups0 = await api('GET', '/node-groups')
  assert.equal(groups0.length, 1)
  const defaultGroupId = groups0[0].id
  const startPort = await findTestPortRange(30)
  await api('PATCH', `/node-groups/${defaultGroupId}`, { start_port: startPort })
  // 不能删除最后一个分组
  const delLast = await reqAPI('DELETE', `/node-groups/${defaultGroupId}`)
  assert.equal(delLast.status, 409)
  steps.push('default-group-and-last-group-guard')

  // 1. 失败检测状态持久化 + 配置变更重置为未检测
  const deadNode = await api('POST', '/nodes', {
    group_id: defaultGroupId,
    name: 'Unreachable Node',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: unusedPort }),
    auth_enabled: false,
    enabled: true,
  })
  assert.equal(deadNode.check_status || 'untested', 'untested')
  const failCheck = await reqAPI('POST', `/nodes/${deadNode.id}/check-ip`)
  assert.ok(failCheck.status >= 400, `不可达节点测速应失败，实际 ${failCheck.status}`)
  const afterFail = await api('GET', `/nodes/${deadNode.id}`)
  assert.equal(afterFail.check_status, 'failed', `失败状态未落库：${JSON.stringify(afterFail)}`)
  assert.ok(afterFail.check_error && afterFail.check_error.length > 0, '缺少失败错误原因')
  // 重启验证失败状态持久化
  await stopProcess(backend, { graceMs: 15000, requireGraceful: true })
  await startBackend()
  const afterRestartFail = await api('GET', `/nodes/${deadNode.id}`)
  assert.equal(afterRestartFail.check_status, 'failed', '重启后失败状态丢失')
  // 修改节点服务器端口，验证重置为 untested
  await api('PUT', `/nodes/${deadNode.id}`, {
    name: deadNode.name,
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: unusedPort + 1 }),
    auth_enabled: false,
    enabled: true,
  })
  const afterReset = await api('GET', `/nodes/${deadNode.id}`)
  assert.equal(afterReset.check_status, 'untested', '修改出站配置后未重置为 untested')
  assert.equal(afterReset.check_error || '', '', '修改出站配置后未清空 check_error')
  // 再次触发失败，留给前端 UI 验收红点与中性色叉号
  await reqAPI('POST', `/nodes/${deadNode.id}/check-ip`)
  steps.push('check-failed-persistence-and-reset')

  // 2. 创建无端口池分组 + 成员节点 + 无端口节点测 IP + 订阅增量同步
  const noPortGroup = await api('POST', '/node-groups', {
    name: 'Pool NoPort',
    allocate_port: false,
    start_port: startPort + 100,
  })
  const memberA = await api('POST', '/nodes', {
    group_id: noPortGroup.id,
    name: 'Member A',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortA }),
    auth_enabled: false,
    enabled: true,
  })
  const memberB = await api('POST', '/nodes', {
    group_id: noPortGroup.id,
    name: 'Member B',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortB }),
    auth_enabled: false,
    enabled: true,
  })
  assert.equal(memberA.inbound_port, 0, '无端口分组节点不应分配入站端口')
  assert.equal(memberB.inbound_port, 0, '无端口分组节点不应分配入站端口')

  // 无端口节点应支持独立临时 probe 测 IP
  const noPortCheck = await api('POST', `/nodes/${memberA.id}/check-ip`)
  assert.equal(noPortCheck.ip, '203.0.113.101', `无端口节点测 IP 结果异常：${JSON.stringify(noPortCheck)}`)
  steps.push('noport-group-and-ipcheck')

  // 分组独立订阅（验证回环 URL 触发 SSRF 拦截且保留原有节点/端口/备注，成功增量同步由 Go 集成测试覆盖）
  const subGroup = await api('POST', '/node-groups', {
    name: 'Sub Group',
    allocate_port: true,
    start_port: startPort + 10,
    subscription_url: `http://127.0.0.1:${subPort}/sub`,
    subscription_auto_update: false,
    subscription_interval_minutes: 60,
  })
  const subNodeA = await api('POST', '/nodes', {
    group_id: subGroup.id,
    name: 'SubKeepNode',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortA }),
    auth_enabled: false,
    enabled: true,
  })
  assert.ok(subNodeA && subNodeA.inbound_port > 0)
  await api('PUT', `/nodes/${subNodeA.id}/remark`, { remark: 'keep-me-on-sync' })
  await api('PUT', `/nodes/${subNodeA.id}/port-pin`, { pinned: true })
  const blockedRefresh = await reqAPI('POST', `/node-groups/${subGroup.id}/subscription/refresh`)
  assert.ok(blockedRefresh.status >= 400, `回环订阅地址应被 SSRF 保护拦截，实际 ${blockedRefresh.status}`)
  assert.equal(subHits, 0, 'SSRF 保护生效时不应请求本地回环订阅服务')
  const subNodeAAfter = await api('GET', `/nodes/${subNodeA.id}`)
  assert.equal(subNodeAAfter.inbound_port, subNodeA.inbound_port, '订阅刷新失败破坏了原有入站端口')
  assert.equal(subNodeAAfter.inbound_port_pinned, true, '订阅刷新失败丢失了固定端口标记')
  assert.equal(subNodeAAfter.remark, 'keep-me-on-sync', '订阅刷新失败丢失了备注')
  // 关闭含固定端口分组的 allocate_port 应返回 409 pinned_ports_exist
  const disablePinConflict = await reqAPI('PATCH', `/node-groups/${subGroup.id}`, { allocate_port: false })
  assert.equal(disablePinConflict.status, 409, `含固定端口关闭端口分配应返回 409，实际 ${disablePinConflict.status}`)
  // 非空分组默认删除拦截
  const delNonEmpty = await reqAPI('DELETE', `/node-groups/${subGroup.id}`)
  assert.equal(delNonEmpty.status, 409, '非空分组默认删除未拦截')
  await api('DELETE', `/node-groups/${subGroup.id}?mode=delete_nodes`)
  steps.push('group-subscription-ssrf-and-guards')

  // 3. 策略出站节点：Failover（实时故障转移）+ LoadBalance（轮询分发 + 一致性哈希）
  const failoverNode = await api('POST', '/nodes', {
    group_id: defaultGroupId,
    name: 'JP Failover',
    type: 'failover',
    config: JSON.stringify({
      member_ids: [memberA.id, memberB.id],
      health_check_url: `http://127.0.0.1:${targetPortA}/`,
      health_check_interval: 15,
    }),
    username: 'policy-user',
    password: 'policy-secret',
    auth_enabled: true,
    enabled: true,
  })
  assert.ok(failoverNode.inbound_port > 0, '策略节点未分配公网入站端口')

  // 正常状态下应优先走 Member A
  const resFailover1 = await curl([
    '--proxy', `http://127.0.0.1:${failoverNode.inbound_port}`,
    '--proxy-user', 'policy-user:policy-secret',
    `http://127.0.0.1:${targetPortA}/`,
  ])
  assert.equal(resFailover1.code, 0, `Failover 首次转发失败：${resFailover1.stderr}`)
  assert.equal(resFailover1.stdout.trim(), 'backend-A', `Failover 应优先走 Member A，实际 ${resFailover1.stdout}`)

  // 策略节点测 IP 应可用
  const policyCheck = await api('POST', `/nodes/${failoverNode.id}/check-ip`)
  assert.equal(policyCheck.ip, '203.0.113.101', `Failover 测 IP 异常：${JSON.stringify(policyCheck)}`)

  // 杀掉 Member A 的上游端口，触发立即故障转移到 Member B
  await closeServer(socksA)
  const resFailover2 = await curl([
    '--socks5-hostname', `127.0.0.1:${failoverNode.inbound_port}`,
    '--proxy-user', 'policy-user:policy-secret',
    `http://127.0.0.1:${targetPortA}/`,
  ])
  assert.equal(resFailover2.code, 0, `Failover 切换 Member B 失败：${resFailover2.stderr}`)
  assert.equal(resFailover2.stdout.trim(), 'backend-B', `Failover 未切换到 Member B，实际 ${resFailover2.stdout}`)

  // 重新拉起 Member A 在同一端口，测试 LoadBalance Round-Robin
  socksA = createTaggedSocks5Server(targetPortA)
  await new Promise((resolve, reject) => {
    socksA.once('error', reject)
    socksA.listen(socksPortA, '127.0.0.1', resolve)
  })
  const lbNode = await api('POST', '/nodes', {
    group_id: defaultGroupId,
    name: 'JP LoadBalance',
    type: 'loadbalance',
    config: JSON.stringify({
      member_ids: [memberA.id, memberB.id],
      strategy: 'round-robin',
      health_check_interval: 15,
    }),
    username: 'lb-user',
    password: 'lb-secret',
    auth_enabled: true,
    enabled: true,
  })
  const lbHits = new Set()
  for (let i = 0; i < 6; i += 1) {
    const r = await curl([
      '--proxy', `http://127.0.0.1:${lbNode.inbound_port}`,
      '--proxy-user', 'lb-user:lb-secret',
      `http://127.0.0.1:${targetPortA}/req-${i}`,
    ])
    assert.equal(r.code, 0, `LoadBalance 请求失败：${r.stderr}`)
    lbHits.add(r.stdout.trim())
  }
  assert.ok(lbHits.has('backend-A') && lbHits.has('backend-B'), `LoadBalance 未在双成员间轮询分发：${[...lbHits].join(',')}`)
  steps.push('policy-failover-and-loadbalance-live')

  // 4. 打开真实浏览器：验证 UI、当前连接实时流、日志与持久化校验/落盘 + 视觉截图
  browser = await launchBrowser({ userDataDir: path.join(scratch, 'browser') })
  page = await browser.newPage()
  page.setDefaultTimeout(25000)
  await page.setViewport({ width: 1440, height: 960 })
  await page.evaluateOnNewDocument((authToken) => {
    localStorage.setItem('token', authToken)
    localStorage.setItem('language', 'zh')
  }, token)
  page.on('pageerror', (err) => browserErrors.push(err.message))
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (msg.location().url.includes('/api/logs/settings') && msg.text().includes('400')) return
    browserErrors.push(`${msg.text()} (${msg.location().url})`)
  })

  await page.goto(base, { waitUntil: 'networkidle2' })
  await page.waitForSelector('[data-testid="nodes-list"]', { visible: true })
  // 验证失败节点红点与中性叉号在真实全栈中展示
  assert.equal(await page.$eval(`[data-testid="node-health-${deadNode.id}"]`, (el) => el.dataset.health), 'failed')
  assert.ok(await page.$(`[data-row-id="${deadNode.id}"] [data-testid="latency-failed"]`))
  if (ARTIFACT_DIR) {
    fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-nodes-overview.png'), fullPage: true })
  }

  // 启动一条慢速持续下载流经过 Failover 策略节点，检验「当前连接」实时三列视图与速率
  const streamProc = spawn('curl', [
    '--silent', '--no-buffer', '--max-time', '15', '--noproxy', '',
    '--proxy', `http://127.0.0.1:${failoverNode.inbound_port}`,
    '--proxy-user', 'policy-user:policy-secret',
    `http://127.0.0.1:${targetPortA}/stream`,
  ], { stdio: ['ignore', 'ignore', 'ignore'] })

  await clickControl(page, '[data-testid="nav-tab-connections"]')
  await page.waitForSelector('[data-testid="connections-panel"]', { visible: true })
  await wait(async () => {
    const rows = await page.$$('[data-testid="conn-outbound-row"]')
    return rows.length >= 1
  }, '当前连接面板未采集到活跃代理连接', 15000)
  // 等待第二帧出现速率数值
  await sleep(1600)
  const connText = await page.$eval('[data-testid="connections-topology"]', (el) => el.textContent)
  assert.ok(connText.includes('JP Failover'), `当前连接未展示策略节点名称：${connText}`)
  assert.ok(connText.includes(`:${failoverNode.inbound_port}`), `当前连接未展示入站端口：${connText}`)
  assert.equal(connText.includes('internal-policy'), false, `当前连接泄漏了内部回环标签：${connText}`)
  if (ARTIFACT_DIR) {
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-connections-live.png'), fullPage: true })
  }
  await stopProcess(streamProc, { graceMs: 2000 })
  steps.push('connections-live-topology-and-policy-mapping')

  // 切换到「日志」面板：验证默认内存缓冲、保存失败不生效、开启本地 SQLite 持久化与流量统计
  await clickControl(page, '[data-testid="nav-tab-logs"]')
  await page.waitForSelector('[data-testid="logs-panel"]', { visible: true })
  await wait(async () => (await page.$$('.logs-row[data-level]')).length >= 1, '默认内存日志缓冲为空', 10000)
  if (ARTIFACT_DIR) {
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-logs-memory.png'), fullPage: true })
  }

  // 打开日志设置：测试无效远程库保存拦截（不破坏现有运行态）
  await clickControl(page, '[data-testid="logs-settings-open"]')
  await page.waitForSelector('[data-testid="log-settings-form"]', { visible: true })
  await toggle(page, '[data-testid="log-persist-switch"]', true)
  await clickControl(page, '[data-testid="log-store-mode"] label:has(input[value="independent"])')
  await setInput(page, '[data-testid="log-remote-dsn-input"]', 'postgres://bad:bad@127.0.0.1:1/bad?sslmode=disable')
  await clickControl(page, '[data-testid="log-settings-save"]')
  await page.waitForSelector('[data-testid="log-settings-error"]', { visible: true })
  assert.equal((await api('GET', '/logs/settings')).enabled, false, '远程库校验失败不应启用持久化')

  // 切回本地独立 SQLite 并保存生效
  await clickControl(page, '[data-testid="log-store-mode"] label:has(input[value="local_sqlite"])')
  if (ARTIFACT_DIR) {
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-logs-settings.png'), fullPage: true })
  }
  await clickControl(page, '[data-testid="log-settings-save"]')
  await waitOverlaysClosed(page)
  const persistedSettings = await api('GET', '/logs/settings')
  assert.equal(persistedSettings.enabled, true)
  assert.equal(persistedSettings.store_mode, 'local_sqlite')

  // 制造一次代理下载流量以便流量桶统计采集并验证流量统计子页
  const trafficRes = await curl([
    '--proxy', `http://127.0.0.1:${failoverNode.inbound_port}`,
    '--proxy-user', 'policy-user:policy-secret',
    `http://127.0.0.1:${targetPortA}/stream`,
  ], 10)
  assert.equal(trafficRes.code, 0, `流量统计采样请求失败：${trafficRes.stderr}`)
  await sleep(1500)
  await clickControl(page, '[data-testid="logs-subtab"] [data-value="traffic"]')
  await page.waitForSelector('[data-testid="traffic-summary"]', { visible: true })
  await wait(async () => {
    const tData = await api('GET', '/logs/traffic')
    return tData.persisted === true && Number(tData.total_bytes || 0) > 0
  }, '开启持久化后流量统计未累计入库', 15000)
  await clickControl(page, '[data-testid="traffic-refresh"]')
  await sleep(400)
  if (ARTIFACT_DIR) {
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-logs-traffic.png'), fullPage: true })
  }
  steps.push('logs-memory-validation-sqlite-and-traffic')

  assert.deepEqual(browserErrors, [], `全栈浏览器出现未预期错误：${JSON.stringify(browserErrors)}`)
  console.log(JSON.stringify({ success: true, scenario: 'full-stack-features', steps }))
} catch (error) {
  if (ARTIFACT_DIR && page) {
    fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-stack-features-failure.png'), fullPage: true }).catch(() => {})
  }
  throw new Error(`全栈新特性回归失败：${error.stack}\n已完成：${steps.join(', ')}\n后端日志：\n${backendLogs.join('').slice(-10000)}`)
} finally {
  await browser?.close().catch(() => {})
  await stopProcess(backend, { graceMs: 10000 })
  await closeServer(socksA)
  await closeServer(socksB)
  await closeServer(targetA)
  await closeServer(targetB)
  await closeServer(subServer)
  fs.rmSync(scratch, { recursive: true, force: true })
}
