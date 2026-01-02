/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\repository.go
 * @Description: Hub 仓库管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"
)

// ============================================================================
// 仓库设置方法
// ============================================================================

// SetOfflineMessageHandler 设置离线消息处理器
func (h *Hub) SetOfflineMessageHandler(handler OfflineMessageHandler) {
	h.offlineMessageHandler = handler
	// TODO: ACK 管理器的离线消息接口需要重新设计
	// 同时设置到 ACK 管理器（统一离线消息处理）
	// if h.ackManager != nil {
	// 	h.ackManager.SetOfflineRepo(handler)
	// }
	h.logger.InfoKV("离线消息处理器已设置",
		"handler_type", "HybridOfflineMessageHandler",
		"ack_integration", false,
	)
}

// SetOfflineMessageRepo 设置离线消息仓库（兼容旧接口）
func (h *Hub) SetOfflineMessageRepo(repo OfflineMessageHandler) {
	h.SetOfflineMessageHandler(repo)
}

// SetOnlineStatusRepository 设置在线状态仓库（Redis）
func (h *Hub) SetOnlineStatusRepository(repo OnlineStatusRepository) {
	h.onlineStatusRepo = repo
	h.logger.InfoKV("在线状态仓库已设置", "repository_type", "redis")
}

// SetWorkloadRepository 设置负载管理仓库（Redis）
func (h *Hub) SetWorkloadRepository(repo WorkloadRepository) {
	h.workloadRepo = repo
	h.logger.InfoKV("负载管理仓库已设置", "repository_type", "redis")
}

// SetMessageRecordRepository 设置消息记录仓库（MySQL）
func (h *Hub) SetMessageRecordRepository(repo MessageRecordRepository) {
	h.messageRecordRepo = repo
	h.logger.InfoKV("消息记录仓库已设置", "repository_type", "mysql")
}

// SetConnectionRecordRepository 设置连接记录仓库（MySQL）
func (h *Hub) SetConnectionRecordRepository(repo ConnectionRecordRepository) {
	h.connectionRecordRepo = repo
	h.logger.InfoKV("连接记录仓库已设置", "repository_type", "mysql")
}

// SetHubStatsRepository 设置 Hub 统计仓库（Redis）
func (h *Hub) SetHubStatsRepository(repo HubStatsRepository) {
	h.statsRepo = repo
	h.logger.InfoKV("Hub统计仓库已设置", "repository_type", "redis")

	// 设置启动时间到 Redis
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = repo.SetStartTime(ctx, h.nodeID, time.Now().Unix())
}

// SetMessageExpireDuration 设置ACK消息的过期时间
func (h *Hub) SetMessageExpireDuration(duration time.Duration) {
	if h.ackManager != nil {
		h.ackManager.SetExpireDuration(duration)
	}
}
