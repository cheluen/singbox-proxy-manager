# HTTP API 参考

路由注册见 `backend/main.go`，请求和响应处理见 `backend/api/`。

---

## 1. 通用约定

- **基础前缀**：`/api`（默认服务监听 `http://0.0.0.0:30000`）。
- **鉴权方式**：受保护接口需携带 `Authorization` 请求头，同时支持 `Authorization: Bearer <token>`（前缀大小写不敏感）与裸令牌 `Authorization: <token>`。会话无效或已吊销时返回 `401 Unauthorized`。
- **请求体限制**：默认受 `API_MAX_BODY_BYTES`（`1048576` 字节，即 `1 MiB`）约束。
- 配置 `CORS_ALLOWED_ORIGINS` 后精确匹配 Origin，允许 `GET, POST, PUT, PATCH, DELETE, OPTIONS`。
- **重要差异提醒**：`POST /api/nodes` 与 `POST /api/nodes/batch-import` 中的 `enabled` 字段为 Go 普通布尔值，**API 请求中省略时默认为 `false`**（管理面板 UI 表单默认勾选为 `true`）。

---

## 2. 公开接口（无需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/health` | 无 | 轻量健康探针（供 Docker `HEALTHCHECK` 与监控系统调用，不请求外部 GitHub API）。以 `2s` 超时校验数据库连接池存活并返回 `{ "status": "ok", "runtime_running": true, "runtime_degraded": false }`；数据库不可用时返回 `503 Service Unavailable` 及 `{ "status": "unhealthy" }`。 |
| `GET /api/version` | 无 | 返回 `version` 与 `update`。`update.latest_release_url` 始终指向配置仓库的 `/releases/latest`，`update.release_url` 是查询成功时 GitHub 返回的具体版本地址。失败仍返回 `200` 并附 `update.error` 和 `checked_at`。 |
| `POST /api/login` | `{ "password": "..." }` | 验证管理员密码，成功返回 `200` 及 `{ "token": "...", "expiry": 1700000000 }`（`expiry` 为整数 Unix 秒时间戳）；密码错误返回 `401`；触发 IP 限流或全局并发校验上限返回 `429`（附 `Retry-After` 头）。 |

---

## 3. 会话与运行状态接口（需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `POST /api/logout` | 无 | 删除当前请求 Token 哈希对应的数据库会话记录。 |
| `GET /api/runtime/status` | 无 | 返回当前 `sing-box` 运行状态对象，含 `state`、`running`、`degraded`、`message`、`pid`、`started_at`、`last_exit_at`、`apply_stage`、`desired_config_hash`、`active_config_hash`、`recovery_attempts`、`next_retry_at`。 |
| `POST /api/runtime/restart` | 无 | 从数据库重新生成配置、校验并重启 `sing-box` 内核；成功与降级失败响应均附带最新 `status`。 |

---

## 4. 节点管理接口（需鉴权）

### 4.1 节点通用请求字段（Upsert）

`POST /api/nodes` 与 `PUT /api/nodes/:id` 共用以下字段集合（其中 `config` 必须是合法协议结构的 **JSON 字符串**）：

```json
{
  "name": "Tokyo-SS-01",
  "remark": "可选备注",
  "type": "ss",
  "config": "{\"server\":\"tokyo.example.net\",\"server_port\":8388,\"method\":\"aes-256-gcm\",\"password\":\"demo-secret\"}",
  "inbound_port": 30001,
  "inbound_mode": "mixed",
  "username": "user01",
  "password": "pass-value",
  "auth_enabled": true,
  "enabled": true,
  "tcp_reuse_enabled": true,
  "upstream_mode": "none",
  "upstream_type": "",
  "upstream_config": ""
}
```

> 认证约束：`username` 与 `password` 必须同时提供或同时为空，且 `username` 禁止包含 `+`。

