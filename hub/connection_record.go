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
// 拆表后只填充 connect 身份+会话生命周期字段，质量指标由 saveConnectionQuality 落到 wsc_connection_qualities
func (h *Hub) CreateConnectionRecord(client *Client) *ConnectionRecord {
	record := &ConnectionRecord{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		AppID:        client.GetAppID(),
		Namespace:    client.GetNamespace(),
		NodeID:       client.NodeID,
		NodeIP:       client.NodeIP,
		NodePort:     client.NodePort,
		ClientIP:     client.GetClientIP(),
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
// ctx 应为 client.Context（带 client 维度的 trace_id），实现异步保存的全链路追踪
func (h *Hub) saveConnectionRecord(ctx context.Context, record *ConnectionRecord) {
	if h.connectionRecordRepo == nil {
		return
	}

	syncx.Go(ctx).
		WithTimeout(10 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorContextKV(ctx, "保存连接记录崩溃", "panic", r, "stack", string(debug.Stack()), "user_id", record.UserID)
		}).
		OnError(func(err error) {
			h.logger.ErrorContextKV(ctx, "保存连接记录失败",
				"user_id", record.UserID,
				"connection_id", record.ConnectionID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionRecordRepo.Upsert(ctx, record)
		})
}

// saveConnectionQuality 保存连接质量初始行到数据库
// 首次连接建零值行(QualityScore=100)，重连 reconnect_count+1（由 qualityRepo.Upsert 内部 OnConflict 处理）
// ctx 应为 client.Context（带 client 维度的 trace_id），实现异步保存的全链路追踪
func (h *Hub) saveConnectionQuality(ctx context.Context, client *Client) {
	if h.connectionQualityRepo == nil {
		return
	}

	quality := &ConnectionQuality{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		AppID:        client.GetAppID(),
		Namespace:    client.GetNamespace(),
	}

	syncx.Go(ctx).
		WithTimeout(10 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorContextKV(ctx, "保存连接质量崩溃", "panic", r, "stack", string(debug.Stack()), "user_id", client.UserID)
		}).
		OnError(func(err error) {
			h.logger.ErrorContextKV(ctx, "保存连接质量失败",
				"user_id", client.UserID,
				"connection_id", client.ID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionQualityRepo.Upsert(ctx, quality)
		})
}

// updateConnectionOnDisconnect 更新连接断开信息 + 质量终评
// 顺序：先 MarkDisconnected 写 duration/disconnected_at，再 FinalizeOnDisconnect 读 duration 算 FinalScore
// 用 client.Context 派生异步任务 ctx，保留 client 维度的 trace_id 实现全链路追踪
func (h *Hub) updateConnectionOnDisconnect(client *Client, reason DisconnectReason) {
	if h.connectionRecordRepo == nil && h.connectionQualityRepo == nil {
		return
	}

	syncx.Go(client.Context).
		WithTimeout(10 * time.Second).
		OnPanic(func(r interface{}) {
			h.logger.ErrorContextKV(client.Context, "更新连接断开记录崩溃", "panic", r, "stack", string(debug.Stack()), "user_id", client.UserID)
		}).
		OnError(func(err error) {
			h.logger.ErrorContextKV(client.Context, "更新连接断开记录失败",
				"client_id", client.ID,
				"user_id", client.UserID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			// 1. 先写 connect 表 duration/disconnected_at（终评依赖 duration）
			if h.connectionRecordRepo != nil {
				if err := h.connectionRecordRepo.MarkDisconnected(ctx, client.ID, reason, 1000); err != nil {
					return err
				}
			}
			// 2. 读 quality 行 + connect.duration，算 FinalScore 写 quality_score
			if h.connectionQualityRepo != nil {
				if err := h.connectionQualityRepo.FinalizeOnDisconnect(ctx, client.ID); err != nil {
					// 终评失败不中断（质量行可能已被清理）
					h.logger.WarnContextKV(ctx, "质量终评失败",
						"client_id", client.ID,
						"user_id", client.UserID,
						"error", err,
					)
				}
			}
			return nil
		})
}

// ============================================================================
// 欢迎消息
// ============================================================================

// sendWelcomeMessage 发送欢迎消息
// 已有 client 参数，内部直接用 client.Context 保留连接级 trace_id
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

	// 用 client.Context 保留连接级 trace_id，欢迎消息日志可全链路追踪
	h.sendToClient(client.Context, client, msg)
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
// 已有 client 参数，内部直接用 client.Context 保留连接级 trace_id
func (h *Hub) pushOfflineMessagesOnConnect(client *Client) {
	if h.offlineMessageHandler == nil {
		return
	}

	// 用 client.Context 派生超时 ctx（保留连接级 trace_id），全链路追踪离线消息推送
	ctx, cancel := context.WithTimeout(client.Context, 60*time.Second)
	defer cancel()

	namespace := client.Namespace
	// 基础 ctx 注入 appID+namespace（groupIDs 按组动态派生，drain 时单独注入对应 group）
	ctx = routing.NewRoute().WithAppID(client.GetAppID()).WithNamespace(namespace).Inject(ctx)

	// MySQL 为双写超集，count==0 表示 Redis/MySQL 均无待推送消息，直接跳过
	totalCount, err := h.offlineMessageHandler.GetOfflineMessageCount(ctx, client.UserID)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "获取离线消息数量失败",
			"user_id", client.UserID, "namespace", namespace, "error", err)
		return
	}
	if totalCount == 0 {
		h.logger.DebugContextKV(ctx, "用户无离线消息", "user_id", client.UserID, "namespace", namespace)
		return
	}

	h.logger.InfoContextKV(ctx, "开始推送离线消息",
		"user_id", client.UserID, "namespace", namespace, "total_count", totalCount)

	totalSuccess, totalFailed := 0, 0
	allPushedIDs := make([]string, 0)
	allFailedIDs := make([]string, 0)

	// ===== 阶段1: 按组 drain Redis 队列 =====
	// groupIDs 首项为 "" 表示 P2P 队列（ns::userID），其后追加用户加入的全部 group
	groupIDs := []string{""}
	if h.groupRepo != nil {
		if userGroups, err := h.GetUserGroups(ctx, client.UserID); err != nil {
			h.logger.WarnContextKV(ctx, "获取用户群组失败，降级只 drain P2P + 跨组查 MySQL",
				"user_id", client.UserID, "namespace", namespace, "error", err)
		} else {
			groupIDs = append(groupIDs, userGroups...)
		}
	}

	for _, gid := range groupIDs {
		// 按组注入 (app, ns, group)，DrainOfflineQueue 据此定位 Redis 队列 app:ns:group:userID
		groupCtx := routing.NewRoute().WithAppID(client.GetAppID()).WithNamespace(namespace).WithGroup(gid).Inject(ctx)
		msgs, err := h.offlineMessageHandler.DrainOfflineQueue(groupCtx, client.UserID, 0) // 0=一次取尽
		if err != nil {
			h.logger.WarnContextKV(ctx, "drain 离线队列失败",
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
			h.logger.ErrorContextKV(ctx, "获取离线消息失败",
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

	h.logger.InfoContextKV(ctx, "离线消息推送完成",
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
			h.logger.ErrorContextKV(ctx, "离线消息推送失败",
				"user_id", userID, "message_id", message.MessageID, "error", err)
			failedIDs = append(failedIDs, message.MessageID)
			// 🔥 离线推送失败 → 更新 message_record 状态为 Failed
			// 离线消息推送失败通常因用户连接突然断开或队列满，message_record 应反映最终投递结果
			h.updateMessageStatusAsync(message.MessageID, MessageSendStatusFailed, FailureReasonConnError, err.Error())
			if err := h.offlineMessageHandler.UpdatePushStatus(ctx, []string{message.MessageID}, err); err != nil {
				h.logger.ErrorContextKV(ctx, "更新离线消息推送失败状态失败",
					"user_id", userID, "message_id", message.MessageID, "error", err)
			}
			continue
		}
		pushedIDs = append(pushedIDs, message.MessageID)
	}

	// 推送成功的按 message_id 删 MySQL（drain 路径去重 + MySQL 路径清理）
	if len(pushedIDs) > 0 {
		if err := h.offlineMessageHandler.DeleteOfflineMessages(ctx, userID, pushedIDs); err != nil {
			h.logger.ErrorContextKV(ctx, "删除已推送的离线消息失败",
				"user_id", userID, "count", len(pushedIDs), "error", err)
		} else {
			h.logger.DebugContextKV(ctx, "删除已推送的离线消息",
				"user_id", userID, "count", len(pushedIDs))
		}
	}
	return pushedIDs, failedIDs
}
