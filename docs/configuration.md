# 配置、数据库与运维指南

本文档提供 SingBox Proxy Manager（`1.7.27`）完整的环境变量参考、多数据库配置、数据持久化、安全加固与故障排查指南。

---

## 1. 环境变量加载规则

- **独立二进制部署**：启动时自动查找 `.env` 文件，生效优先级从高到低为：**进程已有环境变量 > 可执行文件同目录 `.env` > 当前工作目录 `.env`**。
- **Docker Compose 部署**：通过 `env_file: [.env]` 注入同目录 `.env`，同时在 `environment` 区块映射常用默认值；未在 `docker-compose.yml` 显式列出的受支持运行变量也可直接在 `.env` 中配置生效。
- **重要提示**：修改 `CONFIG_DIR` 不会自动改变 Compose 的宿主机卷挂载目标（默认 `./config:/app/config`），若修改容器内 `CONFIG_DIR`，必须同步调整挂载路径。

---

## 2. 完整环境变量参考

> 适用范围标识：`R` = 容器与二进制运行时；`D` = 仅外置内核开发构建；`F` = 前端构建期；`C` = Docker Compose / 镜像构建期。

### 2.1 基础服务与 HTTP 保护

| 变量名 | 默认值 | 格式 / 合法范围 | 适用 | 说明 |
| --- | --- | --- | --- | --- |
| `PORT` | `30000` | 端口号字符串 | R | 管理后台 HTTP 监听端口 |
| `BIND_ADDRESS` | `0.0.0.0` | IPv4 / IPv6 地址（自动剥离 IPv6 方括号） | R | **仅控制管理后台 HTTP 监听地址**；节点入站始终写为 `::` |
| `CONFIG_DIR` | 二进制 `./config`<br/>容器 `/app/config` | 目录路径 | R | 存放 SQLite 数据库、`config.json`、`last-good`、内置内核与日志文件 |
| `TZ` | `UTC+8` | IANA 时区或 `UTC/GMT` 偏移 | R | 全局时区；解析失败时回退 `Asia/Shanghai` 或固定 `UTC+08` |
| `ADMIN_PASSWORD` | 无默认密码 | 非空，上限 `72` UTF-8 字节 | R | 新库首次启动必填；老库已有有效密码时可留空；老库已有有效密码但尚未记录环境指纹时，首次设置仅登记指纹、不会覆盖旧密码或吊销会话；此后更改为新非空值并重启才会覆盖密码并吊销全部会话 |
| `TRUSTED_PROXIES` | 空（不信任任何代理） | 逗号分隔 IP / CIDR | R | 反向代理可信列表（如 `127.0.0.1,10.0.0.0/8`），供 Gin 获取真实客户端 IP 用于登录限流 |
| `CORS_ALLOWED_ORIGINS` | 空（关闭跨域） | 逗号分隔精确 Origin | R | 跨域白名单（忽略 `*`）；允许方法为 `GET/POST/PUT/DELETE/OPTIONS`（不含 `PATCH`） |
| `API_MAX_BODY_BYTES` | `1048576`（1 MiB） | 整数；`<= 0` 关闭限制 | R | API 请求正文最大字节数 |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | Go duration | R | HTTP 读取请求头超时 |
| `HTTP_READ_TIMEOUT` | `15s` | Go duration | R | HTTP 完整请求读取超时 |
| `HTTP_WRITE_TIMEOUT` | `2m` | Go duration | R | HTTP 响应写入超时 |
| `HTTP_IDLE_TIMEOUT` | `60s` | Go duration | R | HTTP Keep-Alive 空闲连接超时 |
| `HTTP_MAX_HEADER_BYTES` | `1048576`（1 MiB） | 整数 | R | HTTP 请求头最大字节数 |

### 2.2 会话认证与登录防爆破

> 限流类参数遇到非法或非正值时回退代码默认值，**不支持填 `0` 关闭登录限流**。

| 变量名 | 默认值 | 合法范围 | 适用 | 说明 |
| --- | --- | --- | --- | --- |
| `ADMIN_SESSION_TTL_HOURS` | `168`（7 天） | `1–8760` 整数小时 | R | 管理会话绝对有效期（无滑动续期） |
| `LOGIN_RATE_LIMIT_WINDOW_SECONDS` | `60` | 正整数（秒） | R | 单 IP 登录失败统计窗口 |
| `LOGIN_RATE_LIMIT_MAX_ATTEMPTS` | `10` | 正整数 | R | 窗口内允许的最大失败/进行中次数 |
| `LOGIN_RATE_LIMIT_BLOCK_SECONDS` | `600` | 正整数（秒） | R | 触发限流后的临时封禁时长 |
| `LOGIN_MAX_CONCURRENT_CHECKS` | `2` | 正整数 | R | 全局同时进行的 bcrypt 密码校验上限，超限立即返回 `429` |

