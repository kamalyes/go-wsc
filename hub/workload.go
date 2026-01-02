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

import "fmt"

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

// SetAgentWorkload 设置客服工作负载
func (h *Hub) SetAgentWorkload(agentID string, workload int64) error {
	if err := h.checkWorkloadRepo("设置工作负载"); err != nil {
		return err
	}
	return h.workloadRepo.SetAgentWorkload(h.ctx, agentID, workload)
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
func (h *Hub) GetLeastLoadedAgent() (string, int64, error) {
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

	return h.workloadRepo.GetLeastLoadedAgent(h.ctx, onlineAgents)
}
