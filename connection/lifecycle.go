/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 18:35:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 18:35:00
 * @FilePath: \go-wsc\connection\lifecycle.go
 * @Description: 连接域生命周期管理器 —— 多端登录治理 / 踢出断链 / 精简移除
 *
 * 从 hub/registry.go 域化下沉：域组件持有连接生命周期的治理与断链逻辑，
 * hub 的注册/注销编排（handleRegister/handleUnregister）经本组件委托。
 * 断链顺序契约：CloseChannel（MarkClosed + 关闭生命周期信号）先于
 * CloseConnection（可选 1001 GoingAway + 关闭底层连接），保证写泵先退出。
 * 外部依赖（注销 / 强制下线通知投递 / 关闭状态）经 LifecycleHost 端口注入。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
)

// MultiLoginPolicy 多端登录策略快照（编排层构造期从全局配置提取）
type MultiLoginPolicy struct {
	// AllowMultiLogin 是否允许同一用户多端在线（false：新连接踢掉全部旧连接）
	AllowMultiLogin bool
	// MaxConnectionsPerUser 单用户连接数上限（0 表示不限制；达上限踢最旧连接）
	MaxConnectionsPerUser int
}

// LifecycleManager 连接生命周期管理器（连接域）
//
// 覆盖注册后的准入治理（多端登录策略）与断链执行（踢出通知、
// 精简移除、半注册清理、通道/连接关闭原语）
type LifecycleManager struct {
	host      LifecycleHost
	registry  *ShardedRegistry
	heartbeat *HeartbeatManager
	policy    MultiLoginPolicy
	logger    spi.Logger
}

// NewLifecycleManager 构造连接生命周期管理器
func NewLifecycleManager(host LifecycleHost, registry *ShardedRegistry, heartbeat *HeartbeatManager, policy MultiLoginPolicy) *LifecycleManager {
	logger := host.GetLogger()
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &LifecycleManager{
		host:      host,
		registry:  registry,
		heartbeat: heartbeat,
		policy:    policy,
		logger:    logger,
	}
}

// ============================================================================
// 多端登录策略
// ============================================================================

// EnforceMultiLoginPolicy 统一处理多端登录策略
// 根据策略决定是否允许多端登录、是否限制连接数：
//   - AllowMultiLogin=false：踢掉该用户全部旧连接（同信封快速检查，无旧连接零开销）
//   - MaxConnectionsPerUser>0：达到上限时踢掉最不活跃（最旧心跳）的旧连接
//
// 调用时新连接已入注册表（AddClient 前置），故踢人时排除新连接自身；
// 同 clientID 覆盖已由注销路径的指针一致性校验保护。
// 多端登录策略按 appID+namespace 信封隔离：不同应用/命名空间的连接互不影响
// （app-A 的连接数不挤占 app-B 的配额）
func (m *LifecycleManager) EnforceMultiLoginPolicy(newClient *models.Client) {
	ctx := newClient.Context
	userID := newClient.UserID
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)

	// O(1) 快速检查同信封下用户是否有现有客户端（原子计数器，无锁）
	if !m.registry.HasUser(userID, appID, ns) {
		return
	}

	m.logger.DebugContextKV(ctx, "处理多端登录策略",
		"user_id", userID,
		"new_client_id", newClient.ID,
		"allow_multi_login", m.policy.AllowMultiLogin,
		"max_connections_per_user", m.policy.MaxConnectionsPerUser)

	// 不允许多端登录：踢掉所有旧连接（新连接已入表，排除自身）
	if !m.policy.AllowMultiLogin {
		// 使用 ForEachUserClient 持读锁零拷贝收集客户端（消除锁外遍历数据竞争）
		var clients []*models.Client
		m.registry.ForEachUserClient(userID, func(_ string, client *models.Client) bool {
			if client != newClient {
				clients = append(clients, client)
			}
			return true
		})

		m.logger.InfoContextKV(ctx, "不允许多端登录，踢掉所有旧连接",
			"user_id", userID,
			"old_connections", len(clients))

		m.kickClients(clients)
		return
	}

	// 允许多端登录，但有连接数限制（计数含新连接：old+1 <= max 等价于 old < max）
	if m.policy.MaxConnectionsPerUser > 0 {
		currentCount := m.registry.GetUserClientCount(userID)

		// 未达上限，无需踢人
		if currentCount <= m.policy.MaxConnectionsPerUser {
			return
		}

		// 达到上限：踢掉最早的旧连接
		m.logger.InfoContextKV(ctx, "达到连接数上限，踢掉最早的连接",
			"user_id", userID,
			"current_count", currentCount,
			"max_allowed", m.policy.MaxConnectionsPerUser)

		m.kickOldest(userID, newClient)
	}
	// 允许多端登录且无限制：无需处理
}

