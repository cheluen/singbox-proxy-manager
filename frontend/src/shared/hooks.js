import { useCallback, useEffect, useState } from 'react'
import { readNodesVirtualThreshold } from '../core/nodeList'

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

// 正数决定首批和每次追加的行数；0 关闭分批渲染，直接展示全部节点。
export function useIncrementalList(items) {
  const [batchSize] = useState(readNodesVirtualThreshold)
  const [limit, setLimit] = useState(batchSize)
  useEffect(() => {
    setLimit((prev) => (items.length < prev - batchSize ? batchSize : prev))
  }, [batchSize, items.length])
  const [sentinel, setSentinel] = useState(null)
  // 小批次追加后哨兵可能仍在视口内，重新观察以继续填充，避免短列表卡住。
  useEffect(() => {
    if (!sentinel || batchSize === 0) return undefined
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) setLimit((prev) => prev + batchSize)
      },
      { rootMargin: '600px 0px' }
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [batchSize, limit, sentinel])
  const reveal = useCallback((index) => setLimit((prev) => Math.max(prev, index + 1)), [])
  const hasMore = batchSize > 0 && items.length > limit
  return { shown: hasMore ? items.slice(0, limit) : items, hasMore, sentinelRef: setSentinel, reveal }
}
