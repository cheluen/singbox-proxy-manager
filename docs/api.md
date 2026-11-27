# HTTP API 参考

路由注册见 `backend/main.go`，请求和响应处理见 `backend/api/`。

---

## 1. 通用约定

- **基础前缀**：`/api`（默认服务监听 `http://0.0.0.0:30000`）。
- **鉴权方式**：受保护接口需携带 `Authorization` 请求头，同时支持 `Authorization: Bearer <token>`（前缀大小写不敏感）与裸令牌 `Authorization: <token>`。会话无效或已吊销时返回 `401 Unauthorized`。
- **请求体限制**：默认受 `API_MAX_BODY_BYTES`（`1048576` 字节，即 `1 MiB`）约束。已知 `Content-Length` 超限返回 `413`，未知长度流式超限在解码阶段返回 `400`。
- **CORS 与请求校验**：配置 `CORS_ALLOWED_ORIGINS` 后精确匹配 Origin（不匹配时不返回跨域头，而非 `403`），`OPTIONS` 统一返回 `204`，允许方法为 `GET, POST, PUT, PATCH, DELETE, OPTIONS`。普通 JSON 接口采用标准绑定，仅 `PATCH /api/nodes/:id` 严格拒绝未知字段与尾随 JSON 数据。
- **重要差异提醒**：`POST /api/nodes`、`PUT /api/nodes/:id` 与 `POST /api/nodes/batch-import` 中的 `enabled` 字段为 Go 普通布尔值，**API 请求中省略时默认为 `false`**（管理面板 UI 表单默认勾选为 `true`）。

---

## 2. 公开接口（无需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/health` | 无 | 轻量健康探针（供 Docker `HEALTHCHECK` 与监控系统调用，不请求外部 GitHub API）。以 `2s` 超时校验数据库连接池存活并返回 `200` 及 `{ "status": "ok", "runtime_running": true, "runtime_degraded": false }`（内核停机或降级仍返回 `200`）；数据库不可用时返回 `503` 及 `{ "status": "unhealthy" }`。 |
| `GET /api/version` | 无 | 返回 `200` 及 `{ "version", "update" }`。`update.latest_release_url` 始终指向配置仓库的 `/releases/latest`，`update.release_url` 是查询成功时 GitHub 返回的具体版本地址。查询失败仍返回 `200` 并附 `update.error` 和 `checked_at`。 |
| `POST /api/login` | `{ "password": "..." }` | 验证管理员密码，成功返回 `200` 及 `{ "token": "...", "expiry": 1700000000 }`（`expiry` 为整数 Unix 秒时间戳）；非法请求 `400`，密码错误 `401`，触发 IP 限流或全局并发校验上限返回 `429`（附 `Retry-After` 头），存储错误 `500`。 |

---

## 3. 会话与运行状态接口（需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `POST /api/logout` | 无 | 删除当前请求 Token 哈希对应的数据库会话记录，返回 `200 { "message": "logged out" }`（数据库异常返回 `500`）。 |
| `GET /api/runtime/status` | 无 | 返回 `200` 当前 `sing-box` 运行状态对象：`state`、`running`、`degraded` 恒定存在，其余字段（`message`、`pid`、`started_at`、`last_exit_at`、`apply_stage`、`desired_config_hash`、`active_config_hash`、`recovery_attempts`、`next_retry_at`）在为空时省略。 |
| `POST /api/runtime/restart` | 无 | 从数据库重新生成配置、校验并重启 `sing-box` 内核；成功返回 `200 { "message", "status" }`，重应用失败返回 `500 { "error", "status" }`，变更协调器不可用返回 `503 { "error" }`（不含 `status`）。 |

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

> 字段与认证约束：`name`、`username`、`password` 上限 255 字符，`remark` 上限 1024 字符，`type` 上限 64 字符；`username` 与 `password` 必须同时提供或同时为空（不得含换行符且 `username` 禁止包含 `+`），`auth_enabled=true` 时账密必填，`false` 时须留空。

