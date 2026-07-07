/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-06 21:21:26
 * @FilePath: \go-wsc\hub\worker_pool.go
 * @Description: Hub WorkerPool 封装
 *   基于 go-toolbox/syncx.WorkerPool，按任务类型分池控制并发
 *   防止 goroutine 无限增长导致 OOM，提升稳定性
 *
 * 分池设计：
 *   - MessagePool：消息发送（高优先级，worker 多）
 *   - CallbackPool：业务回调（中优先级）
 *   - RecordPool：数据库记录/统计同步（低优先级，可丢弃）
 *   - DistributedPool：跨节点消息处理
 *
 * 注意：syncx.NewWorkerPool 在构造时自动启动 worker，无需手动 Start
 */

package hub

import (
	"context"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// WorkerPoolConfig WorkerPool 配置
type WorkerPoolConfig struct {
	// MessageWorkers 消息发送 worker 数（默认 64）
	MessageWorkers int
	// MessageQueueSize 消息发送队列大小（默认 4096）
	MessageQueueSize int
	// CallbackWorkers 业务回调 worker 数（默认 32）
	CallbackWorkers int
	// CallbackQueueSize 业务回调队列大小（默认 2048）
	CallbackQueueSize int
	// RecordWorkers 数据库记录 worker 数（默认 16）
	RecordWorkers int
	// RecordQueueSize 数据库记录队列大小（默认 2048）
	RecordQueueSize int
	// DistributedWorkers 跨节点消息 worker 数（默认 32）
	DistributedWorkers int
	// DistributedQueueSize 跨节点消息队列大小（默认 2048）
	DistributedQueueSize int
}

// DefaultWorkerPoolConfig 默认 WorkerPool 配置
// 针对 8C16G 节点调优，可按实际负载调整
func DefaultWorkerPoolConfig() *WorkerPoolConfig {
	return &WorkerPoolConfig{
		MessageWorkers:       64,
		MessageQueueSize:     4096,
		CallbackWorkers:      32,
		CallbackQueueSize:    2048,
		RecordWorkers:        16,
		RecordQueueSize:      2048,
		DistributedWorkers:   32,
		DistributedQueueSize: 2048,
	}
}

// HubWorkerPool Hub 工作池集合
// 按任务类型分池，避免低优先级任务饿死高优先级任务
type HubWorkerPool struct {
	// MessagePool 消息发送池（sendToUser、broadcast 等）
	MessagePool *syncx.WorkerPool
	// CallbackPool 业务回调池（连接/断开回调、事件回调）
	CallbackPool *syncx.WorkerPool
	// RecordPool 数据库记录池（消息记录、统计同步）
	RecordPool *syncx.WorkerPool
	// DistributedPool 跨节点消息处理池
	DistributedPool *syncx.WorkerPool

	logger WSCLogger
}

// NewHubWorkerPool 创建 Hub 工作池集合
// syncx.NewWorkerPool 在构造时自动启动 worker goroutine
func NewHubWorkerPool(cfg *WorkerPoolConfig, log WSCLogger) *HubWorkerPool {
	if cfg == nil {
		cfg = DefaultWorkerPoolConfig()
	}

	return &HubWorkerPool{
		// NewWorkerPool 构造时自动启动 workers，无需调 Start
		MessagePool:     syncx.NewWorkerPool(cfg.MessageWorkers, cfg.MessageQueueSize),
		CallbackPool:    syncx.NewWorkerPool(cfg.CallbackWorkers, cfg.CallbackQueueSize),
		RecordPool:      syncx.NewWorkerPool(cfg.RecordWorkers, cfg.RecordQueueSize),
		DistributedPool: syncx.NewWorkerPool(cfg.DistributedWorkers, cfg.DistributedQueueSize),
		logger:          log,
	}
}

// Stop 关闭所有工作池并等待任务完成
func (w *HubWorkerPool) Stop() {
	_ = w.MessagePool.Close()
	_ = w.CallbackPool.Close()
	_ = w.RecordPool.Close()
	_ = w.DistributedPool.Close()
}

// ============================================================================
// 消息发送池
// ============================================================================

// SubmitMessage 提交消息发送任务（阻塞式，队列满时等待）
func (w *HubWorkerPool) SubmitMessage(ctx context.Context, task func()) {
	if err := w.MessagePool.Submit(ctx, task); err != nil {
		w.logger.Error("消息发送池提交失败: %v", err)
	}
}

// TrySubmitMessage 非阻塞提交消息发送任务
// 队列满时返回 false，调用方可降级处理（如丢弃、记录失败）
func (w *HubWorkerPool) TrySubmitMessage(task func()) bool {
	if err := w.MessagePool.SubmitNonBlocking(task); err != nil {
		w.logger.Warn("消息发送队列已满，任务被拒绝")
		return false
	}
	return true
}

// ============================================================================
// 业务回调池
// ============================================================================

// SubmitCallback 提交业务回调任务（阻塞式）
func (w *HubWorkerPool) SubmitCallback(ctx context.Context, task func()) {
	if err := w.CallbackPool.Submit(ctx, task); err != nil {
		w.logger.Error("回调池提交失败: %v", err)
	}
}

// TrySubmitCallback 非阻塞提交业务回调任务
func (w *HubWorkerPool) TrySubmitCallback(task func()) bool {
	if err := w.CallbackPool.SubmitNonBlocking(task); err != nil {
		w.logger.Warn("回调队列已满，任务被拒绝")
		return false
	}
	return true
}

// ============================================================================
// 数据库记录池
// ============================================================================

// SubmitRecord 提交数据库记录任务（阻塞式）
func (w *HubWorkerPool) SubmitRecord(ctx context.Context, task func()) {
	if err := w.RecordPool.Submit(ctx, task); err != nil {
		w.logger.Error("记录池提交失败: %v", err)
	}
}

// TrySubmitRecord 非阻塞提交数据库记录任务
// 队列满时丢弃（记录任务可丢失，不影响核心业务）
func (w *HubWorkerPool) TrySubmitRecord(task func()) bool {
	if err := w.RecordPool.SubmitNonBlocking(task); err != nil {
		// 记录任务可丢弃，仅 debug 级别日志
		w.logger.Debug("记录队列已满，任务被丢弃")
		return false
	}
	return true
}

// ============================================================================
// 跨节点消息池
// ============================================================================

// SubmitDistributed 提交跨节点消息处理任务（阻塞式）
func (w *HubWorkerPool) SubmitDistributed(ctx context.Context, task func()) {
	if err := w.DistributedPool.Submit(ctx, task); err != nil {
		w.logger.Error("跨节点消息池提交失败: %v", err)
	}
}

// TrySubmitDistributed 非阻塞提交跨节点消息处理任务
func (w *HubWorkerPool) TrySubmitDistributed(task func()) bool {
	if err := w.DistributedPool.SubmitNonBlocking(task); err != nil {
		w.logger.Warn("跨节点消息队列已满，任务被拒绝")
		return false
	}
	return true
}

// ============================================================================
// 状态查询
// ============================================================================

// WorkerPoolStats 工作池统计信息
type WorkerPoolStats struct {
	MessageQueueLen     int
	CallbackQueueLen    int
	RecordQueueLen      int
	DistributedQueueLen int
}

// Stats 获取各池队列长度
func (w *HubWorkerPool) Stats() WorkerPoolStats {
	return WorkerPoolStats{
		MessageQueueLen:     w.MessagePool.GetQueueSize(),
		CallbackQueueLen:    w.CallbackPool.GetQueueSize(),
		RecordQueueLen:      w.RecordPool.GetQueueSize(),
		DistributedQueueLen: w.DistributedPool.GetQueueSize(),
	}
}
