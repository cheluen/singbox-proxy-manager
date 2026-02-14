const normalizeVersion = (value) => {
  if (typeof value !== 'string') {
    return ''
  }
  return value.trim()
}

export const frontendBuildVersion = normalizeVersion(import.meta.env.VITE_APP_VERSION) || 'dev'
export const frontendBuildFingerprint =
  normalizeVersion(import.meta.env.VITE_APP_FINGERPRINT) || frontendBuildVersion

const sessionRefreshPrefix = 'sbpm:version-refresh:'

export const ensureLatestFrontendBuild = (serverVersion) => {
  const normalizedServerVersion = normalizeVersion(serverVersion)
  if (!normalizedServerVersion || normalizedServerVersion === frontendBuildVersion) {
    return { refreshed: false, mismatch: false }
  }

  const refreshKey = `${sessionRefreshPrefix}${normalizedServerVersion}`
  if (window.sessionStorage.getItem(refreshKey) === '1') {
    return {
      refreshed: false,
      mismatch: true,
      serverVersion: normalizedServerVersion,
      frontendVersion: frontendBuildVersion,
    }
  }

  window.sessionStorage.setItem(refreshKey, '1')
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
