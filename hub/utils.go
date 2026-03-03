/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 20:55:15
 * @FilePath: \go-wsc\hub\utils.go
 * @Description: Hub 工具辅助方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/protocol"
)

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

// ============================================================================
// 客户端管理辅助方法
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
			// 在异步任务中读取最新连接数
			count := syncx.WithRLockReturnValue(&h.mutex, func() int {
				return len(h.clients)
			})
			_ = h.statsRepo.UpdateConnectionStats(ctx, h.nodeID, int64(count))
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
		"活跃连接数": len(h.clients),
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

// CreateConnectionRecord 从 Client 创建连接记录
func (h *Hub) CreateConnectionRecord(client *Client) *ConnectionRecord {
	record := &ConnectionRecord{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		NodeID:       client.NodeID,
		NodeIP:       client.NodeIP,
		NodePort:     client.NodePort,
		ClientIP:     client.GetClientIP(),
		LastPingAt:   &client.LastHeartbeat,
		LastPongAt:   &client.LastPong,
		Protocol:     client.ConnectionType,
		ClientType:   client.ClientType,
		ConnectedAt:  client.ConnectedAt,
		IsActive:     true,
	}

	// 设置 metadata
	record.Metadata = sqlbuilder.MapAny(client.Metadata)

	return record
}

// saveConnectionRecord 保存或更新连接记录到数据库
func (h *Hub) saveConnectionRecord(record *ConnectionRecord) {
	if h.connectionRecordRepo == nil {
		return
	}

	syncx.Go().
		WithTimeout(10 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("保存连接记录崩溃", "panic", r, "user_id", record.UserID)
		}).
		OnError(func(err error) {
			h.logger.ErrorKV("保存连接记录失败",
				"user_id", record.UserID,
				"connection_id", record.ConnectionID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionRecordRepo.Upsert(ctx, record)
		})
}

// updateConnectionOnDisconnect 更新连接断开信息
func (h *Hub) updateConnectionOnDisconnect(client *Client, reason DisconnectReason) {
	if h.connectionRecordRepo == nil {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("更新连接断开记录崩溃", "panic", r, "user_id", client.UserID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionRecordRepo.MarkDisconnected(ctx, client.ID, reason, 1000, string(reason))
		})
}

// sendWelcomeMessage 发送欢迎消息
func (h *Hub) sendWelcomeMessage(client *Client) {
	provider := h.welcomeProvider
	if provider == nil {
		return
	}

	extraData := map[string]interface{}{
		"client_id": client.ID,
		"node_id":   h.nodeID,
		"time":      time.Now().Format(time.DateTime),
	}

	welcomeMsg, enabled, err := provider.GetWelcomeMessage(
		client.UserID,
		client.Role,
		client.UserType,
		extraData,
	)

	if err != nil || !enabled || welcomeMsg == nil {
		return
	}

	msg := &HubMessage{
		MessageType: MessageTypeWelcome,
		Sender:      UserTypeSystem.String(),
		Receiver:    client.UserID,
		Content:     welcomeMsg.Content,
		Data:        welcomeMsg.Data,
		CreateAt:    time.Now(),
		Priority:    welcomeMsg.Priority,
	}

	if msg.Data == nil {
		msg.Data = make(map[string]interface{})
	}
	msg.Data["title"] = welcomeMsg.Title

	h.sendToClient(client, msg)
}

