// 分组表单提交载荷构建（纯函数）：编辑模式仅提交用户实际改动过的字段，
// 防止后台刷新后旧草稿覆盖其他管理员刚保存的分组前置代理配置，并识别同字段并发冲突。
export function buildGroupFormPlan({
  editing,
  values,
  savedGroup,
  openedSnapshot,
  touchedFields,
  upstreamDraft,
  upstreamEnabledTouched,
}) {
  const upstream = upstreamDraft ?? {
    type: savedGroup?.upstream_type || '',
    config: savedGroup?.upstream_config || '',
  }
  const upstreamConfigured = Boolean(upstream.type && upstream.config)
  const effectiveEnabled = upstreamEnabledTouched
    ? Boolean(values.upstream_enabled)
    : Boolean(savedGroup?.upstream_enabled)

  if (!editing) {
    if (values.upstream_enabled && !upstreamConfigured) {
      return { missingUpstream: true, upstreamConflict: false, payload: null }
    }
    const subUrl = String(values.subscription_url || '').trim()
    const payload = {
      name: String(values.name || '').trim(),
      allocate_port: Boolean(values.allocate_port),
      start_port: values.allocate_port ? Number(values.start_port || 30001) : Number(values.start_port || 30001),
      subscription_url: subUrl,
      subscription_auto_update: Boolean(subUrl && values.subscription_auto_update),
      subscription_interval_minutes: Number(values.subscription_interval_minutes || 60),
    }
    if (values.upstream_enabled || upstreamConfigured) {
      payload.upstream_enabled = Boolean(values.upstream_enabled)
      payload.upstream_type = upstream.type
      payload.upstream_config = upstream.config
    }
    return { missingUpstream: false, upstreamConflict: false, payload }
  }

  const localUpstreamTouched = Boolean(upstreamEnabledTouched || upstreamDraft !== null)
  if (localUpstreamTouched && effectiveEnabled && !upstreamConfigured) {
    return { missingUpstream: true, upstreamConflict: false, payload: null }
  }

  const serverUpstreamChanged = Boolean(
    openedSnapshot && (
      Boolean(savedGroup?.upstream_enabled) !== Boolean(openedSnapshot.upstream_enabled) ||
      (savedGroup?.upstream_type || '') !== (openedSnapshot.upstream_type || '') ||
      (savedGroup?.upstream_config || '') !== (openedSnapshot.upstream_config || '') ||
      (typeof savedGroup?.upstream_revision === 'number' &&
        typeof openedSnapshot.upstream_revision === 'number' &&
        savedGroup.upstream_revision !== openedSnapshot.upstream_revision)
    )
  )

  const touched = (field) => Boolean(touchedFields?.[field])
  const payload = {}

  const nextName = String(values.name || '').trim()
  if (touched('name') && nextName !== String(savedGroup?.name || '').trim()) {
    payload.name = nextName
  }

  const nextAllocate = Boolean(values.allocate_port)
  if (touched('allocate_port') && nextAllocate !== Boolean(savedGroup?.allocate_port)) {
    payload.allocate_port = nextAllocate
  }

  if (nextAllocate && (touched('start_port') || (touched('allocate_port') && !Number(savedGroup?.start_port)))) {
    const nextStart = Number(values.start_port || 30001)
    if (nextStart !== Number(savedGroup?.start_port || 0)) {
      payload.start_port = nextStart
    }
  }

  const nextSubUrl = String(values.subscription_url || '').trim()
  const subUrlChanged = touched('subscription_url') && nextSubUrl !== String(savedGroup?.subscription_url || '').trim()
  if (subUrlChanged) {
    payload.subscription_url = nextSubUrl
  }
  const effectiveSubUrl = subUrlChanged ? nextSubUrl : String(savedGroup?.subscription_url || '').trim()
  if (subUrlChanged && !nextSubUrl) {
    if (savedGroup?.subscription_auto_update) {
      payload.subscription_auto_update = false
    }
  } else if (touched('subscription_auto_update')) {
    const nextAuto = Boolean(effectiveSubUrl && values.subscription_auto_update)
    if (nextAuto !== Boolean(savedGroup?.subscription_auto_update)) {
      payload.subscription_auto_update = nextAuto
    }
  }
  if (touched('subscription_interval_minutes')) {
    const nextInterval = Number(values.subscription_interval_minutes || 60)
    if (nextInterval !== Number(savedGroup?.subscription_interval_minutes || 60)) {
      payload.subscription_interval_minutes = nextInterval
    }
  }

  if (upstreamEnabledTouched && Boolean(values.upstream_enabled) !== Boolean(savedGroup?.upstream_enabled)) {
    payload.upstream_enabled = Boolean(values.upstream_enabled)
  }
  if (upstreamDraft !== null) {
    const typeChanged = upstreamDraft.type !== (savedGroup?.upstream_type || '')
    const configChanged = upstreamDraft.config !== (savedGroup?.upstream_config || '')
    if (typeChanged || configChanged) {
      payload.upstream_type = upstreamDraft.type
      payload.upstream_config = upstreamDraft.config
    }
  }

  const upstreamPayloadMutates =
    Object.hasOwn(payload, 'upstream_enabled') ||
    Object.hasOwn(payload, 'upstream_type') ||
    Object.hasOwn(payload, 'upstream_config')

  if (upstreamPayloadMutates) {
    if (typeof savedGroup?.upstream_revision === 'number') {
      payload.expected_upstream_revision = savedGroup.upstream_revision
    }
    payload.expected_upstream_enabled = Boolean(savedGroup?.upstream_enabled)
  }

  return {
    missingUpstream: false,
    upstreamConflict: Boolean(upstreamPayloadMutates && serverUpstreamChanged),
    payload,
  }
}
