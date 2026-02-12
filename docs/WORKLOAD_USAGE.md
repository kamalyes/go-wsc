# Workload Repository 使用指南

## 概述

Workload Repository 用于管理客服的工作负载，支持负载均衡、客服上下线管理和多维度统计

## 核心设计

### 数据结构
- **String Key**: `workload:agent:{agentID}` - 存储客服的实时负载值（持久化）
- **ZSet**: `workload:zset` - 用于快速查询负载最小的在线客服（临时）
- **DB 表**: `wsc_agent_workload` - 持久化存储，支持多维度统计（realtime/hourly/daily/monthly/yearly）

### 多维度统计

系统自动在 5 个时间维度上统计客服负载：

| 维度 | 说明 | 数据保留期 | 示例 TimeKey |
|------|------|-----------|-------------|
| `realtime` | 实时负载 | 永久 | 空字符串 |
| `hourly` | 小时统计 | 7 天 | `2026022513` |
| `daily` | 日统计 | 90 天 | `20260225` |
| `monthly` | 月统计 | 13 个月 | `202602` |
| `yearly` | 年统计 | 5 年 | `2026` |

所有维度默认启用，无需配置。

### 生命周期管理

```
客服上线 → 分配工单 → 客服下线 → 客服重新上线
   ↓          ↓           ↓            ↓
Reload   Increment    Remove       Reload
(恢复)     (+1)      (保留key)    (恢复负载)
```

## 注意事项

### 1. 客服上线必须调用 ReloadAgentWorkload

```go
// ✅ 正确：客服上线时重新加载负载（优先级：Redis > DB > 默认值 0）
workload, err := repo.ReloadAgentWorkload(ctx, agentID)

// ❌ 错误：直接设置负载会覆盖现有值
err := repo.ForceSetAgentWorkload(ctx, agentID, 0)
```

### 2. 客服下线只删除 ZSet

```go
// ✅ 正确：下线时保留 string key 和 DB 记录
err := repo.RemoveAgentWorkload(ctx, agentID)

// ❌ 错误：ForceSetAgentWorkload 会覆盖现有负载
repo.ForceSetAgentWorkload(ctx, agentID, 0)
```

### 3. 负载变化自动同步多维度

```go
// ✅ 正确：使用 Increment/Decrement 自动同步所有维度
err := repo.IncrementAgentWorkload(ctx, agentID)
// 自动更新：realtime, hourly, daily, monthly, yearly

// ❌ 错误：手动修改 Redis key 不会同步到其他维度
// 这会导致统计数据不一致
```

### 4. ForceSetAgentWorkload 慎用

```go
// ⚠️ 慎用场景：手动修正错误数据、系统初始化
repo.ForceSetAgentWorkload(ctx, agentID, 10)

// ❌ 不适用：正常业务流程（会破坏负载恢复机制）
```

## 方法对比

| 方法 | String Key | ZSet | DB | 多维度 | 使用场景 |
|------|-----------|------|----|----|---------|
| `ReloadAgentWorkload` | 恢复/创建 | 同步 | 读取/创建 | ✅ | 客服上线/重新上线 |
| `ForceSetAgentWorkload` | 强制覆盖 | 同步 | 异步同步 | ✅ | 手动修正数据（慎用） |
| `IncrementAgentWorkload` | +1 | +1 | 异步同步 | ✅ | 分配工单 |
| `DecrementAgentWorkload` | -1 | -1 | 异步同步 | ✅ | 完成工单 |
| `RemoveAgentWorkload` | 保留 | 删除 | 保留 | - | 客服下线 |
| `GetAgentWorkload` | 读取 | - | 降级读取 | - | 查询负载 |
| `GetAllAgentWorkloads` | - | 读取 | - | - | 批量查询 |
| `BatchSetAgentWorkload` | 批量设置 | 批量同步 | - | - | 批量初始化 |

## 常见问题

### Q1: 客服重新上线后负载为什么不是 0？

A: 这是设计行为 客服下线时保留了 string key，重新上线时会恢复之前的负载;如果需要重置负载，可以在客服下线时使用 `BatchRemoveAgentWorkload`

### Q2: 为什么要区分 RemoveAgentWorkload 和 BatchRemoveAgentWorkload？

A: 
- `RemoveAgentWorkload`: 客服临时下线（午休、下班），保留负载数据以便恢复
- `BatchRemoveAgentWorkload`: 客服永久离职或数据清理，完全删除

### Q3: 如何处理客服长时间离线的情况？

A: 可以定期清理长时间离线的客服数据：

```go
// 清理 7 天未上线的客服
func cleanupInactiveAgents(ctx context.Context) error {
    inactiveAgents := getInactiveAgents(ctx, 7*24*time.Hour)
    if repo, ok := repo.(*RedisWorkloadRepository); ok {
        return repo.BatchRemoveAgentWorkload(ctx, inactiveAgents)
    }
    return nil
}
```

### Q4: 负载查询为什么有时候不准确？

A: 可能的原因：
1. 客服上线后没有调用 `InitAgentWorkload`
2. 手动修改了 string key 但没有同步到 ZSet
3. 使用了 `SetAgentWorkload` 而不是 `Increment/Decrement`

解决方案：严格按照 API 使用规范操作

## 性能优化

### 1. 批量操作

```go
// 批量设置多个客服的负载
workloads := map[string]int64{
    "agent1": 5,
    "agent2": 3,
    "agent3": 8,
}
err := repo.BatchSetAgentWorkload(ctx, workloads)
```

### 2. 分页查询

```go
// 获取负载最小的前 100 个客服
top100, err := repo.GetAllAgentWorkloads(ctx, 100)
```

### 3. 并发安全

所有操作都使用 Lua 脚本保证原子性，可以安全地并发调用

## 总结

Workload Repository 的核心设计理念：
- **String Key 持久化**：保存客服的真实负载，支持下线后恢复
- **ZSet 临时索引**：快速查询在线客服的负载，支持负载均衡
- **生命周期管理**：清晰的上线/下线流程，避免数据丢失
- **原子性保证**：所有操作使用 Lua 脚本，确保数据一致性