// pushOfflineMessagesOnConnect 客户端连接时推送离线消息
func (h *Hub) pushOfflineMessagesOnConnect(client *Client) {
	if h.offlineMessageHandler == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 获取离线消息数量
	totalCount, err := h.offlineMessageHandler.GetOfflineMessageCount(ctx, client.UserID)
	if err != nil {
		h.logger.ErrorKV("获取离线消息数量失败",
			"user_id", client.UserID,
			"error", err,
		)
		return
	}

	if totalCount == 0 {
		h.logger.DebugKV("用户无离线消息", "user_id", client.UserID)
		return
	}

	h.logger.InfoKV("开始推送离线消息",
		"user_id", client.UserID,
		"total_count", totalCount,
	)

	const batchSize = 100
	totalSuccess := 0
	totalFailed := 0
	allFailedMessageIDs := make([]string, 0)
	cursor := ""

	// 分批获取并推送所有离线消息
	for {
		messages, nextCursor, err := h.offlineMessageHandler.GetOfflineMessages(ctx, client.UserID, batchSize, cursor)
		if err != nil {
			h.logger.ErrorKV("获取离线消息失败",
				"user_id", client.UserID,
				"cursor", cursor,
				"error", err,
			)
			break
		}

		if len(messages) == 0 {
			break
		}

		// 推送这批消息
		pushedMessageIDs := make([]string, 0, len(messages))
		failedMessages := make(map[string]error) // 记录失败消息和具体错误

		for _, message := range messages {
			// 标记为离线消息来源
			message.Source = models.MessageSourceOffline
			if message.Data == nil {
				message.Data = make(map[string]interface{})
			}
			message.Data["offline"] = true

			if err := h.sendToUser(ctx, client.UserID, message); err != nil {
				h.logger.ErrorKV("离线消息推送失败",
					"user_id", client.UserID,
					"message_id", message.MessageID,
					"error", err,
				)
				failedMessages[message.MessageID] = err
			} else {
				pushedMessageIDs = append(pushedMessageIDs, message.MessageID)
			}
		}

		totalSuccess += len(pushedMessageIDs)
		totalFailed += len(failedMessages)

		// 更新离线消息推送状态
		if h.offlineMessageHandler != nil {
			// 推送成功的消息：直接删除（Redis已Dequeue，MySQL也删除）
			if len(pushedMessageIDs) > 0 {
				if err := h.offlineMessageHandler.DeleteOfflineMessages(ctx, client.UserID, pushedMessageIDs); err != nil {
					h.logger.ErrorKV("删除已推送的离线消息失败",
						"user_id", client.UserID,
						"count", len(pushedMessageIDs),
						"error", err,
					)
				} else {
					h.logger.DebugKV("删除已推送的离线消息",
						"user_id", client.UserID,
						"count", len(pushedMessageIDs),
					)
				}
			}

			// 推送失败的消息 - 逐条更新状态以记录具体错误（保留以便重试）
			for msgID, pushErr := range failedMessages {
				allFailedMessageIDs = append(allFailedMessageIDs, msgID)
				if err := h.offlineMessageHandler.UpdatePushStatus(ctx, []string{msgID}, pushErr); err != nil {
					h.logger.ErrorKV("更新离线消息推送失败状态失败",
						"user_id", client.UserID,
						"message_id", msgID,
						"error", err,
					)
				}
			}
		}

		// 更新游标
		cursor = nextCursor

		// 如果 nextCursor 为空，说明没有更多数据了
		if nextCursor == "" {
			break
		}
	}

	h.logger.InfoKV("离线消息推送完成",
		"user_id", client.UserID,
		"success", totalSuccess,
		"failed", totalFailed,
	)

	// 调用回调通知上游
	if h.offlineMessagePushCallback != nil && totalSuccess > 0 {
		allPushedIDs := make([]string, 0, totalSuccess)
		// 这里简化处理，实际应该收集所有成功的ID
		h.offlineMessagePushCallback(client.UserID, allPushedIDs, allFailedMessageIDs)
	}
}

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

	for {
		messageType, data, err := client.Conn.ReadMessage()
		if err != nil {
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

		client.LastSeen = time.Now()

		switch messageType {
		case websocket.TextMessage:
			h.handleTextMessage(client, data)
		case websocket.BinaryMessage:
			h.handleBinaryMessage(client, data)
		case websocket.CloseMessage:
			return
		case websocket.PingMessage:
			_ = client.Conn.WriteMessage(websocket.PongMessage, nil)
		}
	}
}

// handleTextMessage 处理文本消息
func (h *Hub) handleTextMessage(client *Client, data []byte) {
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
				h.logger.ErrorKV("转发消息panic", "panic", r, "message_id", msg.MessageID)
			}).
			ExecWithContext(func(ctx context.Context) error {
				return h.handleForwardableMessage(ctx, msg)
			})
		return
	}

	// 调用消息接收回调（其他类型消息交给业务层处理）
	ctx := context.Background()
	if err := h.InvokeMessageReceivedCallback(ctx, client, msg); err != nil {
		h.logger.WarnKV("消息接收回调执行失败",
			"client_id", client.ID,
			"error", err,
		)
	}
}

