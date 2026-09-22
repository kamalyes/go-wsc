/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 19:00:00
 * @FilePath: \go-wsc\batcher\message_status_updater.go
 * @Description: 消息状态批量更新器
 *   基于 syncx.BatchProcessor 泛型批量处理器实现
 *   收集消息状态更新请求，按 batch flush 到 DB
 *   相同 status/reason/errorMsg 的更新合并成一条 SQL（WHERE message_id IN (...)）
 *   广播 1 万人成功 = 1 次 UPDATE，而非 1 万次
 *
 * 工作流程：
 *   1. 消息发送路径调用 Submit（非阻塞，队列满时丢弃）
 *   2. BatchProcessor 后台 worker 收集，满 batchSize 或每 flushInterval 触发 flush
 *   3. flush 时按 status+reason+errMsg 分组，每组一条 BatchUpdateStatus SQL
 *   4. SafeShutdown 时调用 Stop，flush 剩余数据后退出
 */

package batcher

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
)

// StatusUpdateItem 消息状态更新项（Hub 侧构造）
// Receiver 参与 MessageSink 复合键定位：P2P 场景下同一 message_id 为每个
// receiver 各建一条记录，缺少 receiver 会造成多 receiver 状态相互覆盖
type StatusUpdateItem struct {
	MessageID string
	Receiver  string                   // 接收者ID（广播类记录为空串）
	Status    models.MessageSendStatus // 消息发送状态
	Reason    models.FailureReason     // 失败原因
	ErrMsg    string                   // 错误信息
}

// MessageStatusUpdater 消息状态批量更新器
type MessageStatusUpdater struct {
	hub       StorageBatchWriter
	processor *syncx.BatchProcessor[*StatusUpdateItem]
}

// NewMessageStatusUpdater 创建消息状态批量更新器
func NewMessageStatusUpdater(hub StorageBatchWriter, queueSize, batchSize int, flushInterval time.Duration) *MessageStatusUpdater {
	u := &MessageStatusUpdater{hub: hub}
	u.processor = syncx.NewBatchProcessor(queueSize, batchSize, flushInterval, u.flush)
	return u
}

// Submit 非阻塞提交状态更新
// 队列满时返回 false（调用方可记 metric 或降级处理）
func (u *MessageStatusUpdater) Submit(item *StatusUpdateItem) bool {
	return u.processor.Submit(item)
}

// Stop 停止更新器，flush 剩余数据后退出
func (u *MessageStatusUpdater) Stop() {
	u.processor.Stop()
}

// flush 批量更新 DB
// 按 status+reason+errMsg 分组，每组一条 BatchUpdateStatus SQL
func (u *MessageStatusUpdater) flush(batch []*StatusUpdateItem) {
	if len(batch) == 0 {
		return
	}

	// 按 status+reason+errMsg 分组
	// 广播场景大部分是 success（reason="", errMsg=""），可合并成一条 SQL
	type groupKey struct {
		status models.MessageSendStatus
		reason models.FailureReason
		errMsg string
	}
	groups := make(map[groupKey][]models.MessageRecordKey, 4)
	for _, item := range batch {
		key := groupKey{item.Status, item.Reason, item.ErrMsg}
		groups[key] = append(groups[key], models.MessageRecordKey{
			MessageID: item.MessageID,
			Receiver:  item.Receiver,
		})
	}

	// 用 context.Background() 而非 h.ctx
	// SafeShutdown 中 Stop() 在 h.cancel() 之前调用，但 flush 可能耗时较长
	// 用独立 context 避免被 Hub cancel 截断
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for grp, keys := range groups {
		if err := u.hub.GetMessageRecordRepo().BatchUpdateStatus(ctx, keys, grp.status, grp.reason, grp.errMsg); err != nil {
			u.hub.GetLogger().DebugContextKV(u.hub.Context(), "批量更新消息状态失败",
				"count", len(keys),
				"status", grp.status,
				"error", err,
			)
		}
	}
}
