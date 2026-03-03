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
	"fmt"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/events"
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
	h.unregister <- client
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

	h.logger.InfoKV("handleRegister开始",
		"client_id", client.ID,
		"user_id", client.UserID)

	h.mutex.Lock()
	defer h.mutex.Unlock()

	// 设置客户端所在节点
	client.NodeID = h.nodeID
	client.NodeIP = h.config.NodeIP
	client.NodePort = h.config.NodePort

	// 初始化客户端 SendChan（根据客户端类型使用配置的容量）
	h.initClientSendChan(client)

	// 初始化客户端时间戳（如果未设置）
	now := time.Now()
	client.ConnectedAt = mathx.IfNotZero(client.ConnectedAt, now)
	client.LastHeartbeat = mathx.IfNotZero(client.LastHeartbeat, now)
	client.LastSeen = mathx.IfNotZero(client.LastSeen, now)

	// 统一处理多端登录逻辑
	h.handleMultiLoginPolicy(client)
	h.syncClientStats()              // 增量更新总连接数（异步读最新值）
	h.syncActiveConnectionsToRedis() // 设置活跃连接数（异步读最新值）
	h.logClientConnection(client)

	// 保存连接记录到数据库（异步）
	if h.connectionRecordRepo != nil {
		record := h.CreateConnectionRecord(client)
		h.saveConnectionRecord(record)
	}

	// 调用客户端连接回调
	if h.clientConnectCallback != nil {
		ctx := context.Background()
		if err := h.clientConnectCallback(ctx, client); err != nil {
			h.logger.ErrorKV("客户端连接回调执行失败",
				"client_id", client.ID,
				"user_id", client.UserID,
				"error", err,
			)
			// 调用错误回调
			if h.errorCallback != nil {
				_ = h.errorCallback(ctx, err, ErrorSeverityError)
			}
		}
	}

	// 异步任务
	go h.syncOnlineStatus(client)
	go h.pushOfflineMessagesOnConnect(client)

	// 📡 发布用户上线事件（所有用户类型）
	go events.PublishUserOnline(h, client.UserID, client.UserType, client.ID)

	h.sendWelcomeMessage(client)

	if client.Conn != nil {
		go h.handleClientWrite(client)
		go h.handleClientRead(client)
	}
}

// handleUnregister 处理客户端注销（内部方法）
func (h *Hub) handleUnregister(client *Client) {
	// 📡 发布用户下线事件（所有用户类型，在锁外发布）
	go events.PublishUserOffline(h, client.UserID, client.UserType, client.ID)

	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.removeClientUnsafe(client)
}

// ============================================================================
// 多端登录策略处理
// ============================================================================

