# 运维技术说明

本文以当前实现为基线，补充 README 中的部署、认证、端口与版本规则，不把历史验证结果当作本次测试结果。

## 环境变量与部署方式

### Compose

- `sb-proxy-ghcr` 和 `sb-proxy-local` 复用同一份公共配置，通过 `env_file` 读取与 `docker-compose.yml` 同目录的 `.env`；文件必须存在。
- `.env` 同时用于 Compose 变量插值与管理进程环境注入，不需要把文件挂载到容器。公共 `environment` 中的显式配置覆盖 `env_file`，其插值值来自宿主机环境、`.env` 或声明的默认值。
- 当前插值使用 `:-`，已解析的空值会采用声明默认值；已导出的空宿主机变量不会再改用 `.env` 的同名值。
- `CONFIG_VOLUME_HOST` 是宿主机目录；默认挂载目标固定为 `/app/config`。`CONFIG_DIR` 是容器内路径，修改它时必须让挂载目标同步指向该目录。
- 容器环境与 sing-box 子进程环境不是同一层：管理进程接收 `.env`，内核只继承最小环境和允许的非敏感白名单项。
- `.env` 改动后使用 `docker compose up -d` 重建容器。`docker compose restart` 只重启原容器，不重新读取其环境配置。
- 两种模式共用容器名 `sb-proxy`，不能同时运行。日志命令使用服务名 `sb-proxy-ghcr` 或 `sb-proxy-local`；`docker exec` 才使用容器名。

`sb-proxy-ghcr` 只有 `image`，没有 `build`，并设置 `pull_policy: always`。`up --build` 不会编译此服务的源码，但启动时仍会按拉取策略检查配置的镜像标签，不能据此认定一定重用旧镜像。可靠升级步骤是等待 CI 发布目标镜像，再执行 `pull` 和 `up -d`。

`sb-proxy-local` 配置了本地构建上下文；更新源码后使用 `up -d --build sb-proxy-local`。更换模式前先移除旧模式容器，保留配置卷，具体命令见 README。

### 独立二进制

启动时依次发现可执行文件同目录、当前工作目录的 `.env`，相同绝对路径只读取一次。加载不会覆盖已有进程环境变量；同名变量优先采用先加载的文件值。

未设置 `CONFIG_DIR` 时使用当前工作目录下的 `config`。`.env.example` 中的 `/app/config` 是 Docker 路径，二进制部署应改成实际数据目录。升级时保持工作目录、配置目录及数据库连接配置不变。

源码依据：[启动与环境加载](backend/main.go)、[Compose 配置](docker-compose.yml)、[内核命令与环境过滤](backend/services/singbox_command.go)。

## 并发与超时参数

以下五项在两种 Compose 模式中均会传入容器，独立二进制也使用相同变量：

| 变量 | 默认值 | 允许范围 | 生效范围 |
| --- | --- | --- | --- |
| `SBPM_SINGBOX_CHECK_CONCURRENCY` | `4` | `1–16` | 同时执行配置校验的槽位数 |
| `SBPM_SINGBOX_CHECK_TIMEOUT` | `10s` | `100ms–2m` | 单次配置校验，包括等待槽位、准备配置及校验子进程 |
| `SBPM_UPSTREAM_CHECK_CONCURRENCY` | `3` | `1–16` | 上游 IP 检测使用的临时内核进程槽位数 |
| `SBPM_UPSTREAM_CHECK_TIMEOUT` | `30s` | `1s–5m` | 单次上游检测的总时限，包括等待槽位 |
| `SBPM_BATCH_IMPORT_VALIDATION_TIMEOUT` | `45s` | `100ms–2m` | 批量导入在展开输入后，从基础状态读取、候选预检到最终提交的校验阶段 |

数值留空、格式错误或超出范围时回退到表中的默认值，不会截断为边界值。时长使用 Go duration 格式，例如 `500ms`、`10s`、`1m`。

