/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-30 23:55:51
 * @FilePath: \go-wsc\hub\lifecycle.go
 * @Description: Hub 生命周期管理
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

const (
	// heartbeatRenewChunkSize 心跳批量续期的分块大小（单次 RenewClientsOnline Eval 的客户端数）
	heartbeatRenewChunkSize = 512
	// heartbeatRenewWorkers 并行续期的最大并发块数（对 Redis 的并发 Eval 上限）
	heartbeatRenewWorkers = 8
	// coalescerDrainInterval 合并快照投递周期（50ms：高频状态消息的可感知延迟上限）
	// 合并器本体在 overload 域（Offer/Drain），drain 节拍与投递归编排层
	coalescerDrainInterval = 50 * time.Millisecond
	// nodeAckFallbackScanInterval 跨节点 ACK 超时兜底扫描间隔（与 messaging 域扫描器节拍一致）
	// 主路径为 messaging 域的 per-message ACK 超时时间轮，此低频扫描仅恢复发送节点宕机
	// 导致 in-memory timer 丢失、永久停留 sending 的记录（见 messaging/node_ack_timeout.go）
	nodeAckFallbackScanInterval = 5 * time.Minute
)

// Run 启动Hub
func (h *Hub) Run() {
	h.wg.Add(1)
	defer h.wg.Done()

	start := time.Now()

	// 显示启动配置（单行 KV，便于日志采集端结构化解析）
	h.logger.InfoKV("🚀 WebSocket Hub 启动",
		"node_id", h.nodeID,
		"node_ip", h.config.NodeIP,
		"node_port", h.config.NodePort,
		"message_buffer_size", h.config.MessageBufferSize,
		"heartbeat_interval", h.config.HeartbeatInterval,
		"client_timeout", h.config.ClientTimeout,
	)

	// 设置已启动标志并通知等待的goroutine
	// 所有后台 goroutine 必须在此保护块内启动，避免 Run() 被重复调用时
	// 启动多份心跳/订阅 goroutine，导致节点反复注册等问题
	if !h.started.CompareAndSwap(false, true) {
		// Hub 已经启动过，直接返回，避免重复启动后台 goroutine 和 EventLoop
		h.logger.WarnKV("Hub 已经启动，跳过重复启动", "node_id", h.nodeID)
		return
	}

	// 设置启动时间到 Redis
	if h.statsRepo != nil {
		syncx.Go().
			WithTimeout(2 * time.Second).
			OnError(func(err error) {
				h.logger.ErrorKV("注册节点到Redis失败", "error", err)
			}).
			ExecWithContext(func(execCtx context.Context) error {
				return h.statsRepo.RegisterNode(execCtx, h.nodeID, time.Now().Unix())
			})
	}

	h.logger.InfoKV("✅ Hub 启动成功",
		"node_id", h.nodeID,
		"startup_duration_ms", time.Since(start).Milliseconds(),
	)

	// 心跳统计批量更新器在构造时已自动启动（BatchProcessor 内部 worker）
	// ⏰ 心跳/跨节点 ACK 超时时间轮已在 NewHub() 构造期无条件初始化
	//（避免与并发 Register 产生数据竞争，见 hub.go）

	// 🚦 启动准入闸门水位评估循环（AIMD 升降级；NewHub 构造期已初始化默认水位）
	if gate := h.admission.Load(); gate != nil {
		gate.Start()
		h.logger.InfoKV("准入闸门已启动",
			"eval_interval", constants.DefaultAdmissionEvalInterval,
			"high_watermark", constants.DefaultAdmissionHighWatermark,
			"low_watermark", constants.DefaultAdmissionLowWatermark,
		)
	}

	// 🌊 启动广播延迟队列 drain 循环（填谷重投：按整形速率节拍投递，AIMD 提速联动）
	if h.broadcastDelayQueue != nil {
		syncx.Go(h.ctx).
			OnPanic(func(r any) {
				h.logger.ErrorKV("广播延迟队列 drain panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
			}).
			Exec(func() {
				h.broadcastDelayQueue.DrainLoop(h.ctx)
			})
	}

	// 🧹 启动高频合并器 drain ticker（latest-wins 快照周期投递）
	if h.ephemeralCoalescer.Load() != nil {
		h.startCoalescerDrain()
	}

	// 🐌 启动慢消费者扫描器（三级递进治理：记录 → 告警 → 驱逐；驱逐前消息保全）
	h.startSlowConsumerScanner()

	// 启动心跳 Redis 更新 worker（单 goroutine 处理所有客户端的心跳 Redis 更新）
	if h.onlineStatusRepo != nil {
		syncx.Go().
			OnPanic(func(r any) {
				h.logger.ErrorKV("心跳 Redis 更新 worker panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
			}).
			Exec(h.processHeartbeatRedisUpdates)
	}

	// 启动指标收集器（如果已配置）
	close(h.startCh)

	// 🌐 启动分布式服务（如果启用了 PubSub）
	if h.pubsub != nil {
		// 节点心跳已由 cluster.NodeRegistry.refreshLoop 接管（gRPC 模式）
		// 订阅节点间消息
		syncx.Go(h.ctx).
			OnPanic(func(r any) {
				h.logger.ErrorKV("订阅节点消息 panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
			}).
			Exec(func() {
				if err := h.SubscribeNodeMessages(h.ctx); err != nil {
					h.logger.ErrorKV("订阅节点消息失败", "error", err)
				}
			})

		// 订阅全局广播频道
		syncx.Go(h.ctx).
			OnPanic(func(r any) {
				h.logger.ErrorKV("订阅广播频道 panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
			}).
			Exec(func() {
				if err := h.SubscribeBroadcastChannel(h.ctx); err != nil {
					h.logger.ErrorKV("订阅广播频道失败", "error", err)
				}
			})

		// 订阅观察者通知频道
		syncx.Go(h.ctx).
			OnPanic(func(r any) {
				h.logger.ErrorKV("订阅观察者频道 panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
			}).
			Exec(func() {
				if err := h.SubscribeObserverChannel(h.ctx); err != nil {
					h.logger.ErrorKV("订阅观察者频道失败", "error", err)
				}
			})

		h.logger.InfoKV("🌐 分布式服务已启动", "node_id", h.nodeID)
	}

	// 🔗 启动节点间 gRPC 通信（若启用 node-grpc 配置）
	// gRPC 直连优先于 Redis PubSub 用于点对点路由，降低跨节点消息延迟
	// 组件「用时才装配」：配置启用且未经 InitNodeGRPC 显式初始化（也未经
	// WithNodeRegistry 注入）时在此补装，避免配置开了却不生效；
	// InitNodeGRPC 内部自检 PubSub 依赖，未设置时降级 Redis PubSub 模式
	if h.IsGRPCEnabled() && h.nodeRegistry == nil {
		h.InitNodeGRPC()
	}
	h.startNodeGRPC()

	// 使用 EventLoop 管理事件循环
	// 统一处理客户端注册/注销、消息广播和定时任务
	syncx.NewEventLoop(h.ctx).
		// 心跳检查定时器：SSE 客户端超时兜底（WebSocket 由 heartbeatTimer O(1) 管理）
		OnTicker(h.config.HeartbeatInterval, h.checkHeartbeat).
		// 统计计数器定时刷写：将原子计数器累积的统计批量写入 Redis
		OnTicker(30*time.Second, h.flushStatsCounters).
		// 性能监控定时器：定期报告性能指标
		// 使用配置中的 PerformanceMetricsInterval (默认5分钟)
		OnTicker(h.config.PerformanceMetricsInterval, h.reportPerformanceMetrics).
		// ACK清理定时器：定期清理过期的ACK记录（经 messaging 域管理器触发）
		// 使用配置中的 AckCleanupInterval (默认1分钟)
		OnTicker(h.config.AckCleanupInterval, h.cleanupExpiredAck).
		// user_not_found 重路由守卫过期条目清扫（防泄漏；PubSub 或 gRPC 任一跨节点通道
		// 启用即需要——gRPC-only 部署同样产生守卫条目；P2P 条目由 ACK 超时回调终态删除，
		// 广播兜底条目仅靠本清扫回收，见 self_heal.go）
		IfTicker(h.pubsub != nil || h.IsGRPCEnabled(),
			rerouteGuardSweepInterval,
			h.sweepRerouteGuard).
		// 在线状态清理定时器：定期清理过期的在线状态数据
		// 使用 OnlineStatus 配置中的 StatusRefreshInterval 和 EnableAutoCleanup
		IfTicker(h.onlineStatusRepo != nil && h.config.RedisRepository != nil &&
			h.config.RedisRepository.OnlineStatus != nil && h.config.RedisRepository.OnlineStatus.EnableAutoCleanup,
			mathx.IfNotZero(h.config.RedisRepository.OnlineStatus.StatusRefreshInterval, 60*time.Second),
			h.cleanupExpiredOnlineStatus).
		// 添加消息记录清理定时器（如果启用了消息记录仓库）
		IfTicker(h.messageSink != nil,
			mathx.IfNotZero(h.config.RecordCleanupInterval, 30*time.Minute),
			h.cleanupExpiredMessageRecords).
		// ⏰ 跨节点投递 ACK 超时兜底（崩溃安全网）：主路径由 messaging 域 per-message 时间轮接管，
		// 此低频扫描仅恢复发送节点宕机导致 in-memory timer 丢失、永久停留 sending 的记录
		// （PubSub 至多一次投递：目标节点订阅失活/消息丢失时状态会永远停留 sending，
		// 见 messaging/node_ack_timeout.go）
		IfTicker(h.messageSink != nil && h.pubsub != nil,
			nodeAckFallbackScanInterval,
			h.messagingMgr.ScanNodeAckTimeouts).
		// Panic处理：捕获事件处理过程中的panic，防止整个Hub崩溃
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("Hub事件循环panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
		}).
		// 优雅关闭：事件循环停止时记录日志
		OnShutdown(func() {
			h.logger.InfoKV("Hub事件循环已停止", "node_id", h.nodeID)
		}).
		// 运行事件循环（阻塞），直到context被取消
		Run()
}

// reportPerformanceMetrics 报告性能指标
func (h *Hub) reportPerformanceMetrics() {
	// 使用 shardedRegistry 原子计数器快速获取连接数，避免加锁
	activeClients := h.shardedRegistry.GetActiveClientCount()
	sseClients := h.shardedRegistry.GetSSEClientCount()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 从 Redis 获取统计信息
	if h.statsRepo == nil {
		return
	}

	// 🔥 保活：周期性 touch 重注册（HSETNX 幂等，保留原 start_time；EXPIRE 刷新 TTL）
	// 根因：stats key 续期仅由连接事件（syncClientStats）触发，空闲节点（0 连接）无事件，
	// key 在 ttl（如 10m）后过期 → GetNodeStats 永久报 "node stats not found"。
	// 本周期 touch（5m < ttl 10m）使活跃节点 stats key 不过期，同时自愈缺失的 key
	if err := h.statsRepo.RegisterNode(ctx, h.nodeID, time.Now().Unix()); err != nil {
		h.logger.WarnKV("刷新节点统计注册失败", "node_id", h.nodeID, "error", err)
		return
	}

	stats, err := h.statsRepo.GetNodeStats(ctx, h.nodeID)
	if err != nil {
		h.logger.WarnKV("获取节点统计失败", "node_id", h.nodeID, "error", err)
		return
	}

	// 单行 KV 输出性能指标（周期性日志，便于采集端结构化解析与监控聚合）
	h.logger.InfoKV("📊 Hub 性能指标报告",
		"node_id", h.nodeID,
		"websocket_connections", activeClients,
		"sse_connections", sseClients,
		"total_connections", stats.TotalConnections,
		"messages_sent", stats.MessagesSent,
		"broadcasts_sent", stats.BroadcastsSent,
		"uptime_seconds", stats.Uptime,
		// 本周期 routeToClusterForOfflineUser 触发次数，治本后应趋近 0
		"broadcast_fallback_count", h.messagingMgr.SwapBroadcastFallbackCount(),
	)
}

// processHeartbeatRedisUpdates 单 goroutine 收集所有客户端的心跳 Redis 更新
// 替代每次心跳创建独立 goroutine 的模式，大幅减少 goroutine 创建/GC 压力
//
// 关键设计：投递 *Client，flush 时分块并行调用 RenewClientsOnline 轻量续期
// （EXPIRE client:<id> + 索引刷新，跳过 JSON 序列化/压缩/SETEX 全量重写）。
// client:<id> 键已过期或被淘汰的客户端由 repo 内部检测后走全量重建，保留自愈语义
// ——即使 Redis 键丢失，心跳仍能重建索引，避免「用户实际在线但查询为离线」、
// 跨节点路由 GetUserNodes 返回空的问题
//
// 断开竞态保护：removeClientUnsafe 中 closeClientChannel(MarkClosed) 先于
// removeOnlineStatusFromRedis(SetClientOffline) 执行，故 flush 时用 IsClosed()
// 过滤已断开客户端，避免为已下线客户端重新写入在线索引
func (h *Hub) processHeartbeatRedisUpdates() {
	h.wg.Add(1)
	defer h.wg.Done()

	// 按 clientID 去重收集客户端（同一客户端多次心跳只保留最新指针）
	batch := make(map[string]*models.Client, 256)
	// 心跳批量续期在线索引的 flush 间隔：从 OnlineStatus.HeartbeatRefreshInterval 读取（默认 2s）
	// 可配置以应对不同负载场景（高并发可调小到 500ms 缩短索引续期窗口；该间隔仅影响续期与
	// 键缺失时的自愈重建，首次注册的索引写入由 handleRegister 提交到记录池的
	// statsMgr.SyncOnlineStatus 异步完成，不依赖此 ticker）
	heartbeatRefreshInterval := 2 * time.Second
	if h.config != nil && h.config.RedisRepository != nil && h.config.RedisRepository.OnlineStatus != nil {
		heartbeatRefreshInterval = mathx.IfNotZero(h.config.RedisRepository.OnlineStatus.HeartbeatRefreshInterval, 2*time.Second)
	}
	ticker := time.NewTicker(heartbeatRefreshInterval)
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
		// （500 万客户端/2s 周期 ÷ 100/批 = 5 万次 Eval，串行不可行）
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

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				// 轻量续期：跳过 JSON 序列化/压缩/SETEX，仅刷新 TTL 与索引；
				// client:<id> 键缺失的由 repo 内部走全量重建（保留原自愈语义）
				if err := h.onlineStatusRepo.RenewClientsOnline(ctx, chunk); err != nil {
					h.logger.DebugKV("心跳续期 Redis 在线状态失败",
						"count", len(chunk), "error", err)
				}
			}()
		}
		wg.Wait()
	}

	for {
		select {
		case client := <-h.heartbeatRedisCh:
			if client == nil {
				continue
			}
			batch[client.ID] = client
			// 批量到达阈值时提前刷写
			if len(batch) >= 256 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-h.ctx.Done():
			flush()
			return
		}
	}
}

// flushStatsCounters 将原子计数器累积的统计刷写到 Redis
// 替代每次消息/广播创建 goroutine 更新 Redis 的模式
// （计数器归 messaging 域，经 Swap 系列方法取出并清零）
func (h *Hub) flushStatsCounters() {
	if h.statsRepo == nil {
		return
	}

	// 原子读取并重置
	msgs := h.messagingMgr.SwapMessageSentCount()
	bcasts := h.messagingMgr.SwapBroadcastSentCount()

	if msgs > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := h.statsRepo.IncrementMessagesSent(ctx, h.nodeID, msgs); err != nil {
			h.logger.DebugKV("批量更新消息统计失败", "error", err)
		}
		cancel()
	}
	if bcasts > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := h.statsRepo.IncrementBroadcastsSent(ctx, h.nodeID, bcasts); err != nil {
			h.logger.DebugKV("批量更新广播统计失败", "error", err)
		}
		cancel()
	}
}