### 4.2 节点单条与查询接口

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/nodes` | 可选查询参数 `group_id` | 返回包含 `group_id` 的节点数组，依次按组序、组 ID、组内排序、节点 ID 排序。`inbound_port: 0` 表示仅有出站。 |
| `GET /api/nodes/:id` | 无 | 返回单个节点完整记录；不存在返回 `404`。 |
| `POST /api/nodes` | 节点 Upsert 字段（可选 `group_id`） | 创建节点、校验并应用至 `sing-box`，成功返回 `201`。自动端口模式下忽略请求中的 `inbound_port`；保留端口模式下填 `0` 表示自动分配；所属分组关闭端口分配时端口为 `0`。 |
| `PUT /api/nodes/:id` | 节点 Upsert 字段 | 全量更新节点并原子应用；保留原 `sort_order` 与 `inbound_port_pinned` 状态；未传入 `tcp_reuse_enabled` 或前置代理字段时保留原值。 |
| `PATCH /api/nodes/:id` | `{ "enabled"?: bool, "tcp_reuse_enabled"?: bool, "inbound_mode"?: "mixed"\|"https" }` | 仅允许切换启用状态、TCP 复用开关或入站模式（至少提供一项）；切换至 `https` 时要求存在已选定或唯一自动选中的有效 TLS 证书，严格拒绝未知字段与尾随 JSON 数据。 |
| `PUT /api/nodes/:id/upstream` | `{ "upstream_mode", "upstream_type", "upstream_config" }` | 更新节点前置代理策略（`none` / `group` / `global` / `custom`，优先级：`custom > group > global`；`group` 仅走本组前置不回退全局，`global` 在本组前置启用时优先走分组前置）并执行防环路与内核校验，返回更新后节点。 |
| `PUT /api/nodes/:id/remark` | `{ "remark": "..." }` | 更新节点备注（最多 `1024` 字符），仅改数据库，**不重启 `sing-box`**。 |
| `PUT /api/nodes/:id/port-pin` | `{ "pinned": true }` | 切换自动端口模式下的端口固定标记，**不重启 `sing-box`**；处于「保留入站端口」模式时拒绝并返回 `409`。 |
| `PUT /api/nodes/:id/replace` | `{ "link": "...", "update_name"?: bool }` | 使用分享链接替换该节点的出站协议与配置（`update_name` 默认 `true`）；保留入站端口、账密与备注，清空旧出口 IP/延迟数据并重新应用内核。 |
| `DELETE /api/nodes/:id` | 无 | 删除节点并规范化剩余节点 `sort_order`；自动端口模式下同步重排未固定节点端口并应用内核。 |
| `GET /api/nodes/:id/export` | 无 | 导出单节点出站分享链接，返回 `{ "link": "..." }`；`direct` 或多 Peer WireGuard 等不支持导出的节点返回 `400`。 |
| `POST /api/nodes/:id/check-ip` | 无 | 成功返回 `IPInfo` 与 `check_status`、`check_error`、`checked_at`。无端口节点使用临时回环入站。自定义前置额外返回 `upstream`。禁用节点返回 `400`，探测中路径变化返回 `409`，失败返回 `502` 或限流 `429` 并持久化失败状态与脱敏原因。 |

### 节点分组

节点创建和批量导入支持可选 `group_id`，省略时归入排序第一组。更新节点时省略该字段会保留原组。每组可独立设置 `allocate_port`、`start_port`（1024–65535）、分组级前置代理（`upstream_enabled`、`upstream_type`、`upstream_config`）以及 `subscription_url`、`subscription_auto_update`、`subscription_interval_minutes`。

| 接口 | 行为 |
| --- | --- |
| `GET /api/node-groups`、`GET /api/node-groups/:id` | 返回组设置、分组前置代理配置及检测结果（`upstream_enabled` / `upstream_type` / `upstream_config` / `upstream_ip` / `upstream_location` / `upstream_country_code` / `upstream_latency` / `upstream_error` / `upstream_revision`）、`revision`、`node_count`、`enabled_node_count`、最后订阅时间与错误。已鉴权管理员能读取完整订阅地址。 |
| `POST /api/node-groups`、`PATCH /api/node-groups/:id` | 创建时需要 `name`。支持配置与启停分组级前置代理（启用时经防环路、自引用与 `sing-box check` 校验，仅影响当前分组内 `group` / `global` 节点，无实际路由变化时不重启共享内核；`PATCH` 修改前置时可附带 `expected_upstream_revision` / `expected_upstream_enabled` 核对编辑基线，并发冲突返回 `409` 及 `code: upstream_conflict`）。关闭端口分配遇到固定端口返回 `409`、`code: pinned_ports_exist` 和 `pinned_node_ids`。传 `release_pins: true` 可释放端口和固定标记。 |
| `POST /api/node-groups/:id/upstream/check-ip` | 通过临时隔离 `sing-box` 进程检测**已保存**的分组前置代理出口 IP（不要求「启用分组前置代理」开关打开，防环路边界与正式启用一致）。成功返回 `200` 并落库检测结果；未配置或定义非法/命中防环路返回 `400`；分组不存在返回 `404`；检测期间分组前置定义被并发修改返回 `409`；上游测速限流返回 `429`（附 `Retry-After` 头）；探测失败返回 `502` 并持久化脱敏错误。 |
| `DELETE /api/node-groups/:id` | 删除最后一组返回 `409` 和 `last_group`。非空组须指定 `?mode=move&target_group_id=整数` 或 `?mode=delete_nodes`，否则返回 `409` 和 `group_not_empty`。 |
| `POST /api/node-groups/reorder` | `ids` 是按目标顺序排列的全部分组 ID。 |
| `POST /api/nodes/batch-move` | 提交 `ids` 与目标 `group_id`，按目标组设置分配或释放端口，并在实际前置链路变化时自动重置受影响节点的出口检测状态。 |
| `POST /api/node-groups/:id/subscription/refresh` | 增量更新本组托管节点，返回 `added`、`updated`、`removed`、`unchanged` 及 `warnings`。抓取失败或空订阅会保留已有节点。 |

`POST /api/nodes/reorder` 支持 `group_id` 和完整组内 `nodes` 序列。省略 `group_id` 时沿用全量节点格式。端口在所有组之间唯一。节点位于无端口组时，已启用的出站依然可以被引用。

### 4.3 批量操作与解析接口

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `POST /api/parse-link` | `{ "link": "..." }` | 解析单条分享链接但不落库，返回 `{ "type", "name", "config" }`（`config` 为 JSON 字符串）。 |
| `POST /api/nodes/batch-import` | `{ "content"?: "...", "links"?: [...], "source_type"?: "auto"\|"subscription"\|"http_proxy", "enabled"?: bool, "group_id"?: int }` | `content` 优先于 `links`。执行抓取展开、分组 `sing-box check` 预检隔离、端口分配与联合复校；无可解析节点时返回 `400`；若解析出候选但全部被内核校验拒绝，仍可能返回 `200` 且 `success: 0`；正常返回 `{ "total", "success", "failed", "results": [...] }`。 |
| `POST /api/nodes/batch-delete` | `{ "ids": [1, 2, 3] }` | 批量删除节点；要求全部为正整数 ID、无重复且全部存在；返回含 `deleted_count` 的结果并应用内核。 |
| `POST /api/nodes/batch-export` | `{ "ids": [1, 2, 3] }` | 逐项导出选中节点分享链接，返回成功数、失败数及明细列表。 |
| `POST /api/nodes/batch-auth` | `{ "node_ids": [1, 2], "auth_enabled"?: bool, "username": "...", "password": "..." }` | 批量设置或清空选中节点的入站认证（账密需成对，用户名不含 `+`），校验全部 ID 有效且无重复后应用内核。 |
| `POST /api/nodes/reorder` | `{ "group_id"?: int, "nodes": [{ "id": 1, "sort_order": 0 }, ...] }` | 重排节点顺序；指定 `group_id` 时须提交该组完整节点序列，省略 `group_id` 时须提交数据库全部节点且 `sort_order` 为 `0..N-1` 完整排列。自动模式下重排未固定端口，保留模式下不改动端口。 |

---

## 5. 全局设置接口（需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/settings` | 无 | 返回 `preserve_inbound_ports`、全局前置代理及其检测结果、`node_list_column_widths`、`inbound_tls_cert_path`、`inbound_tls_key_path` 与实时扫描结果 `inbound_tls`（含 `ready`、`selection_mode`、`active` 及候选 `certificates` 列表）。起始端口通过分组接口读取。 |
| `PUT /api/settings` | 可选 `preserve_inbound_ports`、`admin_password`、`global_upstream_enabled`、`global_upstream_type`、`global_upstream_config`、`node_list_column_widths`、`inbound_tls_cert_path`、`inbound_tls_key_path` | 返回 `changed` 和最新列宽。选择 `inbound_tls_cert_path` / `inbound_tls_key_path` 必须匹配当前扫描到的有效证书套件并会触发内核原子重载。`node_list_column_widths` 接受白名单列键与 40–1200 像素整数，传 `{}` 恢复默认。单独更新列宽时只写数据库。管理员密码至少 8 个 Unicode 字符，最多 72 个 UTF-8 字节。改密成功即吊销全部会话。旧版请求中的 `start_port` 会被忽略。 |
| `POST /api/settings/upstream/check-ip` | 无 | 通过临时隔离 `sing-box` 进程检测**已保存**的全局前置代理出口 IP（不要求「启用全局前置代理」开关处于打开状态，防环路边界与正式启用一致）。成功返回 `200` 并落库结果；未配置或命中防环路返回 `400`；检测期间全局前置定义被并发修改返回 `409`；上游测速限流返回 `429`（附 `Retry-After` 头）；探测失败返回 `502` 并持久化脱敏错误。 |

