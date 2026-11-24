# 本地开发、构建发布与测试指南

本文档以 `1.7.26` 为基线，说明工具链、本地联调、单文件发行、许可证分发和 CI 测试矩阵。

---

## 1. 开发环境要求

| 依赖项 | 版本要求 | 说明 |
| --- | --- | --- |
| Go | **精确 `1.26.11`** | `go.mod` 声明最低工具链版本，发行与内核构建脚本（`scripts/build-sing-box.sh` / `scripts/verify-go-build.sh`）强制校验精确等于 `go1.26.11`（`GOTOOLCHAIN=local`、`GOWORK=off`）并拒绝其他版本 |
| Node.js | `>= 22.12.0` | Docker 与 CI 使用 Node `22` |
| sing-box 源码版本 | **固定 `1.12.12`** | 由 `scripts/build-sing-box.sh` 从 Go 模块缓存按固定 `h1:` 校验和重编译 |
| Chromium / Chrome | 任意现代版本 | 仅运行前端及全栈浏览器回归测试时需要 |
| Python 3 | 3.x | 仅运行 Linux 单文件发行回归 `scripts/test-release.py` 时需要 |

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

# 4. 启动后端（新数据库首次启动需设置 ADMIN_PASSWORD）
ADMIN_PASSWORD="dev-only-password" SINGBOX_BINARY="$PWD/sing-box" ./sb-proxy
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
| `npm run test:e2e:all` | 运行 20 项共享 Mock API 浏览器回归 + `nodeform-config-preservation`（注：含 `live` 字样的共享场景仍基于 Mock 状态，**不包含** `full-stack` 真实后端测试） |

节点列表回归覆盖节点名截断、备注上方显示及复制完整名称、入站用户名、固定宽度毛玻璃密码掩码、键盘切换及改密复位。连接区回归核对「路由号」双语标签、复制值与 TCP 复用开关。

### 3.3 全栈与发行版系统验证

以下测试会拉起真实后端进程、数据库与 `sing-box` 内核，串联真实浏览器验证完整链路。

> [!WARNING]
> **切勿在包含生产 `.env` 或远程生产数据库环境变量的环境中执行全栈测试！**
> `full-stack-protocol-import` 与 `full-stack-upstream` 会继承当前进程环境并在项目根目录启动后端（后端会自动加载根目录 `.env`），临时 `CONFIG_DIR` **无法隔离** PostgreSQL / MySQL / Turso 远程数据库连接。请务必在不含生产 `.env` 的干净测试副本中执行，并清空 `DATABASE_URL`、`POSTGRES_*`、`PGSQL*`、`MYSQL*`、`TURSO_*` 等环境变量（或显式指向独立可销毁测试库）。

```bash
# 1. 准备浏览器可执行文件路径（按环境选择 chrome / chromium）
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

触发条件：推送至 `main` 或面向 `main` 的 Pull Request（并非由 tag 触发）。

1. **构建测试内核与静态检查**：重编译 `sing-box 1.12.12`，并行执行 `golangci-lint`、`govulncheck`（扫描管理程序与内核并阻断 `GO-2026-6809` / `GO-2026-6859`）、Go `-race` 测试、前端 ESLint、生产依赖 `npm audit`、`test:core`、`audit`、`test:version-sync`、`build`（上传共享 `frontend-dist` 产物供下游复用）以及 21 项前端 E2E 矩阵。
2. **全栈与容器系统测试**：复用 `frontend-dist` 与测试内核执行真实上游与协议导入全栈回归、`docker compose --profile local` 源码构建系统测试（验证 Shadowsocks 与 Direct 真实 HTTP/SOCKS5 转发）、单文件发行回归（在 `main` push 时上传已验收的 Linux AMD64/ARM64 二进制供发布直接复用）、`full-stack-system` / `full-stack-node-list` / `precompressed-static` 浏览器验证，以及 `Dockerfile.release`（`linux/amd64` + `linux/arm64` 两阶段按架构单文件复制）多架构构建验证。
   - 源码构建和发行镜像均验证 `/app/LICENSE`、`/app/THIRD_PARTY_NOTICES.md`、`/app/licenses/SING-BOX-GPL-3.0.txt` 存在且非空，并匹配仓库文件的 SHA-256。发行路径通过载入 `linux/amd64` 镜像检查运行阶段文件。
3. **自动发布（仅 `main` push 通过后执行）**：
   - `build-binaries` 复用 `frontend-dist` 补齐 FreeBSD / macOS 4 平台统一单文件，连同已验收的 Linux 2 平台产物共汇总 **6 平台统一单文件**（无 Windows 发布矩阵）并生成 `checksums.txt`。
   - 创建或更新 `v<version>` GitHub Release，随裸二进制与校验清单附带 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`SING-BOX-GPL-3.0.txt`。发行说明标明内核版权、许可证、固定上游源码和重编译说明。[第三方声明](../THIRD_PARTY_NOTICES.md)
   - 复用 Release 中的 Linux 二进制构建并推送 GHCR 多架构镜像（`linux/amd64`, `linux/arm64`），同时打标 `<sha>`、`v<version>`、`v<version>-linux-amd64-arm64`、`latest`。
   - 末尾按消费作业结果条件清理本次运行产生的临时 Artifacts（若有消费作业失败则保留对应共享产物直至 1 天自动过期，确保失败作业可直接重跑）。

### 4.2 GitLab CI（`.gitlab-ci.yml`）

包含 `lint / test / build / release` 四个阶段，共享校验后的测试内核产物，覆盖 Go lint、漏洞扫描、race 测试、前端构建与版本一致性检查、GitLab Registry 镜像推送及 `linux/amd64`、`freebsd/amd64` 单文件构建。相比 GitHub Actions，GitLab 流水线不包含同等的浏览器全栈、Compose 实测、单文件运行回归链及 6 平台完整发布矩阵。

GitLab 使用同一个源码构建 Dockerfile 分发许可证文件，`auto-release` 校验并附带 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`SING-BOX-GPL-3.0.txt` 附件链接，同时在描述中包含内核许可与对应源码链接。

> 两条流水线均未内置外部 PostgreSQL / MySQL / Turso 实例，远程数据库集成测试需按需在独立测试库手动触发。

---

## 5. 发布版本同步清单

每次变更源码准备发版时，需同步更新以下文件并运行 `cd frontend && npm run test:version-sync` 确认一致：

1. `internal/version/version.txt`
2. `frontend/package.json`（`version`）
3. `frontend/package-lock.json`（根 `version` 及 `packages[""].version`）
4. `README.md` 顶部的版本徽章 `https://img.shields.io/badge/version-<x.y.z>-blue.svg`
5. `README.md` 二进制部署命令中独立的顶格行 `APP_VERSION=<x.y.z>`（不能缩进、不加引号或行尾注释）

同时同步 `docs/` 各文档声明的版本基线和 API 响应示例。
