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
	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"github.com/kamalyes/go-toolbox/pkg/metadata"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/protocol"
)

// ============================================================================
// 客户端管理辅助方法
// ============================================================================

// syncClientStats 同步客户端统计信息到Redis
func (h *Hub) syncClientStats(clientCount int) {
	if h.statsRepo == nil {
		return
	}

	syncx.Go(h.ctx).
		WithTimeout(2 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("同步客户端统计崩溃", "panic", r)
		}).
		ExecWithContext(func(ctx context.Context) error {
			_ = h.statsRepo.IncrementTotalConnections(ctx, h.nodeID, 1)
			_ = h.statsRepo.SetActiveConnections(ctx, h.nodeID, int64(clientCount))
			_ = h.statsRepo.UpdateNodeHeartbeat(ctx, h.nodeID)
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

	if err := h.onlineStatusRepo.SetOnline(ctx, client); err != nil {
		h.logger.ErrorKV("同步在线状态到Redis失败",
			"user_id", client.UserID,
			"error", err,
		)
	}
}

// CreateConnectionRecord 从 Client 创建连接记录
func (h *Hub) CreateConnectionRecord(client *Client) *ConnectionRecord {
	now := time.Now()

	record := &ConnectionRecord{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		NodeID:       h.GetNodeID(),
		ClientType:   string(client.ClientType),
		ConnectedAt:  now,
		IsActive:     true,
	}

	// 设置节点信息
	if h.config != nil {
		record.NodeIP = h.config.NodeIP
		record.NodePort = h.config.NodePort
	}

	// 从 Metadata 提取索引字段
	if client.Metadata != nil {
		meta := metadata.FromMap(client.Metadata)
		record.ClientIP = meta.ClientIP
		if meta.Protocol != "" {
			record.Protocol = meta.Protocol
		} else {
			record.Protocol = "websocket"
		}
	} else {
		record.Protocol = "websocket"
	}

	// 设置 metadata (如果存在)
	if client.Metadata != nil {
		record.Metadata = sqlbuilder.MapAny(client.Metadata)
	}

	return record
}

// saveConnectionRecord 保存连接记录到数据库
func (h *Hub) saveConnectionRecord(record *ConnectionRecord) {
	if h.connectionRecordRepo == nil {
		return
	}

	syncx.Go(h.ctx).
		WithTimeout(5 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("保存连接记录崩溃", "panic", r, "connection_id", record.ConnectionID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			err := h.connectionRecordRepo.Create(ctx, record)
			if err == nil {
				h.logger.InfoKV("连接记录已保存",
					"connection_id", record.ConnectionID,
					"user_id", record.UserID,
					"client_ip", record.ClientIP,
				)
			}
			return err
		})
}

// updateConnectionOnDisconnect 更新连接断开信息
func (h *Hub) updateConnectionOnDisconnect(client *Client, reason DisconnectReason) {
	if h.connectionRecordRepo == nil {
		return
	}

	syncx.Go(h.ctx).
		WithTimeout(5 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("更新连接断开记录崩溃", "panic", r, "connection_id", client.ID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionRecordRepo.MarkDisconnected(ctx, client.ID, reason, 1000, string(reason))
		})
}

// updateConnectionHeartbeat 更新连接心跳记录
func (h *Hub) updateConnectionHeartbeat(connectionID string) {
	if h.connectionRecordRepo == nil {
		return
	}

	syncx.Go(h.ctx).
		WithTimeout(2 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("更新连接心跳崩溃", "panic", r, "connection_id", connectionID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			now := time.Now()
			return h.connectionRecordRepo.UpdateHeartbeat(ctx, connectionID, &now, nil)
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
		h.logger.InfoKV("客户端写入协程结束",
			"client_id", client.ID,
			"user_id", client.UserID,
		)
	}()

	h.logger.InfoKV("客户端写入协程启动",
		"client_id", client.ID,
		"user_id", client.UserID,
	)

	for {
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				h.logger.InfoKV("客户端发送通道关闭", "client_id", client.ID)
				return
			}

			if client.Conn != nil {
				client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
					h.logger.ErrorKV("客户端消息写入失败",
						"client_id", client.ID,
						"error", err,
					)
					return
				}
			}
		case <-h.ctx.Done():
			h.logger.InfoKV("客户端写入协程因Hub关闭而结束", "client_id", client.ID)
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
		h.logger.InfoKV("客户端读取协程结束", "client_id", client.ID)
	}()

	h.logger.InfoKV("客户端读取协程启动", "client_id", client.ID)

	for {
		messageType, data, err := client.Conn.ReadMessage()
		if err != nil {
			h.logger.InfoKV("客户端连接读取错误",
				"client_id", client.ID,
				"error", err,
			)
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

	// 调用消息接收回调
	ctx := context.Background()
	if err := h.InvokeMessageReceivedCallback(ctx, client, msg); err != nil {
		h.logger.WarnKV("消息接收回调执行失败",
			"client_id", client.ID,
			"error", err,
		)
	}
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

// normalizeMessageFields 规范化消息字段（补充缺失的字段）
func (h *Hub) normalizeMessageFields(client *Client, msg *HubMessage) {
	if msg.Sender == "" {
		msg.Sender = client.UserID
	}
	if msg.SenderType == "" {
		msg.SenderType = client.UserType
	}
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}
	if msg.MessageType == "" {
		msg.MessageType = MessageTypeText
	}
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("json_%s_%d", client.UserID, time.Now().UnixNano())
	}
}

// ============================================================================
// 心跳检查
// ============================================================================

// checkHeartbeat 检查客户端心跳
func (h *Hub) checkHeartbeat() {
	allClients := h.GetClientsCopy()
	now := time.Now()
	timeoutClients := h.checkWebSocketTimeout(now, allClients)

	if timeoutClients > 0 {
		h.logger.InfoKV("心跳检查完成",
			"timeout_clients", timeoutClients,
		)
	}
}

// checkWebSocketTimeout 检查WebSocket超时
func (h *Hub) checkWebSocketTimeout(now time.Time, clients []*Client) int {
	timeoutCount := 0
	for _, client := range clients {
		// 加锁读取时间戳以避免数据竞争
		h.mutex.RLock()
		// SSE 客户端使用 LastSeen，WebSocket 使用 LastHeartbeat
		var lastActive time.Time
		if client.ConnectionType == ConnectionTypeSSE {
			lastActive = client.LastSeen
		} else {
			lastActive = client.LastHeartbeat
		}
		h.mutex.RUnlock()

		if now.Sub(lastActive) > h.config.ClientTimeout {
			h.Unregister(client)
			timeoutCount++

			if h.heartbeatTimeoutCallback != nil {
				h.heartbeatTimeoutCallback(client.ID, client.UserID, lastActive)
			}
		}
	}
	return timeoutCount
}

// ============================================================================
// 广播处理
// ============================================================================

// handleBroadcast 处理广播消息
func (h *Hub) handleBroadcast(msg *HubMessage) {
	if msg.Receiver != "" {
		h.handleDirectMessage(msg)
	} else {
		h.handleBroadcastMessage(msg)
	}
}

// handleDirectMessage 处理点对点消息
func (h *Hub) handleDirectMessage(msg *HubMessage) {
	// 在锁内复制客户端列表，避免竞争
	clients := h.GetClientsCopyForUser(msg.Receiver, msg.ReceiverClient)

	if len(clients) > 0 {
		// 增加消息发送统计
		if h.statsRepo != nil {
			syncx.Go(h.ctx).
				WithTimeout(1 * time.Second).
				ExecWithContext(func(ctx context.Context) error {
					return h.statsRepo.IncrementMessagesSent(ctx, h.nodeID, 1)
				})
		}

		// 如果指定了接收客户端且找到了，只发送给该客户端
		if msg.ReceiverClient != "" && len(clients) == 1 {
			h.sendToClient(clients[0], msg)
			return
		}
		// 发送给所有客户端
		for _, client := range clients {
			h.sendToClient(client, msg)
		}
		return
	}

	if h.SendToUserViaSSE(msg.Receiver, msg) {
		h.logger.DebugKV("消息已通过SSE发送", "message_id", msg.ID)
	}
}

// handleBroadcastMessage 处理广播消息
func (h *Hub) handleBroadcastMessage(msg *HubMessage) {
	if h.statsRepo != nil {
		syncx.Go(h.ctx).
			WithTimeout(1 * time.Second).
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

	// SSE 客户端使用专用的消息通道
	if client.ConnectionType == ConnectionTypeSSE {
		if client.TrySendSSE(msg) {
			client.LastSeen = time.Now()
			h.logger.DebugKV("SSE消息发送", "message_id", msg.ID, "client_id", client.ID, "user_id", client.UserID)
			// SSE消息成功发送，更新为成功状态
			if h.messageRecordRepo != nil && msg.MessageID != "" {
				go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
					return h.messageRecordRepo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
				})
			}
		} else {
			h.logger.WarnKV("SSE客户端消息通道已满或已关闭", "client_id", client.ID, "user_id", client.UserID)
			// SSE通道已满或已关闭，更新为失败状态
			if h.messageRecordRepo != nil && msg.MessageID != "" {
				go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
					return h.messageRecordRepo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusFailed, FailureReasonQueueFull, "SSE channel full or closed")
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
		if h.messageRecordRepo != nil && msg.MessageID != "" {
			go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
				return h.messageRecordRepo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusFailed, FailureReasonUnknown, err.Error())
			})
		}
		return
	}

	if client.TrySend(data) {
		// 消息成功发送到客户端通道，更新为成功状态
		if h.messageRecordRepo != nil && msg.MessageID != "" {
			go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
				return h.messageRecordRepo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
			})
		}
	} else {
		h.logger.WarnKV("客户端发送通道已满或已关闭", "client_id", client.ID)
		// 发送通道已满或已关闭，更新为失败状态
		if h.messageRecordRepo != nil && msg.MessageID != "" {
			go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
				return h.messageRecordRepo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusFailed, FailureReasonQueueFull, "client send channel full or closed")
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

	if _, exists := h.userToClients[client.UserID]; !exists {
		h.userToClients[client.UserID] = make(map[string]*Client)
	}
	h.userToClients[client.UserID][client.ID] = client

	// SSE 客户端单独存储
	if client.ConnectionType == ConnectionTypeSSE {
		h.sseMutex.Lock()
		h.sseClients[client.UserID] = client
		h.sseMutex.Unlock()
	}

	if client.UserType == UserTypeAgent || client.UserType == UserTypeBot {
		if _, exists := h.agentClients[client.UserID]; !exists {
			h.agentClients[client.UserID] = make(map[string]*Client)
		}
		h.agentClients[client.UserID][client.ID] = client
	}
}