---

## 6. 最小调用示例

```bash
# 1. 登录获取会话令牌
TOKEN=$(curl -fsS -X POST http://127.0.0.1:30000/api/login \
  -H 'Content-Type: application/json' \
  -d '{"password":"<你的管理员密码>"}' | jq -r '.token')

# 2. 查询运行状态与节点列表
curl -fsS -H "Authorization: Bearer ${TOKEN}" http://127.0.0.1:30000/api/runtime/status
curl -fsS -H "Authorization: Bearer ${TOKEN}" http://127.0.0.1:30000/api/nodes

# 3. 批量导入分享链接（API 层显式传 enabled: true）
curl -fsS -X POST http://127.0.0.1:30000/api/nodes/batch-import \
  -H "Authorization: Bearer ${TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"content":"socks5://127.0.0.1:1080#demo-socks","source_type":"auto","enabled":true}'
```

### 策略节点

在 `POST /api/nodes` 或 `PUT /api/nodes/:id` 中使用 `type=failover` 或 `type=loadbalance`。`config` 沿用节点接口的 JSON 字符串格式，其解码示例如下。

```json
{"member_ids":[12,9],"member_sources":[{"type":"group","id":2},{"type":"node","id":9}],"strategy":"round-robin","health_check_url":"https://example.org/health","health_check_interval":30}
```

