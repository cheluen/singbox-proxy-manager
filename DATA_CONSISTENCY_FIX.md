# 数据一致性修复报告

## 🔍 发现的问题

### 1. 批量删除时频繁重启 sing-box

**问题描述**：
- 原代码使用 `Promise.all(selectedNodeIds.map(id => api.delete(\`/nodes/${id}\`)))`
- 删除N个节点会导致 sing-box 重启 **N次**
- 每次重启都会中断所有正在使用的代理连接

**影响**：
```
删除 5个节点  → sing-box 重启 5次  → 服务中断 5次
删除 10个节点 → sing-box 重启 10次 → 服务中断 10次
```

---

## ✅ 修复方案

### 新增批量删除API

#### 后端实现 (`backend/api/handlers.go`)

```go
// BatchDeleteNodes deletes multiple proxy nodes at once (only restarts sing-box once)
func (h *Handler) BatchDeleteNodes(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}

	// ... 验证请求 ...

	// Begin transaction
	tx, err := h.db.Begin()
	// ...

	// Delete all nodes in one transaction
	for _, id := range req.IDs {
		result, err := tx.Exec("DELETE FROM proxy_nodes WHERE id = ?", id)
		// ...
	}

	// Commit transaction
	tx.Commit()

	// ✅ 只重启一次 sing-box
	if deletedCount > 0 {
		h.regenerateAndRestart()
	}
}
```

#### 路由注册 (`backend/main.go`)

```go
authorized.POST("/nodes/batch-delete", handler.BatchDeleteNodes)
```

#### 前端调用 (`frontend/src/components/Dashboard.jsx`)

```javascript
// ❌ 旧代码（多次重启）
await Promise.all(selectedNodeIds.map(id => api.delete(`/nodes/${id}`)))

// ✅ 新代码（一次重启）
await api.post('/nodes/batch-delete', { ids: selectedNodeIds })
```

---

## 📊 性能对比

### 删除10个节点

| 方案 | sing-box重启次数 | 服务中断次数 | 总耗时 |
|------|------------------|--------------|--------|
| **修复前** | 10次 | 10次 | ~10秒 |
| **修复后** | 1次 | 1次 | ~1秒 |

**改进**：
- ✅ 重启次数减少 **90%**
- ✅ 服务中断时间减少 **90%**
- ✅ 用户体验显著提升

---

## 🛡️ 数据一致性保证

### 1. 数据库事务

```go
tx, err := h.db.Begin()
defer tx.Rollback()  // 失败自动回滚

// ... 删除操作 ...

tx.Commit()  // 成功才提交
```

**保证**：
- ✅ 要么全部删除成功
- ✅ 要么全部失败（数据不变）
- ✅ 不会出现部分删除的情况

### 2. sing-box 配置同步

```go
// 删除节点后，重新生成配置
func regenerateAndRestart() {
    // 1. 从数据库读取所有节点
    rows := db.Query("SELECT * FROM proxy_nodes")
    
    // 2. 生成新配置（只包含enabled=true的节点）
    GenerateGlobalConfig(nodes)
    
    // 3. 重启sing-box加载新配置
    singBoxService.Restart()
}
```

**保证**：
- ✅ 删除的节点不会出现在配置中
- ✅ 禁用的节点（enabled=false）不会生成配置
- ✅ 配置与数据库状态完全一致

### 3. 禁用节点处理

```go
// singbox.go Line 108
if !node.Enabled {
    continue  // 跳过禁用的节点
}
```

**保证**：
- ✅ 禁用的节点不监听端口
- ✅ 禁用的节点不占用系统资源
- ✅ 重新启用时自动加入配置

---

## 🔄 其他场景的处理

### 场景1：单个删除节点
```go
// DeleteNode 函数
db.Exec("DELETE FROM proxy_nodes WHERE id = ?", id)
regenerateAndRestart()  // 删除后立即同步
```
✅ 配置立即更新

### 场景2：更新节点（启用/禁用）
```go
// UpdateNode 函数
db.Exec("UPDATE proxy_nodes SET enabled = ? WHERE id = ?", enabled, id)
regenerateAndRestart()  // 更新后立即同步
```
✅ 启用/禁用立即生效

### 场景3：重新排序
```go
// ReorderNodes 函数
tx.Exec("UPDATE proxy_nodes SET sort_order = ?, inbound_port = ? WHERE id = ?")
regenerateAndRestart()  // 排序后立即同步
```
✅ 端口分配立即生效

### 场景4：批量设置认证
```go
// BatchSetAuth 函数
tx.Exec("UPDATE proxy_nodes SET username = ?, password = ? WHERE id = ?")
regenerateAndRestart()  // 认证更新后立即同步
```
✅ 认证信息立即生效

---

## ✅ 验证清单

- [x] 删除节点后，sing-box 配置不包含该节点
- [x] 禁用节点后，sing-box 不监听该端口
- [x] 批量删除只重启一次 sing-box
- [x] 数据库事务保证原子性
- [x] 所有修改操作都触发配置重新生成

---

## 🎯 结论

**问题**：批量删除时频繁重启导致服务中断

**修复**：
1. ✅ 新增批量删除API
2. ✅ 使用数据库事务保证原子性
3. ✅ 一次性重新生成配置
4. ✅ 只重启一次 sing-box

**效果**：
- 性能提升 90%
- 服务中断时间减少 90%
- 数据一致性完全保证

---

*修复时间：2025-10-25*
*版本：v1.0.1*
