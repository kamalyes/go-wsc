/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:17:07
 * @FilePath: \go-wsc\hub\registry.go
 * @Description: Hub 客户端注册/注销管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"runtime/debug"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/events"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 客户端注册/注销
// ============================================================================

// Register 注册客户端
// 直接异步执行 handleRegister，不经过 EventLoop channel 串行化
// handleRegister 内部已用 shardedRegistry 分片锁保护临界区，IO 操作通过 workerPool 异步化
// 避免单 goroutine EventLoop 成为并发连接的 QPS 瓶颈
// client.Context 在 http_upgrade 升级时已注入 trace_id，内部直接用 client.Context 实现全链路追踪
func (h *Hub) Register(client *Client) {
	h.logger.DebugContextKV(client.Context, "客户端注册请求", "client_id", client.ID, "user_id", client.UserID)
	go h.handleRegister(client)
}

// Unregister 注销客户端
// 直接异步执行 handleUnregister，不经过 EventLoop channel 串行化
// handleUnregister 内部已用 shardedRegistry 分片锁保护临界区，IO 操作通过 workerPool 异步化
// client.Context 在升级时已注入 trace_id，内部直接用 client.Context 实现全链路追踪
func (h *Hub) Unregister(client *Client) {
	h.logger.DebugContextKV(client.Context, "客户端注销请求", "client_id", client.ID, "user_id", client.UserID)
	go h.handleUnregister(client)
}