`member_ids` 按优先级排序，重复 ID 会合并；也支持通过可选 `member_sources`（每项为 `{"type":"group"|"node","id":整数}`）按分组整组引入实体节点并自动随组内节点增删、移动与订阅刷新同步（组内实体节点按 ID 升序展开；当所有分组来源断链移除后，`member_sources` 自动省略并退回普通手工 `member_ids` 管理）。创建或编辑时至少选择一个已有实体节点或有效分组，支持跨组与无端口成员。故障转移忽略 `strategy`。负载均衡支持 `round-robin` 和按目标主机的 `consistent-hashing`。健康检查周期为 10 至 3600 秒，默认 30 秒。策略的前置模式为 `none`，成员沿用各自的前置设置。策略支持普通入站认证、TCP 复用、`mixed` / `https` 入站模式及 `POST /api/nodes/:id/check-ip`。分享链接仅适用于支持该格式的实体节点。

节点响应附加 `effective_member_ids`、`policy_active_member_id`、`policy_member_health` 和 `policy_warnings`。健康记录包括 `node_id`、`healthy`、`latency`（毫秒）、`last_error`、`checked_at`。删除成员会移除引用，停用成员会保留引用并暂停转发。成员全部不可用时可查询告警、编辑策略并添加替代成员。

### 当前连接

