# 本地开发、构建发布与测试指南

本文档以 `1.7.35` 为基线，说明工具链、本地联调、单文件与 Alpine 镜像发行、许可证分发和 CI 测试矩阵。

---

## 1. 开发环境要求

| 依赖项 | 版本要求 | 说明 |
| --- | --- | --- |
| Go | **精确 `1.26.11`** | `go.mod` 声明最低工具链版本，发行与内核构建脚本（`scripts/build-sing-box.sh` / `scripts/verify-go-build.sh`）强制校验精确等于 `go1.26.11`（`GOTOOLCHAIN=local`、`GOWORK=off`）并拒绝其他版本 |
| Node.js | `>= 22.12.0` | Docker 与 CI 使用 Node `22` |
| sing-box 源码版本 | **固定 `1.12.12`** | 由 `scripts/build-sing-box.sh` 从 Go 模块缓存按固定 `h1:` 校验和重编译 |
| Chromium / Chrome | 任意现代版本 | 仅运行前端及全栈浏览器回归测试时需要 |
| Python 3 | 3.x | 用于运行 Linux 单文件发行回归 `scripts/test-release.py`（另需 `curl`），以及 CI 发布计划、镜像标签计算、Release Notes 生成、GHCR 清理脚本与对应单元测试 |

---

## 2. 本地开发与联调

### 2.1 外置内核开发模式（前后端热更新联调）

开发模式下不嵌入内核与前端（不传 `bundled_kernel` 与 `frontend_dist` tag）。注意：**未携带 `frontend_dist` 编译的后端启动时仍强制检查磁盘上的 `frontend/dist/index.html`**，缺失会直接退出，因此新检出仓库需先执行一次前端构建再启动后端：

```bash
# 1. 首次先安装依赖并构建一份磁盘前端产物（生成 frontend/dist/index.html）
(
  cd frontend
  npm ci
  npm run build
)

# 2. 重编译一份本地开发用 sing-box 1.12.12 内核
./scripts/build-sing-box.sh "$PWD/sing-box"

# 3. 编译后端开发二进制
GOTOOLCHAIN=local go build -o sb-proxy ./backend

# 4. 启动后端（新数据库首次启动需同意用户协议并设置 ADMIN_PASSWORD）
ACCEPT_USER_AGREEMENT=true ADMIN_PASSWORD="dev-only-password" SINGBOX_BINARY="$PWD/sing-box" ./sb-proxy
```

在另一终端启动前端 Vite 开发服务器进行热更新开发（固定监听 `127.0.0.1`，默认将 `/api` 代理至 `http://127.0.0.1:30000`，可通过 `VITE_API_TARGET` 调整）：

```bash
cd frontend
npm run dev
```

### 2.2 构建统一单文件发行版

统一发行二进制同时内嵌 `frontend/dist`（`frontend_dist` tag）与匹配目标平台的 `sing-box 1.12.12` 内核（`bundled_kernel` tag）：

```bash
# 1. 构建并预压缩前端静态资源（生成 .gz 与 .br）
(
  cd frontend
  npm ci
  npm run build
)

# 2. 拉取 Go 依赖并执行统一发行构建（输出路径必须为绝对路径）
GOTOOLCHAIN=local go mod download
GOOS=linux GOARCH=amd64 \
  ./scripts/build-release.sh \
  "$PWD/release-binaries/singbox-proxy-manager-linux-amd64"
```

构建脚本内部保障：
- `scripts/build-sing-box.sh`：验证本机 Go 版本精确为 `go1.26.11`、验证 `sing-box 1.12.12` 模块归档固定 `h1:` 校验和、以 `CGO_ENABLED=0` + `-trimpath` + `-buildvcs=false` + 固定功能 tags 编译内核并复核编译器元数据。
- `scripts/build-release.sh`：使用工作区目录锁防止并行交叉编译误嵌入错误架构内核，构建完成后自动清理临时内嵌内核文件与锁目录。
- `scripts/verify-go-build.sh <binary>`：通过 `go version -m` 验证产物确由 `go1.26.11` 编译。

---

## 3. 测试与质量验证体系

### 3.1 后端测试与扫描

```bash
# 静态检查（需安装 golangci-lint v2.12.0）
golangci-lint run ./backend/... ./internal/...

# 竞态与单元/安全/集成测试（设置 SINGBOX_TEST_BINARY 后可激活真实内核转发测试）
SINGBOX_TEST_BINARY="$PWD/sing-box" go test -race -count=1 ./backend/... ./internal/... -v
```

