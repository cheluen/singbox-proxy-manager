const normalizeVersion = (value) => {
  if (typeof value !== 'string') {
    return ''
  }
  return value.trim()
}

export const frontendBuildVersion = normalizeVersion(import.meta.env?.VITE_APP_VERSION) || 'dev'
export const frontendBuildFingerprint =
  normalizeVersion(import.meta.env?.VITE_APP_FINGERPRINT) || frontendBuildVersion

const sessionRefreshPrefix = 'sbpm-dusk:version-refresh:'
const attemptedVersions = new Set()

export const ensureLatestFrontendBuild = (serverVersion) => {
  const normalizedServerVersion = normalizeVersion(serverVersion)
  if (!normalizedServerVersion || normalizedServerVersion === frontendBuildVersion) {
    return { refreshed: false, mismatch: false }
  }

  const refreshKey = `${sessionRefreshPrefix}${normalizedServerVersion}`
  let attempted = attemptedVersions.has(refreshKey)
  try {
    attempted ||= window.sessionStorage.getItem(refreshKey) === '1'
  } catch {
    // 禁用浏览器存储时仍用内存避免重复刷新。
  }
  if (attempted || new URL(window.location.href).searchParams.get('v') === normalizedServerVersion) {
    return {
      refreshed: false,
      mismatch: true,
      serverVersion: normalizedServerVersion,
      frontendVersion: frontendBuildVersion,
    }
  }

  attemptedVersions.add(refreshKey)
  try {
    window.sessionStorage.setItem(refreshKey, '1')
  } catch {
    // URL 版本参数跨刷新保留，存储不可用时也不会陷入循环。
  }
  const refreshURL = new URL(window.location.href)
  refreshURL.searchParams.set('v', normalizedServerVersion)
  window.location.replace(refreshURL.toString())

  return {
    refreshed: true,
    mismatch: true,
    serverVersion: normalizedServerVersion,
    frontendVersion: frontendBuildVersion,
  }
}
