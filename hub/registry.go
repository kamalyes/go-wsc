/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-05 10:22:41
 * @FilePath: \go-wsc\hub\registry.go
 * @Description: Hub 连接生命周期 —— 注册/注销/心跳管理
 *
 * 实现 transport.Registrar 端口契约（Register 异步 / RegisterSync 同步 /
 * Unregister / IsShutdown / SendRegisteredMessage）与 messaging.Host 的
 * Unregister / HandleHeartbeat：注册表操作全部走 shardedRegistry 分片锁，
 * WebSocket 心跳超时由分片时间轮 O(1) 管理（取消旧任务 + 注册新任务），
 * SSE 客户端不发送 PING，由 checkHeartbeat 定期兜底扫描。
 * 读写泵与协议级 PING 处理已由 transport/messaging 域接管，此处不迁移。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 客户端注册/注销（transport.Registrar 端口契约）
// ============================================================================

// Register 异步注册客户端（WS 升级路径：升级后立即返回，注册在后台完成）
// client.Context 在 http_upgrade 升级时已注入 trace_id，内部直接用实现全链路追踪
func (h *Hub) Register(client *models.Client) {
	if client == nil {
		return
	}
	h.logger.DebugContextKV(client.Context, "客户端注册请求",
		"client_id", client.ID,
		"user_id", client.UserID,
	)
	// 注册任务计入 h.wg：SafeShutdown 的 h.wg.Wait() 需等待在途注册完成，
	// 避免半注册连接在 shutdown 批量清理后才加入注册表造成泄漏
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.handleRegister(client)
	}()
}

// RegisterSync 同步注册客户端（SSE 路径：注册完成才进写循环，避免首条消息竞态丢失）
func (h *Hub) RegisterSync(client *models.Client) {
	if client == nil {
		return
	}
	h.handleRegister(client)
}

// Unregister 异步注销客户端（写循环退出后的兜底清理，幂等）
func (h *Hub) Unregister(client *models.Client) {
	if client == nil {
		return
	}
	h.logger.DebugContextKV(client.Context, "客户端注销请求",
		"client_id", client.ID,
		"user_id", client.UserID,
	)
	// 注销任务计入 h.wg：SafeShutdown 的 h.wg.Wait() 需等待在途注销完成，
	// 避免注册表条目/心跳时间轮任务在 shutdown 清理后被残留移除操作改动
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.handleUnregister(client)
	}()
}

// SendRegisteredMessage 发送注册成功确认消息（配置启用时由传输域调用）
// 经消息域投递，走客户端写泵统一写出（单写者模式）
func (h *Hub) SendRegisteredMessage(client *models.Client) {
	if client == nil {
		return
	}
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeClientRegistered).
		SetSender(models.UserTypeSystem.String()).
		SetSenderType(models.UserTypeSystem).
		SetReceiver(client.UserID).
		SetReceiverType(client.UserType)
	h.messagingMgr.SendToClient(client.Context, client, msg)
}

// ============================================================================
// 注册/注销内部实现
// ============================================================================

