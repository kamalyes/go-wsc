/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 17:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 19:30:00
 * @FilePath: \go-wsc\spi\workload_store.go
 * @Description: 客服负载存储 SPI - WorkloadStore 接口契约定义
 *
 * 客服工作负载的读写契约，支撑最小负载分配（工单/会话路由）
 * Redis 实现见 adapter/redis 包 WorkloadStore
 *
 * 存储维度：namespace 从 ctx 提取（routing.NamespaceFromContext），空时兜底 DefaultNamespace
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
)

// WorkloadStore 客服负载存储接口
type WorkloadStore interface {
	// ReloadAgentWorkload 重新加载客服工作负载（客服上线时调用）
	// 优先级：Redis > DB > 默认值 0；从持久化存储恢复负载，不存在则初始化为 0 并同步 ZSet
	ReloadAgentWorkload(ctx context.Context, agentID string) (int64, error)

	// ForceSetAgentWorkload 强制设置客服工作负载（慎用，会覆盖现有值）
	// 直接覆盖 Redis 和 ZSet 中的负载值，异步同步到 DB
	ForceSetAgentWorkload(ctx context.Context, agentID string, workload int64) error

	// GetAgentWorkload 获取客服工作负载
	GetAgentWorkload(ctx context.Context, agentID string) (int64, error)

	// IncrementAgentWorkload 增加客服工作负载
	IncrementAgentWorkload(ctx context.Context, agentID string) error

	// DecrementAgentWorkload 减少客服工作负载
	DecrementAgentWorkload(ctx context.Context, agentID string) error

	// GetLeastLoadedAgent 获取负载最小的在线客服
	GetLeastLoadedAgent(ctx context.Context, onlineAgents []string, dimension models.WorkloadDimension) (string, int64, error)

	// AcquireLeastLoadedAgent 原子地选择负载最小的在线客服并将其负载 +1
	//
	// 与 GetLeastLoadedAgent 的区别：
	//   - GetLeastLoadedAgent 只读，纯查询，返回后业务侧还需单独调用 IncrementAgentWorkload，
	//     这会导致"读-改-写"之间存在 TOCTOU 窗口，多进程/多 goroutine 并发时读到同一份旧负载快照，
	//     选出同一个客服，造成工单扎堆分配到同一人（负载均衡不均）
	//   - AcquireLeastLoadedAgent 在同一段 Lua 中完成 "选中 + 所有维度原子 +1"，
	//     Redis 单线程保证跨进程原子，是分布式场景下负载均衡分配的正确做法
	//
	// 返回值:
	//   - agentID: 被选中的客服 ID
	//   - workload: 选中客服在选中前的 realtime 负载值（用于日志/监控）
	//   - error: 执行失败的错误
	//
	// 业务失败回滚：调用方应在后续业务失败时调用 DecrementAgentWorkload 回滚此次预扣减
	AcquireLeastLoadedAgent(ctx context.Context, onlineAgents []string, dimension models.WorkloadDimension) (string, int64, error)

	// RemoveAgentWorkload 移除客服负载记录（客服下线时调用）
	// 只删除 ZSet 记录，保留 string key 以便重新上线时恢复
	RemoveAgentWorkload(ctx context.Context, agentID string) error

	// GetAllAgentWorkloads 获取所有客服负载（按负载升序）
	GetAllAgentWorkloads(ctx context.Context, limit int64) ([]models.WorkloadInfo, error)
}