// FlushStats 公共方法：立即将内存中累积的消息/广播统计计数器刷写到 Redis
// 正常运行时由 30 秒定时器自动刷写，测试或需要即时统计的场景可手动调用
func (h *Hub) FlushStats() {
	h.flushStatsCounters()
}

// cleanupExpiredAck 清理过期的ACK消息（经 messaging 域管理器触发，未注入 ACK 管理器时 no-op）
func (h *Hub) cleanupExpiredAck() {
	cleaned := h.messagingMgr.CleanupExpiredAcks()
	if cleaned > 0 {
		h.logger.InfoKV("清理过期ACK消息",
			"count", cleaned,
			"node_id", h.nodeID,
		)
	}
}

// cleanupExpiredMessageRecords 清理过期的消息记录
func (h *Hub) cleanupExpiredMessageRecords() {
	if h.messageSink == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deletedCount, err := h.messageSink.DeleteExpired(ctx)
	if err != nil {
		h.logger.WarnKV("清理过期消息记录失败",
			"error", err,
			"node_id", h.nodeID,
		)
		return
	}

	if deletedCount > 0 {
		h.logger.InfoKV("清理过期消息记录",
			"count", deletedCount,
			"node_id", h.nodeID,
		)
	}
}

// WaitForStart 等待Hub启动完成
// 这个方法对于用户来说很重要，确保Hub完全启动后再进行操作
func (h *Hub) WaitForStart() {
	<-h.startCh
}