// handleForwardableMessage 处理可转发类型的消息（窗口消息、状态消息等）
// 这些消息无需业务层处理，框架自动转发
func (h *Hub) handleForwardableMessage(ctx context.Context, msg *HubMessage) error {
	emoji := msg.MessageType.GetEmoji()

	h.logger.DebugKV(fmt.Sprintf("%s 自动转发消息", emoji),
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
		h.logger.ErrorKV(fmt.Sprintf("%s 转发失败", emoji), "from", msg.Sender, "to", msg.Receiver, "error", result.FinalError)
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
// 连接统计辅助方法
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
func (h *Hub) trackHeartbeatStats(client *Client) {
	if h.connectionRecordRepo == nil || client == nil {
		return
	}

	// 排除系统、机器人、观察者
	if !h.shouldTrackUserStats(client.UserType) {
		return
	}

	now := time.Now()
	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r any) {
			h.logger.ErrorKV("更新心跳统计崩溃", "panic", r, "user_id", client.UserID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			if err := h.connectionRecordRepo.UpdateHeartbeat(ctx, client.ID, &client.LastHeartbeat, &client.LastPong); err != nil {
				h.logger.DebugKV("更新连接记录心跳失败", "user_id", client.UserID, "error", err)
			}

			// 计算Ping延迟（如果客户端有上次心跳时间）
			if !client.LastHeartbeat.IsZero() {
				pingMs := float64(now.Sub(client.LastHeartbeat).Milliseconds())
				if err := h.connectionRecordRepo.UpdatePingStats(ctx, client.ID, pingMs); err != nil {
					h.logger.DebugKV("更新Ping统计失败", "user_id", client.UserID, "error", err)
				}
			}
			return nil
		})
}

// normalizeMessageFields 规范化消息字段（补充缺失的字段）
func (h *Hub) normalizeMessageFields(client *Client, msg *HubMessage) {
	msg.Sender = mathx.IfEmpty(msg.Sender, client.UserID)
	msg.SenderType = mathx.IfEmpty(msg.SenderType, client.UserType)
	// 🔥 设置发送者客户端ID，用于多端同步时排除当前设备
	msg.SenderClient = mathx.IfEmpty(msg.SenderClient, client.ID)
	msg.CreateAt = mathx.IF(msg.CreateAt.IsZero(), time.Now(), msg.CreateAt)
	msg.MessageType = mathx.IfEmpty(msg.MessageType, MessageTypeText)
	snowflakeId := h.idGenerator.GenerateRequestID()
	msg.ID = mathx.IfNotEmpty(msg.ID, fmt.Sprintf("%s-%s", client.UserID, snowflakeId))
}

// ============================================================================
// 心跳检查
// ============================================================================

// checkHeartbeat 检查客户端心跳
func (h *Hub) checkHeartbeat() {
	allClients := h.GetClientsCopy()
	now := time.Now()
	for _, client := range allClients {
		// 加锁读取时间戳以避免数据竞争
		h.mutex.RLock()
		// SSE 客户端使用 LastSeen，WebSocket 使用 LastHeartbeat
		lastActive := mathx.IF(client.ConnectionType == ConnectionTypeSSE, client.LastSeen, client.LastHeartbeat)
		h.mutex.RUnlock()

		// 检查是否超时
		inactiveDuration := now.Sub(lastActive)
		if inactiveDuration > h.config.ClientTimeout {
			h.logger.Debug("❤️ 检测到心跳超时，注销客户端",
				"client_id", client.ID,
				"user_id", client.UserID,
				"user_type", client.UserType,
				"connection_type", client.ConnectionType,
				"last_active", lastActive,
				"inactive_duration", inactiveDuration.String(),
				"timeout_threshold", h.config.ClientTimeout.String(),
			)

			h.Unregister(client)

			if h.heartbeatTimeoutCallback != nil {
				h.heartbeatTimeoutCallback(client.ID, client.UserID, lastActive)
			}
		}
	}
}

// ============================================================================
// 广播处理
// ============================================================================

// handleBroadcast 处理广播消息
func (h *Hub) handleBroadcast(msg *HubMessage) {
	// 🔍 通知所有观察者（异步，不阻塞主流程）
	h.notifyObservers(msg)

	if msg.BroadcastType == BroadcastTypeGlobal {
		h.handleBroadcastMessage(msg)
		return
	}
	h.handleDirectMessage(msg)
}

// handleDirectMessage 处理点对点消息
func (h *Hub) handleDirectMessage(msg *HubMessage) {
	// 在锁内复制客户端列表，避免竞争
	receiverClients := h.GetClientsCopyForUser(msg.Receiver, msg.ReceiverClient)

	if len(receiverClients) > 0 {
		// 增加消息发送统计（异步更新，避免阻塞消息发送）
		if h.statsRepo != nil {
			syncx.Go().
				WithTimeout(1 * time.Second).
				OnError(func(err error) {
					h.logger.ErrorKV("更新消息统计失败", "error", err, "node_id", h.nodeID)
				}).
				ExecWithContext(func(ctx context.Context) error {
					return h.statsRepo.IncrementMessagesSent(ctx, h.nodeID, 1)
				})
		}

		// 发送给接收者的所有客户端
		for _, receiverClient := range receiverClients {
			h.sendToClient(receiverClient, msg)
		}
	} else if h.SendToUserViaSSE(msg.Receiver, msg) {
		h.logger.DebugKV("消息已通过SSE发送", "message_id", msg.MessageID)
	}

	// 🔥 多端同步：如果发送者有多个设备在线，同步给发送者的其他设备（排除当前发送设备）
	if msg.Sender != "" && msg.SenderClient != "" {
		h.syncToSenderDevices(msg)
	}
}

// handleBroadcastMessage 处理广播消息
func (h *Hub) handleBroadcastMessage(msg *HubMessage) {
	if h.statsRepo != nil {
		syncx.Go().
			WithTimeout(1 * time.Second).
			OnError(func(err error) {
				h.logger.ErrorKV("更新广播统计失败", "error", err, "node_id", h.nodeID)
			}).
			ExecWithContext(func(ctx context.Context) error {
				return h.statsRepo.IncrementBroadcastsSent(ctx, h.nodeID, 1)
			})
	}

	clients := h.GetClientsCopy()
	for _, client := range clients {
		h.sendToClient(client, msg)
	}

	h.broadcastToSSEClients(msg)
}

// GetClientsCopy 获取所有客户端的副本
func (h *Hub) GetClientsCopy() []*Client {
	return syncx.WithRLockReturnValue(&h.mutex, func() []*Client {
		return CopyClientsFromMap(h.clients)
	})
}

// GetUserClientsCopy 获取每个用户最活跃的客户端副本列表
func (h *Hub) GetUserClientsCopy() []*Client {
	return syncx.WithRLockReturnValue(&h.mutex, func() []*Client {
		clients := make([]*Client, 0, len(h.userToClients))
		for _, clientMap := range h.userToClients {
			if len(clientMap) == 0 {
				continue
			}
			// 找到最近活跃的客户端
			var mostRecent *Client
			for _, client := range clientMap {
				if mostRecent == nil || client.LastSeen.After(mostRecent.LastSeen) {
					mostRecent = client
				}
			}
			if mostRecent != nil {
				clients = append(clients, mostRecent)
			}
		}
		return clients
	})
}

// GetUserClientsMapWithLock 获取指定用户的所有客户端映射(带锁)
func (h *Hub) GetUserClientsMapWithLock(userID string) (map[string]*Client, bool) {
	return syncx.WithRLockReturnWithE(&h.mutex, func() (map[string]*Client, bool) {
		clientMap, exists := h.userToClients[userID]
		return clientMap, exists
	})
}

// GetClientsCopyForUser 获取用户的客户端列表副本（带锁，线程安全）
// 如果指定了 clientID，只返回该客户端；否则返回用户的所有客户端
func (h *Hub) GetClientsCopyForUser(userID, clientID string) []*Client {
	return syncx.WithRLockReturnValue(&h.mutex, func() []*Client {
		clientMap, exists := h.userToClients[userID]
		if !exists || len(clientMap) == 0 {
			return nil
		}

		// 如果指定了客户端ID，只返回该客户端
		if clientID != "" {
			if targetClient, ok := clientMap[clientID]; ok {
				return []*Client{targetClient}
			}
			return nil
		}

		// 返回所有客户端的副本
		return CopyClientsFromMap(clientMap)
	})
}

// SendToAllClientsInMap 发送消息到映射中的所有客户端
func (h *Hub) SendToAllClientsInMap(clientMap map[string]*Client, msg *HubMessage) {
	// 复制客户端列表,避免在遍历时map被修改导致竞争
	clients := CopyClientsFromMap(clientMap)

	// 遍历复制后的列表发送消息
	for _, client := range clients {
		h.sendToClient(client, msg)
	}
}

// sendToClient 发送消息到客户端
func (h *Hub) sendToClient(client *Client, msg *HubMessage) {
	// 检查客户端是否已关闭
	if client.IsClosed() {
		return
	}

	// 🔥 如果 MessageID 为空，使用 HubID
	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)

	// SSE 客户端使用专用的消息通道
	if client.ConnectionType == ConnectionTypeSSE {
		if client.TrySendSSE(msg) {
			client.LastSeen = time.Now()
			h.logger.DebugKV("SSE消息发送", "message_id", msg.MessageID, "client_id", client.ID, "user_id", client.UserID)
			// SSE消息成功发送，更新为成功状态
			if h.messageRecordRepo != nil {
				go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
					return h.messageRecordRepo.UpdateStatus(ctx, msgID, MessageSendStatusSuccess, "", "")
				})
			}
		} else {
			h.logger.WarnKV("SSE客户端消息通道已满或已关闭", "client_id", client.ID, "user_id", client.UserID)
			// SSE通道已满或已关闭，更新为失败状态
			if h.messageRecordRepo != nil {
				go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
					return h.messageRecordRepo.UpdateStatus(ctx, msgID, MessageSendStatusFailed, FailureReasonQueueFull, "SSE channel full or closed")
				})
			}
		}
		return
	}

	// WebSocket 客户端使用原有逻辑
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorKV("消息序列化失败", "error", err)
		// 更新为失败状态
		if h.messageRecordRepo != nil {
			go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
				return h.messageRecordRepo.UpdateStatus(ctx, msgID, MessageSendStatusFailed, FailureReasonUnknown, err.Error())
			})
		}
		return
	}

	if client.TrySend(data) {
		// 消息成功发送到客户端通道，更新为成功状态
		if h.messageRecordRepo != nil {
			go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
				return h.messageRecordRepo.UpdateStatus(ctx, msgID, MessageSendStatusSuccess, "", "")
			})
		}

		// 更新接收者的消息统计和字节统计
		h.trackReceiverMessageStats(client.ID, client.UserType, len(data))
	} else {
		h.logger.WarnKV("客户端发送通道已满或已关闭", "client_id", client.ID)
		// 发送通道已满或已关闭，更新为失败状态
		if h.messageRecordRepo != nil {
			go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
				return h.messageRecordRepo.UpdateStatus(ctx, msgID, MessageSendStatusFailed, FailureReasonQueueFull, "client send channel full or closed")
			})
		}
	}
}