// handleRegister 处理客户端注册（内部方法）
// client.Context 在升级时已注入 trace_id，全程沿用同一 trace_id 串联长连接生命周期
func (h *Hub) handleRegister(client *Client) {
	// ctx 兜底：生产中 http upgrade 时已注入 client.Context；直接构造 Client 调用 Register 的场景（如测试、集成）
	// 传 nil 时降级为 h.ctx，保证 workerPool/handleMultiLoginPolicy 等下游不因 nil ctx panic
	ctx := client.Context
	if ctx == nil {
		ctx = h.ctx
		client.Context = ctx
	}
	defer syncx.RecoverWithHandler(func(r interface{}) {
		h.logger.ErrorContextKV(ctx, "handleRegister panic",
			"client_id", client.ID,
			"user_id", client.UserID,
			"panic", r,
		)
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

	h.logger.InfoContextKV(ctx, "handleRegister开始",
		"client_id", client.ID,
		"user_id", client.UserID)

	// ================================================================
	// 客户端初始化（无锁，client 尚未共享）
	// ============================================================
	client.NodeID = h.nodeID
	client.NodeIP = h.config.NodeIP
	client.NodePort = h.config.NodePort

	// appID 归一化：空→DefaultAppID（入口层统一归一化，ClientMatchesEnvelope 严格匹配要求）
	// appID 无广播语义，必填，空值统一补默认值
	client.AppID = constants.NormalizeAppID(client.AppID)
	// 命名空间归一化：非观察者补默认（观察者保留空，表示全局观察所有命名空间）
	if client.UserType != models.UserTypeObserver {
		client.Namespace = constants.NormalizeNamespace(client.Namespace)
	}

	// 初始化客户端 SendChan
	h.initClientSendChan(client)

	// 初始化客户端时间戳（原子更新）
	now := time.Now()
	client.ConnectedAt = mathx.IfNotZero(client.ConnectedAt, now)
	client.SetLastHeartbeat(mathx.IfNotZero(client.GetLastHeartbeat(), now))
	client.SetLastSeen(mathx.IfNotZero(client.GetLastSeen(), now))

	// ================================================================
	// 节点级总连接数硬限制（动态扩容上限）
	// MaxConnectionsPerNode > 0 时生效，0 表示不限制
	// 超过上限：发送 Close 帧(1013 Try Again Later)告知客户端稍后重试，再关闭连接
	// ============================================================
	if maxConns := h.GetMaxConnectionsPerNode(); maxConns > 0 && h.shardedRegistry.GetClientCount() >= int64(maxConns) {
		current := h.shardedRegistry.GetClientCount()
		h.logger.WarnContextKV(ctx, "节点连接数已达上限，拒绝注册",
			"client_id", client.ID,
			"user_id", client.UserID,
			"current_connections", current,
			"max_connections", maxConns,
		)
		if client.Conn != nil {
			// WriteControl 内部加锁且并发安全，可与读循环并发执行
			msg := websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "节点连接数已达上限，请稍后重试")
			_ = client.Conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
			client.Conn.Close()
		}
		return
	}

	// ================================================================
	// 临界区 - 仅 map 操作（shardedRegistry 分片锁，粒度细）
	// 多端登录策略 + 添加到注册表，同一 shard 内原子完成
	// ============================================================
	h.handleMultiLoginPolicy(client) // 内部通过 shardedRegistry 加分片锁

	// ⏰ 在时间轮上调度心跳超时任务（WebSocket 客户端）
	// 收到 PING 时 Refresh 刷新，超时未刷新则触发注销
	h.scheduleHeartbeatTimeout(client)

	// ================================================================
	// Phase 3: 非临界区 - IO 操作异步执行（WorkerPool 控制并发）
	// 不再持有任何锁，避免阻塞其他客户端的注册/注销/发送
	// ============================================================
	// ctx 为业务调用方透传的 ctx（携带 trace_id），异步任务沿用实现全链路追踪

	// 统计同步 + 日志（提交到记录池，可丢弃）
	// syncClientStats/syncActiveConnectionsToRedis 聚合统计用 h.ctx；logClientConnection 用 client.Context
	h.workerPool.TrySubmitRecord(func() {
		h.syncClientStats()
		h.syncActiveConnectionsToRedis()
		h.logClientConnection(client)
	})

	// 创建连接记录（内存对象，供异步保存 + 连接回调使用，无条件构造）
	record := h.CreateConnectionRecord(client)

	// 保存连接记录到数据库（提交到记录池）
	// 传入 client.Context 保留 client 维度 trace_id，异步保存仍可全链路追踪
	if h.connectionRecordRepo != nil {
		h.workerPool.TrySubmitRecord(func() {
			h.saveConnectionRecord(ctx, record)
		})
	}

	// 保存连接质量初始行（提交到记录池，与连接记录并行落库）
	// 首次连接建零值行(QualityScore=100)，重连 reconnect_count+1（由 qualityRepo.Upsert 内部 OnConflict 处理）
	if h.connectionQualityRepo != nil {
		h.workerPool.TrySubmitRecord(func() {
			h.saveConnectionQuality(ctx, client)
		})
	}

	// 调用客户端连接回调（提交到回调池，不可丢弃）
	// 传 record 让调用方获取 connect 身份+会话生命周期做额外落盘（record 已异步落库，回调方不应再写 wsc_connection_records）
	h.workerPool.SubmitCallback(ctx, func() {
		if h.clientConnectCallback != nil {
			if err := h.clientConnectCallback(ctx, client, record); err != nil {
				h.logger.ErrorContextKV(ctx, "客户端连接回调执行失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
				if h.errorCallback != nil {
					_ = h.errorCallback(ctx, err, ErrorSeverityError)
				}
			}
		}
	})

	// 🔑 同步写 Redis 在线索引：注册完成的瞬间让其他节点 checkUserOnline 直查 Redis 可见
	// 原异步路径（TrySubmitDistributed）在 DistributedPool 队列积压时索引写入滞后，
	// 期间其他节点 checkUserOnline 返回 false → 触发 routeToClusterForOfflineUser 广播兜底
	// 同步调用仅阻塞本 handleRegister goroutine ~1ms（一次 Redis Pipeline 往返），
	// handleRegister 由 go h.handleRegister(client) 异步触发（registry.go:39），不阻塞 EventLoop、不影响其他连接
	// syncOnlineStatus 内部 onlineStatusRepo==nil 时早返回；SetClientOnline 失败仅记日志不 return，行为与原异步路径一致
	h.syncOnlineStatus(client) // 内部用 client.Context 保留连接级 trace_id

	// 系统组加入 + 成员组加入 + 离线消息推送（提交到分布式池，均不依赖在线状态索引）
	// joinSystemGroupsOnConnect/joinMemberGroupOnConnect 写 group ZSET（wsc:group:* 命名空间）
	// pushOfflineMessagesOnConnect 操作离线消息队列（ns::userID），依赖本地 shardedRegistry（L122 已完成）
	// 内部方法已有 client 参数，直接用 client.Context 保留连接级 trace_id
	h.workerPool.TrySubmitDistributed(func() {
		h.joinSystemGroupsOnConnect(ctx, client)
		h.joinMemberGroupOnConnect(ctx, client)
		h.pushOfflineMessagesOnConnect(client)
	})

	// 📡 发布用户上线事件（提交到回调池）
	h.workerPool.TrySubmitCallback(func() {
		events.PublishUserOnline(ctx, h, client.UserID, client.UserType, client.ID)
	})

	// 发送欢迎消息（提交到消息池）
	h.workerPool.TrySubmitMessage(func() {
		h.sendWelcomeMessage(client)
	})

	// 启动客户端读写 goroutine
	if client.Conn != nil {
		go h.handleClientWrite(client)
		go h.handleClientRead(client)
	}

	// 🚀 失效路由缓存（让其他节点下次路由时重新加载用户节点信息）
	if h.routerCache != nil {
		h.routerCache.InvalidateUser(ctx, client.UserID)
	}
}

// GetMaxConnectionsPerNode 获取节点最大连接数（0 表示不限制）
// 从 Performance.MaxConnectionsPerNode 读取；config 或 Performance 为 nil 时返回 0（不限制）
func (h *Hub) GetMaxConnectionsPerNode() int {
	if h.config == nil || h.config.Performance == nil {
		return 0
	}
	return h.config.Performance.MaxConnectionsPerNode
}

// handleUnregister 处理客户端注销（内部方法）
// client.Context 在升级时已注入 trace_id，内部用 client.Context 串联长连接生命周期
func (h *Hub) handleUnregister(client *Client) {
	ctx := client.Context
	// 📡 发布用户下线事件（在锁外发布，避免阻塞）
	go events.PublishUserOffline(ctx, h, client.UserID, client.UserType, client.ID)

	// Phase 1: 临界区 - 仅从注册表移除（shardedRegistry 分片锁）
	// removeClientUnsafe 内部用 client.Context 保留连接级 trace_id
	h.removeClientUnsafe(client)

	// 系统组离开（提交到分布式池，与在线状态清理并行）
	h.workerPool.TrySubmitDistributed(func() {
		h.leaveSystemGroupsOnDisconnect(ctx, client)
	})

	// 调用断开回调（提交到回调池）
	if h.clientDisconnectCallback != nil {
		h.workerPool.SubmitCallback(ctx, func() {
			if err := h.clientDisconnectCallback(ctx, client, DisconnectReasonClientRequest); err != nil {
				h.logger.ErrorContextKV(ctx, "客户端断开回调执行失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
				if h.errorCallback != nil {
					_ = h.errorCallback(ctx, err, ErrorSeverityWarning)
				}
			}
		})
	}

	// 🚀 失效路由缓存
	if h.routerCache != nil {
		h.routerCache.InvalidateUser(ctx, client.UserID)
	}
}

// ============================================================================
// 多端登录策略处理
// ============================================================================

// handleMultiLoginPolicy 统一处理多端登录策略（内部方法）
// 根据配置决定是否允许多端登录、是否限制连接数
// 全程使用原子/O(1) 查询 + ForEachUserClient 持锁遍历，消除 GetUserClients 锁外遍历的数据竞争
// newClient.Context 在升级时已注入 trace_id + 路由信封，内部用 newClient.Context 串联长连接生命周期
// 多端登录策略按 appID+namespace 信封隔离：不同应用/命名空间的连接互不影响（app-A 的连接数不挤占 app-B 的配额）
func (h *Hub) handleMultiLoginPolicy(newClient *Client) {
	ctx := newClient.Context
	userID := newClient.UserID
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)

	// O(1) 快速检查同信封下用户是否有现有客户端（原子计数器，无锁）
	if !h.shardedRegistry.HasUser(userID, appID, ns) {
		h.addNewClient(newClient)
		return
	}

	h.logger.DebugContextKV(ctx, "处理多端登录策略",
		"user_id", userID,
		"new_client_id", newClient.ID,
		"allow_multi_login", h.config.AllowMultiLogin,
		"max_connections_per_user", h.config.MaxConnectionsPerUser)

	// 检测断线重连：O(1) 查找相同 ClientID 的旧客户端（GetClient 持读锁）
	if oldClient, exists := h.shardedRegistry.GetClient(newClient.ID); exists && oldClient.UserID == userID {
		h.logger.InfoContextKV(ctx, "检测到相同ClientID的旧连接，执行断线重连替换",
			"user_id", userID,
			"client_id", newClient.ID,
		)
		// 清理旧客户端：关闭通道和连接，停止旧协程
		// 注意：不调用 removeClientFromMaps，因为 addNewClient 会覆盖 map 条目
		h.closeClientChannel(oldClient)
		h.closeClientConnection(oldClient)
	}

	// 不允许多端登录：踢掉所有旧连接
	if !h.config.AllowMultiLogin {
		// 使用 ForEachUserClient 持读锁零拷贝收集客户端（消除 CopyClientsFromMap 锁外遍历数据竞争）
		var clients []*Client
		h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
			clients = append(clients, client)
			return true
		})

		h.logger.InfoContextKV(ctx, "不允许多端登录，踢掉所有旧连接",
			"user_id", userID,
			"old_connections", len(clients))

		h.kickExistingClients(clients, DisconnectReasonForceOffline)
		h.addNewClient(newClient)
		return
	}

	// 允许多端登录，但有连接数限制
	if h.config.MaxConnectionsPerUser > 0 {
		currentCount := h.shardedRegistry.GetUserClientCount(userID)
		maxAllowed := h.config.MaxConnectionsPerUser

		// 如果未达到上限，直接添加
		if currentCount < maxAllowed {
			h.addNewClient(newClient)
			return
		}

		// 达到上限：踢掉最早的连接
		h.logger.InfoContextKV(ctx, "达到连接数上限，踢掉最早的连接",
			"user_id", userID,
			"current_count", currentCount,
			"max_allowed", maxAllowed)

		h.kickOldestConnection(userID)
		h.addNewClient(newClient)
		return
	}

	// 允许多端登录且无限制，直接添加
	h.addNewClient(newClient)
}