批量导入的订阅抓取与展开在上述校验阶段之前执行，其总超时固定为 `70s`，单次抓取超时 `15s`，抓取并发为 `5`。候选分组校验并发固定为 `4`，实际校验进程还受 `SBPM_SINGBOX_CHECK_CONCURRENCY` 限制。因此 `SBPM_BATCH_IMPORT_VALIDATION_TIMEOUT` 不是整个 HTTP 请求或订阅抓取的总超时。

源码依据：[内核服务与配置校验](backend/services/singbox.go)、[数值范围解析](backend/services/singbox_log.go)、[上游检测](backend/services/upstream_ipcheck.go)、[批量校验与提交](backend/api/handlers.go)、[订阅展开](backend/services/batch_import.go)。

## 管理员密码同步

管理员密码哈希、环境密码的 bcrypt 指纹与会话代数保存在数据库设置中。启动时由 `ReconcileAdminPassword` 对齐：

| 数据库状态 | `ADMIN_PASSWORD` | 结果 |
| --- | --- | --- |
| 没有有效管理员密码 | 未设置、空值或全空白 | 启动失败，与 `BIND_ADDRESS` 无关 |
| 没有有效管理员密码 | 非空且不超过 72 字节 | 初始化密码和环境指纹，提升会话代数并清除已有会话 |
| 已有有效密码，未记录环境指纹 | 未设置或空值 | 保留数据库密码与会话 |
| 已有有效密码，未记录环境指纹 | 首次提供非空值 | 只记录环境指纹，不覆盖数据库密码或撤销会话 |
| 已有有效密码与环境指纹 | 非空值与已记录指纹一致 | 保留数据库密码与会话，包括面板内修改后的密码 |
| 已有有效密码与环境指纹 | 非空值与已记录指纹不同 | 覆盖数据库密码、更新指纹、提升会话代数并删除全部管理员会话 |
| 已有有效密码与环境指纹 | 未设置或空值 | 保留数据库密码与会话，不视为空密码重置 |

提供的环境密码超过 `72` 字节时启动失败。环境初始化不检查八字符下限，但应使用强密码；面板修改密码要求至少八个字符且不超过 `72` 字节。

历史内置默认密码的识别常量仅用于迁移防护：未记录环境指纹的旧默认密码不会被视为有效密码。这不是当前版本的默认登录密码，也不存在回环地址下的空密码首次设密入口。

面板改密更新密码哈希并提升会话代数，不改环境指纹；所以环境值保持不变时，下次启动不会覆盖面板密码。Compose 用户修改 `.env` 后需重建容器，否则管理进程仍收到旧环境值。

源码依据：[数据库初始化与密码对齐](backend/models/proxy.go)、[面板改密](backend/api/handlers.go)、[管理员会话](backend/api/auth_sessions.go)。

## 数据库选择

从以下变量中，按顺序选择第一个去除首尾空白后仍非空的值：

1. `DATABASE_URL`
2. `POSTGRES_DATABASE_URL`
3. `POSTGRES_URL`
4. `PGSQL_DATABASE_URL`
5. `PGSQL`
6. `MYSQL_DATABASE_URL`
7. `MYSQL`

变量名称不决定驱动：`postgres://`、`postgresql://` 使用 PostgreSQL，`mysql://` 或可识别的原生 MySQL DSN 使用 MySQL。常规同时配置 PostgreSQL 专用变量和 MySQL 专用变量时，前者优先；若 `DATABASE_URL` 非空，仍由它决定连接。

选中的连接串格式不支持或连接失败时直接返回错误，不尝试下一个变量。只有全部 URL 变量为空时，才检查 Turso；`TURSO_DATABASE_URL` 与 `TURSO_AUTH_TOKEN` 均非空时使用 Turso，两项不完整则记录日志并回退到本地 SQLite。