// ============================================================================
// 多端登录辅助方法
// ============================================================================

// addNewClient 添加新客户端（不加锁，需要外部加锁）
func (h *Hub) addNewClient(client *Client) {
	h.clients[client.ID] = client

	// 更新原子计数器
	if client.ConnectionType == ConnectionTypeSSE {
		h.sseClientsCount.Add(1)
	} else {
		h.activeClientsCount.Add(1)
	}

	if _, exists := h.userToClients[client.UserID]; !exists {
		h.userToClients[client.UserID] = make(map[string]*Client)
	}
	h.userToClients[client.UserID][client.ID] = client

	// SSE 客户端单独存储（支持多设备）
	if client.ConnectionType == ConnectionTypeSSE {
		h.sseMutex.Lock()
		if _, exists := h.sseClients[client.UserID]; !exists {
			h.sseClients[client.UserID] = make(map[string]*Client)
		}
		h.sseClients[client.UserID][client.ID] = client
		h.sseMutex.Unlock()
	}

	// 如果是观察者，添加到观察者映射 - O(1)
	if client.UserType == UserTypeObserver {
		h.addObserver(client)
	}

	// 如果是客服或机器人，添加到客服映射
	if client.UserType == UserTypeAgent || client.UserType == UserTypeBot {
		if _, exists := h.agentClients[client.UserID]; !exists {
			h.agentClients[client.UserID] = make(map[string]*Client)
		}
		h.agentClients[client.UserID][client.ID] = client
	}
}