// ============================================================================
// 踢人相关方法
// ============================================================================

// KickUser 踢出用户的所有连接
// ctx 由调用方传入（通常为请求级 ctx 或 client.Context），用于全链路追踪踢人操作
func (h *Hub) KickUser(ctx context.Context, userID string, reason string, sendNotification bool, notificationMsg string) *KickUserResult {
	result := &KickUserResult{
		UserID:   userID,
		Reason:   reason,
		KickedAt: time.Now(),
	}

	// 1. 获取用户的所有连接（按 ctx 路由信封 appID+namespace 隔离，避免跨 app/ns 误踢）
	clients := h.GetConnectionsByUserID(ctx, userID)
	if len(clients) == 0 {
		result.Error = errorx.NewError(ErrTypeUserNotFound, userID)
		result.Success = false
		result.Reason = reason + " (用户不在线)"
		h.logger.WarnContextKV(ctx, "踢出用户失败：用户不在线",
			"user_id", userID,
			"reason", reason,
		)
		return result
	}

	result.KickedConnections = len(clients)

	// 2. 发送踢出通知消息（在断开连接之前）
	// 批量操作：内部每个 client 用各自 client.Context 保留连接级 trace_id
	if sendNotification {
		notification := h.createKickNotification(userID, reason, notificationMsg, result.KickedAt)
		result.NotificationSent = h.sendKickNotificationToClients(clients, notification)
		// 消息已写入各客户端 SendChan，handleClientWrite 会异步发送
		// 不再使用 time.Sleep 阻塞，后续 CloseAllClientsInMap 会触发连接关闭
	}

	// 3. 记录踢出操作
	h.logger.InfoContextKV(ctx, "开始踢出用户",
		"user_id", userID,
		"reason", reason,
		"connection_count", len(clients),
		"notification_sent", result.NotificationSent,
	)

	// 4. 并发断开所有连接
	// 每个 client 用自己的 client.Context，使断开回调日志携带各自 trace_id
	syncx.ParallelForEachSlice(clients, func(i int, client *Client) {
		h.disconnectKickedClient(client.Context, client, reason)
	})

	// 5. 设置成功标志并记录完成
	result.Success = true
	h.logger.InfoContextKV(ctx, "用户踢出完成",
		"user_id", userID,
		"reason", reason,
		"kicked_connections", result.KickedConnections,
		"notification_sent", result.NotificationSent,
	)

	return result
}

