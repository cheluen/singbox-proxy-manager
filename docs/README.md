# SingBox Proxy Manager 技术文档

本目录收录 **SingBox Proxy Manager**（当前代码版本 `1.7.35`）的架构、配置、协议、API 与开发验证文档，所有内容均以仓库当前源码为准。

> 如果你是首次使用并希望快速部署与上手，请优先阅读根目录的 [README.md](../README.md) 与 [用户协议（USER_AGREEMENT.md）](../USER_AGREEMENT.md)。

## 文档索引

| 文档 | 内容说明 | 适用读者 |
| --- | --- | --- |
| [architecture.md](architecture.md) | 系统架构、单常驻 `sing-box` 进程模型、节点分组与策略调度、当前连接/日志观测、内置内核与预压缩前端、原子变更与崩溃自愈、端口与 TCP 复用、链式前置代理及安全边界 | 维护者、二次开发者、架构评审 |
| [configuration.md](configuration.md) | 全部环境变量参考、HTTPS 入站证书目录扫描与热加载边界、多数据库（SQLite / PostgreSQL / MySQL / Turso）接入与单实例租约、策略与连接采集、日志持久化与轮转、数据目录结构、健康检查、安全加固与故障排查 | 运维人员、生产环境部署者 |
| [protocols.md](protocols.md) | 13 种实体出站协议与 3 种策略节点规范、sing-box 1.12.12 映射规则、16 种 URI Scheme 与 Clash 导入边界、分组订阅同步、导出限制及手动出站去重说明 | 高级用户、协议集成与排障人员 |
| [api.md](api.md) | HTTP 鉴权机制、全部 42 个 API 路由（节点、分组、分组前置测速、订阅、策略、当前连接、日志与流量、全局设置）的请求参数、返回结构、状态码与边界说明 | 自动化脚本作者、API 调用方 |
| [development.md](development.md) | 固定版本重编译（Go 1.26.11 + sing-box 1.12.12）、单文件与 Alpine 3.24 镜像构建、分发文件与按版本条件触发的 CI 回归 | 贡献者、项目维护者 |
| [用户协议](../USER_AGREEMENT.md) | 合法合规使用条款与免责声明 | 所有使用者 |
| [第三方声明](../THIRD_PARTY_NOTICES.md) | 内置 sing-box 与离线 IP 地区表的许可证、对应源码和重编译参数 | 发行维护者、使用者 |

## 文档维护约定

1. **以源码为唯一事实基线**：新增环境变量、协议字段、API 路由或构建约束时，需同步更新对应技术文档。
2. **版本一致性约束**：`frontend/scripts/version-consistency-regression.mjs` 会自动校验以下六处版本号完全一致：
   - `internal/version/version.txt`
   - `frontend/package.json` 的 `version`
   - `frontend/package-lock.json` 根字段及 `packages[""].version`
   - 根目录 `README.md` 中的版本徽章（`shields.io/badge/version-<ver>-blue.svg`）
   - 根目录 `README.md` 中独立的 `APP_VERSION=<ver>` 行
3. **敏感信息零入库**：文档、示例配置及截图/GIF 中不得包含真实密码、公网服务器 IP、私有数据库连接串、会话 Token 或真实代理节点链接；示例地址统一使用 RFC 5737 / RFC 2606 保留地址与域名。
