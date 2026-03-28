/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\handlers.go
 * @Description: Hub 处理器方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 心跳处理
// ============================================================================

// UpdateHeartbeat 更新客户端心跳时间
func (h *Hub) UpdateHeartbeat(clientID string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if client, exists := h.clients[clientID]; exists {
		now := time.Now()
		client.LastHeartbeat = now
		client.LastSeen = now
	}
}

// UpdatePongTime 更新客户端PONG响应时间（发送PONG时调用）
func (h *Hub) UpdatePongTime(clientID string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if client, exists := h.clients[clientID]; exists {
		now := time.Now()
		client.LastPong = now
	}
}

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

// handleHeartbeatMessage 处理心跳消息
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
// 断开连接处理
// ============================================================================

// DisconnectUser 主动断开指定用户的所有连接
func (h *Hub) DisconnectUser(userID string, reason string) error {
	clientMap, exists := h.GetUserClientsMapWithLock(userID)

	if !exists || len(clientMap) == 0 {
		return errorx.NewError(ErrTypeUserNotFound, "user_id: %s", userID)
	}

	// 断开所有客户端连接
	h.CloseAllClientsInMap(clientMap)
	return nil
}

// DisconnectClient 主动断开特定客户端
func (h *Hub) DisconnectClient(clientID string, reason string) error {
	client, exists := h.GetClientByIDWithLock(clientID)

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	if client.Conn != nil {
		client.Conn.Close()
	}
	return nil
}

// disconnectKickedClient 断开被踢出的客户端
func (h *Hub) disconnectKickedClient(ctx context.Context, client *Client, reason string) {
	// 调用断开回调
	if h.clientDisconnectCallback != nil {
		syncx.Go().
			OnPanic(func(r any) {
				h.logger.ErrorKV("踢出用户断开回调 panic", "panic", r, "client_id", client.ID)
			}).
			OnError(func(err error) {
				h.logger.ErrorKV("踢出用户时断开回调执行失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
				if h.errorCallback != nil {
					_ = h.errorCallback(ctx, err, ErrorSeverityWarning)
				}
			}).
			ExecWithContext(func(execCtx context.Context) error {
				return h.clientDisconnectCallback(execCtx, client, DisconnectReasonKickOut)
			})
	}

	// 关闭连接
	h.logger.InfoKV("关闭被踢用户的连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"reason", reason,
	)
	if client.Conn != nil {
		client.Conn.Close()
	}

	// 从 Hub 中移除
	h.Unregister(client)
}

// ============================================================================
// 客户端状态管理
// ============================================================================

// ResetClientStatus 重置客户端状态
func (h *Hub) ResetClientStatus(clientID string, status UserStatus) error {
	client, exists := h.GetClientByIDWithLock(clientID)

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	client.Status = status
	return nil
}

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
// 配置管理
// ============================================================================

// SetHeartbeatConfig 设置心跳配置
// interval: 心跳间隔，建议30秒
// timeout: 心跳超时时间，建议90秒（interval的3倍）
func (h *Hub) SetHeartbeatConfig(interval, timeout time.Duration) {
	h.config.HeartbeatInterval = interval
	h.config.ClientTimeout = timeout
}

// ============================================================================
// 消息队列
// ============================================================================

// GetMessageQueue 获取消息队列长度
func (h *Hub) GetMessageQueue() int {
	return len(h.pendingMessages)
}
