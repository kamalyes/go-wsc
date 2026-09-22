/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 02:25:00
 * @FilePath: \go-wsc\connection\heartbeat.go
 * @Description: 连接域 —— 心跳保活组件（时间轮超时 + 异步续期 + SSE 兜底扫描）
 *   - PONG 响应发送
 *   - 心跳消息处理流程（前置回调 → 更新 → Redis 同步 → PONG → 统计 → 后置回调）
 *   - 时间轮心跳超时管理（WebSocket O(1)，SSE 由 CheckHeartbeat 扫描兜底）
 *   - 单 worker 收集 + 分块并行轻量续期（替代每心跳一个 goroutine）
 *
 * 从 hub/heartbeat.go、hub/lifecycle.go（processHeartbeatRedisUpdates）、
 * hub/message_handler.go（checkHeartbeat）拆解归位：保活全链路内聚于
 * HeartbeatKeeper，依赖经 HeartbeatHost 端口注入，不持有 *hub.Hub。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 回调签名
// ============================================================================

// HeartbeatFilter 心跳前置回调（返回 false 跳过本次心跳处理）
type HeartbeatFilter func(client *models.Client) bool

// HeartbeatReporter 心跳上报回调
type HeartbeatReporter func(client *models.Client)

// HeartbeatPostHook 心跳后置回调
type HeartbeatPostHook func(client *models.Client)

// HeartbeatTimeoutCallback 心跳超时回调（超时注销前通知，参数：clientID、userID、最后心跳时间）
type HeartbeatTimeoutCallback func(clientID, userID string, lastHeartbeat time.Time)

const (
	// heartbeatRenewChCapacity 异步续期通道容量（心跳热路径非阻塞提交，满则跳过本次）
	heartbeatRenewChCapacity = 8192
	// heartbeatRenewChunkSize 单次轻量续期的分块大小（并行 Eval 的粒度）
	heartbeatRenewChunkSize = 512
	// heartbeatRenewWorkers 并行续期 worker 上限
	heartbeatRenewWorkers = 8
	// heartbeatRenewFlushThreshold 批量到达阈值时提前刷写
	heartbeatRenewFlushThreshold = 256
	// heartbeatRenewFlushTimeout 单次续期刷写的查询超时
	heartbeatRenewFlushTimeout = 10 * time.Second
	// heartbeatUpdateTimeout 外部心跳更新（UpdateClientHeartbeat）的查询超时
	heartbeatUpdateTimeout = 2 * time.Second
	// pongSendTimeout 非阻塞发送失败后带超时阻塞重试的时长
	pongSendTimeout = 500 * time.Millisecond
)

// HeartbeatKeeper 心跳保活组件
type HeartbeatKeeper struct {
	host    HeartbeatHost
	timer   *syncx.HashedWheelTimer
	renewCh chan *models.Client
	idGen   models.IDGenerator

	beforeCallback  HeartbeatFilter
	reportCallback  HeartbeatReporter
	afterCallback   HeartbeatPostHook
	timeoutCallback HeartbeatTimeoutCallback

	wg sync.WaitGroup
}

// NewHeartbeatKeeper 创建心跳保活组件
// timer 由编排层构造注入（心跳与 ACK 各持独立时间轮实例，互不争用）
func NewHeartbeatKeeper(host HeartbeatHost, timer *syncx.HashedWheelTimer, idGen models.IDGenerator) *HeartbeatKeeper {
	return &HeartbeatKeeper{
		host:    host,
		timer:   timer,
		renewCh: make(chan *models.Client, heartbeatRenewChCapacity),
		idGen:   idGen,
	}
}

// SetBeforeHeartbeatCallback 注入心跳前置回调
func (k *HeartbeatKeeper) SetBeforeHeartbeatCallback(fn HeartbeatFilter) { k.beforeCallback = fn }

// SetHeartbeatReportCallback 注入心跳上报回调
func (k *HeartbeatKeeper) SetHeartbeatReportCallback(fn HeartbeatReporter) { k.reportCallback = fn }

// SetAfterHeartbeatCallback 注入心跳后置回调
func (k *HeartbeatKeeper) SetAfterHeartbeatCallback(fn HeartbeatPostHook) { k.afterCallback = fn }

