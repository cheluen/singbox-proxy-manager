import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const packageJson = JSON.parse(
  readFileSync(new URL('./package.json', import.meta.url), 'utf8')
)
const appVersion = (process.env.APP_VERSION || packageJson.version || 'dev').trim()
const appFingerprint = createHash('sha256').update(appVersion).digest('hex').slice(0, 12)
const apiTarget = process.env.VITE_API_TARGET || `http://localhost:${process.env.E2E_API_PORT || 30000}`
const nodesVirtualThresholdRaw =
  (process.env.SBPM_NODES_VIRTUAL_THRESHOLD ||
    process.env.VITE_NODES_VIRTUAL_THRESHOLD ||
    process.env.VITE_VIRTUAL_TABLE_THRESHOLD ||
    '').trim()
const nodesVirtualThresholdParsed = Number.parseInt(nodesVirtualThresholdRaw, 10)
const nodesVirtualThreshold =
  Number.isFinite(nodesVirtualThresholdParsed) && nodesVirtualThresholdParsed >= 0
    ? nodesVirtualThresholdParsed
    : 50

const batchCheckIPConcurrencyRaw =
  (process.env.SBPM_BATCH_CHECK_IP_CONCURRENCY ||
    process.env.VITE_BATCH_CHECK_IP_CONCURRENCY ||
    '').trim()
const batchCheckIPConcurrencyParsed = Number.parseInt(batchCheckIPConcurrencyRaw, 10)
const batchCheckIPConcurrency =
  Number.isFinite(batchCheckIPConcurrencyParsed) && batchCheckIPConcurrencyParsed > 0
    ? batchCheckIPConcurrencyParsed
    : 10

const injectBuildMetaPlugin = () => ({
  name: 'inject-build-meta',
  transformIndexHtml(html) {
    return html.replace(
      '</head>',
      `    <meta name="sbpm-build-version" content="${appVersion}" />\n    <meta name="sbpm-build-fingerprint" content="${appFingerprint}" />\n    <meta name="sbpm-nodes-virtual-threshold" content="${nodesVirtualThreshold}" />\n    <meta name="sbpm-batch-check-ip-concurrency" content="${batchCheckIPConcurrency}" />\n  </head>`
    )
  },
})

export default defineConfig({
  plugins: [react(), injectBuildMetaPlugin()],
  define: {
    'import.meta.env.VITE_APP_VERSION': JSON.stringify(appVersion),
    'import.meta.env.VITE_APP_FINGERPRINT': JSON.stringify(appFingerprint),
  },
  server: {
    proxy: {
      '/api': {
        target: apiTarget,
        changeOrigin: true,
      }
    }
  }
})
