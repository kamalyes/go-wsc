/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 15:17:56
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:21:36
 * @FilePath: \go-wsc\hub\online_status.go
 * @Description: Hub 在线状态相关方法
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"context"
	"time"
)

// GetAllOnlineUserIDs 获取所有在线用户ID列表
// 返回:
//   - []string: 用户ID列表
//   - error: 错误信息
func (h *Hub) GetAllOnlineUserIDs() ([]string, error) {
	// 如果没有 repository，返回本地在线用户
	if h.onlineStatusRepo == nil {
		userIDs := make(map[string]bool)

		// 收集 WebSocket 用户
		h.mutex.RLock()
		for _, client := range h.clients {
			userIDs[client.UserID] = true
		}
		h.mutex.RUnlock()

		// 收集 SSE 用户
		h.sseMutex.RLock()
		for userID := range h.sseClients {
			userIDs[userID] = true
		}
		h.sseMutex.RUnlock()

		result := make([]string, 0, len(userIDs))
		for userID := range userIDs {
			result = append(result, userID)
		}
		return result, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetAllOnlineUsers(ctx)
}

// GetOnlineUsersByNode 获取指定节点的在线用户
// 参数:
//   - nodeID: 节点ID
//
// 返回:
//   - []string: 用户ID列表
//   - error: 错误信息
func (h *Hub) GetOnlineUsersByNode(nodeID string) ([]string, error) {
	// 如果查询本节点且没有 repository，返回本地数据
	if nodeID == h.nodeID && h.onlineStatusRepo == nil {
		return h.GetAllOnlineUserIDs()
	}

	if h.onlineStatusRepo == nil {
		return nil, ErrOnlineStatusRepositoryNotSet
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetOnlineUsersByNode(ctx, nodeID)
}

// GetOnlineUserCount 获取在线用户总数
// 返回:
//   - int64: 在线用户数量
//   - error: 错误信息
func (h *Hub) GetOnlineUserCount() (int64, error) {
	// 如果没有 repository，返回本地在线用户数
	if h.onlineStatusRepo == nil {
		userIDs, _ := h.GetAllOnlineUserIDs()
		return int64(len(userIDs)), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetOnlineCount(ctx)
}

// SyncOnlineStatusToRedis 同步当前所有在线用户到 Redis（使用批量接口）
// 用于 Hub 启动时或定期同步
func (h *Hub) SyncOnlineStatusToRedis() error {
	if h.onlineStatusRepo == nil {
		return ErrOnlineStatusRepositoryNotSet
	}

	h.mutex.RLock()
	clientsCopy := make(map[string]*Client, len(h.clients))
	for id, client := range h.clients {
		clientsCopy[id] = client
	}
	h.mutex.RUnlock()

	// 使用批量接口
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.onlineStatusRepo.BatchSetOnline(ctx, clientsCopy); err != nil {
		h.logger.ErrorKV("批量同步在线状态到Redis失败",
			"error", err,
			"count", len(clientsCopy),
			"node_id", h.nodeID,
		)
		return err
	}

	h.logger.InfoKV("批量同步在线状态到Redis成功",
		"count", len(clientsCopy),
		"node_id", h.nodeID,
	)

	return nil
}