MySQL URL 未显式提供 `tls` 时，`localhost`、回环 IP、私有 IP 与未指定地址（`0.0.0.0`、`::`）不自动启用 TLS，其他主机名和 IP 默认启用。系统数据库名称或空名称映射为 `test`，`MYSQL_DATABASE_NAME` 可覆盖。管理实例的数据库租约与上述数据库类型无关，同一个数据库只能有一个活跃管理实例。

源码依据：[连接选择与 MySQL URL 规范化](backend/database/database.go)、[单实例租约](backend/database/instance_lease.go)。

## 节点认证与端口策略

### 入站认证

新增表单默认开启认证，入站 `username` 和 `password` 初始为空；必须手动填写或点击「生成」。分享链接解析只回填节点配置，不等于创建入站凭据。关闭认证后前端提交两个空值，后端会拒绝只填一项或认证状态与凭据不一致的请求。

批量导入为候选节点生成 `12` 字符用户名与 `24` 字符密码，成功提交后才成为已保存节点。生成的用户名不包含路由分隔符 `+`。入站凭据与上游协议配置中的用户名、密码是不同字段。

源码依据：[表单初始值](frontend/src/core/protocolForm.js)、[生成与提交按钮](frontend/src/shared/NodeEditor.jsx)、[链接回填](frontend/src/shared/NodeDialogs.jsx)、[后端导入与认证校验](backend/api/handlers.go)。

### 端口分配

`start_port` 保存在全局设置中，新数据库默认 `30001`，可设置为 `1024–65535`。添加节点只读取这个值，不负责设置起始端口。

| 操作 | 自动模式：`preserve_inbound_ports=false` | 保留模式：`preserve_inbound_ports=true` |
| --- | --- | --- |
| 新增单个节点 | 忽略请求中的手动端口，从起始端口找可用端口 | 可指定端口；未指定时自动找可用端口 |
| 批量导入 | 自动分配新节点端口 | 仍自动分配新节点端口 |
| 拖拽或键盘排序 | 只重分配未固定节点，跳过固定端口 | 只更新排序，不改端口 |
| 修改起始端口 | 重分配未固定节点，固定端口不变 | 现有端口不变，新分配使用新的起始值 |
| 关闭全局保留模式 | 按顺序重分配未固定节点，固定端口不变 | 切换后进入自动模式 |

固定端口由节点的 `inbound_port_pinned` 标记控制，不能把全局保留模式与单节点固定混为一谈。自动重分配先预留全部固定端口，再按 `sort_order`、`id` 遍历未固定节点；固定节点即使被移动位置也不会重新分配。全局保留模式下不能切换单节点固定标记，原有标记会保留。

源码依据：[新增、排序与设置接口](backend/api/handlers.go)、[自动重分配](backend/api/node_mutation.go)。

## 版本提示与缓存

面板初始化或整体刷新会请求 `/api/version`；收到 `sbpm-dusk:external-change` 事件时也会整体刷新。前端的五秒定时器只读取 `/api/runtime/status`，没有周期性的版本查询任务。

后端收到 `/api/version` 请求时按需查询 GitHub 最新 Release，并按当前运行版本缓存查询结果，包括失败结果：

| 环境变量 | 默认值 | 含义 |
| --- | --- | --- |
| `SBPM_UPDATE_CHECK_DISABLED` | `false` | 禁用远程更新检查，仍返回当前运行版本 |
| `SBPM_UPDATE_CHECK_REPO` | `cheluen/singbox-proxy-manager` | 查询最新 Release 的 GitHub 仓库 |
| `SBPM_UPDATE_CHECK_TTL` | `6h` | 服务端结果缓存有效期，不是每六小时自动运行一次 |
| `SBPM_UPDATE_CHECK_TIMEOUT` | `5s` | 实际 GitHub 请求的超时 |

缓存未过期时复用已有结果；过期后也必须等到下一次版本请求才重新查询。服务重启会清空进程内缓存。最新 Release 的语义版本高于运行版本时，面板显示向上箭头并提供 Release 链接。