// KickUserWithMessage 踢出用户并发送自定义消息
// ctx 由调用方传入（grpc/distributed 路径已恢复 trace_id），透传给 KickUser 实现全链路追踪
func (h *Hub) KickUserWithMessage(ctx context.Context, userID string, reason string, message string) error {
	result := h.KickUser(ctx, userID, reason, true, message)
	return result.Error
}

// KickUserSimple 简单踢出用户（不发送通知）
// ctx 由调用方传入（grpc/distributed 路径已恢复 trace_id），透传给 KickUser 实现全链路追踪
func (h *Hub) KickUserSimple(ctx context.Context, userID string, reason string) int {
	result := h.KickUser(ctx, userID, reason, false, "")
	return result.KickedConnections
}

// ============================================================================
// 内部辅助方法
// ============================================================================

// removeClientUnsafe 从注册表移除客户端（含指针一致性校验、时间轮取消、清理流程）
// 主存储 + 分类索引（SSE/Observer/Agent）全部由 shardedRegistry.RemoveClient 内部原子完成
// 已有 client 参数，内部用 client.Context 保留连接级 trace_id
// 回调由调用方（handleUnregister）通过 workerPool 处理，避免重复
func (h *Hub) removeClientUnsafe(client *Client) {
	// 1. 从 shardedRegistry 移除主存储 + 分类索引（若不存在则直接返回）
	removed := h.shardedRegistry.RemoveClient(client.ID, client.UserID)
	if removed == nil {
		return
	}

	// ⏰ 取消时间轮上的心跳超时任务（客户端已注销，不再需要超时检测）
	h.cancelHeartbeatTimeout(client.ID)

	// 关键修复：验证客户端指针是否一致
	// TemporalHasher 在时间窗口内为相同用户+设备生成相同 ClientID，
	// 断线重连时新客户端会覆盖旧客户端的注册表条目，
	// 旧客户端的读协程退出时调用 Unregister 不应删除新客户端
	if removed != client {
		// 旧客户端已被新连接替换，重新添加新客户端并跳过旧客户端的注销
		h.shardedRegistry.AddClient(removed)
		h.logger.InfoContextKV(client.Context, "客户端已被新连接替换，跳过旧客户端的注销",
			"client_id", client.ID,
			"user_id", client.UserID,
		)
		return
	}

	// shutdown 路径：精简清理，只关闭连接（含 1001 close frame）
	// Redis 在线状态、DB 连接记录、逐条日志由 SafeShutdown 统一批量处理
	// 避免大量串行写 Redis/DB 导致 shutdown 超时
	if h.shutdown.Load() {
		h.closeClientChannel(client)
		h.closeClientConnection(client)
		return
	}

	// 正常路径：完整清理流程
	// 2. 日志
	h.logClientDisconnection(client)

	// 3. Redis 同步（IO 操作，调用方应通过 workerPool 异步化）
	h.syncClientRemovalToRedis(client)

	// 4. 关闭 channel 和连接
	h.closeClientChannel(client)
	h.closeClientConnection(client)

	// 5. 更新连接断开记录
	h.updateConnectionOnDisconnect(client, DisconnectReasonClientRequest)
}

