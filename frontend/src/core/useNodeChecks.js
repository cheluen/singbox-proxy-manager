// 「测 IP」任务调度：并发执行、可中途停止、进度可观测。语义与线上面板的 runNodeIPChecks 一致。
import { useCallback, useEffect, useRef, useState } from 'react'
import api from './api'

const DEFAULT_CONCURRENCY = 3

const readConcurrency = () => {
  const raw = document.querySelector('meta[name="sbpm-batch-check-ip-concurrency"]')?.getAttribute('content')
  const parsed = Number.parseInt(String(raw || ''), 10)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : DEFAULT_CONCURRENCY
}

const isCanceled = (error, controller) =>
  controller.signal.aborted || error?.code === 'ERR_CANCELED' || error?.name === 'CanceledError'

// onResult(id, data, { updateFinal })：写回出口信息；onClear(id)：清空出口信息。
export function useNodeChecks({ onResult, onClear, onFinished }) {
  const [checkingIds, setCheckingIds] = useState(() => new Set())
  const [progress, setProgress] = useState(null)
  const runsRef = useRef(new Set())
  const reservedIdsRef = useRef(new Set())
  const slotsRef = useRef({ active: 0, waiters: [] })
  const progressRunRef = useRef(null)
  const mountedRef = useRef(false)
  const callbacksRef = useRef({ onResult, onClear, onFinished })

  useEffect(() => {
    callbacksRef.current = { onResult, onClear, onFinished }
  })

  const markChecking = useCallback((id, active) => {
    if (!mountedRef.current) return
    setCheckingIds((prev) => {
      const next = new Set(prev)
      if (active) next.add(id)
      else next.delete(id)
      return next
    })
  }, [])

  const acquireSlot = useCallback((run) => {
    if (run.cancelled) return Promise.resolve(false)
    const slots = slotsRef.current
    if (slots.active < readConcurrency()) {
      slots.active += 1
      return Promise.resolve(true)
    }
    return new Promise((resolve) => slots.waiters.push({ run, resolve }))
  }, [])

  const releaseSlot = useCallback(() => {
    const slots = slotsRef.current
    while (slots.waiters.length > 0) {
      const waiter = slots.waiters.shift()
      if (waiter.run.cancelled) {
        waiter.resolve(false)
        continue
      }
      waiter.resolve(true)
      return
    }
    slots.active -= 1
  }, [])

  const stopChecks = useCallback(() => {
    for (const run of runsRef.current) {
      run.cancelled = true
      run.controllers.forEach((controller) => controller.abort())
      run.controllers.clear()
    }
    for (const waiter of slotsRef.current.waiters.splice(0)) waiter.resolve(false)
  }, [])

  // 组件卸载时终止全部进行中的检测。
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      stopChecks()
    }
  }, [stopChecks])

  const runChecks = useCallback(async (nodeIds, { silent = false } = {}) => {
    if (!mountedRef.current) return null
    const ids = Array.from(new Set(nodeIds)).filter((id) => id !== null && id !== undefined && !reservedIdsRef.current.has(id))
    if (ids.length === 0) return null
    for (const id of ids) reservedIdsRef.current.add(id)

    const run = { cancelled: false, controllers: new Set() }
    runsRef.current.add(run)
    const total = ids.length
    const failures = []
    let completed = 0
    let cursor = 0
    const showProgress = total > 1 && !progressRunRef.current
    if (showProgress) {
      progressRunRef.current = run
      setProgress({ total, done: 0, failed: 0 })
    }

    const worker = async () => {
      while (!run.cancelled) {
        const index = cursor
        cursor += 1
        if (index >= total) return
        const id = ids[index]
        const controller = new AbortController()
        run.controllers.add(controller)
        markChecking(id, true)
        let acquired = false
        try {
          // 单条检测与多个批量任务共享并发预算，而不是各自再开一组请求。
          acquired = await acquireSlot(run)
          if (!acquired || run.cancelled || !mountedRef.current) return
          const response = await api.post(`/nodes/${id}/check-ip`, null, { signal: controller.signal })
          if (run.cancelled || !mountedRef.current) return
          callbacksRef.current.onResult(id, response.data, { updateFinal: true })
          completed += 1
        } catch (error) {
          if (isCanceled(error, controller)) {
            run.cancelled = true
            return
          }
          const status = error?.response?.status
          if (mountedRef.current && typeof status === 'number' && (status === 429 || status >= 500)) {
            callbacksRef.current.onClear(id)
          }
          if (mountedRef.current && Object.prototype.hasOwnProperty.call(error?.response?.data || {}, 'upstream')) {
            callbacksRef.current.onResult(id, error.response.data, { updateFinal: false })
          }
          failures.push({ id, error: error?.response?.data?.error || '' })
        } finally {
          run.controllers.delete(controller)
          if (acquired) releaseSlot()
          markChecking(id, false)
          if (showProgress && !run.cancelled && mountedRef.current) {
            setProgress({ total, done: completed + failures.length, failed: failures.length })
          }
        }
      }
    }

    await Promise.all(Array.from({ length: Math.min(readConcurrency(), total) }, worker))
    runsRef.current.delete(run)
    for (const id of ids) reservedIdsRef.current.delete(id)
    if (showProgress) {
      progressRunRef.current = null
      if (mountedRef.current) setProgress(null)
    }

    const outcome = { total, completed, failures, stopped: run.cancelled, done: completed + failures.length }
    if (mountedRef.current) callbacksRef.current.onFinished?.(outcome, { silent })
    return outcome
  }, [acquireSlot, markChecking, releaseSlot])

  return { checkingIds, progress, runChecks, stopChecks }
}
