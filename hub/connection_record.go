/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 01:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-30 11:20:15
 * @FilePath: \go-wsc\hub\connection_record.go
 * @Description: Hub 连接记录与消息推送
 *   - 连接记录的创建与持久化（CreateConnectionRecord/saveConnectionRecord）
 *   - 断开连接记录更新
 *   - 欢迎消息发送
 *   - 离线消息推送
 *
 * 从 utils.go 拆分而来，职责单一：连接记录与上线推送相关逻辑
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 连接记录管理
// ============================================================================

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

// ============================================================================
// 欢迎消息
// ============================================================================

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

// ============================================================================
// 离线消息推送
// ============================================================================

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
