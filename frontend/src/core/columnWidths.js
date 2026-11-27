// 节点列表可调整列宽的定义、清洗与 CSS 变量映射。
// 所有列在未自定义时保持原有 CSS 轨道默认值；自定义后按像素值持久化到数据库。

export const NODE_LIST_COLUMN_KEYS = ['name', 'port', 'user', 'pass', 'type', 'server', 'region', 'ip', 'latency']

export const NODE_LIST_COLUMN_LIMITS = {
  name: { min: 84, max: 520, fallbackMin: 108 },
  port: { min: 64, max: 160, fallbackMin: 66 },
  user: { min: 64, max: 420, fallbackMin: 88 },
  pass: { min: 84, max: 480, fallbackMin: 108 },
  type: { min: 60, max: 220, fallbackMin: 98 },
  server: { min: 72, max: 560, fallbackMin: 90 },
  region: { min: 44, max: 200, fallbackMin: 54 },
  ip: { min: 72, max: 360, fallbackMin: 88 },
  latency: { min: 58, max: 160, fallbackMin: 64 },
}

export function clampColumnWidth(key, raw) {
  const limits = NODE_LIST_COLUMN_LIMITS[key]
  if (!limits) return null
  const value = Number(raw)
  if (!Number.isFinite(value)) return null
  return Math.min(limits.max, Math.max(limits.min, Math.round(value)))
}

export function normalizeColumnWidths(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
  const normalized = {}
  for (const key of NODE_LIST_COLUMN_KEYS) {
    if (!Object.hasOwn(raw, key)) continue
    const clamped = clampColumnWidth(key, raw[key])
    if (clamped !== null) normalized[key] = clamped
  }
  return normalized
}

export function columnWidthsEqual(a, b) {
  const left = normalizeColumnWidths(a)
  const right = normalizeColumnWidths(b)
  const leftKeys = Object.keys(left)
  const rightKeys = Object.keys(right)
  if (leftKeys.length !== rightKeys.length) return false
  return leftKeys.every((key) => left[key] === right[key])
}

const FALLBACK_MIN_BY_BREAKPOINT = {
  wide: { name: 108, port: 66, user: 88, pass: 108, type: 98, server: 90, region: 54, ip: 88, latency: 64 },
  medium: { name: 96, port: 66, user: 88, pass: 112, type: 76, server: 90, region: 52, latency: 62 },
  compact: { name: 80, port: 65, user: 88, pass: 104, type: 76, region: 50, latency: 60 },
}

// 依据当前视口断点计算可见列组合及表格最小总宽，确保自定义列宽超出视口时可水平滚动而不挤压重叠。
export function getVisibleColumnKeys(viewportWidth) {
  if (viewportWidth <= 1060) return ['name', 'port', 'user', 'pass', 'type', 'region', 'latency']
  if (viewportWidth <= 1280) return ['name', 'port', 'user', 'pass', 'type', 'server', 'region', 'latency']
  return NODE_LIST_COLUMN_KEYS
}

export function calculateTableMinWidth(widths, viewportWidth = 1440) {
  const normalized = normalizeColumnWidths(widths)
  if (Object.keys(normalized).length === 0) return 0
  const tier = viewportWidth <= 1060 ? 'compact' : viewportWidth <= 1280 ? 'medium' : 'wide'
  const visible = getVisibleColumnKeys(viewportWidth)
  const fallbackMin = FALLBACK_MIN_BY_BREAKPOINT[tier]
  const metaCount = visible.length - 1
  // 精确计入外层固定轨道 + 8 个外层 column-gap + 左右内边距（12px），避免临界宽度偏差。
  const outerChrome = tier === 'compact' ? 250 : tier === 'medium' ? 272 : 290
  const metaGap = tier === 'compact' ? 6 : tier === 'medium' ? 8 : 10
  let total = outerChrome + Math.max(0, metaCount - 1) * metaGap
  for (const key of visible) {
    total += normalized[key] ?? fallbackMin[key]
  }
  return total
}

export function buildColumnWidthStyle(widths, desktop, viewportWidth = 1440) {
  if (!desktop) return undefined
  const normalized = normalizeColumnWidths(widths)
  const entries = Object.entries(normalized)
  if (entries.length === 0) return undefined
  const style = {}
  for (const [key, px] of entries) {
    style[`--dusk-col-${key}`] = `${px}px`
  }
  const minWidth = calculateTableMinWidth(normalized, viewportWidth)
  if (minWidth > 0) style['--dusk-table-min-width'] = `${minWidth}px`
  return style
}
