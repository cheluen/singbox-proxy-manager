const restartReads = new Set(['/api/nodes', '/api/node-groups', '/api/settings', '/api/runtime/status', '/api/version'])
const revokedReads = new Set(['/api/nodes', '/api/node-groups', '/api/settings', '/api/runtime/status'])

export function isExpectedSystemBrowserError({ text, url }, { origin, restarting = false, revoking = false }) {
  let resource
  try { resource = new URL(url) } catch { return false }
  if (resource.origin !== origin) return false
  if (resource.pathname === '/api/nodes/batch-import' && /\b400\b/.test(text)) return true
  if (restarting && restartReads.has(resource.pathname) && text.includes('net::ERR_CONNECTION_REFUSED')) return true
  return revoking && revokedReads.has(resource.pathname) && /\b401\b/.test(text)
}
