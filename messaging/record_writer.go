/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-17 15:36:16
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-17 15:36:16
 * @FilePath: \go-wsc\messaging\record_writer.go
 * @Description: 消息记录写入路径 —— write-ahead 落库 / 状态异步回报 / 失败转存离线
 *
 * 拆分自 send.go：message_record.go 承载记录查询门面，本文件承载投递链路的
 * 写入三件套（sending 预落库、状态批量回报、终态失败转离线），与查询面分离
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// recordMessageToDatabase 记录消息到数据库（write-ahead：先落 sending 记录再投递）
//
// 攒批化：经 MessageRecordOutbox 批量 INSERT（满批/50ms flush，高吞吐下写放大
// 降低 2 个数量级）；ACK 超时注册延后到 flush 成功时（ScheduleAckTimeouts 回调）。
// outbox 未注入时降级原 workerPool 单条 Create 路径（本地测试/轻量嵌入场景）
func (m *Manager) recordMessageToDatabase(msg *models.HubMessage, sendErr error, payload []byte) {
	if m.host.GetMessageSink() == nil {
		return
	}

	now := time.Now()

	// 计算过期时间
	expiresAt := now.Add(mathx.IfNotZero(m.host.GetConfig().MessageRecordTTL, 24*time.Hour))

	// 完整记录所有字段
	record := &models.MessageSendRecord{
		SessionID:    msg.SessionID,
		MessageID:    msg.MessageID,
		HubID:        msg.ID,
		Sender:       msg.Sender,
		Receiver:     msg.Receiver,
		MessageType:  msg.MessageType,
		Source:       msg.Source,
		NodeIP:       m.host.GetNodeID(),
		CreateTime:   msg.CreateAt,
		Status:       models.MessageSendStatusSending, // 消息已入队,标记为sending
		RetryCount:   0,
		MaxRetry:     m.host.GetConfig().RetryPolicy.MaxRetries,
		RetryHistory: []models.RetryAttempt{},
		ExpiresAt:    &expiresAt,
	}

	// SetMessage 序列化消息体并同步 Namespace/GroupID 等路由信封字段到 record
	// payload：调用方预序列化的编码字节，与本地投递共享同一份，消除逐成员二次 Marshal
	if err := record.SetMessage(msg, payload); err != nil {
		m.host.GetLogger().WarnContextKV(msg.ContextFrom(m.host.Context()), "序列化消息数据失败",
			"message_id", msg.MessageID, "error", err)
	}

	if sendErr != nil {
		record.Status = models.MessageSendStatusFailed
		record.ErrorMessage = sendErr.Error()
		record.FailureReason = models.FailureReason(sendErr.Error())
		record.FirstSendTime = &now
		record.LastSendTime = &now
	}

	// 攒批落库唯一路径：outbox 满批/定时批量 INSERT（发送 goroutine 零阻塞，Submit 仅内存入队）
	// 编排层恒注入 outbox；未注入（测试桩）时跳过落库
	if outbox := m.host.GetMessageRecordOutbox(); outbox != nil {
		outbox.Submit(record)
	}
}

