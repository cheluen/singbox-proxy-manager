export function createLatestRequestCoordinator() {
  const active = new Map()
  const invalidate = (key) => {
    active.get(key)?.abort()
    active.delete(key)
  }
  return {
    invalidate,
    abortAll() {
      for (const key of active.keys()) invalidate(key)
    },
    async run(key, request, apply, { onError, onSettled } = {}) {
      invalidate(key)
      const controller = new AbortController()
      active.set(key, controller)
      const current = () => active.get(key) === controller && !controller.signal.aborted
      try {
        const response = await request(controller.signal)
        if (current()) apply(response)
      } catch (error) {
        if (current()) onError?.(error)
      } finally {
        if (active.get(key) === controller) {
          active.delete(key)
          onSettled?.()
        }
      }
    },
  }
}
