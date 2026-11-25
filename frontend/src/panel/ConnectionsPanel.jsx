// 「当前连接」实时路由路径面板：按需 SSE 采集（离开页面立即断开释放）、三列拓扑布局、双层 SVG 贝塞尔流光曲线与路径高亮。
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Button } from 'antd'
import { Activity, ArrowDown, ArrowUp, ChevronDown, ChevronUp, Pause, Play, RefreshCw, Route as RouteIcon, Shield } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import api from '../core/api'
import { formatRate } from '../core/format'
import { formatUpstreamLabel } from '../shared/labels'
import { EmptyState } from '../shared/primitives'

const COLLAPSE_THRESHOLD = 12
const POLL_FALLBACK_MS = 1500

function bezierPath(x1, y1, x2, y2) {
  const dx = Math.max(24, Math.abs(x2 - x1) * 0.5)
  return `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`
}

function cubicMidpoint(x1, y1, x2, y2) {
  const dx = Math.max(24, Math.abs(x2 - x1) * 0.5)
  const cx1 = x1 + dx
  const cx2 = x2 - dx
  // B(0.5) for cubic Bezier
  const x = 0.125 * x1 + 0.375 * cx1 + 0.375 * cx2 + 0.125 * x2
  const y = 0.125 * y1 + 0.375 * y1 + 0.375 * y2 + 0.125 * y2
  return { x, y }
}