// WaitForStartWithTimeout 带超时的等待Hub启动
func (h *Hub) WaitForStartWithTimeout(timeout time.Duration) error {
	select {
	case <-h.startCh:
		return nil
	case <-time.After(timeout):
		return models.ErrHubStartupTimeout
	}
}

// Wait 等待所有显式登记进 h.wg 的后台 goroutine 退出（SafeShutdown 完成后返回）
// 当前登记项：Run 事件循环、心跳 Redis worker（processHeartbeatRedisUpdates）、
// 注册/注销异步任务（Register/Unregister 的 go 协程）
// 注意：syncx.Go 启动的订阅/drain/扫描协程未绑定 h.wg，依赖 h.ctx 取消退出；
// 消息域读写泵由 messaging.Manager.Wait 单独等待（不同接收者，互不冲突）
func (h *Hub) Wait() {
	h.wg.Wait()
}

// SafeShutdown 安全关闭Hub，确保所有操作完成
func (h *Hub) SafeShutdown() error {
	// 检查是否已经关闭
	if h.shutdown.Load() {
		h.logger.Debug("Hub已经关闭，跳过重复关闭操作")
		return nil
	}

	// 设置关闭标志（先标记避免新操作进入）
	if !h.shutdown.CompareAndSwap(false, true) {
		return nil // 已经在关闭中
	}

	// 停止 workerPool：defer 保证在 h.wg.Wait() 完成后才执行（LIFO），
	// 此时所有 wg 管理的 goroutine 已退出，不会再向 workerPool 提交任务，
	// 可安全关闭 4 个子池（Message/Callback/Record/Distributed），避免 worker goroutine 泄漏
	defer h.workerPool.Stop()

	// 关闭 PubSub（等待全部 goroutine 退出后释放底层 Redis 连接）
	defer func() {
		if h.pubsub != nil {
			if err := h.pubsub.Close(); err != nil {
				h.logger.WarnKV("关闭 PubSub 失败", "error", err, "node_id", h.nodeID)
			}
		}
	}()

	shutdownStart := time.Now()

	h.logger.InfoKV("🛑 开始安全关闭 Hub", "node_id", h.nodeID)

	// 停止心跳统计批量更新器，刷写剩余数据（Stop 内部 flush 剩余数据并等待完成）
	if h.heartbeatBatcher != nil {
		h.heartbeatBatcher.Stop()
	}

	// 停止心跳时间轮（停止所有 worker，不再触发超时注销）
	if h.heartbeatTimer != nil {
		h.heartbeatTimer.Stop()
	}

	// 停止跨节点 ACK 超时时间轮（停止所有 worker，pending 超时任务由 5min 兜底扫描接管）
	if h.ackTimeoutTimer != nil {
		h.ackTimeoutTimer.Stop()
	}

	// 停止消息统计批量更新器，刷写剩余数据
	if h.messageStatsBatcher != nil {
		h.messageStatsBatcher.Stop()
	}

	// 停止观察者通知批量处理器，flush 剩余通知
	if h.observerBatcher != nil {
		h.observerBatcher.Stop()
	}

	// （旧 clusterBatcher 已随跨节点分发域化移除：gRPC 直连 + PubSub 定向发布，无批量处理器）

	// 停止准入闸门水位评估循环（写泵/投递埋点为 atomic add，无需 flush）
	if gate := h.admission.Load(); gate != nil {
		gate.Stop()
	}

	// 刷写消息/广播原子计数器到 Redis，避免关闭时统计丢失
	h.flushStatsCounters()

	time.Sleep(50 * time.Millisecond)

	// 并行关闭所有客户端连接
	allClients := h.shardedRegistry.GetAllClients()
	h.logger.InfoKV("并行关闭所有客户端连接", "node_id", h.nodeID, "client_count", len(allClients))
	h.shutdownAllClientsParallel(allClients)

	// 批量清理 Redis 在线状态和 DB 连接记录
	// 替代 removeClientUnsafe 中的逐个调用，用 worker pool 限流避免 goroutine 爆炸
	h.logger.InfoKV("批量清理 Redis 在线状态和连接记录", "node_id", h.nodeID)
	h.batchCleanupOnShutdown(allClients)

	// 停止消息记录攒批 outbox，flush 剩余记录到 DB（先于状态更新器 Stop，保 INSERT→UPDATE 落库顺序）
	if h.messageRecordOutbox != nil {
		h.logger.InfoKV("flush 消息记录 outbox", "node_id", h.nodeID)
		h.messageRecordOutbox.Stop()
	}

	// 停止消息状态批量更新器，flush 剩余状态更新到 DB
	// 在 h.cancel() 之前调用，确保 flush 时 h.ctx 仍然有效
	if h.statusUpdater != nil {
		h.logger.InfoKV("flush 消息状态更新", "node_id", h.nodeID)
		h.statusUpdater.Stop()
	}

	// 🔗 停止节点间 gRPC 通信（注销节点、关闭服务端与客户端连接池）
	// 在 h.cancel() 之前调用，确保注销请求的 context 仍可用
	h.logger.InfoKV("停止节点 gRPC 通信", "node_id", h.nodeID)
	h.stopNodeGRPC()

	// 取消context（通知所有 goroutine 停止）
	h.cancel()

	// 等待一小段时间让goroutine有机会响应取消信号
	time.Sleep(10 * time.Millisecond)

	// 使用原子计数器快速计算超时时间
	// 基础超时：从配置读取（默认5秒）
	// 最大超时：从配置读取（默认60秒）
	// 动态计算：基础超时 + (连接数 * 10ms)，但不超过最大超时
	baseTimeout := mathx.IfNotZero(h.config.ShutdownBaseTimeout, 5*time.Second)
	maxTimeout := mathx.IfNotZero(h.config.ShutdownMaxTimeout, 60*time.Second)

	// 使用前面获取的 allClients 快照长度
	// 注意：此时 registry 已被 shutdownAllClientsParallel 清空，
	// 不能用 h.shardedRegistry.GetClientCount()（会返回 0 导致超时计算失效）
	totalClients := len(allClients)

	// 每个连接增加10ms超时时间，限制在最大超时范围内
	calculatedTimeout := mathx.IfClamp(
		baseTimeout+time.Duration(totalClients)*10*time.Millisecond,
		0,
		maxTimeout,
	)

	// 等待所有goroutine完成，带超时保护
	h.logger.InfoKV("等待所有协程完成", "node_id", h.nodeID, "timeout", calculatedTimeout.String())
	done := make(chan struct{})
	syncx.Go().
		OnPanic(func(r any) {
			h.logger.ErrorKV("WaitGroup等待崩溃", "panic", r, "stack", string(debug.Stack()))
		}).
		Exec(func() {
			h.wg.Wait()
			// 消息域读写泵（messaging.Manager 自持 wg）：连接已全部关闭、ctx 已取消，泵随之退出
			h.messagingMgr.Wait()
			close(done)
		})

	select {
	case <-done:
		// 正常关闭
		var totalConnections, messagesSent, broadcastsSent int64
		if h.statsRepo != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			stats, _ := h.statsRepo.GetNodeStats(ctx, h.nodeID)

			if stats != nil {
				totalConnections = stats.TotalConnections
				messagesSent = stats.MessagesSent
				broadcastsSent = stats.BroadcastsSent
			}

			// 优雅退出时清理本节点统计（DEL stats/heartbeat + SREM nodes 集合）：
			// 滚动发布中旧 Pod 退出后其 nodeID 立即从集群消失，保证集合成员数 = 存活 Pod 数；
			// 崩溃场景（无法执行此处）由 GetAllNodesStats 惰性剔除兜底
			if err := h.statsRepo.CleanupNodeStats(ctx, h.nodeID); err != nil {
				h.logger.WarnKV("清理节点统计失败", "node_id", h.nodeID, "error", err)
			}
			cancel()
		}

		h.logger.InfoKV("✅ Hub 安全关闭成功",
			"node_id", h.nodeID,
			"shutdown_duration_ms", time.Since(shutdownStart).Milliseconds(),
			"total_connections", totalConnections,
			"messages_sent", messagesSent,
			"broadcasts_sent", broadcastsSent,
		)
		return nil

	case <-time.After(calculatedTimeout):
		// 超时关闭
		h.logger.ErrorKV("⚠️ Hub 关闭超时",
			"node_id", h.nodeID,
			"timeout", calculatedTimeout.String(),
			"shutdown_duration_ms", time.Since(shutdownStart).Milliseconds(),
		)
		return models.ErrHubShutdownTimeout
	}
}

