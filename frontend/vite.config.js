import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { parseNodesVirtualThreshold } from './src/core/nodeList.js'

const packageJson = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8'))
const appVersion = (process.env.APP_VERSION || packageJson.version || 'dev').trim()
const appFingerprint = createHash('sha256').update(appVersion).digest('hex').slice(0, 12)
const apiTarget = process.env.VITE_API_TARGET || `http://127.0.0.1:${process.env.E2E_API_PORT || 30000}`
const concurrency = Number.parseInt(process.env.SBPM_BATCH_CHECK_IP_CONCURRENCY || process.env.VITE_BATCH_CHECK_IP_CONCURRENCY || '', 10)
const batchCheckIPConcurrency = Number.isFinite(concurrency) && concurrency > 0 ? concurrency : 3
const nodesVirtualThreshold = parseNodesVirtualThreshold(process.env.SBPM_NODES_VIRTUAL_THRESHOLD)

const buildMeta = () => ({
  name: 'sbpm-build-meta',
  transformIndexHtml() {
    return [
      { tag: 'meta', attrs: { name: 'sbpm-build-version', content: appVersion }, injectTo: 'head' },
      { tag: 'meta', attrs: { name: 'sbpm-build-fingerprint', content: appFingerprint }, injectTo: 'head' },
      { tag: 'meta', attrs: { name: 'sbpm-nodes-virtual-threshold', content: String(nodesVirtualThreshold) }, injectTo: 'head' },
      { tag: 'meta', attrs: { name: 'sbpm-batch-check-ip-concurrency', content: String(batchCheckIPConcurrency) }, injectTo: 'head' },
    ]
  },
})

const vendorChunk = (id) => {
  if (!id.includes('node_modules')) return undefined
  if (/node_modules\/(react|react-dom|scheduler)\//.test(id)) return 'vendor-react'
  if (/node_modules\/(antd|@ant-design|rc-[^/]+|@rc-component)\//.test(id)) return 'vendor-antd'
  if (/node_modules\/(@dnd-kit|lucide-react|axios|i18next|react-i18next)\//.test(id)) return 'vendor-kit'
  return undefined
}

export default defineConfig({
  base: '/',
  server: {
    host: '127.0.0.1',
    proxy: { '/api': { target: apiTarget, changeOrigin: true } },
  },
  plugins: [react(), buildMeta()],
  define: {
    'import.meta.env.VITE_APP_VERSION': JSON.stringify(appVersion),
    'import.meta.env.VITE_APP_FINGERPRINT': JSON.stringify(appFingerprint),
  },
  build: {
    target: 'es2020',
    chunkSizeWarningLimit: 1400,
    rollupOptions: { output: { manualChunks: vendorChunk } },
  },
})
