/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 18:23:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 18:23:00
 * @FilePath: \go-wsc\connection\heartbeat.go
 * @Description: 连接域心跳管理器 —— 内存时间戳刷新 + 时间轮 O(1) 超时 + SSE 兜底扫描
 *
 * 从 hub/registry.go 域化下沉：域组件持有心跳全部逻辑（时间轮随迁为域内资产），
 * hub 仅做编排委托（HandleHeartbeat 端口转发，见 hub/interfaces.go）。
 * WebSocket 心跳超时由分片时间轮 O(1) 管理（取消旧任务 + 注册新任务），
 * SSE 客户端不发送 PING，由 ScanSSETimeouts 定期兜底扫描；
 * 外部依赖（注销 / 统计 / Redis 续期入队 / 应用回调）经 HeartbeatHost 端口注入。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// HeartbeatManager 心跳管理器（连接域）
//
// 持有分片时间轮，管理 WebSocket 客户端的 O(1) 心跳超时；SSE 客户端
// 不发送 PING，无法通过时间轮 Refresh，由 ScanSSETimeouts 周期扫描
// LastSeen 判断活跃度
type HeartbeatManager struct {
	host          HeartbeatHost
	registry      *ShardedRegistry
	timer         *syncx.HashedWheelTimer
	clientTimeout time.Duration
	logger        spi.Logger
}

// NewHeartbeatManager 构造心跳管理器
//
// 构造期初始化时间轮（不依赖 Run），确保 Schedule/Refresh/Cancel 在
// 任何 goroutine 启动前可用；timerOptions 透传时间轮分片配置
func NewHeartbeatManager(host HeartbeatHost, registry *ShardedRegistry, clientTimeout time.Duration, timerOptions ...syncx.TimerOption) *HeartbeatManager {
	logger := host.GetLogger()
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &HeartbeatManager{
		host:          host,
		registry:      registry,
		timer:         syncx.NewHashedWheelTimer(timerOptions...),
		clientTimeout: clientTimeout,
		logger:        logger,
	}
}

// Handle 处理一次心跳（协议级 PING 与应用层心跳共用同一保活入口）
// 流程：前置回调 → 续期 → Redis 异步续期入队 → 上报回调 → 后置回调 → 统计
func (m *HeartbeatManager) Handle(client *models.Client) {
	// 检查客户端是否已关闭（防止处理已断开客户端的心跳）
	if client == nil || client.IsClosed() {
		return
	}

	// 触发心跳前置回调，返回 false 则跳过后续心跳处理
	if cb := m.host.GetBeforeHeartbeatCallback(); cb != nil && !cb(client) {
		return
	}

	// 更新心跳请求时间（内存）+ O(1) 续期时间轮超时任务
	m.touch(client, time.Now())

	// 异步续期 Redis 在线索引与跨节点路由（不阻塞心跳主流程）
	m.host.EnqueueHeartbeatRenew(client)

	// 触发心跳上报回调（业务侧按心跳周期感知活跃度，先于后置回调与旧版时序一致）
	if cb := m.host.GetHeartbeatReportCallback(); cb != nil {
		cb(client)
	}

	// 触发心跳后置回调
	if cb := m.host.GetAfterHeartbeatCallback(); cb != nil {
		cb(client)
	}

	// 异步追踪心跳统计（不阻塞主流程）
	m.host.TrackHeartbeatStats(client)
}

// touch 刷新客户端心跳（协议级 PING 与应用层心跳共用同一保活路径）
// 更新内存时间戳 + O(1) 刷新时间轮超时任务（取消旧任务 + 调度新任务）
func (m *HeartbeatManager) touch(client *models.Client, now time.Time) {
	client.SetLastHeartbeat(now)
	client.SetLastSeen(now)

	// WebSocket 客户端超时由时间轮管理；SSE 客户端由 ScanSSETimeouts 扫描兜底
	if client.ConnectionType == models.ConnectionTypeSSE {
		return
	}
	m.timer.Refresh(client.ID, m.clientTimeout, m.timeoutTask(client))
}

// ScheduleTimeout 注册连接时在时间轮上调度心跳超时任务
// 仅用于 WebSocket 客户端；SSE 客户端由 ScanSSETimeouts 扫描兜底
func (m *HeartbeatManager) ScheduleTimeout(client *models.Client) {
	if client.ConnectionType == models.ConnectionTypeSSE {
		return
	}
	m.timer.ScheduleWithKey(client.ID, m.clientTimeout, m.timeoutTask(client))
}

// CancelTimeout 注销连接时取消时间轮上的心跳超时任务
func (m *HeartbeatManager) CancelTimeout(clientID string) {
	m.timer.CancelByKey(clientID)
}

// timeoutTask 创建心跳超时回调闭包
func (m *HeartbeatManager) timeoutTask(client *models.Client) func() {
	return func() {
		m.onTimeout(client)
	}
}

// onTimeout 心跳超时处理：触发超时回调并异步注销客户端
func (m *HeartbeatManager) onTimeout(client *models.Client) {
	// 客户端已关闭（正常断开），跳过
	if client.IsClosed() {
		return
	}
	// 触发心跳超时回调
	if cb := m.host.GetHeartbeatTimeoutCallback(); cb != nil {
		cb(client.ID, client.UserID, client.GetLastHeartbeat())
	}
	// 异步注销客户端
	m.host.Unregister(client)
}

// ScanSSETimeouts 扫描 SSE 客户端心跳超时（兜底机制）
// WebSocket 客户端由时间轮 O(1) 管理，此处仅扫描 SSE 客户端
// SSE 客户端不发送 PING，无法通过时间轮 Refresh，需定期扫描 LastSeen 判断活跃度
//
// ⚠️ 死锁防御：遍历持有 shard 读锁，先收集超时客户端到本地 slice（mutex 保护
// 并发 append），遍历结束后在锁外统一调用 Unregister
func (m *HeartbeatManager) ScanSSETimeouts() {
	now := time.Now()

	// Phase 1：并行持读锁收集 SSE 超时客户端
	var mu sync.Mutex
	var timeouts []*models.Client
	var scanned int64

	m.registry.ForEachSSEClientParallel(0, func(_, _ string, client *models.Client) {
		atomic.AddInt64(&scanned, 1)
		// 原子读时间戳（并发安全，无数据竞争）
		if now.Sub(client.GetLastSeen()) > m.clientTimeout {
			mu.Lock()
			timeouts = append(timeouts, client)
			mu.Unlock()
		}
	})

	// Phase 2：锁外批量注销（Unregister 内部 go 异步，均安全）
	for _, client := range timeouts {
		lastActive := client.GetLastSeen()
		m.logger.DebugContextKV(client.Context, "检测到SSE心跳超时，注销客户端",
			"client_id", client.ID,
			"user_id", client.UserID,
			"user_type", client.UserType,
			"last_active", lastActive,
			"inactive_duration", now.Sub(lastActive).String(),
			"timeout_threshold", m.clientTimeout.String(),
		)

		m.host.Unregister(client)

		if cb := m.host.GetHeartbeatTimeoutCallback(); cb != nil {
			cb(client.ID, client.UserID, lastActive)
		}
	}

	if n := atomic.LoadInt64(&scanned); n > 0 || len(timeouts) > 0 {
		m.logger.DebugContextKV(m.host.Context(), "SSE心跳检查完成",
			"scanned", n,
			"timeouts", len(timeouts),
		)
	}
}

// Stop 停止时间轮（不再触发超时注销；停机路径调用）
func (m *HeartbeatManager) Stop() {
	m.timer.Stop()
}
