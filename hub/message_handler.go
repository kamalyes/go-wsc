/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-06 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-06 21:30:00
 * @FilePath: \go-wsc\hub\message_handler.go
 * @Description: Hub 消息处理逻辑
 *   - 客户端读写循环（handleClientRead/handleClientWrite）
 *   - 文本/二进制消息处理
 *   - 可转发消息自动转发
 *   - 消息字段规范化
 *   - 消息接收/错误回调触发
 *   - 心跳检查
 *   - 广播/点对点消息分发
 *
 * 从 utils.go 拆分而来，职责单一：所有消息处理相关逻辑
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"runtime/debug"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/protocol"
)

// ============================================================================
// 客户端读写处理
// ============================================================================

// handleClientWrite 处理客户端消息写入
func (h *Hub) handleClientWrite(client *Client) {
	h.wg.Add(1)
	defer h.wg.Done()
	defer func() {
		h.logWithClient(logger.INFO, "客户端写入协程结束", client)
	}()

	h.logWithClient(logger.INFO, "客户端写入协程启动", client)

	for {
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				h.logWithClient(logger.INFO, "客户端发送通道关闭", client)
				return
			}

			if client.Conn != nil {
				client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
					h.logWithClient(logger.ERROR, "客户端消息写入失败", client, "error", err)
					// 主动关闭连接，让读 goroutine 的 ReadMessage 立即报错退出
					// 否则读 goroutine 会卡在 IO wait 直到 TCP keepalive 超时，造成半死连接泄漏
					// （读 goroutine 退出后会触发 defer Unregister 完成清理）
					_ = client.Conn.Close()
					return
				}
			}
		case <-h.ctx.Done():
			h.logWithClient(logger.INFO, "客户端写入协程因Hub关闭而结束", client)
			return
		}
	}
}

// handleClientRead 处理客户端消息读取
func (h *Hub) handleClientRead(client *Client) {
	h.wg.Add(1)
	defer h.wg.Done()
	defer h.Unregister(client)
	defer func() {
		h.logWithClient(logger.INFO, "客户端读取协程结束", client)
	}()

	h.logWithClient(logger.INFO, "客户端读取协程启动", client)

	// 使用 client.Context（从 Hub 生命周期 h.ctx 派生的连接级 ctx，Hub 关闭时自动取消）
	reqCtx := client.Context

	for {
		messageType, data, err := client.Conn.ReadMessage()
		if err != nil {
			// Hub 正在关闭（SafeShutdown 触发），连接是被服务端主动关闭的
			// 此时读循环会拿到 "use of closed network connection"，走 ClassifyCloseError
			// 会被误判为 1006 异常断开，所以这里短路掉，单独记一条 INFO 日志
			if h.shutdown.Load() {
				h.logWithClient(logger.INFO, "服务关闭，断开客户端连接", client, "error", err.Error())
				return
			}

			// 🔍 识别断开类型和原因
			errStr := err.Error()
			closeCode, isNormal := ClassifyCloseError(err)

			// 获取关闭码描述
			codeDesc := "未知错误"
			if info, exists := WsCloseCodeMap[closeCode]; exists {
				codeDesc = info.Desc
			}

			// 根据错误类型记录不同级别的日志
			if isNormal {
				h.logWithClient(logger.INFO, "客户端正常断开", client, "close_code", closeCode, "code_desc", codeDesc)
			} else {
				// 异常断开 - 记录详细信息用于排查
				h.logWithClient(logger.WARN, "客户端异常断开", client, "close_code", closeCode, "code_desc", codeDesc, "error", errStr)
				// 记录错误到连接记录
				h.trackConnectionError(client.ID, client.UserType, err)
			}
			return
		}

		client.SetLastSeen(time.Now())

		switch messageType {
		case websocket.TextMessage:
			h.handleTextMessage(reqCtx, client, data)
		case websocket.BinaryMessage:
			h.handleBinaryMessage(client, data)
		case websocket.CloseMessage:
			return
		case websocket.PingMessage:
			// 设置写超时，避免恶意客户端通过频繁 Ping 阻塞写 goroutine
			_ = client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_ = client.Conn.WriteMessage(websocket.PongMessage, nil)
		}
	}
}