> **重要说明**：
> - 未设置 `SINGBOX_TEST_BINARY` 时，涉及真实 `sing-box` 转发的部分 Go 测试会自动跳过。
> - 远程数据库集成测试需显式传入 `SBPM_INTEGRATION_DATABASE_URL` / `SBPM_INTEGRATION_TURSO_*` 或 `SBPM_BATCH_IMPORT_INTEGRATION_*` 才会执行；**这些测试会 `DROP` 应用表，切勿指向生产库！**

### 3.2 前端质量检查与回归测试

所有命令在 `frontend/` 目录下执行：

| 命令 | 作用说明 |
| --- | --- |
| `npm run lint` | ESLint 代码规范检查 |
| `npm run audit` | 色彩对比度（`audit-contrast.mjs`）与 i18n 键值完整性（`audit-i18n.mjs`）检查 |
| `npm run test:core` | 前端核心纯逻辑单元测试（`core-regression.test.mjs`） |
| `npm run test:version-sync` | 校验六处版本号（`version.txt`、`package.json`、`package-lock.json`、`README.md` 徽章与 `APP_VERSION` 行）完全一致 |
| `npm run build` | Vite 构建并生成 Brotli / Gzip 预压缩资源 |
| `npm audit --omit=dev --audit-level=moderate` | 生产依赖漏洞审计（CI 独立运行，非 `npm run audit`） |
| `npm run test:e2e:all` | 运行 25 项共享 Mock API 浏览器回归（含 `column-resize`、`check-failed-ui`、`groups-policy-ui`、`inbound-https-ui`、`connections-logs-ui` 等）+ `nodeform-config-preservation`（注：含 `live` 字样的共享场景仍基于 Mock 状态，**不包含** `full-stack` 真实后端测试） |

节点列表回归覆盖节点名截断、列表列宽拖拽缩放与恢复默认持久化、三栏等高对称且入站优先的节点展开布局、备注上方显示及复制完整名称、入站用户名、固定宽度毛玻璃密码掩码、键盘切换及改密复位。连接区回归核对「路由号」双语标签、复制值与 TCP 复用开关。

### 3.3 全栈与发行版系统验证

以下测试会拉起真实后端进程、数据库与 `sing-box` 内核，串联真实浏览器验证完整链路。

> [!WARNING]
> **切勿在包含生产 `.env` 或远程生产数据库环境变量的环境中执行全栈测试！**
> `full-stack-protocol-import` 与 `full-stack-upstream` 会继承当前进程环境并在项目根目录启动后端（后端会自动加载根目录 `.env`），临时 `CONFIG_DIR` **无法隔离** PostgreSQL / MySQL / Turso 远程数据库连接。请务必在不含生产 `.env` 的干净测试副本中执行，并清空 `DATABASE_URL`、`POSTGRES_*`、`PGSQL*`、`MYSQL*`、`TURSO_*` 等环境变量（或显式指向独立可销毁测试库）。

```bash
# 1. 可选：指定浏览器可执行文件路径（若 Chrome / Chromium 位于常见系统路径，test-harness.mjs 会自动探测）
export PUPPETEER_EXECUTABLE_PATH="$(command -v google-chrome || command -v chromium)"

# 2. 针对单文件发行版执行 Linux 系统回归（验证启动、内嵌内核校验修复、数据保留、转发与崩溃自愈）
python3 scripts/test-release.py "$PWD/release-binaries/singbox-proxy-manager-linux-amd64"
SBPM_RELEASE_TEST_CUSTOM_CONFIG=1 python3 scripts/test-release.py "$PWD/release-binaries/singbox-proxy-manager-linux-amd64"

# 3. 针对统一发行版执行完整系统、节点列表、协议导入与预压缩静态资源全栈回归
(
  cd frontend
  SBPM_E2E_BACKEND_BINARY="$PWD/../release-binaries/singbox-proxy-manager-linux-amd64" \
    npm run test:e2e:full-stack-system

  SBPM_E2E_BACKEND_BINARY="$PWD/../release-binaries/singbox-proxy-manager-linux-amd64" \
    npm run test:e2e:full-stack-node-list

  SBPM_E2E_BACKEND_BINARY="$PWD/../release-binaries/singbox-proxy-manager-linux-amd64" \
    SINGBOX_TEST_BINARY="$PWD/../sing-box" \
    npm run test:e2e:full-stack-protocol-import

  SBPM_E2E_BACKEND_BINARY="$PWD/../release-binaries/singbox-proxy-manager-linux-amd64" \
    SINGBOX_TEST_BINARY="$PWD/../sing-box" \
    npm run test:e2e:full-stack-features

  SBPM_TEST_BINARY="$PWD/../release-binaries/singbox-proxy-manager-linux-amd64" \
    npm run test:e2e:precompressed-static
)

# 4. 前置代理全栈回归（脚本后半段通过 SINGBOX_BINARY 注入慢校验包装器，
#    与 CI 一致需使用仅含 frontend_dist、不含 bundled_kernel 的后端构建）
CGO_ENABLED=0 go build -tags frontend_dist -o /tmp/sbpm-upstream-e2e-backend ./backend
(
  cd frontend
  SBPM_E2E_BACKEND_BINARY=/tmp/sbpm-upstream-e2e-backend \
    SINGBOX_TEST_BINARY="$PWD/../sing-box" \
    npm run test:e2e:full-stack-upstream
)
rm -f /tmp/sbpm-upstream-e2e-backend
```

