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

// ============================================================================
// 客服工作负载管理方法
// ============================================================================

// SetAgentWorkload 设置客服工作负载
func (h *Hub) SetAgentWorkload(agentID string, workload int64) error {
	if h.workloadRepo == nil {
		return nil // 如果没有配置 workloadRepo，静默返回
	}
	return h.workloadRepo.SetAgentWorkload(h.ctx, agentID, workload)
}

// GetAgentWorkload 获取客服工作负载
func (h *Hub) GetAgentWorkload(agentID string) (int64, error) {
	if h.workloadRepo == nil {
		return 0, nil // 如果没有配置 workloadRepo，返回默认值
	}
	return h.workloadRepo.GetAgentWorkload(h.ctx, agentID)
}

// RemoveAgentWorkload 移除客服工作负载
func (h *Hub) RemoveAgentWorkload(agentID string) error {
	if h.workloadRepo == nil {
		return nil // 如果没有配置 workloadRepo，静默返回
	}
	return h.workloadRepo.RemoveAgentWorkload(h.ctx, agentID)
}

// IncrementAgentWorkload 增加客服工作负载
func (h *Hub) IncrementAgentWorkload(agentID string) error {
	if h.workloadRepo == nil {
		return nil
	}
	return h.workloadRepo.IncrementAgentWorkload(h.ctx, agentID)
}

// DecrementAgentWorkload 减少客服工作负载
func (h *Hub) DecrementAgentWorkload(agentID string) error {
	if h.workloadRepo == nil {
		return nil
	}
	return h.workloadRepo.DecrementAgentWorkload(h.ctx, agentID)
}

// GetLeastLoadedAgent 获取负载最小的在线客服
func (h *Hub) GetLeastLoadedAgent() (string, int64, error) {
	if h.workloadRepo == nil {
		h.logger.WarnMsg("负载管理仓库未设置")
		return "", 0, nil
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
