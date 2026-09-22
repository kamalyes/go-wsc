/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-25 12:28:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 12:28:00
 * @FilePath: \go-wsc\stats\manager.go
 * @Description: 统计域 —— 客户端统计同步、在线状态同步、消息/心跳/错误追踪
 *
 * 从 hub/stats.go 抽出。依赖通过 Host 端口注入，不持有 *hub.Hub。
 *
 * 所有仓储字段都是「未注入即 no-op」语义 —— 纯 WebSocket 连接管理场景
 * 不配置任何存储后端时，追踪方法应在首个 nil 判断处返回，不做多余计算。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package stats

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/models"
)

// Manager 统计域管理器
type Manager struct {
	host Host
}

// NewManager 创建统计域管理器
func NewManager(host Host) *Manager {
	return &Manager{host: host}
}

// ============================================================================
// 客户端统计同步与日志
// ============================================================================

// SyncClientStats 同步客户端统计信息到 Redis（内部获取最新连接数）
func (m *Manager) SyncClientStats() {
	repo := m.host.GetStatsRepo()
	if repo == nil {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r interface{}) {
			m.host.GetLogger().ErrorKV("同步客户端统计崩溃", "panic", r, "stack", string(debug.Stack()))
		}).
		ExecWithContext(func(ctx context.Context) error {
			_ = repo.UpdateConnectionStats(ctx, m.host.GetNodeID(), m.host.GetShardedRegistry().GetClientCount())
			return nil
		})
}

// LogClientConnection 记录客户端连接日志（单行 KV，高频路径）
func (m *Manager) LogClientConnection(client *models.Client) {
	m.host.GetLogger().InfoContextKV(client.Context, "👤 客户端连接成功",
		"user_id", client.UserID,
		"client_id", client.ID,
		"user_type", client.UserType,
		"client_ip", client.ClientIP,
		"active_connections", m.host.GetShardedRegistry().GetClientCount(),
	)
}

// SyncOnlineStatus 同步在线状态到指定存储
func (m *Manager) SyncOnlineStatus(client *models.Client) {
	repo := m.host.GetOnlineStatusRepo()
	if repo == nil {
		return
	}
	logger := m.host.GetLogger()

	// 连接级 ctx 优先（携带握手 trace_id），nil 兜底 Background 避免 WithTimeout panic
	baseCtx := client.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 3*time.Second)
	defer cancel()

	logger.DebugContextKV(ctx, "开始同步在线状态到Redis",
		"user_id", client.UserID,
		"client_id", client.ID,
	)

	if err := repo.SetClientOnline(ctx, client); err != nil {
		logger.ErrorContextKV(ctx, "同步在线状态到Redis失败",
			"user_id", client.UserID,
			"error", err,
		)
	} else {
		logger.DebugContextKV(ctx, "同步在线状态到Redis成功",
			"user_id", client.UserID,
			"client_id", client.ID,
		)
	}
}

// ============================================================================
// 连接统计追踪
// ============================================================================

// ShouldTrackUserStats 判断是否应该追踪用户统计（排除系统、机器人、观察者）
func (m *Manager) ShouldTrackUserStats(userType models.UserType) bool {
	return userType != models.UserTypeSystem &&
		userType != models.UserTypeBot &&
		userType != models.UserTypeObserver
}

// TrackSenderMessageStats 追踪发送者的消息统计
func (m *Manager) TrackSenderMessageStats(connectionID string, senderType models.UserType) {
	if m.host.GetConnectionQualityRepository() == nil || connectionID == "" {
		return
	}

	// 排除系统、机器人、观察者
	if !m.ShouldTrackUserStats(senderType) {
		return
	}

	// 使用批量更新器，避免每条消息创建 goroutine
	if b := m.host.GetMessageStatsBatcher(); b != nil {
		b.Submit(&batcher.StatsIncrementItem{
			ConnectionID: connectionID,
			MessagesSent: 1,
		})
	}
}

// TrackReceiverMessageStats 追踪接收者的消息和字节统计
func (m *Manager) TrackReceiverMessageStats(connectionID string, receiverType models.UserType, dataSize int) {
	if m.host.GetConnectionQualityRepository() == nil || connectionID == "" {
		return
	}

	// 排除系统、机器人、观察者
	if !m.ShouldTrackUserStats(receiverType) {
		return
	}

	// 使用批量更新器，避免每条消息创建 goroutine
	if b := m.host.GetMessageStatsBatcher(); b != nil {
		b.Submit(&batcher.StatsIncrementItem{
			ConnectionID:     connectionID,
			MessagesReceived: 1,
			BytesReceived:    int64(dataSize),
		})
	}
}