// SetHeartbeatTimeoutCallback 注入心跳超时回调
func (k *HeartbeatKeeper) SetHeartbeatTimeoutCallback(fn HeartbeatTimeoutCallback) {
	k.timeoutCallback = fn
}

// ============================================================================
// PONG 响应与心跳消息处理
// ============================================================================

// sendPongResponse 发送 pong 响应（心跳热路径专用）
// 接收已获取的客户端对象，避免在发送时重新查询 shardedRegistry 导致的竞态条件与冗余开销
func (k *HeartbeatKeeper) sendPongResponse(client *models.Client, now time.Time) error {
	if client == nil {
		return errorx.WrapError("client is nil")
	}

	pongMsg := &models.HubMessage{
		ID:           k.idGen.GenerateRequestID(),
		MessageType:  models.MessageTypePong,
		Sender:       models.UserTypeSystem.String(),
		SenderType:   models.UserTypeSystem,
		Receiver:     client.UserID,
		ReceiverType: client.UserType,
		CreateAt:     now,
		Priority:     models.PriorityNormal,
	}

	// 序列化消息
	data, err := json.Marshal(pongMsg)
	if err != nil {
		return errorx.WrapError("failed to marshal pong message", err)
	}

	// 优先使用非阻塞发送
	if client.TrySend(data) {
		client.SetLastPong(now) // 直接更新，避免 UpdatePongTime 的冗余 GetClient 查询
		return nil
	}

	// 非阻塞发送失败（通道满或客户端刚注册写协程尚未就绪），
	// 使用带超时的阻塞发送重试，避免 pong 响应被静默丢弃
	client.CloseMu.Lock()
	defer client.CloseMu.Unlock()

	if client.IsClosed() || client.SendChan == nil {
		return errorx.WrapError("client is closed or send channel is nil")
	}

	timer := time.NewTimer(pongSendTimeout)
	defer timer.Stop()

	select {
	case client.SendChan <- data:
		client.SetLastPong(now) // 直接更新
		return nil
	case <-timer.C:
		k.host.GetLogger().WarnContextKV(client.Context, "心跳 pong 响应发送超时",
			"client_id", client.ID,
			"user_id", client.UserID,
		)
		return errorx.WrapError("pong send timeout, client send channel may be full")
	}
}

// TouchHeartbeat 刷新客户端心跳（协议级 PING 与应用层心跳共用同一保活路径）
// 更新内存时间戳 + O(1) 刷新时间轮超时任务 + 异步续期 Redis 在线索引
// 异步续期说明：单 goroutine worker 收集 channel（替代每次心跳创建独立 goroutine），
// 周期性分块并行调用 RenewClientsOnline 轻量续期（跳过序列化/压缩/SETEX）；
// client:<id> 键已过期/被淘汰的客户端由 repo 内部检测并走全量重建，
// 即使 Redis 键丢失也能基于内存客户端恢复索引
func (k *HeartbeatKeeper) TouchHeartbeat(client *models.Client, now time.Time) {
	client.SetLastHeartbeat(now)
	client.SetLastSeen(now)

	// ⏰ 刷新时间轮心跳超时（O(1) 操作，取消旧任务 + 调度新任务）
	k.RefreshHeartbeatTimeout(client)

	// 异步续期 Redis 在线索引与跨节点路由（不阻塞心跳主流程）
	if k.host.GetOnlineStatusRepo() != nil {
		select {
		case k.renewCh <- client:
		default:
			// channel 满，跳过本次 Redis 更新（心跳下次还会来）
		}
	}
}