// handleRegister 处理客户端注册（内部方法）
// ctx 兜底：生产中 http upgrade 时已注入 client.Context；直接构造 Client 调用的
// 场景（如测试、集成）传 nil 时降级为 h.ctx，保证下游不因 nil ctx panic
func (h *Hub) handleRegister(client *models.Client) {
	ctx := client.Context
	if ctx == nil {
		ctx = h.ctx
		client.Context = ctx
	}
	defer syncx.RecoverWithHandler(func(r interface{}) {
		h.logger.ErrorContextKV(ctx, "handleRegister panic，清理半注册连接",
			"client_id", client.ID,
			"user_id", client.UserID,
			"panic", r,
		)
		// panic 点可能在注册表加入/心跳任务调度之后：不清理会留下
		// "已完成 Upgrade 却无法读取心跳"的幽灵连接
		h.cleanupHalfRegisteredClient(client)
	})

	// 双重检查：如果 Hub 正在关闭，拒绝注册
	if h.shutdown.Load() {
		h.logger.WarnContextKV(ctx, "Hub 正在关闭，拒绝注册",
			"client_id", client.ID,
			"user_id", client.UserID)
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
		return
	}

	// ================================================================
	// 客户端初始化（无锁，client 尚未共享）
	// ================================================================
	client.NodeID = h.nodeID
	client.NodeIP = h.config.NodeIP
	client.NodePort = h.config.NodePort

	// appID 归一化：空→DefaultAppID（入口层统一归一化，ClientMatchesEnvelope 严格匹配要求）
	client.AppID = constants.NormalizeAppID(client.AppID)
	// 命名空间归一化：非观察者补默认（观察者保留空，表示全局观察所有命名空间）
	if client.UserType != models.UserTypeObserver {
		client.Namespace = constants.NormalizeNamespace(client.Namespace)
	}

	// 初始化客户端时间戳（原子更新，已有值则保留——断线重连场景）
	now := time.Now()
	client.ConnectedAt = mathx.IfNotZero(client.ConnectedAt, now)
	client.SetLastHeartbeat(mathx.IfNotZero(client.GetLastHeartbeat(), now))
	client.SetLastSeen(mathx.IfNotZero(client.GetLastSeen(), now))

	// ================================================================
	// 临界区 - 仅注册表操作（shardedRegistry 分片锁，粒度细）
	// 主存储 + 分类索引（SSE/Observer/Agent）由 AddClient 内部原子完成
	// ================================================================
	// 首连守卫：注册前用户在本节点无活跃连接（0→1）才触发离线回放，
	// 多端同时上线仅首条连接拉取一次；读数与 AddClient 非原子的极小竞态窗口内
	// 重复触发也无害（drain 破坏性读 + 推送成功删 MySQL，天然幂等去重）
	wasFirstConnection := h.shardedRegistry.GetUserClientCount(client.UserID) == 0

	h.shardedRegistry.AddClient(client)

	// 多端登录治理（迁移自备份 registry.go）：AllowMultiLogin=false 踢旧连接、
	// MaxConnectionsPerUser 达上限踢最旧连接（均异步注销，不阻塞注册主流程）
	h.handleMultiLoginPolicy(client)

	// ⏰ 在时间轮上调度心跳超时任务（仅 WebSocket；SSE 由 checkHeartbeat 兜底扫描）
	h.scheduleHeartbeatTimeout(client)

	// ================================================================
	// 非临界区 - IO 操作异步执行（WorkerPool 控制并发）
	// ================================================================

	// 创建连接记录（内存对象，供异步保存 + 连接回调使用）
	record := h.createConnectionRecord(client)

	// 🔥 跨节点迁移检测：读取 clientID 旧归属节点（约 1 次 Redis 读，单机模式内部早返回）。
	// 必须在下方记录池任务的 SyncOnlineStatus 覆写 owner key 之前读取，否则读到的
	// 是本节点自己；断线重连漂移到本节点时旧节点可能残留同 clientID 幽灵连接，
	// 消息按索引路由到旧节点会扑空 → 检测到迁移后通知旧节点回收
	migratedFromNode := h.detectClientMigration(ctx, client)

	// 统计同步 + 在线索引写 Redis + 连接记录落库（提交到记录池，可丢弃）
	// SyncOnlineStatus：注册即写在线索引，其他节点 checkUserOnline 直查 Redis 即可见，
	// 不依赖心跳续期 ticker 的自愈重建
	h.workerPool.TrySubmitRecord(func() {
		h.statsMgr.LogClientConnection(client)
		h.statsMgr.SyncOnlineStatus(client)
		h.saveConnectionRecord(ctx, record)
		// 旧归属其他节点 → 通知旧节点回收幽灵连接。须在 SyncOnlineStatus 写入本节点
		// owner 之后调用（旧节点清理受 Lua 归属校验保护，仅清自身集合不动共享索引），
		// 与 SyncOnlineStatus 同闭包顺序执行保证时序；任务被丢弃时由旧节点连接超时清理兜底
		if migratedFromNode != "" && migratedFromNode != h.nodeID {
			h.notifyClientReclaim(ctx, client, migratedFromNode)
		}
	})

	// 调用客户端连接回调（提交到回调池，不可丢弃）
	// 传 record 让调用方获取 connect 身份+会话生命周期做额外落盘
	if h.clientConnectCallback != nil {
		cb := h.clientConnectCallback
		h.workerPool.SubmitCallback(ctx, func() {
			if err := cb(ctx, client, record); err != nil {
				h.logger.ErrorContextKV(ctx, "客户端连接回调执行失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
			}
		})
	}

	h.logger.InfoContextKV(ctx, "客户端注册完成",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"connection_type", client.ConnectionType,
		"total_clients", h.shardedRegistry.GetClientCount(),
	)

	// 离线消息回放（用户从全离线转上线，仅首条活跃连接触发一次；提交到回调池不可丢弃，
	// 不阻塞注册主流程）。两阶段全量补发在 messaging 域内执行（见 PushOfflineMessages），
	// 状态全部在注入的共享存储（Redis 队列 + RDBMS），Deployment 滚动更新 / Pod 重新
	// 调度下跨 Pod 可见；未注入离线处理器时域内 nil-safe 跳过。
	// 有成功推送时触发应用回调（上游据此感知离线消息已送达）
	//
	// 🔒 首连门闩：回放是异步任务，期间新到的实时消息会被 replayGate 暂存，
	// 回放完成后按序补投，保证用户收到的消息顺序 = 真实时序（无门闩时
	// 实时消息会先于离线历史消息进入 sendChan，重连场景乱序）
	if wasFirstConnection {
		h.messagingMgr.BeginUserReplay(client.UserID)
		h.workerPool.SubmitCallback(ctx, func() {
			// defer 保证回放 panic 也必开闸，实时投递不会永久堆积
			defer h.messagingMgr.EndUserReplay(client.UserID)
			pushedIDs, failedIDs := h.messagingMgr.PushOfflineMessages(client.Context, client)
			if len(pushedIDs) > 0 && h.offlineMessagePushCallback != nil {
				h.offlineMessagePushCallback(client.UserID, pushedIDs, failedIDs)
			}
		})
	}
}

// handleUnregister 处理客户端注销（内部方法）
func (h *Hub) handleUnregister(client *models.Client) {
	ctx := client.Context
	if ctx == nil {
		ctx = h.ctx
	}

	// Phase 1: 临界区 - 从注册表移除（shardedRegistry 分片锁）
	removed := h.shardedRegistry.RemoveClient(client.ID, client.UserID)
	if removed == nil {
		return
	}

	// ⏰ 取消时间轮上的心跳超时任务（客户端已注销，不再需要超时检测）
	h.cancelHeartbeatTimeout(client.ID)

	// 关键修复：验证客户端指针一致性
	// TemporalHasher 在时间窗口内为相同用户+设备生成相同 ClientID，
	// 断线重连时新客户端会覆盖旧客户端的注册表条目，
	// 旧客户端的读协程退出时调用 Unregister 不应删除新客户端
	if removed != client {
		h.shardedRegistry.AddClient(removed)
		h.logger.InfoContextKV(ctx, "客户端已被新连接替换，跳过旧客户端的注销",
			"client_id", client.ID,
			"user_id", client.UserID,
		)
		return
	}

	// Phase 2: 关闭通道与连接（幂等，重复调用无副作用）
	h.closeClientChannel(client)
	h.closeClientConnection(client)

	h.logger.InfoContextKV(ctx, "客户端断开连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"remaining_connections", h.shardedRegistry.GetClientCount(),
	)

	// Phase 3: 回调与记录落库（异步，不阻塞注销主流程）

	// 调用断开回调（提交到回调池，不可丢弃）
	if h.clientDisconnectCallback != nil {
		cb := h.clientDisconnectCallback
		h.workerPool.SubmitCallback(ctx, func() {
			if err := cb(ctx, client, models.DisconnectReasonClientRequest); err != nil {
				h.logger.ErrorContextKV(ctx, "客户端断开回调执行失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
			}
		})
	}

	// 标记连接断开记录（提交到记录池，可丢弃）
	h.workerPool.TrySubmitRecord(func() {
		h.markConnectionDisconnected(ctx, client)
	})
}

// ============================================================================
// 多端登录策略处理（迁移自备份 registry.go，适配 AddClient 前置调用）
// ============================================================================

// handleMultiLoginPolicy 统一处理多端登录策略（内部方法）
// 根据配置决定是否允许多端登录、是否限制连接数：
//   - AllowMultiLogin=false：踢掉该用户全部旧连接（同信封快速检查，无旧连接零开销）
//   - MaxConnectionsPerUser>0：达到上限时踢掉最不活跃（最旧心跳）的旧连接
//
// 与备份实现的差异（适配新树调用点）：新树 handleRegister 已先 AddClient 再调本方法，
// 故踢人时排除新连接自身（newClient），且不再需要备份中的 addNewClient / 同 clientID
// 替换清理（同 clientID 覆盖已由 handleUnregister/removeClientUnsafe 的指针一致性校验保护）。
// 多端登录策略按 appID+namespace 信封隔离：不同应用/命名空间的连接互不影响
// （app-A 的连接数不挤占 app-B 的配额）
func (h *Hub) handleMultiLoginPolicy(newClient *models.Client) {
	ctx := newClient.Context
	userID := newClient.UserID
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)

	// O(1) 快速检查同信封下用户是否有现有客户端（原子计数器，无锁）
	if !h.shardedRegistry.HasUser(userID, appID, ns) {
		return
	}

	h.logger.DebugContextKV(ctx, "处理多端登录策略",
		"user_id", userID,
		"new_client_id", newClient.ID,
		"allow_multi_login", h.config.AllowMultiLogin,
		"max_connections_per_user", h.config.MaxConnectionsPerUser)

	// 不允许多端登录：踢掉所有旧连接（新连接已入表，排除自身）
	if !h.config.AllowMultiLogin {
		// 使用 ForEachUserClient 持读锁零拷贝收集客户端（消除锁外遍历数据竞争）
		var clients []*models.Client
		h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *models.Client) bool {
			if client != newClient {
				clients = append(clients, client)
			}
			return true
		})

		h.logger.InfoContextKV(ctx, "不允许多端登录，踢掉所有旧连接",
			"user_id", userID,
			"old_connections", len(clients))

		h.kickExistingClients(clients)
		return
	}

	// 允许多端登录，但有连接数限制（计数含新连接：old+1 <= max 等价于备份的 old < max）
	if h.config.MaxConnectionsPerUser > 0 {
		currentCount := h.shardedRegistry.GetUserClientCount(userID)

		// 未达上限，无需踢人
		if currentCount <= h.config.MaxConnectionsPerUser {
			return
		}

		// 达到上限：踢掉最早的旧连接
		h.logger.InfoContextKV(ctx, "达到连接数上限，踢掉最早的连接",
			"user_id", userID,
			"current_count", currentCount,
			"max_allowed", h.config.MaxConnectionsPerUser)

		h.kickOldestConnection(userID, newClient)
	}
	// 允许多端登录且无限制：无需处理
}

