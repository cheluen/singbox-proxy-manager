# SingBox 代理转发管理系统

<div align="center">

![Version](https://img.shields.io/badge/version-1.0.0-blue.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)
![SingBox](https://img.shields.io/badge/sing--box-1.12.11-orange.svg)

一个基于 sing-box 的代理节点管理和转发系统，提供简洁易用的 Web 界面。

[功能特性](#-功能特性) • [快速开始](#-快速开始) • [使用说明](#-使用说明) • [配置说明](#-配置说明)

</div>

---

## 📋 功能特性

### 核心功能
- 🚀 **单进程架构** - 使用单个 sing-box 进程管理所有节点，资源占用低
- 🔐 **认证保护** - 支持为每个代理节点设置独立的用户名密码
- 🌐 **多协议支持** - 完整支持 VLESS、VMess、Hysteria2、TUIC、Shadowsocks
- 🔄 **双模式代理** - 单端口同时支持 HTTP 和 SOCKS5 协议
- 📊 **IP 检测** - 实时检测节点 IP、地理位置和延迟

### 管理功能
- ✨ **可视化管理** - 现代化的 React + Ant Design 界面
- 📥 **批量操作** - 支持批量导入、删除、设置认证、检测 IP
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

# 2. 修改默认密码（重要！）
# 编辑 docker-compose.yml，修改 ADMIN_PASSWORD
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
- 默认密码：`admin123`（**请立即修改！**）

### 2. 添加节点

#### 方式一：单个添加
1. 点击「添加节点」按钮
2. 粘贴分享链接（支持 vless://、vmess://、hysteria2:// 等）
3. 设置入站端口（留空自动分配）
4. 设置认证用户名密码（可选）

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
用户名：在管理界面设置的用户名
密码：在管理界面设置的密码
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
  - ADMIN_PASSWORD=admin123 # 管理密码（请修改！）
```

### 端口说明

- `30000`：管理界面端口（可修改）
- `30001+`：代理节点入站端口（自动分配或手动设置）

### 数据持久化

数据存储在 `./config` 目录：
- `config.json`：sing-box 配置文件
- `proxy.db`：节点数据库
- `singbox.log`：sing-box 日志

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

### 自定义入站端口范围

首次添加节点时设置起始端口，后续节点将顺延分配。

---

## 🛠️ 技术栈

### 后端
- **语言**：Go 1.21
- **框架**：Gin
- **数据库**：SQLite
- **代理核心**：sing-box 1.12.11

### 前端
- **框架**：React 18
- **UI 库**：Ant Design 5
- **构建工具**：Vite 5
- **国际化**：i18next

---

## 📝 支持的协议

| 协议 | 入站 | 出站 | 特性 |
|------|------|------|------|
| HTTP | ✅ | - | 支持认证 |
| SOCKS5 | ✅ | - | 支持认证 |
| VLESS | - | ✅ | Reality、WebSocket、gRPC |
| VMess | - | ✅ | WebSocket、HTTP/2 |
| Hysteria2 | - | ✅ | UDP 优化 |
| TUIC | - | ✅ | QUIC 协议 |
| Shadowsocks | - | ✅ | AEAD 加密 |

> **说明**：入站端口使用 mixed 模式，单端口同时支持 HTTP 和 SOCKS5

---

## 🔒 安全建议

1. **立即修改默认密码**
   ```bash
   # 编辑 docker-compose.yml
   - ADMIN_PASSWORD=您的强密码
   ```

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