// HandleHeartbeat 处理心跳消息
// 流程：前置回调 → 更新心跳 → 日志 → Redis同步 → PONG响应 → 统计 → 后置回调
func (k *HeartbeatKeeper) HandleHeartbeat(client *models.Client) {
	// 检查客户端是否已关闭（防止处理已断开客户端的心跳）
	if client.IsClosed() {
		k.host.GetLogger().DebugContextKV(client.Context, "客户端已关闭，忽略心跳消息",
			"client_id", client.ID,
			"user_id", client.UserID)
		return
	}

	// 触发心跳前置回调，返回 false 则跳过后续心跳处理
	if k.beforeCallback != nil {
		if !k.beforeCallback(client) {
			return
		}
	}

	// 更新心跳请求时间（内存）- 收到PING时直接更新 client 字段，避免 shardedRegistry 冗余查询
	now := time.Now()
	k.TouchHeartbeat(client, now)

	// 💓 记录心跳日志
	logWithClient(k.host.GetLogger(), logger.DEBUG, "💓 收到心跳消息", client)

	// 直接发送 pong 响应（使用已获取的客户端对象，避免竞态条件）
	if err := k.sendPongResponse(client, now); err != nil {
		k.host.GetLogger().WarnContextKV(client.Context, "心跳 pong 响应发送失败",
			"client_id", client.ID,
			"user_id", client.UserID,
			"error", err,
		)
	}

	// 异步追踪心跳统计（不阻塞主流程）
	k.host.TrackHeartbeatStats(client)

	// 触发心跳上报回调
	if k.reportCallback != nil {
		k.reportCallback(client)
	}

	// 触发心跳后置回调
	if k.afterCallback != nil {
		k.afterCallback(client)
	}
}

// UpdateClientHeartbeat 按 clientID 更新客户端心跳（外部心跳驱动入口）
func (k *HeartbeatKeeper) UpdateClientHeartbeat(clientID string) error {
	repo := k.host.GetOnlineStatusRepo()
	if repo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatUpdateTimeout)
	defer cancel()
	return repo.UpdateClientHeartbeat(ctx, clientID)
}

// ============================================================================
// 心跳配置
// ============================================================================

// SetHeartbeatConfig 设置心跳配置
// interval: 心跳间隔，建议30秒
// timeout: 心跳超时时间，建议90秒（interval的3倍）
func (k *HeartbeatKeeper) SetHeartbeatConfig(interval, timeout time.Duration) {
	k.host.SetHeartbeatConfig(interval, timeout)
}

// ============================================================================
// 时间轮心跳超时管理（替代 O(N) 全量扫描）
// ============================================================================

// ScheduleHeartbeatTimeout 在时间轮上调度客户端心跳超时任务
// 仅用于 WebSocket 客户端；SSE 客户端由 CheckHeartbeat 扫描兜底
func (k *HeartbeatKeeper) ScheduleHeartbeatTimeout(client *models.Client) {
	if k.timer == nil || client.ConnectionType == models.ConnectionTypeSSE {
		return
	}
	k.timer.ScheduleWithKey(client.ID, k.host.GetClientTimeout(), k.makeHeartbeatTimeoutCallback(client))
}

// RefreshHeartbeatTimeout 刷新客户端心跳超时（O(1) 操作，取消旧 + 调度新）
// 收到 PING 或任何消息时调用；公开供集成测试/外部心跳驱动刷新时间轮
//
// 设计说明：WebSocket 客户端的超时由时间轮管理（非 CheckHeartbeat 全量扫描），
// 仅更新 client.LastHeartbeat 字段不会重排时间轮任务，必须调用本方法刷新。
func (k *HeartbeatKeeper) RefreshHeartbeatTimeout(client *models.Client) {
	if k.timer == nil || client.ConnectionType == models.ConnectionTypeSSE {
		return
	}
	k.timer.Refresh(client.ID, k.host.GetClientTimeout(), k.makeHeartbeatTimeoutCallback(client))
}

// CancelHeartbeatTimeout 取消客户端心跳超时任务（注销时调用）
func (k *HeartbeatKeeper) CancelHeartbeatTimeout(clientID string) {
	if k.timer == nil {
		return
	}
	k.timer.CancelByKey(clientID)
}

// makeHeartbeatTimeoutCallback 创建心跳超时回调
// 超时触发时注销客户端并通知 timeoutCallback
func (k *HeartbeatKeeper) makeHeartbeatTimeoutCallback(client *models.Client) func() {
	return func() {
		// 客户端已关闭（正常断开），跳过
		if client.IsClosed() {
			return
		}
		// 触发心跳超时回调
		if k.timeoutCallback != nil {
			k.timeoutCallback(client.ID, client.UserID, client.GetLastHeartbeat())
		}
		// 异步注销客户端
		k.host.UnregisterClient(client)
	}
}

