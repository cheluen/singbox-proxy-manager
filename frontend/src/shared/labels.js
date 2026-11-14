// 文案映射（纯函数）。与组件分开放置，保证组件文件只导出组件。
export const healthLabel = (health, t) =>
  ({ healthy: t('status_healthy'), unverified: t('status_unverified_short'), disabled: t('disabled') })[health]

export const upstreamModeLabel = (mode, t) =>
  ({ global: t('upstream_follow_global'), custom: t('upstream_custom'), none: t('upstream_direct'), legacy: t('upstream_legacy') })[mode] || mode

export const statusOptions = (summary, t) => [
  { value: 'all', label: t('all'), count: summary.total },
  { value: 'healthy', label: t('status_healthy'), count: summary.healthy },
  { value: 'unverified', label: t('status_unverified_short'), count: summary.unverified },
  { value: 'disabled', label: t('disabled'), count: summary.disabled },
]

// 行点击时忽略来自按钮、开关、菜单等交互元素的点击（菜单渲染在 portal 里，但事件仍会沿 React 树冒泡）。
export const isInteractive = (event) =>
  Boolean(event.target instanceof Element && event.target.closest('button, a, input, textarea, select, label, [role="menuitem"], [role="switch"]'))
