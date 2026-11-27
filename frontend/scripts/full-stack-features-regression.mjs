// 全栈新增特性端到端回归：失败检测持久化、节点分组/无端口组/订阅增量同步、策略出站（故障转移+负载均衡+最低延迟真实转发）、
// 「当前连接」按需实时路由路径/内网归属/Mixed+HTTPS 并发、以及「日志与流量统计」内存缓冲 + 持久化校验/落盘 + 视觉截图采样。
import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { randomBytes } from 'node:crypto'
import { execFileSync, spawn } from 'node:child_process'
import { findFreePort, findTestPortRange } from './test-ports.mjs'
import { launchBrowser, stopProcess } from './test-harness.mjs'
import { clickControl, closeOverlay, setInput, sleep, toggle, waitOverlaysClosed } from './ui-driver.mjs'

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
const createBackendEchoServer = (tag, ip = '') => http.createServer((req, res) => {
  if (req.url === '/ip') {
    const resolvedIP = ip || (tag === 'A' ? '203.0.113.101' : '203.0.113.102')
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ ip: resolvedIP, country: 'Japan', cc: 'JP' }))
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
const createTaggedSocks5Server = (targetPort, connectionDelayMs = 0) => net.createServer((client) => {
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
      upstream.once('connect', async () => {
        if (connectionDelayMs > 0) await sleep(connectionDelayMs)
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
const certsDir = path.join(scratch, 'certs')
const keysDir = path.join(scratch, 'keys')
fs.mkdirSync(path.join(certsDir, 'nested'), { recursive: true })
fs.mkdirSync(path.join(keysDir, 'archive'), { recursive: true })
// 生成两套测试用自签 TLS 证书（一套同目录、一套跨目录软链接），覆盖自动跨目录配对与多证书选择
const genSelfSigned = (certPath, keyPath, cn) => {
  execFileSync('openssl', [
    'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1',
    '-keyout', keyPath, '-out', certPath, '-days', '30', '-nodes',
    '-subj', `/CN=${cn}`, '-addext', `subjectAltName=DNS:${cn},DNS:localhost,IP:127.0.0.1`,
  ], { stdio: 'ignore' })
}
genSelfSigned(path.join(certsDir, 'alpha.crt'), path.join(certsDir, 'alpha.key'), 'alpha.tls.test')
genSelfSigned(path.join(certsDir, 'nested', 'beta.pem'), path.join(keysDir, 'archive', 'beta-1.key'), 'beta.tls.test')
fs.symlinkSync(path.join(keysDir, 'archive', 'beta-1.key'), path.join(keysDir, 'beta.key'))
const SECRET = `Features-${randomBytes(16).toString('hex')}`
const backendLogs = []
const browserErrors = []
const steps = []

let backend
let browser
let page
let httpsStreamProc
let token = ''
let targetA
let targetB
let targetGlobal
let targetGroup
let targetCustom
let socksA
let socksB
let socksSlow
let socksGlobal
let socksGroup
let socksCustom
let subServer

try {
  targetA = createBackendEchoServer('A', '203.0.113.101')
  targetB = createBackendEchoServer('B', '203.0.113.102')
  targetGlobal = createBackendEchoServer('GLOBAL', '203.0.113.201')
  targetGroup = createBackendEchoServer('GROUP', '203.0.113.202')
  targetCustom = createBackendEchoServer('CUSTOM', '203.0.113.203')
  const targetPortA = await listen(targetA)
  const targetPortB = await listen(targetB)
  const targetPortGlobal = await listen(targetGlobal)
  const targetPortGroup = await listen(targetGroup)
  const targetPortCustom = await listen(targetCustom)

  socksA = createTaggedSocks5Server(targetPortA)
  socksB = createTaggedSocks5Server(targetPortB)
  socksGlobal = createTaggedSocks5Server(targetPortGlobal)
  socksGroup = createTaggedSocks5Server(targetPortGroup)
  socksCustom = createTaggedSocks5Server(targetPortCustom)
  const socksPortA = await listen(socksA)
  const socksPortB = await listen(socksB)
  const socksPortGlobal = await listen(socksGlobal)
  const socksPortGroup = await listen(socksGroup)
  const socksPortCustom = await listen(socksCustom)

  const unusedPort = await findFreePort()
  let subHits = 0
  subServer = http.createServer((_req, res) => {
    subHits += 1
    res.writeHead(200, { 'Content-Type': 'text/plain' })
    res.end(Buffer.from(`socks5://127.0.0.1:${socksPortA}#SubNodeA`).toString('base64'))
  })
  const subPort = await listen(subServer)

  const httpPort = await findFreePort()
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
        SBPM_INBOUND_TLS_CERT_DIR: certsDir,
        SBPM_INBOUND_TLS_KEY_DIR: keysDir,
        SBPM_UPDATE_CHECK_DISABLED: '1',
        SBPM_IPCHECK_URLS: `http://127.0.0.1:${targetPortA}/ip`,
        TZ: 'Asia/Singapore',
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

  // 3. 策略出站节点：Failover（实时故障转移）+ LoadBalance（轮询分发 + 目标粘性）+ URLTest（最低延迟）
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

  socksSlow = createTaggedSocks5Server(targetPortB, 150)
  const socksPortSlow = await listen(socksSlow)
  const slowGroup = await api('POST', '/node-groups', {
    name: 'Lowest Latency NoPort',
    allocate_port: false,
    start_port: startPort + 100,
  })
  const memberSlow = await api('POST', '/nodes', {
    group_id: slowGroup.id,
    name: 'Member Slow',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortSlow }),
    auth_enabled: false,
    enabled: true,
  })
  assert.equal(memberSlow.inbound_port, 0, '慢成员不应分配入站端口')
  const urltestNode = await api('POST', '/nodes', {
    group_id: defaultGroupId,
    name: 'JP Lowest Latency',
    type: 'urltest',
    config: JSON.stringify({
      member_ids: [memberSlow.id, memberA.id],
      health_check_url: `http://127.0.0.1:${targetPortA}/`,
      health_check_interval: 15,
    }),
    auth_enabled: false,
    enabled: true,
  })
  assert.equal(JSON.parse(urltestNode.config).tolerance, 50, '最低延迟默认容差未落库为 50ms')
  await wait(async () => {
    const health = (await api('GET', `/nodes/${urltestNode.id}`)).policy_member_health || []
    return health.length === 2 && health.every((member) => member.checked_at)
  }, '最低延迟策略未完成双成员首轮健康探测')
  const resUrltest = await curl([
    '--proxy', `http://127.0.0.1:${urltestNode.inbound_port}`,
    `http://127.0.0.1:${targetPortA}/`,
  ])
  assert.equal(resUrltest.code, 0, `最低延迟策略转发失败：${resUrltest.stderr}`)
  assert.equal(resUrltest.stdout.trim(), 'backend-A', `最低延迟策略未选择更快的 Member A：${resUrltest.stdout}`)
  steps.push('policy-urltest-lowest-latency-live')

  // 3.2 策略整组关联自动同步（跟随组内排序）+ 拖拽重排实时生效 + 断链后独立管理不再受分组影响
  const groupPolicy = await api('POST', '/nodes', {
    group_id: defaultGroupId,
    name: 'Group Sync Policy',
    type: 'failover',
    config: JSON.stringify({
      member_sources: [{ type: 'group', id: noPortGroup.id }],
      health_check_interval: 15,
    }),
    auth_enabled: false,
    enabled: true,
  })
  assert.deepEqual(JSON.parse(groupPolicy.config).member_ids, [memberA.id, memberB.id], '整组策略未按组内顺序展开')
  const beforeReorderHit = await curl([
    '--proxy', `http://127.0.0.1:${groupPolicy.inbound_port}`,
    `http://127.0.0.1:${targetPortA}/policy-before-reorder`,
  ])
  assert.equal(beforeReorderHit.code, 0, `整组策略排序前转发失败：${beforeReorderHit.stderr}`)
  assert.equal(beforeReorderHit.stdout.trim(), 'backend-A', '排序前故障转移应优先命中排在首位的 memberA')
  // 将组内节点拖动重排为 [memberB, memberA]，不编辑策略即可自动同步优先级并改变实际流量路由
  await api('POST', '/nodes/reorder', {
    group_id: noPortGroup.id,
    nodes: [
      { id: memberB.id, sort_order: 0 },
      { id: memberA.id, sort_order: 1 },
    ],
  })
  const syncedAfterReorder = await api('GET', `/nodes/${groupPolicy.id}`)
  assert.deepEqual(JSON.parse(syncedAfterReorder.config).member_ids, [memberB.id, memberA.id], '组内拖动排序未自动同步到整组策略优先级')
  const afterReorderHit = await curl([
    '--proxy', `http://127.0.0.1:${groupPolicy.inbound_port}`,
    `http://127.0.0.1:${targetPortA}/policy-after-reorder`,
  ])
  assert.equal(afterReorderHit.code, 0, `整组策略排序后转发失败：${afterReorderHit.stderr}`)
  assert.equal(afterReorderHit.stdout.trim(), 'backend-B', '拖动重排后未编辑策略，真实流量未优先走排到首位的 memberB')
  // 还原顺序后再验证新增节点同步
  await api('POST', '/nodes/reorder', {
    group_id: noPortGroup.id,
    nodes: [
      { id: memberA.id, sort_order: 0 },
      { id: memberB.id, sort_order: 1 },
    ],
  })
  const memberC = await api('POST', '/nodes', {
    group_id: noPortGroup.id,
    name: 'Member C',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortB }),
    auth_enabled: false,
    enabled: true,
  })
  const syncedAfterAdd = await api('GET', `/nodes/${groupPolicy.id}`)
  assert.deepEqual(JSON.parse(syncedAfterAdd.config).member_ids, [memberA.id, memberB.id, memberC.id], '组内新增节点未自动同步到整组策略')
  // 断链后手动调换优先级顺序，再删除 memberC 不影响其余顺序且新加入组内节点不会再同步
  await api('PUT', `/nodes/${groupPolicy.id}`, {
    group_id: defaultGroupId,
    name: groupPolicy.name,
    type: 'failover',
    config: JSON.stringify({ member_ids: [memberB.id, memberA.id], health_check_interval: 15 }),
    auth_enabled: false,
    enabled: true,
  })
  const afterUnlink = await api('GET', `/nodes/${groupPolicy.id}`)
  assert.equal(Object.hasOwn(JSON.parse(afterUnlink.config), 'member_sources'), false, '断链后仍残留 member_sources')
  assert.deepEqual(JSON.parse(afterUnlink.config).member_ids, [memberB.id, memberA.id], '断链后自定义优先级顺序未生效')
  const memberD = await api('POST', '/nodes', {
    group_id: noPortGroup.id,
    name: 'Member D',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortB }),
    auth_enabled: false,
    enabled: true,
  })
  const afterUnlinkGroupAdd = await api('GET', `/nodes/${groupPolicy.id}`)
  assert.deepEqual(JSON.parse(afterUnlinkGroupAdd.config).member_ids, [memberB.id, memberA.id], '断链后组内新增节点不应再同步到策略')
  await api('DELETE', `/nodes/${memberC.id}`)
  await api('DELETE', `/nodes/${memberD.id}`)
  steps.push('policy-group-sync-and-unlink')

  // 3.3 入站 HTTPS (TLS) 加密：两套证书扫描、多证书未选择拦截止步、选定保存、真实 HTTPS 代理转发 + TCP 复用 + HTTPS 测 IP
  const initialSettings = await api('GET', '/settings')
  assert.equal(initialSettings.inbound_tls?.ready, false, '多套证书未选择时 ready 应为 false')
  assert.equal(initialSettings.inbound_tls?.selection_mode, 'unselected')
  assert.equal(initialSettings.inbound_tls?.certificates?.length, 2, `预期扫描到 2 套有效 TLS 证书，实际：${JSON.stringify(initialSettings.inbound_tls)}`)
  const betaCert = initialSettings.inbound_tls.certificates.find((c) => c.domains?.includes('beta.tls.test'))
  assert.ok(betaCert && betaCert.key_path.endsWith('/keys/beta.key'), `跨目录软链接证书配对路径异常：${JSON.stringify(betaCert)}`)

  const httpsTargetNode = await api('POST', '/nodes', {
    group_id: defaultGroupId,
    name: 'HTTPS Direct Node',
    type: 'socks5',
    config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortA }),
    username: 'https-user',
    password: 'https-secret',
    auth_enabled: true,
    tcp_reuse_enabled: true,
    enabled: true,
  })
  const rejectedSwitch = await reqAPI('PATCH', `/nodes/${httpsTargetNode.id}`, { inbound_mode: 'https' })
  assert.equal(rejectedSwitch.status, 400, `多证书未选择时切换到 HTTPS 应被拒绝，实际 ${rejectedSwitch.status}`)

  await api('PUT', '/settings', {
    inbound_tls_cert_path: betaCert.cert_path,
    inbound_tls_key_path: betaCert.key_path,
  })
  const switchedHttps = await api('PATCH', `/nodes/${httpsTargetNode.id}`, { inbound_mode: 'https' })
  assert.equal(switchedHttps.inbound_mode, 'https')
  const httpsRes = await curl([
    '--proxy', `https://127.0.0.1:${switchedHttps.inbound_port}`,
    '--proxy-insecure',
    '--proxy-user', 'https-user:https-secret',
    `http://127.0.0.1:${targetPortA}/`,
  ])
  assert.equal(httpsRes.code, 0, `HTTPS 入站代理转发失败：${httpsRes.stderr}`)
  assert.equal(httpsRes.stdout.trim(), 'backend-A')

  // 验证 HTTPS 入站原生兼容 TCP 复用单端口路由（通过 HTTPS 端口复用路由到 lbNode / failoverNode）
  const reuseOverHttps = await curl([
    '--proxy', `https://127.0.0.1:${switchedHttps.inbound_port}`,
    '--proxy-insecure',
    '--proxy-user', `policy-user+${failoverNode.inbound_port}:policy-secret`,
    `http://127.0.0.1:${targetPortA}/`,
  ])
  assert.equal(reuseOverHttps.code, 0, `HTTPS 入站 TCP 复用转发失败：${reuseOverHttps.stderr}`)
  assert.equal(reuseOverHttps.stdout.trim(), 'backend-A')

  // 验证后端测 IP 经由 https:// 回环拨号且返回 transport: https
  const httpsIPCheck = await api('POST', `/nodes/${httpsTargetNode.id}/check-ip`)
  assert.equal(httpsIPCheck.ip, '203.0.113.101', `HTTPS 节点测 IP 异常：${JSON.stringify(httpsIPCheck)}`)
  assert.equal(httpsIPCheck.transport, 'https', `HTTPS 节点测 IP 未走 https 通道：${JSON.stringify(httpsIPCheck)}`)
  steps.push('inbound-https-tls-proxy-reuse-and-ipcheck')

  // 3.5 分组级前置代理 + 三层优先级真实转发验证（节点自定义 > 分组前置代理 > 全局前置代理）
  await api('PUT', '/settings', {
    global_upstream_enabled: true,
    global_upstream_type: 'socks5',
    global_upstream_config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortGlobal }),
  })
  const groupWithUpstream = await api('POST', '/node-groups', {
    name: 'Group Upstream A',
    allocate_port: true,
    start_port: startPort + 15,
    upstream_enabled: true,
    upstream_type: 'socks5',
    upstream_config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortGroup }),
  })
  const groupUpstreamCheck = await api('POST', `/node-groups/${groupWithUpstream.id}/upstream/check-ip`)
  assert.equal(groupUpstreamCheck.ip, '203.0.113.202', `分组前置代理测 IP 异常：${JSON.stringify(groupUpstreamCheck)}`)

  const groupPlain = await api('POST', '/node-groups', {
    name: 'Group Plain B',
    allocate_port: true,
    start_port: startPort + 22,
  })
  const nodeCustomWins = await api('POST', '/nodes', {
    group_id: groupWithUpstream.id,
    name: 'Tier Custom Wins',
    type: 'direct',
    config: '{}',
    auth_enabled: false,
    enabled: true,
    upstream_mode: 'custom',
    upstream_type: 'socks5',
    upstream_config: JSON.stringify({ server: '127.0.0.1', server_port: socksPortCustom }),
  })
  const nodeGroupFollow = await api('POST', '/nodes', {
    group_id: groupWithUpstream.id,
    name: 'Tier Group Mode',
    type: 'direct',
    config: '{}',
    auth_enabled: false,
    enabled: true,
    upstream_mode: 'group',
  })
  const nodeGroupBeatsGlobal = await api('POST', '/nodes', {
    group_id: groupWithUpstream.id,
    name: 'Tier Global Cascade To Group',
    type: 'direct',
    config: '{}',
    auth_enabled: false,
    enabled: true,
    upstream_mode: 'global',
  })
  const nodeDirectBypass = await api('POST', '/nodes', {
    group_id: groupWithUpstream.id,
    name: 'Tier Direct Bypass',
    type: 'direct',
    config: '{}',
    auth_enabled: false,
    enabled: true,
    upstream_mode: 'none',
  })
  const nodeOtherGroupGlobal = await api('POST', '/nodes', {
    group_id: groupPlain.id,
    name: 'Other Group Uses Global',
    type: 'direct',
    config: '{}',
    auth_enabled: false,
    enabled: true,
    upstream_mode: 'global',
  })
  const nodeOtherGroupOnly = await api('POST', '/nodes', {
    group_id: groupPlain.id,
    name: 'Other Group Mode Direct',
    type: 'direct',
    config: '{}',
    auth_enabled: false,
    enabled: true,
    upstream_mode: 'group',
  })

  for (const [label, node, want] of [
    ['custom > group > global', nodeCustomWins, 'backend-CUSTOM'],
    ['group mode in group A', nodeGroupFollow, 'backend-GROUP'],
    ['global mode in group A -> group beats global', nodeGroupBeatsGlobal, 'backend-GROUP'],
    ['direct mode bypasses group and global', nodeDirectBypass, 'backend-A'],
    ['global mode in group B -> system global', nodeOtherGroupGlobal, 'backend-GLOBAL'],
    ['group mode in group B -> direct (no global fallback)', nodeOtherGroupOnly, 'backend-A'],
  ]) {
    const r = await curl([
      '--socks5-hostname', `127.0.0.1:${node.inbound_port}`,
      `http://127.0.0.1:${targetPortA}/`,
    ])
    assert.equal(r.code, 0, `${label} 请求失败：${r.stderr}`)
    assert.equal(r.stdout.trim(), want, `${label} 预期 ${want}，实际 ${r.stdout.trim()}`)
  }
  steps.push('group-upstream-3tier-priority-live')

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
  // 验证失败节点红点与中性叉号在真实全栈中展示，且中文下策略节点名称与摘要完整中文化
  assert.equal(await page.$eval(`[data-testid="node-health-${deadNode.id}"]`, (el) => el.dataset.health), 'failed')
  assert.ok(await page.$(`[data-row-id="${deadNode.id}"] [data-testid="latency-failed"]`))
  assert.equal(await page.$eval(`[data-row-id="${failoverNode.id}"] .dusk-col--type`, (el) => el.textContent.trim()), '故障转移')
  assert.equal(await page.$eval(`[data-row-id="${failoverNode.id}"] .dusk-col--server`, (el) => el.textContent.trim()), '2 · 故障转移')
  assert.equal(await page.$eval(`[data-row-id="${lbNode.id}"] .dusk-col--type`, (el) => el.textContent.trim()), '负载均衡')
  assert.equal(await page.$eval(`[data-row-id="${lbNode.id}"] .dusk-col--server`, (el) => el.textContent.trim()), '2 · 轮询')
  assert.equal(await page.$eval(`[data-row-id="${urltestNode.id}"] .dusk-col--type`, (el) => el.textContent.trim()), '最低延迟')
  assert.equal(await page.$eval(`[data-row-id="${urltestNode.id}"] .dusk-col--server`, (el) => el.textContent.trim()), '2 · 最低延迟')
  await page.waitForSelector(`[data-testid="node-https-mark-${httpsTargetNode.id}"]`, { visible: true })
  // 打开 HTTPS 节点详情验证仅展示 HTTPS 与 curl 连接方式，且可平滑切换回 Mixed 再切回 HTTPS
  await clickControl(page, `[data-testid="node-open-${httpsTargetNode.id}"]`)
  const httpsLabels = await page.$$eval('[data-testid="node-inspector"] .sb-connect .sb-url__label', (els) => els.map((el) => el.textContent.trim()))
  assert.ok(httpsLabels.includes('HTTPS') && httpsLabels.includes('curl'), `全栈 HTTPS 连接区标签异常：${httpsLabels.join(',')}`)
  assert.equal(httpsLabels.includes('HTTP') || httpsLabels.includes('SOCKS5'), false, '全栈 HTTPS 连接区不应显示明文 HTTP 或 SOCKS5')
  await clickControl(page, `[data-testid="node-open-${httpsTargetNode.id}"]`)
  // 打开系统设置验证多证书列表与当前生效卡片
  await clickControl(page, '[data-testid="open-settings"]')
  await page.waitForSelector('[data-testid="inbound-tls-multi"]', { visible: true })
  await page.waitForSelector('[data-testid="inbound-tls-selected-info"]', { visible: true })
  if (ARTIFACT_DIR) {
    fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
    await sleep(300)
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-settings-tls.png'), fullPage: true })
  }
  await closeOverlay(page)
  if (ARTIFACT_DIR) {
    fs.mkdirSync(ARTIFACT_DIR, { recursive: true })
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-nodes-overview.png'), fullPage: true })
  }

  // 切到启用了分组前置代理的分组，验证分组栏徽标、节点详情实际生效来源以及分组设置弹窗
  await clickControl(page, `[data-testid="group-tab-${groupWithUpstream.id}"]`)
  await page.waitForSelector('[data-testid="active-group-upstream-pill"]', { visible: true })
  await clickControl(page, `[data-testid="node-open-${nodeGroupBeatsGlobal.id}"]`)
  const effectiveLabel = await page.$eval(`[data-testid="node-upstream-effective-${nodeGroupBeatsGlobal.id}"]`, (el) => el.textContent.trim())
  assert.ok(effectiveLabel.includes('分组前置'), `分组优先于全局的生效来源展示异常：${effectiveLabel}`)
  if (ARTIFACT_DIR) {
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-group-upstream-overview.png'), fullPage: true })
  }
  await clickControl(page, `[data-testid="group-tab-${defaultGroupId}"]`)
  await page.waitForSelector(`[data-row-id="${failoverNode.id}"]`, { visible: true })

  // 并发慢速下载流经过 Mixed Failover 与 HTTPS 入站，检验「当前连接」实时三列视图、速率与内网/通道并发
  const streamProc = spawn('curl', [
    '--silent', '--no-buffer', '--max-time', '15', '--noproxy', '',
    '--proxy', `http://127.0.0.1:${failoverNode.inbound_port}`,
    '--proxy-user', 'policy-user:policy-secret',
    `http://127.0.0.1:${targetPortA}/stream`,
  ], { stdio: ['ignore', 'ignore', 'ignore'] })
  httpsStreamProc = spawn('curl', [
    '--silent', '--no-buffer', '--max-time', '15', '--noproxy', '',
    '--proxy', `https://127.0.0.1:${switchedHttps.inbound_port}`,
    '--proxy-insecure',
    '--proxy-user', 'https-user:https-secret',
    `http://127.0.0.1:${targetPortA}/stream`,
  ], { stdio: ['ignore', 'ignore', 'ignore'] })

  await clickControl(page, '[data-testid="nav-tab-connections"]')
  await page.waitForSelector('[data-testid="connections-panel"]', { visible: true })
  await wait(async () => {
    const rows = await page.$$('[data-testid="conn-outbound-row"]')
    return rows.length >= 1
  }, '当前连接面板未采集到活跃代理连接', 15000)
  await page.waitForFunction(() => ['mixed', 'https'].every((mode) => {
    const channel = document.querySelector(`[data-testid="conn-ch-${mode}-127.0.0.1"]`)
    return channel?.dataset.active === 'true' && Number(channel.textContent.trim()) >= 1
  }), { timeout: 15000 })
  // 等待第二帧出现速率数值
  await sleep(1600)
  const connText = await page.$eval('[data-testid="connections-topology"]', (el) => el.textContent)
  assert.ok(connText.includes('JP Failover'), `当前连接未展示策略节点名称：${connText}`)
  assert.ok(connText.includes(`:${failoverNode.inbound_port}`), `当前连接未展示入站端口：${connText}`)
  assert.equal(connText.includes('internal-policy'), false, `当前连接泄漏了内部回环标签：${connText}`)
  await page.waitForSelector('[data-testid="conn-geo-127.0.0.1"]', { visible: true })
  assert.ok(await page.$eval('[data-testid="conn-geo-127.0.0.1"]', (el) => el.textContent.includes('内网')), '当前连接未展示内网归属')
  for (const mode of ['mixed', 'https']) {
    const channel = await page.$eval(`[data-testid="conn-ch-${mode}-127.0.0.1"]`, (el) => ({
      active: el.dataset.active,
      count: Number(el.textContent.trim()),
    }))
    assert.equal(channel.active, 'true', `${mode} 通道未激活`)
    assert.ok(channel.count >= 1, `${mode} 通道未显示活跃并发数：${channel.count}`)
  }
  if (ARTIFACT_DIR) {
    await page.screenshot({ path: path.join(ARTIFACT_DIR, 'full-connections-live.png'), fullPage: true })
  }
  await stopProcess(streamProc, { graceMs: 2000 })
  await stopProcess(httpsStreamProc, { graceMs: 2000 })
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
  await stopProcess(httpsStreamProc, { graceMs: 2000 })
  await stopProcess(backend, { graceMs: 10000 })
  await closeServer(socksA)
  await closeServer(socksB)
  await closeServer(socksSlow)
  await closeServer(socksGlobal)
  await closeServer(socksGroup)
  await closeServer(socksCustom)
  await closeServer(targetA)
  await closeServer(targetB)
  await closeServer(targetGlobal)
  await closeServer(targetGroup)
  await closeServer(targetCustom)
  await closeServer(subServer)
  fs.rmSync(scratch, { recursive: true, force: true })
}