// handleMultiLoginPolicy 统一处理多端登录策略（内部方法，需要外部加锁）
// 根据配置决定是否允许多端登录、是否限制连接数
func (h *Hub) handleMultiLoginPolicy(newClient *Client) {
	userID := newClient.UserID
	existingClients, exists := h.userToClients[userID]

	h.logger.DebugKV("处理多端登录策略",
		"user_id", userID,
		"new_client_id", newClient.ID,
		"existing_clients_count", len(existingClients),
		"allow_multi_login", h.config.AllowMultiLogin,
		"max_connections_per_user", h.config.MaxConnectionsPerUser)

	// 如果用户没有旧连接，直接添加新客户端
	if !exists || len(existingClients) == 0 {
		h.addNewClient(newClient)
		return
	}

	// 不允许多端登录：踢掉所有旧连接
	if !h.config.AllowMultiLogin {
		h.logger.InfoKV("不允许多端登录，踢掉所有旧连接",
			"user_id", userID,
			"old_connections", len(existingClients))

		h.kickExistingClientsUnsafe(existingClients, DisconnectReasonForceOffline)
		h.addNewClient(newClient)
		return
	}

	// 允许多端登录，但有连接数限制
	if h.config.MaxConnectionsPerUser > 0 {
		currentCount := len(existingClients)
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

		h.kickOldestConnection(existingClients)
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
		result.Reason = fmt.Sprintf("%s (用户不在线)", reason)
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
		// 等待一小段时间，确保通知消息送达
		time.Sleep(100 * time.Millisecond)
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

// removeClientUnsafe 移除客户端（不加锁，需要外部加锁）
func (h *Hub) removeClientUnsafe(client *Client) {
	if _, exists := h.clients[client.ID]; !exists {
		return
	}

	h.logClientDisconnection(client)
	h.removeClientFromMaps(client)
	h.syncClientRemovalToRedis(client)
	h.closeClientChannel(client)
	h.closeClientConnection(client) // 关闭 WebSocket 连接

	// 更新连接断开记录
	h.updateConnectionOnDisconnect(client, DisconnectReasonClientRequest)

	// 调用客户端断开回调
	if h.clientDisconnectCallback != nil {
		syncx.Go().
			OnError(func(err error) {
				h.logger.ErrorKV("客户端断开回调执行失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
				// 调用错误回调
				if h.errorCallback != nil {
					ctx := context.Background()
					_ = h.errorCallback(ctx, err, ErrorSeverityWarning)
				}
			}).
			ExecWithContext(func(execCtx context.Context) error {
				return h.clientDisconnectCallback(execCtx, client, DisconnectReasonClientRequest)
			})
	}
}

// logClientDisconnection 记录客户端断开日志
func (h *Hub) logClientDisconnection(client *Client) {
	h.logger.InfoKV("客户端断开连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"remaining_connections", len(h.clients)-1,
	)
}

// removeClientFromMaps 从内存映射中移除客户端
func (h *Hub) removeClientFromMaps(client *Client) {
	delete(h.clients, client.ID)

	// 更新原子计数器
	if client.ConnectionType == ConnectionTypeSSE {
		h.sseClientsCount.Add(-1)
	} else {
		h.activeClientsCount.Add(-1)
	}

	// 从用户连接列表中移除
	if clientMap, exists := h.userToClients[client.UserID]; exists {
		delete(clientMap, client.ID)
		// 如果该用户没有其他连接了，删除整个 map
		if len(clientMap) == 0 {
			delete(h.userToClients, client.UserID)
		}
	}

	// SSE 客户端从专用 map 中移除
	if client.ConnectionType == ConnectionTypeSSE {
		h.sseMutex.Lock()
		if sseMap, exists := h.sseClients[client.UserID]; exists {
			delete(sseMap, client.ID)
			// 如果该用户没有其他 SSE 连接了，删除整个 map
			if len(sseMap) == 0 {
				delete(h.sseClients, client.UserID)
			}
		}
		h.sseMutex.Unlock()
	}

	// 如果是观察者，从观察者映射中移除 - O(1)
	if client.UserType == UserTypeObserver {
		h.removeObserver(client)
	}

	// 从客服连接列表中移除
	if client.UserType == UserTypeAgent || client.UserType == UserTypeBot {
		if agentMap, exists := h.agentClients[client.UserID]; exists {
			delete(agentMap, client.ID)
			// 如果该客服没有其他连接了，删除整个 map
			if len(agentMap) == 0 {
				delete(h.agentClients, client.UserID)
			}
		}
	}
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
				h.logger.ErrorKV("同步活跃连接数到Redis崩溃", "panic", r)
			}).
			ExecWithContext(func(ctx context.Context) error {
				// 再次检查shutdown
				if h.shutdown.Load() {
					return h.statsRepo.SetActiveConnections(ctx, h.nodeID, 0)
				}
				// 读取当前连接数（使用读锁）
				count := syncx.WithRLockReturnValue(&h.mutex, func() int {
					return len(h.clients)
				})
				return h.statsRepo.SetActiveConnections(ctx, h.nodeID, int64(count))
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

// closeClientChannel 关闭客户端发送通道并回收到对象池
func (h *Hub) closeClientChannel(client *Client) {
	// 使用互斥锁保护关闭操作
	client.CloseMu.Lock()
	defer client.CloseMu.Unlock()

	// 标记为已关闭，防止其他goroutine继续发送
	if client.IsClosed() {
		return // 已经关闭过了
	}
	client.MarkClosed()

	// 关闭并回收 WebSocket 发送通道
	if client.SendChan != nil {
		close(client.SendChan)
		// 将 channel 放回对象池复用
		h.releaseClientSendChan(client)
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
func (h *Hub) closeClientConnection(client *Client) {
	if client.Conn != nil {
		client.Conn.Close()
	}
}
