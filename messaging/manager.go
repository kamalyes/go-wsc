/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 10:35:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 10:35:00
 * @FilePath: \go-wsc\messaging\manager.go
 * @Description: 消息域管理器 —— 发送 / 广播 / 分发 / ACK / 离线转存的域内编排
 *
 * 从 hub 的上帝对象拆出：编排留 hub，域逻辑进域。本结构持有消息链路
 * 的全部域内组件（ACK 管理器、超时时间轮、工作池、离线处理器），
 * 跨域能力一律经 Host 端口获取。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 应用层回调类型（由编排层注入，消息链路各环节触发）
// ============================================================================

// MessageSendCallback 消息发送完成回调
type MessageSendCallback func(msg *models.HubMessage, result *models.SendResult)

// MessageReceivedCallback 消息接收回调（客户端上行消息交给业务层处理）
type MessageReceivedCallback func(ctx context.Context, client *models.Client, msg *models.HubMessage) error

// ErrorCallback 错误处理回调（统一处理消息链路各环节错误）
type ErrorCallback func(ctx context.Context, err error, severity models.ErrorSeverity) error

// Manager 消息域管理器
//
// 组件注入采用「未注入即降级」语义：
//   - ackManager 为 nil 时 ACK 快路径跳过
//   - ackTimeoutTimer 为 nil 时跨节点 ACK 超时调度 no-op（兜底扫描仍在）
//   - offlineHandler 为 nil 时投递兜底仅计数不转存
//   - idGenerator 为 nil 时需调用方保证 msg.ID 非空
type Manager struct {
	host Host

	// ========== 域内组件 ==========

	// ackManager ACK 待确认消息管理器（EnableAck 时注入）
	ackManager *AckManager
	// ackTimeoutTimer 跨节点 ACK 超时时间轮（per-record O(1) 调度/取消）
	ackTimeoutTimer *syncx.HashedWheelTimer
	// workerPool 工作池集合（按任务类型分池控制并发）
	workerPool *HubWorkerPool
	// offlineHandler 离线消息处理器（spi.OfflineQueue 契约；混合实现见 HybridOfflineMessageHandler，
	// 持久化全部经注入的共享存储，Pod 本地无状态）
	offlineHandler spi.OfflineQueue
	// idGenerator 消息 ID 生成器（雪花 ID）
	idGenerator models.IDGenerator

	// ========== 应用层回调 ==========

	messageReceivedCallback MessageReceivedCallback
	errorCallback           ErrorCallback
	messageSendCallback     MessageSendCallback

	// ========== 消息计数（编排层定时刷写到 statsRepo） ==========

	msgSentCount           atomic.Int64
	broadcastSentCount     atomic.Int64
	broadcastFallbackCount atomic.Int64

	// wg 读写泵 goroutine 生命周期跟踪（编排层关闭时 Wait）
	wg sync.WaitGroup
}

// NewManager 创建消息域管理器
func NewManager(host Host) *Manager {
	return &Manager{host: host}
}

// ============================================================================
// 链式组件注入
// ============================================================================

// WithAckManager 注入 ACK 管理器
func (m *Manager) WithAckManager(am *AckManager) *Manager {
	m.ackManager = am
	return m
}

// WithAckTimeoutTimer 注入跨节点 ACK 超时时间轮
func (m *Manager) WithAckTimeoutTimer(t *syncx.HashedWheelTimer) *Manager {
	m.ackTimeoutTimer = t
	return m
}

// WithWorkerPool 注入工作池集合
func (m *Manager) WithWorkerPool(wp *HubWorkerPool) *Manager {
	m.workerPool = wp
	return m
}

// WithOfflineHandler 注入离线消息处理器（spi.OfflineQueue 契约；混合实现见 HybridOfflineMessageHandler）
func (m *Manager) WithOfflineHandler(h spi.OfflineQueue) *Manager {
	m.offlineHandler = h
	return m
}

// WithIDGenerator 注入消息 ID 生成器
func (m *Manager) WithIDGenerator(g models.IDGenerator) *Manager {
	m.idGenerator = g
	return m
}

// ============================================================================
// 链式回调注入
// ============================================================================

// WithMessageReceivedCallback 注入消息接收回调
func (m *Manager) WithMessageReceivedCallback(cb MessageReceivedCallback) *Manager {
	m.messageReceivedCallback = cb
	return m
}

// WithErrorCallback 注入错误处理回调
func (m *Manager) WithErrorCallback(cb ErrorCallback) *Manager {
	m.errorCallback = cb
	return m
}

// WithMessageSendCallback 注入消息发送完成回调
func (m *Manager) WithMessageSendCallback(cb MessageSendCallback) *Manager {
	m.messageSendCallback = cb
	return m
}

// ============================================================================
// 计数器访问（编排层 flushStatsCounters 消费后清零）
// ============================================================================

// SwapMessageSentCount 取出并清零点对点消息发送计数
func (m *Manager) SwapMessageSentCount() int64 {
	return m.msgSentCount.Swap(0)
}

// SwapBroadcastSentCount 取出并清零广播发送计数
func (m *Manager) SwapBroadcastSentCount() int64 {
	return m.broadcastSentCount.Swap(0)
}

// SwapBroadcastFallbackCount 取出并清零广播兜底转存计数
func (m *Manager) SwapBroadcastFallbackCount() int64 {
	return m.broadcastFallbackCount.Swap(0)
}

// ============================================================================
// 周期任务入口（编排层 EventLoop 定时触发）
// ============================================================================

// CleanupExpiredAcks 清理过期的 ACK 待确认消息（未注入 ACK 管理器时 no-op 返回 0）
func (m *Manager) CleanupExpiredAcks() int {
	if m.ackManager == nil {
		return 0
	}
	return m.ackManager.CleanupExpired()
}

// ScanNodeAckTimeouts 跨节点投递 ACK 超时兜底扫描（见 node_ack_timeout.go）
func (m *Manager) ScanNodeAckTimeouts() {
	m.timeoutStaleSendingRecords()
}

// ============================================================================
// 生命周期
// ============================================================================

// Wait 等待全部读写泵 goroutine 退出（编排层关闭时调用）
func (m *Manager) Wait() {
	m.wg.Wait()
}

// Stop 停止域内后台组件（工作池等由编排层持有的组件不在此列）
func (m *Manager) Stop() {
	if m.ackTimeoutTimer != nil {
		m.ackTimeoutTimer.Stop()
	}
}