// kickExistingClientsUnsafe 踢掉现有客户端（不加锁）
func (h *Hub) kickExistingClientsUnsafe(clients map[string]*Client, reason DisconnectReason) {
	for _, client := range clients {
		h.kickClientWithNotification(client, reason, "您的账号在其他设备登录，当前连接将被断开")

		h.logger.InfoKV("踢出旧连接",
			"user_id", client.UserID,
			"client_id", client.ID,
			"reason", reason,
		)
	}
}

// kickOldestConnection 踢掉最不活跃的连接（基于最后心跳时间）
// 优先踢掉长时间没有心跳的连接，保留活跃连接
func (h *Hub) kickOldestConnection(clients map[string]*Client) {
	var oldestClient *Client
	var oldestTime time.Time

	// 找出最久没有心跳的客户端
	for _, client := range clients {
		if oldestClient == nil || client.LastHeartbeat.Before(oldestTime) {
			oldestClient = client
			oldestTime = client.LastHeartbeat
		}
	}

	if oldestClient == nil {
		return
	}

	h.logger.InfoKV("踢掉最不活跃的连接",
		"client_id", oldestClient.ID,
		"user_id", oldestClient.UserID,
		"last_heartbeat", oldestClient.LastHeartbeat,
		"connected_at", oldestClient.ConnectedAt,
	)

	h.kickClientWithNotification(oldestClient, DisconnectReasonForceOffline, "连接数已达上限，当前连接将被断开")
}

