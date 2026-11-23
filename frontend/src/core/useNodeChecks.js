// 单条和批量 IP 检测共用并发队列。
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
  const [concurrency] = useState(readConcurrency)
  const [checkingIds, setCheckingIds] = useState(() => new Set())
  const [progress, setProgress] = useState(null)
  const runsRef = useRef(new Set())
  const reservedIdsRef = useRef(new Set())
  const activeCountRef = useRef(0)
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

  const finishRun = useCallback((run) => {
    if (run.controllers.size > 0 || (!run.cancelled && run.cursor < run.ids.length)) return
    runsRef.current.delete(run)
    for (const id of run.ids) reservedIdsRef.current.delete(id)
    if (progressRunRef.current === run) {
      progressRunRef.current = null
      if (mountedRef.current) setProgress(null)
    }
    const outcome = {
      total: run.ids.length,
      completed: run.completed,
      failures: run.failures,
      stopped: run.cancelled,
      done: run.completed + run.failures.length,
    }
    run.resolve(outcome)
    if (mountedRef.current) callbacksRef.current.onFinished?.(outcome, { silent: run.silent })
  }, [])

  const cancelRun = useCallback((run) => {
    run.cancelled = true
    for (const controller of run.controllers) controller.abort()
    finishRun(run)
  }, [finishRun])

  const stopChecks = useCallback(() => {
    for (const run of runsRef.current) cancelRun(run)
  }, [cancelRun])

  // 组件卸载时终止全部进行中的检测。
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      stopChecks()
    }
  }, [stopChecks])

  const drainQueue = useCallback(function drain() {
    const execute = async (run, id, controller) => {
      try {
        const response = await api.post(`/nodes/${id}/check-ip`, null, { signal: controller.signal })
        if (run.cancelled || !mountedRef.current) return
        callbacksRef.current.onResult(id, response.data, { updateFinal: true })
        run.completed += 1
      } catch (error) {
        if (isCanceled(error, controller)) {
          cancelRun(run)
          return
        }
        const { status, data } = error.response || {}
        if (mountedRef.current && !run.cancelled) {
          if (status === 429 || status >= 500) callbacksRef.current.onClear(id)
          if (Object.prototype.hasOwnProperty.call(data || {}, 'upstream')) {
            callbacksRef.current.onResult(id, data, { updateFinal: false })
          }
          run.failures.push({ id, error: data?.error || '' })
        }
      } finally {
        run.controllers.delete(controller)
        activeCountRef.current -= 1
        markChecking(id, false)
        if (progressRunRef.current === run && !run.cancelled && mountedRef.current) {
          setProgress({ total: run.ids.length, done: run.completed + run.failures.length, failed: run.failures.length })
        }
        finishRun(run)
        drain()
      }
    }

    while (mountedRef.current && activeCountRef.current < concurrency) {
      const run = [...runsRef.current].find((candidate) => !candidate.cancelled && candidate.cursor < candidate.ids.length)
      if (!run) return
      runsRef.current.delete(run)
      runsRef.current.add(run)
      const id = run.ids[run.cursor]
      run.cursor += 1
      const controller = new AbortController()
      run.controllers.add(controller)
      activeCountRef.current += 1
      markChecking(id, true)
      void execute(run, id, controller)
    }
  }, [cancelRun, concurrency, finishRun, markChecking])

  const runChecks = useCallback(async (nodeIds, { silent = false } = {}) => {
    if (!mountedRef.current) return null
    const ids = [...new Set(nodeIds)].filter((id) => id != null && !reservedIdsRef.current.has(id))
    if (ids.length === 0) return null
    for (const id of ids) reservedIdsRef.current.add(id)

    return new Promise((resolve) => {
      const run = { ids, silent, resolve, cursor: 0, completed: 0, failures: [], cancelled: false, controllers: new Set() }
      runsRef.current.add(run)
      if (ids.length > 1 && !progressRunRef.current) {
        progressRunRef.current = run
        setProgress({ total: ids.length, done: 0, failed: 0 })
      }
      drainQueue()
    })
  }, [drainQueue])

  return { checkingIds, progress, runChecks, stopChecks }
}