### 2.3 内核控制、并发超时与日志自愈

| 变量名 | 默认值 | 合法范围 | 适用 | 说明 |
| --- | --- | --- | --- | --- |
| `SINGBOX_BINARY` | 空 | 可执行文件路径/命令 | D | 外置 `sing-box` 路径；**统一发行版忽略此变量** |
| `SINGBOX_ENV_ALLOWLIST` | 空 | 逗号分隔变量名 | R | 额外允许 `sing-box` 继承的非敏感环境变量名（密码/Token/DSN 等敏感名称会被强制拦截） |
| `SBPM_SINGBOX_CHECK_CONCURRENCY` | `4` | `1–16` | R | `sing-box check` 并发进程槽位数 |
| `SBPM_SINGBOX_CHECK_TIMEOUT` | `10s` | `100ms–2m` | R | 单次 `sing-box check` 硬超时（含等待槽位时间） |
| `SBPM_UPSTREAM_CHECK_CONCURRENCY` | `3` | `1–16` | R | 前置代理独立测 IP 的临时内核并发上限 |
| `SBPM_UPSTREAM_CHECK_TIMEOUT` | `30s` | `1s–5m` | R | 前置代理测 IP 总时限（含等待槽位时间） |
| `SBPM_BATCH_IMPORT_VALIDATION_TIMEOUT` | `45s` | `100ms–2m` | R | 批量导入从候选预检到最终提交的校验总时限（不含订阅抓取展开耗时） |
| `SBPM_SINGBOX_STARTUP_GRACE` | `1s` | `10ms–30s` | R | `sing-box` 启动后判定存活稳定的观察窗口 |
| `SBPM_SINGBOX_LOG_OUTPUT` | 二进制 `file`<br/>Docker `stdout` | `file` / `stdout` / `both` | R | `sing-box` 日志输出目标；设为 `file` 或 `both` 时写入 `<CONFIG_DIR>/singbox.log`（权限 `0600`） |
| `SBPM_SINGBOX_LOG_MAX_BYTES` | `10485760`（10 MiB） | 正整数 | R | 单个 `singbox.log` 轮转阈值 |
| `SBPM_SINGBOX_LOG_BACKUPS` | `3` | `1–100` | R | 历史轮转日志保留份数 |
| `SBPM_SINGBOX_RECOVERY_MAX_ATTEMPTS` | `5` | `1–100` | R | 异常退出后的最大自动恢复重试次数 |
| `SBPM_SINGBOX_RECOVERY_BASE_DELAY` | `1s` | `10ms–1m` | R | 指数退避恢复基准延迟 |
| `SBPM_SINGBOX_RECOVERY_MAX_DELAY` | `30s` | `base_delay–10m` | R | 指数退避恢复最大延迟上限 |
| `SBPM_SINGBOX_RECOVERY_STABLE_WINDOW` | `1m` | `10ms–1h` | R | 稳定运行判定窗口：下一次非预期退出时，若此前连续存活时长达到该值则清零既往恢复计数 |
| `SBPM_BATCH_CHECK_IP_CONCURRENCY` | `3` | 正整数 | R / F | 注入前端的批量测 IP 共享并发额度（非后端全局 API 限流） |
| `SBPM_NODES_VIRTUAL_THRESHOLD` | `120` | 非负整数 | R / F | 前端节点列表首批及每次滚动追加渲染行数；设为 `0` 表示一次全量渲染 |
| `SBPM_IPCHECK_URLS` | 内置 7 个公共服务 | 逗号或空白分隔 HTTP(S) URL | R | 自定义出口 IP 检测端点列表；默认并发竞速查询 `ip2location / ip.sb / ipwho.is / ipapi.co / myip / ipify / ip-api` |

### 2.4 数据库连接与单实例租约