// kickExistingClients 踢掉现有客户端（接收切片，调用方负责通过 ForEachUserClient 持锁收集）
// 逐连接用各自 client.Context 保留连接级 trace_id
func (h *Hub) kickExistingClients(clients []*models.Client) {
	for _, client := range clients {
		h.kickClientWithNotification(client, models.DisconnectReasonForceOffline, "您的账号在其他设备登录，当前连接将被断开")

		h.logger.InfoContextKV(client.Context, "踢出旧连接",
			"user_id", client.UserID,
			"client_id", client.ID,
			"reason", models.DisconnectReasonForceOffline,
		)
	}
}

// kickOldestConnection 踢掉最不活跃的旧连接（基于最后心跳时间，排除 exclude 指定的新连接）
// 使用 ForEachUserClient 持读锁遍历，消除锁外遍历 map 的数据竞争
func (h *Hub) kickOldestConnection(userID string, exclude *models.Client) {
	var oldestClient *models.Client
	var oldestTime time.Time

	// 持读锁遍历找出最久没有心跳的客户端
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *models.Client) bool {
		if client == exclude {
			return true
		}
		heartbeat := client.GetLastHeartbeat()
		if oldestClient == nil || heartbeat.Before(oldestTime) {
			oldestClient = client
			oldestTime = heartbeat
		}
		return true
	})

	if oldestClient == nil {
		return
	}

	h.logger.InfoContextKV(oldestClient.Context, "踢掉最不活跃的连接",
		"client_id", oldestClient.ID,
		"user_id", oldestClient.UserID,
		"last_heartbeat", oldestClient.GetLastHeartbeat(),
		"connected_at", oldestClient.ConnectedAt,
	)

	h.kickClientWithNotification(oldestClient, models.DisconnectReasonForceOffline, "连接数已达上限，当前连接将被断开")
}

