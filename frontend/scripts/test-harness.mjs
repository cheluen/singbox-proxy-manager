import { accessSync, constants } from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import puppeteer from 'puppeteer-core'

const DEFAULT_BROWSER_ARGS = [
  '--no-sandbox',
  '--disable-setuid-sandbox',
  '--disable-dev-shm-usage',
  '--disable-gpu',
  '--no-first-run',
  '--no-default-browser-check',
]

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

const isLaunchTimeout = (error) =>
  error?.name === 'TimeoutError' || /WS endpoint/i.test(error?.message || '')

export async function launchBrowser({
  args = [],
  executablePath = findBrowserExecutable(),
  timeout = 60000,
  retries = 1,
  userDataDir,
  ...options
} = {}) {
  const launchOptions = {
    headless: true,
    executablePath,
    timeout,
    ...options,
    args: Array.from(new Set([...DEFAULT_BROWSER_ARGS, ...args])),
  }
  for (let attempt = 0; ; attempt += 1) {
    try {
      return await puppeteer.launch({
        ...launchOptions,
        userDataDir: attempt > 0 && userDataDir ? path.join(userDataDir, `retry-${attempt}`) : userDataDir,
      })
    } catch (error) {
      if (attempt >= retries || !isLaunchTimeout(error)) throw error
    }
  }
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
