# 协议支持、导入导出与链接规范

本文档记录 SingBox Proxy Manager（`1.7.26`）支持的全部代理协议、面板表单字段、`sing-box 1.12.12` 配置映射规则、分享链接与结构化批量导入规范，以及导出与手动去重边界。

---

## 1. 支持的 12 种正式协议类型

前端面板与后端数据模型共定义 **12 种出站协议类型**（含 `socks5h`）。所有非 `direct` 节点共用「节点名称（UI 上限 255 字符）」「服务器地址」「服务器端口」「入站端口」「启用状态」「入站认证（用户名/密码）」字段；表单新建非 WireGuard 节点时默认服务器端口为 `443`，切换至 WireGuard 时仅在当前端口为空或为 `443` 时自动调整为 `2408`（已有自定义端口会保留）。

> **关于原始 JSON 与自定义协议的边界**：
> - 节点编辑弹窗底部的「原始配置」折叠区为**只读展示**（`<pre>`），不能直接编辑任意 JSON；仅 WireGuard 提供可编辑的「多 Peer JSON」高级字段。
> - 后端 API 的 `config` 字段采用**严格 JSON 解码**，拒绝未知顶层字段；虽支持预定义的 `tls_options`、`transport_options` 等 `sing-box 1.12.12` 原生扩展结构，但**不支持任意 `custom` 出站协议透传**。
> - 面板编辑已有节点且未更换协议时，前端会将表单变更与原配置合并，自动保留表单未展示但合法的后端字段。
> - 虽然 `sing-box` 重编译包含 `with_tailscale` 等上游构建 tag，但本应用**不提供** Tailscale 节点类型。

### 协议字段与核心能力矩阵

| 协议 (`type`) | `sing-box` 映射 | 核心协议字段（面板可编辑） | 分享链接导入 | 分享链接导出 | Clash 结构导入 |
| --- | --- | --- | :---: | :---: | :---: |
| `direct` | `outbounds` (`direct`) | 无协议专用字段（隐藏出站服务器与端口） | 不支持 | 不支持 | 不支持 |
| `ss` | `outbounds` (`shadowsocks`) | 加密方式（`aes-128-gcm` / `aes-256-gcm` / `chacha20-ietf-poly1305` / `2022-blake3-aes-128-gcm` / `2022-blake3-aes-256-gcm`）、协议密码、Plugin、Plugin Options | 支持 | 支持 | 支持 |
| `vless` | `outbounds` (`vless`) | UUID、Flow（空 / `xtls-rprx-vision`）、传输（TCP / WS / gRPC / QUIC）、路径、gRPC 服务名、安全层（None / TLS / Reality）、SNI、ALPN、指纹、Reality 公钥、Short ID | 支持 | 支持 | 支持 |
| `vmess` | `outbounds` (`vmess`) | UUID、Alter ID（`0–65535`）、加密策略（`auto` / `aes-128-gcm` / `aes-128-cfb` / `chacha20-poly1305` / `none`）、传输（TCP / WS / HTTP / gRPC）、路径、服务名、TLS、SNI、ALPN | 支持 | 支持 | 支持 |
| `trojan` | `outbounds` (`trojan`) | 密码、传输（TCP / WS / gRPC / HTTP / HTTPUpgrade）、Host、路径、HTTP 方法、gRPC 服务名、SNI、ALPN、指纹、跳过证书校验 | 支持 | 支持 | 支持 |
| `hy2` | `outbounds` (`hysteria2`) | 密码、上/下行限速（Mbps）、混淆类型（如 `salamander`）、混淆密码（`salamander` 必填）、SNI、ALPN、指纹、跳过证书校验 | 支持 | 支持 | 支持 |
| `tuic` | `outbounds` (`tuic`) | UUID、密码、拥塞控制（`cubic` / `new_reno` / `bbr`）、UDP 转发模式（`native` / `quic`）、SNI、ALPN、指纹、跳过证书校验、0-RTT | 支持 | 支持 | 支持 |
| `anytls` | `outbounds` (`anytls`) | 密码、SNI、ALPN、指纹（`chrome` / `firefox` / `safari` / `edge` / `random`）、跳过证书校验、空闲检查间隔、空闲超时、最小空闲会话数 | 支持 | 支持 | 支持 |
| `socks5` | `outbounds` (`socks`, v5) | 出站用户名、出站密码 | 支持 | 支持 | 支持 |
| `socks5h` | `outbounds` (`socks`, v5) | 同 `socks5` | 支持 | 支持 | 支持 |
| `http` | `outbounds` (`http`) | 出站用户名、出站密码、HTTP/HTTPS 开关、HTTPS SNI、跳过证书校验 | 支持 | 支持 | 支持 |
| `wireguard` | `endpoints` (`wireguard`) | 本地 CIDR、本地私钥、对端公钥、预共享密钥、Allowed IPs、Cloudflare Reserved 字节（支持逗号分隔数字或 Base64）、系统网卡开关与名称、MTU（`0–65535`）、Workers（`0–128`）、域名解析器与策略、Routing Mark、连接超时、UDP 分片、多 Peer JSON（填写后优先于单 Peer 字段） | 支持 | 仅单 Peer | 支持 |