// ============================================================================
// 心跳处理（messaging.Host 端口 + 时间轮 O(1) 超时管理）
// ============================================================================

// HandleHeartbeat 处理心跳消息（连接域时间轮续期 + 统计刷新）
// 流程：前置回调 → 续期 → Redis 异步续期通道 → 后置回调 → 统计
func (h *Hub) HandleHeartbeat(client *models.Client) {
	// 检查客户端是否已关闭（防止处理已断开客户端的心跳）
	if client == nil || client.IsClosed() {
		return
	}

	// 触发心跳前置回调，返回 false 则跳过后续心跳处理
	if h.beforeHeartbeatCallback != nil {
		if !h.beforeHeartbeatCallback(client) {
			return
		}
	}

	// 更新心跳请求时间（内存）+ O(1) 续期时间轮超时任务
	h.touchHeartbeat(client, time.Now())

	// 异步续期 Redis 在线索引与跨节点路由（不阻塞心跳主流程）
	// 单 goroutine worker 消费 channel，满则丢弃（心跳下次还会来）
	if h.onlineStatusRepo != nil {
		select {
		case h.heartbeatRedisCh <- client:
		default:
			// channel 满，跳过本次 Redis 更新
		}
	}

	// 触发心跳后置回调
	if h.afterHeartbeatCallback != nil {
		h.afterHeartbeatCallback(client)
	}

	// 异步追踪心跳统计（不阻塞主流程）
	h.statsMgr.TrackHeartbeatStats(client)
}