| 变量名 | 默认值 | 合法范围 / 说明 | 适用 |
| --- | --- | --- | --- |
| `DATABASE_URL` | 空 | 优先级 1：支持 `postgres://`、`postgresql://`、`mysql://` 或 MySQL 原生 DSN（不接受 `sqlite`/`libsql`） | R |
| `POSTGRES_DATABASE_URL` | 空 | 优先级 2：远程数据库 URL 别名 | R |
| `POSTGRES_URL` | 空 | 优先级 3：远程数据库 URL 别名 | R |
| `PGSQL_DATABASE_URL` | 空 | 优先级 4：远程数据库 URL 别名 | R |
| `PGSQL` | 空 | 优先级 5：短变量别名 | R |
| `MYSQL_DATABASE_URL` | 空 | 优先级 6：远程数据库 URL 别名 | R |
| `MYSQL` | 空 | 优先级 7：短变量别名 | R |
| `MYSQL_DATABASE_NAME` | 空 | 覆盖 MySQL/TiDB URL 或 DSN 中的库名；原库名为空或系统库（`sys`/`mysql`/`information_schema`/`performance_schema`）时默认回退 `test` | R |
| `TURSO_DATABASE_URL` | 空 | 仅在以上 7 项均为空时生效；需与 `TURSO_AUTH_TOKEN` 成对提供 | R |
| `TURSO_AUTH_TOKEN` | 空 | Turso / libSQL 认证 Token | R |
| `DB_MAX_OPEN_CONNS` | `10` | 非负整数，仅对 PostgreSQL / MySQL 生效；`0` 表示不限制最大打开连接数 | R |
| `DB_MAX_IDLE_CONNS` | `5` | 非负整数，仅对 PostgreSQL / MySQL 生效；`0` 表示不保留空闲连接 | R |
| `DB_CONN_MAX_LIFETIME` | MySQL `3m`<br/>PostgreSQL `30m` | 非负 Go duration，仅对 PostgreSQL / MySQL 生效；`0` 表示不限寿命 | R |
| `SBPM_INSTANCE_LEASE_DURATION` | `30s` | `5s–10m`；单实例数据库租约有效期 | R |
| `SBPM_INSTANCE_LEASE_HEARTBEAT` | 二进制 `lease/3`<br/>Compose `10s` | `1s–lease/2`；单实例租约心跳续期周期 | R |

### 2.5 版本更新提示

> 版本检查在访问 `GET /api/version`（如加载或刷新管理面板）时**按需触发**，不存在后台定时轮询任务；`TTL` 是服务端结果缓存有效期（含成功与失败结果）。面板每 5 秒轮询的是 `/api/runtime/status`，不会频繁触发 GitHub 请求。

| 变量名 | 默认值 | 合法范围 | 适用 | 说明 |
| --- | --- | --- | --- | --- |
| `SBPM_UPDATE_CHECK_DISABLED` | `false` | 仅 `1`/`true`/`yes`/`on` 识别为开启禁用 | R | 关闭 GitHub Release 新版本提示 |
| `SBPM_UPDATE_CHECK_REPO` | `cheluen/singbox-proxy-manager` | `owner/repo` 或 GitHub URL | R | 检查最新 Release 的目标仓库，格式非法时回退默认值 |
| `SBPM_UPDATE_CHECK_TTL` | `6h` | 正 Go duration | R | 更新查询服务端缓存时长 |
| `SBPM_UPDATE_CHECK_TIMEOUT` | `5s` | 正 Go duration | R | 请求 GitHub API 的网络超时 |
| `GITHUB_TOKEN` | 空 | GitHub Token | R | 可选，用于提高 GitHub API 速率配额 |

### 2.6 Compose 与构建期变量

| 变量名 | 默认值 | 适用 | 说明 |
| --- | --- | --- | --- |
| `COMPOSE_PROFILES` | `.env.example` 为 `ghcr` | C | `ghcr` 拉取预构建镜像；`local` 使用本地源码构建 |
| `GHCR_IMAGE` | `ghcr.io/cheluen/singbox-proxy-manager:latest` | C | `ghcr` profile 使用的镜像地址 |
| `LOCAL_IMAGE` | `singbox-proxy-manager:local` | C | `local` profile 构建的本地镜像标签 |
| `CONFIG_VOLUME_HOST` | `./config` | C | Compose 挂载到容器 `/app/config` 的宿主机目录 |
| `GO_MODULE_PROXY` / `GOPROXY` | `https://proxy.golang.org,direct` | C | Docker 本地构建 Go 模块代理 |
| `GO_SUM_DB` / `GOSUMDB` | `sum.golang.org` | C | Docker 本地构建 Go 校验和数据库 |
| `APP_VERSION` | `frontend/package.json` 版本 | F | 前端构建版本号覆盖（不修改后端 `version.txt`） |
| `VITE_API_TARGET` | `http://127.0.0.1:30000` | F | 前端 Vite 开发服务器 API 代理目标 |
| `VITE_BATCH_CHECK_IP_CONCURRENCY` | 空 | F | 前端构建期并发备用值，优先级低于服务端注入的 `SBPM_BATCH_CHECK_IP_CONCURRENCY` |