// TrackConnectionError 追踪连接错误
func (m *Manager) TrackConnectionError(ctx context.Context, connectionID string, userType models.UserType, err error) {
	repo := m.host.GetConnectionQualityRepository()
	if repo == nil || connectionID == "" || err == nil {
		return
	}

	// 排除系统、机器人、观察者
	if !m.ShouldTrackUserStats(userType) {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r any) {
			m.host.GetLogger().ErrorContextKV(ctx, "记录连接错误崩溃", "panic", r, "stack", string(debug.Stack()), "connection_id", connectionID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return repo.AddError(ctx, connectionID, err)
		})
}

// TrackHeartbeatStats 追踪心跳和 Ping 统计
// 优化：使用批量聚合器，避免每次心跳都启动 goroutine 写数据库
// 心跳时间戳由 batcher flush 写 connect 表，Ping 统计写 quality 表
func (m *Manager) TrackHeartbeatStats(client *models.Client) {
	if (m.host.GetConnectionQualityRepository() == nil && m.host.GetConnectionRecordRepo() == nil) || client == nil {
		return
	}

	// 排除系统、机器人、观察者
	if !m.ShouldTrackUserStats(client.UserType) {
		return
	}

	// 计算Ping延迟（原子读，避免与 SetLastHeartbeat 并发写产生数据竞争）
	pingMs := float64(0)
	lastHeartbeat := client.GetLastHeartbeat()
	if !lastHeartbeat.IsZero() {
		pingMs = float64(time.Since(lastHeartbeat).Milliseconds())
	}

	// 使用批量更新器，避免每次心跳都启动 goroutine
	if b := m.host.GetHeartbeatBatcher(); b != nil {
		b.Submit(&batcher.HeartbeatStatsEntry{
			ClientID: client.ID,
			PingTime: lastHeartbeat,
			PongTime: client.GetLastPong(),
			PingMs:   pingMs,
		})
	}
}

// GetUptime 获取 Hub 运行时长（毫秒）
func (m *Manager) GetUptime() int64 {
	return time.Since(m.host.GetStartTime()).Milliseconds()
}

// GetStats 获取统计信息快照（本地注册表计数 + 节点级消息计数）
func (m *Manager) GetStats() *models.HubStats {
	// shardedRegistry 原子计数（主存储 + 分类索引）
	registry := m.host.GetShardedRegistry()
	totalCount := registry.GetClientCount()
	sseCount := registry.GetSSEClientCount()
	agentCount := int64(registry.GetAgentUserCount())

	stats := &models.HubStats{
		TotalClients:     totalCount,
		WebSocketClients: totalCount - sseCount, // WS 连接数 = 总连接数 - SSE 连接数
		SSEClients:       sseCount,
		AgentConnections: agentCount,
		QueuedMessages:   0,
		OnlineUsers:      int(registry.GetUserCount()),
		Uptime:           m.GetUptime(),
	}

	// 从 statsRepo 获取更详细的统计信息
	if repo := m.host.GetStatsRepo(); repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		if nodeStats, err := repo.GetNodeStats(ctx, m.host.GetNodeID()); err == nil && nodeStats != nil {
			stats.MessagesSent = nodeStats.MessagesSent
			stats.MessagesReceived = nodeStats.MessagesReceived
			stats.BroadcastsSent = nodeStats.BroadcastsSent
		}
	}

	return stats
}

// GetHubHealth 获取健康状态快照（使用 shardedRegistry 原子计数器）
func (m *Manager) GetHubHealth() *models.HubHealthInfo {
	registry := m.host.GetShardedRegistry()
	totalCount := int(registry.GetClientCount())
	sseCount := int(registry.GetSSEClientCount())

	return &models.HubHealthInfo{
		Status:           "healthy",
		IsRunning:        m.host.IsStarted(),
		WebSocketCount:   totalCount - sseCount,
		SSECount:         sseCount,
		TotalConnections: totalCount,
		NodeID:           m.host.GetNodeID(),
	}
}
