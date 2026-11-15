const DEFAULT_NODES_VIRTUAL_THRESHOLD = 120

export const parseNodesVirtualThreshold = (raw) => {
  const text = String(raw ?? '').trim()
  const value = Number(text)
  return /^\+?\d+$/.test(text) && Number.isSafeInteger(value) && value >= 0
    ? value
    : DEFAULT_NODES_VIRTUAL_THRESHOLD
}

export const readNodesVirtualThreshold = () => parseNodesVirtualThreshold(
  globalThis.document?.querySelector('meta[name="sbpm-nodes-virtual-threshold"]')?.getAttribute('content')
)
