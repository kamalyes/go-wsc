/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 20:55:15
 * @FilePath: \go-wsc\hub\utils.go
 * @Description: Hub 通用工具方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/gorilla/websocket"
)

// ============================================================================
// WebSocket 错误分类
// ============================================================================

// ClassifyCloseError 分类关闭错误
func ClassifyCloseError(err error) (closeCode int, isNormal bool) {
	closeCode = websocket.CloseAbnormalClosure // 默认异常关闭

	// 遍历检查各种关闭错误
	for code, info := range WsCloseCodeMap {
		if websocket.IsCloseError(err, code) {
			return code, info.IsNormal
		}
	}

	return closeCode, false
}

// ============================================================================
// 心跳维护方法
// ============================================================================

// UpdateClientHeartbeat 更新客户端心跳时间
func (h *Hub) UpdateClientHeartbeat(clientID string) error {
	if h.onlineStatusRepo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.onlineStatusRepo.UpdateClientHeartbeat(ctx, clientID)
}

// ============================================================================
// 测试辅助方法 - 提供安全的写操作
// ============================================================================

// SetClientLastHeartbeatForTest 设置客户端最后心跳时间（用于测试，线程安全）
func (h *Hub) SetClientLastHeartbeatForTest(clientID string, lastHeartbeat time.Time) bool {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if !exists {
		return false
	}
	client.LastHeartbeat = lastHeartbeat
	return true
}
