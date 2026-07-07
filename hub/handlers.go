/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\handlers.go
 * @Description: Hub 资源管理与配置查询
 *   - 连接池管理器（SMTP 等）
 *   - 频率限制器
 *   - 消息队列状态
 *
 * 心跳相关逻辑已拆分至 heartbeat.go
 * 断开连接/状态管理已拆分至 connection.go
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

// ============================================================================
// 资源管理
// ============================================================================

// GetPoolManager 获取连接池管理器
func (h *Hub) GetPoolManager() PoolManager {
	return h.poolManager
}

// GetSMTPClient 从连接池管理器获取SMTP客户端
func (h *Hub) GetSMTPClient() interface{} {
	if h.poolManager != nil {
		return h.poolManager.GetSMTPClient()
	}
	return nil
}

// GetRateLimiter 获取消息频率限制器
func (h *Hub) GetRateLimiter() *RateLimiter {
	return h.rateLimiter
}

// ============================================================================
// 消息队列
// ============================================================================

// GetMessageQueue 获取消息队列长度
func (h *Hub) GetMessageQueue() int {
	return len(h.pendingMessages)
}