// updateMessageStatusAsync 非阻塞更新消息状态到 DB
// 按 (msgID, receiver) 精确定位记录：P2P 同一 message_id 会为每个 receiver 各建一条记录
// receiver 缺失会造成多 receiver 状态相互覆盖（如 A 的 success 覆盖 B 的 failed）
func (m *Manager) updateMessageStatusAsync(ctx context.Context, msgID, receiver string, status models.MessageSendStatus, reason models.FailureReason, errMsg string) {
	if m.host.GetMessageSink() == nil || m.host.GetMessageStatusUpdater() == nil {
		return
	}

	// ⏰ 状态由 sending 变更时 O(1) 取消跨节点 ACK 超时任务（本地投递即时取消，跨节点目标取消为 no-op）
	// 本节点持有的 timer 被取消后不再触发冗余 ClaimStaleSending 检查；详见 ack_timer.go
	m.cancelAckTimeout(models.MessageRecordKey{MessageID: msgID, Receiver: receiver})
	// 攒批竞态消除：记录仍在 outbox 未落库时直接合并状态（INSERT 时带终态）
	// 跳过 statusUpdater 的 UPDATE —— 否则 UPDATE 可能先于 INSERT 到库而扑空
	if outbox := m.host.GetMessageRecordOutbox(); outbox != nil &&
		outbox.UpdateStatusIfPending(models.MessageRecordKey{MessageID: msgID, Receiver: receiver}, status, reason, errMsg) {
		return
	}
	if !m.host.GetMessageStatusUpdater().Submit(&batcher.StatusUpdateItem{
		MessageID: msgID,
		Receiver:  receiver,
		Status:    status,
		Reason:    reason,
		ErrMsg:    errMsg,
	}) {
		// trace 恢复：ctx 由调用方传入（投递路径已恢复消息信封 trace_id）
		m.host.GetLogger().DebugContextKV(ctx, "消息状态更新队列已满，丢弃",
			"message_id", msgID,
			"receiver", receiver,
			"status", status,
		)
	}
}

// StoreOfflineOnDeliveryFailure 在在线投递失败时异步转存离线消息
//
// 触发条件（全部满足才转存）：
//   - offlineMessageHandler 可用
//   - msg.Receiver 非空（P2P 消息，广播场景不转存）
//   - msg.Source != offline（离线推送本身失败不重新存，避免无限循环）
//
// 状态流转：
//   - 转存成功 → message_record 状态从 Failed 覆盖为 UserOffline（消息已安全暂存，等用户上线推送）
//   - 转存失败 → 保持调用方已标记的 Failed 状态（消息确实丢了）
//
// 注意：多设备场景下若部分设备投递成功部分失败，失败设备仍会触发转存
// 这不会导致重复推送：用户上线 drain 时 pushAndDeleteOffline 成功后按 message_id 删 MySQL，客户端也可按 message_id 去重
func (m *Manager) StoreOfflineOnDeliveryFailure(msg *models.HubMessage, deliveryErr error) {
	if m.offlineHandler == nil || msg.Receiver == "" {
		return
	}
	// 离线消息推送失败不重新存（避免无限循环），只标记 Failed
	if msg.Source == models.MessageSourceOffline {
		return
	}

	syncx.Go().
		OnError(func(storeErr error) {
			m.host.GetLogger().WarnContextKV(msg.ContextFrom(m.host.Context()), "在线投递失败后转存离线也失败",
				"message_id", msg.MessageID,
				"user_id", msg.Receiver,
				"delivery_error", deliveryErr.Error(),
				"store_error", storeErr.Error(),
			)
			// 转存失败，保持已有的 Failed 状态，不覆盖
		}).
		ExecWithContext(func(storeCtx context.Context) error {
			storeCtx = msg.ContextFrom(storeCtx)
			if err := m.offlineHandler.StoreOfflineMessage(storeCtx, msg.Receiver, msg); err != nil {
				if errors.Is(err, spi.ErrOfflineStorageSkipped) {
					m.host.GetLogger().DebugContextKV(storeCtx, "offline fallback skipped; message not queued", "message_id", msg.MessageID)
					return nil
				}
				return err
			}
			// 转存成功 → 覆盖状态为 UserOffline（消息已暂存，等用户上线推送）
			m.updateMessageStatusAsync(storeCtx, msg.MessageID, msg.Receiver, models.MessageSendStatusUserOffline, models.FailureReasonUserOffline, "")
			m.host.GetLogger().InfoContextKV(storeCtx, "在线投递失败，消息已转存离线队列",
				"message_id", msg.MessageID,
				"user_id", msg.Receiver,
				"delivery_error", deliveryErr.Error(),
			)
			return nil
		})
}