// kickClientWithNotification 踢掉客户端并发送通知（公共方法）
func (h *Hub) kickClientWithNotification(client *Client, reason DisconnectReason, message string) {
	// 发送强制下线通知
	if client.Conn != nil {
		forceOfflineMsg := models.NewHubMessage().
			SetMessageType(models.MessageTypeForceOffline).
			SetSender("system").
			SetSenderType(models.UserTypeSystem).
			SetReceiver(client.UserID).
			SetReceiverType(client.UserType).
			SetContent(message)

		h.sendToClient(client, forceOfflineMsg)
		time.Sleep(100 * time.Millisecond) // 等待消息发送
	}
	h.Unregister(client)
}

// ============================================================================
// 踢人辅助方法
// ============================================================================

// GetConnectionsByUserID 获取用户的所有连接
func (h *Hub) GetConnectionsByUserID(userID string) []*Client {
	return syncx.WithRLockReturnValue(&h.mutex, func() []*Client {
		clientMap, exists := h.userToClients[userID]
		if !exists {
			return nil
		}
		return CopyClientsFromMap(clientMap)
	})
}

// createKickNotification 创建踢人通知消息
func (h *Hub) createKickNotification(userID, reason, customMsg string, kickedAt time.Time) *HubMessage {
	content := customMsg
	if content == "" {
		content = fmt.Sprintf("您已被踢出: %s", reason)
	}

	return &HubMessage{
		MessageType: MessageTypeKickOut,
		Sender:      "system",
		Receiver:    userID,
		Content:     content,
		CreateAt:    kickedAt,
		Data: map[string]interface{}{
			"reason":    reason,
			"kicked_at": kickedAt.Unix(),
		},
	}
}

// sendKickNotificationToClients 发送踢人通知到客户端
func (h *Hub) sendKickNotificationToClients(clients []*Client, msg *HubMessage) bool {
	if len(clients) == 0 {
		return false
	}

	for _, client := range clients {
		h.sendToClient(client, msg)
	}
	return true
}

