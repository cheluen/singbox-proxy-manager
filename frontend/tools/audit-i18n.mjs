import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { Linter } from 'eslint'
import baseZh from '../src/core/i18n/base-zh.js'
import baseEn from '../src/core/i18n/base-en.js'
import extraZh from '../src/core/i18n/extra-zh.js'
import extraEn from '../src/core/i18n/extra-en.js'

const src = fileURLToPath(new URL('../src/', import.meta.url))
const zh = { ...baseZh, ...extraZh }
const en = { ...baseEn, ...extraEn }
const sourceTypes = ['auto', 'subscription', 'http_proxy']
const templateValues = {
  'upstream_mode_hint_*': ['none', 'global', 'custom', 'legacy'],
  'import_source_*': sourceTypes,
  'import_source_*_hint': sourceTypes,
}

const walk = (directory) => readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
  const full = path.join(directory, entry.name)
  if (entry.isDirectory()) return full === path.join(src, 'core', 'i18n') ? [] : walk(full)
  return /\.(jsx?|mjs)$/.test(entry.name) ? [full] : []
})

function readTranslationKeys(expression, sourceCode) {
  if (expression?.type === 'Literal' && typeof expression.value === 'string') return [expression.value]
  if (expression?.type === 'ConditionalExpression') {
    const consequent = readTranslationKeys(expression.consequent, sourceCode)
    const alternate = readTranslationKeys(expression.alternate, sourceCode)
    return consequent && alternate ? [...consequent, ...alternate] : null
  }
  if (expression?.type === 'TemplateLiteral' && expression.expressions.length === 1) {
    const [prefix, suffix] = expression.quasis.map((quasi) => quasi.value.cooked)
    return templateValues[`${prefix}*${suffix}`]?.map((value) => `${prefix}${value}${suffix}`) ?? null
  }
  if (expression?.type === 'MemberExpression' && expression.computed && expression.object.type === 'Identifier') {
    let scope = sourceCode.getScope(expression)
    while (scope && !scope.set.has(expression.object.name)) scope = scope.upper
    const definition = scope?.set.get(expression.object.name)?.defs[0]
    const initializer = definition?.node.init
    if (definition?.parent?.kind === 'const' && initializer?.type === 'ObjectExpression') {
      const properties = initializer.properties
      if (properties.every(({ type, value }) => type === 'Property' && value.type === 'Literal' && typeof value.value === 'string')) {
        return properties.map(({ value }) => value.value)
      }
    }
  }
  return null
}

const used = new Map()
const translations = {
  create(context) {
    return {
      CallExpression(node) {
        if (node.callee.type !== 'Identifier' || node.callee.name !== 't') return
        const keys = readTranslationKeys(node.arguments[0], context.sourceCode)
        if (!keys) {
          context.report({ node, message: '无法确定翻译词条的取值' })
          return
        }
        for (const key of keys) {
          if (!used.has(key)) used.set(key, new Set())
          used.get(key).add(path.relative(src, context.filename))
        }
      },
    }
  },
}
const auditConfig = {
  files: ['**/*.{js,jsx,mjs}'],
  languageOptions: { parserOptions: { ecmaFeatures: { jsx: true } } },
  plugins: { audit: { rules: { translations } } },
  rules: { 'audit/translations': 'error' },
}
const linter = new Linter({ cwd: src })
const unresolved = walk(src).flatMap((file) =>
  linter.verify(readFileSync(file, 'utf8'), [auditConfig], { filename: file, allowInlineConfig: false }).map((message) =>
    `${path.relative(src, file)}:${message.line ?? 1} ${message.message}`))
const formatMissing = (keys) => process.argv.includes('--where') ? keys.map((key) => `${key}  <- ${[...used.get(key)].join(', ')}`) : keys
const issues = [
  ['无法审计的翻译调用或语法', unresolved],
  ['中文缺失', formatMissing([...used.keys()].filter((key) => !Object.hasOwn(zh, key)).sort())],
  ['英文缺失', formatMissing([...used.keys()].filter((key) => !Object.hasOwn(en, key)).sort())],
  ['仅中文有', Object.keys(zh).filter((key) => !Object.hasOwn(en, key))],
  ['仅英文有', Object.keys(en).filter((key) => !Object.hasOwn(zh, key))],
  ['词条未被使用', [...new Set([...Object.keys(zh), ...Object.keys(en)])].filter((key) => !used.has(key)).sort()],
]
for (const [title, list] of issues) {
  if (list.length) process.stdout.write(`${title} (${list.length}):\n  ${list.join('\n  ')}\n`)
}
const failed = issues.reduce((total, [, list]) => total + list.length, 0)
process.stdout.write(`引用 ${used.size} 个 key；${failed === 0 ? '词条审计通过' : `词条审计失败：${failed} 项`}\n`)
process.exitCode = failed === 0 ? 0 : 1
