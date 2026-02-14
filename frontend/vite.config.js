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

const injectBuildMetaPlugin = () => ({
  name: 'inject-build-meta',
  transformIndexHtml(html) {
    return html.replace(
      '</head>',
      `    <meta name="sbpm-build-version" content="${appVersion}" />\n    <meta name="sbpm-build-fingerprint" content="${appFingerprint}" />\n  </head>`
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