// ============================================================================
// SSE 心跳兜底扫描（WebSocket 由时间轮 O(1) 管理，扫描只兜底 SSE）
// ============================================================================

// CheckHeartbeat 扫描 SSE 客户端心跳超时并注销（周期性兜底，EventLoop 触发）
//
// Phase 1 并行持读锁收集超时 SSE 客户端，Phase 2 锁外批量注销。
func (k *HeartbeatKeeper) CheckHeartbeat() {
	start := time.Now()
	now := start

	clientTimeout := k.host.GetClientTimeout()

	// 并发数快照（遍历开始时的总连接数）
	totalClients := k.host.GetShardedRegistry().GetClientCount()

	// Phase 1：并行持读锁收集 SSE 超时客户端（WebSocket 由时间轮管理，跳过）
	type timeoutClient struct {
		client     *models.Client
		lastActive time.Time
	}
	var mu sync.Mutex
	var timeouts []timeoutClient
	var scanned int64

	k.host.GetShardedRegistry().ForEachClientParallel(0, func(_ string, client *models.Client) {
		// WebSocket 客户端由 timer O(1) 管理，跳过
		if client.ConnectionType != models.ConnectionTypeSSE {
			return
		}
		atomic.AddInt64(&scanned, 1)
		// 原子读时间戳（并发安全，无数据竞争）
		lastActive := client.GetLastSeen()

		// 检查是否超时
		inactiveDuration := now.Sub(lastActive)
		if inactiveDuration > clientTimeout {
			mu.Lock()
			timeouts = append(timeouts, timeoutClient{client: client, lastActive: lastActive})
			mu.Unlock()
		}
	})

	traversalDuration := time.Since(start)

	// Phase 2：锁外批量注销（Unregister 内部走 channel 异步或 default 同步均安全）
	for _, tc := range timeouts {
		k.host.GetLogger().DebugContextKV(tc.client.Context, "❤️ 检测到心跳超时，注销客户端",
			"client_id", tc.client.ID,
			"user_id", tc.client.UserID,
			"user_type", tc.client.UserType,
			"connection_type", tc.client.ConnectionType,
			"last_active", tc.lastActive,
			"inactive_duration", now.Sub(tc.lastActive).String(),
			"timeout_threshold", clientTimeout.String(),
		)
		k.host.UnregisterClient(tc.client)

		if k.timeoutCallback != nil {
			k.timeoutCallback(tc.client.ID, tc.client.UserID, tc.lastActive)
		}
	}

	totalDuration := time.Since(start)
	k.host.GetLogger().DebugContextKV(k.host.Context(), "❤️ 心跳检查完成",
		"total_clients", totalClients,
		"scanned_sse", atomic.LoadInt64(&scanned),
		"timeout_count", len(timeouts),
		"traversal_duration_ms", traversalDuration.Milliseconds(),
		"unregister_duration_ms", (totalDuration - traversalDuration).Milliseconds(),
		"total_duration_ms", totalDuration.Milliseconds(),
	)
}

// ============================================================================
// 异步续期 worker（单 goroutine 收集 + 分块并行轻量续期）
// ============================================================================

