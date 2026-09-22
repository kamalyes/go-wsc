/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:26:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:26:00
 * @FilePath: \go-wsc\group\workload.go
 * @Description: 客服工作负载 —— 从 hub/workload.go 抽出（支持多命名空间隔离）
 *
 * 本域是 spi.WorkloadStore 的门面：负责「仓储未注入」的显式报错与
 * 「在线客服列表」的补齐，真正的读写语义在存储实现里。
 *
 * 与统计域不同，这里未注入仓储时**必须报错**而非 no-op —— 负载分配静默
 * 失败会导致工单扎堆到同一客服，是业务事故而非可有可无的观测数据。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"
	"fmt"

	"github.com/kamalyes/go-wsc/models"
)

// WorkloadManager 客服负载管理器
type WorkloadManager struct {
	host Host
}

// NewWorkloadManager 创建客服负载管理器
func NewWorkloadManager(host Host) *WorkloadManager {
	return &WorkloadManager{host: host}
}

// checkStore 检查工作负载仓储是否已初始化
func (m *WorkloadManager) checkStore(ctx context.Context, operation string) error {
	if m.host.GetWorkloadStore() == nil {
		m.host.GetLogger().ErrorContext(ctx, "❌ WorkloadRepository未初始化,无法%s", operation)
		return fmt.Errorf("workloadRepo is not initialized")
	}
	return nil
}

// ForceSetAgentWorkload 强制设置客服工作负载（慎用）
func (m *WorkloadManager) ForceSetAgentWorkload(ctx context.Context, agentID string, workload int64) error {
	if err := m.checkStore(ctx, "强制设置工作负载"); err != nil {
		return err
	}
	return m.host.GetWorkloadStore().ForceSetAgentWorkload(ctx, agentID, workload)
}

// GetAgentWorkload 获取客服工作负载
func (m *WorkloadManager) GetAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	if err := m.checkStore(ctx, "获取工作负载"); err != nil {
		return 0, err
	}
	return m.host.GetWorkloadStore().GetAgentWorkload(ctx, agentID)
}

// RemoveAgentWorkload 移除客服工作负载
func (m *WorkloadManager) RemoveAgentWorkload(ctx context.Context, agentID string) error {
	if err := m.checkStore(ctx, "移除工作负载"); err != nil {
		return err
	}
	return m.host.GetWorkloadStore().RemoveAgentWorkload(ctx, agentID)
}

// IncrementAgentWorkload 增加客服工作负载
func (m *WorkloadManager) IncrementAgentWorkload(ctx context.Context, agentID string) error {
	if err := m.checkStore(ctx, "增加工作负载"); err != nil {
		return err
	}
	return m.host.GetWorkloadStore().IncrementAgentWorkload(ctx, agentID)
}

// DecrementAgentWorkload 减少客服工作负载
func (m *WorkloadManager) DecrementAgentWorkload(ctx context.Context, agentID string) error {
	if err := m.checkStore(ctx, "减少工作负载"); err != nil {
		return err
	}
	return m.host.GetWorkloadStore().DecrementAgentWorkload(ctx, agentID)
}

// GetLeastLoadedAgent 获取负载最小的在线客服
func (m *WorkloadManager) GetLeastLoadedAgent(ctx context.Context, dimension models.WorkloadDimension) (string, int64, error) {
	if err := m.checkStore(ctx, "获取负载最小的客服"); err != nil {
		return "", 0, err
	}

	// 获取在线客服列表
	onlineAgents, err := m.host.GetOnlineUsersByType(models.UserTypeAgent)
	if err != nil {
		return "", 0, err
	}

	if len(onlineAgents) == 0 {
		return "", 0, nil
	}

	return m.host.GetWorkloadStore().GetLeastLoadedAgent(ctx, onlineAgents, dimension)
}

// AcquireLeastLoadedAgent 原子地选择负载最小的在线客服并将其负载 +1（分布式安全）
//
// 与 GetLeastLoadedAgent 的区别：在同一个 Redis Lua 中完成"选中 + 多维度原子 +1"，
// 多副本/多 goroutine 并发场景下不会因"读-改-写"窗口读到同一份旧快照而选中同一客服
//
// 参数 onlineAgents 由调用方传入（可以是过滤后的"可接单"列表）；若传入空切片，
// 函数会自动回退到调用 GetOnlineUsersByType(UserTypeAgent) 获取全部在线客服
//
// 业务失败回滚：调用方应在后续业务失败时显式调用 DecrementAgentWorkload 回滚此次预扣减
func (m *WorkloadManager) AcquireLeastLoadedAgent(ctx context.Context, onlineAgents []string, dimension models.WorkloadDimension) (string, int64, error) {
	if err := m.checkStore(ctx, "原子获取并预扣减负载最小的客服"); err != nil {
		return "", 0, err
	}

	if len(onlineAgents) == 0 {
		agents, err := m.host.GetOnlineUsersByType(models.UserTypeAgent)
		if err != nil {
			return "", 0, err
		}
		if len(agents) == 0 {
			return "", 0, nil
		}
		onlineAgents = agents
	}

	return m.host.GetWorkloadStore().AcquireLeastLoadedAgent(ctx, onlineAgents, dimension)
}

// ReloadAgentWorkload 重新加载客服工作负载（客服上线时调用）
func (m *WorkloadManager) ReloadAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	if err := m.checkStore(ctx, "重新加载工作负载"); err != nil {
		return 0, err
	}
	return m.host.GetWorkloadStore().ReloadAgentWorkload(ctx, agentID)
}

// GetAllAgentWorkloads 获取所有客服的负载信息
func (m *WorkloadManager) GetAllAgentWorkloads(ctx context.Context, limit int64) ([]models.WorkloadInfo, error) {
	if err := m.checkStore(ctx, "获取所有客服负载"); err != nil {
		return nil, err
	}
	return m.host.GetWorkloadStore().GetAllAgentWorkloads(ctx, limit)
}
