/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 10:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 10:00:00
 * @FilePath: \go-wsc\hub\ack_timer.go
 * @Description: 跨节点投递 ACK 超时时间轮管理（per-message O(1) 调度/取消）
 *
 * 替代 node_ack_timeout.go 的 30s 全量 DB 扫描主路径：
 *   - recordMessageToDatabase 创建 sending 记录时调度 per-message 超时任务（O(1)）
 *   - updateMessageStatusAsync 状态变更时 O(1) 取消（本地投递即时取消，跨节点目标取消为 no-op）
 *   - 超时回调：单条 ClaimStaleSending 认领（状态守卫，多节点去重）→ 标记 AckTimeout → 转存离线
 *   - 节点崩溃导致内存 timer 丢失时，由 node_ack_timeout.go 的低频兜底扫描接管（见 nodeAckFallbackScanInterval）
 *
 * 性能对比（百万级连接）：
 *   - 旧：每 30s 一次 SELECT...WHERE status='sending' LIMIT 200 + 内存时间过滤 → 批量 DB 尖峰
 *   - 新：每条跨节点消息 +30s 单条 ClaimStaleSending（message_id 索引，O(1)）→ DB 负载随时间分散
 *
 * 语义保留：
 *   - 本地投递：状态由 sending→success，updateMessageStatusAsync 即时 CancelByKey，0 冗余查询
 *   - 跨节点成功：目标节点更新共享 DB，本节点 timer 在 +30s 触发 ClaimStaleSending 扑空（状态已 success）→ no-op，1 次冗余索引查询
 *   - 跨节点失活/丢失：状态停留 sending，timer 触发 ClaimStaleSending 认领成功 → 标记 + 转存离线（与批量扫描等价）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"
)

// scheduleAckTimeout 在时间轮上调度跨节点 ACK 超时任务（per-message，O(1)）
// 在 recordMessageToDatabase 创建 sending 记录后调用
func (h *Hub) scheduleAckTimeout(messageID string) {
	if h.ackTimeoutTimer == nil || messageID == "" {
		return
	}
	h.ackTimeoutTimer.ScheduleWithKey(messageID, nodeAckTimeout, h.makeAckTimeoutCallback(messageID))
}

// cancelAckTimeout 取消跨节点 ACK 超时任务（O(1) 惰性取消）
// 在 updateMessageStatusAsync 状态从 sending 变更为 success/failed/useroffline 时调用
// 跨节点场景：目标节点 CancelByKey 扑空（key 不在本节点时间轮）→ no-op，发送节点 timer 仍会触发兜底检查
func (h *Hub) cancelAckTimeout(messageID string) {
	if h.ackTimeoutTimer == nil || messageID == "" {
		return
	}
	h.ackTimeoutTimer.CancelByKey(messageID)
}

// makeAckTimeoutCallback 创建 ACK 超时回调
// 超时触发时：原子认领（ClaimStaleSending 状态守卫，多节点去重）→ 标记 AckTimeout → 转存离线
func (h *Hub) makeAckTimeoutCallback(messageID string) func() {
	return func() {
		if h.messageRecordRepo == nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// 原子认领：仅当状态仍为 sending 时更新为 AckTimeout
		// 目标节点已回报 success/failed 时状态已变更，ClaimStaleSending 返回空 → no-op
		claimed, err := h.messageRecordRepo.ClaimStaleSending(ctx, []string{messageID},
			MessageSendStatusAckTimeout, FailureReasonAckTimeout, errNodeAckTimeout.Error())
		if err != nil {
			h.logger.WarnKV("ACK超时(时间轮)：认领失败",
				"message_id", messageID, "error", err)
			return
		}
		if len(claimed) == 0 {
			return // 状态已变更（成功/失败）或已被其他节点认领
		}

		h.logger.WarnKV("跨节点消息ACK超时(时间轮)，已标记待重试",
			"message_id", messageID,
			"timeout", nodeAckTimeout,
			"node_id", h.nodeID,
		)

		// 从 DB 取完整记录恢复消息体，转存离线（用户上线时推送）
		// 不在闭包中持有 msg 指针：避免百万级 in-flight 消息长期占用内存
		record, rErr := h.messageRecordRepo.FindByMessageID(ctx, messageID)
		if rErr != nil || record == nil {
			h.logger.WarnKV("ACK超时(时间轮)：查询记录失败，无法转存离线",
				"message_id", messageID, "error", rErr)
			return
		}
		if record.Receiver == "" {
			return // 广播类记录不转存，仅标记状态供审计
		}
		msg, mErr := record.GetMessage()
		if mErr != nil || msg == nil {
			h.logger.WarnKV("ACK超时(时间轮)：反序列化消息失败，无法转存离线",
				"message_id", messageID, "error", mErr)
			return
		}
		h.tryStoreOfflineOnDeliveryFailure(msg, errNodeAckTimeout)
	}
}