// kickExistingClientsUnsafe 踢掉现有客户端（不加锁）
func (h *Hub) kickExistingClientsUnsafe(userID string, clients map[string]*Client, reason DisconnectReason) {
	for _, client := range clients {
		// 1. 发送强制下线通知给旧连接
		if client.Conn != nil {
			forceOfflineMsg := models.NewHubMessage().
				SetMessageType(models.MessageTypeForceOffline).
				SetSender("system").
				SetSenderType(models.UserTypeSystem).
				SetReceiver(client.UserID).
				SetReceiverType(client.UserType).
				SetContent("您的账号在其他设备登录，当前连接将被断开")

			// 同步发送通知（确保在断开前送达）
			h.sendToClient(client, forceOfflineMsg)
			// 等待消息发送完成
			time.Sleep(100 * time.Millisecond)
		}

		// 2. 记录日志并注销连接
		h.logger.InfoKV("踢出旧连接",
			"user_id", userID,
			"client_id", client.ID,
			"reason", reason,
		)
		h.Unregister(client)
	}
}

// kickOldestConnection 踢掉最早的连接
func (h *Hub) kickOldestConnection(clients map[string]*Client) {
	var oldestClient *Client
	var oldestTime time.Time

	for _, client := range clients {
		if oldestClient == nil || client.LastSeen.Before(oldestTime) {
			oldestClient = client
			oldestTime = client.LastSeen
		}
	}

	if oldestClient != nil {
		h.Unregister(oldestClient)
	}
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

// checkUserOnline 检查用户是否在线（简化版）
func (h *Hub) checkUserOnline(userID string) bool {
	h.mutex.RLock()
	_, exists := h.userToClients[userID]
	h.mutex.RUnlock()
	return exists
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

// UpdateUserHeartbeat 更新用户心跳时间
func (h *Hub) UpdateUserHeartbeat(userID string) error {
	if h.onlineStatusRepo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.onlineStatusRepo.UpdateHeartbeat(ctx, userID)
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