// touchHeartbeat 刷新客户端心跳（协议级 PING 与应用层心跳共用同一保活路径）
// 更新内存时间戳 + O(1) 刷新时间轮超时任务（取消旧任务 + 调度新任务）
func (h *Hub) touchHeartbeat(client *models.Client, now time.Time) {
	client.SetLastHeartbeat(now)
	client.SetLastSeen(now)

	// WebSocket 客户端超时由时间轮管理；SSE 客户端由 checkHeartbeat 扫描兜底
	if h.heartbeatTimer == nil || client.ConnectionType == models.ConnectionTypeSSE {
		return
	}
	h.heartbeatTimer.Refresh(client.ID, h.config.ClientTimeout, h.makeHeartbeatTimeoutCallback(client))
}

// ============================================================================
// 时间轮心跳超时管理（替代 O(N) 全量扫描）
// ============================================================================

// scheduleHeartbeatTimeout 在时间轮上调度客户端心跳超时任务
// 仅用于 WebSocket 客户端；SSE 客户端由 checkHeartbeat 扫描兜底
func (h *Hub) scheduleHeartbeatTimeout(client *models.Client) {
	if h.heartbeatTimer == nil || client.ConnectionType == models.ConnectionTypeSSE {
		return
	}
	h.heartbeatTimer.ScheduleWithKey(client.ID, h.config.ClientTimeout, h.makeHeartbeatTimeoutCallback(client))
}

// cancelHeartbeatTimeout 取消客户端心跳超时任务（注销时调用）
func (h *Hub) cancelHeartbeatTimeout(clientID string) {
	if h.heartbeatTimer == nil {
		return
	}
	h.heartbeatTimer.CancelByKey(clientID)
}

// makeHeartbeatTimeoutCallback 创建心跳超时回调闭包
func (h *Hub) makeHeartbeatTimeoutCallback(client *models.Client) func() {
	return func() {
		h.onHeartbeatTimeout(client)
	}
}

// onHeartbeatTimeout 心跳超时处理：触发超时回调并异步注销客户端
func (h *Hub) onHeartbeatTimeout(client *models.Client) {
	// 客户端已关闭（正常断开），跳过
	if client.IsClosed() {
		return
	}
	// 触发心跳超时回调
	if h.heartbeatTimeoutCallback != nil {
		h.heartbeatTimeoutCallback(client.ID, client.UserID, client.GetLastHeartbeat())
	}
	// 异步注销客户端
	h.Unregister(client)
}