// StartRenewalWorker 启动异步续期 worker（幂等；重复调用仅首个 ctx 生效）
//
// 替代每次心跳创建独立 goroutine 的模式，大幅减少 goroutine 创建/GC 压力。
//
// 关键设计：投递 *models.Client，flush 时分块并行调用 RenewClientsOnline 轻量续期
// （EXPIRE client:<id> + ZADD user_clients/node_clients/all_users/type 刷新 score + SETBIT 续期，
// 跳过 JSON 序列化/压缩/SETEX 全量重写）。client:<id> 键已过期或被淘汰的客户端
// 由 repo 内部检测后走全量重建，保留自愈语义——即使 Redis 键丢失，
// 心跳仍能重建索引，避免「用户实际在线但查询为离线」、跨节点路由 GetUserNodes 返回空的问题
//
// 断开竞态保护：removeClientUnsafe 中 closeClientChannel(MarkClosed) 先于
// removeOnlineStatusFromRedis(SetClientOffline) 执行，故 flush 时用 IsClosed()
// 过滤已断开客户端，避免为已下线客户端重新写入在线索引
func (k *HeartbeatKeeper) StartRenewalWorker(ctx context.Context) {
	k.wg.Add(1)

	// 按 clientID 去重收集客户端（同一客户端多次心跳只保留最新指针）
	batch := make(map[string]*models.Client, 256)
	// 心跳批量续期在线索引的 flush 间隔：由 host 端口提供（默认 2s）。
	// 可配置以应对不同负载场景（高并发可调小到 500ms 缩短索引续期窗口；该间隔仅影响续期与
	// 键缺失时的自愈重建，首次注册的索引写入由注册路径同步完成，不依赖此 ticker）
	ticker := time.NewTicker(k.host.GetHeartbeatRefreshInterval())
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}

		// 过滤已断开客户端，避免为其重建在线索引（断开竞态保护）
		liveClients := make([]*models.Client, 0, len(batch))
		for clientID, client := range batch {
			delete(batch, clientID)
			if client != nil && !client.IsClosed() {
				liveClients = append(liveClients, client)
			}
		}
		if len(liveClients) == 0 {
			return
		}

		// 并行续期：分块 + 固定 worker，千万级连接下单 goroutine 串行 Eval 必然积压
		// （500 万客户端/2s 周期 ÷ 512/批 ≈ 万次 Eval，串行不可行）
		var wg sync.WaitGroup
		sem := make(chan struct{}, heartbeatRenewWorkers)
		for start := 0; start < len(liveClients); start += heartbeatRenewChunkSize {
			end := start + heartbeatRenewChunkSize
			if end > len(liveClients) {
				end = len(liveClients)
			}
			chunk := liveClients[start:end]

			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				ctx, cancel := context.WithTimeout(context.Background(), heartbeatRenewFlushTimeout)
				defer cancel()

				// 轻量续期：跳过 JSON 序列化/压缩/SETEX，仅刷新 TTL 与 ZSET score；
				// client:<id> 键缺失的由 repo 内部走全量重建（保留原自愈语义）
				if err := k.host.GetOnlineStatusRepo().RenewClientsOnline(ctx, chunk); err != nil {
					k.host.GetLogger().DebugKV("心跳续期 Redis 在线状态失败",
						"count", len(chunk), "error", err)
				}
			}()
		}
		wg.Wait()
	}

	go func() {
		defer k.wg.Done()

		for {
			select {
			case client := <-k.renewCh:
				if client == nil {
					continue
				}
				batch[client.ID] = client
				// 批量到达阈值时提前刷写
				if len(batch) >= heartbeatRenewFlushThreshold {
					flush()
				}
			case <-ticker.C:
				flush()
			case <-ctx.Done():
				flush()
				return
			}
		}
	}()
}

// WaitRenewalWorker 等待续期 worker 退出（ctx 取消后 flush 完再退，保证优雅停机）
func (k *HeartbeatKeeper) WaitRenewalWorker() {
	k.wg.Wait()
}

// ============================================================================
// 包内日志辅助（带客户端上下文的单行 KV 日志）
// ============================================================================

// logWithClient 输出带客户端上下文与身份字段的日志
// 优先使用 client.Context（携带连接级 trace_id），fallback 到 Hub ctx 由调用方传入的 logger 上下文承担
func logWithClient(log spi.Logger, level logger.LogLevel, msg string, client *models.Client, extraFields ...interface{}) {
	fields := []interface{}{
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"client_ip", client.ClientIP,
	}
	fields = append(fields, extraFields...)

	ctx := client.Context
	if ctx == nil {
		ctx = context.Background()
	}

	switch level {
	case logger.INFO:
		log.InfoContextKV(ctx, msg, fields...)
	case logger.WARN:
		log.WarnContextKV(ctx, msg, fields...)
	case logger.ERROR:
		log.ErrorContextKV(ctx, msg, fields...)
	case logger.DEBUG:
		log.DebugContextKV(ctx, msg, fields...)
	}
}