管理员可通过 `GET /api/connections` 获取快照，或通过 `GET /api/connections/stream` 订阅 SSE 的 `snapshot` 事件。采样间隔约 1.2 秒。内核停止或连续三次采样失败时，服务端发送一次 `error` 事件，数据为 `{"error":"原因"}`。采样恢复后继续发送 `snapshot`。服务端逐帧复核管理员会话。退出、过期或修改密码后，下次复核会关闭流。单次轮询保留 5 秒计数基线，供后续轮询计算速率。

- 顶层字段为 `runtime_epoch`、`sampled_at`、`active_connections`、`client_count`、`route_count`、`total_upload_bps`、`total_download_bps`、`routes`。
- 每条路径包含 `key`、`client_ip`、`inbound_port`、`inbound_node_id`、`inbound_node_name`、`route_id`、`outbound_node_id`、`outbound_node_name`、`outbound_type`、`policy_member_id`、`policy_member_name`、`upstream_label`、`active_count`、`upload_bytes`、`download_bytes`、`upload_bps`、`download_bps`，以及可选字段 `upstream_source`（无前置代理时省略，存在时取值为 `custom`、`group`、`global` 或 `legacy`）。
- 速率单位为字节/秒。内核首次采样或重载后第一帧的速率为 `null`。`route_id` 始终对应实际命中的复用用户名。单个策略有多个成员同时访问相同目标时，官方内核可能缺少区分外层连接所需的信息，无法唯一关联的成员 ID 返回 `0`，名称为空。

### 日志与流量统计

以下接口均需管理员认证。时间参数使用 RFC3339，可带毫秒与时区。

| 请求 | 参数与返回 |
| --- | --- |
| `GET /api/logs` | 支持 `source`、`level`、`q`、`client_ip`、`from`、`to`、`cursor`、`limit`（默认 50，最多 200）。返回 `items`、`next_cursor`、`storage_mode`、`persisted`、`dropped_events`。每条日志含 `id`、`ts`、`source`、`level`、`message`、`client_ip`、`inbound_port`、`outbound_name`、`policy_name`、`member_name`。 |
| `GET /api/logs/settings` | 返回 `enabled`、`min_level`、`store_mode`、`remote_driver`、`local_sqlite_path`、`remote_dsn_masked`、`remote_url_masked`、`has_remote_dsn`、`has_remote_token`、`retention_days`、`max_size_mb`。`status` 含 `active`、`driver`、`size_bytes`、`dropped_events`、`last_error`。 |
| `PUT /api/logs/settings` | 显式保存配置。`dsn_action` 控制 `remote_dsn` 或 Turso `remote_url`，`token_action` 控制 `remote_token`。各自支持 `keep`、`replace`、`clear`，默认 `keep`。原地址配置了令牌时，更换 Turso 地址须显式替换或清除令牌。启用前验证目标库的读、写和删除权限，失败返回 `400`，继续使用上次生效的设置。 |
| `GET /api/logs/traffic` | 支持 `from`、`to`、`client_ip`、`inbound_port`、`outbound_node_id`、`q`。返回 `persisted`、`upload_bytes`、`download_bytes`、`total_bytes`、按路径汇总的 `items` 和按分钟汇总的 `buckets`。查询计入待写入的采样增量。 |
