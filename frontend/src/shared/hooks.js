import { useCallback, useEffect, useState } from 'react'

export function useMediaQuery(query) {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches)
  useEffect(() => {
    const list = window.matchMedia(query)
    const onChange = () => setMatches(list.matches)
    onChange()
    list.addEventListener('change', onChange)
    return () => list.removeEventListener('change', onChange)
  }, [query])
  return matches
}

// 记住用户的界面偏好（视图模式等）。存储不可用时退化为普通 state。
export function useStoredState(key, initial) {
  const storageKey = `sbpm-dusk:${key}`
  const [value, setValue] = useState(() => {
    try {
      const raw = window.localStorage.getItem(storageKey)
      return raw === null ? initial : JSON.parse(raw)
    } catch {
      return initial
    }
  })
  const update = useCallback((next) => {
    setValue((prev) => {
      const resolved = typeof next === 'function' ? next(prev) : next
      try {
        window.localStorage.setItem(storageKey, JSON.stringify(resolved))
      } catch {
        // 仅本次会话生效
      }
      return resolved
    })
  }, [storageKey])
  return [value, update]
}

// 节点数量很多时分批渲染：先画一屏，其余在滚动接近底部时追加，避免一次性挂载上千行。
export function useIncrementalList(items, { initial = 80, step = 80 } = {}) {
  const [limit, setLimit] = useState(initial)
  useEffect(() => {
    setLimit((prev) => (items.length < prev - step ? initial : prev))
  }, [initial, items.length, step])
  const [sentinel, setSentinel] = useState(null)
  useEffect(() => {
    if (!sentinel) return undefined
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) setLimit((prev) => prev + step)
      },
      { rootMargin: '600px 0px' }
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [sentinel, step])
  const reveal = useCallback((index) => setLimit((prev) => Math.max(prev, index + 1)), [])
  return { shown: items.length > limit ? items.slice(0, limit) : items, hasMore: items.length > limit, sentinelRef: setSentinel, reveal }
}