### 4.2 节点单条与查询接口

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/nodes` | 可选查询参数 `group_id`（正整数） | 返回包含 `group_id` 的节点数组（无分页），依次按组序、组 ID、组内排序、节点 ID 排序。`inbound_port: 0` 表示无入站端口；`group_id` 非法返回 `400`，不存在的正整数组 ID 返回 `[]`。 |
| `GET /api/nodes/:id` | 无 | 返回 `200` 单个节点完整记录；非整数 ID 返回 `400`，不存在返回 `404`。 |
| `POST /api/nodes` | 节点 Upsert 字段（可选 `group_id`） | 创建节点、校验并应用至 `sing-box`，成功返回 `201` 完整节点；校验错误 `400`，分组不存在 `404`，数据库或内核应用失败 `500`。自动端口模式下忽略请求中的 `inbound_port`；保留端口模式下填 `0` 表示自动分配；所属分组关闭端口分配时端口为 `0`。 |
| `PUT /api/nodes/:id` | 节点 Upsert 字段 | 全量更新节点并原子应用，返回 `200` 完整节点（`400`/`404`/`500`）。不跨组时保留原 `sort_order` 与 `inbound_port_pinned`；显式更改 `group_id` 时追加至目标组末尾，移入无端口组会释放端口与固定状态。未传入 `inbound_port`、`inbound_mode`、`tcp_reuse_enabled` 或实体节点前置字段时保留原值（保留或固定端口模式下显式传 `inbound_port: 0` 表示自动重新分配）；省略 `enabled` 时默认视为 `false`。 |
| `PATCH /api/nodes/:id` | `{ "enabled"?: bool, "tcp_reuse_enabled"?: bool, "inbound_mode"?: "mixed"\|"https" }` | 仅允许切换启用状态、TCP 复用开关或入站模式（至少提供一项非 `null` 值），严格拒绝未知字段与尾随 JSON 数据；切换至 `https` 时要求存在已选定或唯一自动选中的有效 TLS 证书。返回 `200` 完整节点（`400`/`404`/`500`）。 |
| `PUT /api/nodes/:id/upstream` | `{ "upstream_mode", "upstream_type", "upstream_config" }` | 更新节点前置代理策略（`none` / `group` / `global` / `custom`，历史节点兼容 `legacy`，策略节点仅允许 `none`；优先级：`custom > group > global`）并执行防环路与内核校验，返回 `200` 完整节点（`400`/`404`/`500`）。 |
| `PUT /api/nodes/:id/remark` | `{ "remark": "..." }` | 更新节点备注（最多 `1024` 字符，空值清空），仅改数据库、**不重启 `sing-box`**，返回 `200 { "id", "remark" }`（`400`/`404`/`500`）。 |
| `PUT /api/nodes/:id/port-pin` | `{ "pinned": true }` | 切换所属分组自动端口模式下的端口固定标记，**不重启 `sing-box`**，返回 `200 { "id", "inbound_port_pinned" }`；所属分组处于「保留入站端口」模式或节点无入站端口时返回 `409`（其他错误 `400`/`404`/`500`）。 |
| `PUT /api/nodes/:id/replace` | `{ "link": "...", "update_name"?: bool }` | 使用分享链接替换该节点的出站协议与配置（`update_name` 默认 `true`）；保留入站端口、账密、所属组与备注，清空旧出口 IP/延迟数据并重新应用内核，返回 `200` 完整节点（`400`/`404`/`500`）。 |
| `DELETE /api/nodes/:id` | 无 | 删除节点并规范化所属组剩余节点 `sort_order`；自动端口模式下同步重排未固定节点端口并应用内核，返回 `200 { "message": "node deleted" }`（`400`/`404`/`500`）。 |
| `GET /api/nodes/:id/export` | 无 | 导出单节点出站分享链接，返回 `200 { "link": "..." }`；`direct`、策略节点或多 Peer WireGuard 等不支持导出项返回 `400`（不存在 `404`）。 |
| `POST /api/nodes/:id/check-ip` | 无 | 成功返回 `200` 出口 `IPInfo` 与 `check_status`、`check_error`、`checked_at`（`custom` 前置额外返回 `upstream`）。无端口实体节点使用临时隔离内核探测，无端口策略节点直接经运行中的本机策略调度器探测。禁用或定义非法返回 `400`，不存在 `404`，探测期间配置变化 `409`，限流 `429`，探测失败 `502` 并持久化失败状态与脱敏原因。 |

### 节点分组

节点创建和批量导入支持可选 `group_id`，省略时归入排序第一组；更新节点时省略该字段会保留原组。分组字段上限与范围：`name` 最多 255 字符，`start_port` 为 `1024–65535`，`subscription_interval_minutes` 为 `1–525600`，`subscription_url` 最长 `4096` 字符（开启自动更新须配置 URL）。创建分组默认 `allocate_port=true`、`preserve_inbound_ports=false`、`start_port=30001`、`subscription_interval_minutes=60`。

| 接口 | 行为 |
| --- | --- |
| `GET /api/node-groups`、`GET /api/node-groups/:id` | 返回组设置（含 `allocate_port`、`preserve_inbound_ports`、`start_port`）、分组前置代理配置及检测结果（`upstream_enabled` / `upstream_type` / `upstream_config` / `upstream_ip` / `upstream_location` / `upstream_country_code` / `upstream_latency` / `upstream_error` / `upstream_revision`）、`revision`、`node_count`、`enabled_node_count`、最后订阅时间与错误。已鉴权管理员能读取完整订阅地址。 |
| `POST /api/node-groups`、`PATCH /api/node-groups/:id` | 创建需提供非空 `name`（成功 `201`），局部更新成功返回 `200` 完整分组。`preserve_inbound_ports` 依赖 `allocate_port=true`：最终 `allocate_port=false` 且显式请求 `preserve_inbound_ports=true` 返回 `400`；关闭 `allocate_port` 自动将 `preserve_inbound_ports` 归零；将 `preserve_inbound_ports` 由 `true` 切回 `false` 时仅重排本组未固定端口，空组或有效路由未变化时仅提交数据库、不重启内核。支持启停与配置分组级前置代理（`PATCH` 可附 `expected_upstream_revision` / `expected_upstream_enabled` 核对编辑基线，冲突返回 `409` 及 `code: upstream_conflict`）。关闭端口分配遇到固定端口返回 `409`、`code: pinned_ports_exist` 与 `pinned_node_ids`，传入 `release_pins: true` 可一并释放。 |
| `POST /api/node-groups/:id/upstream/check-ip` | 通过临时隔离 `sing-box` 进程检测**已保存**的分组前置代理出口 IP（不要求「启用分组前置代理」开关打开，防环路边界与正式启用一致）。成功返回 `200` 并落库检测结果；未配置或定义非法/命中防环路返回 `400`；分组不存在返回 `404`；检测期间分组前置定义被并发修改返回 `409`；上游测速限流返回 `429`（附 `Retry-After` 头）；探测失败返回 `502` 并持久化脱敏错误。 |
| `DELETE /api/node-groups/:id` | 成功返回 `200 { "deleted": <id> }`。删除最后一组返回 `409`（`code: last_group`）；非空组须指定 `?mode=move&target_group_id=<正整数>`（不得为本组）或 `?mode=delete_nodes`，否则返回 `409`（`code: group_not_empty`）。 |
| `POST /api/node-groups/reorder` | `{ "ids": [...] }` 须为正整数、无重复的全部分组 ID，成功返回 `200 { "ids": [...] }`（`400`/`500`）。 |
| `POST /api/nodes/batch-move` | 提交非空无重复正整数 `ids` 与目标 `group_id`，按目标组设置分配或释放端口，并在实际前置链路变化时自动重置受影响节点的出口检测状态，成功返回 `200 { "group_id", "ids" }`（`400`/`404`/`500`）。 |
| `POST /api/node-groups/:id/subscription/refresh` | 增量更新本组托管节点，成功返回 `200 { "added", "updated", "removed", "unchanged", "warnings" }`；无 URL 返回 `400`，分组不存在 `404`，正在刷新/基线变化/身份歧义 `409`，空订阅或部分非法订阅 `422`，抓取失败 `502`，保存或应用失败 `500`。抓取失败或空订阅绝不清空已有节点。 |

`POST /api/nodes/reorder` 支持 `group_id` 和完整组内 `nodes` 序列。省略 `group_id` 时沿用全量节点格式。端口在所有组之间唯一。节点位于无端口组时，已启用的出站依然可以被引用。

### 4.3 批量操作与解析接口

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `POST /api/parse-link` | `{ "link": "..." }` | 解析单条分享链接但不落库，返回 `200 { "type", "name", "config" }`（`config` 为 JSON 字符串）；格式非法返回 `400`。 |
| `POST /api/nodes/batch-import` | `{ "content"?: "...", "links"?: [...], "source_type"?: "auto"\|"subscription"\|"http_proxy", "enabled"?: bool, "group_id"?: int }` | **非空白** `content` 优先于 `links`。执行抓取展开、分组 `sing-box check` 预检隔离、端口分配与联合复校；无可解析节点返回 `400`，目标组不存在 `404`，存储或最终应用失败 `500`；若解析出候选但全部被内核校验拒绝，仍返回 `200` 且 `success: 0`。正常返回 `200 { "total", "success", "failed", "results": [...] }`（成功项含 `success/id/name/port/username/password` 及可选 `source/link`，失败项含 `source/error`）。 |
| `POST /api/nodes/batch-delete` | `{ "ids": [1, 2, 3] }` | 批量删除节点；要求全部为正整数 ID、无重复且全部存在；返回 `200 { "message", "deleted_count" }`（`400`/`404`/`500`）并应用内核。 |
| `POST /api/nodes/batch-export` | `{ "ids": [1, 2, 3] }` | 逐项导出选中节点分享链接，整体返回 `200`（成功项含 `id/success/name/type/link`，缺失或不可导出项在 `results` 中以 `id/success:false/error` 逐项反馈）；仅空列表或非法 JSON 返回 `400`。 |
| `POST /api/nodes/batch-auth` | `{ "node_ids": [1, 2], "auth_enabled"?: bool, "username": "...", "password": "..." }` | 批量设置或清空选中节点的入站认证（账密需成对、用户名不含 `+`），校验全部 ID 有效且无重复后应用内核，返回 `200 { "message": "authentication updated" }`（`400`/`404`/`500`）。 |
| `POST /api/nodes/reorder` | `{ "group_id"?: int, "nodes": [{ "id": 1, "sort_order": 0 }, ...] }` | 重排节点顺序；无论是否指定 `group_id`，均须提交目标范围内的完整节点集合且 `sort_order` 为 `0..N-1` 唯一排列。自动模式下重排未固定端口，保留模式下不改动端口；返回 `200 { "message": "nodes reordered" }`（`400`/`404`/`500`）。 |

---

## 5. 全局设置接口（需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/settings` | 无 | 返回 `200` 全局前置代理及其检测结果、`node_list_column_widths`、`inbound_tls_cert_path`、`inbound_tls_key_path` 与实时扫描结果 `inbound_tls`（含 `ready`、`selection_mode`、`active` 及候选 `certificates` 列表）。`start_port` 与 `preserve_inbound_ports` 通过分组接口读取。 |
| `PUT /api/settings` | 可选 `admin_password`、`global_upstream_enabled`、`global_upstream_type`、`global_upstream_config`、`node_list_column_widths`、`inbound_tls_cert_path`、`inbound_tls_key_path` | 返回 `200 { "message", "changed", "node_list_column_widths" }`（修改密码时附带改密标志；校验失败 `400`）。若显式传入非 `null` 旧字段 `preserve_inbound_ports` 直接返回 `400` 提示已迁移至分组设置；旧字段 `start_port` 会被忽略。选择 `inbound_tls_cert_path` / `inbound_tls_key_path` 须匹配扫描到的有效证书套件（传空可退回自动选择），实际有变化时触发内核重载。`node_list_column_widths` 仅接受白名单列键（`name/port/user/pass/type/server/region/ip/latency`）与 `40–1200` 像素整数（传 `{}` 恢复默认，拒绝 `null`），单独更新列宽仅写数据库。管理员密码至少 8 个 Unicode 字符、最多 72 个 UTF-8 字节，改密成功立即吊销全部会话。 |
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

