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
	"fmt"
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

// SendPongResponse 发送 pong 响应给客户端
func (h *Hub) SendPongResponse(clientID string) error {
	client, exists := h.GetClientByIDWithLock(clientID)

	if !exists {
		return errorx.WrapError(fmt.Sprintf("client not found: %s", clientID))
	}

	pongMsg := &HubMessage{
		ID:           fmt.Sprintf("pong_%s_%d", client.UserID, time.Now().UnixNano()),
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

	// 直接发送
	select {
	case client.SendChan <- data:
		return nil
	default:
		return errorx.WrapError("client send channel is full")
	}
}

// handleHeartbeatMessage 处理心跳消息
func (h *Hub) handleHeartbeatMessage(client *Client) {
	// 更新心跳时间（内存）
	h.UpdateHeartbeat(client.ID)

	// 💓 记录心跳日志
	h.logWithClient(logger.DEBUG, "💓 收到心跳消息", client)

	// 同步更新 Redis 中的在线状态和心跳时间
	if err := h.UpdateUserHeartbeat(client.UserID); err != nil {
		h.logger.DebugKV("更新 Redis 心跳失败",
			"client_id", client.ID,
			"user_id", client.UserID,
			"error", err,
		)
	}

	// 使用内部方法直接发送 pong 响应
	if err := h.SendPongResponse(client.ID); err != nil {
		h.logger.WarnKV("心跳 pong 响应发送失败",
			"client_id", client.ID,
			"user_id", client.UserID,
			"error", err,
		)
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
func (h *Hub) disconnectKickedClient(ctx context.Context, client *Client, userID, reason string) {
	// 调用断开回调
	if h.clientDisconnectCallback != nil {
		syncx.Go(h.ctx).
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
		"user_id", userID,
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