// logClientDisconnection 记录客户端断开日志
// 已有 client 参数，内部用 client.Context 保留连接级 trace_id
func (h *Hub) logClientDisconnection(client *Client) {
	h.logger.InfoContextKV(client.Context, "客户端断开连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"remaining_connections", h.shardedRegistry.GetClientCount(),
	)
}

// syncClientRemovalToRedis 同步客户端移除到Redis
// 已有 client 参数，内部用 client.Context 保留连接级 trace_id
func (h *Hub) syncClientRemovalToRedis(client *Client) {
	h.syncActiveConnectionsToRedis()
	h.removeOnlineStatusFromRedis(client)
}

// syncActiveConnectionsToRedis 同步活跃连接数到Redis（使用防抖机制避免竞态条件）
// 当多个客户端快速注册时，使用防抖延迟50ms执行，避免多个goroutine读取不同的连接数并乱序写入Redis
// 聚合统计无具体 client 维度，用 h.ctx
func (h *Hub) syncActiveConnectionsToRedis() {
	if h.statsRepo == nil {
		return
	}

	// 检查Hub是否正在关闭
	if h.shutdown.Load() {
		// Hub正在关闭，立即同步连接数为0
		go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
			return h.statsRepo.SetActiveConnections(ctx, h.nodeID, 0)
		})
		return
	}

	// 使用防抖机制
	h.syncActiveConnMutex.Lock()
	defer h.syncActiveConnMutex.Unlock()

	// 取消之前的定时器
	if h.syncActiveConnTimer != nil {
		h.syncActiveConnTimer.Stop()
	}

	// 设置新的定时器，100ms后执行同步（增加延迟确保所有注册操作完成）
	h.syncActiveConnTimer = time.AfterFunc(100*time.Millisecond, func() {
		// 标记正在执行同步
		if !h.syncActiveConnPending.CompareAndSwap(false, true) {
			return // 已有同步任务在执行
		}
		defer h.syncActiveConnPending.Store(false)

		syncx.Go(h.ctx).
			WithTimeout(2 * time.Second).
			OnPanic(func(r any) {
				h.logger.ErrorContextKV(h.ctx, "同步活跃连接数到Redis崩溃", "panic", r, "stack", string(debug.Stack()))
			}).
			ExecWithContext(func(ctx context.Context) error {
				// 再次检查shutdown
				if h.shutdown.Load() {
					return h.statsRepo.SetActiveConnections(ctx, h.nodeID, 0)
				}
				// 读取当前连接数（shardedRegistry 原子计数器，零锁开销）
				return h.statsRepo.SetActiveConnections(ctx, h.nodeID, h.shardedRegistry.GetClientCount())
			})
	})
}