export default function ConnectionsPanel({ token }) {
  const { t, i18n } = useTranslation()
  const zh = Boolean(i18n.language?.startsWith('zh'))
  const [snapshot, setSnapshot] = useState(null)
  const [status, setStatus] = useState('connecting') // connecting | live | paused | error
  const [paused, setPaused] = useState(false)
  const [expandedAll, setExpandedAll] = useState(false)
  const [hoverTarget, setHoverTarget] = useState(null) // { kind: 'client'|'inbound'|'route', value }
  const [geometry, setGeometry] = useState({ width: 0, height: 0, leftEdges: [], rightEdges: [] })
  const [refreshTick, setRefreshTick] = useState(0)

  const stageRef = useRef(null)
  const clientRefs = useRef(new Map())
  const inboundRefs = useRef(new Map())
  const routeRefs = useRef(new Map())

  // 按需采集：挂载或恢复时连接 SSE（断开后退避重连，不支持 SSE 时降级轮询），暂停或卸载立即 abort 释放后端采集。
  useEffect(() => {
    if (paused) {
      setStatus('paused')
      return undefined
    }
    let alive = true
    let controller = null
    let activeReader = null
    let retryTimer = null
    let pollTimer = null

    const fetchOnce = async (signal) => {
      const response = await api.get('/connections', { signal })
      if (!alive) return
      setSnapshot(response.data || null)
      setStatus('live')
    }

    const startPolling = () => {
      const poll = async () => {
        if (!alive) return
        controller = new AbortController()
        try {
          await fetchOnce(controller.signal)
        } catch (error) {
          if (!alive || error?.code === 'ERR_CANCELED') return
          setStatus('error')
        }
        if (alive) pollTimer = window.setTimeout(poll, POLL_FALLBACK_MS)
      }
      void poll()
    }

    const connectSSE = async () => {
      if (!alive) return
      setStatus('connecting')
      controller = new AbortController()
      try {
        const headers = { Accept: 'text/event-stream' }
        const auth = token || api.defaults.headers.common.Authorization
        if (auth) headers.Authorization = auth.startsWith('Bearer ') ? auth : `Bearer ${auth}`
        const response = await fetch('/api/connections/stream', {
          method: 'GET',
          headers,
          signal: controller.signal,
          cache: 'no-store',
        })
        if (!alive) return
        if (response.status === 404 || response.status === 405) {
          startPolling()
          return
        }
        if (!response.ok || !response.body) {
          throw new Error(`HTTP ${response.status}`)
        }
        const reader = response.body.getReader()
        activeReader = reader
        const decoder = new TextDecoder()
        let buffer = ''
        while (alive) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          let boundary = buffer.indexOf('\n\n')
          while (boundary !== -1) {
            const chunk = buffer.slice(0, boundary)
            buffer = buffer.slice(boundary + 2)
            let eventName = 'message'
            let dataLines = []
            for (const line of chunk.split(/\r?\n/)) {
              if (line.startsWith('event:')) eventName = line.slice(6).trim()
              else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
            }
            if (eventName === 'error') {
              if (alive) setStatus('error')
            } else if (eventName === 'snapshot' && dataLines.length > 0) {
              try {
                const parsed = JSON.parse(dataLines.join('\n'))
                if (alive) {
                  setSnapshot(parsed)
                  setStatus('live')
                }
              } catch {
                // 忽略心跳或非 JSON 片段
              }
            }
            boundary = buffer.indexOf('\n\n')
          }
        }
        if (alive) {
          setStatus('connecting')
          retryTimer = window.setTimeout(connectSSE, 1200)
        }
      } catch (error) {
        if (!alive || error?.name === 'AbortError') return
        setStatus('error')
        retryTimer = window.setTimeout(connectSSE, 2000)
      }
    }

    void connectSSE()
    return () => {
      alive = false
      window.clearTimeout(retryTimer)
      window.clearTimeout(pollTimer)
      activeReader?.cancel().catch(() => {})
      controller?.abort()
    }
  }, [paused, refreshTick, token])

  const sortedRoutes = useMemo(() => {
    const list = Array.isArray(snapshot?.routes) ? [...snapshot.routes] : []
    list.sort((a, b) => {
      const rateA = (Number(a.upload_bps) || 0) + (Number(a.download_bps) || 0)
      const rateB = (Number(b.upload_bps) || 0) + (Number(b.download_bps) || 0)
      if (rateB !== rateA) return rateB - rateA
      if ((b.active_count || 0) !== (a.active_count || 0)) return (b.active_count || 0) - (a.active_count || 0)
      return String(a.key || '').localeCompare(String(b.key || ''))
    })
    return list
  }, [snapshot])

  const hasOverflow = sortedRoutes.length > COLLAPSE_THRESHOLD
  const displayedRoutes = useMemo(
    () => (hasOverflow && !expandedAll ? sortedRoutes.slice(0, COLLAPSE_THRESHOLD) : sortedRoutes),
    [expandedAll, hasOverflow, sortedRoutes]
  )

  // 按客户端 IP 与入站端口聚合左/中列节点卡片：同一 IP 连向多个入站只保留 1 张卡片。
  const { clients, inbounds, clientInboundPairs } = useMemo(() => {
    const clientMap = new Map()
    const inboundMap = new Map()
    const pairMap = new Map()

    for (const route of displayedRoutes) {
      const ip = route.client_ip || 'unknown'
      const port = Number(route.inbound_port) || 0
      const up = route.upload_bps == null ? null : Number(route.upload_bps)
      const down = route.download_bps == null ? null : Number(route.download_bps)
      const count = Number(route.active_count) || 0

      if (!clientMap.has(ip)) {
        clientMap.set(ip, { ip, activeCount: 0, inboundPorts: new Set(), routeKeys: new Set(), activeTraffic: false })
      }
      const cEntry = clientMap.get(ip)
      cEntry.activeCount += count
      cEntry.inboundPorts.add(port)
      cEntry.routeKeys.add(route.key)
      if ((up || 0) + (down || 0) > 0) cEntry.activeTraffic = true

      if (!inboundMap.has(port)) {
        inboundMap.set(port, {
          port,
          nodeName: route.inbound_node_name || '',
          activeCount: 0,
          clients: new Set(),
          routeKeys: new Set(),
          activeTraffic: false,
        })
      }
      const iEntry = inboundMap.get(port)
      iEntry.activeCount += count
      iEntry.clients.add(ip)
      iEntry.routeKeys.add(route.key)
      if ((up || 0) + (down || 0) > 0) iEntry.activeTraffic = true

      const pairKey = `${ip}|${port}`
      if (!pairMap.has(pairKey)) {
        pairMap.set(pairKey, { key: pairKey, ip, port, activeTraffic: false, routeKeys: new Set() })
      }
      const pEntry = pairMap.get(pairKey)
      pEntry.routeKeys.add(route.key)
      if ((up || 0) + (down || 0) > 0) pEntry.activeTraffic = true
    }

    return {
      clients: Array.from(clientMap.values()),
      inbounds: Array.from(inboundMap.values()),
      clientInboundPairs: Array.from(pairMap.values()),
    }
  }, [displayedRoutes])

  // 计算悬浮高亮集合：悬停左/中/右任一元素，整条关联路径同时点亮。
  const highlightSet = useMemo(() => {
    if (!hoverTarget) return null
    const routeKeys = new Set()
    const clientIPs = new Set()
    const inboundPorts = new Set()
    const pairKeys = new Set()
    for (const route of displayedRoutes) {
      const match =
        (hoverTarget.kind === 'client' && route.client_ip === hoverTarget.value) ||
        (hoverTarget.kind === 'inbound' && Number(route.inbound_port) === hoverTarget.value) ||
        (hoverTarget.kind === 'route' && route.key === hoverTarget.value)
      if (match) {
        routeKeys.add(route.key)
        clientIPs.add(route.client_ip || 'unknown')
        inboundPorts.add(Number(route.inbound_port) || 0)
        pairKeys.add(`${route.client_ip || 'unknown'}|${Number(route.inbound_port) || 0}`)
      }
    }
    return { routeKeys, clientIPs, inboundPorts, pairKeys }
  }, [displayedRoutes, hoverTarget])

  const measureGeometry = useCallback(() => {
    const stage = stageRef.current
    if (!stage) return
    const stageRect = stage.getBoundingClientRect()
    const width = Math.round(stageRect.width)
    const height = Math.round(stageRect.height)
    const leftEdges = []
    const rightEdges = []

    for (const pair of clientInboundPairs) {
      const fromEl = clientRefs.current.get(pair.ip)
      const toEl = inboundRefs.current.get(pair.port)
      if (!fromEl || !toEl) continue
      const fromRect = fromEl.getBoundingClientRect()
      const toRect = toEl.getBoundingClientRect()
      const x1 = fromRect.right - stageRect.left
      const y1 = fromRect.top + fromRect.height / 2 - stageRect.top
      const x2 = toRect.left - stageRect.left
      const y2 = toRect.top + toRect.height / 2 - stageRect.top
      leftEdges.push({
        key: pair.key,
        path: bezierPath(x1, y1, x2, y2),
        active: pair.activeTraffic,
      })
    }

    for (const route of displayedRoutes) {
      const port = Number(route.inbound_port) || 0
      const fromEl = inboundRefs.current.get(port)
      const toEl = routeRefs.current.get(route.key)
      if (!fromEl || !toEl) continue
      const fromRect = fromEl.getBoundingClientRect()
      const toRect = toEl.getBoundingClientRect()
      const x1 = fromRect.right - stageRect.left
      const y1 = fromRect.top + fromRect.height / 2 - stageRect.top
      const x2 = toRect.left - stageRect.left
      const y2 = toRect.top + toRect.height / 2 - stageRect.top
      const mid = cubicMidpoint(x1, y1, x2, y2)
      const active = (Number(route.upload_bps) || 0) + (Number(route.download_bps) || 0) > 0
      rightEdges.push({
        key: route.key,
        path: bezierPath(x1, y1, x2, y2),
        midX: mid.x,
        midY: mid.y,
        active,
        uploadBps: route.upload_bps,
        downloadBps: route.download_bps,
      })
    }

    setGeometry({ width, height, leftEdges, rightEdges })
  }, [clientInboundPairs, displayedRoutes])

  useLayoutEffect(() => {
    measureGeometry()
    const stage = stageRef.current
    if (!stage || typeof ResizeObserver === 'undefined') return undefined
    const observer = new ResizeObserver(() => measureGeometry())
    observer.observe(stage)
    window.addEventListener('resize', measureGeometry)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', measureGeometry)
    }
  }, [measureGeometry])

  const setMapRef = (mapRef, key) => (el) => {
    if (el) mapRef.current.set(key, el)
    else mapRef.current.delete(key)
  }

  const totalConnections = snapshot?.active_connections ?? sortedRoutes.reduce((acc, r) => acc + (Number(r.active_count) || 0), 0)
  const totalClients = snapshot?.client_count ?? new Set(sortedRoutes.map((r) => r.client_ip)).size
  const totalRoutes = snapshot?.route_count ?? sortedRoutes.length

  return (
    <section className="conn-panel" aria-label={t('nav_connections')} data-testid="connections-panel">
      <header className="conn-toolbar">
        <div className="conn-live" data-state={status} data-testid="connections-live-indicator">
          <span className={`conn-live__dot conn-live__dot--${status}`} aria-hidden="true" />
          <strong>
            {status === 'live' ? t('conn_live') : status === 'paused' ? t('conn_paused') : status === 'error' ? t('conn_reconnecting') : t('conn_connecting')}
          </strong>
        </div>

        <div className="conn-stats">
          <span className="conn-stat">
            <span className="conn-stat__label">{t('conn_clients')}</span>
            <strong className="mono">{totalClients}</strong>
          </span>
          <span className="conn-stat">
            <span className="conn-stat__label">{t('conn_active')}</span>
            <strong className="mono">{totalConnections}</strong>
          </span>
          <span className="conn-stat">
            <span className="conn-stat__label">{t('conn_routes')}</span>
            <strong className="mono">{totalRoutes}</strong>
          </span>
          <span className="conn-stat conn-stat--rate">
            <ArrowUp size={12} strokeWidth={2.2} aria-hidden="true" className="conn-up" />
            <strong className="mono">{formatRate(snapshot?.total_upload_bps)}</strong>
          </span>
          <span className="conn-stat conn-stat--rate">
            <ArrowDown size={12} strokeWidth={2.2} aria-hidden="true" className="conn-down" />
            <strong className="mono">{formatRate(snapshot?.total_download_bps)}</strong>
          </span>
        </div>

        <div className="conn-toolbar__actions">
          <Button
            size="small"
            icon={paused ? <Play size={13} strokeWidth={2} /> : <Pause size={13} strokeWidth={2} />}
            onClick={() => setPaused((prev) => !prev)}
            data-testid="connections-toggle-pause"
          >
            {paused ? t('conn_resume') : t('conn_pause')}
          </Button>
          <Button
            size="small"
            icon={<RefreshCw size={13} strokeWidth={2} />}
            onClick={() => {
              setPaused(false)
              setRefreshTick((v) => v + 1)
            }}
            data-testid="connections-refresh"
          >
            {t('refresh')}
          </Button>
        </div>
      </header>

      {displayedRoutes.length === 0 ? (
        <div className="conn-card">
          <EmptyState
            icon={Activity}
            title={t('conn_empty_title')}
            hint={t('conn_empty_hint')}
          />
        </div>
      ) : (
        <div className="conn-card">
          <div className="conn-columns-head">
            <span>{t('conn_col_client')}</span>
            <span>{t('conn_col_inbound')}</span>
            <span>{t('conn_col_outbound')}</span>
          </div>

          <div
            ref={stageRef}
            className={`conn-stage${highlightSet ? ' is-highlighting' : ''}`}
            data-testid="connections-topology"
            onMouseLeave={() => setHoverTarget(null)}
          >
            {/* 双层 SVG：底层画静态细线，顶层对有流量的活跃路径绘制虚线流动动画 */}
            {geometry.width > 0 && geometry.height > 0 ? (
              <svg
                className="conn-svg"
                width={geometry.width}
                height={geometry.height}
                viewBox={`0 0 ${geometry.width} ${geometry.height}`}
                aria-hidden="true"
              >
                <g className="conn-svg__base">
                  {geometry.leftEdges.map((edge) => {
                    const hl = highlightSet?.pairKeys.has(edge.key)
                    return (
                      <path
                        key={`lb-${edge.key}`}
                        d={edge.path}
                        className={`conn-curve conn-curve--base${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                      />
                    )
                  })}
                  {geometry.rightEdges.map((edge) => {
                    const hl = highlightSet?.routeKeys.has(edge.key)
                    return (
                      <path
                        key={`rb-${edge.key}`}
                        d={edge.path}
                        className={`conn-curve conn-curve--base${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                      />
                    )
                  })}
                </g>
                <g className="conn-svg__flow">
                  {geometry.leftEdges.filter((edge) => edge.active).map((edge) => {
                    const hl = highlightSet?.pairKeys.has(edge.key)
                    return (
                      <path
                        key={`lf-${edge.key}`}
                        d={edge.path}
                        className={`conn-curve conn-curve--flow${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                      />
                    )
                  })}
                  {geometry.rightEdges.filter((edge) => edge.active).map((edge) => {
                    const hl = highlightSet?.routeKeys.has(edge.key)
                    return (
                      <path
                        key={`rf-${edge.key}`}
                        d={edge.path}
                        className={`conn-curve conn-curve--flow${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                      />
                    )
                  })}
                </g>
              </svg>
            ) : null}

            {/* 连线上的速率胶囊标签 */}
            <div className="conn-pills-layer" aria-hidden="true">
              {geometry.rightEdges.map((edge) => {
                const hl = highlightSet?.routeKeys.has(edge.key)
                const hasRate = edge.uploadBps != null || edge.downloadBps != null
                return (
                  <div
                    key={`pill-${edge.key}`}
                    className={`conn-rate-pill mono${edge.active ? ' is-active' : ''}${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                    style={{ left: `${edge.midX}px`, top: `${edge.midY}px` }}
                    onMouseEnter={() => setHoverTarget({ kind: 'route', value: edge.key })}
                  >
                    {!hasRate ? (
                      <span>—</span>
                    ) : (
                      <>
                        <span className="conn-up">↑{formatRate(edge.uploadBps)}</span>
                        <span className="conn-down">↓{formatRate(edge.downloadBps)}</span>
                      </>
                    )}
                  </div>
                )
              })}
            </div>

            {/* 三列网格：客户端（紧凑） · 入站枢纽（紧凑） · 出站节点行（自适应占满剩余宽度） */}
            <div className="conn-grid">
              <div className="conn-col conn-col--clients">
                {clients.map((client) => {
                  const hl = highlightSet?.clientIPs.has(client.ip)
                  return (
                    <div
                      key={client.ip}
                      ref={setMapRef(clientRefs, client.ip)}
                      className={`conn-node conn-node--client${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                      data-testid={`conn-client-${client.ip}`}
                      onMouseEnter={() => setHoverTarget({ kind: 'client', value: client.ip })}
                    >
                      <span className="conn-node__ip mono truncate" title={client.ip}>{client.ip}</span>
                      <span className="conn-node__badge mono">{t('conn_concurrent', { count: client.activeCount })}</span>
                    </div>
                  )
                })}
              </div>

              <div className="conn-col conn-col--inbounds">
                {inbounds.map((inbound) => {
                  const hl = highlightSet?.inboundPorts.has(inbound.port)
                  return (
                    <div
                      key={inbound.port}
                      ref={setMapRef(inboundRefs, inbound.port)}
                      className={`conn-node conn-node--inbound${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                      data-testid={`conn-inbound-${inbound.port}`}
                      onMouseEnter={() => setHoverTarget({ kind: 'inbound', value: inbound.port })}
                    >
                      <span className="conn-node__port mono">:{inbound.port}</span>
                      <span className="conn-node__sub mono">{t('conn_concurrent', { count: inbound.activeCount })}</span>
                    </div>
                  )
                })}
              </div>

              <div className="conn-col conn-col--outbounds" role="list" aria-label={t('conn_col_outbound')}>
                {displayedRoutes.map((route) => {
                  const hl = highlightSet?.routeKeys.has(route.key)
                  const active = (Number(route.upload_bps) || 0) + (Number(route.download_bps) || 0) > 0
                  const badgeText = route.route_id ? route.route_id : (route.inbound_port ? `:${route.inbound_port}` : '')
                  const targetName = route.policy_member_name
                    ? `${route.outbound_node_name} → ${route.policy_member_name}`
                    : (route.outbound_node_name || t('direct_local'))
                  const inboundAria = route.inbound_port
                    ? (route.route_id ? `:${route.inbound_port} (${route.route_id})` : `:${route.inbound_port}`)
                    : (route.route_id || '—')
                  const upstreamText = formatUpstreamLabel(route.upstream_label, t, { zh, source: route.upstream_source })
                  const routeSummary = t('conn_route_aria', {
                    client: route.client_ip || 'unknown',
                    inbound: inboundAria,
                    target: upstreamText ? `${targetName} (${upstreamText})` : targetName,
                    count: route.active_count,
                    up: formatRate(route.upload_bps),
                    down: formatRate(route.download_bps),
                  })
                  return (
                    <div
                      key={route.key}
                      ref={setMapRef(routeRefs, route.key)}
                      className={`conn-node conn-node--outbound${active ? ' is-active' : ''}${hl ? ' is-highlighted' : highlightSet ? ' is-dimmed' : ''}`}
                      role="listitem"
                      aria-label={routeSummary}
                      data-testid="conn-outbound-row"
                      onMouseEnter={() => setHoverTarget({ kind: 'route', value: route.key })}
                    >
                      <div className="conn-outbound__main">
                        <span className="conn-outbound__name truncate" title={targetName}>{targetName}</span>
                        {badgeText ? (
                          <span className={`conn-outbound__tag mono${route.route_id ? ' is-route' : ''}`}>
                            {route.route_id ? <RouteIcon size={11} strokeWidth={2} aria-hidden="true" /> : null}
                            {badgeText}
                          </span>
                        ) : null}
                        {upstreamText ? (
                          <span className="conn-outbound__upstream" data-tip={upstreamText}>
                            <Shield size={11} strokeWidth={2} aria-hidden="true" />
                            <span className="truncate">{upstreamText}</span>
                          </span>
                        ) : null}
                      </div>
                      <div className="conn-outbound__metrics mono">
                        <span className="conn-outbound__count">{route.active_count}</span>
                        <span className="conn-up">↑ {formatRate(route.upload_bps)}</span>
                        <span className="conn-down">↓ {formatRate(route.download_bps)}</span>
                      </div>
                    </div>
                  )
                })}
              </div>
            </div>
          </div>

          {hasOverflow ? (
            <div className="conn-fold">
              <Button
                type="text"
                size="small"
                icon={expandedAll ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
                onClick={() => setExpandedAll((prev) => !prev)}
                data-testid="connections-fold-toggle"
              >
                {expandedAll
                  ? t('conn_collapse_routes')
                  : t('conn_expand_routes', { count: sortedRoutes.length - COLLAPSE_THRESHOLD })}
              </Button>
            </div>
          ) : null}
        </div>
      )}
    </section>
  )
}
