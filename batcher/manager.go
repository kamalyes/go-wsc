/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 09:21:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 09:21:00
 * @FilePath: \go-wsc\batcher\manager.go
 * @Description: 批处理器域管理器 —— 五个攒批组件的统一构造与停机编排
 *
 * 编排层只持一个 Manager 引用，组件构造参数解析与停机编排放归本域：
 * - 记录 outbox 复用 MessageStatus 攒批参数（write-ahead INSERT 与状态
 *   UPDATE 同节奏，flush 间隔即 ACK 超时注册延后的上界）
 * - 停机分两段：StopTracking（连接清理前）与 StopRecords（连接清理后，
 *   先 outbox 后 statusUpdater，保 INSERT→UPDATE 落库顺序）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// Manager 批处理器域管理器：持有五个攒批组件并提供域内访问器
type Manager struct {
	statusUpdater  *MessageStatusUpdater
	recordOutbox   *MessageRecordOutbox
	heartbeatStats *HeartbeatStatsUpdater
	messageStats   *MessageStatsBatcher
	observerNotify *ObserverNotificationBatcher
}

// NewManager 构造全部批处理器并启动后台 flush 协程
// observerNotify 为观察者直投端口（消息域 Manager 实现，flush 回调直连域组件）；
// cfg 为 nil 或子项零值时，各组件内部使用默认参数兜底（Get*Params 已处理）
func NewManager(host Host, observerNotify ObserverNotifier, cfg *wscconfig.BatcherConfig) *Manager {
	msgStatus := cfg.GetMessageStatusParams()
	hbStats := cfg.GetHeartbeatStatsParams()
	msgStats := cfg.GetMessageStatsParams()
	obsNotify := cfg.GetObserverNotifyParams()
	return &Manager{
		statusUpdater:  NewMessageStatusUpdater(host, msgStatus.QueueSize, msgStatus.BatchSize, msgStatus.FlushInterval),
		recordOutbox:   NewMessageRecordOutbox(host, msgStatus.QueueSize, msgStatus.BatchSize, msgStatus.FlushInterval),
		heartbeatStats: NewHeartbeatStatsUpdater(host, hbStats.QueueSize, hbStats.BatchSize, hbStats.FlushInterval),
		messageStats:   NewMessageStatsBatcher(host, msgStats.QueueSize, msgStats.BatchSize, msgStats.FlushInterval),
		observerNotify: NewObserverNotificationBatcher(observerNotify, obsNotify.QueueSize, obsNotify.BatchSize, obsNotify.FlushInterval),
	}
}

// StatusUpdater 消息状态批量更新器
func (m *Manager) StatusUpdater() *MessageStatusUpdater { return m.statusUpdater }

// RecordOutbox 消息记录攒批 outbox
func (m *Manager) RecordOutbox() *MessageRecordOutbox { return m.recordOutbox }

// HeartbeatStats 心跳统计批量更新器
func (m *Manager) HeartbeatStats() *HeartbeatStatsUpdater { return m.heartbeatStats }

// MessageStats 消息统计批量聚合器
func (m *Manager) MessageStats() *MessageStatsBatcher { return m.messageStats }

// ObserverNotify 观察者通知批量处理器
func (m *Manager) ObserverNotify() *ObserverNotificationBatcher { return m.observerNotify }

// StopTracking 停止心跳统计 / 消息统计 / 观察者通知批处理器
// 连接清理前调用，Stop 内部 flush 剩余数据并等待完成
func (m *Manager) StopTracking() {
	m.heartbeatStats.Stop()
	m.messageStats.Stop()
	m.observerNotify.Stop()
}

// StopRecords 停止记录 outbox 与状态更新器（连接清理后调用）
// 先 outbox 后 statusUpdater：保 INSERT→UPDATE 落库顺序，避免 UPDATE 扑空；
// 在 Hub cancel 之前调用，确保 flush 时 h.ctx 仍然有效
func (m *Manager) StopRecords() {
	m.recordOutbox.Stop()
	m.statusUpdater.Stop()
}
