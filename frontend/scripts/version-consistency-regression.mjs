import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const SCRIPT_PATH = fileURLToPath(import.meta.url)
const FRONTEND_ROOT = path.resolve(path.dirname(SCRIPT_PATH), '..')
const PROJECT_ROOT = path.resolve(FRONTEND_ROOT, '..')

const readText = (relativePath) =>
  fs.readFileSync(path.join(PROJECT_ROOT, relativePath), 'utf8').trim()

const readJSON = (relativePath) =>
  JSON.parse(fs.readFileSync(path.join(PROJECT_ROOT, relativePath), 'utf8'))

const backendVersion = readText('internal/version/version.txt')
const frontendPackage = readJSON('frontend/package.json')
const frontendLock = readJSON('frontend/package-lock.json')
const readme = readText('README.md')
const badgeMatch = readme.match(/shields\.io\/badge\/version-(.+?)-blue\.svg/)
const readmeVersion = badgeMatch ? decodeURIComponent(badgeMatch[1]) : ''

const versions = {
  backend: backendVersion,
  package: String(frontendPackage.version || '').trim(),
  lockfile: String(frontendLock.version || '').trim(),
  lockfileRoot: String(frontendLock.packages?.['']?.version || '').trim(),
  readme: readmeVersion,
}

const mismatches = Object.entries(versions)
  .filter(([, value]) => value !== backendVersion)
  .map(([source, value]) => ({ source, expected: backendVersion, actual: value || '<missing>' }))

if (!backendVersion) {
  throw new Error('internal/version/version.txt is empty')
}

if (mismatches.length > 0) {
  throw new Error(`Version consistency regression failed: ${JSON.stringify(mismatches)}`)
}

process.stdout.write(`${JSON.stringify({ success: true, versions })}\n`)