> `full-stack-system-regression.mjs` 支持通过 `SBPM_E2E_DATABASE_MODE` 切换 `sqlite / postgres / mysql / turso` 数据库验证模式；`scripts/test-release.py` 读取 `/proc/<PID>/exe`，仅适用于 Linux 环境。

---

## 4. CI/CD 流水线说明

### 4.1 GitHub Actions（`.github/workflows/ci.yml`）

触发条件：推送至 `main`、推送 `v*` Git Tag 或面向 `main` 的 Pull Request。

1. **发布计划与条件触发判定**：`release-plan` 自动判定是否执行完整流水线（`run_full`）以及是否发版（`should_release`）：
   - Pull Request 与 `v*` Git Tag 始终执行完整 CI；
   - 推送至 `main` 时，判定仅看 `internal/version/version.txt` 内容是否变化及对应 `v<版本号>` 标签是否存在（不按改动文件类型区分）：若版本文件内容未变且对应标签已存在，自动跳过重型构建与测试作业；若版本文件内容变化或对应标签尚不存在，则执行完整 CI 与发版校验。
   - 完整 CI 会重编译 `sing-box 1.12.12`，并行执行 `golangci-lint`、`govulncheck`（同时扫描管理程序源码与二进制及固定内核二进制）、Go `-race` 测试、四库真实服务集成测试（`postgres:17-alpine`、`mysql:8.4`、`libsql-server` 覆盖旧库升级、批量导入与独立日志库）、发布说明与 GHCR 清理脚本单测、前端 ESLint、生产依赖 `npm audit`、`test:core`、`audit`、`test:version-sync`、`build` 以及 26 项前端 E2E 矩阵。
2. **全栈、容器系统测试与质量门禁**：在零削减任何测试项与安全扫描的前提下采用并行 DAG 与增量隔离缓存（按任务用途与目标架构隔离 Go 模块/编译缓存与 `golangci-lint` 缓存，单写者维护 npm 下载缓存，隔离源码与发行 Docker Buildx scope）。`test-system-local-deploy`、`test-release-image-build` 与 `build-binaries` 并行执行，所有质量/安全任务汇总至严格 `quality-gate`（任一项未 `success` 即阻断发布）后才允许 `build-ghcr` 与 `create-release` 执行。复用 `frontend-dist` 与测试内核执行真实上游与协议导入全栈回归、`docker compose --profile local`（基于 `alpine:3.24`）源码构建系统测试（验证未同意协议拒绝启动、`HEALTHCHECK` 达到 `healthy`、Shadowsocks 与 Direct 真实 HTTP/SOCKS5 转发）、单文件发行回归（覆盖非交互/伪终端协议门禁与 `v1.7.29` 原地升级）、`full-stack-system` / `full-stack-node-list` / `full-stack-features` / `precompressed-static` 浏览器验证，以及 `Dockerfile.release`（`alpine:3.24`）多架构构建、镜像体积报告与容器实际启动探活验证。
   - 源码构建和发行镜像均验证 `/app/LICENSE`、`/app/THIRD_PARTY_NOTICES.md`、`/app/USER_AGREEMENT.md`、`/app/licenses/SING-BOX-GPL-3.0.txt` 存在且匹配仓库文件 SHA-256，且校验镜像内置 `HEALTHCHECK`。
