import http from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { isDeepStrictEqual } from 'node:util'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const API_PORT = Number(process.env.E2E_API_PORT || 30023)
const FRONTEND_PORT = Number(process.env.E2E_FRONTEND_PORT || 5186)
const FRONTEND_URL = `http://127.0.0.1:${FRONTEND_PORT}`
const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const FRONTEND_PACKAGE = JSON.parse(
  fs.readFileSync(path.join(FRONTEND_ROOT, 'package.json'), 'utf8')
)

const fixtures = [
  {
    key: 'direct',
    type: 'direct',
    config: {
      override_address: '1.1.1.1',
      override_port: 53,
      proxy_protocol: 2,
      detour: 'dialer-selector',
      bind_interface: 'eth0',
      inet4_bind_address: '192.0.2.10',
      inet6_bind_address: '2001:db8::10',
      protect_path: '/run/sing-box/protect.sock',
      routing_mark: 16,
      reuse_addr: true,
      netns: 'audit-netns',
      connect_timeout: '7s',
      tcp_fast_open: true,
      tcp_multi_path: true,
      udp_fragment: false,
      domain_resolver: { server: 'local', strategy: 'prefer_ipv4' },
      network_strategy: 'hybrid',
      network_type: ['wifi', 'ethernet'],
      fallback_network_type: 'cellular',
      fallback_delay: '250ms',
      domain_strategy: 'prefer_ipv4',
    },
  },
  {
    key: 'ss',
    type: 'ss',
    config: {
      server: 'ss.example.com',
      server_port: 8388,
      method: 'aes-256-cfb',
      password: 'secret',
      plugin: 'v2ray-plugin',
      plugin_opts: 'mode=websocket',
      network: ['tcp', 'udp'],
      udp_over_tcp: { enabled: true, version: 2 },
      udp_over_tcp_options: { enabled: true, version: 2 },
      multiplex: { enabled: true, max_connections: 4 },
    },
  },
  {
    key: 'vless',
    type: 'vless',
    config: {
      server: 'vless.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000001',
      flow: 'xtls-rprx-vision',
      encryption: 'none',
      network: 'httpupgrade',
      security: 'reality',
      sni: 'sni.example.com',
      alpn: 'h2,http/1.1',
      fingerprint: 'chrome',
      public_key: 'reality-public-key',
      short_id: 'abcd',
      spider_x: '/spider',
      insecure: true,
      path: '/ws',
      headers: { Host: 'host.example.com' },
      host: 'host.example.com',
      max_early_data: 2048,
      early_data_header: 'Sec-WebSocket-Protocol',
      service_name: 'grpc-service',
      header_type: 'none',
      seed: 'seed',
      http_upgrade_path: '/upgrade',
      http_upgrade_host: 'upgrade.example.com',
      packet_encoding: 'xudp',
      multiplex: { enabled: true },
      outbound_network: ['tcp', 'udp'],
      tls_options: { enabled: true, server_name: 'sni.example.com' },
      transport_options: { type: 'httpupgrade', path: '/upgrade' },
    },
  },
  {
    key: 'vmess',
    type: 'vmess',
    selectVMessCFB: true,
    config: {
      server: 'vmess.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000002',
      alter_id: 0,
      security: 'aes-128-cfb',
      network: 'quic',
      tls: 'tls',
      sni: 'sni.example.com',
      alpn: 'h2,http/1.1',
      fingerprint: 'firefox',
      insecure: true,
      path: '/ws',
      headers: { Host: 'host.example.com' },
      host: 'host.example.com',
      max_early_data: 1024,
      early_data_header: 'Sec-WebSocket-Protocol',
      service_name: 'grpc-service',
      method: 'GET',
      http_path: ['/one', '/two'],
      header_type: 'srtp',
      seed: 'seed',
      http_upgrade_path: '/upgrade',
      http_upgrade_host: 'upgrade.example.com',
      packet_encoding: 'packetaddr',
      global_padding: true,
      authenticated_length: true,
      multiplex: { enabled: true },
      outbound_network: 'tcp',
      tls_options: { enabled: true, server_name: 'sni.example.com' },
      transport_options: { type: 'quic' },
    },
  },
  {
    key: 'hy2',
    type: 'hy2',
    config: {
      server: 'hy2.example.com',
      server_port: 443,
      server_ports: ['443', '8443'],
      password: 'secret',
      up_mbps: 10,
      down_mbps: 20,
      obfs: { type: 'salamander', password: 'obfs-secret' },
      obfs_password: 'obfs-secret',
      sni: 'sni.example.com',
      alpn: ['h3'],
      fingerprint: 'chrome',
      insecure_skip_verify: true,
      salamander_password: 'legacy-secret',
      brutal_down_mbps: 30,
      brutal_up_mbps: 40,
      network: 'udp',
      hop_interval: '30s',
      brutal_debug: true,
      tls_options: { enabled: true, server_name: 'sni.example.com' },
    },
    expectedOverrides: {
      obfs: 'salamander',
      salamander_password: '',
    },
  },
  {
    key: 'tuic',
    type: 'tuic',
    config: {
      server: 'tuic.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000003',
      password: '',
      congestion_control: 'bbr',
      udp_relay_mode: 'native',
      sni: 'sni.example.com',
      alpn: ['h3'],
      fingerprint: 'chrome',
      insecure_skip_verify: true,
      zero_rtt_handshake: true,
      udp_over_stream: true,
      heartbeat: '10s',
      network: 'udp',
      disable_sni: true,
      reduce_rtt: true,
      tls_options: { enabled: true, server_name: 'sni.example.com' },
    },
  },
  {
    key: 'trojan',
    type: 'trojan',
    config: {
      server: 'trojan.example.com',
      server_port: 443,
      password: 'secret',
      network: 'httpupgrade',
      sni: 'sni.example.com',
      alpn: ['h2', 'http/1.1'],
      fingerprint: 'chrome',
      insecure: true,
      host: 'host.example.com',
      path: '/upgrade',
      service_name: 'grpc-service',
      method: 'POST',
      headers: { 'X-Audit': 'preserve' },
      multiplex: { enabled: true },
      outbound_network: ['tcp'],
      tls_options: { enabled: true, server_name: 'sni.example.com' },
      transport_options: { type: 'httpupgrade', path: '/upgrade' },
    },
  },
  {
    key: 'anytls',
    type: 'anytls',
    config: {
      server: 'anytls.example.com',
      server_port: 443,
      password: 'secret',
      sni: 'sni.example.com',
      alpn: ['h2'],
      fingerprint: 'chrome',
      insecure: true,
      idle_session_check_interval: '30s',
      idle_session_timeout: '10m',
      min_idle_session: 2,
      tls_options: { enabled: true, server_name: 'sni.example.com' },
    },
  },
  {
    key: 'socks5',
    type: 'socks5',
    config: {
      server: 'socks5.example.com',
      server_port: 1080,
      username: 'proxy-user',
      password: 'proxy-pass',
      network: ['tcp', 'udp'],
      udp_over_tcp: { enabled: true, version: 2 },
      udp_over_tcp_options: { enabled: true, version: 2 },
    },
  },
  {
    key: 'socks5h',
    type: 'socks5h',
    config: {
      server: 'socks5h.example.com',
      server_port: 1080,
      username: 'proxy-user',
      password: 'proxy-pass',
      network: 'tcp',
      udp_over_tcp: { enabled: true, version: 2 },
      udp_over_tcp_options: { enabled: true, version: 2 },
    },
  },
  {
    key: 'http',
    type: 'http',
    config: {
      server: 'http.example.com',
      server_port: 8443,
      username: 'proxy-user',
      password: 'proxy-pass',
      tls: true,
      insecure: true,
      sni: 'sni.example.com',
      path: '/proxy-path',
      headers: { 'X-Audit': 'preserve', Host: 'http.example.com' },
      tls_options: { enabled: true, server_name: 'sni.example.com' },
    },
  },
  {
    key: 'vless-native-only-tls',
    type: 'vless',
    config: {
      server: 'vless-native.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000011',
      tls_options: {
        enabled: true,
        server_name: 'vless-native-sni.example.com',
        alpn: ['h2', 'http/1.1'],
        min_version: '1.2',
        utls: { enabled: false, fingerprint: 'chrome' },
      },
      transport_options: {
        type: 'grpc',
        service_name: 'native-grpc',
        permit_without_stream: true,
      },
    },
    expectedSavedFields: {
      security: 'tls',
      sni: 'vless-native-sni.example.com',
      alpn: 'h2,http/1.1',
      network: 'grpc',
      service_name: 'native-grpc',
    },
    expectedAbsentFields: ['fingerprint'],
  },
  {
    key: 'vless-native-disabled-reality',
    type: 'vless',
    config: {
      server: 'vless-disabled-reality.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000014',
      tls_options: {
        enabled: true,
        server_name: 'vless-disabled-reality-sni.example.com',
        min_version: '1.2',
        reality: {
          enabled: false,
          public_key: 'disabled-reality-public-key',
          short_id: 'deadbeef',
        },
      },
    },
    expectedSavedFields: {
      security: 'tls',
      sni: 'vless-disabled-reality-sni.example.com',
    },
    expectedAbsentFields: ['public_key', 'short_id'],
  },
  {
    key: 'vless-native-active-reality',
    type: 'vless',
    config: {
      server: 'vless-active-reality.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000015',
      tls_options: {
        enabled: true,
        server_name: 'vless-active-reality-sni.example.com',
        min_version: '1.3',
        utls: { enabled: true, fingerprint: 'chrome' },
        reality: {
          enabled: true,
          public_key: 'active-reality-public-key',
          short_id: '0123456789abcdef',
        },
      },
    },
    expectedSavedFields: {
      security: 'reality',
      sni: 'vless-active-reality-sni.example.com',
      fingerprint: 'chrome',
      public_key: 'active-reality-public-key',
      short_id: '0123456789abcdef',
    },
  },
  {
    key: 'vless-reality-switch-to-tls',
    type: 'vless',
    switchSecurityToTLS: true,
    config: {
      server: 'vless-switch-reality.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000016',
      security: 'reality',
      sni: 'vless-switch-reality-sni.example.com',
      fingerprint: 'chrome',
      public_key: 'switch-reality-public-key',
      short_id: 'feedface',
      tls_options: {
        enabled: true,
        server_name: 'vless-switch-reality-sni.example.com',
        utls: { enabled: true, fingerprint: 'chrome' },
        reality: {
          enabled: true,
          public_key: 'switch-reality-public-key',
          short_id: 'feedface',
        },
      },
    },
    expectedOverrides: { security: 'tls' },
    expectedAbsentFields: ['public_key', 'short_id'],
  },
  {
    key: 'vmess-native-only-tls',
    type: 'vmess',
    config: {
      server: 'vmess-native.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000012',
      security: 'auto',
      tls_options: {
        enabled: true,
        server_name: 'vmess-native-sni.example.com',
        alpn: ['h2', 'http/1.1'],
        min_version: '1.2',
      },
      transport_options: {
        type: 'ws',
        path: '/native-vmess',
        headers: { Host: 'vmess-native-host.example.com', 'X-Audit': 'preserve' },
      },
    },
    expectedSavedFields: {
      tls: 'tls',
      sni: 'vmess-native-sni.example.com',
      alpn: 'h2,http/1.1',
      network: 'ws',
      path: '/native-vmess',
    },
  },
  {
    key: 'hy2-native-only-tls',
    type: 'hy2',
    config: {
      server: 'hy2-native.example.com',
      server_port: 443,
      password: 'secret',
      tls_options: {
        enabled: true,
        server_name: 'hy2-native-sni.example.com',
        alpn: ['h3'],
        min_version: '1.3',
      },
    },
    expectedSavedFields: {
      sni: 'hy2-native-sni.example.com',
      alpn: ['h3'],
    },
  },
  {
    key: 'tuic-native-only-tls',
    type: 'tuic',
    config: {
      server: 'tuic-native.example.com',
      server_port: 443,
      uuid: '00000000-0000-0000-0000-000000000013',
      password: 'secret',
      tls_options: {
        enabled: true,
        server_name: 'tuic-native-sni.example.com',
        alpn: ['h3'],
        min_version: '1.3',
      },
    },
    expectedSavedFields: {
      sni: 'tuic-native-sni.example.com',
      alpn: ['h3'],
    },
  },
  {
    key: 'trojan-native-only-tls',
    type: 'trojan',
    config: {
      server: 'trojan-native.example.com',
      server_port: 443,
      password: 'secret',
      tls_options: {
        enabled: true,
        server_name: 'trojan-native-sni.example.com',
        alpn: ['h2', 'http/1.1'],
        min_version: '1.2',
      },
    },
    expectedSavedFields: {
      sni: 'trojan-native-sni.example.com',
      alpn: ['h2', 'http/1.1'],
    },
  },
  {
    key: 'anytls-native-only-tls',
    type: 'anytls',
    config: {
      server: 'anytls-native.example.com',
      server_port: 443,
      password: 'secret',
      tls_options: {
        enabled: true,
        server_name: 'anytls-native-sni.example.com',
        alpn: ['h2', 'http/1.1'],
        min_version: '1.2',
      },
    },
    expectedSavedFields: {
      sni: 'anytls-native-sni.example.com',
      alpn: ['h2', 'http/1.1'],
    },
  },
  {
    key: 'wireguard-peer-array',
    type: 'wireguard',
    peerArray: true,
    config: {
      local_address: ['172.16.0.2/32', '2606:4700:110:8765::2/128'],
      private_key: 'private-key',
      peers: [
        {
          server: 'engage.cloudflareclient.com',
          server_port: 2408,
          public_key: 'peer-public-key',
          pre_shared_key: 'pre-shared-key',
          allowed_ips: ['0.0.0.0/0', '::/0'],
          reserved: [162, 104, 222],
          persistent_keepalive_interval: 30,
        },
      ],
      system_interface: false,
      interface_name: '',
      mtu: 1280,
      workers: 2,
      network: 'udp',
      listen_port: 51820,
      udp_timeout: '5m',
      detour: 'direct',
      domain_resolver: { server: 'local', strategy: 'prefer_ipv4' },
      domain_resolver_strategy: 'prefer_ipv4',
      domain_resolver_options: {
        server: 'stale-resolver',
        strategy: 'prefer_ipv6',
        disable_cache: true,
      },
      routing_mark: '0x10',
      udp_fragment: true,
      connect_timeout: '5s',
    },
    expectedOverrides: {
      domain_resolver: 'local',
      domain_resolver_options: { disable_cache: true },
    },
  },
  {
    key: 'wireguard-peer-array-base64-reserved',
    type: 'wireguard',
    peerArray: true,
    config: {
      local_address: ['172.16.0.2/32'],
      private_key: 'private-key',
      peers: [
        {
          server: 'engage.cloudflareclient.com',
          server_port: 2408,
          public_key: 'peer-public-key',
          allowed_ips: ['0.0.0.0/0', '::/0'],
          // Historical encoding/json output for a Go []uint8 field.
          reserved: 'omje',
        },
      ],
      mtu: 1280,
    },
    expectedOverrides: {
      peers: [
        {
          server: 'engage.cloudflareclient.com',
          server_port: 2408,
          public_key: 'peer-public-key',
          allowed_ips: ['0.0.0.0/0', '::/0'],
          reserved: [162, 104, 222],
        },
      ],
    },
  },
  {
    key: 'wireguard-peer-array-base64-reserved-clear',
    type: 'wireguard',
    peerArray: true,
    clearPeerArray: true,
    config: {
      local_address: ['172.16.0.2/32'],
      private_key: 'private-key',
      peers: [
        {
          server: 'engage.cloudflareclient.com',
          server_port: 2408,
          public_key: 'peer-public-key',
          allowed_ips: ['0.0.0.0/0', '::/0'],
          reserved: 'omje',
        },
      ],
      mtu: 1280,
    },
    expectedOverrides: { peers: [] },
    expectedSavedFields: {
      server: 'engage.cloudflareclient.com',
      server_port: 2408,
      peer_public_key: 'peer-public-key',
      allowed_ips: ['0.0.0.0/0', '::/0'],
      reserved: [162, 104, 222],
      peers: [],
    },
  },
  {
    key: 'wireguard-peer-array-clear',
    type: 'wireguard',
    peerArray: true,
    clearPeerArray: true,
    config: {
      local_address: ['172.16.0.2/32'],
      private_key: 'private-key',
      peers: [
        {
          server: 'engage.cloudflareclient.com',
          server_port: 2408,
          public_key: 'peer-public-key',
          allowed_ips: ['0.0.0.0/0', '::/0'],
          reserved: [162, 104, 222],
        },
      ],
      mtu: 1280,
    },
    expectedOverrides: { peers: [] },
    expectedSavedFields: {
      server: 'engage.cloudflareclient.com',
      server_port: 2408,
      peer_public_key: 'peer-public-key',
      allowed_ips: ['0.0.0.0/0', '::/0'],
      reserved: [162, 104, 222],
      peers: [],
    },
  },
  {
    key: 'wireguard-native-resolver-object',
    type: 'wireguard',
    flatWireGuard: true,
    nativeResolverObject: true,
    config: {
      server: 'engage.cloudflareclient.com',
      server_port: 2408,
      local_address: ['172.16.0.2/32'],
      private_key: 'private-key',
      peer_public_key: 'peer-public-key',
      allowed_ips: ['0.0.0.0/0', '::/0'],
      domain_resolver: {
        server: 'local',
        strategy: 'prefer_ipv4',
        disable_cache: true,
        rewrite_ttl: 60,
        client_subnet: '192.0.2.0/24',
      },
    },
    expectedOverrides: { domain_resolver: 'local' },
  },
  {
    key: 'wireguard-flat',
    type: 'wireguard',
    flatWireGuard: true,
    config: {
      server: 'engage.cloudflareclient.com',
      server_port: 2408,
      local_address: ['172.16.0.2/32'],
      private_key: 'private-key',
      peer_public_key: 'peer-public-key',
      pre_shared_key: 'pre-shared-key',
      allowed_ips: ['0.0.0.0/0', '::/0'],
      reserved: [162, 104, 222],
      mtu: 1280,
      udp_fragment: false,
    },
  },
  {
    key: 'wireguard-flat-base64-reserved',
    type: 'wireguard',
    flatWireGuard: true,
    config: {
      server: 'engage.cloudflareclient.com',
      server_port: 2408,
      local_address: ['172.16.0.2/32'],
      private_key: 'private-key',
      peer_public_key: 'peer-public-key',
      allowed_ips: ['0.0.0.0/0', '::/0'],
      // Historical encoding/json output for a Go []uint8 field.
      reserved: 'omje',
    },
    expectedOverrides: { reserved: [162, 104, 222] },
  },
]

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
  const fixtureByLink = new Map(fixtures.map((fixture) => [`audit://${fixture.key}`, fixture]))

  const server = http.createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/api/version') {
      sendJSON(res, 200, { version: FRONTEND_PACKAGE.version, update: { available: false } })
      return
    }
    if (req.method === 'GET' && req.url === '/api/settings') {
      sendJSON(res, 200, { start_port: 30001, preserve_inbound_ports: true })
      return
    }
    if (req.method === 'GET' && req.url === '/api/nodes') {
      sendJSON(res, 200, [])
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

  return { server, createdPayloads }
}

