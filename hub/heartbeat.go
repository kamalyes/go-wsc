/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 01:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-30 11:20:15
 * @FilePath: \go-wsc\hub\heartbeat.go
 * @Description: Hub 心跳处理
 *   - 客户端心跳时间更新
 *   - PONG 响应发送
 *   - 心跳消息处理流程（前置回调 → 更新 → Redis 同步 → PONG → 统计 → 后置回调）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
)

// ============================================================================
// 心跳时间更新
// ============================================================================

// UpdateHeartbeat 更新客户端心跳时间
// 使用 shardedRegistry 查找客户端（分片读锁，粒度细）
func (h *Hub) UpdateHeartbeat(clientID string) {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if !exists {
		return
	}
	now := time.Now()
	client.LastHeartbeat = now
	client.LastSeen = now
}

// UpdatePongTime 更新客户端PONG响应时间（发送PONG时调用）
// 使用 shardedRegistry 查找客户端
func (h *Hub) UpdatePongTime(clientID string) {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if !exists {
		return
	}
	client.LastPong = time.Now()
}

// ============================================================================
// PONG 响应
// ============================================================================

// SendPongResponse 发送 pong 响应给客户端（避免竞态条件）
// 此方法接收已获取的客户端对象，避免在发送时重新查询导致的竞态条件
func (h *Hub) SendPongResponse(client *Client) error {
	if client == nil {
		return errorx.WrapError("client is nil")
	}

	pongMsg := &HubMessage{
		ID:           h.idGenerator.GenerateRequestID(),
		MessageType:  MessageTypePong,
		Sender:       UserTypeSystem.String(),
		SenderType:   UserTypeSystem,
		Receiver:     client.UserID,
		ReceiverType: client.UserType,
		CreateAt:     time.Now(),
		Priority:     PriorityNormal,
	}

	// 序列化消息
	data, err := json.Marshal(pongMsg)
	if err != nil {
		return errorx.WrapError("failed to marshal pong message", err)
	}

	// 使用客户端的 TrySend 方法，它内部已经有锁保护
	if client.TrySend(data) {
		h.UpdatePongTime(client.ID)
		return nil
	}

	return errorx.WrapError("client send channel is full or closed")
}

// ============================================================================
// 心跳消息处理
// ============================================================================

// handleHeartbeatMessage 处理心跳消息
// 流程：前置回调 → 更新心跳 → 日志 → Redis同步 → PONG响应 → 统计 → 后置回调
func (h *Hub) handleHeartbeatMessage(client *Client) {
	// 检查客户端是否已关闭（防止处理已断开客户端的心跳）
	if client.IsClosed() {
		h.logger.DebugKV("客户端已关闭，忽略心跳消息",
			"client_id", client.ID,
			"user_id", client.UserID)
		return
	}

	// 触发心跳前置回调，返回 false 则跳过后续心跳处理
	if h.beforeHeartbeatCallback != nil {
		if !h.beforeHeartbeatCallback(client) {
			return
		}
	}

	// 更新心跳请求时间（内存）- 收到PING时
	h.UpdateHeartbeat(client.ID)

	// 💓 记录心跳日志
	h.logWithClient(logger.DEBUG, "💓 收到心跳消息", client)

	// 同步更新 Redis 中的在线状态和心跳时间
	if h.onlineStatusRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := h.onlineStatusRepo.UpdateClientHeartbeat(ctx, client.ID); err != nil {
			h.logger.DebugKV("更新 Redis 心跳失败",
				"client_id", client.ID,
				"user_id", client.UserID,
				"error", err,
			)
		}
	}

	// 直接发送 pong 响应（使用已获取的客户端对象，避免竞态条件）
	if err := h.SendPongResponse(client); err != nil {
		h.logger.WarnKV("心跳 pong 响应发送失败",
			"client_id", client.ID,
			"user_id", client.UserID,
			"error", err,
		)
	}

	// 异步追踪心跳统计（不阻塞主流程）
	h.trackHeartbeatStats(client)

	// 触发心跳上报回调
	if h.heartbeatReportCallback != nil {
		h.heartbeatReportCallback(client)
	}

	// 触发心跳后置回调
	if h.afterHeartbeatCallback != nil {
		h.afterHeartbeatCallback(client)
	}
}

// ============================================================================
// 心跳配置
// ============================================================================

// SetHeartbeatConfig 设置心跳配置
// interval: 心跳间隔，建议30秒
// timeout: 心跳超时时间，建议90秒（interval的3倍）
func (h *Hub) SetHeartbeatConfig(interval, timeout time.Duration) {
	h.config.HeartbeatInterval = interval
	h.config.ClientTimeout = timeout
}
