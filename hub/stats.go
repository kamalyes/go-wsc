/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 20:55:15
 * @FilePath: \go-wsc\hub\stats.go
 * @Description: Hub 统计追踪方法
 *   - 客户端统计同步到 Redis
 *   - 连接日志记录
 *   - 在线状态同步
 *   - 消息/字节/心跳/错误统计追踪
 *
 * 从 utils.go 拆分而来，职责单一：所有与统计追踪相关的逻辑
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 客户端统计同步与日志
// ============================================================================

// syncClientStats 同步客户端统计信息到Redis（内部获取最新连接数）
func (h *Hub) syncClientStats() {
	if h.statsRepo == nil {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("同步客户端统计崩溃", "panic", r)
		}).
		ExecWithContext(func(ctx context.Context) error {
			_ = h.statsRepo.UpdateConnectionStats(ctx, h.nodeID, h.shardedRegistry.GetClientCount())
			return nil
		})
}

// logClientConnection 记录客户端连接日志
func (h *Hub) logClientConnection(client *Client) {
	cg := h.logger.NewConsoleGroup()
	cg.Group("👤 客户端连接成功 [%s]", client.UserID)

	clientInfo := map[string]interface{}{
		"客户端ID": client.ID,
		"用户ID":  client.UserID,
		"用户类型":  client.UserType,
		"客户端IP": client.ClientIP,
		"活跃连接数": h.shardedRegistry.GetClientCount(),
	}
	cg.Table(clientInfo)
	cg.GroupEnd()
}

// syncOnlineStatus 同步在线状态到 Redis
func (h *Hub) syncOnlineStatus(client *Client) {
	if h.onlineStatusRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	h.logger.DebugKV("开始同步在线状态到Redis",
		"user_id", client.UserID,
		"client_id", client.ID,
	)

	if err := h.onlineStatusRepo.SetClientOnline(ctx, client); err != nil {
		h.logger.ErrorKV("同步在线状态到Redis失败",
			"user_id", client.UserID,
			"error", err,
		)
	} else {
		h.logger.DebugKV("同步在线状态到Redis成功",
			"user_id", client.UserID,
			"client_id", client.ID,
		)
	}
}

// ============================================================================
// 连接统计追踪
// ============================================================================

// shouldTrackUserStats 判断是否应该追踪用户统计（排除系统、机器人、观察者）
func (h *Hub) shouldTrackUserStats(userType UserType) bool {
	return userType != UserTypeSystem &&
		userType != UserTypeBot &&
		userType != UserTypeObserver
}

// trackSenderMessageStats 追踪发送者的消息统计
func (h *Hub) trackSenderMessageStats(connectionID string, senderType UserType) {
	if h.connectionRecordRepo == nil || connectionID == "" {
		return
	}

	// 排除系统、机器人、观察者
	if !h.shouldTrackUserStats(senderType) {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r any) {
			h.logger.ErrorKV("更新发送统计崩溃", "panic", r, "connection_id", connectionID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			if err := h.connectionRecordRepo.IncrementMessageStats(ctx, connectionID, 1, 0); err != nil {
				h.logger.DebugKV("更新发送消息统计失败", "connection_id", connectionID, "error", err)
			}
			return nil
		})
}

// trackReceiverMessageStats 追踪接收者的消息和字节统计
func (h *Hub) trackReceiverMessageStats(connectionID string, receiverType UserType, dataSize int) {
	if h.connectionRecordRepo == nil || connectionID == "" {
		return
	}

	// 排除系统、机器人、观察者
	if !h.shouldTrackUserStats(receiverType) {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r any) {
			h.logger.ErrorKV("更新接收统计崩溃", "panic", r, "connection_id", connectionID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			// 增加接收消息计数
			if err := h.connectionRecordRepo.IncrementMessageStats(ctx, connectionID, 0, 1); err != nil {
				h.logger.DebugKV("更新接收消息统计失败", "connection_id", connectionID, "error", err)
			}
			// 增加接收字节数
			if err := h.connectionRecordRepo.IncrementBytesStats(ctx, connectionID, 0, int64(dataSize)); err != nil {
				h.logger.DebugKV("更新接收字节统计失败", "connection_id", connectionID, "error", err)
			}
			return nil
		})
}

// trackConnectionError 追踪连接错误
func (h *Hub) trackConnectionError(connectionID string, userType UserType, err error) {
	if h.connectionRecordRepo == nil || connectionID == "" || err == nil {
		return
	}

	// 排除系统、机器人、观察者
	if !h.shouldTrackUserStats(userType) {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r any) {
			h.logger.ErrorKV("记录连接错误崩溃", "panic", r, "connection_id", connectionID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionRecordRepo.AddError(ctx, connectionID, err)
		})
}

// trackHeartbeatStats 追踪心跳和Ping统计
// 优化：使用批量聚合器，避免每次心跳都启动 goroutine 写数据库
func (h *Hub) trackHeartbeatStats(client *Client) {
	if h.connectionRecordRepo == nil || client == nil {
		return
	}

	// 排除系统、机器人、观察者
	if !h.shouldTrackUserStats(client.UserType) {
		return
	}

	// 计算Ping延迟
	pingMs := float64(0)
	if !client.LastHeartbeat.IsZero() {
		pingMs = float64(time.Since(client.LastHeartbeat).Milliseconds())
	}

	// 使用批量聚合器，避免每次心跳都启动 goroutine
	if h.heartbeatBatcher != nil {
		h.heartbeatBatcher.Add(client.ID, client.LastHeartbeat, client.LastPong, pingMs, client.LastHeartbeat)
	}
}

// ============================================================================
// 日志辅助方法
// ============================================================================

// logWithClient 带客户端信息的日志记录辅助方法
func (h *Hub) logWithClient(level logger.LogLevel, msg string, client *Client, extraFields ...interface{}) {
	fields := []interface{}{
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"client_ip", client.ClientIP,
	}
	fields = append(fields, extraFields...)

	switch level {
	case logger.INFO:
		h.logger.InfoKV(msg, fields...)
	case logger.WARN:
		h.logger.WarnKV(msg, fields...)
	case logger.ERROR:
		h.logger.ErrorKV(msg, fields...)
	case logger.DEBUG:
		h.logger.DebugKV(msg, fields...)
	}
}