// removeOnlineStatusFromRedis 从Redis移除在线状态
// 已有 client 参数，内部用 client.Context 保留连接级 trace_id
// context.WithoutCancel 确保 Hub 关闭（h.ctx 取消）后仍能完成 Redis 清理
func (h *Hub) removeOnlineStatusFromRedis(client *Client) {
	if h.onlineStatusRepo == nil {
		return
	}
	// 用 client.Context 派生（保留连接级 trace_id，全链路追踪下线清理）；
	// context.WithoutCancel 确保 Hub 关闭（h.ctx 取消）后仍能完成 Redis 清理
	syncx.Go(context.WithoutCancel(client.Context)).
		WithTimeout(3 * time.Second).
		OnError(func(err error) {
			h.logger.ErrorContextKV(client.Context, "从Redis移除在线状态失败",
				"user_id", client.UserID,
				"client_id", client.ID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.onlineStatusRepo.SetClientOffline(ctx, client)
		})
}

// closeClientChannel 关闭客户端发送通道
// 仅关闭 channel 通知 handleClientWrite 退出，不回收到对象池（已关闭的 channel 无法复用）
// 不置 nil SendChan，避免与 handleClientWrite 的 select 读产生数据竞争
func (h *Hub) closeClientChannel(client *Client) {
	// 使用互斥锁保护关闭操作
	client.CloseMu.Lock()
	defer client.CloseMu.Unlock()

	// 标记为已关闭，防止其他goroutine继续发送
	if client.IsClosed() {
		return // 已经关闭过了
	}
	client.MarkClosed()

	// 关闭 WebSocket 发送通道（handleClientWrite 会读完缓冲后收到 ok=false 退出）
	// 不调用 releaseClientSendChan：1) 已关闭的 channel 不能放回池中复用
	//   2) 不置 nil SendChan，避免与 handleClientWrite 的 <-client.SendChan 数据竞争
	if client.SendChan != nil {
		close(client.SendChan)
	}

	// SSE 客户端需要关闭专用通道
	if client.ConnectionType == ConnectionTypeSSE {
		if client.SSEMessageCh != nil {
			close(client.SSEMessageCh)
		}
		if client.SSECloseCh != nil {
			close(client.SSECloseCh)
		}
	}
}

// closeClientConnection 关闭WebSocket连接
// Hub 关闭（如 K8s 滚动更新）时先发送 1001 GoingAway 控制帧，
// 让客户端识别为服务端主动离开并触发重连，而不是收到 1006 异常断开
func (h *Hub) closeClientConnection(client *Client) {
	if client.Conn == nil {
		return
	}

	// Hub 正在关闭时，先发送 1001 GoingAway 控制帧通知客户端
	// WriteControl 内部加锁且并发安全，可与读循环并发执行
	if h.shutdown.Load() {
		msg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "server is shutting down")
		_ = client.Conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
	}

	client.Conn.Close()
}