项目版本保存于 `internal/version/version.txt`，后端通过嵌入文件返回版本。前端 Vite 默认读取 `frontend/package.json`，可由构建环境 `APP_VERSION` 覆盖，并由版本生成构建指纹。维护版本时同时对齐后端文件、前端清单、锁文件根版本、README 徽标和安装版本，不修改依赖自身版本。

前端读取后端版本后会比较构建版本；发现不一致时，每个服务端版本最多尝试一次页面刷新，仍不一致则给出版本不匹配提示。GitHub／GitLab 的发布版本与发行测试期望值同样读取后端版本文件。

源码依据：[版本接口与服务端缓存](internal/version/update.go)、[前端加载和状态轮询](frontend/src/core/useNodeManager.js)、[构建元数据](frontend/vite.config.js)、[前后端版本比较](frontend/src/core/version.js)、[版本一致性检查](frontend/scripts/version-consistency-regression.mjs)。

## 节点列表与运行时重启

`SBPM_NODES_VIRTUAL_THRESHOLD` 决定列表首批显示和每次滚动追加的行数，默认 `120`。正整数可按节点数量和设备性能调整；`0` 表示全量展开；负数、空值或格式无效时使用默认值。节点不足一批时直接显示全部，不显示加载提示。

管理进程把运行时环境值写入页面的 `sbpm-nodes-virtual-threshold` 元数据，覆盖前端构建时的值；Vite 开发服务也读取同名变量。修改后重启管理进程，Compose 部署需重建容器，再刷新浏览器页面。

弹窗只使用居中 Modal，窗口或屏幕方向改变不更换容器、不丢失草稿。批量导入进行中仍禁止 Esc 和关闭按钮取消弹窗。排序只支持纵向列表，保留鼠标、触屏和键盘操作。

`POST /api/runtime/restart` 与节点修改共用互斥锁，只读取一次节点列表和一次运行时设置，不开启数据库事务、不捕获数据修改快照，也不写回数据库。生成配置和内核校验通过后才替换运行时；启动或保存可用配置失败时，内核事务恢复之前的配置和进程。启动流程与 API 共用模型读取函数，只有启动流程保留数据库读取失败时启用已有配置的容错逻辑。

批量导入的展开阶段保留分享链接的协议、名称和配置；API 直接使用该结果，原链接仅用于导入明细，不再次解析。

源码依据：[共用数据库读取](backend/models/proxy_queries.go)、[只读重启](backend/api/node_mutation.go)、[内核事务](backend/services/runtime_applier.go)、[列表阈值](frontend/src/core/nodeList.js)、[分批渲染](frontend/src/shared/hooks.js)、[弹窗](frontend/src/shared/Overlays.jsx)。

## IP 检测与协议模型

- `CheckProxyIPContext` 的地址参数只能是无认证、无协议前缀的 `host:port`；IPv6 使用方括号包围地址。TCP 就绪检查直接使用该地址，因此不能传 `user:pass@host:port`。认证通过独立参数传递。`direct` 节点同样通过自身入站检测，节点功能与取消支持均保留。
- VLESS／VMess 的应用层 `network` 兼容字段接受普通 TCP、WS、HTTP／H2、QUIC、gRPC、HTTPUpgrade。普通 TCP 通过省略 sing-box 的 `transport` 表示，不生成 `type=tcp` 的原生传输对象；KCP／mKCP 会被生成器拒绝。
- `HeaderType`、`Seed` 等历史字段保留用于兼容读取，不表示内核支持相应扩展。QUIC 扩展头和非空 seed 不属于固定内核能力，详细协议边界见 README。
- WireGuard 配置生成到顶层 `endpoints`，不是 `outbounds`；节点和前置代理都采用这一结构。应用层历史字段在生成时转换为 endpoint 原生字段。

源码依据：[IP 检测](backend/services/ipcheck.go)、[协议模型](backend/models/proxy.go)、[传输校验及 WireGuard 生成](backend/services/singbox.go)。
