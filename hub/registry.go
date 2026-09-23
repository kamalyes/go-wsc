/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-05 10:22:41
 * @FilePath: \go-wsc\hub\registry.go
 * @Description: Hub 连接生命周期 —— 注册/注销编排
 *
 * 实现 transport.Registrar 端口契约（Register 异步 / RegisterSync 同步 /
 * Unregister / IsShutdown / SendRegisteredMessage）：注册表操作全部走
 * shardedRegistry 分片锁；心跳（时间轮 O(1) 超时 + SSE 兜底扫描）、
 * 多端登录治理、踢出断链与连接记录已域化下沉至 connection.HeartbeatManager /
 * LifecycleManager / RecordManager，此处经 heartbeatMgr / lifecycleMgr /
 * recordMgr 委托。
 * 读写泵与协议级 PING 处理已由 transport/messaging 域接管，此处不迁移。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
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
		h.lifecycleMgr.CleanupHalfRegistered(client)
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

	// 多端登录治理（连接域）：AllowMultiLogin=false 踢旧连接、
	// MaxConnectionsPerUser 达上限踢最旧连接（均异步注销，不阻塞注册主流程）
	h.lifecycleMgr.EnforceMultiLoginPolicy(client)

	// ⏰ 在时间轮上调度心跳超时任务（仅 WebSocket；SSE 由心跳管理器兜底扫描）
	h.heartbeatMgr.ScheduleTimeout(client)

	// ================================================================
	// 非临界区 - IO 操作异步执行（WorkerPool 控制并发）
	// ================================================================

	// 创建连接记录（内存对象，供异步保存 + 连接回调使用）
	record := h.recordMgr.Create(client)

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
		h.recordMgr.Save(ctx, record)
		// 旧归属其他节点 → 通知旧节点回收幽灵连接。须在 SyncOnlineStatus 写入本节点
		// owner 之后调用（旧节点清理受 Lua 归属校验保护，仅清自身集合不动共享索引），
		// 与 SyncOnlineStatus 同闭包顺序执行保证时序；任务被丢弃时由旧节点连接超时清理兜底
		if migratedFromNode != "" && migratedFromNode != h.nodeID {
			h.notifyClientReclaim(ctx, client, migratedFromNode)
		}
	})

	// 调用客户端连接回调（提交到回调池，不可丢弃）
	// 传 record 让调用方获取 connect 身份+会话生命周期做额外落盘
	if h.callbacks.ClientConnect != nil {
		cb := h.callbacks.ClientConnect
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
			if len(pushedIDs) > 0 && h.callbacks.OfflineMessagePush != nil {
				h.callbacks.OfflineMessagePush(client.UserID, pushedIDs, failedIDs)
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
	h.heartbeatMgr.CancelTimeout(client.ID)

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
	h.lifecycleMgr.CloseChannel(client)
	h.lifecycleMgr.CloseConnection(client)

	h.logger.InfoContextKV(ctx, "客户端断开连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"remaining_connections", h.shardedRegistry.GetClientCount(),
	)

	// Phase 3: 回调与记录落库（异步，不阻塞注销主流程）

	// 调用断开回调（提交到回调池，不可丢弃）
	if h.callbacks.ClientDisconnect != nil {
		cb := h.callbacks.ClientDisconnect
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
		h.recordMgr.MarkDisconnected(ctx, client)
	})
}