// kickClients 踢掉现有客户端（接收切片，调用方负责通过 ForEachUserClient 持锁收集）
// 逐连接用各自 client.Context 保留连接级 trace_id
func (m *LifecycleManager) kickClients(clients []*models.Client) {
	for _, client := range clients {
		m.KickClient(client, models.DisconnectReasonForceOffline, "您的账号在其他设备登录，当前连接将被断开")

		m.logger.InfoContextKV(client.Context, "踢出旧连接",
			"user_id", client.UserID,
			"client_id", client.ID,
			"reason", models.DisconnectReasonForceOffline,
		)
	}
}

// kickOldest 踢掉最不活跃的旧连接（基于最后心跳时间，排除 exclude 指定的新连接）
// 使用 ForEachUserClient 持读锁遍历，消除锁外遍历 map 的数据竞争
func (m *LifecycleManager) kickOldest(userID string, exclude *models.Client) {
	var oldestClient *models.Client
	var oldestTime time.Time

	// 持读锁遍历找出最久没有心跳的客户端
	m.registry.ForEachUserClient(userID, func(_ string, client *models.Client) bool {
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

	m.logger.InfoContextKV(oldestClient.Context, "踢掉最不活跃的连接",
		"client_id", oldestClient.ID,
		"user_id", oldestClient.UserID,
		"last_heartbeat", oldestClient.GetLastHeartbeat(),
		"connected_at", oldestClient.ConnectedAt,
	)

	m.KickClient(oldestClient, models.DisconnectReasonForceOffline, "连接数已达上限，当前连接将被断开")
}

// ============================================================================
// 踢出
// ============================================================================

// KickUser 统一踢出用户全部连接（唯一踢人实现，按 ctx 路由信封 appID+namespace 隔离）
//
// 收集维度：按 ctx 路由信封过滤（与 P2P 投递同一隔离语义）——同名 userID 跨
// app/namespace 多端在线时，仅踢出信封内的连接，不同应用/租户互不误踢；
// gRPC/distributed 路径经路由信封恢复/注入后传入，本地路径继承调用方信封
//
// 幂等语义：用户已无连接（收集数 0）即"已离线"目标达成，结果 KickedConnections=0
// 不视为失败；sendNotification=true 时先向全部连接写入 KickOut 通知再注销
// （Guaranteed 级控制消息，断链前投递，notificationMsg 为通知文案）
//
// ctx 由调用方传入（grpc/distributed 路径已恢复 trace_id），实现全链路追踪
func (m *LifecycleManager) KickUser(ctx context.Context, userID string, reason string, sendNotification bool, notificationMsg string) *models.KickUserResult {
	// 🔏 按路由信封收集（appID+namespace 隔离）：appID 为空（无路由 ctx 的边界场景）
	// 退化为全维度收集（与 ForEachUserClientFiltered 空值语义对称）
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	var clients []*models.Client
	m.registry.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
		clients = append(clients, client)
		return true
	})

	result := &models.KickUserResult{Success: true, KickedConnections: len(clients)}

	// 幂等达成：用户已无信封内连接，无需踢出（0=已离线，非失败）
	if len(clients) == 0 {
		m.logger.InfoContextKV(ctx, "用户已不在线，踢出目标已达成",
			"user_id", userID,
			"reason", reason,
			"app_id", appID,
			"namespace", ns,
		)
		return result
	}

	// 逐连接：可选通知 + 注销；通知先于断开写入发送通道（与 KickClient 同序）
	// 通知消息仅构造一次，多端复用；经各自 client.Context 保留连接级 trace_id
	var kickMsg *models.HubMessage
	if sendNotification {
		content := notificationMsg
		if content == "" {
			content = "您已被强制下线"
		}
		kickMsg = models.NewHubMessage().
			SetMessageType(models.MessageTypeKickOut).
			SetSender(models.UserTypeSystem.String()).
			SetSenderType(models.UserTypeSystem).
			SetReceiver(userID).
			SetContent(content).
			WithContentExtra("reason", reason).
			WithContentExtra("kicked_at", time.Now().Unix())
	}

	for _, client := range clients {
		// Conn 为 nil（SSE 半注册等）跳过通知直接注销，与 KickClient 同语义
		if kickMsg != nil && client.Conn != nil {
			m.host.SendToClient(client.Context, client, kickMsg)
			result.NotificationSent = true
		}
		m.host.Unregister(client)
	}

	m.logger.InfoContextKV(ctx, "用户踢出完成",
		"user_id", userID,
		"reason", reason,
		"app_id", appID,
		"namespace", ns,
		"kicked_connections", result.KickedConnections,
		"notification_sent", result.NotificationSent,
	)

	return result
}

