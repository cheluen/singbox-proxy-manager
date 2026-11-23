# SingBox Proxy Manager 技术文档

本目录收录 **SingBox Proxy Manager**（当前代码版本 `1.7.23`）的架构、配置、协议、API 与开发验证文档，所有内容均以仓库当前源码为准。

> 如果你是首次使用并希望快速部署与上手，请优先阅读根目录的 [README.md](../README.md)。

## 文档索引

| 文档 | 内容说明 | 适用读者 |
| --- | --- | --- |
| [architecture.md](architecture.md) | 系统架构、单常驻 `sing-box` 进程模型、内置内核与预压缩前端、原子变更与崩溃自愈、端口与 TCP 复用、链式前置代理及安全边界 | 维护者、二次开发者、架构评审 |
| [configuration.md](configuration.md) | 全部环境变量参考、多数据库（SQLite / PostgreSQL / MySQL / Turso）接入与单实例租约、数据目录结构、安全加固、升级与故障排查 | 运维人员、生产环境部署者 |
| [protocols.md](protocols.md) | 12 种出站协议字段规范、sing-box 1.12.12 映射规则、15 种 URI Scheme 与 Clash 结构化导入边界、导出限制及手动出站去重说明 | 高级用户、协议集成与排障人员 |
| [api.md](api.md) | HTTP 鉴权机制、全部 26 个 API 路由的请求参数、返回结构、状态码与边界说明 | 自动化脚本作者、API 调用方 |
| [development.md](development.md) | 本地前后端开发、固定版本安全重编译（Go 1.26.11 + sing-box 1.12.12）、统一单文件构建、全套测试矩阵与 CI/CD 流水线 | 贡献者、项目维护者 |

## 文档维护约定

1. **以源码为唯一事实基线**：新增环境变量、协议字段、API 路由或构建约束时，需同步更新对应技术文档。
2. **版本一致性约束**：`frontend/scripts/version-consistency-regression.mjs` 会自动校验以下六处版本号完全一致：
   - `internal/version/version.txt`
   - `frontend/package.json` 的 `version`
   - `frontend/package-lock.json` 根字段及 `packages[""].version`
   - 根目录 `README.md` 中的版本徽章（`shields.io/badge/version-<ver>-blue.svg`）
   - 根目录 `README.md` 中独立的 `APP_VERSION=<ver>` 行
3. **敏感信息零入库**：文档、示例配置及截图/GIF 中不得包含真实密码、公网服务器 IP、私有数据库连接串、会话 Token 或真实代理节点链接；示例地址统一使用 RFC 5737 / RFC 2606 保留地址与域名。