// Shutdown 关闭Hub（旧API，兼容性方法）
func (h *Hub) Shutdown() {
	_ = h.SafeShutdown()
}

// cleanupExpiredOnlineStatus 清理过期的在线状态数据
func (h *Hub) cleanupExpiredOnlineStatus() {
	cleaned, err := h.onlineStatusRepo.CleanupExpired(h.ctx, h.nodeID)
	if err != nil {
		h.logger.ErrorKV("清理在线状态失败",
			"error", err,
			"node_id", h.nodeID,
		)
		return
	}

	if cleaned > 0 {
		h.logger.InfoKV("清理过期在线状态",
			"count", cleaned,
			"node_id", h.nodeID,
		)
	}
}

// shutdownAllClientsParallel 并行关闭所有客户端连接
func (h *Hub) shutdownAllClientsParallel(clients []*models.Client) {
	if len(clients) == 0 {
		return
	}
	syncx.ParallelForEachSlice(clients, func(i int, client *models.Client) {
		h.removeClientUnsafe(client)
	})
}

// batchCleanupOnShutdown 批量清理 Redis 在线状态和 DB 连接记录
func (h *Hub) batchCleanupOnShutdown(clients []*models.Client) {
	if len(clients) == 0 {
		return
	}

	// 统一设置活跃连接数为 0（只调一次，替代正常路径中每客户端一次的防抖同步）
	if h.statsRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := h.statsRepo.SetActiveConnections(ctx, h.nodeID, 0); err != nil {
			h.logger.WarnKV("shutdown: 设置活跃连接数为0失败", "error", err)
		}
		cancel()
	}

	// 批量清理 Redis 在线状态
	if h.onlineStatusRepo != nil {
		syncx.ParallelForEachSlice(clients, func(i int, client *models.Client) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := h.onlineStatusRepo.SetClientOffline(ctx, client); err != nil {
				h.logger.DebugContextKV(client.Context, "shutdown: 清理 Redis 在线状态失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
			}
		})
	}

	// 批量更新连接记录为断开
	if h.connectionStore != nil {
		syncx.ParallelForEachSlice(clients, func(i int, client *models.Client) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.connectionStore.MarkDisconnected(ctx, client.ID, models.DisconnectReasonServerShutdown, 1001); err != nil {
				h.logger.DebugContextKV(client.Context, "shutdown: 更新连接断开记录失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
			}
		})
	}

	// 批量质量终评（读 connect.duration 算 FinalScore 写 quality_score）
	if h.connectionQualityStore != nil {
		syncx.ParallelForEachSlice(clients, func(i int, client *models.Client) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.connectionQualityStore.FinalizeOnDisconnect(ctx, client.ID); err != nil {
				h.logger.DebugContextKV(client.Context, "shutdown: 质量终评失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
			}
		})
	}

	h.logger.InfoKV("shutdown: 批量清理完成", "client_count", len(clients))
}

