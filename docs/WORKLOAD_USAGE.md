# Workload Repository 使用指南

## 概述

Workload Repository 用于管理客服的工作负载，支持负载均衡和客服上下线管理

## 核心设计

### 数据结构
- **String Key**: `workload:agent:{agentID}` - 存储客服的实际负载值（持久化）
- **ZSet**: `workload:zset` - 用于快速查询负载最小的在线客服（临时）

### 生命周期管理

```
客服上线 → 分配工单 → 客服下线 → 客服重新上线
   ↓          ↓           ↓            ↓
 Init    Increment    Remove        Init
  (0)      (+1)      (保留key)    (恢复负载)
```

## 注意事项

### 1. 客服上线必须调用 InitAgentWorkload

```go
// ✅ 正确：客服上线时初始化
workload, err := repo.InitAgentWorkload(ctx, agentID, 0)

// ❌ 错误：直接设置负载会覆盖现有值
err := repo.SetAgentWorkload(ctx, agentID, 0)
```

### 2. 客服下线只删除 ZSet

```go
// ✅ 正确：下线时保留 string key
err := repo.RemoveAgentWorkload(ctx, agentID)

// ❌ 错误：使用批量删除会丢失负载数据
repo.BatchRemoveAgentWorkload(ctx, []string{agentID})
```

### 3. 负载变化必须同步更新

```go
// ✅ 正确：使用 Increment/Decrement 自动同步
err := repo.IncrementAgentWorkload(ctx, agentID)

// ❌ 错误：手动修改 string key 不会同步到 ZSet
// 这会导致负载查询不准确
```

### 4. 批量删除仅用于永久清理

```go
// ✅ 适用场景：客服永久离职、数据清理
repo.BatchRemoveAgentWorkload(ctx, []string{agentID})

// ❌ 不适用：客服临时下线（会丢失负载数据）
```

## 方法对比

| 方法 | String Key | ZSet | 使用场景 |
|------|-----------|------|---------|
| `InitAgentWorkload` | 不存在则创建，存在则保留 | 同步 | 客服上线/重新上线 |
| `SetAgentWorkload` | 强制覆盖 | 同步 | 手动设置负载（慎用） |
| `IncrementAgentWorkload` | +1 | +1 | 分配工单 |
| `DecrementAgentWorkload` | -1 | -1 | 完成工单 |
| `RemoveAgentWorkload` | 保留 | 删除 | 客服下线 |
| `BatchRemoveAgentWorkload` | 删除 | 删除 | 永久清理 |

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
