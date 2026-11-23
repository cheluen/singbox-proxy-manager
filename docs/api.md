# HTTP API 参考

本文档基于 `1.7.23` 后端路由注册（`backend/main.go`、`backend/api/`）整理全部 **26 个业务接口**（2 个公开接口 + 24 个鉴权接口）。

---

## 1. 通用约定

- **基础前缀**：`/api`（默认服务监听 `http://0.0.0.0:30000`）。
- **鉴权方式**：受保护接口需携带 `Authorization` 请求头，同时支持 `Authorization: Bearer <token>`（前缀大小写不敏感）与裸令牌 `Authorization: <token>`。会话无效或已吊销时返回 `401 Unauthorized`。
- **请求体限制**：默认受 `API_MAX_BODY_BYTES`（`1048576` 字节，即 `1 MiB`）约束。
- **跨域边界**：默认关闭 CORS；配置 `CORS_ALLOWED_ORIGINS` 时仅做精确 Origin 匹配，允许方法为 `GET, POST, PUT, DELETE, OPTIONS`（**不含 `PATCH`**）。
- **重要差异提醒**：`POST /api/nodes` 与 `POST /api/nodes/batch-import` 中的 `enabled` 字段为 Go 普通布尔值，**API 请求中省略时默认为 `false`**（管理面板 UI 表单默认勾选为 `true`）。

---

## 2. 公开接口（无需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/version` | 无 | 返回 `{ "version": "1.7.23", "update": { ... } }`。按需检查 GitHub 最新 Release 并缓存 `SBPM_UPDATE_CHECK_TTL`；即使上游查询失败仍返回 HTTP `200` 并在 `update` 中携带错误信息。 |
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
| `GET /api/nodes` | 无 | 按 `sort_order` 升序返回全部节点数组（**无服务端分页**）。 |
| `GET /api/nodes/:id` | 无 | 返回单个节点完整记录；不存在返回 `404`。 |
| `POST /api/nodes` | 节点 Upsert 字段 | 创建节点、校验并应用至 `sing-box`，成功返回 `201`。自动端口模式下忽略请求中的 `inbound_port`；保留端口模式下填 `0` 表示自动分配。 |
| `PUT /api/nodes/:id` | 节点 Upsert 字段 | 全量更新节点并原子应用；保留原 `sort_order` 与 `inbound_port_pinned` 状态；未传入 `tcp_reuse_enabled` 或前置代理字段时保留原值。 |
| `PATCH /api/nodes/:id` | `{ "enabled"?: bool, "tcp_reuse_enabled"?: bool }` | 仅允许切换启用状态或 TCP 复用开关（至少提供一项）；严格拒绝未知字段与尾随 JSON 数据。 |
| `PUT /api/nodes/:id/upstream` | `{ "upstream_mode", "upstream_type", "upstream_config" }` | 更新节点前置代理策略（`none` / `global` / `custom`）并执行防环路与内核校验，返回更新后节点。 |
| `PUT /api/nodes/:id/remark` | `{ "remark": "..." }` | 更新节点备注（最多 `1024` 字符），仅改数据库，**不重启 `sing-box`**。 |
| `PUT /api/nodes/:id/port-pin` | `{ "pinned": true }` | 切换自动端口模式下的端口固定标记，**不重启 `sing-box`**；处于「保留入站端口」模式时拒绝并返回 `409`。 |
| `PUT /api/nodes/:id/replace` | `{ "link": "...", "update_name"?: bool }` | 使用分享链接替换该节点的出站协议与配置（`update_name` 默认 `true`）；保留入站端口、账密与备注，清空旧出口 IP/延迟数据并重新应用内核。 |
| `DELETE /api/nodes/:id` | 无 | 删除节点并规范化剩余节点 `sort_order`；自动端口模式下同步重排未固定节点端口并应用内核。 |
| `GET /api/nodes/:id/export` | 无 | 导出单节点出站分享链接，返回 `{ "link": "..." }`；`direct` 或多 Peer WireGuard 等不支持导出的节点返回 `400`。 |
| `POST /api/nodes/:id/check-ip` | 无 | 通过节点本地混合入站（先 HTTP、失败回退 SOCKS5）检测出口 IP，返回 `IPInfo`（含 `ip / country / country_code / city / region / location / latency / transport / http_error`）；若节点配置了自定义前置代理，同时返回嵌套的 `upstream` 检测结果。禁用节点返回 `400`；检测期间若节点自定义前置定义被并发修改返回 `409`；探测失败返回 `502` 或上游限流 `429`（并清空该节点旧出口显示）。 |

### 4.3 批量操作与解析接口

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `POST /api/parse-link` | `{ "link": "..." }` | 解析单条分享链接但不落库，返回 `{ "type", "name", "config" }`（`config` 为 JSON 字符串）。 |
| `POST /api/nodes/batch-import` | `{ "content"?: "...", "links"?: [...], "source_type"?: "auto"\|"subscription"\|"http_proxy", "enabled"?: bool }` | `content` 优先于 `links`。执行抓取展开、分组 `sing-box check` 预检隔离、端口分配与联合复校；无可解析节点时返回 `400`；若解析出候选但全部被内核校验拒绝，仍可能返回 `200` 且 `success: 0`；正常返回 `{ "total", "success", "failed", "results": [...] }`。 |
| `POST /api/nodes/batch-delete` | `{ "ids": [1, 2, 3] }` | 批量删除节点；要求全部为正整数 ID、无重复且全部存在；返回含 `deleted_count` 的结果并应用内核。 |
| `POST /api/nodes/batch-export` | `{ "ids": [1, 2, 3] }` | 逐项导出选中节点分享链接，返回成功数、失败数及明细列表。 |
| `POST /api/nodes/batch-auth` | `{ "node_ids": [1, 2], "auth_enabled"?: bool, "username": "...", "password": "..." }` | 批量设置或清空选中节点的入站认证（账密需成对，用户名不含 `+`），校验全部 ID 有效且无重复后应用内核。 |
| `POST /api/nodes/reorder` | `{ "nodes": [{ "id": 1, "sort_order": 0 }, ...] }` | 重排节点顺序；必须提交数据库全部节点且 `sort_order` 为 `0..N-1` 完整排列。自动模式下重排未固定端口，保留模式下不改动端口。 |

---

## 5. 全局设置接口（需鉴权）

| 方法与路径 | 请求体 | 响应与行为说明 |
| --- | --- | --- |
| `GET /api/settings` | 无 | 返回 `start_port`、`preserve_inbound_ports`、全局前置代理配置及其最近检测结果（**绝不返回管理员密码哈希**）。 |
| `PUT /api/settings` | 可选字段：`start_port`、`preserve_inbound_ports`、`admin_password`、`global_upstream_enabled`、`global_upstream_type`、`global_upstream_config` | 更新全局配置并按需重新应用内核。有变更时返回含 `changed: true` 与 `password_changed` 的响应；配置无变化时仅返回 `message` 与 `changed: false`（不含 `password_changed`）。`start_port` 范围 `1024–65535`；修改 `admin_password` 要求至少 `8` 个 Unicode 字符且不超过 `72` UTF-8 字节，修改成功后立即递增 `auth_generation` 吊销全部会话。 |
| `POST /api/settings/upstream/check-ip` | 无 | 通过临时隔离 `sing-box` 进程检测**已保存**的全局前置代理出口 IP（不要求「启用全局前置代理」开关处于打开状态）；若检测期间全局前置定义被并发修改，返回 `409`。 |

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