// handleTextMessage 处理文本消息
func (h *Hub) handleTextMessage(ctx context.Context, client *Client, data []byte) {
	var msg *HubMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		msg = NewHubMessage().
			SetSender(client.UserID).
			SetSenderType(client.UserType).
			SetContent(string(data)).
			SetMessageType(MessageTypeText)
	}

	// 规范化消息字段
	h.normalizeMessageFields(client, msg)

	// 根据消息类型进行特殊处理
	switch msg.MessageType {
	case models.MessageTypePing, models.MessageTypeHeartbeat:
		// 处理心跳/Ping消息
		h.handleHeartbeatMessage(client)
		return
	case models.MessageTypeAck:
		// ACK消息由AckManager处理
		if h.config.EnableAck && h.ackManager != nil {
			ackMsg := &protocol.AckMessage{
				MessageID: msg.MessageID,
				Status:    protocol.AckStatusConfirmed,
				Timestamp: time.Now(),
			}
			h.ackManager.ConfirmMessage(msg.MessageID, ackMsg)
		}
		return
	}

	// 🔄 自动转发可转发类型的消息（异步执行，避免阻塞）
	if models.MessageType(msg.MessageType).IsForwardableType() {
		syncx.Go().
			WithTimeout(5 * time.Second).
			OnPanic(func(r interface{}) {
				h.logger.ErrorContextKV(ctx, "转发消息panic", "panic", r, "stack", string(debug.Stack()), "message_id", msg.MessageID)
			}).
			ExecWithContext(func(ctx context.Context) error {
				return h.handleForwardableMessage(ctx, msg)
			})
		return
	}

	// 调用消息接收回调（其他类型消息交给业务层处理）
	if err := h.InvokeMessageReceivedCallback(ctx, client, msg); err != nil {
		h.logger.WarnContextKV(ctx, "消息接收回调执行失败",
			"client_id", client.ID,
			"error", err,
		)
	}
}

// handleForwardableMessage 处理可转发类型的消息（窗口消息、状态消息等）
// 这些消息无需业务层处理，框架自动转发
func (h *Hub) handleForwardableMessage(ctx context.Context, msg *HubMessage) error {
	emoji := msg.MessageType.GetEmoji()

	h.logger.DebugKV(emoji+" 自动转发消息",
		"message_type", msg.MessageType,
		"from", msg.Sender,
		"to", msg.Receiver,
		"message_id", msg.MessageID,
	)

	// 检查接收者是否指定
	if msg.Receiver == "" {
		h.logger.WarnKV("可转发消息缺少接收者",
			"message_type", msg.MessageType,
			"sender", msg.Sender,
		)
		return nil
	}

	// 使用 SendToUserWithRetry 自动转发消息
	ctx = context.WithValue(ctx, ContextKeySenderID, msg.Sender)
	result := h.SendToUserWithRetry(ctx, msg.Receiver, msg)

	if !result.Success {
		h.logger.ErrorKV(emoji+" 转发失败", "from", msg.Sender, "to", msg.Receiver, "error", result.FinalError)
		return result.FinalError
	}
	return nil
}

// handleBinaryMessage 处理二进制消息
func (h *Hub) handleBinaryMessage(client *Client, data []byte) {
	h.logger.DebugKV("收到二进制消息",
		"client_id", client.ID,
		"size", len(data),
	)
}

// ============================================================================
// 回调触发方法
// ============================================================================

// InvokeMessageReceivedCallback 触发消息接收回调
func (h *Hub) InvokeMessageReceivedCallback(ctx context.Context, client *Client, msg *HubMessage) error {
	if h.messageReceivedCallback == nil {
		return nil
	}

	// 规范化消息字段（补充发送者信息等）
	h.normalizeMessageFields(client, msg)

	return h.messageReceivedCallback(ctx, client, msg)
}

// InvokeErrorCallback 触发错误处理回调
// 此方法用于统一处理各种错误
func (h *Hub) InvokeErrorCallback(ctx context.Context, err error, severity ErrorSeverity) error {
	if h.errorCallback == nil {
		return nil
	}
	return h.errorCallback(ctx, err, severity)
}

// ============================================================================
// 消息字段规范化
// ============================================================================

// normalizeMessageFields 规范化消息字段（补充缺失的字段）
func (h *Hub) normalizeMessageFields(client *Client, msg *HubMessage) {
	msg.Sender = mathx.IfEmpty(msg.Sender, client.UserID)
	msg.SenderType = mathx.IfEmpty(msg.SenderType, client.UserType)
	// 🔥 设置发送者客户端ID，用于多端同步时排除当前设备
	msg.SenderClient = mathx.IfEmpty(msg.SenderClient, client.ID)
	msg.CreateAt = mathx.IF(msg.CreateAt.IsZero(), time.Now(), msg.CreateAt)
	msg.MessageType = mathx.IfEmpty(msg.MessageType, MessageTypeText)
	// 仅在 ID 为空时才生成雪花ID，避免每条消息都调用 idGenerator（热路径 CPU 浪费）
	if msg.ID == "" {
		snowflakeId := h.idGenerator.GenerateRequestID()
		msg.ID = client.UserID + "-" + snowflakeId
	}
}

// ============================================================================
// 心跳检查
// ============================================================================

