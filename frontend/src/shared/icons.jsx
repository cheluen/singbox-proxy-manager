// 图标库（lucide）里没有的少量图形：GitHub 标记、健康状态形状、信号格，以及统一菜单图标包装。

export function MenuIcon({ icon: IconComponent, ...props }) {
  return <IconComponent size={15} strokeWidth={1.75} aria-hidden="true" {...props} />
}

export function GithubMark({ size = 16 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  )
}

// 四种健康状态用「形状 + 颜色」区分：实心圆(绿)=可用，空心环(黄)=未测，实心圆(红)=失败，带斜线的环(灰)=已禁用。
export function HealthGlyph({ health, size = 10 }) {
  const common = { width: size, height: size, viewBox: '0 0 10 10', 'aria-hidden': true, className: `sb-health-glyph sb-health-glyph--${health}` }
  if (health === 'healthy' || health === 'failed') {
    return <svg {...common}><circle cx="5" cy="5" r="4.2" fill="currentColor" /></svg>
  }
  if (health === 'disabled') {
    return (
      <svg {...common} fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
        <circle cx="5" cy="5" r="3.7" />
        <path d="M2.4 7.6 7.6 2.4" />
      </svg>
    )
  }
  return <svg {...common} fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="5" cy="5" r="3.5" /></svg>
}

// 延迟信号格：格数表达快慢（3 格 = 快），不依赖颜色。
export function SignalBars({ tone, size = 12 }) {
  const filled = { fast: 3, ok: 2, slow: 1, none: 0 }[tone] ?? 0
  return (
    <svg width={size} height={size} viewBox="0 0 12 12" aria-hidden="true" className="sb-signal">
      {[0, 1, 2].map((index) => (
        <rect
          key={index}
          x={1 + index * 3.8}
          y={8 - index * 3}
          width="2.6"
          height={3 + index * 3}
          rx="0.8"
          className={index < filled ? 'sb-signal__on' : 'sb-signal__off'}
        />
      ))}
    </svg>
  )
}

export function PinGlyph({ pinned, size = 14 }) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden="true">
      {pinned ? (
        <path fill="currentColor" d="M7 2.5h6v1.4l-1.15 1.15v3.82l2.3 2.3v1.33h-3.4V18h-1.5v-5.5h-3.4v-1.33l2.3-2.3V5.05L7 3.9V2.5Z" />
      ) : (
        <path fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.45" d="M7 2.75h6v1.1L11.8 5.1v3.75l2.25 2.3v1.1h-3.3V18m-1.5 0v-5.75h-3.3v-1.1l2.25-2.3V5.1L7 3.85v-1.1Z" />
      )}
    </svg>
  )
}