// addNewClient 添加新客户端到注册表
// 主存储 + 分类索引（SSE/Observer/Agent）全部由 shardedRegistry.AddClient 内部原子完成
func (h *Hub) addNewClient(client *Client) {
	h.shardedRegistry.AddClient(client)
}

// kickExistingClients 踢掉现有客户端（接收切片，调用方负责通过 ForEachUserClient 持锁收集）
// 已有 client 参数，内部循环每个 client 用各自 client.Context 保留连接级 trace_id
func (h *Hub) kickExistingClients(clients []*Client, reason DisconnectReason) {
	for _, client := range clients {
		h.kickClientWithNotification(client, reason, "您的账号在其他设备登录，当前连接将被断开")

		h.logger.InfoContextKV(client.Context, "踢出旧连接",
			"user_id", client.UserID,
			"client_id", client.ID,
			"reason", reason,
		)
	}
}

// kickOldestConnection 踢掉最不活跃的连接（基于最后心跳时间）
// 使用 ForEachUserClient 持读锁遍历，消除锁外遍历 map 的数据竞争
// 找到 oldestClient 后用其 client.Context 保留连接级 trace_id
func (h *Hub) kickOldestConnection(userID string) {
	var oldestClient *Client
	var oldestTime time.Time

	// 持读锁遍历找出最久没有心跳的客户端
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
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

	h.kickClientWithNotification(oldestClient, DisconnectReasonForceOffline, "连接数已达上限，当前连接将被断开")
}

// kickClientWithNotification 踢掉客户端并发送通知（公共方法）
// 已有 client 参数，内部用 client.Context 保留连接级 trace_id
func (h *Hub) kickClientWithNotification(client *Client, reason DisconnectReason, message string) {
	// 发送强制下线通知
	if client.Conn != nil {
		forceOfflineMsg := models.NewHubMessage().
			SetMessageType(models.MessageTypeForceOffline).
			SetSender("system").
			SetSenderType(models.UserTypeSystem).
			SetReceiver(client.UserID).
			SetReceiverType(client.UserType).
			SetContent(message).
			WithContentExtra("reason", reason)

		// 用 client.Context 保留连接级 trace_id，强制下线消息日志可全链路追踪
		h.sendToClient(client.Context, client, forceOfflineMsg)
		// 不再使用 time.Sleep 阻塞等待，sendToClient 已将消息写入 SendChan，
		// handleClientWrite 会异步发送 Unregister 后通道关闭前消息仍会被消费
	}
	h.Unregister(client)
}

// createKickNotification 创建踢人通知消息
func (h *Hub) createKickNotification(userID, reason, customMsg string, kickedAt time.Time) *HubMessage {
	content := mathx.IfEmpty(customMsg, "您已被踢出: "+reason)

	return &HubMessage{
		MessageType: MessageTypeKickOut,
		Sender:      "system",
		Receiver:    userID,
		Content:     content,
		CreateAt:    kickedAt,
		Data: map[string]interface{}{
			"reason":    reason,
			"kicked_at": kickedAt.Unix(),
		},
	}
}

// sendKickNotificationToClients 发送踢人通知到客户端
// 预序列化一次消息，所有客户端复用，消除逐客户端 json.Marshal 开销
// 批量操作：循环内每个 client 用各自 client.Context 保留连接级 trace_id
func (h *Hub) sendKickNotificationToClients(clients []*Client, msg *HubMessage) bool {
	if len(clients) == 0 {
		return false
	}

	// 预序列化一次（所有客户端复用）
	preSerialized, _ := json.Marshal(msg)

	// 每个 client 用自己的 client.Context，使踢人通知投递日志携带各自 trace_id
	for _, client := range clients {
		h.sendToClientSerialized(client.Context, client, msg, preSerialized)
	}
	return true
}

// CloseAllClientsInMap 关闭用户的所有客户端连接(并发)
func (h *Hub) CloseAllClientsInMap(clientMap map[string]*Client) {
	syncx.ParallelForEach(clientMap, func(_ string, client *Client) {
		if client.Conn != nil {
			client.Conn.Close()
		}
	})
}