在 `POST /api/nodes` 或 `PUT /api/nodes/:id` 中使用 `type=failover`、`type=loadbalance` 或 `type=urltest`。`config` 沿用节点接口的 JSON 字符串格式，其解码示例如下。

```json
{"member_ids":[12,9],"member_sources":[{"type":"group","id":2},{"type":"node","id":9}],"strategy":"round-robin","tolerance":50,"health_check_url":"https://example.org/health","health_check_interval":30}
```

`member_ids` 按优先级排序，重复 ID 会合并；也支持通过可选 `member_sources`（每项为 `{"type":"group"|"node","id":整数}`）按分组整组引入实体节点并自动随组内节点增删、移动、拖拽排序与订阅刷新同步（组内实体节点跟随 `(sort_order ASC, id ASC)` 分组顺序展开；面板点击「断链」时会把该组当前实体成员按原位置固化为独立成员保留优先级，当所有分组来源均移除后 `member_sources` 自动省略并退回静态 `member_ids` 管理）。创建或编辑时至少选择一个已有实体节点或有效分组，支持跨组与无端口成员。故障转移忽略并清理 `strategy` 与 `tolerance`。负载均衡清理 `tolerance`，支持 `round-robin`（面板显示“轮询”）和 `consistent-hashing`（面板显示“目标粘性 / Target Sticky”，采用基于规范化目标主机 `SHA256(host + NUL + 成员ID)` 的 Rendezvous/HRW 排名，忽略端口、域名与 IP 独立计算，仅在建连阶段选路）。最低延迟（`urltest`）清理 `strategy`，支持 `tolerance` 切换容差（单位毫秒，范围 `0–5000`，默认 `50`，允许显式 `0`，越界返回 `400`），按统一健康探测测得的基准延迟升序选路（不混入真实业务拨号耗时），主用失效即时顺延次快健康成员并触发紧急探测。健康检查仅探测已启用实体成员，周期为 10 至 3600 秒（默认 30 秒），未配置 URL 时默认探测 `1.1.1.1:443` TCP 连通性（自定义 URL 发送 `HEAD`、接受 `200–399`、不跟随重定向），共享 8 并发异步探测并在无流量空闲后懒暂停；`failover`/`loadbalance` 所有成员本次均建连失败时不立即标记全部不健康。策略的前置模式为 `none`，成员沿用各自的前置设置。策略支持普通入站认证、TCP 复用、`mixed` / `https` 入站模式及 `POST /api/nodes/:id/check-ip`。分享链接仅适用于支持该格式的实体节点。