// ============================================================================
// 高频合并器 drain（合并器本体在 overload 域，节拍与投递归编排层）
// ============================================================================

// startCoalescerDrain 启动合并器 drain ticker（Run 时调用，ctx 结束自动退出）
//
// 周期：每 50ms Drain 一次 latest-wins 快照，逐条按 Receiver 走 P2P 投递
// （TrySend：满则高频语义丢弃——最新值本就只需送达一次）
func (h *Hub) startCoalescerDrain() {
	syncx.Go(h.ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("合并器 drain panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
		}).
		Exec(func() {
			ticker := time.NewTicker(coalescerDrainInterval)
			defer ticker.Stop()

			for {
				select {
				case <-h.ctx.Done():
					return
				case <-ticker.C:
					// atomic load：热替换后的新合并器实例即刻生效（nil 时本轮跳过）
					c := h.ephemeralCoalescer.Load()
					if c == nil {
						continue
					}
					batch := c.Drain()
					for _, msg := range batch {
						h.deliverCoalesced(msg)
					}
				}
			}
		})
}

// deliverCoalesced 投递一条合并后的高频消息（按 Receiver 查找在线客户端）
func (h *Hub) deliverCoalesced(msg *models.HubMessage) {
	if msg == nil || msg.Receiver == "" {
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorContextKV(h.ctx, "高频合并消息序列化失败",
			"message_id", msg.MessageID,
			"error", err,
		)
		return
	}

	h.shardedRegistry.ForEachUserClientFiltered(msg.Receiver, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *models.Client) bool {
		if client.IsClosed() {
			return true
		}
		if client.ConnectionType == models.ConnectionTypeSSE {
			client.TrySendSSE(msg)
		} else {
			// 高频语义：满即弃（下一条同 key 消息自然覆盖；无需转离线）
			if client.TrySend(data) {
				if gate := h.admission.Load(); gate != nil {
					gate.OnDelivered()
				}
			}
		}
		return true
	})
}

