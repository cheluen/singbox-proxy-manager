// 「日志」面板：运行/连接日志（默认轻量内存缓冲 + 触底懒加载）+ 流量统计（按需持久化分钟桶聚合）+ 三目标多库轮转存储显式保存设置。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Form, Input, InputNumber, Modal, Radio, Select, Switch } from 'antd'
import { ArrowDown, ArrowUp, BarChart3, Database, HardDrive, RefreshCw, ScrollText, Search, Settings2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import api from '../core/api'
import { formatBytes } from '../core/format'
import { createLatestRequestCoordinator } from '../core/latestRequest'
import { formatDateTime } from '../core/nodeView'
import { EmptyState, Segmented, Spinner } from '../shared/primitives'

const LOG_PAGE_SIZE = 50
const RANGE_PRESETS = [
  { key: '1h', ms: 60 * 60 * 1000 },
  { key: '24h', ms: 24 * 60 * 60 * 1000 },
  { key: '7d', ms: 7 * 24 * 60 * 60 * 1000 },
  { key: '30d', ms: 30 * 24 * 60 * 60 * 1000 },
]
const RANGE_LABELS = {
  '1h': 'traffic_range_1h',
  '24h': 'traffic_range_24h',
  '7d': 'traffic_range_7d',
  '30d': 'traffic_range_30d',
}

function LogSettingsModal({ open, settings, loadingSettings, settingsError, onRetryLoad, onClose, onSaved }) {
  const { t } = useTranslation()
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)
  const [errorMsg, setErrorMsg] = useState('')
  const [replaceDsn, setReplaceDsn] = useState(false)
  const [replaceToken, setReplaceToken] = useState(false)
  const [clearToken, setClearToken] = useState(false)
  const initializedRef = useRef(false)
  const prevOpenRef = useRef(false)

  const enabled = Form.useWatch('enabled', form)
  const storeMode = Form.useWatch('store_mode', form)
  const remoteDriver = Form.useWatch('remote_driver', form)

  useEffect(() => {
    const justOpened = open && !prevOpenRef.current
    prevOpenRef.current = open
    if (!open) {
      initializedRef.current = false
      return
    }
    if (justOpened && !settings && !loadingSettings) {
      initializedRef.current = false
      onRetryLoad?.()
      return
    }
    if (!settings) {
      initializedRef.current = false
      return
    }
    if (initializedRef.current) return
    initializedRef.current = true
    setErrorMsg('')
    setReplaceDsn(!settings.has_remote_dsn)
    setReplaceToken(!settings.has_remote_token)
    setClearToken(false)
    form.setFieldsValue({
      enabled: Boolean(settings.enabled),
      min_level: settings.min_level || 'INFO',
      store_mode: settings.store_mode || 'local_sqlite',
      remote_driver: settings.remote_driver || 'postgres',
      local_sqlite_path: settings.local_sqlite_path || '',
      remote_dsn: '',
      remote_url: '',
      remote_token: '',
      retention_days: settings.retention_days ?? 7,
      max_size_mb: settings.max_size_mb ?? 200,
    })
  }, [form, loadingSettings, onRetryLoad, open, settings])

  const submit = async (values) => {
    if (!settings || settingsError) return
    setSaving(true)
    setErrorMsg('')
    try {
      const payload = {
        enabled: Boolean(values.enabled),
        min_level: values.min_level || 'INFO',
        store_mode: values.store_mode || 'local_sqlite',
        remote_driver: values.remote_driver || 'postgres',
        local_sqlite_path: String(values.local_sqlite_path || '').trim(),
        retention_days: Number(values.retention_days ?? 0),
        max_size_mb: Number(values.max_size_mb ?? 0),
        dsn_action: 'keep',
        token_action: 'keep',
      }
      if (values.store_mode === 'independent') {
        if (values.remote_driver === 'turso') {
          if (replaceDsn || String(values.remote_url || '').trim()) {
            payload.remote_url = String(values.remote_url || '').trim()
            payload.dsn_action = payload.remote_url ? 'replace' : 'keep'
          }
          const nextToken = String(values.remote_token || '').trim()
          if (clearToken) {
            payload.remote_token = ''
            payload.token_action = 'clear'
          } else if (nextToken) {
            payload.remote_token = nextToken
            payload.token_action = 'replace'
          } else if (replaceToken && settings?.has_remote_token) {
            payload.remote_token = ''
            payload.token_action = 'clear'
          }
        } else if (replaceDsn || String(values.remote_dsn || '').trim()) {
          payload.remote_dsn = String(values.remote_dsn || '').trim()
          payload.dsn_action = payload.remote_dsn ? 'replace' : 'keep'
        }
      }
      const response = await api.put('/logs/settings', payload, { timeout: 30000 })
      onSaved(response.data)
      onClose()
    } catch (error) {
      setErrorMsg(error?.response?.data?.error || t('log_settings_save_failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      title={t('log_storage_settings')}
      open={open}
      onCancel={() => { if (!saving) onClose() }}
      footer={null}
      width={640}
      destroyOnHidden
      centered
      maskClosable={false}
      keyboard={!saving}
      closable={!saving}
      rootClassName="sb-overlay"
    >
      <Form form={form} layout="vertical" requiredMark={false} className="sb-form" onFinish={submit} data-testid="log-settings-form">
        <p className="sb-dialog-lead">{t('log_settings_explicit_hint')}</p>
        {!settings && loadingSettings ? <div className="logs-loading"><Spinner size={18} /></div> : null}
        {!settings && settingsError ? (
          <Alert
            type="error"
            showIcon
            className="sb-gap"
            message={t('log_settings_load_failed')}
            description={settingsError}
            action={<Button size="small" onClick={() => onRetryLoad?.()} data-testid="log-settings-retry">{t('retry_load')}</Button>}
            data-testid="log-settings-load-error"
          />
        ) : null}
        {errorMsg ? <Alert type="error" showIcon className="sb-gap" message={t('log_settings_save_failed')} description={errorMsg} data-testid="log-settings-error" /> : null}

        <section className="sb-form-section">
          <div className="sb-setting-row">
            <div className="sb-setting-row__text">
              <strong>{t('log_persist_enable')}</strong>
              <span>{t('log_persist_enable_desc')}</span>
            </div>
            <Form.Item name="enabled" valuePropName="checked" noStyle>
              <Switch aria-label={t('log_persist_enable')} data-testid="log-persist-switch" />
            </Form.Item>
          </div>

          <Form.Item name="min_level" label={t('log_persist_level')}>
            <Select
              disabled={!enabled}
              style={{ width: 200 }}
              options={[
                { value: 'INFO', label: 'INFO' },
                { value: 'WARN', label: 'WARN' },
                { value: 'ERROR', label: 'ERROR' },
              ]}
              data-testid="log-persist-level"
            />
          </Form.Item>
        </section>

        <section className="sb-form-section">
          <h3 className="sb-form-section__title">{t('log_storage_target')}</h3>
          <Form.Item name="store_mode">
            <Radio.Group disabled={!enabled} style={{ display: 'flex', flexDirection: 'column', gap: 8 }} data-testid="log-store-mode">
              <Radio value="local_sqlite">{t('log_store_local_sqlite')}</Radio>
              <Radio value="primary">{t('log_store_primary')}</Radio>
              <Radio value="independent">{t('log_store_independent')}</Radio>
            </Radio.Group>
          </Form.Item>

          {storeMode === 'local_sqlite' ? (
            <Form.Item name="local_sqlite_path" label={t('log_local_sqlite_path')} extra={t('log_local_sqlite_path_desc')}>
              <Input disabled={!enabled} spellCheck={false} placeholder="logs.db" data-testid="log-local-path" />
            </Form.Item>
          ) : null}

          {storeMode === 'primary' ? (
            <p className="sb-form-note">{t('log_store_primary_hint')}</p>
          ) : null}

          {storeMode === 'independent' ? (
            <div className="sb-gap">
              <Form.Item name="remote_driver" label={t('log_remote_driver')}>
                <Select
                  disabled={!enabled}
                  onChange={(nextDriver) => {
                    if (nextDriver !== settings?.remote_driver) {
                      setReplaceDsn(true)
                      setReplaceToken(true)
                    }
                  }}
                  options={[
                    { value: 'sqlite', label: 'SQLite' },
                    { value: 'turso', label: 'Turso / libSQL' },
                    { value: 'mysql', label: 'MySQL / TiDB' },
                    { value: 'postgres', label: 'PostgreSQL' },
                  ]}
                  data-testid="log-remote-driver"
                />
              </Form.Item>

              {remoteDriver === 'turso' ? (
                <>
                  {settings?.remote_url_masked && !replaceDsn ? (
                    <div className="sb-inline-card">
                      <div className="sb-inline-card__text">
                        <strong>{t('log_turso_url')}</strong>
                        <span className="mono">{settings.remote_url_masked}</span>
                      </div>
                      <Button size="small" disabled={!enabled} onClick={() => { setReplaceDsn(true); if (settings?.has_remote_token) setReplaceToken(true) }}>{t('replace')}</Button>
                    </div>
                  ) : (
                    <Form.Item name="remote_url" label={t('log_turso_url')} rules={enabled ? [{ required: true, message: t('log_remote_required') }] : []}>
                      <Input disabled={!enabled} spellCheck={false} placeholder="libsql://your-db.turso.io" data-testid="log-turso-url" />
                    </Form.Item>
                  )}
                  {settings?.has_remote_token && !replaceToken && !clearToken ? (
                    <div className="sb-inline-card sb-gap">
                      <div className="sb-inline-card__text">
                        <strong>{t('log_turso_token')}</strong>
                        <span className="mono">•••••••••••• ({t('configured')})</span>
                      </div>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <Button size="small" disabled={!enabled} onClick={() => { setClearToken(false); setReplaceToken(true) }}>{t('replace')}</Button>
                        <Button size="small" danger disabled={!enabled} onClick={() => { setClearToken(true); setReplaceToken(false); form.setFieldValue('remote_token', '') }} data-testid="log-turso-token-clear">{t('log_turso_token_clear')}</Button>
                      </div>
                    </div>
                  ) : clearToken ? (
                    <div className="sb-inline-card sb-gap">
                      <div className="sb-inline-card__text">
                        <strong>{t('log_turso_token')}</strong>
                        <span className="sb-dim">{t('log_turso_token_cleared')}</span>
                      </div>
                      <Button size="small" disabled={!enabled} onClick={() => { setClearToken(false); setReplaceToken(true) }}>{t('replace')}</Button>
                    </div>
                  ) : (
                    <Form.Item name="remote_token" label={t('log_turso_token')} className="sb-gap">
                      <Input.Password disabled={!enabled} autoComplete="new-password" placeholder={t('log_turso_token_placeholder')} data-testid="log-turso-token" />
                    </Form.Item>
                  )}
                </>
              ) : (
                settings?.remote_dsn_masked && !replaceDsn ? (
                  <div className="sb-inline-card">
                    <div className="sb-inline-card__text">
                      <strong>{t('log_remote_dsn')}</strong>
                      <span className="mono" data-testid="log-remote-dsn-masked">{settings.remote_dsn_masked}</span>
                    </div>
                    <Button size="small" disabled={!enabled} onClick={() => setReplaceDsn(true)} data-testid="log-remote-dsn-replace">{t('replace')}</Button>
                  </div>
                ) : (
                  <Form.Item
                    name="remote_dsn"
                    label={t('log_remote_dsn')}
                    extra={remoteDriver === 'sqlite' ? t('log_remote_sqlite_hint') : t('log_remote_dsn_hint')}
                    rules={enabled ? [{ required: true, message: t('log_remote_required') }] : []}
                  >
                    <Input.Password
                      disabled={!enabled}
                      autoComplete="new-password"
                      placeholder={remoteDriver === 'mysql' ? 'mysql://user:pass@host:3306/db' : remoteDriver === 'postgres' ? 'postgres://user:pass@host:5432/db?sslmode=require' : '/path/to/remote-logs.db'}
                      data-testid="log-remote-dsn-input"
                    />
                  </Form.Item>
                )
              )}
            </div>
          ) : null}
        </section>

        <section className="sb-form-section">
          <h3 className="sb-form-section__title">{t('log_retention_title')}</h3>
          <div className="sb-form-grid">
            <Form.Item name="retention_days" label={t('log_retention_days')} extra={t('log_zero_unlimited')}>
              <InputNumber min={0} max={3650} disabled={!enabled} style={{ width: '100%' }} suffix={t('days')} data-testid="log-retention-days" />
            </Form.Item>
            <Form.Item name="max_size_mb" label={t('log_max_size_mb')} extra={t('log_zero_unlimited')}>
              <InputNumber min={0} max={102400} disabled={!enabled} style={{ width: '100%' }} suffix="MB" data-testid="log-max-size" />
            </Form.Item>
          </div>
        </section>

        <div className="sb-form-footer">
          <Button onClick={onClose} disabled={saving}>{t('cancel')}</Button>
          <Button type="primary" htmlType="submit" loading={saving} disabled={saving || loadingSettings || !settings || Boolean(settingsError)} data-testid="log-settings-save">
            {t('log_settings_save_apply')}
          </Button>
        </div>
      </Form>
    </Modal>
  )
}

export default function LogsPanel({ ctl }) {
  const { t, i18n } = useTranslation()
  const locale = i18n.language?.startsWith('zh') ? 'zh' : 'en'
  const [subTab, setSubTab] = useState('events') // events | traffic
  const [settings, setSettings] = useState(null)
  const [loadingSettings, setLoadingSettings] = useState(false)
  const [settingsError, setSettingsError] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)

  // 运行与连接日志状态
  const [source, setSource] = useState('')
  const [level, setLevel] = useState('')
  const [keyword, setKeyword] = useState('')
  const [clientIP, setClientIP] = useState('')
  const [events, setEvents] = useState([])
  const [nextCursor, setNextCursor] = useState('')
  const [eventsMeta, setEventsMeta] = useState({ persisted: false, dropped: 0 })
  const [eventsError, setEventsError] = useState('')
  const [loadMoreError, setLoadMoreError] = useState('')
  const [loadingEvents, setLoadingEvents] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const sentinelRef = useRef(null)

  // 流量统计状态
  const [rangePreset, setRangePreset] = useState('24h')
  const [trafficIP, setTrafficIP] = useState('')
  const [trafficPort, setTrafficPort] = useState('')
  const [trafficOutbound, setTrafficOutbound] = useState('')
  const [traffic, setTraffic] = useState(null)
  const [trafficError, setTrafficError] = useState('')
  const [loadingTraffic, setLoadingTraffic] = useState(false)

  const requestsRef = useRef(null)
  if (!requestsRef.current) {
    requestsRef.current = createLatestRequestCoordinator()
  }

  const loadSettings = useCallback(async () => {
    setLoadingSettings(true)
    setSettingsError('')
    try {
      const response = await api.get('/logs/settings')
      setSettings(response.data || null)
    } catch (error) {
      setSettings(null)
      setSettingsError(error?.response?.data?.error || t('network_error'))
    } finally {
      setLoadingSettings(false)
    }
  }, [t])

  const loadEvents = useCallback(async ({ cursor = '', append = false } = {}) => {
    if (append) {
      setLoadingMore(true)
      setLoadMoreError('')
    } else {
      setLoadingEvents(true)
      setEventsError('')
      setLoadMoreError('')
    }
    const params = { limit: LOG_PAGE_SIZE }
    if (source) params.source = source
    if (level) params.level = level
    if (keyword.trim()) params.q = keyword.trim()
    if (clientIP.trim()) params.client_ip = clientIP.trim()
    if (cursor) params.cursor = cursor
    await requestsRef.current.run(
      'events',
      (signal) => api.get('/logs', { params, signal }),
      (response) => {
        const data = response.data || {}
        const list = Array.isArray(data.items) ? data.items : []
        setEvents((prev) => (append ? [...prev, ...list] : list))
        setNextCursor(data.next_cursor || '')
        setEventsMeta({ persisted: Boolean(data.persisted), dropped: Number(data.dropped_events) || 0 })
        setEventsError('')
        setLoadMoreError('')
      },
      {
        onError: (error) => {
          if (error?.code === 'ERR_CANCELED') return
          const msg = error?.response?.data?.error || t('network_error')
          if (append) {
            setLoadMoreError(msg)
          } else {
            setEvents([])
            setNextCursor('')
            setEventsError(msg)
          }
        },
        onSettled: () => {
          setLoadingEvents(false)
          setLoadingMore(false)
        },
      },
    )
  }, [clientIP, keyword, level, source, t])

  const loadTraffic = useCallback(async () => {
    setLoadingTraffic(true)
    setTrafficError('')
    const preset = RANGE_PRESETS.find((item) => item.key === rangePreset) || RANGE_PRESETS[1]
    const now = new Date()
    const from = new Date(now.getTime() - preset.ms).toISOString()
    const params = { from, to: now.toISOString() }
    if (trafficIP.trim()) params.client_ip = trafficIP.trim()
    if (trafficPort.trim()) params.inbound_port = trafficPort.trim()
    if (trafficOutbound) params.outbound_node_id = trafficOutbound
    await requestsRef.current.run(
      'traffic',
      (signal) => api.get('/logs/traffic', { params, signal }),
      (response) => {
        setTraffic(response.data || null)
      },
      {
        onError: (error) => {
          if (error?.code === 'ERR_CANCELED') return
          setTraffic(null)
          setTrafficError(error?.response?.data?.error || t('network_error'))
        },
        onSettled: () => {
          setLoadingTraffic(false)
        },
      },
    )
  }, [rangePreset, t, trafficIP, trafficOutbound, trafficPort])

  useEffect(() => () => {
    requestsRef.current?.abortAll()
  }, [])

  useEffect(() => {
    void loadSettings()
  }, [loadSettings])

  useEffect(() => {
    if (subTab === 'events') void loadEvents({ cursor: '', append: false })
  }, [loadEvents, subTab])

  useEffect(() => {
    if (subTab === 'traffic') void loadTraffic()
  }, [loadTraffic, subTab])

  // 滚动触底懒加载下一页日志（翻页失败后暂停自动触发，需手动点击重试）。
  useEffect(() => {
    if (subTab !== 'events' || !nextCursor || loadingEvents || loadingMore || loadMoreError) return undefined
    const node = sentinelRef.current
    if (!node || typeof IntersectionObserver === 'undefined') return undefined
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        void loadEvents({ cursor: nextCursor, append: true })
      }
    }, { rootMargin: '160px' })
    observer.observe(node)
    return () => observer.disconnect()
  }, [loadEvents, loadMoreError, loadingEvents, loadingMore, nextCursor, subTab])

  const storageBadge = useMemo(() => {
    if (!settings?.enabled) return { icon: HardDrive, text: t('log_mode_memory'), active: false }
    if (settings.store_mode === 'primary') return { icon: Database, text: t('log_store_primary_short'), active: true }
    if (settings.store_mode === 'independent') {
      return { icon: Database, text: `${t('log_store_remote_short')} (${String(settings.remote_driver || '').toUpperCase()})`, active: true }
    }
    return { icon: HardDrive, text: t('log_store_local_short'), active: true }
  }, [settings, t])

  const StorageIcon = storageBadge.icon
  const outboundOptions = useMemo(
    () => (ctl.allNodes || ctl.nodes || []).map((node) => ({ value: String(node.id), label: node.name })),
    [ctl.allNodes, ctl.nodes]
  )

  return (
    <section className="logs-panel" aria-label={t('nav_logs')} data-testid="logs-panel">
      <header className="logs-toolbar">
        <Segmented
          size="sm"
          label={t('nav_logs')}
          value={subTab}
          onChange={setSubTab}
          testId="logs-subtab"
          options={[
            { value: 'events', label: t('log_tab_events') },
            { value: 'traffic', label: t('log_tab_traffic') },
          ]}
        />
        <span className="dusk-top__spacer" />
        <span className={`logs-storage-pill${storageBadge.active ? ' is-active' : ''}`} data-testid="logs-storage-badge">
          <StorageIcon size={13} strokeWidth={1.8} aria-hidden="true" />
          <span>{storageBadge.text}</span>
          {storageBadge.active && settings?.status?.size_bytes > 0 ? (
            <span className="mono sb-dim">· {formatBytes(settings.status.size_bytes)}</span>
          ) : null}
        </span>
        <Button size="small" icon={<Settings2 size={13} strokeWidth={1.8} />} onClick={() => setSettingsOpen(true)} data-testid="logs-settings-open">
          {t('log_storage_settings')}
        </Button>
      </header>

      {settings?.status?.last_error ? (
        <Alert type="warning" showIcon message={t('log_storage_last_error')} description={settings.status.last_error} />
      ) : null}

      {subTab === 'events' ? (
        <>
          <div className="logs-filters">
            <Segmented
              size="sm"
              label={t('log_source')}
              value={source}
              onChange={setSource}
              testId="logs-filter-source"
              options={[
                { value: '', label: t('all') },
                { value: 'panel', label: t('log_source_panel') },
                { value: 'singbox', label: 'sing-box' },
              ]}
            />
            <Segmented
              size="sm"
              label={t('log_level')}
              value={level}
              onChange={setLevel}
              testId="logs-filter-level"
              options={[
                { value: '', label: t('all') },
                { value: 'INFO', label: 'INFO' },
                { value: 'WARN', label: 'WARN' },
                { value: 'ERROR', label: 'ERROR' },
              ]}
            />
            <Input
              allowClear
              size="small"
              prefix={<Search size={13} />}
              placeholder={t('log_search_placeholder')}
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
              style={{ width: 210 }}
              data-testid="logs-filter-keyword"
            />
            <Input
              allowClear
              size="small"
              placeholder={t('log_client_ip_placeholder')}
              value={clientIP}
              onChange={(event) => setClientIP(event.target.value)}
              style={{ width: 150 }}
              data-testid="logs-filter-ip"
            />
            <span className="dusk-top__spacer" />
            {eventsMeta.dropped > 0 ? <span className="sb-dim mono">{t('log_dropped_count', { count: eventsMeta.dropped })}</span> : null}
            <Button size="small" icon={<RefreshCw size={13} />} loading={loadingEvents} onClick={() => void loadEvents({ cursor: '', append: false })} data-testid="logs-refresh">
              {t('refresh')}
            </Button>
          </div>

          <div className="logs-card" data-testid="logs-events-list">
            {loadingEvents && events.length === 0 ? (
              <div className="logs-loading"><Spinner size={18} /></div>
            ) : eventsError && events.length === 0 ? (
              <div style={{ padding: 16 }}>
                <Alert
                  type="error"
                  showIcon
                  message={t('log_events_load_failed')}
                  description={eventsError}
                  action={<Button size="small" onClick={() => void loadEvents({ cursor: '', append: false })} data-testid="logs-retry">{t('retry_load')}</Button>}
                  data-testid="logs-events-error"
                />
              </div>
            ) : events.length === 0 ? (
              <EmptyState icon={ScrollText} title={t('log_events_empty')} hint={t('log_events_empty_hint')} />
            ) : (
              <div className="logs-list">
                {events.map((item) => {
                  const lvl = String(item.level || 'INFO').toUpperCase()
                  const routeLabel = [
                    item.inbound_port ? `:${item.inbound_port}` : '',
                    item.policy_name && item.member_name ? `${item.policy_name} → ${item.member_name}` : (item.outbound_name || ''),
                  ].filter(Boolean).join(' → ')
                  return (
                    <div key={item.id} className="logs-row" data-level={lvl} data-testid="log-event-row">
                      <span className="logs-row__ts mono">{formatDateTime(item.ts, locale)}</span>
                      <span className={`logs-lvl logs-lvl--${lvl.toLowerCase()} mono`}>{lvl}</span>
                      <span className="logs-src mono">{item.source || 'panel'}</span>
                      {item.client_ip ? <span className="logs-ip mono">{item.client_ip}</span> : null}
                      {routeLabel ? <span className="logs-route mono truncate" title={routeLabel}>{routeLabel}</span> : null}
                      <span className="logs-msg mono">{item.message}</span>
                    </div>
                  )
                })}
                {nextCursor ? (
                  <div ref={sentinelRef} className="logs-more">
                    {loadMoreError ? <span className="sb-dim" style={{ marginRight: 8 }}>{loadMoreError}</span> : null}
                    <Button size="small" type="text" loading={loadingMore} onClick={() => void loadEvents({ cursor: nextCursor, append: true })} data-testid="logs-load-more">
                      {loadMoreError ? t('retry_load') : t('log_load_more')}
                    </Button>
                  </div>
                ) : (
                  <div className="logs-more sb-dim">{t('log_end_of_list')}</div>
                )}
              </div>
            )}
          </div>
        </>
      ) : (
        <>
          {!settings?.enabled ? (
            <div className="logs-card">
              <EmptyState icon={BarChart3} title={t('traffic_disabled_title')} hint={t('traffic_disabled_hint')}>
                <Button type="primary" icon={<Settings2 size={14} />} onClick={() => setSettingsOpen(true)} data-testid="traffic-enable-btn">
                  {t('log_storage_settings')}
                </Button>
              </EmptyState>
            </div>
          ) : (
            <>
              <div className="logs-filters">
                <Segmented
                  size="sm"
                  label={t('traffic_range')}
                  value={rangePreset}
                  onChange={setRangePreset}
                  testId="traffic-range"
                  options={RANGE_PRESETS.map((preset) => ({ value: preset.key, label: t(RANGE_LABELS[preset.key]) }))}
                />
                <Input
                  allowClear
                  size="small"
                  placeholder={t('log_client_ip_placeholder')}
                  value={trafficIP}
                  onChange={(event) => setTrafficIP(event.target.value)}
                  style={{ width: 150 }}
                  data-testid="traffic-filter-ip"
                />
                <Input
                  allowClear
                  size="small"
                  placeholder={t('inbound_port')}
                  value={trafficPort}
                  onChange={(event) => setTrafficPort(event.target.value)}
                  style={{ width: 100 }}
                  data-testid="traffic-filter-port"
                />
                <Select
                  allowClear
                  showSearch
                  size="small"
                  optionFilterProp="label"
                  placeholder={t('conn_col_outbound')}
                  value={trafficOutbound || undefined}
                  onChange={(val) => setTrafficOutbound(val || '')}
                  options={outboundOptions}
                  style={{ width: 180 }}
                  data-testid="traffic-filter-outbound"
                />
                <span className="dusk-top__spacer" />
                <Button size="small" icon={<RefreshCw size={13} />} loading={loadingTraffic} onClick={() => void loadTraffic()} data-testid="traffic-refresh">
                  {t('refresh')}
                </Button>
              </div>

              {trafficError ? (
                <Alert type="error" showIcon message={trafficError} data-testid="traffic-error" />
              ) : (
                <div className="traffic-summary" data-testid="traffic-summary">
                  <div className="traffic-stat-card">
                    <span className="traffic-stat-card__label"><ArrowUp size={13} className="conn-up" /> {t('traffic_upload')}</span>
                    <strong className="mono">{formatBytes(traffic?.upload_bytes ?? 0)}</strong>
                  </div>
                  <div className="traffic-stat-card">
                    <span className="traffic-stat-card__label"><ArrowDown size={13} className="conn-down" /> {t('traffic_download')}</span>
                    <strong className="mono">{formatBytes(traffic?.download_bytes ?? 0)}</strong>
                  </div>
                  <div className="traffic-stat-card">
                    <span className="traffic-stat-card__label"><BarChart3 size={13} /> {t('traffic_total')}</span>
                    <strong className="mono">{formatBytes(traffic?.total_bytes ?? 0)}</strong>
                  </div>
                </div>
              )}

              <div className="logs-card" data-testid="traffic-table">
                {loadingTraffic && !traffic ? (
                  <div className="logs-loading"><Spinner size={18} /></div>
                ) : !(traffic?.items?.length) ? (
                  <EmptyState icon={BarChart3} title={t('traffic_empty_title')} hint={t('traffic_empty_hint')} />
                ) : (
                  <div className="traffic-table">
                    <div className="traffic-row traffic-row--head">
                      <span>{t('conn_col_client')}</span>
                      <span>{t('conn_col_inbound')}</span>
                      <span>{t('conn_col_outbound')}</span>
                      <span className="traffic-num">{t('traffic_upload')}</span>
                      <span className="traffic-num">{t('traffic_download')}</span>
                      <span className="traffic-num">{t('traffic_total')}</span>
                    </div>
                    {traffic.items.map((item, idx) => {
                      const target = item.policy_member_name
                        ? `${item.outbound_node_name} → ${item.policy_member_name}`
                        : (item.outbound_node_name || t('direct_local'))
                      return (
                        <div key={`${item.client_ip}-${item.inbound_port}-${item.outbound_node_id}-${idx}`} className="traffic-row">
                          <span className="mono truncate">{item.client_ip || '—'}</span>
                          <span className="mono">{item.inbound_port ? `:${item.inbound_port}` : '—'}{item.route_id ? ` · ${item.route_id}` : ''}</span>
                          <span className="truncate" title={target}>{target}</span>
                          <span className="mono traffic-num conn-up">{formatBytes(item.upload_bytes)}</span>
                          <span className="mono traffic-num conn-down">{formatBytes(item.download_bytes)}</span>
                          <span className="mono traffic-num"><strong>{formatBytes(item.total_bytes)}</strong></span>
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            </>
          )}
        </>
      )}

      <LogSettingsModal
        open={settingsOpen}
        settings={settings}
        loadingSettings={loadingSettings}
        settingsError={settingsError}
        onRetryLoad={loadSettings}
        onClose={() => setSettingsOpen(false)}
        onSaved={(next) => {
          setSettings(next)
          setSettingsError('')
          ctl.helpers.notify.success(t('log_settings_saved'))
          void loadEvents({ cursor: '', append: false })
          if (subTab === 'traffic') void loadTraffic()
        }}
      />
    </section>
  )
}
