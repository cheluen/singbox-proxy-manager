# SingBox 代理转发管理系统-纯为了学习相关技术自用

<div align="center">

![Version](https://img.shields.io/badge/version-1.2.0-blue.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)
![SingBox](https://img.shields.io/badge/sing--box-1.12.12-orange.svg)

一个基于 sing-box 的代理节点管理和转发系统，提供简洁易用的 Web 界面。

[功能特性](#-功能特性) • [快速开始](#-快速开始) • [使用说明](#-使用说明) • [配置说明](#-配置说明)

</div>

---

## 📋 功能特性

### 核心功能
- 🚀 **单进程架构** - 使用单个 sing-box 进程管理所有节点，资源占用低
- 🔐 **认证保护** - 支持为每个代理节点设置独立的用户名密码
- 🌐 **多协议支持** - 支持 VLESS、VMess、Trojan、Hysteria2、TUIC、Shadowsocks、AnyTLS，以及 SOCKS5/HTTP 上游代理
- 🔄 **双模式代理** - 单端口同时支持 HTTP 和 SOCKS5 协议
- 📊 **IP 检测** - 实时检测节点 IP、地理位置和延迟

### 管理功能
- ✨ **可视化管理** - 现代化的 React + Ant Design 界面
- 📥 **批量操作** - 支持批量导入、删除、设置认证、检测 IP
- 📝 **备注/导出/替换** - 给节点加备注；支持导出原分享链接；支持用新分享链接直接替换节点
- 🔧 **灵活配置** - 自定义入站端口，自动或手动分配
- 🌍 **多语言** - 支持中文/英文界面切换
- 📱 **拖拽排序** - 拖拽节点即可重新排序并自动分配端口

---

## 🚀 快速开始

### 环境要求
- Docker & Docker Compose
- 网络模式：host（用于多端口监听）

### 一键部署

```bash
# 1. 克隆项目
git clone https://github.com/cheluen/singbox-proxy-manager.git
cd singbox-proxy-manager

# 2. 可选：设置管理员密码（推荐）
# 方式 A：通过环境变量 ADMIN_PASSWORD 指定固定管理员密码（面板内将无法修改）
# 方式 B：不设置 ADMIN_PASSWORD，首次打开面板会要求设置管理员密码（可在面板内修改）
nano docker-compose.yml

# 3. 启动服务
docker compose up -d

# 4. 查看日志
docker compose logs -f
```

服务启动后访问：`http://您的服务器IP:30000`

---

## 📖 使用说明

### 1. 登录系统

- 默认端口：`30000`
- **不再提供默认密码**
  - 若设置 `ADMIN_PASSWORD` 且不为空：登录密码为该值；面板内无法修改管理员密码（需修改环境变量并重启服务）
  - 若未设置 `ADMIN_PASSWORD`：首次打开管理面板会要求先设置管理员密码（设置后可在面板内修改）

### 2. 添加节点

#### 方式一：单个添加
1. 点击「添加节点」按钮
2. 粘贴分享链接（支持 vless://、vmess://、hysteria2:// 等）
3. 设置入站端口（留空自动分配）
4. 系统会为每个新节点自动生成入站用户名/密码（可在面板内单独或批量修改）

#### 方式二：批量导入
1. 点击「批量导入」
2. 每行一个分享链接
3. 系统自动解析并添加

### 3. 使用代理

配置您的客户端（如浏览器、指纹浏览器等）：

```
代理类型：HTTP 或 SOCKS5
服务器：您的服务器IP
端口：30001、30002、30003...（对应各节点）
用户名：在管理界面查看/修改（新节点会自动生成）
密码：在管理界面查看/修改（新节点会自动生成）
```

### 4. 管理节点

- **启用/禁用**：点击开关即可切换节点状态
- **检测 IP**：勾选节点后点击「批量测IP」
- **拖拽排序**：拖动节点到目标位置，端口自动重新分配
- **批量认证**：勾选多个节点，统一设置用户名密码
- **批量删除**：勾选节点后点击「批量删除」

---

## ⚙️ 配置说明

### 环境变量

在 `docker-compose.yml` 中配置：

```yaml
environment:
  - PORT=30000              # 管理界面端口
  - CONFIG_DIR=/app/config  # 配置文件目录
  - ADMIN_PASSWORD=          # 可选：管理员密码（不为空则使用该值；面板内无法修改）
  - CORS_ALLOWED_ORIGINS=    # 可选：管理 API 允许的跨域来源（逗号分隔；默认不启用 CORS）
  - LOGIN_RATE_LIMIT_WINDOW_SECONDS=60   # 可选：登录限速窗口（秒）
  - LOGIN_RATE_LIMIT_MAX_ATTEMPTS=10     # 可选：窗口内最大失败次数
  - LOGIN_RATE_LIMIT_BLOCK_SECONDS=600   # 可选：触发限速后的封禁时间（秒）
  - HTTP_READ_HEADER_TIMEOUT=5s          # 可选：管理 API 读请求头超时
  - HTTP_READ_TIMEOUT=15s                # 可选：读请求体超时
  - HTTP_WRITE_TIMEOUT=30s               # 可选：写响应超时
  - HTTP_IDLE_TIMEOUT=60s                # 可选：空闲连接超时
  - HTTP_MAX_HEADER_BYTES=1048576        # 可选：最大请求头大小（字节）
  - TURSO_DATABASE_URL=${TURSO_DATABASE_URL} # 可选：Turso 远程数据库 URL（需与 TURSO_AUTH_TOKEN 一起设置）
  - TURSO_AUTH_TOKEN=${TURSO_AUTH_TOKEN}     # 可选：Turso 认证 Token（需与 TURSO_DATABASE_URL 一起设置）
```

> 不想用 Turso 可以不设置 `TURSO_DATABASE_URL / TURSO_AUTH_TOKEN`，留空会自动使用本地 SQLite。

### 端口说明

- `30000`：管理界面端口（可修改）
- `30001+`：代理节点入站端口（自动分配或手动设置）

### 数据持久化

默认使用本地 SQLite，数据存储在 `./config` 目录：
- `config.json`：sing-box 配置文件
- `proxy.db`：节点数据库
- `singbox.log`：sing-box 日志

如果启用了 Turso 远程数据库：
- 节点/设置数据会存到 Turso（不再写入本地 `proxy.db`）
- `./config` 目录仍会保存 `config.json` 和 `singbox.log`（方便排障）

```bash
# 备份数据
cp -r ./config ./config.backup

# 恢复数据
cp -r ./config.backup ./config
docker compose restart
```

---

## 🔧 高级配置

### 修改默认端口

```yaml
# docker-compose.yml
environment:
  - PORT=8080  # 改为您想要的端口
```

### 使用环境变量管理密码

```bash
# 创建 .env 文件
echo "ADMIN_PASSWORD=您的超级安全密码" > .env

# 修改 docker-compose.yml
environment:
  - ADMIN_PASSWORD=${ADMIN_PASSWORD}
```

### 使用 Turso 远程数据库（可选）

不想折腾就别配：默认走本地 SQLite，配合 `./config` 挂载已经能持久化数据。

当你希望“数据不跟着机器/磁盘走”（比如迁移服务器、或者想把数据放云上）时，可以用 Turso。

1. 在 Turso 控制台创建数据库，拿到两项信息：`TURSO_DATABASE_URL` 和 `TURSO_AUTH_TOKEN`（两者缺一不可）
2. 在项目根目录新建 `.env`，把这两项按原样写进去（不要提交到 Git）
   - 本项目的 `docker-compose.yml` 已经预留了这两项环境变量，你只需要提供 `.env` 的值
3. 重启服务

```bash
docker compose down
docker compose up -d
```

### 自定义入站端口范围

首次添加节点时设置起始端口，后续节点将顺延分配。

---

## 🛠️ 技术栈

### 后端
- **语言**：Go 1.24
- **框架**：Gin
- **数据库**：SQLite（默认，本地存储）/ Turso（可选，远程 libsql）
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
| HTTP | ✅ | ✅ | 入站/出站均支持认证（出站可选 TLS） |
| SOCKS5 | ✅ | ✅ | 入站/出站均支持认证 |
| VLESS | - | ✅ | Reality（pbk/sid/spx）、WS、gRPC、HTTP/Upgrade、ALPN、指纹 |
| VMess | - | ✅ | WS、HTTP/2、gRPC、HTTPUpgrade、全套安全/填充参数 |
| Trojan | - | ✅ | TLS/utls 指纹、WS/GRPC/HTTP/HTTPUpgrade 传输、SNI/ALPN、可跳过校验 |
| Hysteria2 | - | ✅ | brutal/obfs/salamander、带宽/网络/跳点参数、TLS 指纹 |
| TUIC | - | ✅ | QUIC/UDP 模式、0-RTT、心跳、SNI/ALPN、RTT 优化 |
| Shadowsocks | - | ✅ | AEAD/2022、UDP over TCP、插件参数 |
| AnyTLS | - | ✅ | TLS 伪装、SNI/ALPN/指纹、会话管理（idle_session_*） |

> **说明**：入站端口使用 mixed 模式，单端口同时支持 HTTP 和 SOCKS5

---

## 🔒 安全建议

1. **设置强管理员密码**
   - 推荐：通过环境变量 `ADMIN_PASSWORD` 配置固定密码（面板内无法修改，需改环境变量并重启）
   - 或者：不设置 `ADMIN_PASSWORD`，首次打开面板时设置一个强密码（后续可在面板内修改）

2. **限制访问 IP**（可选）
   ```bash
   # 使用防火墙限制管理端口访问
   ufw allow from 您的IP to any port 30000
   ```

3. **定期备份数据**
   ```bash
   # 设置定时备份
   crontab -e
   # 添加：每天凌晨2点备份
   0 2 * * * cp -r /root/sb-proxy/config /root/sb-proxy/config.$(date +\%Y\%m\%d)
   ```

4. **使用 HTTPS**（生产环境）
   - 建议在前面部署 Nginx 反向代理
   - 配置 SSL 证书（Let's Encrypt）

---

## 🐛 故障排查

### 服务无法启动
```bash
# 查看日志
docker compose logs -f

# 检查端口占用
ss -tlnp | grep 30000

# 重新构建
docker compose down
docker compose up -d --build
```

### 代理无法连接
```bash
# 测试代理连通性
curl --proxy http://用户名:密码@服务器IP:30001 http://httpbin.org/ip

# 检查 sing-box 日志
docker exec sb-proxy tail -f /app/config/singbox.log

# 检查端口监听
docker exec sb-proxy ss -tlnp | grep sing-box
```

### 节点 IP 检测失败
- 检查服务器网络连接
- 确认节点配置正确
- 查看后端日志：`docker compose logs sb-proxy`

---

## 📊 性能优化

### 资源占用
- **单进程模式**：10个节点仅使用 ~40MB 内存
- **多节点支持**：理论支持上百个节点（受端口限制）

### 推荐配置
- **CPU**：1核心以上
- **内存**：512MB 以上
- **硬盘**：1GB 以上

---

## 🔄 更新升级

```bash
# 拉取最新代码
git pull

# 重新构建并启动
docker compose down
docker compose up -d --build

# 数据会自动保留（./config 目录）
```

---

## 📄 许可证

本项目采用 MIT 许可证。详见 [LICENSE](LICENSE) 文件。

---

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

---

## ⚠️ 免责声明

本项目仅供学习和研究使用。使用本软件所产生的任何后果由使用者自行承担，开发者不承担任何责任。

---

## 📮 联系方式

如有问题或建议，欢迎通过以下方式联系：

- 提交 [GitHub Issue](https://github.com/cheluen/singbox-proxy-manager/issues)

---

<div align="center">

**[⬆ 回到顶部](#singbox-代理转发管理系统)**

Made with ❤️ | [GitHub](https://github.com/cheluen/singbox-proxy-manager)

</div>