### 协议合法性校验要点

- **传输层限制**：支持 `tcp / ws / http / quic / grpc / httpupgrade`（`h2` 自动映射为 `http`，`gun` 自动映射为 `grpc`）；拒绝 `kcp / mkcp` 及未知传输类型。
- **VLESS 限制**：仅允许 `none` 加密，不支持 Reality `spider_x` 参数；Packet Encoding 仅允许实现支持的值（如 `packetaddr`、`xudp`）。
- **TUIC / Hysteria2 兼容归一**：TUIC 旧别名 `reduce_rtt` 自动归一化至 `zero_rtt_handshake`（显式提交 `zero_rtt_handshake: false` 时优先生效并清除旧别名）；Hysteria2 出站生成与分享导出共用同一套有效混淆解析规则。
- **WireGuard 配置位置**：按 `sing-box 1.12` 规范生成至顶层 `endpoints`，使用 `local` 域名解析器时会自动注入对应 DNS 配置。

---

## 2. 分享链接编解码规范（15 种 URI Scheme）

后端 `backend/services/sharelink.go` 与 `sharelink_export.go` 支持以下 **15 种 URI Scheme**：

```text
ss://  vless://  vmess://  trojan://  hysteria2://  hy2://  tuic://  anytls://
socks://  socks5://  socks5h://  wireguard://  wg://  http://  https://
```

| 协议 | 解析与导出细节 |
| --- | --- |
| Shadowsocks | 兼容 SIP002 与传统整体 Base64 格式；支持 Plugin 参数；2022 密码导出时自动 URL 编码。 |
| VMess | 同时支持传统 `vmess://<base64-json>` 与 URL 形式；导出按节点配置自动选择对应形式。URL 形式**必须提供显式端口**。 |
| VLESS / Trojan | 标准 URL 形式，**必须提供显式有效端口**（不可省略端口期待默认 `443`）。 |
| Hysteria2 | 兼容 `hysteria2://` 与 `hy2://`，支持多端口/端口范围；省略端口默认 `443`。 |
| TUIC / AnyTLS | 标准 URL 形式；省略端口默认 `443`。 |
| SOCKS | 兼容 `socks://`、`socks5://`、`socks5h://`，默认端口 `1080`；导出按类型输出 `socks5://` 或 `socks5h://`。 |
| HTTP / HTTPS | 默认端口 `80` / `443`；导出时自动附带查询参数 `proxy=1`，防止再次自动批量导入时被误判为远程订阅地址。 |
| WireGuard | 兼容 `wireguard://` 与 `wg://`；导入要求**显式端口**；导出仅支持**单 Peer** 节点，多 Peer 节点无法导出为分享链接。 |
| Direct | 无 URI Scheme，仅可通过面板表单或 HTTP API 创建，不可导出为分享链接。 |

---

## 3. 批量导入机制与安全边界

面板「批量导入」弹窗提供三种来源模式（对应 `source_type`）：
- `auto`（自动识别）：根据内容自动判断分享链接、Base64 文本、Clash 结构化配置或远程订阅 URL。其中顶层 `http://` / `https://` 仅在包含 `proxy=1`、`proxy=true` 或 `proxy=spm` 参数时视为 HTTP 代理节点，其余默认按远程订阅 URL 抓取。
- `subscription`（订阅模式）：强制将顶层 `http://` / `https://` 作为订阅源展开。
- `http_proxy`（HTTP 代理模式）：将每行 `http://` / `https://` 明确解析为 HTTP(S) 出站代理节点。

