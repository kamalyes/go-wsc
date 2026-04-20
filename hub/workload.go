/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-30 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-30 00:00:00
 * @FilePath: \go-wsc\hub\workload.go
 * @Description: 客服工作负载管理相关方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"fmt"
)

// ============================================================================
// 客服工作负载管理方法
// ============================================================================

// checkWorkloadRepo 检查工作负载仓库是否已初始化
func (h *Hub) checkWorkloadRepo(operation string) error {
	if h.workloadRepo == nil {
		h.logger.Error("❌ WorkloadRepository未初始化,无法%s", operation)
		return fmt.Errorf("workloadRepo is not initialized")
	}
	return nil
}

// ForceSetAgentWorkload 强制设置客服工作负载（慎用）
func (h *Hub) ForceSetAgentWorkload(agentID string, workload int64) error {
	if err := h.checkWorkloadRepo("强制设置工作负载"); err != nil {
		return err
	}
	return h.workloadRepo.ForceSetAgentWorkload(h.ctx, agentID, workload)
}

// GetAgentWorkload 获取客服工作负载
func (h *Hub) GetAgentWorkload(agentID string) (int64, error) {
	if err := h.checkWorkloadRepo("获取工作负载"); err != nil {
		return 0, err
	}
	return h.workloadRepo.GetAgentWorkload(h.ctx, agentID)
}

// RemoveAgentWorkload 移除客服工作负载
func (h *Hub) RemoveAgentWorkload(agentID string) error {
	if err := h.checkWorkloadRepo("移除工作负载"); err != nil {
		return err
	}
	return h.workloadRepo.RemoveAgentWorkload(h.ctx, agentID)
}

// IncrementAgentWorkload 增加客服工作负载
func (h *Hub) IncrementAgentWorkload(agentID string) error {
	if err := h.checkWorkloadRepo("增加工作负载"); err != nil {
		return err
	}
	return h.workloadRepo.IncrementAgentWorkload(h.ctx, agentID)
}

// DecrementAgentWorkload 减少客服工作负载
func (h *Hub) DecrementAgentWorkload(agentID string) error {
	if err := h.checkWorkloadRepo("减少工作负载"); err != nil {
		return err
	}
	return h.workloadRepo.DecrementAgentWorkload(h.ctx, agentID)
}

// GetLeastLoadedAgent 获取负载最小的在线客服
func (h *Hub) GetLeastLoadedAgent(dimension WorkloadDimension) (string, int64, error) {
	if err := h.checkWorkloadRepo("获取负载最小的客服"); err != nil {
		return "", 0, err
	}

	// 获取在线客服列表
	onlineAgents, err := h.GetOnlineUsersByType(UserTypeAgent)
	if err != nil {
		return "", 0, err
	}

	if len(onlineAgents) == 0 {
		return "", 0, nil
	}

	return h.workloadRepo.GetLeastLoadedAgent(h.ctx, onlineAgents, dimension)
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
func (h *Hub) AcquireLeastLoadedAgent(onlineAgents []string, dimension WorkloadDimension) (string, int64, error) {
	if err := h.checkWorkloadRepo("原子获取并预扣减负载最小的客服"); err != nil {
		return "", 0, err
	}

	if len(onlineAgents) == 0 {
		agents, err := h.GetOnlineUsersByType(UserTypeAgent)
		if err != nil {
			return "", 0, err
		}
		if len(agents) == 0 {
			return "", 0, nil
		}
		onlineAgents = agents
	}

	return h.workloadRepo.AcquireLeastLoadedAgent(h.ctx, onlineAgents, dimension)
}

// ReloadAgentWorkload 重新加载客服工作负载（客服上线时调用）
func (h *Hub) ReloadAgentWorkload(agentID string) (int64, error) {
	if err := h.checkWorkloadRepo("重新加载工作负载"); err != nil {
		return 0, err
	}
	return h.workloadRepo.ReloadAgentWorkload(h.ctx, agentID)
}

// GetAllAgentWorkloads 获取所有客服的负载信息
func (h *Hub) GetAllAgentWorkloads(limit int64) ([]WorkloadInfo, error) {
	if err := h.checkWorkloadRepo("获取所有客服负载"); err != nil {
		return nil, err
	}
	return h.workloadRepo.GetAllAgentWorkloads(h.ctx, limit)
}
