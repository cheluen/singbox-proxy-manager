// 全局提示层：任意元素加上 data-tip 即可获得提示，整页只有一个监听器和一个浮层。
// 列表里每行都有多个带提示的图标，这样做能让大列表的渲染成本保持很低。
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

const HOVER_DELAY_MS = 380
const EDGE = 8

export function TipLayer() {
  const [tip, setTip] = useState(null)
  const boxRef = useRef(null)

  useEffect(() => {
    let timer = 0
    let current = null
    const canHover = window.matchMedia?.('(hover: hover)').matches !== false

    const hide = () => {
      window.clearTimeout(timer)
      current = null
      setTip(null)
    }
    const show = (element, delay) => {
      if (current === element) return
      window.clearTimeout(timer)
      current = element
      timer = window.setTimeout(() => {
        const text = element.isConnected ? element.getAttribute('data-tip') : ''
        if (!text) return
        const rect = element.getBoundingClientRect()
        const above = rect.top > 56
        setTip({ text, x: rect.left + rect.width / 2, y: above ? rect.top - 6 : rect.bottom + 6, above })
      }, delay)
    }
    const targetOf = (event) => (event.target instanceof Element ? event.target.closest('[data-tip]') : null)
    const onOver = (event) => {
      if (!canHover) return
      const element = targetOf(event)
      if (element) show(element, HOVER_DELAY_MS)
      else if (current) hide()
    }
    const onFocus = (event) => {
      const element = targetOf(event)
      if (element?.matches(':focus-visible')) show(element, 0)
    }
    const onKey = (event) => {
      if (event.key === 'Escape') hide()
    }
    document.addEventListener('mouseover', onOver, true)
    document.addEventListener('focusin', onFocus, true)
    document.addEventListener('focusout', hide, true)
    document.addEventListener('pointerdown', hide, true)
    document.addEventListener('keydown', onKey, true)
    window.addEventListener('scroll', hide, true)
    window.addEventListener('blur', hide)
    return () => {
      window.clearTimeout(timer)
      document.removeEventListener('mouseover', onOver, true)
      document.removeEventListener('focusin', onFocus, true)
      document.removeEventListener('focusout', hide, true)
      document.removeEventListener('pointerdown', hide, true)
      document.removeEventListener('keydown', onKey, true)
      window.removeEventListener('scroll', hide, true)
      window.removeEventListener('blur', hide)
    }
  }, [])

  // 渲染后按实际宽度修正水平位置，保证不超出视口。
  useLayoutEffect(() => {
    const box = boxRef.current
    if (!box || !tip) return
    const width = box.offsetWidth
    const left = Math.min(Math.max(tip.x - width / 2, EDGE), window.innerWidth - width - EDGE)
    box.style.left = `${Math.round(left)}px`
    box.style.top = `${Math.round(tip.above ? tip.y - box.offsetHeight : tip.y)}px`
    box.style.opacity = '1'
  }, [tip])

  if (!tip) return null
  return createPortal(
    <div ref={boxRef} className="sb-tip" role="tooltip" style={{ opacity: 0 }}>
      {tip.text}
    </div>,
    document.body
  )
}