3. **自动发布与规范化 GHCR 打标（含 SBOM）**：
   - 工作流顶层对 PR 按编号去重、对 push 按 `run_id` 独立隔离（避免后续纯文档提交取消正在执行的发版流水线）；`build-ghcr` 按版本标签隔离并发并推送对应精确版本镜像（或 `edge`），`create-release` 在全局发布锁内先核对同版本标签未指向其他提交（防止跨提交覆盖；同提交重跑允许重建该标签，如需内容绝对固定请按镜像 digest 拉取）并基于最新远端标签通过 `scripts/publish_tags.py` 计算待推进标签，随后创建 GitHub Release 并推进共享滚动镜像标签（`1.7`、`1`、`latest`），保障并发发布不覆盖、不倒退。
   - **GHCR 多架构镜像（`linux/amd64`, `linux/arm64`）**：`build-ghcr` 启用 `provenance: mode=max` 与 `sbom: true` 同时附带构建来源证明与 SBOM，推送精确版本或 `edge`；正式稳定版由 `create-release` 在创建 Release 后同步推进滚动标签：
     - 正式稳定版（如 `v1.7.30`）：推送精确标签 `1.7.30`，并仅在版本号不低于同主/次/全局既有稳定版时同步推进 `1.7`、`1`、`latest` 滚动标签（旧维护分支补丁不会倒退 `latest`）；
     - 预发布版（如 `v1.8.0-beta.1`）：仅推送精确预发布标签 `1.8.0-beta.1`（不覆盖滚动标签与 `latest`）；
     - 触发了完整 CI 的日常 `main` 非发版提交：跳过 `create-release`，仅覆盖更新 `edge` 标签。
   - **正式/预发布发版时**：`build-binaries` 补齐 FreeBSD / macOS 4 平台单文件并汇总 **6 平台统一单文件**与 `checksums.txt`；待 `build-ghcr` 成功后，`create-release` 调用 `scripts/generate-release-notes.py` 基于上一版本 tag 到当前提交的 commit 历史自动生成分类 Changelog 发布至 GitHub Releases，并附带 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`USER_AGREEMENT.md`、`SING-BOX-GPL-3.0.txt`。
   - 推送完成后自动运行 `scripts/cleanup-ghcr-packages.py` 安全清理历史纯 40 位 SHA 镜像与无任何保留索引引用的孤立未打标层；末尾按消费结果条件清理本次运行产生的临时 Artifacts。

### 4.2 GitLab CI（`.gitlab-ci.yml`）

包含 `lint / test / build / release` 四个阶段并采用 `needs` 并行 DAG、项目内 `.ci-cache/`（按用途隔离 Go / golangci-lint / npm 下载缓存）及 Docker Buildx Registry 缓存，共享校验后的测试内核产物与单次构建的 `frontend/dist`，覆盖 Go lint、漏洞扫描、race 测试、前端核心回归/审计/版本一致性校验与构建、严格 `quality-gate` 质量门禁、GitLab Registry 镜像推送及 `linux/amd64`、`freebsd/amd64` 单文件构建。相比 GitHub Actions，GitLab 流水线不包含同等的浏览器全栈、PostgreSQL / MySQL / libSQL 实时服务矩阵、Compose 实测、单文件运行回归链及 6 平台完整发布矩阵。

GitLab 使用同一个源码构建 Dockerfile 分发许可证文件与用户协议，`auto-release` 校验并附带 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`USER_AGREEMENT.md`、`SING-BOX-GPL-3.0.txt` 附件链接，同时在描述中包含内核许可与对应源码链接。

> GitHub Actions 的 `test-backend` 作业已内置 PostgreSQL 17、MySQL 8.4 与 libSQL 服务容器自动执行多数据库集成测试；若需针对外部托管云数据库（如 Prisma / TiDB / Turso Cloud）手工验证，仍可通过对应 `SBPM_*INTEGRATION*` 环境变量在独立测试库触发。

---

## 5. 发布版本同步清单

每次变更源码准备发版时，需同步更新以下文件并运行 `cd frontend && npm run test:version-sync` 确认一致：

1. `internal/version/version.txt`
2. `frontend/package.json`（`version`）
3. `frontend/package-lock.json`（根 `version` 及 `packages[""].version`）
4. `README.md` 顶部的版本徽章 `https://img.shields.io/badge/version-<x.y.z>-blue.svg`
5. `README.md` 二进制部署命令中独立的顶格行 `APP_VERSION=<x.y.z>`（不能缩进、不加引号或行尾注释）

同时同步 `docs/` 各文档声明的版本基线和 API 响应示例。
