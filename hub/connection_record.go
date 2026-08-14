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
	"runtime/debug"
	"time"

	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 连接记录管理
// ============================================================================

// CreateConnectionRecord 从 Client 创建连接记录
func (h *Hub) CreateConnectionRecord(client *Client) *ConnectionRecord {
	// 原子读取时间戳快照，避免取字段地址后与 SetLastHeartbeat/SetLastPong 并发写产生数据竞争
	lastHeartbeat := client.GetLastHeartbeat()
	lastPong := client.GetLastPong()

	record := &ConnectionRecord{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		NodeID:       client.NodeID,
		NodeIP:       client.NodeIP,
		NodePort:     client.NodePort,
		ClientIP:     client.GetClientIP(),
		LastPingAt:   &lastHeartbeat,
		LastPongAt:   &lastPong,
		Protocol:     client.ConnectionType,
		ClientType:   client.ClientType,
		ConnectedAt:  client.ConnectedAt,
		IsActive:     true,
	}

	// 设置 metadata（线程安全读取快照）
	record.Metadata = sqlbuilder.MapAny(client.GetMetadataSnapshot())

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
			h.logger.ErrorKV("保存连接记录崩溃", "panic", r, "stack", string(debug.Stack()), "user_id", record.UserID)
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
			h.logger.ErrorKV("更新连接断开记录崩溃", "panic", r, "stack", string(debug.Stack()), "user_id", client.UserID)
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

	h.sendToClient(h.ctx, client, msg)
}

// ============================================================================
// 离线消息推送
// ============================================================================