// checkHeartbeat 检查 SSE 客户端心跳超时（兜底机制）
// WebSocket 客户端由 heartbeatTimer O(1) 管理，此处仅扫描 SSE 客户端
// SSE 客户端不发送 PING，无法通过时间轮 Refresh，需定期扫描 LastSeen 判断活跃度
//
// ⚠️ 死锁防御：遍历持有 shard 读锁，先收集超时客户端到本地 slice（mutex 保护
// 并发 append），遍历结束后在锁外统一调用 Unregister
func (h *Hub) checkHeartbeat() {
	now := time.Now()

	// Phase 1：并行持读锁收集 SSE 超时客户端
	var mu sync.Mutex
	var timeouts []*models.Client
	var scanned int64

	h.shardedRegistry.ForEachSSEClientParallel(0, func(_, _ string, client *models.Client) {
		atomic.AddInt64(&scanned, 1)
		// 原子读时间戳（并发安全，无数据竞争）
		if now.Sub(client.GetLastSeen()) > h.config.ClientTimeout {
			mu.Lock()
			timeouts = append(timeouts, client)
			mu.Unlock()
		}
	})

	// Phase 2：锁外批量注销（Unregister 内部 go 异步，均安全）
	for _, client := range timeouts {
		lastActive := client.GetLastSeen()
		h.logger.DebugContextKV(client.Context, "检测到SSE心跳超时，注销客户端",
			"client_id", client.ID,
			"user_id", client.UserID,
			"user_type", client.UserType,
			"last_active", lastActive,
			"inactive_duration", now.Sub(lastActive).String(),
			"timeout_threshold", h.config.ClientTimeout.String(),
		)

		h.Unregister(client)

		if h.heartbeatTimeoutCallback != nil {
			h.heartbeatTimeoutCallback(client.ID, client.UserID, lastActive)
		}
	}

	if n := atomic.LoadInt64(&scanned); n > 0 || len(timeouts) > 0 {
		h.logger.DebugContextKV(h.ctx, "SSE心跳检查完成",
			"scanned", n,
			"timeouts", len(timeouts),
		)
	}
}

// ============================================================================
// 连接记录（构造 + 落库）
// ============================================================================

// createConnectionRecord 构造连接记录（内存对象，供异步保存 + 连接回调使用）
func (h *Hub) createConnectionRecord(client *models.Client) *models.ConnectionRecord {
	record := &models.ConnectionRecord{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		AppID:        client.GetAppID(),
		Namespace:    client.GetNamespace(),
		NodeID:       client.NodeID,
		NodeIP:       client.NodeIP,
		NodePort:     client.NodePort,
		ClientIP:     client.GetClientIP(),
		Protocol:     client.ConnectionType,
		ClientType:   client.ClientType,
		ConnectedAt:  client.ConnectedAt,
		IsActive:     true,
	}

	// 设置 metadata（线程安全读取快照）
	record.Metadata = sqlbuilder.MapAny(client.GetMetadataSnapshot())

	return record
}