### 支持的批量内容形态

1. **多行分享链接**：每行一条受支持的 URI Scheme 链接。
2. **Base64 编码订阅文本**：支持标准 Base64 与 URL-Safe Base64（有无 `=` padding 均可），但解码后正文必须包含 `://`、`proxies:`、`proxy-groups:` 或 `proxy-providers:` 之一才会被识别为有效订阅（最大展开深度为 `3`；因此裸 `payload:` YAML 或不含上述关键字的纯 Clash JSON 仅支持直接结构化文本导入，不保证其 Base64 形式可识别）。
3. **Clash 形状的 YAML / JSON**：仅提取以下三个位置的节点列表：
   - 顶层 `proxies` 数组
   - 顶层 `payload` 数组
   - `proxy-providers` 中 `type: inline` 下的 `payload`
   > 注意：不会导入 Clash 规则（`rules`）或策略组（`proxy-groups`），不会自动抓取远程 `proxy-providers`，也不接受任意完整 `sing-box` 配置文件或顶层裸 JSON 数组；JSON 仅作为上述 Clash 结构的兼容语法被解析。
4. **远程 HTTP(S) 订阅链接**：单次一次性抓取并展开，**不会在后台创建定时自动更新任务**。抓取受严格 SSRF 与资源限制（仅公网 IP、禁用宿主代理、单次 `15s` / 整次 `70s`、最多 `20` 个 URL、单响应 `<= 5 MiB`、单次节点与错误项总计 `<= 2000`、禁止二次联网抓取订阅内嵌套 URL、拒绝 HTML 正文）。

### 批量提交预检与失败隔离

- 批量导入在服务端提交阶段自动完成分批预检：按 `detour` 依赖分组（最多 `4` 组并发）调用 `sing-box check` 隔离异常节点，再对通过项重新分配入站端口并做联合复校；即使选择导入后禁用节点，预检时也会先按启用状态验证配置合法性。
- 批量导入默认由后端自动为每个新节点生成 12 字符入站用户名与 24 字符入站密码。
- 部分成功时，弹窗逐项列出失败来源与原因，并在同一弹窗内禁止原样重复提交，避免已成功节点被重复导入；前端会对已启用导入的成功节点自动触发一轮出口 IP 检测。

---

## 4. 导出、复制连接与手动出站去重

- **分享链接导出**：单节点和批量导出均支持一键复制或下载为 `sbpm-nodes.txt`。批量导出时前端自动跳过 `direct` 节点，若包含多 Peer WireGuard 等不支持导出的节点会逐项反馈失败原因。本应用不提供导出完整 `sing-box JSON`、Clash YAML 或 CSV 功能。
- **列表与详情**：列表从左到右依次划分为基础标识（`#`、缩窄截断的节点名称）、入站信息（入站端口、明文入站用户名、固定宽度毛玻璃掩码入站密码，点击眼睛图标可显示完整密码；空凭据显示 `—`）和出站信息（节点类型、服务器地址、地区、节点IP、延迟）；行内详情在备注上方提供完整名称与复制按钮。
- **复制入站连接信息**：行内展开详情支持复制 HTTP URL、SOCKS5 URL、`host:port`、入站用户名、密码、出口 IP、路由号和 `curl -x ... http://httpbin.org/ip` 示例命令。路由号为开启 TCP 复用时显示的 `<用户名>+<入站端口>`。连接主机名取浏览器访问面板的 `hostname`。
- **手动出站去重**：批量导入阶段**不自动去重**。需要去重时，在列表中勾选目标范围后点击底部批量操作栏的「出站去重」：
  - 前端对 `type + config + upstream_mode + upstream_type + upstream_config`（JSON 对象键递归排序后）做严格比对。
  - 节点名称、备注、入站端口与入站认证凭据**不参与**重复判定。
  - 按当前列表顺序保留每组第一条，经二次确认后批量删除其余重复项。