// pushOfflineMessagesOnConnect 客户端连接时推送离线消息
//
// 全量补发策略（双写存储下的两阶段推送）：
//  1. 按组 drain Redis 队列（FIFO，短期高性能消息优先投递）
//     - 枚举用户在该 namespace 下加入的全部 group + P2P（空 group）队列
//     - drain 出的消息推送成功后按 message_id 删 MySQL（双写去重，避免阶段 2 重复推送）
//  2. 跨组分页查 MySQL 剩余消息（Redis 已过期 / drain 异常残留），推送后删除
//
// 降级：groupRepo 不可用时只 drain P2P 队列 + 跨组查 MySQL
// （MySQL 跨组覆盖所有 group 的消息，不丢消息，仅 Redis 短期队列残留待自然过期）
//
// namespace 取自 client.Namespace（注册时已归一化）；groupIDs 按组动态注入到 drain ctx
func (h *Hub) pushOfflineMessagesOnConnect(client *Client) {
	if h.offlineMessageHandler == nil {
		return
	}

	ctx, cancel := context.WithTimeout(h.ctx, 60*time.Second)
	defer cancel()

	namespace := client.Namespace
	// 基础 ctx 注入 namespace（groupIDs 按组动态派生，drain 时单独注入对应 group）
	ctx = routing.WithNamespaceGroupIDs(ctx, namespace, nil)

	// MySQL 为双写超集，count==0 表示 Redis/MySQL 均无待推送消息，直接跳过
	totalCount, err := h.offlineMessageHandler.GetOfflineMessageCount(ctx, client.UserID)
	if err != nil {
		h.logger.ErrorKV("获取离线消息数量失败",
			"user_id", client.UserID, "namespace", namespace, "error", err)
		return
	}
	if totalCount == 0 {
		h.logger.DebugKV("用户无离线消息", "user_id", client.UserID, "namespace", namespace)
		return
	}

	h.logger.InfoKV("开始推送离线消息",
		"user_id", client.UserID, "namespace", namespace, "total_count", totalCount)

	totalSuccess, totalFailed := 0, 0
	allPushedIDs := make([]string, 0)
	allFailedIDs := make([]string, 0)

	// ===== 阶段1: 按组 drain Redis 队列 =====
	// groupIDs 首项为 "" 表示 P2P 队列（ns::userID），其后追加用户加入的全部 group
	groupIDs := []string{""}
	if h.groupRepo != nil {
		if userGroups, err := h.GetUserGroups(ctx, namespace, client.UserID); err != nil {
			h.logger.WarnKV("获取用户群组失败，降级只 drain P2P + 跨组查 MySQL",
				"user_id", client.UserID, "namespace", namespace, "error", err)
		} else {
			groupIDs = append(groupIDs, userGroups...)
		}
	}

	for _, gid := range groupIDs {
		// 按组注入 (ns, group)，DrainOfflineQueue 据此定位 Redis 队列 ns:group:userID
		groupCtx := routing.WithNamespaceGroupIDs(ctx, namespace, []string{gid})
		msgs, err := h.offlineMessageHandler.DrainOfflineQueue(groupCtx, client.UserID, 0) // 0=一次取尽
		if err != nil {
			h.logger.WarnKV("drain 离线队列失败",
				"user_id", client.UserID, "namespace", namespace, "group_id", gid, "error", err)
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		pushedIDs, failedIDs := h.pushAndDeleteOffline(groupCtx, client.UserID, msgs)
		allPushedIDs = append(allPushedIDs, pushedIDs...)
		allFailedIDs = append(allFailedIDs, failedIDs...)
		totalSuccess += len(pushedIDs)
		totalFailed += len(failedIDs)
	}

	// ===== 阶段2: 跨组分页查 MySQL 剩余消息（drain 已删的不重复）=====
	const batchSize = 100
	cursor := ""
	for {
		messages, nextCursor, err := h.offlineMessageHandler.GetOfflineMessages(ctx, client.UserID, batchSize, cursor)
		if err != nil {
			h.logger.ErrorKV("获取离线消息失败",
				"user_id", client.UserID, "cursor", cursor, "error", err)
			break
		}
		if len(messages) == 0 {
			break
		}
		pushedIDs, failedIDs := h.pushAndDeleteOffline(ctx, client.UserID, messages)
		allPushedIDs = append(allPushedIDs, pushedIDs...)
		allFailedIDs = append(allFailedIDs, failedIDs...)
		totalSuccess += len(pushedIDs)
		totalFailed += len(failedIDs)
		cursor = nextCursor
		if nextCursor == "" {
			break
		}
	}

	h.logger.InfoKV("离线消息推送完成",
		"user_id", client.UserID, "namespace", namespace,
		"success", totalSuccess, "failed", totalFailed)

	// 调用回调通知上游
	if h.offlineMessagePushCallback != nil && totalSuccess > 0 {
		h.offlineMessagePushCallback(client.UserID, allPushedIDs, allFailedIDs)
	}
}

// pushAndDeleteOffline 推送一批离线消息：成功的按 message_id 删除（MySQL），失败的更新推送状态（保留待下次重试）
// drain 路径下 Redis 已由 Dequeue 删除，本方法只删 MySQL（双写去重）；MySQL 路径下同样删 MySQL
// 返回成功/失败的 messageID 列表
func (h *Hub) pushAndDeleteOffline(ctx context.Context, userID string, messages []*HubMessage) (pushedIDs, failedIDs []string) {
	pushedIDs = make([]string, 0, len(messages))
	failedIDs = make([]string, 0)
	for _, message := range messages {
		// 标记为离线消息来源
		message.Source = models.MessageSourceOffline
		if message.Data == nil {
			message.Data = make(map[string]interface{})
		}
		message.Data["offline"] = true

		if err := h.sendToUser(ctx, userID, message); err != nil {
			h.logger.ErrorKV("离线消息推送失败",
				"user_id", userID, "message_id", message.MessageID, "error", err)
			failedIDs = append(failedIDs, message.MessageID)
			// 🔥 离线推送失败 → 更新 message_record 状态为 Failed
			// 离线消息推送失败通常因用户连接突然断开或队列满，message_record 应反映最终投递结果
			h.updateMessageStatusAsync(message.MessageID, MessageSendStatusFailed, FailureReasonConnError, err.Error())
			if err := h.offlineMessageHandler.UpdatePushStatus(ctx, []string{message.MessageID}, err); err != nil {
				h.logger.ErrorKV("更新离线消息推送失败状态失败",
					"user_id", userID, "message_id", message.MessageID, "error", err)
			}
			continue
		}
		pushedIDs = append(pushedIDs, message.MessageID)
	}

	// 推送成功的按 message_id 删 MySQL（drain 路径去重 + MySQL 路径清理）
	if len(pushedIDs) > 0 {
		if err := h.offlineMessageHandler.DeleteOfflineMessages(ctx, userID, pushedIDs); err != nil {
			h.logger.ErrorKV("删除已推送的离线消息失败",
				"user_id", userID, "count", len(pushedIDs), "error", err)
		} else {
			h.logger.DebugKV("删除已推送的离线消息",
				"user_id", userID, "count", len(pushedIDs))
		}
	}
	return pushedIDs, failedIDs
}
