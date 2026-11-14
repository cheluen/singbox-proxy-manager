// 轻量表单控件：原生元素 + 样式。列表行里大量出现，因此不使用更重的组件库控件。
import { useEffect, useRef } from 'react'
import { Spinner } from './primitives'

export function Toggle({ checked, onChange, busy = false, disabled = false, label, size = 'md', testId }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      data-tip={label}
      data-testid={testId}
      className={`sb-toggle sb-toggle--${size}${busy ? ' is-busy' : ''}`}
      disabled={disabled || busy}
      onClick={(event) => {
        event.stopPropagation()
        onChange(!checked)
      }}
    >
      <span className="sb-toggle__thumb">{busy ? <Spinner size={size === 'sm' ? 8 : 10} /> : null}</span>
    </button>
  )
}

export function CheckBox({ checked, indeterminate = false, onChange, label, testId, className = '' }) {
  const ref = useRef(null)
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate && !checked
  }, [indeterminate, checked])
  return (
    <label className={`sb-check ${className}`} onClick={(event) => event.stopPropagation()}>
      <input
        ref={ref}
        type="checkbox"
        checked={checked}
        aria-label={label}
        data-testid={testId}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className="sb-check__box" aria-hidden="true" />
    </label>
  )
}
