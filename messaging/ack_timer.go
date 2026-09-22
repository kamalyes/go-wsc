/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 10:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 10:00:00
 * @FilePath: \go-wsc\messaging\ack_timer.go
 * @Description: 跨节点投递 ACK 超时时间轮管理（per-record O(1) 调度/取消）
 *
 * 替代 node_ack_timeout.go 的 30s 全量 DB 扫描主路径：
 *   - recordMessageToDatabase 创建 sending 记录时调度 per-record 超时任务（O(1)）
 *   - updateMessageStatusAsync 状态变更时 O(1) 取消（本地投递即时取消，跨节点目标取消为 no-op）
 *   - 超时回调：单条 ClaimStaleSending 认领（状态守卫，多节点去重）→ 标记 AckTimeout → 转存离线
 *   - 节点崩溃导致内存 timer 丢失时，由 node_ack_timeout.go 的低频兜底扫描接管（见 nodeAckFallbackScanInterval）
 *
 * 记录维度：P2P 同一 message_id 会为每个 receiver 各建一条记录，
 * 时间轮 key 为 (message_id, receiver) 复合键，超时认领/取消均精确到单条记录，
 * 避免多 receiver 场景 A 的 ACK 取消 B 的超时任务
 *
 * 性能对比（百万级连接）：
 *   - 旧：每 30s 一次 SELECT...WHERE status='sending' LIMIT 200 + 内存时间过滤 → 批量 DB 尖峰
 *   - 新：每条跨节点消息 +30s 单条 ClaimStaleSending（复合索引，O(1)）→ DB 负载随时间分散
 *
 * 语义保留：
 *   - 本地投递：状态由 sending→success，updateMessageStatusAsync 即时 CancelByKey，0 冗余查询
 *   - 跨节点成功：目标节点更新共享 DB，本节点 timer 在 +30s 触发 ClaimStaleSending 扑空（状态已 success）→ no-op，1 次冗余索引查询
 *   - 跨节点失活/丢失：状态停留 sending，timer 触发 ClaimStaleSending 认领成功 → 标记 + 转存离线（与批量扫描等价）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// ackTimerKey 时间轮复合键：message_id + "|" + receiver
// 同一 message_id 的多 receiver 记录各自独立调度/取消，互不干扰
func ackTimerKey(key models.MessageRecordKey) string {
	return key.MessageID + "|" + key.Receiver
}

// scheduleAckTimeout 在时间轮上调度跨节点 ACK 超时任务（per-record，O(1)）
// 在 recordMessageToDatabase 创建 sending 记录后调用
//
// 超时窗口附加 0~nodeAckTimeoutJitter 随机抖动：目标节点的慢回报恰好落在 30s 边缘时，
// 会与整点定时器在毫秒级窗口对撞同一行（行锁排队 → SLOW SQL / CRDB 40001 冲突），
// 抖动把认领时刻错峰到 30s~33s，边缘碰撞概率断崖式下降；
// 兜底扫描的 30s cutoff（node_ack_timeout.go）不受影响——先到者认领，后到者守卫扑空 no-op
func (m *Manager) scheduleAckTimeout(key models.MessageRecordKey) {
	if m.ackTimeoutTimer == nil || key.MessageID == "" {
		return
	}
	timeout := nodeAckTimeout + time.Duration(rand.Int64N(int64(nodeAckTimeoutJitter)))
	m.ackTimeoutTimer.ScheduleWithKey(ackTimerKey(key), timeout, m.makeAckTimeoutCallback(key))
}

// cancelAckTimeout 取消跨节点 ACK 超时任务（O(1) 惰性取消）
// 在 updateMessageStatusAsync 状态从 sending 变更为 success/failed/useroffline 时调用
// 跨节点场景：目标节点 CancelByKey 扑空（key 不在本节点时间轮）→ no-op，发送节点 timer 仍会触发兜底检查
func (m *Manager) cancelAckTimeout(key models.MessageRecordKey) {
	if m.ackTimeoutTimer == nil || key.MessageID == "" {
		return
	}
	m.ackTimeoutTimer.CancelByKey(ackTimerKey(key))
}

// makeAckTimeoutCallback 创建 ACK 超时回调
// 超时触发时：原子认领（ClaimStaleSending 状态守卫，多节点去重）→ 标记 AckTimeout → 转存离线
func (m *Manager) makeAckTimeoutCallback(key models.MessageRecordKey) func() {
	return func() {
		// ⏰ 终态清理 user_not_found 重路由守卫条目（见 self_heal.go，守卫是消息维度）
		m.host.DeleteRerouteGuard(key.MessageID)

		if m.host.GetMessageSink() == nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// 原子认领：仅当状态仍为 sending 时更新为 AckTimeout
		// 目标节点已回报 success/failed 时状态已变更，ClaimStaleSending 返回空 → no-op
		claimed, err := m.host.GetMessageSink().ClaimStaleSending(ctx, []models.MessageRecordKey{key},
			models.MessageSendStatusAckTimeout, models.FailureReasonAckTimeout, errNodeAckTimeout.Error())
		if err != nil {
			m.host.GetLogger().WarnContextKV(m.host.Context(), "ACK超时(时间轮)：认领失败",
				"message_id", key.MessageID, "receiver", key.Receiver, "error", err)
			return
		}
		if len(claimed) == 0 {
			return // 状态已变更（成功/失败）或已被其他节点认领
		}

		// 从 DB 取完整记录恢复消息体，转存离线（用户上线时推送）
		// 不在闭包中持有 msg 指针：避免百万级 in-flight 消息长期占用内存
		record, rErr := m.host.GetMessageSink().FindByMessageID(ctx, key)
		if rErr != nil || record == nil {
			m.host.GetLogger().WarnContextKV(m.host.Context(), "ACK超时(时间轮)：查询记录失败，无法转存离线",
				"message_id", key.MessageID, "receiver", key.Receiver, "error", rErr)
			return
		}
		if record.Receiver == "" {
			return // 广播类记录不转存，仅标记状态供审计
		}
		msg, mErr := record.GetMessage()
		if mErr != nil || msg == nil {
			m.host.GetLogger().WarnContextKV(m.host.Context(), "ACK超时(时间轮)：反序列化消息失败，无法转存离线",
				"message_id", key.MessageID, "error", mErr)
			return
		}
		// trace 恢复：MessageData 序列化了完整 HubMessage（含信封 trace_id），
		// 恢复到 ctx 后"已标记待重试"与转存离线日志可追溯原始发送链路
		ctx = msg.ContextFrom(ctx)
		m.host.GetLogger().WarnContextKV(ctx, "跨节点消息ACK超时(时间轮)，已标记待重试",
			"message_id", key.MessageID,
			"timeout", nodeAckTimeout,
			"node_id", m.host.GetNodeID(),
			"receiver", record.Receiver,
		)
		m.StoreOfflineOnDeliveryFailure(msg, errNodeAckTimeout)
	}
}