const getBrowserExecutablePath = () => {
  if (process.env.PUPPETEER_EXECUTABLE_PATH) {
    return process.env.PUPPETEER_EXECUTABLE_PATH
  }
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
      // Continue looking for a browser.
    }
  }
  return undefined
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

const stopProcess = async (child) => {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    sleep(5000).then(() => child.kill('SIGKILL')),
  ])
}

const clickVisibleButton = async (page, text, timeoutMs = 10000) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const clicked = await page.evaluate((needle) => {
      const button = Array.from(document.querySelectorAll('button')).find((candidate) => {
        const rect = candidate.getBoundingClientRect()
        return (
          rect.width > 0 &&
          rect.height > 0 &&
          (candidate.textContent || '').trim() === needle &&
          !candidate.disabled
        )
      })
      if (!button) return false
      button.click()
      return true
    }, text)
    if (clicked) return
    await sleep(100)
  }
  throw new Error(`Visible button not found: ${text}`)
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
  await page.waitForSelector('#import-link-input', { visible: true, timeout: 10000 })
  await page.$eval(
    '#import-link-input',
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

const waitForNodeFormClosed = async (page) => {
  await page.waitForFunction(
    () =>
      !Array.from(document.querySelectorAll('.ant-modal .ant-form')).some((form) => {
        const rect = form.getBoundingClientRect()
        return rect.width > 0 && rect.height > 0
      }),
    { timeout: 10000 }
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

  try {
    await waitForHTTP(FRONTEND_URL, 60000)
    browser = await puppeteer.launch({
      headless: true,
      executablePath: getBrowserExecutablePath(),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    })
    const page = await browser.newPage()
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

    await page.goto(FRONTEND_URL, { waitUntil: 'networkidle2' })
    await page.waitForSelector('.dashboard-repo-link', { timeout: 15000 })

    for (const fixture of fixtures) {
      const captureIndex = mockAPI.createdPayloads.length
      await clickVisibleButton(page, 'Import Node')
      await setImportLink(page, `audit://${fixture.key}`)
      await clickVisibleButton(page, 'Parse Link')
      await waitForNodeForm(page, `Imported ${fixture.key}`)

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

    const unexpectedErrors = consoleErrors.filter((line) => !isIgnorableConsoleError(line))
    if (unexpectedErrors.length > 0) {
      throw new Error(`Unexpected browser errors: ${unexpectedErrors.join('\n')}`)
    }

    process.stdout.write(
      `${JSON.stringify({ success: true, fixtures: fixtures.map((fixture) => fixture.key) })}\n`
    )
  } catch (error) {
    throw new Error(`${error.message}\nVite logs:\n${vite.getLogs()}`)
  } finally {
    try {
      await browser?.close()
    } catch {
      // Ignore browser cleanup failures.
    }
    await stopProcess(vite.child)
    await new Promise((resolve) => mockAPI.server.close(resolve))
  }
}

run().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