// KickClient 踢掉客户端并发送强制下线通知
// 通知先于断开写入客户端发送通道，写循环异步发出；随后注销连接
func (m *LifecycleManager) KickClient(client *models.Client, reason models.DisconnectReason, message string) {
	if client.Conn != nil {
		forceOfflineMsg := models.NewHubMessage().
			SetMessageType(models.MessageTypeForceOffline).
			SetSender(models.UserTypeSystem.String()).
			SetSenderType(models.UserTypeSystem).
			SetReceiver(client.UserID).
			SetReceiverType(client.UserType).
			SetContent(message).
			WithContentExtra("reason", reason)
		m.host.SendToClient(client.Context, client, forceOfflineMsg)
	}
	m.host.Unregister(client)
}

// ============================================================================
// 移除与断链原语
// ============================================================================

// RemoveUnsafe 从注册表移除客户端（shutdown 路径专用精简清理）
// 仅做：注册表移除 + 时间轮取消 + 指针一致性校验 + 关闭通道与连接；
// 不触发回调与逐条记录落库（由停机批量清理统一处理，
// 避免大量串行写 Redis/DB 导致 shutdown 超时）
func (m *LifecycleManager) RemoveUnsafe(client *models.Client) {
	removed := m.registry.RemoveClient(client.ID, client.UserID)
	if removed == nil {
		return
	}

	// ⏰ 取消时间轮上的心跳超时任务
	m.heartbeat.CancelTimeout(client.ID)

	// 指针一致性校验：旧客户端已被新连接替换时不误删新客户端
	if removed != client {
		m.registry.AddClient(removed)
		return
	}

	m.CloseChannel(client)
	m.CloseConnection(client)
}

// CleanupHalfRegistered 清理半注册连接（注册编排 panic 兜底）
// 半注册状态：连接已加入注册表 + 心跳超时任务已调度，但注册流程中断
// 幂等安全：各清理步骤对"未执行到"的步骤均为无操作
func (m *LifecycleManager) CleanupHalfRegistered(client *models.Client) {
	if client == nil {
		return
	}
	// 移除注册表条目（未注册时无操作）
	m.registry.RemoveClient(client.ID, client.UserID)
	// 撤销已调度的心跳超时任务（未调度时无操作），避免重复注销
	m.heartbeat.CancelTimeout(client.ID)
	// 关闭生命周期信号与底层连接（触发客户端立即重连）
	m.CloseChannel(client)
	m.CloseConnection(client)
}

// CloseChannel 关闭客户端发送通道
// 用 DoneCh 通知写循环退出，数据通道（SendChan/SSEMessageCh）永不 close：
//  1. 消除 TrySend 的 chansend 与本函数 closechan 的数据竞态
//  2. 不回收到对象池（竞态窗口内 racing sender 仍可能写入残留消息，复用会跨连接串消息）
//  3. 不置 nil SendChan，避免与写循环的 select 读产生数据竞争
func (m *LifecycleManager) CloseChannel(client *models.Client) {
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

// CloseConnection 关闭 WebSocket 连接
// Hub 关闭（如 K8s 滚动更新）时先发送 1001 GoingAway 控制帧，
// 让客户端识别为服务端主动离开并触发重连，而不是收到 1006 异常断开
func (m *LifecycleManager) CloseConnection(client *models.Client) {
	if client.Conn == nil {
		return
	}

	// Hub 正在关闭时，先发送 1001 GoingAway 控制帧通知客户端
	// 此时 CloseChannel 已先执行（写循环即将退出），紧随其后的
	// Conn.Close() 保证即使帧交错客户端也只是走异常断开重连，不影响正确性
	if m.host.IsShuttingDown() {
		msg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "server is shutting down")
		_ = client.Conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
	}

	client.Conn.Close()
}
