<a id="top"></a>

# SingBox 代理转发管理系统-纯为了学习相关技术自用

<div align="center">

<img src="./logo.svg" alt="SingBox Proxy Manager Logo" width="96" />

![Version](https://img.shields.io/badge/version-1.7.23-blue.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)
![SingBox](https://img.shields.io/badge/sing--box-1.12.12-orange.svg)

一个基于 sing-box 的代理节点管理和转发系统，提供简洁易用的 Web 界面。

[功能特性](#-功能特性) • [快速开始](#-快速开始) • [二进制部署](#-二进制部署releases) • [使用说明](#-使用说明) • [配置说明](#-配置说明)

</div>

---

## 📋 功能特性

### 核心功能
- 🚀 **单进程架构** - 使用单个 sing-box 进程管理所有节点，资源占用低
- 🔐 **认证保护** - 支持为每个代理节点设置独立的用户名密码
- 🌐 **多协议支持** - 支持 Direct、VLESS、VMess、Trojan、Hysteria2、TUIC、Shadowsocks、AnyTLS、WireGuard，以及 SOCKS5/SOCKS5H/HTTP 上游代理
- 🔄 **双模式代理** - 单端口同时支持 HTTP 和 SOCKS5 协议
- 📊 **IP 检测** - 实时检测节点 IP、地理位置和延迟
- ⬆️ **版本更新提示** - GitHub Release 有新版本时，在面板版本号旁显示向上箭头

### 管理功能
- ✨ **暮色管理面板** - 单一暗色指挥台、高密度节点列表、行内详情和响应式居中弹窗
- 📥 **批量操作** - 支持批量导入、删除、设置认证、检测 IP
- 📝 **备注/导出/替换** - 给节点加备注；支持导出可表达的分享链接；支持用新分享链接直接替换节点
- 🔧 **灵活配置** - 自定义入站端口，自动或手动分配
- 🌍 **多语言** - 支持中文/英文界面切换
- 📱 **排序与端口保留** - 支持鼠标和键盘排序，保留固定端口和全局保留端口策略
- ⌨️ **键盘操作** - `Ctrl/⌘ K` 打开命令面板，`j/k` 移动、`Enter` 展开、`x` 选择、`e` 编辑、`t` 检测

### 前端集成
- 管理面板直接使用同源 `/api` 真实接口，不包含演示数据、模拟请求适配器或演示密码。
- 升级保留原有登录会话；退出登录、会话失效和修改管理员密码会清除本地认证。
- 筛选后批量操作仅作用于可见节点；单条和批量 IP 检测共享并发预算，支持中止。
- 协议编辑保留原始扩展配置，支持原生 TLS、WireGuard 多 Peer 和历史保留字节格式。
- 前端、后端、发行文件和构建指纹对齐；窗口尺寸变化不会清空正在编辑的表单。
- 内置静态服务按 `Accept-Encoding` 权重协商预压缩 `.br`／`.gz`，同权重优先 Brotli，选不到压缩表示时原版兜底；存在压缩变体时设置 `Vary: Accept-Encoding`，404 不使用一年期 `immutable` 缓存。
- HEAD 忽略 `Range` 并返回完整长度；GET 范围请求直接回退原文，由标准库处理 206／416，不在压缩表示上截取范围。
- 首页和 SPA 页面使用启动时注入运行参数后的 HTML；若注入改写内容，仅一次性生成并缓存 gzip，不返回过期压缩包。原版 ETag 保持兼容，压缩强 ETag 在启动时依据实际压缩字节计算；`If-Match` 优先于 `If-None-Match`，304／412 不残留错误的压缩长度。

前端开发工具使用 Node.js 22。持续验证包含核心逻辑、对比度与词条审计、浏览器交互以及真实 sing-box 全链路回归。

---

## 🚀 快速开始

### 环境要求
- Linux、Docker 和 Docker Compose V2
- 网络模式：host（用于多端口监听）；以下部署命令统一使用 `/opt/singbox-proxy-manager`

### 一键部署

```bash
# 1. 克隆项目
git clone https://github.com/cheluen/singbox-proxy-manager.git /opt/singbox-proxy-manager
cd /opt/singbox-proxy-manager

# 2. 生成部署环境变量文件（所有部署方式建议参考这个变量清单）
cp /opt/singbox-proxy-manager/.env.example /opt/singbox-proxy-manager/.env
chmod 600 /opt/singbox-proxy-manager/.env

# 3. 修改 .env；新数据库必须设置 ADMIN_PASSWORD，监听回环地址也不能留空
# 镜像来源切换：
#   COMPOSE_PROFILES=ghcr  -> 使用 GHCR 预构建镜像（默认）
#   COMPOSE_PROFILES=local -> 使用本地源码构建镜像
nano /opt/singbox-proxy-manager/.env

# 4. 启动服务
docker compose up -d

# 5. 查看日志
docker compose logs -f
```

两个 Compose 服务都会读取与 `docker-compose.yml` 同目录的 `.env`，包括配置校验、上游检测及批量导入的五个超时／并发参数。默认 `ghcr` 模式直接使用已发布镜像；只有 `local` 模式从本地源码构建。不要同时启用两种模式，它们共用容器名 `sb-proxy`。

服务启动后访问：`http://您的服务器IP:30000`

---

## 📦 二进制部署（Releases）

每次 `main` 分支更新后，GitHub Actions 会自动构建并发布多平台二进制到对应版本 Release（标签 `vX.Y.Z`）：

- Linux：`amd64` / `arm64`
- FreeBSD：`amd64` / `arm64`
- macOS：`amd64` / `arm64`

> 每个平台只需一个可执行文件，已内置前端和 sing-box，不需要额外安装 Go、Node、Docker 或内核。

程序自动读取**可执行文件同目录**和**当前工作目录**的 `.env`，已有进程环境变量优先。首次使用新数据库必须设置非空 `ADMIN_PASSWORD`，不受监听地址影响；未设置 `TZ` 时默认使用 `Asia/Shanghai`。如果复用 `.env.example`，请将容器内的 `CONFIG_DIR=/app/config` 改为二进制实际使用的数据目录。

```bash
# 下载适合平台的单文件（示例：Linux amd64）
mkdir -p /opt/singbox-proxy-manager
cd /opt/singbox-proxy-manager
APP_VERSION=1.7.23
curl -fL -o /opt/singbox-proxy-manager/singbox-proxy-manager "https://github.com/cheluen/singbox-proxy-manager/releases/download/v${APP_VERSION}/singbox-proxy-manager-linux-amd64"
chmod +x /opt/singbox-proxy-manager/singbox-proxy-manager
# 首次启动设置自己的管理员密码；之后可在管理面板修改
read -rs -p '管理员密码: ' ADMIN_PASSWORD; echo
export ADMIN_PASSWORD
/opt/singbox-proxy-manager/singbox-proxy-manager
```

### 单文件升级与数据目录

- 停止旧管理程序，替换该可执行文件，再用原来的工作目录、环境变量和启动方式启动即可。
- **数据库位置不变**：默认仍为当前工作目录下的 `config/proxy.db`；指定 `CONFIG_DIR` 时仍使用该目录。不迁移、复制或重建数据库，不更改远程数据库选择规则。
- 保留原 `.env`、`CONFIG_DIR` 和数据库连接配置；改变工作目录且使用默认相对路径会指向另一份数据库，这与旧版行为相同。
- 内核自动释放到 `CONFIG_DIR/kernels/sing-box-<SHA256>`，每次管理程序启动都校验内容和权限，损坏时原子替换；目录和内核权限为 `0700`。目录必须可写，所在文件系统必须允许执行。
- 新发行文件按内置内核内容选择路径，不使用 `SINGBOX_BINARY`、`PATH`、同目录或配置目录里的旧内核；旧内核文件不会被运行或覆盖。内置前端也不会被遗留的 `frontend/dist` 覆盖。
- 管理程序和 sing-box 仍为独立进程。下载的单文件是分发形式，不是把两个项目代码合并。
- 普通开发构建（不使用 `bundled_kernel` 标签）仍支持 `SINGBOX_BINARY` 及原有外部内核查找顺序，便于开发测试。

---

## 安全重编译说明

- 后台和 sing-box 内核均固定使用 Go 1.26.11 编译，修复 CVE-2026-111943 和 CVE-2026-97054；两者编译后打包进一个发行文件，升级只替换管理程序。
- 后台另将 `golang.org/x/text` 最小升级至 0.41.0，修复扫描中发现的可达漏洞 GO-2026-6629；相关工具依赖按其最低要求同步。
- 当前前端锁文件使用 Axios 1.21.1、follow-redirects 1.16.3；依赖的实际安装版本以 `frontend/package-lock.json` 为准。
- 远程回归发现 Turso 并发写锁及驱动关闭远程流后的复用错误；限制为单连接，事务结束后不复用远程连接，并增加排队读写回归测试。
- sing-box 保留 1.12.12 版本，源码提交为 `54ed58499d7063136ed52dabf87d179d252425d0`，模块归档校验和固定在 `scripts/build-sing-box.sh`。
- 源码构建使用该脚本并保留原官方内核的八项功能标签，支持通过 `GOOS`、`GOARCH` 交叉编译；脚本参数必须为输出文件的绝对路径。
- 普通 Docker 构建通过 `scripts/build-release.sh` 生成统一发行文件；Release 镜像直接复用本次发布的单文件，不再下载上游旧的预编译包。
- CI 保留后台完整可达漏洞扫描，并对内核执行二进制扫描和阻断本次 CVE 回归。GitHub Actions 不再上传内核扫描报告，扫描失败或命中门禁时在作业日志输出错误摘要。保留旧版内核的历史第三方依赖告警不等于已修复，本次没有升级其功能版本或依赖。

---

## 📖 使用说明

### 1. 登录系统

- 默认端口：`30000`
- **不再提供默认密码**
  - 新数据库首次启动时，`ADMIN_PASSWORD` 必须为非空值，最长 72 字节；无论监听公网还是回环地址，都先持久化安全哈希，再开放管理端口
  - 已有数据库包含有效管理员密码哈希时，可以不设置 `ADMIN_PASSWORD`，登录密码以数据库为准
  - 老数据库已有有效密码、但尚未记录环境密码指纹时，第一次配置 `ADMIN_PASSWORD` 只记录指纹，不覆盖旧密码，也不撤销会话
  - 指纹已记录后，再更改非空 `ADMIN_PASSWORD` 并重启才会更新密码、撤销已有会话；环境值未变化时保留面板内修改的密码

### 2. 添加节点

#### 方式一：单个添加
1. 点击「添加节点」按钮
2. 粘贴分享链接并点击「解析」，或选择协议后手动填写服务器与协议配置
3. 默认由后端从「全局设置」的起始端口分配入站端口；开启「保留入站端口」后才可在新增表单指定端口，留空仍自动分配
4. 入站认证默认开启，但用户名和密码初始为空：自行填写或点击「生成」，也可关闭认证后保存

#### 方式二：批量导入
1. 点击「批量导入」
2. 选择输入类型，粘贴分享链接、订阅地址或 HTTP 代理列表
3. 系统解析并校验候选节点，为每个成功导入的节点自动生成独立入站用户名和密码；失败项目会返回明细

### 3. 使用代理

配置您的客户端（如浏览器、指纹浏览器等）：

```
代理类型：HTTP 或 SOCKS5
服务器：您的服务器IP
端口：30001、30002、30003...（对应各节点）
用户名：使用节点详情中的入站用户名（关闭认证时不填）
密码：使用节点详情中的入站密码（关闭认证时不填）
```

### 4. 管理节点

- **启用/禁用**：点击开关即可切换节点状态
- **检测 IP**：勾选节点后点击「批量测IP」
- **拖拽排序**：自动模式按顺序重分配未固定节点的端口；固定端口不变，开启全局「保留入站端口」时排序也不改端口
- **固定端口**：自动模式可给现有节点固定端口，后续排序或端口重分配会跳过这些节点
- **批量认证**：勾选多个节点，统一设置用户名密码
- **批量删除**：勾选节点后点击「批量删除」

---

## ⚙️ 配置说明

### 环境变量

Compose 使用项目根目录 `.env`（必须先从 `.env.example` 复制），通过 `env_file` 将内容传入容器；`environment` 中显式声明的变量优先采用宿主机环境，再采用 `.env`，最后采用默认值。二进制部署按其工作目录与可执行文件目录加载 `.env`。

下面列出常用配置；完整清单见 [`.env.example`](.env.example)，参数边界及版本检查实现见 [运维技术说明](OPERATIONS.md)。空白 `ADMIN_PASSWORD` 仅用于模板，新数据库启动前必须填写。

```bash
# 镜像来源（关键开关）
COMPOSE_PROFILES=ghcr            # ghcr 或 local
GHCR_IMAGE=ghcr.io/cheluen/singbox-proxy-manager:latest
LOCAL_IMAGE=singbox-proxy-manager:local
SINGBOX_VERSION=1.12.12
GO_MODULE_PROXY=https://proxy.golang.org,direct
GO_SUM_DB=sum.golang.org

# 运行配置
PORT=30000
BIND_ADDRESS=0.0.0.0
CONFIG_DIR=/app/config
CONFIG_VOLUME_HOST=./config
TZ=UTC+8
ADMIN_PASSWORD=
SINGBOX_BINARY=
SINGBOX_ENV_ALLOWLIST=                 # 额外传给 sing-box 的非敏感环境变量名，逗号分隔
SBPM_SINGBOX_CHECK_CONCURRENCY=4
SBPM_SINGBOX_CHECK_TIMEOUT=10s
SBPM_UPSTREAM_CHECK_CONCURRENCY=3
SBPM_UPSTREAM_CHECK_TIMEOUT=30s
SBPM_BATCH_IMPORT_VALIDATION_TIMEOUT=45s
SBPM_SINGBOX_LOG_OUTPUT=stdout       # stdout、file 或 both
SBPM_SINGBOX_LOG_MAX_BYTES=10485760
SBPM_SINGBOX_LOG_BACKUPS=3
SBPM_SINGBOX_RECOVERY_MAX_ATTEMPTS=5
SBPM_SINGBOX_RECOVERY_BASE_DELAY=1s
SBPM_SINGBOX_RECOVERY_MAX_DELAY=30s
SBPM_SINGBOX_RECOVERY_STABLE_WINDOW=1m

# 安全与稳定性
CORS_ALLOWED_ORIGINS=
LOGIN_RATE_LIMIT_WINDOW_SECONDS=60
LOGIN_RATE_LIMIT_MAX_ATTEMPTS=10
LOGIN_RATE_LIMIT_BLOCK_SECONDS=600
LOGIN_MAX_CONCURRENT_CHECKS=2
ADMIN_SESSION_TTL_HOURS=168
TRUSTED_PROXIES=
HTTP_READ_HEADER_TIMEOUT=5s
HTTP_READ_TIMEOUT=15s
HTTP_WRITE_TIMEOUT=2m
HTTP_IDLE_TIMEOUT=60s
HTTP_MAX_HEADER_BYTES=1048576
API_MAX_BODY_BYTES=1048576

# 数据库 URL 变量按以下顺序取第一个非空值，再考虑 Turso，最后使用本地 SQLite
DATABASE_URL=
POSTGRES_DATABASE_URL=
POSTGRES_URL=
PGSQL_DATABASE_URL=
PGSQL=
MYSQL_DATABASE_URL=
MYSQL=
MYSQL_DATABASE_NAME=                  # MySQL URL 指向 sys 等系统库时可覆盖实际业务库
DB_MAX_OPEN_CONNS=10
DB_MAX_IDLE_CONNS=5
DB_CONN_MAX_LIFETIME=
SBPM_INSTANCE_LEASE_DURATION=30s
SBPM_INSTANCE_LEASE_HEARTBEAT=10s
TURSO_DATABASE_URL=
TURSO_AUTH_TOKEN=
```

> `TZ` 会作为全局服务时区参与日志与排障时间显示；默认建议使用 `UTC+8`（自动映射到上海/香港时区）。

> `SINGBOX_BINARY` 仅用于不内置内核的开发构建；统一发行文件始终使用内置内核。

> `CONFIG_DIR` 是容器内的数据目录，`CONFIG_VOLUME_HOST` 是宿主机挂载目录；默认将宿主机项目下的 `config` 挂到容器的 `/app/config`。更改容器内目录时需同步调整挂载目标，不能只修改 `CONFIG_DIR`。

> sing-box 子进程默认只继承运行所需的最小环境。确需额外变量时可通过 `SINGBOX_ENV_ALLOWLIST` 指定名称，并将变量值写入 `.env`，Compose 的 `env_file` 会传入管理进程；密码、Token、数据库地址及私钥类名称即使列入也会被拒绝。

> 如本地源码构建遇到 Go 模块下载受限，可改为：`GO_MODULE_PROXY=https://goproxy.cn,direct`，必要时再配合 `GO_SUM_DB=off`。

> 数据库 URL 的优先级严格为：`DATABASE_URL` > `POSTGRES_DATABASE_URL` > `POSTGRES_URL` > `PGSQL_DATABASE_URL` > `PGSQL` > `MYSQL_DATABASE_URL` > `MYSQL`。取第一个非空值，由 URL 协议决定数据库类型；配置错误会启动失败，不会继续尝试低优先级变量。上述变量全部为空时，只有 `TURSO_DATABASE_URL` 与 `TURSO_AUTH_TOKEN` 均非空才使用 Turso，否则使用本地 SQLite。

> 管理进程通过数据库租约强制单实例运行。共享同一数据库启动第二个管理实例会直接失败；每个实例仍管理各自本地 sing-box 运行时，因此不支持横向扩容。

### 端口说明

- `30000`：管理界面端口（可修改）
- `30001`：新数据库默认的节点起始端口，在「全局设置」修改，允许范围为 `1024–65535`
- 新增节点从起始端口寻找可用端口；自动模式忽略新增请求中的手动端口，保留模式允许指定端口

### 数据持久化

默认使用本地 SQLite。按前述 Compose 部署路径，宿主机数据目录为 `/opt/singbox-proxy-manager/config`，容器内为 `/app/config`：
- `config.json`：sing-box 配置文件
- `proxy.db`：节点数据库
- `config.json.last-good`：最近一次成功启动的 sing-box 配置快照

如果启用了远程数据库（PostgreSQL/MySQL/Turso）：
- 节点/设置/会话数据会存到远程数据库（不再写入本地 `proxy.db`）
- 配置目录仍会保存 `config.json` 和 `config.json.last-good`

Compose 默认使用 `SBPM_SINGBOX_LOG_OUTPUT=stdout`，内核日志由容器 `json-file` 驱动按 `10m × 3` 轮转，不写入配置卷。独立二进制如设置为 `file` 或 `both`，则在配置目录生成 `singbox.log`，并按 `SBPM_SINGBOX_LOG_MAX_BYTES` 和 `SBPM_SINGBOX_LOG_BACKUPS` 保留轮转文件。

以下是 **Linux 默认 Compose + SQLite 部署**的离线备份与恢复步骤。修改过 `CONFIG_VOLUME_HOST` 时使用实际宿主机路径；远程数据库必须另外通过数据库提供方备份与恢复，复制配置目录不能还原远程节点数据。

备份时先停服务，避免复制正在写入的 SQLite 数据。备份保存在仓库外，不提交到 Git：

```bash
set -e
cd /opt/singbox-proxy-manager
install -d -m 700 /var/backups/singbox-proxy-manager
BACKUP_DIR="/var/backups/singbox-proxy-manager/$(date -u +%Y%m%dT%H%M%S%N)"
docker compose stop
cp -a /opt/singbox-proxy-manager/config "$BACKUP_DIR"
docker compose up -d
printf '备份目录：%s\n' "$BACKUP_DIR"
```

恢复时先把旧目录移到独立回滚位置，再复制备份到不存在的目标，既避免目录嵌套，也避免旧数据库的 WAL／SHM 文件残留：

```bash
set -e
cd /opt/singbox-proxy-manager
read -r -p '要恢复的备份目录（绝对路径）: ' BACKUP_DIR
BACKUP_DIR=$(realpath -e "$BACKUP_DIR")
case "$BACKUP_DIR" in
  /opt/singbox-proxy-manager/config|/opt/singbox-proxy-manager/config/*)
    printf '请选择配置目录外的独立备份\n' >&2
    exit 1
    ;;
esac
test -f "$BACKUP_DIR/proxy.db"
install -d -m 700 /var/backups/singbox-proxy-manager
ROLLBACK_DIR="/var/backups/singbox-proxy-manager/before-restore-$(date -u +%Y%m%dT%H%M%S%N)"
docker compose stop
if [ -e /opt/singbox-proxy-manager/config ]; then
  mv /opt/singbox-proxy-manager/config "$ROLLBACK_DIR"
fi
cp -a "$BACKUP_DIR" /opt/singbox-proxy-manager/config
docker compose up -d
```

保留原 `.env` 和数据库连接配置。确认恢复成功前不要删除回滚目录；备份与配置目录均包含节点凭据，应保持私有。

---

## 🔧 高级配置

### 修改默认端口

在 `/opt/singbox-proxy-manager/.env` 修改管理端口，然后执行 `docker compose up -d` 重建容器使环境生效，不需要修改 Compose 文件：

```dotenv
PORT=8080
```

### 使用环境变量管理密码

在 `.env` 填写非空 `ADMIN_PASSWORD` 后执行 `docker compose up -d`。`docker compose restart` 不会重新载入容器环境变量；老数据库首次接入环境密码的行为见「登录系统」，完整同步规则见 [运维技术说明](OPERATIONS.md#管理员密码同步)。

### 切换镜像来源（仅改 .env）

修改 `.env` 的 `COMPOSE_PROFILES` 后，先移除旧模式容器，再启动新模式；配置卷不会因此删除：

```dotenv
# 快速启动：使用 GHCR 预构建镜像
COMPOSE_PROFILES=ghcr

# 环境适配：使用本地源码构建镜像
COMPOSE_PROFILES=local
```

```bash
cd /opt/singbox-proxy-manager
docker compose --profile ghcr --profile local down
docker compose up -d
```

### GHCR 多架构镜像标签

CI 会自动发布以下镜像标签到 GHCR，并提供 `linux/amd64` + `linux/arm64` 多架构清单：

- `ghcr.io/cheluen/singbox-proxy-manager:latest`
- 版本标签：`v` 加项目版本，例如 `ghcr.io/cheluen/singbox-proxy-manager:v1.7.23`
- 提交标签：完整 Git 提交 SHA；用于固定某次发布构建

镜像构建会复用对应版本 Release 中的 Linux 二进制，确保二进制发布与容器发布支持的架构一致。

### 使用远程数据库（可选）

默认走本地 SQLite，配合配置目录挂载即可持久化。需要由外部数据库保存数据时，可以选择以下任一类型；数据库租约仍限制同一数据库只能运行一个管理实例。

#### PostgreSQL

将数据库服务提供的 `postgres://` 或 `postgresql://` 连接串写入 `.env` 的 `DATABASE_URL`。可使用 `POSTGRES_DATABASE_URL`、`POSTGRES_URL`、`PGSQL_DATABASE_URL` 或 `PGSQL` 作为兼容变量；按前述优先级选择，URL 中的特殊用户名和密码需百分号编码。

#### MySQL / TiDB Cloud

将 `mysql://` 连接串写入 `DATABASE_URL`、`MYSQL_DATABASE_URL` 或 `MYSQL`；后两者也接受 `go-sql-driver/mysql` 原生 DSN。未指定 `tls` 参数时，`localhost`、回环／私有 IP 及未指定地址（`0.0.0.0`、`::`）不自动启用 TLS，其他主机默认启用；可用连接串的 `tls` 参数覆盖。URL 指向 `sys`、`mysql`、`information_schema`、`performance_schema` 或未指定库名时使用 `test`，可通过 `MYSQL_DATABASE_NAME` 明确指定业务库。

#### Turso / libSQL

将 Turso 提供的 libSQL 连接地址和认证令牌分别写入 `TURSO_DATABASE_URL`、`TURSO_AUTH_TOKEN`；两项必须同时设置，且全部 PostgreSQL／MySQL URL 变量必须为空。

> 切换数据库不会自动迁移节点、设置和会话。先备份并迁移数据，再切换连接配置；不要用清空目标库代替数据迁移。

重启服务使配置生效：

```bash
cd /opt/singbox-proxy-manager
docker compose up -d
```

### 自定义入站端口范围

在「全局设置」修改起始端口 `start_port`，默认 `30001`。新增节点只读取此设置，不会修改起始端口。

默认自动模式中，新增请求即使提供端口也会重新分配；修改起始端口或关闭「保留入站端口」会按排序重分配未固定节点，固定节点始终保留原端口。开启「保留入站端口」后，排序不改端口，新增／编辑时允许手动设置；留空仍由系统分配。

---

## 🛠️ 技术栈

### 后端
- **语言**：Go 1.26.11
- **框架**：Gin
- **数据库**：SQLite（默认，本地存储）/ Turso（远程 libsql）/ PostgreSQL / MySQL（含 TiDB Cloud）
- **代理核心**：sing-box 1.12.12

### 前端
- **框架**：React 18
- **UI 库**：Ant Design 5
- **构建工具**：Vite 6
- **国际化**：i18next

---

## 📝 支持的协议

| 协议 | 入站 | 出站 | 特性 |
|------|------|------|------|
| Direct | - | ✅ | sing-box 原生直连出站，支持拨号器选项与可选目标覆盖 |
| HTTP | ✅ | ✅ | 入站/出站均支持认证（出站可选 TLS） |
| SOCKS5 | ✅ | ✅ | 入站/出站均支持认证 |
| SOCKS5H | ✅ | ✅ | 兼容 socks5h 分享链接；sing-box 配置映射为 `socks` + `version=5`，域名由 SOCKS 请求远端解析 |
| VLESS | - | ✅ | Reality（pbk/sid）、WS、gRPC、HTTP/Upgrade、ALPN、uTLS 指纹 |
| VMess | - | ✅ | WS、HTTP/2、gRPC、HTTPUpgrade、全套安全/填充参数 |
| Trojan | - | ✅ | TLS/utls 指纹、WS/GRPC/HTTP/HTTPUpgrade 传输、SNI/ALPN、可跳过校验 |
| Hysteria2 | - | ✅ | brutal/obfs/salamander、带宽/网络/跳点参数、TLS 指纹 |
| TUIC | - | ✅ | QUIC/UDP 模式、0-RTT、心跳、SNI/ALPN、RTT 优化 |
| Shadowsocks | - | ✅ | AEAD/2022、UDP over TCP、插件参数 |
| AnyTLS | - | ✅ | TLS 伪装、SNI/ALPN/指纹、会话管理（idle_session_*） |
| WireGuard | - | ✅ | sing-box 1.12 endpoint 配置、单/多 Peer、reserved 字节、拨号器选项 |

> **说明**：入站端口使用 mixed 模式，单端口同时支持 HTTP、SOCKS5 与 socks5h 客户端的远端 DNS 语义。

> **内核边界**：上表以项目固定的 sing-box `1.12.12` 原生配置为准。mKCP、XHTTP/SplitHTTP、Mekya、Reality SpiderX（`spx`）/ML-DSA 验证（`pqv`）、FinalMask（`fm`）、Xray 证书绑定（`pcs`/`vcn`）等扩展不属于该版本原生出站能力，导入时会明确拒绝，不会静默丢失参数。V2Ray QUIC 传输仅接受 sing-box 原生空 options 形式，并需要 TLS。

> **Shadowsocks 插件**：sing-box `1.12.12` 内置 `obfs-local` 与 `v2ray-plugin` 的 SIP003 实现，项目可直接生成对应 `plugin` / `plugin_opts`，无需在默认镜像中额外安装同名可执行文件。其他 SIP003 插件名称不属于该版本内置能力。

> **分享链接边界**：Direct 没有通用分享 URI，需在面板创建，不支持分享链接导出；WireGuard 可导入 `wireguard://` / `wg://`，但仅单 Peer 配置可无歧义导出为 URI，多 Peer 会完整保存并生成内核配置，但不导出为分享链接。

---

## 🔒 安全建议

1. **设置强管理员密码**
   - 新数据库首次启动前必须通过环境变量 `ADMIN_PASSWORD` 配置强密码，无论监听外部地址还是回环地址
   - 已初始化数据库可使用数据库中保存的有效密码；环境密码同步遵循「登录系统」中的指纹规则

2. **限制访问 IP**（可选）
   ```bash
   # 使用防火墙限制管理端口访问
   read -r -p '允许访问管理端口的来源 IP: ' ALLOWED_IP
   ufw allow from "$ALLOWED_IP" to any port 30000
   ```

3. **定期备份数据**
   - 使用「数据持久化」的停服备份步骤，或数据库提供方的一致性备份功能；不要直接复制正在运行的 SQLite 数据库
   - `.env`、数据库、配置快照和备份都包含凭据，保存在私有目录，不上传公开仓库

4. **使用 HTTPS**（生产环境）
   - 建议在前面部署 Nginx 反向代理
   - 配置 SSL 证书（Let's Encrypt）

---

## 🐛 故障排查

### 服务无法启动
```bash
# 查看日志
cd /opt/singbox-proxy-manager
docker compose logs -f

# 检查端口占用
ss -tlnp '( sport = :30000 )'

# 默认 GHCR 模式：拉取已发布镜像并启动，不编译本地源码
docker compose pull sb-proxy-ghcr
docker compose up -d --no-build sb-proxy-ghcr
```

新数据库优先检查 `.env` 中是否设置了非空 `ADMIN_PASSWORD`；`.env` 缺失时 Compose 会报错。使用 `local` 模式时，启动命令为 `docker compose up -d --build sb-proxy-local`。

### 内置内核释放或启动失败

1. 确认下载了匹配系统和 CPU 架构的管理程序。
2. 确认 `CONFIG_DIR` 可写，且该目录所在文件系统未使用 `noexec` 挂载。
3. `CONFIG_DIR/kernels` 必须是普通目录，不能是符号链接。损坏或缺失的内核会在重启管理程序时自动恢复。
4. 开发构建若提示 `sing-box binary not found`，使用 Go 1.26.11 运行 `/opt/singbox-proxy-manager/scripts/build-sing-box.sh /opt/singbox-proxy-manager/release-binaries/sing-box`，并将 `SINGBOX_BINARY` 设为该输出文件路径。正式发行不需要这一步。

### 从源码构建单文件

使用 Node.js 22 和 Go 1.26.11：

```bash
cd /opt/singbox-proxy-manager/frontend
npm ci
npm run build
cd /opt/singbox-proxy-manager
/opt/singbox-proxy-manager/scripts/build-release.sh /opt/singbox-proxy-manager/release-binaries/singbox-proxy-manager
```

可通过 `GOOS`、`GOARCH` 交叉编译。脚本先独立构建并验证内核，再内置内核及前端构建管理程序，最后校验管理程序编译器版本。相同工作区不允许并发构建不同平台。

### 代理无法连接
```bash
# 测试代理连通性
read -r -p '节点代理地址（host:port）: ' PROXY_ADDRESS
read -r -p '入站用户名（免认证时留空）: ' PROXY_USERNAME
if [ -n "$PROXY_USERNAME" ]; then
  # curl 会交互询问代理密码，不把密码写入 URL 或命令历史
  curl --proxy "http://$PROXY_ADDRESS" --proxy-user "$PROXY_USERNAME" --connect-timeout 5 --max-time 15 https://api.ipify.org
else
  curl --proxy "http://$PROXY_ADDRESS" --connect-timeout 5 --max-time 15 https://api.ipify.org
fi

# Compose 默认日志输出（包含管理进程与 sing-box）
docker compose logs -f

# 仅当 SBPM_SINGBOX_LOG_OUTPUT=file 或 both 时查看文件日志
docker exec sb-proxy tail -f /app/config/singbox.log

# host 网络模式下在宿主机检查节点监听端口（按实际节点端口调整）
ss -tlnp '( sport = :30001 )'
```

### 节点 IP 检测失败
- 检查服务器网络连接
- 确认节点配置正确
- 查看后端日志：`docker compose logs --tail=200 sb-proxy-ghcr`；`local` 模式使用服务名 `sb-proxy-local`。`sb-proxy` 是容器名，不是 Compose 服务名

---

## 📊 资源管理

- 常驻 sing-box 进程统一管理已启用节点；配置校验及上游 IP 检测会临时启动额外内核进程
- 并发数与超时可通过 `.env` 的五个校验／检测参数控制，默认值与允许范围见 [运维技术说明](OPERATIONS.md#并发与超时参数)
- 容器日志与可选文件日志均有轮转限制；配置目录还需为数据库、配置快照和释放的内核预留磁盘空间
- 实际资源消耗取决于协议、并发连接和流量，不以节点数量推算固定内存占用

---

## 🔄 更新升级

先按「数据持久化」备份，保留 `.env` 与配置卷，然后按实际部署方式升级。

### 默认 GHCR 镜像部署

```bash
cd /opt/singbox-proxy-manager
git pull --ff-only origin main
docker compose pull sb-proxy-ghcr
docker compose up -d --no-build sb-proxy-ghcr
```

`git pull` 只更新仓库中的文档、部署配置与源码；运行程序来自 `GHCR_IMAGE` 指定的已发布镜像。等待对应版本 CI 完成镜像发布后再拉取；固定版本标签时先修改 `.env` 的 `GHCR_IMAGE`。该服务没有 `build` 配置，`--build` 不会编译本地源码。

### local 源码构建部署

```bash
cd /opt/singbox-proxy-manager
git pull --ff-only origin main
docker compose up -d --build sb-proxy-local
```

仅在已使用 `COMPOSE_PROFILES=local` 的部署中执行；首次从 GHCR 切换需先按「切换镜像来源」移除旧模式容器。两种升级均保留原配置卷。独立二进制部署按「单文件升级与数据目录」操作。

---

## 📄 许可证

本项目采用 MIT 许可证。详见 [LICENSE](LICENSE) 文件。

---

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

---

## 📮 联系方式

如有问题或建议，欢迎通过以下方式联系：

- 提交 [GitHub Issue](https://github.com/cheluen/singbox-proxy-manager/issues)

---

<div align="center">

**[⬆ 回到顶部](#top)**

Made with ❤️ | [GitHub](https://github.com/cheluen/singbox-proxy-manager)

</div>