// saveConnectionRecord 保存或更新连接记录到数据库（仓储未注入时 no-op）
// ctx 应为 client.Context（带 client 维度的 trace_id），实现异步保存的全链路追踪
func (h *Hub) saveConnectionRecord(ctx context.Context, record *models.ConnectionRecord) {
	if h.connectionStore == nil {
		return
	}
	syncx.Go(ctx).
		WithTimeout(10 * time.Second).
		OnError(func(err error) {
			h.logger.WarnContextKV(ctx, "保存连接记录失败",
				"connection_id", record.ConnectionID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionStore.Upsert(ctx, record)
		})
}

// markConnectionDisconnected 标记连接为已断开（仓储未注入时 no-op）
func (h *Hub) markConnectionDisconnected(ctx context.Context, client *models.Client) {
	if h.connectionStore == nil {
		return
	}
	syncx.Go(ctx).
		WithTimeout(10 * time.Second).
		OnError(func(err error) {
			h.logger.WarnContextKV(ctx, "标记连接断开失败",
				"connection_id", client.ID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.connectionStore.MarkDisconnected(ctx, client.ID, models.DisconnectReasonClientRequest, 0)
		})
}

// ============================================================================
// 内部辅助方法
// ============================================================================

// cleanupHalfRegisteredClient 清理半注册连接（handleRegister panic 兜底）
// 半注册状态：连接已加入注册表 + 心跳超时任务已调度，但注册流程中断
// 幂等安全：各清理步骤对"未执行到"的步骤均为无操作
func (h *Hub) cleanupHalfRegisteredClient(client *models.Client) {
	if client == nil {
		return
	}
	// 移除注册表条目（未注册时无操作）
	h.shardedRegistry.RemoveClient(client.ID, client.UserID)
	// 撤销已调度的心跳超时任务（未调度时无操作），避免重复注销
	h.cancelHeartbeatTimeout(client.ID)
	// 关闭生命周期信号与底层连接（触发客户端立即重连）
	h.closeClientChannel(client)
	h.closeClientConnection(client)
}

// closeClientChannel 关闭客户端发送通道
// 用 DoneCh 通知写循环退出，数据通道（SendChan/SSEMessageCh）永不 close：
//  1. 消除 TrySend 的 chansend 与本函数 closechan 的数据竞态
//  2. 不回收到对象池（竞态窗口内 racing sender 仍可能写入残留消息，复用会跨连接串消息）
//  3. 不置 nil SendChan，避免与写循环的 select 读产生数据竞争
func (h *Hub) closeClientChannel(client *models.Client) {
	// 使用互斥锁保护关闭操作（防并发调用 double close DoneCh/SSECloseCh）
	client.CloseMu.Lock()
	defer client.CloseMu.Unlock()

	// 标记为已关闭，防止其他 goroutine 继续发送
	if client.IsClosed() {
		return // 已经关闭过了
	}
	client.MarkClosed()

	// 关闭生命周期信号（写循环 select 到后退出）
	if client.DoneCh != nil {
		close(client.DoneCh)
	}

	// SSE 客户端关闭专用通道（SSE 写循环已 select SSECloseCh，SSEMessageCh 无需 close）
	if client.ConnectionType == models.ConnectionTypeSSE && client.SSECloseCh != nil {
		close(client.SSECloseCh)
	}
}

// closeClientConnection 关闭 WebSocket 连接
// Hub 关闭（如 K8s 滚动更新）时先发送 1001 GoingAway 控制帧，
// 让客户端识别为服务端主动离开并触发重连，而不是收到 1006 异常断开
func (h *Hub) closeClientConnection(client *models.Client) {
	if client.Conn == nil {
		return
	}

	// Hub 正在关闭时，先发送 1001 GoingAway 控制帧通知客户端
	// 此时 closeClientChannel 已先执行（写循环即将退出），紧随其后的
	// Conn.Close() 保证即使帧交错客户端也只是走异常断开重连，不影响正确性
	if h.shutdown.Load() {
		msg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "server is shutting down")
		_ = client.Conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
	}

	client.Conn.Close()
}

// ============================================================================
// 踢出与 shutdown 精简移除
// ============================================================================

// KickUserSimple 简单踢出用户全部连接（不发送通知），返回已触发注销的连接数
// （Unregister 为异步执行，返回值不代表注销已完成）
// ctx 由调用方传入（grpc/distributed 路径已恢复 trace_id），实现全链路追踪
func (h *Hub) KickUserSimple(ctx context.Context, userID string, reason string) int {
	clients, ok := h.shardedRegistry.GetUserClients(userID)
	if !ok || len(clients) == 0 {
		h.logger.WarnContextKV(ctx, "踢出用户失败：用户不在线",
			"user_id", userID,
			"reason", reason,
		)
		return 0
	}

	h.logger.InfoContextKV(ctx, "开始踢出用户",
		"user_id", userID,
		"reason", reason,
		"connection_count", len(clients),
	)

	kicked := 0
	for _, client := range clients {
		h.Unregister(client)
		kicked++
	}
	return kicked
}

// kickClientWithNotification 踢掉客户端并发送强制下线通知
// 通知先于断开写入客户端发送通道，写循环异步发出；随后注销连接
func (h *Hub) kickClientWithNotification(client *models.Client, reason models.DisconnectReason, message string) {
	if client.Conn != nil {
		forceOfflineMsg := models.NewHubMessage().
			SetMessageType(models.MessageTypeForceOffline).
			SetSender(models.UserTypeSystem.String()).
			SetSenderType(models.UserTypeSystem).
			SetReceiver(client.UserID).
			SetReceiverType(client.UserType).
			SetContent(message).
			WithContentExtra("reason", reason)
		h.messagingMgr.SendToClient(client.Context, client, forceOfflineMsg)
	}
	h.Unregister(client)
}

// removeClientUnsafe 从注册表移除客户端（shutdown 路径专用精简清理）
// 仅做：注册表移除 + 时间轮取消 + 指针一致性校验 + 关闭通道与连接；
// 不触发回调与逐条记录落库（由 batchCleanupOnShutdown 统一批量处理，
// 避免大量串行写 Redis/DB 导致 shutdown 超时）
func (h *Hub) removeClientUnsafe(client *models.Client) {
	removed := h.shardedRegistry.RemoveClient(client.ID, client.UserID)
	if removed == nil {
		return
	}

	// ⏰ 取消时间轮上的心跳超时任务
	h.cancelHeartbeatTimeout(client.ID)

	// 指针一致性校验：旧客户端已被新连接替换时不误删新客户端
	if removed != client {
		h.shardedRegistry.AddClient(removed)
		return
	}

	h.closeClientChannel(client)
	h.closeClientConnection(client)
}
