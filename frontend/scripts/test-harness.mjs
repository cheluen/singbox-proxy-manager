import { accessSync, constants } from 'node:fs'
import process from 'node:process'

export function findBrowserExecutable() {
  const configured = process.env.PUPPETEER_EXECUTABLE_PATH || process.env.CHROME_BIN
  if (configured) return configured
  for (const candidate of [
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/usr/bin/chromium-browser',
    '/usr/bin/chromium',
  ]) {
    try {
      accessSync(candidate, constants.X_OK)
      return candidate
    } catch {
      continue
    }
  }
  throw new Error('找不到 Chrome 或 Chromium，请设置 PUPPETEER_EXECUTABLE_PATH')
}

export async function stopProcess(child, { graceMs = 8000, requireGraceful = false } = {}) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return
  const exited = new Promise((resolve) => child.once('exit', resolve))
  let forced = false
  child.kill('SIGTERM')
  const timer = setTimeout(() => {
    forced = true
    child.kill('SIGKILL')
  }, graceMs)
  try {
    await exited
  } finally {
    clearTimeout(timer)
  }
  if (forced && requireGraceful) throw new Error('后端未正常停止')
}