---

## 3. 数据库选型与单实例租约

### 3.1 数据库优先级与方言特性

启动时按 `DATABASE_URL > POSTGRES_DATABASE_URL > POSTGRES_URL > PGSQL_DATABASE_URL > PGSQL > MYSQL_DATABASE_URL > MYSQL` 选取**第一个非空 URL**，并依据 URL 协议（而非变量名）判定数据库类型：

- 若该 URL 解析或连接失败，服务会**直接报错退出**，不会静默降级至本地 SQLite。
- 上述 7 项全部为空时，若 `TURSO_DATABASE_URL` 与 `TURSO_AUTH_TOKEN` 均非空则连接 Turso/libSQL；仅配置其中一项时会记录提示并降级为本地 SQLite。
- **SQLite**（默认）：使用纯 Go `modernc.org/sqlite` 写入 `<CONFIG_DIR>/proxy.db`，开启 WAL、外键约束与 `10s` `busy_timeout`，固定 1 打开 / 1 空闲连接。
- **PostgreSQL**：支持 `postgres://` 与 `postgresql://`（基于 `pgx/v5`），内部自动转换 SQL 占位符（`?` → `$1`）。
- **MySQL / TiDB**：支持 `mysql://` 与标准 `@tcp/@unix` DSN；未显式配置时默认开启 `parseTime`（不覆盖显式 `parseTime=false`），强制开启 `clientFoundRows`；连接公网主机在未显式指定 `tls` 参数时默认启用 TLS（显式 `tls=false` 或空 `tls=` 均会被保留），节点/配置字段自动使用 `LONGTEXT NOT NULL`。
- **Turso / libSQL**：固定 1 打开 / 0 空闲 / 1ns 连接寿命以规避驱动会话复用异常。

数据库启动后自动创建并增量补齐四张应用表（无独立 `schema_version` 表）：
- `proxy_nodes`：节点出站配置、入站端口/认证/复用、前置代理定义、检测结果与排序。
- `settings`：全局单例配置、管理员密码与环境指纹、`auth_generation`、端口策略与全局前置代理。
- `admin_sessions`：登录会话 SHA-256 哈希、密码代次、有效期、登录 IP 与 User-Agent。
- `manager_instance_lock`：单实例数据库租约锁。

### 3.2 单实例约束（重要）

**接入远程数据库（PostgreSQL / MySQL / Turso）用于异地持久化与备份，不等于支持多副本同时运行。**

- 管理进程启动时会以随机 16 字节 `owner_id` 争抢 `manager_instance_lock` 单例租约，并按心跳周期持续续租。
- 第二个实例尝试连接同一数据库且租约尚未过期时，会直接拒绝启动；运行中实例若持续续租失败或丢失所有权，会主动终止进程，防止双写破坏端口映射与 `sing-box` 一致性。
- 正常退出时自动释放租约；异常强杀后等待不超过 `SBPM_INSTANCE_LEASE_DURATION`（默认 `30s`）新实例即可接管。注意租约时间戳依赖宿主机系统时钟，跨机切换迁移时请确保宿主机时间同步（NTP）。

---

## 4. 数据持久化与安全加固

### 4.1 `<CONFIG_DIR>` 目录结构

```text
config/
├── proxy.db                  # 本地 SQLite 数据库（仅 SQLite 模式生成）
├── config.json               # 当前生效的 sing-box 运行配置（权限 0600）
├── config.json.last-good     # 最近一次成功启动的配置备份（权限 0600）
├── singbox.log               # sing-box 文件日志（仅 LOG_OUTPUT=file/both 时生成，权限 0600）
└── kernels/
    └── sing-box-<sha256>     # 统一发行版自动释放并校验的内核（权限 0700）
```

> **数据敏感性提示**：除管理员密码、环境指纹（bcrypt）与会话 Token（SHA-256）外，数据库与 `config.json` 中的节点出站密钥、前置代理凭据及入站账密均以可读取形式存储。请务必妥善管控宿主机文件权限（`chmod 600 .env`、`chmod 700 config`）并避免将其提交到任何仓库或公开备份。

