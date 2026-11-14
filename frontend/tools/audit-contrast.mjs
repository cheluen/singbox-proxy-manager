// 配色对比度审计：按 WCAG 2.x 计算每套主题里实际会出现的「前景/背景」组合。
// 文本需 >= 4.5:1，非文本界面元素（开关、图标、状态点、焦点环）需 >= 3:1。任何一项不达标则退出码为 1。
import process from 'node:process'
import { THEMES } from '../src/theme/palettes.js'

const channel = (value) => {
  const c = value / 255
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
}
const luminance = (hex) => {
  const n = Number.parseInt(hex.slice(1), 16)
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255)
}
const contrast = (a, b) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const TEXT = 4.5
const UI = 3
// [前景, 背景, 最低对比度, 用途]
const pairs = (c, dark) => [
  ['ink', 'bg', TEXT, '正文 / 页面背景'], ['ink', 'surface', TEXT, '正文 / 卡片'], ['ink', 'surface2', TEXT, '正文 / 次级表面'],
  ['ink2', 'bg', TEXT, '次要文字 / 页面背景'], ['ink2', 'surface', TEXT, '次要文字 / 卡片'], ['ink2', 'surface2', TEXT, '次要文字 / 次级表面'],
  ['ink3', 'bg', TEXT, '辅助文字 / 页面背景'], ['ink3', 'surface', TEXT, '辅助文字 / 卡片'], ['ink3', 'surface2', TEXT, '辅助文字 / 次级表面'],
  ['ink3', 'bg2', TEXT, '辅助文字 / 凹陷背景'], ['ink', 'bg2', TEXT, '正文 / 凹陷背景'],
  ['accentInk', 'accent', TEXT, '主按钮文字'], ['accentInk', 'accentStrong', TEXT, '主按钮文字（悬停）'],
  ['ink', 'accentSoft', TEXT, '选中行文字'], ['ink2', 'accentSoft', TEXT, '选中行次要文字'],
  [dark ? 'accent' : 'accentStrong', 'surface', TEXT, '链接 / 强调文字'], [dark ? 'accent' : 'accentStrong', 'accentSoft', TEXT, '强调文字 / 强调底'],
  ['okInk', 'okSoft', TEXT, '「可用」标签'], ['warnInk', 'warnSoft', TEXT, '「未检测」标签'], ['badInk', 'badSoft', TEXT, '「错误」标签'],
  ['okInk', 'surface', TEXT, '成功文字'], ['warnInk', 'surface', TEXT, '警告文字'], ['badInk', 'surface', TEXT, '错误文字'],
  [dark ? 'bg' : 'accentInk', dark ? 'bad' : 'badInk', TEXT, '危险按钮文字'],
  ['accent', 'surface', UI, '强调色图形 / 开关开启'], ['accent', 'bg', UI, '焦点环 / 页面背景'],
  ['muted', 'surface', UI, '开关关闭 / 禁用标记'], ['muted', 'surface2', UI, '禁用标记 / 次级表面'],
  ['ok', 'surface', UI, '「可用」图形'], ['ok', 'surface2', UI, '「可用」图形 / 次级表面'],
  ['warn', 'surface', UI, '「未检测」图形'], ['warn', 'surface2', UI, '「未检测」图形 / 次级表面'],
  ['bad', 'surface', UI, '「错误」图形'], ['bad', 'surface2', UI, '「错误」图形 / 次级表面'],
]

let failures = 0
for (const [id, theme] of Object.entries(THEMES)) {
  const c = theme.colors
  const rows = pairs(c, theme.dark).map(([fg, bg, min, usage]) => {
    const ratio = contrast(c[fg], c[bg])
    const pass = ratio >= min
    if (!pass) failures += 1
    return { pass, text: `${pass ? 'PASS' : 'FAIL'}  ${ratio.toFixed(2).padStart(5)} (>=${min})  ${fg.padEnd(12)} on ${bg.padEnd(11)} ${usage}` }
  })
  const worst = Math.min(...pairs(c, theme.dark).map(([fg, bg]) => contrast(c[fg], c[bg])))
  const failed = rows.filter((row) => !row.pass)
  process.stdout.write(`\n[${id}] ${rows.length} 组，最低 ${worst.toFixed(2)}，未通过 ${failed.length}\n`)
  const verbose = process.argv.includes('--verbose')
  for (const row of verbose ? rows : failed) process.stdout.write(`  ${row.text}\n`)
}
process.stdout.write(failures === 0 ? '\n对比度审计通过\n' : `\n对比度审计失败：${failures} 项\n`)
process.exit(failures === 0 ? 0 : 1)