// ============================================================================
// 慢消费者扫描（治理逻辑已域化至 connection.SlowConsumerScanner）
// ============================================================================

// startSlowConsumerScanner 启动慢消费者扫描器（Run 时调用）
//
// 周期 = 准入评估周期 × 2（与水位评估同源节拍，避免扫描与评估完全同步造成的
// 周期性毛刺）；三级递进治理（记录 → 告警 → 驱逐）在 connection 域内实现，
// 驱逐动作经 EvictHook 回调本文件 OnSlowConsumerEvicted 落地
func (h *Hub) startSlowConsumerScanner() {
	// atomic load：与 SetOverloadPolicy 热替换并发安全（nil 时用默认节拍）
	interval := time.Second
	if gate := h.admission.Load(); gate != nil && gate.EvalInterval() > 0 {
		interval = 2 * gate.EvalInterval()
	}
	scanner := connection.NewSlowConsumerScanner(h.shardedRegistry, h, interval, h.logger)
	syncx.Go(h.ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("慢消费者扫描器 panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
		}).
		Exec(func() {
			scanner.Start(h.ctx)
		})
}

// OnSlowConsumerEvicted 实现 connection.EvictHook：慢消费者驱逐动作
//
// 扫描器已完成消息保全（SendChan 残留移交 ACK 超时链路兜底）与告警日志，
// 此处只做编排层三件事：
//  1. 过载漏斗埋点（slow_evict）
//  2. KickOut 控制消息通知客户端驱逐理由（控制通道独立 lane，不被业务洪峰淹没）
//  3. Unregister 断链（断链不丢消息）
func (h *Hub) OnSlowConsumerEvicted(client *models.Client, _ int, ratio float64) {
	h.overloadMetrics.RecordSlowEvict()

	kickMsg := models.NewHubMessage().
		SetMessageType(models.MessageTypeKickOut).
		SetSender("system").
		SetSenderType(models.UserTypeSystem).
		SetReceiver(client.UserID).
		SetReceiverType(client.UserType).
		SetContent("slow consumer evicted").
		WithContentExtra("reason", "backlog_ratio").
		WithContentExtra("backlog_ratio", ratio)

	connection.NewControlLane(h.logger).SendControlMessage(client, kickMsg)

	// Unregister（KickOut 控制通道满时 ControlLane 内部已降级直接断链）
	h.Unregister(client)
}
