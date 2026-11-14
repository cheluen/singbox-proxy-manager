// 词条审计：源码里引用的每个 key 必须在中英文词条里都存在；中英文新增词条的 key 集合必须一致。
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'

const SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'src')
const load = async (name) => {
  const file = path.join(SRC, 'core', 'i18n', name)
  return existsSync(file) ? (await import(pathToFileURL(file).href)).default : {}
}
const zh = { ...(await load('base-zh.js')), ...(await load('extra-zh.js')) }
const en = { ...(await load('base-en.js')), ...(await load('extra-en.js')) }
const extraZh = await load('extra-zh.js')
const extraEn = await load('extra-en.js')

const walk = (dir) =>
  readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name)
    return entry.isDirectory() ? walk(full) : /\.(jsx?|mjs)$/.test(entry.name) ? [full] : []
  })

const used = new Map()
const note = (key, file) => used.set(key, [...(used.get(key) || []), path.relative(SRC, file)])
// 动态拼接的 key：在这里列出其全部取值。
const DYNAMIC = {
  'upstream_mode_hint_': ['none', 'global', 'custom', 'legacy'],
  'import_source_': ['auto', 'subscription', 'http_proxy'],
}
const DYNAMIC_HINT = ['auto', 'subscription', 'http_proxy'].map((value) => `import_source_${value}_hint`)

for (const file of walk(SRC)) {
  if (file.includes(`${path.sep}i18n${path.sep}`)) continue
  const text = readFileSync(file, 'utf8')
  for (const match of text.matchAll(/\bt\(\s*'([a-z0-9_]+)'/g)) note(match[1], file)
  for (const match of text.matchAll(/\bt\(\s*[a-zA-Z]+\s*\?\s*'([a-z0-9_]+)'\s*:\s*'([a-z0-9_]+)'/g)) { note(match[1], file); note(match[2], file) }
  for (const match of text.matchAll(/key:\s*'((?:login_point|demo)_[a-z0-9_]+)'/g)) note(match[1], file)
  for (const match of text.matchAll(/\bt\(`([a-z0-9_]+)\$\{/g)) {
    const prefix = match[1]
    if (DYNAMIC[prefix]) {
      for (const value of DYNAMIC[prefix]) note(`${prefix}${value}`, file)
      if (prefix === 'import_source_') for (const key of DYNAMIC_HINT) note(key, file)
    } else {
      note(`<unknown dynamic prefix: ${prefix}>`, file)
    }
  }
}
// GROUP_TITLE 等通过对象间接引用的 key
for (const key of ['form_group_protocol', 'form_group_transport', 'form_group_tls', 'form_group_advanced']) note(key, path.join(SRC, 'shared', 'NodeEditor.jsx'))

const missingZh = [...used.keys()].filter((key) => !(key in zh)).sort()
const missingEn = [...used.keys()].filter((key) => !(key in en)).sort()
const onlyZh = Object.keys(extraZh).filter((key) => !(key in extraEn))
const onlyEn = Object.keys(extraEn).filter((key) => !(key in extraZh))
const unusedExtra = Object.keys(extraZh).filter((key) => !used.has(key))

const print = (title, list) => { if (list.length) process.stdout.write(`${title} (${list.length}):\n  ${list.join('\n  ')}\n`) }
print('中文缺失', process.argv.includes('--where') ? missingZh.map((key) => `${key}  <- ${[...new Set(used.get(key))].join(', ')}`) : missingZh)
print('英文缺失', missingEn.filter((key) => !missingZh.includes(key)))
print('仅中文新增词条有', onlyZh)
print('仅英文新增词条有', onlyEn)
print('新增词条未被使用', unusedExtra)
const failed = missingZh.length + missingEn.length + onlyZh.length + onlyEn.length + unusedExtra.length
process.stdout.write(`引用 ${used.size} 个 key；${failed === 0 ? '词条审计通过' : `词条审计失败：${failed} 项`}\n`)
process.exit(failed === 0 ? 0 : 1)