// checkUserOnline 检查用户是否在线（支持分布式）
func (h *Hub) checkUserOnline(userID string) bool {
	// 1. 先检查本地是否在线
	h.mutex.RLock()
	_, existsLocal := h.userToClients[userID]
	h.mutex.RUnlock()

	if existsLocal {
		return true
	}

	// 2. 如果本地不在线，且启用了分布式，则查询Redis
	if h.onlineStatusRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		nodes, err := h.onlineStatusRepo.GetUserNodes(ctx, userID)
		if err == nil && len(nodes) > 0 {
			// 用户在其他节点在线
			return true
		}
	}

	// 3. 本地和Redis都没有，用户离线
	return false
}

// GetClientByIDWithLock 获取客户端(带锁,返回是否存在)
func (h *Hub) GetClientByIDWithLock(clientID string) (*Client, bool) {
	return syncx.WithRLockReturnWithE(&h.mutex, func() (*Client, bool) {
		client, exists := h.clients[clientID]
		return client, exists
	})
}

// CloseAllClientsInMap 关闭用户的所有客户端连接(并发)
func (h *Hub) CloseAllClientsInMap(clientMap map[string]*Client) {
	syncx.ParallelForEach(clientMap, func(_ string, client *Client) {
		if client.Conn != nil {
			client.Conn.Close()
		}
	})
}

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
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if client, exists := h.clients[clientID]; exists {
		client.LastHeartbeat = lastHeartbeat
		return true
	}
	return false
}

// ============================================================================
// Hub 状态查询方法
// ============================================================================

// GetHubHealth 获取Hub健康状态
func (h *Hub) GetHubHealth() *HubHealthInfo {
	wsCount := syncx.WithRLockReturnValue(&h.mutex, func() int {
		return len(h.clients)
	})

	return &HubHealthInfo{
		Status:           "healthy",
		IsRunning:        h.IsStarted(),
		WebSocketCount:   wsCount,
		SSECount:         0, // SSE 功能待实现
		TotalConnections: wsCount,
		NodeID:           h.nodeID,
	}
}

// GetOnlineUsersByType 按用户类型获取在线用户列表
func (h *Hub) GetOnlineUsersByType(userType UserType) ([]string, error) {
	clients := h.FilterClients(func(c *Client) bool {
		return c.UserType == userType
	})

	userIDs := make([]string, 0, len(clients))
	seen := make(map[string]bool)

	for _, client := range clients {
		if !seen[client.UserID] {
			userIDs = append(userIDs, client.UserID)
			seen[client.UserID] = true
		}
	}

	return userIDs, nil
}

// CopyClientsFromMap 从客户端映射中复制客户端列表
// 用于避免在遍历时map被修改导致的数据竞争
func CopyClientsFromMap(clientMap map[string]*Client) []*Client {
	clients := make([]*Client, 0, len(clientMap))
	for _, client := range clientMap {
		clients = append(clients, client)
	}
	return clients
}

// syncToSenderDevices 同步消息给发送者的其他设备（多端同步）
// 场景：用户A在设备B、C、D登录，设备B发送消息给用户F，设备C和D应该收到此消息
func (h *Hub) syncToSenderDevices(msg *HubMessage) {
	if msg.Sender == "" {
		return
	}

	// 获取发送者的所有在线设备
	senderClients := h.GetClientsCopyForUser(msg.Sender, "")
	if len(senderClients) <= 1 {
		// 只有一个设备或没有设备，无需同步
		return
	}

	// 过滤掉发送消息的设备（排除自己）
	otherDevices := mathx.FilterSlice(senderClients, func(client *Client) bool {
		return client.ID != msg.SenderClient
	})

	if len(otherDevices) == 0 {
		return
	}

	h.logger.DebugKV("🔄 多端同步消息给发送者的其他设备",
		"sender", msg.Sender,
		"sender_client", msg.SenderClient,
		"other_devices_count", len(otherDevices),
		"message_id", msg.MessageID,
	)

	// 发送给发送者的其他设备
	for _, device := range otherDevices {
		h.sendToClient(device, msg)
	}
}