节点响应附加 `effective_member_ids`、`policy_active_member_id`、`policy_member_health` 和 `policy_warnings`。健康记录包括 `node_id`、`healthy`、`latency`（毫秒）、`last_error`、`checked_at`。删除成员会移除引用，停用成员会保留引用并暂停转发。成员全部不可用时可查询告警、编辑策略并添加替代成员。

### 当前连接

管理员可通过 `GET /api/connections` 获取快照（内核未运行时返回 `200` 空快照，本机采集或解码异常返回 `503`），或通过 `GET /api/connections/stream` 订阅 SSE 的 `snapshot` 事件。采样间隔约 1.2 秒。内核停止或连续三次采样失败时，服务端发送一次 `error` 事件，数据为 `{"error":"原因"}`。采样恢复后继续发送 `snapshot`。服务端逐帧并定时复核管理员会话。退出、过期或修改密码后，下次复核会关闭流。单次轮询保留 5 秒计数基线，供后续轮询计算速率。

- 顶层字段为 `runtime_epoch`、`sampled_at`、`active_connections`、`client_count`、`route_count`、`total_upload_bps`、`total_download_bps`、`routes`。
- 每条路径包含 `key`、`client_ip`、`inbound_port`、`inbound_node_id`、`inbound_node_name`、`route_id`、`outbound_node_id`、`outbound_node_name`、`outbound_type`、`policy_member_id`、`policy_member_name`、`upstream_label`、`active_count`、`upload_bytes`、`download_bytes`、`upload_bps`、`download_bps`，以及可选字段 `client_country_code`（公网客户端离线识别的 2 位大写国家码）、`client_is_private`（内网/回环/链路本地/CGNAT 客户端为 `true`）、`inbound_mode`（`mixed` 或 `https`）与 `upstream_source`（无前置代理时省略，存在时取值为 `custom`、`group`、`global` 或 `legacy`）。
- 速率单位为字节/秒。内核首次采样或重载后第一帧的速率为 `null`。`route_id` 始终对应实际命中的复用用户名。单个策略有多个成员同时访问相同目标时，官方内核可能缺少区分外层连接所需的信息，无法唯一关联的成员 ID 返回 `0`，名称为空。

