/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-19 00:00:00
 * @FilePath: \go-wsc\hub\node_ack_timeout.go
 * @Description: 跨节点投递 ACK 超时兜底扫描
 *
 * 背景（跨节点消息丢失的根本设计问题）：
 *   Redis PubSub 是无 ACK、至多一次投递，PUBLISH 命令成功 ≠ 目标节点收到
 *   正常闭环：目标节点收到并投递后回报状态到共享 DB
 *   （handleDistributedSendMessage → sendToClientSerialized → updateMessageStatusAsync，
 *   投递成功 success / 失败 failed+离线转存）
 *   异常场景：目标节点订阅失活（连接断开未恢复）或消息在传输中丢失时，
 *   目标节点永远不会回报 → 记录永远停留 sending
 *
 * 本扫描器等价于"目标节点 ACK"的兜底语义：
 *   - 收到 ACK（状态被目标节点更新）→ 正常送达
 *   - 超时未 ACK（仍 sending）→ 标记 AckTimeout（FindRetryable 可捞，外部重试机制接管）
 *     并转存离线队列（用户上线时推送），保证消息最终不丢
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"errors"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
)

const (
	// nodeAckFallbackScanInterval 兜底扫描间隔（由 Hub EventLoop IfTicker 触发）
	// 主路径已由 ack_timer.go 的 per-message 时间轮接管（每条消息 +nodeAckTimeout 单条 ClaimStaleSending），
	// 此低频扫描仅作为节点崩溃/重启导致内存 timer 丢失的安全网：恢复那些发送节点宕机、
	// 其 in-memory ACK 超时任务随之消失而永久停留 sending 的记录
	nodeAckFallbackScanInterval = 5 * time.Minute
	// nodeTimeoutStaleScanTimeout 单轮兜底扫描的 DB 查询超时（扫描量小，但防 DB 抖动卡死 EventLoop）
	nodeTimeoutStaleScanTimeout = 10 * time.Second
	// nodeAckTimeout 跨节点投递 ACK 超时窗口（时间轮 per-message 调度延迟）
	// 注意：不复用 config.AckTimeout（客户端消息确认超时，默认 500ms，毫秒级语义）——
	// 跨节点回报链路含目标节点投递 + statusUpdater 批量落盘刷写，亚秒到秒级完成，
	// 500ms 会大量误判；30s 足够宽容，只捕获真正的订阅失活/消息丢失
	nodeAckTimeout = 30 * time.Second
	// nodeAckScanLimit 单轮兜底扫描上限（防历史大堆积时打爆 DB/离线队列）
	nodeAckScanLimit = 200
)

// errNodeAckTimeout 跨节点投递 ACK 超时 sentinel（转存离线时作为投递失败原因）
var errNodeAckTimeout = errors.New("跨节点投递未在超时内收到目标节点确认（订阅失活或消息丢失）")

// timeoutStaleSendingRecords 扫描超时未确认的 sending 记录并兜底
// 崩溃安全网（见 nodeAckFallbackScanInterval 注释）：主路径 ack_timer.go 的 per-message 时间轮
// 正常场景下不会走到此分支（每条消息 +nodeAckTimeout 已被单条 ClaimStaleSending 处理），
// 仅当发送节点宕机导致其 in-memory timer 丢失、记录永久停留 sending 时由此低频扫描接管
// 由 Hub EventLoop IfTicker 触发（messageRecordRepo 与 pubsub 均启用时注册，见 lifecycle.go）
func (h *Hub) timeoutStaleSendingRecords() {
	if h.messageRecordRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), nodeTimeoutStaleScanTimeout)
	defer cancel()

	sending := models.MessageSendStatusSending
	records, err := h.messageRecordRepo.QueryRecords(ctx, &repository.MessageRecordFilter{
		Status: &sending,
		Limit:  nodeAckScanLimit,
	})
	if err != nil {
		h.logger.WarnKV("ACK超时扫描：查询 sending 记录失败", "error", err)
		return
	}

	cutoff := time.Now().Add(-nodeAckTimeout)

	// 内存过滤超时记录（MessageRecordFilter 暂不支持时间范围，sending 停留是异常场景，量小）
	stale := make([]*models.MessageSendRecord, 0, len(records))
	for _, record := range records {
		if record.CreateTime.Before(cutoff) {
			stale = append(stale, record)
		}
	}
	if len(stale) == 0 {
		return
	}

	staleIDs := make([]string, 0, len(stale))
	for _, record := range stale {
		staleIDs = append(staleIDs, record.MessageID)
	}

	// ⚠️ 多节点并发去重：所有 Pod 的扫描器都面对同一张共享记录表，若直接
	// BatchUpdateStatus + 转存离线，同一记录会被 N 个 Pod 各处理一次 → 用户上线收到 N 份重复推送。
	// ClaimStaleSending 用状态守卫（status='sending' → ack_timeout）原子认领：
	// 仅认领成功（RowsAffected=1）的节点负责该记录的后续兜底动作
	claimed, claimErr := h.messageRecordRepo.ClaimStaleSending(ctx, staleIDs, models.MessageSendStatusAckTimeout, models.FailureReasonAckTimeout, errNodeAckTimeout.Error())
	if claimErr != nil {
		h.logger.WarnKV("ACK超时扫描：认领超时记录失败",
			"count", len(staleIDs), "claimed", len(claimed), "error", claimErr)
	}
	if len(claimed) == 0 {
		return
	}
	claimedSet := make(map[string]struct{}, len(claimed))
	for _, id := range claimed {
		claimedSet[id] = struct{}{}
	}

	h.logger.WarnKV("跨节点消息ACK超时，已标记待重试",
		"count", len(claimed),
		"timeout", nodeAckTimeout,
		"node_id", h.nodeID,
	)

	// P2P 消息转存离线（用户上线时推送；转存成功后状态会被覆盖为 UserOffline）
	// 广播类记录（Receiver 为空）不转存，仅标记状态供审计
	for _, record := range stale {
		if _, ok := claimedSet[record.MessageID]; !ok {
			continue // 未被本节点认领（其他节点已处理或状态已变更）
		}
		if record.Receiver == "" {
			continue
		}
		msg, mErr := record.GetMessage()
		if mErr != nil || msg == nil {
			h.logger.WarnKV("ACK超时扫描：反序列化消息失败，无法转存离线",
				"message_id", record.MessageID, "error", mErr)
			continue
		}
		h.tryStoreOfflineOnDeliveryFailure(msg, errNodeAckTimeout)
	}
}