// checkHeartbeat 检查客户端心跳
// 使用 ForEachClient 零拷贝遍历 + 原子读时间戳（替代 GetClientsCopy 全量拷贝 + Hub.mutex 误用）
//
// ⚠️ 死锁防御：ForEachClient 持有 shard 读锁，若在 callback 中直接调用 Unregister，
// 当 unregister channel 满时会走 default 同步分支 → handleUnregister → RemoveClient → WithShardLock，
// 同一 shard 持读锁等写锁 → 死锁
// 修复：先收集超时客户端到本地 slice，遍历结束后在锁外统一调用 Unregister
func (h *Hub) checkHeartbeat() {
	now := time.Now()

	// Phase 1：持读锁收集超时客户端（不调用任何会获取写锁的方法）
	type timeoutClient struct {
		client     *Client
		lastActive time.Time
	}
	var timeouts []timeoutClient

	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		// 原子读时间戳（并发安全，无数据竞争）
		lastActive := mathx.IF(client.ConnectionType == ConnectionTypeSSE, client.GetLastSeen(), client.GetLastHeartbeat())

		// 检查是否超时
		inactiveDuration := now.Sub(lastActive)
		if inactiveDuration > h.config.ClientTimeout {
			timeouts = append(timeouts, timeoutClient{client: client, lastActive: lastActive})
		}
		return true
	})

	// Phase 2：锁外批量注销（Unregister 内部走 channel 异步或 default 同步均安全）
	for _, tc := range timeouts {
		h.logger.DebugKV("❤️ 检测到心跳超时，注销客户端",
			"client_id", tc.client.ID,
			"user_id", tc.client.UserID,
			"user_type", tc.client.UserType,
			"connection_type", tc.client.ConnectionType,
			"last_active", tc.lastActive,
			"inactive_duration", now.Sub(tc.lastActive).String(),
			"timeout_threshold", h.config.ClientTimeout.String(),
		)

		h.Unregister(tc.client)

		if h.heartbeatTimeoutCallback != nil {
			h.heartbeatTimeoutCallback(tc.client.ID, tc.client.UserID, tc.lastActive)
		}
	}
}

// ============================================================================
// 广播处理
// ============================================================================

// handleBroadcast 处理广播消息
func (h *Hub) handleBroadcast(msg *HubMessage) {
	// 🔍 通知所有观察者（异步，不阻塞主流程）
	h.notifyObservers(h.ctx, msg, "", "")

	if msg.BroadcastType == BroadcastTypeGlobal {
		h.handleBroadcastMessage(h.ctx, msg)
		return
	}
	h.handleDirectMessage(h.ctx, msg)
}

// handleDirectMessage 处理点对点消息
//
// 性能：
//   - 指定 ReceiverClient 时走 GetClient O(1) 查找，避免遍历
//   - 未指定时走 ForEachUserClient 零拷贝遍历直接发送，避免 GetClientsCopyForUser 切片拷贝
//   - 消息预序列化一次，多设备复用
func (h *Hub) handleDirectMessage(ctx context.Context, msg *HubMessage) {
	// 预序列化一次（接收者多设备复用，消除循环内重复 Marshal）
	// 序列化失败时 data=nil，由 sendToClientSerialized 内部兜底
	data, _ := json.Marshal(msg)

	sent := 0
	if msg.ReceiverClient != "" {
		// 指定客户端：O(1) 查找，避免遍历用户所有设备
		if client, ok := h.shardedRegistry.GetClient(msg.ReceiverClient); ok {
			h.sendToClientSerialized(ctx, client, msg, data)
			sent = 1
		}
	} else {
		// 未指定客户端：零拷贝遍历用户所有设备直接发送
		h.shardedRegistry.ForEachUserClient(msg.Receiver, func(_ string, client *Client) bool {
			h.sendToClientSerialized(ctx, client, msg, data)
			sent++
			return true
		})
	}

	if sent > 0 {
		// 增加消息发送统计（原子计数器，由 flushStatsCounters 定时刷写到 Redis）
		if h.statsRepo != nil {
			h.msgSentCount.Add(1)
		}
	} else if h.SendToUserViaSSE(msg.Receiver, msg) {
		h.logger.DebugContextKV(ctx, "消息已通过SSE发送", "message_id", msg.MessageID)
	}

	// 🔥 多端同步：如果发送者有多个设备在线，同步给发送者的其他设备（排除当前发送设备）
	if msg.Sender != "" && msg.SenderClient != "" {
		h.syncToSenderDevices(ctx, msg)
	}
}

// handleBroadcastMessage 处理广播消息
func (h *Hub) handleBroadcastMessage(ctx context.Context, msg *HubMessage) {
	if h.statsRepo != nil {
		h.broadcastSentCount.Add(1)
	}

	// 预序列化消息（仅一次）
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "广播消息序列化失败", "error", err)
		return
	}

	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	dataLen := len(data)

	// 遍历所有客户端，避免拷贝整个 clients 列表
	successCount := 0
	failCount := 0

	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		if client.IsClosed() || client.ConnectionType == ConnectionTypeSSE {
			return true
		}
		if client.TrySend(data) {
			successCount++
			h.trackReceiverMessageStats(client.ID, client.UserType, dataLen)
		} else {
			failCount++
		}
		return true
	})

	// 消息记录状态只更新一次（同一 msgID，无需每客户端都更新）
	if successCount > 0 {
		h.updateMessageStatusAsync(msgID, MessageSendStatusSuccess, "", "")
	}

	if failCount > 0 {
		h.logger.WarnContextKV(ctx, "广播消息：部分客户端发送失败",
			"success_count", successCount,
			"fail_count", failCount,
			"message_id", msg.MessageID,
		)
	}

	// SSE 客户端通过专用通道发送
	h.broadcastToSSEClients(msg)
}
