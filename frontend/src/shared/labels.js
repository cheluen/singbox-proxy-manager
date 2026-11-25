// 文案映射（纯函数）。与组件分开放置，保证组件文件只导出组件。
import { protocolLabel } from '../core/nodeView.js'

export const healthLabel = (health, t) =>
  ({
    healthy: t('status_healthy'),
    untested: t('status_untested_short'),
    failed: t('status_failed_short'),
    disabled: t('disabled'),
  })[health]

export const upstreamModeLabel = (mode, t) =>
  ({
    group: t('upstream_follow_group'),
    global: t('upstream_follow_global'),
    custom: t('upstream_custom'),
    none: t('upstream_direct'),
    legacy: t('upstream_legacy'),
  })[mode] || mode

const POLICY_WARNING_KEYS = {
  'no healthy enabled members': 'policy_warn_no_healthy',
  'disabled members are skipped': 'policy_warn_disabled_skipped',
  'policy is inactive': 'policy_warn_inactive',
  'no available member nodes': 'policy_warn_no_members',
}

const UPSTREAM_SCOPE_KEYS = {
  custom: 'upstream_label_custom',
  group: 'upstream_label_group',
  global: 'upstream_label_global',
}

const KNOWN_UPSTREAM_PROTOCOLS = new Set([
  'direct', 'ss', 'vless', 'vmess', 'hy2', 'tuic', 'trojan', 'anytls', 'socks5', 'socks5h', 'http', 'wireguard',
])

export const policyWarningLabel = (raw, t) => {
  const normalized = String(raw || '').trim().toLowerCase()
  return Object.hasOwn(POLICY_WARNING_KEYS, normalized) ? t(POLICY_WARNING_KEYS[normalized]) : raw
}

export const formatUpstreamLabel = (raw, t, { zh = false, source } = {}) => {
  const text = String(raw || '').trim()
  if (!text || !zh) return text
  if (source !== undefined && !Object.hasOwn(UPSTREAM_SCOPE_KEYS, source)) return text
  const match = text.match(/^(custom|group|global): ([a-z0-9]+)$/)
  if (!match || !KNOWN_UPSTREAM_PROTOCOLS.has(match[2])) return text
  if (source !== undefined && match[1] !== source) return text
  return `${t(UPSTREAM_SCOPE_KEYS[match[1]])} · ${protocolLabel(match[2], t)}`
}

export const statusOptions = (summary, t) => [
  { value: 'all', label: t('all'), count: summary.total },
  { value: 'healthy', label: t('status_healthy'), count: summary.healthy },
  { value: 'untested', label: t('status_untested_short'), count: summary.untested },
  { value: 'failed', label: t('status_failed_short'), count: summary.failed },
  { value: 'disabled', label: t('disabled'), count: summary.disabled },
]

// 行点击时忽略来自按钮、开关、菜单等交互元素的点击（菜单渲染在 portal 里，但事件仍会沿 React 树冒泡）。
export const isInteractive = (event) =>
  Boolean(event.target instanceof Element && event.target.closest('button, a, input, textarea, select, label, [role="menuitem"], [role="switch"], [role="button"]'))
