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
	"github.com/kamalyes/go-wsc/events"
	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 客户端注册/注销
// ============================================================================

// Register 注册客户端
func (h *Hub) Register(client *Client) {
	h.logger.DebugKV("客户端注册请求", "client_id", client.ID, "user_id", client.UserID)
	h.register <- client
}

// Unregister 注销客户端
func (h *Hub) Unregister(client *Client) {
	h.logger.DebugKV("客户端注销请求", "client_id", client.ID, "user_id", client.UserID)
	select {
	case h.unregister <- client:
		// 成功放入注销队列，由 EventLoop 异步处理
	default:
		// 注销队列已满（大量并发断开），降级为同步处理
		// 避免读 goroutine 的 defer 阻塞在 channel 发送上导致泄漏
		h.logger.WarnKV("注销队列已满，同步处理客户端注销",
			"client_id", client.ID, "user_id", client.UserID)
		h.handleUnregister(client)
	}
}

// handleRegister 处理客户端注册（内部方法）
func (h *Hub) handleRegister(client *Client) {
	defer syncx.RecoverWithHandler(func(r interface{}) {
		h.logger.ErrorKV("handleRegister panic",
			"client_id", client.ID,
			"user_id", client.UserID,
			"panic", r,
		)
	})

	// 双重检查：如果 Hub 正在关闭，拒绝注册
	if h.shutdown.Load() {
		h.logger.WarnKV("Hub 正在关闭，拒绝注册",
			"client_id", client.ID,
			"user_id", client.UserID)
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
		return
	}

	h.logger.InfoKV("handleRegister开始",
		"client_id", client.ID,
		"user_id", client.UserID)

	// ================================================================
	// 客户端初始化（无锁，client 尚未共享）
	// ============================================================
	client.NodeID = h.nodeID
	client.NodeIP = h.config.NodeIP
	client.NodePort = h.config.NodePort

	// 命名空间归一化：空→"default"（观察者保留空，表示全局观察所有命名空间）
	if client.UserType != models.UserTypeObserver && client.Namespace == "" {
		client.Namespace = models.DefaultNamespace
	}

	// 初始化客户端 SendChan
	h.initClientSendChan(client)

	// 初始化客户端时间戳（原子更新）
	now := time.Now()
	client.ConnectedAt = mathx.IfNotZero(client.ConnectedAt, now)
	client.SetLastHeartbeat(mathx.IfNotZero(client.GetLastHeartbeat(), now))
	client.SetLastSeen(mathx.IfNotZero(client.GetLastSeen(), now))

	// ================================================================
	// 临界区 - 仅 map 操作（shardedRegistry 分片锁，粒度细）
	// 多端登录策略 + 添加到注册表，同一 shard 内原子完成
	// ============================================================
	h.handleMultiLoginPolicy(client) // 内部通过 shardedRegistry 加分片锁

	// ================================================================
	// Phase 3: 非临界区 - IO 操作异步执行（WorkerPool 控制并发）
	// 不再持有任何锁，避免阻塞其他客户端的注册/注销/发送
	// ============================================================
	ctx := context.Background()

	// 统计同步 + 日志（提交到记录池，可丢弃）
	h.workerPool.TrySubmitRecord(func() {
		h.syncClientStats()
		h.syncActiveConnectionsToRedis()
		h.logClientConnection(client)
	})

	// 保存连接记录到数据库（提交到记录池）
	if h.connectionRecordRepo != nil {
		record := h.CreateConnectionRecord(client)
		h.workerPool.TrySubmitRecord(func() {
			h.saveConnectionRecord(record)
		})
	}

	// 调用客户端连接回调（提交到回调池，不可丢弃）
	h.workerPool.SubmitCallback(ctx, func() {
		if h.clientConnectCallback != nil {
			if err := h.clientConnectCallback(ctx, client); err != nil {
				h.logger.ErrorKV("客户端连接回调执行失败",
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

	// 系统组加入 + 在线状态同步 + 离线消息推送（提交到分布式池）
	h.workerPool.TrySubmitDistributed(func() {
		h.joinSystemGroupsOnConnect(ctx, client)
		h.syncOnlineStatus(client)
		h.pushOfflineMessagesOnConnect(client)
	})

	// 📡 发布用户上线事件（提交到回调池）
	h.workerPool.TrySubmitCallback(func() {
		events.PublishUserOnline(h, client.UserID, client.UserType, client.ID)
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

// handleUnregister 处理客户端注销（内部方法）
func (h *Hub) handleUnregister(client *Client) {
	// 📡 发布用户下线事件（在锁外发布，避免阻塞）
	go events.PublishUserOffline(h, client.UserID, client.UserType, client.ID)

	// Phase 1: 临界区 - 仅从注册表移除（shardedRegistry 分片锁）
	h.removeClientUnsafe(client)

	// Phase 2: 非临界区 - IO 操作异步执行
	ctx := context.Background()

	// 系统组离开（提交到分布式池，与在线状态清理并行）
	h.workerPool.TrySubmitDistributed(func() {
		h.leaveSystemGroupsOnDisconnect(ctx, client)
	})

	// 调用断开回调（提交到回调池）
	if h.clientDisconnectCallback != nil {
		h.workerPool.SubmitCallback(ctx, func() {
			if err := h.clientDisconnectCallback(ctx, client, DisconnectReasonClientRequest); err != nil {
				h.logger.ErrorKV("客户端断开回调执行失败",
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
func (h *Hub) handleMultiLoginPolicy(newClient *Client) {
	userID := newClient.UserID

	// O(1) 快速检查用户是否有现有客户端（原子计数器，无锁）
	if !h.shardedRegistry.HasUser(userID) {
		h.addNewClient(newClient)
		return
	}

	h.logger.DebugKV("处理多端登录策略",
		"user_id", userID,
		"new_client_id", newClient.ID,
		"allow_multi_login", h.config.AllowMultiLogin,
		"max_connections_per_user", h.config.MaxConnectionsPerUser)

	// 检测断线重连：O(1) 查找相同 ClientID 的旧客户端（GetClient 持读锁）
	if oldClient, exists := h.shardedRegistry.GetClient(newClient.ID); exists && oldClient.UserID == userID {
		h.logger.InfoKV("检测到相同ClientID的旧连接，执行断线重连替换",
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

		h.logger.InfoKV("不允许多端登录，踢掉所有旧连接",
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
		h.logger.InfoKV("达到连接数上限，踢掉最早的连接",
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
func (h *Hub) KickUser(userID string, reason string, sendNotification bool, notificationMsg string) *KickUserResult {
	result := &KickUserResult{
		UserID:   userID,
		Reason:   reason,
		KickedAt: time.Now(),
	}

	ctx := context.Background()

	// 1. 获取用户的所有连接
	clients := h.GetConnectionsByUserID(userID)
	if len(clients) == 0 {
		result.Error = errorx.NewError(ErrTypeUserNotFound, "user not online or not found: %s", userID)
		result.Success = false
		result.Reason = reason + " (用户不在线)"
		h.logger.WarnKV("踢出用户失败：用户不在线",
			"user_id", userID,
			"reason", reason,
		)
		return result
	}

	result.KickedConnections = len(clients)

	// 2. 发送踢出通知消息（在断开连接之前）
	if sendNotification {
		notification := h.createKickNotification(userID, reason, notificationMsg, result.KickedAt)
		result.NotificationSent = h.sendKickNotificationToClients(clients, notification)
		// 消息已写入各客户端 SendChan，handleClientWrite 会异步发送
		// 不再使用 time.Sleep 阻塞，后续 CloseAllClientsInMap 会触发连接关闭
	}

	// 3. 记录踢出操作
	h.logger.InfoKV("开始踢出用户",
		"user_id", userID,
		"reason", reason,
		"connection_count", len(clients),
		"notification_sent", result.NotificationSent,
	)

	// 4. 并发断开所有连接
	syncx.ParallelForEachSlice(clients, func(i int, client *Client) {
		h.disconnectKickedClient(ctx, client, reason)
	})

	// 5. 设置成功标志并记录完成
	result.Success = true
	h.logger.InfoKV("用户踢出完成",
		"user_id", userID,
		"reason", reason,
		"kicked_connections", result.KickedConnections,
		"notification_sent", result.NotificationSent,
	)

	return result
}

// KickUserWithMessage 踢出用户并发送自定义消息
func (h *Hub) KickUserWithMessage(userID string, reason string, message string) error {
	result := h.KickUser(userID, reason, true, message)
	return result.Error
}

// KickUserSimple 简单踢出用户（不发送通知）
func (h *Hub) KickUserSimple(userID string, reason string) int {
	result := h.KickUser(userID, reason, false, "")
	return result.KickedConnections
}

// ============================================================================
// 内部辅助方法
// ============================================================================

// removeClientUnsafe 移除客户端（数据清理，不含回调）
// 主存储 + 分类索引（SSE/Observer/Agent）全部由 shardedRegistry.RemoveClient 内部原子完成
// 回调由调用方（handleUnregister）通过 workerPool 处理，避免重复
func (h *Hub) removeClientUnsafe(client *Client) {
	// 1. 从 shardedRegistry 移除主存储 + 分类索引（若不存在则直接返回）
	removed := h.shardedRegistry.RemoveClient(client.ID, client.UserID)
	if removed == nil {
		return
	}

	// 关键修复：验证客户端指针是否一致
	// TemporalHasher 在时间窗口内为相同用户+设备生成相同 ClientID，
	// 断线重连时新客户端会覆盖旧客户端的注册表条目，
	// 旧客户端的读协程退出时调用 Unregister 不应删除新客户端
	if removed != client {
		// 旧客户端已被新连接替换，重新添加新客户端并跳过旧客户端的注销
		h.shardedRegistry.AddClient(removed)
		h.logger.InfoKV("客户端已被新连接替换，跳过旧客户端的注销",
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
func (h *Hub) logClientDisconnection(client *Client) {
	h.logger.InfoKV("客户端断开连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"remaining_connections", h.shardedRegistry.GetClientCount(),
	)
}

// syncClientRemovalToRedis 同步客户端移除到Redis
func (h *Hub) syncClientRemovalToRedis(client *Client) {
	h.syncActiveConnectionsToRedis()
	h.removeOnlineStatusFromRedis(client)
}

// syncActiveConnectionsToRedis 同步活跃连接数到Redis（使用防抖机制避免竞态条件）
// 当多个客户端快速注册时，使用防抖延迟50ms执行，避免多个goroutine读取不同的连接数并乱序写入Redis
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

		syncx.Go().
			WithTimeout(2 * time.Second).
			OnPanic(func(r any) {
				h.logger.ErrorKV("同步活跃连接数到Redis崩溃", "panic", r, "stack", string(debug.Stack()))
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
func (h *Hub) removeOnlineStatusFromRedis(client *Client) {
	if h.onlineStatusRepo == nil {
		return
	}
	// 使用独立的 context，不依赖 Hub 的生命周期
	// 确保在 Hub 关闭时仍能完成清理操作
	syncx.Go(context.Background()).
		WithTimeout(3 * time.Second).
		OnError(func(err error) {
			h.logger.ErrorKV("从Redis移除在线状态失败",
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
func (h *Hub) kickExistingClients(clients []*Client, reason DisconnectReason) {
	for _, client := range clients {
		h.kickClientWithNotification(client, reason, "您的账号在其他设备登录，当前连接将被断开")

		h.logger.InfoKV("踢出旧连接",
			"user_id", client.UserID,
			"client_id", client.ID,
			"reason", reason,
		)
	}
}

// kickOldestConnection 踢掉最不活跃的连接（基于最后心跳时间）
// 使用 ForEachUserClient 持读锁遍历，消除锁外遍历 map 的数据竞争
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

	h.logger.InfoKV("踢掉最不活跃的连接",
		"client_id", oldestClient.ID,
		"user_id", oldestClient.UserID,
		"last_heartbeat", oldestClient.GetLastHeartbeat(),
		"connected_at", oldestClient.ConnectedAt,
	)

	h.kickClientWithNotification(oldestClient, DisconnectReasonForceOffline, "连接数已达上限，当前连接将被断开")
}

// kickClientWithNotification 踢掉客户端并发送通知（公共方法）
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

		h.sendToClient(h.ctx, client, forceOfflineMsg)
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
func (h *Hub) sendKickNotificationToClients(clients []*Client, msg *HubMessage) bool {
	if len(clients) == 0 {
		return false
	}

	// 预序列化一次（所有客户端复用）
	preSerialized, _ := json.Marshal(msg)

	for _, client := range clients {
		h.sendToClientSerialized(h.ctx, client, msg, preSerialized)
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