### 4.2 生产环境安全建议

1. **反向代理与 HTTPS**：应用自身仅监听原生 HTTP，未内置 TLS 证书配置。公网访问管理面板强烈建议将 `BIND_ADDRESS=127.0.0.1`，前置 Nginx / Caddy / Traefik 提供 HTTPS，并正确设置 `TRUSTED_PROXIES=127.0.0.1` 使登录限流精准识别真实客户端 IP。
2. **防火墙与入站认证**：`sing-box` 节点入站固定监听 `::`（默认从 `30001` 递增）。公网服务器请使用防火墙（ufw / firewalld / 安全组）仅放行可信来源 IP，并保持各节点的**入站认证开启**（禁止对公网开放无认证代理）。
3. **定期轮换强密码**：首次启动使用 `.env` 设置高强度 `ADMIN_PASSWORD`，之后可直接在面板「系统设置」修改密码（至少 8 位，修改后自动吊销所有旧会话）。

---

## 5. 升级、模式切换与故障排查

### 5.1 版本升级与模式切换

- **Docker Compose（GHCR 预构建镜像）**：
  ```bash
  docker compose --profile ghcr pull
  docker compose --profile ghcr up -d
  ```
- **Docker Compose（本地源码构建）**：
  ```bash
  git pull
  docker compose --profile local up -d --build
  ```
- **GHCR 与 Local 模式切换**（两者共用容器名 `sb-proxy`，切勿同时启动，也**不要使用 `down -v`** 常规升级）：
  ```bash
  docker compose --profile ghcr down
  docker compose --profile local up -d --build
  ```
- **独立单文件二进制升级**：
  1. 停止当前管理进程（如 `systemctl stop sb-proxy` 或 `kill -TERM <pid>`）。
  2. 下载新版本二进制并校验 `checksums.txt`，替换原可执行文件。
  3. 保持原工作目录、`.env`、`CONFIG_DIR` 与数据库配置不变，重新启动即可自动释放新版内嵌内核与前端。

GitHub Release 提供 `LICENSE`、`THIRD_PARTY_NOTICES.md` 和 `SING-BOX-GPL-3.0.txt` 附件。容器内可从 `/app/LICENSE`、`/app/THIRD_PARTY_NOTICES.md` 和 `/app/licenses/SING-BOX-GPL-3.0.txt` 读取相同内容。[第三方声明](../THIRD_PARTY_NOTICES.md)

### 5.2 常见故障排查

| 现象 | 排查要点 |
| --- | --- |
| 启动报错：新库缺少管理员密码 | 检查 `.env` 中 `ADMIN_PASSWORD` 是否非空且不超过 `72` 字节；独立二进制确认 `.env` 位于可执行文件同目录或当前工作目录。 |
| 启动报错：未能获取数据库实例租约 | 确认同一数据库没有另一个正在运行的管理实例；若前一个实例异常崩溃，等待 `30s`（`SBPM_INSTANCE_LEASE_DURATION`）租约过期后重试。 |
| 顶部出现「sing-box 运行异常」降级横幅 | 查看容器日志（`docker compose --profile ghcr logs --tail=100`）或 `<CONFIG_DIR>/singbox.log`；核查端口冲突或节点参数异常，修正后点击横幅中的「重启内核」。 |
| 代理端口无法连接 | 1) 检查节点是否处于「启用」状态；2) 宿主机使用 `ss -tulpn` 确认对应入站端口已监听；3) 检查云安全组与宿主机防火墙规则；4) 如开启了入站认证，确认账密匹配。 |
| 出口 IP 检测超时或报 `429` | 1) 确认节点自身出站链路连通；2) 单个检测默认先走 HTTP 入站失败再回退 SOCKS5，内置 7 个公共检测接口在高并发时可能触发上游限流，可调低 `SBPM_BATCH_CHECK_IP_CONCURRENCY` 或通过 `SBPM_IPCHECK_URLS` 指定自建检测接口。 |
| 远程订阅链接导入失败 | 本程序强制拒绝指向内网/回环/保留 IP 的订阅地址，并拦截返回 HTML 登录页/错误页的链接；若源链接本身是 HTTP 代理而非订阅，请在批量导入弹窗将来源类型切换为「HTTP 代理」或在 URL 参数中附带 `proxy=1`。 |