### 日志与流量统计

以下接口均需管理员认证。时间参数使用 RFC3339（支持纳秒与时区，反向区间返回 `400`）。

| 请求 | 参数与返回 |
| --- | --- |
| `GET /api/logs` | 支持 `source`、`level`、`q`、`client_ip`、`from`、`to`、`cursor`（非负整数）、`limit`（正整数，默认 50，超过 200 自动截断）。成功返回 `200 { "items", "next_cursor", "storage_mode", "persisted", "dropped_events" }`（`next_cursor=0` 表示无后续页）；参数非法返回 `400`，持久库查询异常返回 `503`。每条日志含 `id`、`ts`、`source`、`level`、`message`、`client_ip`、`inbound_port`、`outbound_name`、`policy_name`、`member_name`。 |
| `GET /api/logs/settings` | 返回 `200` 脱敏配置（`enabled`、`min_level`、`store_mode`、`remote_driver`、`local_sqlite_path`、`remote_dsn_masked`、`remote_url_masked`、`has_remote_dsn`、`has_remote_token`、`retention_days`、`max_size_mb`）与 `status`（`active`、`driver`、`size_bytes`、`dropped_events`、`last_error`）。 |
| `PUT /api/logs/settings` | 显式保存完整持久化配置（`min_level` 为 `INFO`/`WARN`/`ERROR`，`store_mode` 为 `primary`/`local_sqlite`/`independent`，`remote_driver` 为 `sqlite`/`turso`/`mysql`/`postgres`，`retention_days` 为 `0–36500`，`max_size_mb` 为 `0–1048576`；其中布尔与数值按输入提交，仅空枚举沿用旧值）。`dsn_action` 控制 `remote_dsn` 或 Turso `remote_url`，`token_action` 控制 `remote_token`，各自支持 `keep`、`replace`、`clear`（默认 `keep`）。原地址配置了令牌时，更换 Turso 地址须显式替换或清除令牌。启用前验证目标库的读、写和删除权限，失败返回 `400` 并继续沿用上次生效的设置。 |
| `GET /api/logs/traffic` | 支持 `from`、`to`、`client_ip`、`inbound_port`、`outbound_node_id`、`q`（不分页）。成功返回 `200 { "persisted", "upload_bytes", "download_bytes", "total_bytes", "items", "buckets" }`（未启用持久化时 `persisted=false` 且数组为空）；查询会合并待写入的采样增量；参数非法返回 `400`，存储异常返回 `503`。 |
