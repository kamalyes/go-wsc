/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 01:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-30 11:20:15
 * @FilePath: \go-wsc\hub\heartbeat.go
 * @Description: Hub 心跳处理
 *   - PONG 响应发送
 *   - 心跳消息处理流程（前置回调 → 更新 → Redis 同步 → PONG → 统计 → 后置回调）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"encoding/json"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
)

// sendPongResponse 发送 pong 响应（心跳热路径专用）
// 接收已获取的客户端对象，避免在发送时重新查询 shardedRegistry 导致的竞态条件与冗余开销
func (h *Hub) sendPongResponse(client *Client, now time.Time) error {
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
		CreateAt:     now,
		Priority:     PriorityNormal,
	}

	// 序列化消息
	data, err := json.Marshal(pongMsg)
	if err != nil {
		return errorx.WrapError("failed to marshal pong message", err)
	}

	// 优先使用非阻塞发送
	if client.TrySend(data) {
		client.SetLastPong(now) // 直接更新，避免 UpdatePongTime 的冗余 GetClient 查询
		return nil
	}

	// 非阻塞发送失败（通道满或客户端刚注册写协程尚未就绪），
	// 使用带超时的阻塞发送重试，避免 pong 响应被静默丢弃
	client.CloseMu.Lock()
	defer client.CloseMu.Unlock()

	if client.IsClosed() || client.SendChan == nil {
		return errorx.WrapError("client is closed or send channel is nil")
	}

	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()

	select {
	case client.SendChan <- data:
		client.SetLastPong(now) // 直接更新
		return nil
	case <-timer.C:
		h.logger.WarnKV("心跳 pong 响应发送超时",
			"client_id", client.ID,
			"user_id", client.UserID,
		)
		return errorx.WrapError("pong send timeout, client send channel may be full")
	}
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

	// 更新心跳请求时间（内存）- 收到PING时直接更新 client 字段，避免 shardedRegistry 冗余查询
	now := time.Now()
	client.SetLastHeartbeat(now)
	client.SetLastSeen(now)

	// ⏰ 刷新时间轮心跳超时（O(1) 操作，取消旧任务 + 调度新任务）
	h.refreshHeartbeatTimeout(client)

	// 💓 记录心跳日志
	h.logWithClient(logger.DEBUG, "💓 收到心跳消息", client)

	// 异步重建 Redis 在线索引与跨节点路由（不阻塞心跳主流程）
	// 使用单 goroutine worker 消费 channel，替代每次心跳创建独立 goroutine
	// 投递 *Client：worker 直接调用 SetClientOnline 无条件刷新 ZSET 分数，
	// 即使 Redis 中 client:<id> 键已过期/被淘汰，也能基于内存客户端重建索引
	if h.onlineStatusRepo != nil {
		select {
		case h.heartbeatRedisCh <- client:
		default:
			// channel 满，跳过本次 Redis 更新（心跳下次还会来）
		}
	}

	// 直接发送 pong 响应（使用已获取的客户端对象，避免竞态条件）
	if err := h.sendPongResponse(client, now); err != nil {
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

// ============================================================================
// 时间轮心跳超时管理（替代 O(N) 全量扫描）
// ============================================================================

// scheduleHeartbeatTimeout 在时间轮上调度客户端心跳超时任务
// 仅用于 WebSocket 客户端；SSE 客户端由 checkHeartbeat 扫描兜底
func (h *Hub) scheduleHeartbeatTimeout(client *Client) {
	if h.heartbeatTimer == nil || client.ConnectionType == ConnectionTypeSSE {
		return
	}
	h.heartbeatTimer.ScheduleWithKey(client.ID, h.config.ClientTimeout, h.makeHeartbeatTimeoutCallback(client))
}

// refreshHeartbeatTimeout 刷新客户端心跳超时（O(1) 操作，取消旧 + 调度新）
// 收到 PING 或任何消息时调用
func (h *Hub) refreshHeartbeatTimeout(client *Client) {
	if h.heartbeatTimer == nil || client.ConnectionType == ConnectionTypeSSE {
		return
	}
	h.heartbeatTimer.Refresh(client.ID, h.config.ClientTimeout, h.makeHeartbeatTimeoutCallback(client))
}

// cancelHeartbeatTimeout 取消客户端心跳超任务（注销时调用）
func (h *Hub) cancelHeartbeatTimeout(clientID string) {
	if h.heartbeatTimer == nil {
		return
	}
	h.heartbeatTimer.CancelByKey(clientID)
}

// makeHeartbeatTimeoutCallback 创建心跳超时回调
// 超时触发时注销客户端并通知 heartbeatTimeoutCallback
func (h *Hub) makeHeartbeatTimeoutCallback(client *Client) func() {
	return func() {
		// 客户端已关闭（正常断开），跳过
		if client.IsClosed() {
			return
		}
		// 触发心跳超时回调
		if h.heartbeatTimeoutCallback != nil {
			h.heartbeatTimeoutCallback(client.ID, client.UserID, client.GetLastHeartbeat())
		}
		// 异步注销客户端
		h.Unregister(client)
	}
}
