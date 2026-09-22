/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 12:36:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 12:36:00
 * @FilePath: \go-wsc\stats\online.go
 * @Description: 在线状态查询与同步 —— 从 hub/online_status.go 抽出
 *
 * 无存储后端时读侧退化为本地分片注册表视图（单节点内存态），
 * 写侧（SyncOnlineStatusToRedis）返回 ErrOnlineStatusRepositoryNotSet。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package stats

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// GetAllOnlineUserIDs 获取所有在线用户 ID 列表
// 有存储后端时查询全量（含其他节点）；无后端时返回本地注册表视图
// （分片注册表是 WS + SSE 客户端的超集）。
func (m *Manager) GetAllOnlineUserIDs() ([]string, error) {
	repo := m.host.GetOnlineStatusRepo()
	if repo == nil {
		return m.host.GetShardedRegistry().GetOnlineUserIDs(), nil
	}

	ctx, cancel := context.WithTimeout(m.host.Context(), 3*time.Second)
	defer cancel()
	return repo.GetAllOnlineUsers(ctx)
}

// GetOnlineUsersByNode 获取指定节点的在线用户 ID
func (m *Manager) GetOnlineUsersByNode(nodeID string) ([]string, error) {
	repo := m.host.GetOnlineStatusRepo()

	// 查询本节点且无后端时退化为本地视图
	if repo == nil && nodeID == m.host.GetNodeID() {
		return m.GetAllOnlineUserIDs()
	}
	if repo == nil {
		return nil, models.ErrOnlineStatusRepositoryNotSet
	}

	ctx, cancel := context.WithTimeout(m.host.Context(), 3*time.Second)
	defer cancel()
	return repo.GetNodeUsers(ctx, nodeID)
}

// GetOnlineUserCount 获取在线用户总数
func (m *Manager) GetOnlineUserCount() (int64, error) {
	repo := m.host.GetOnlineStatusRepo()
	if repo == nil {
		userIDs, _ := m.GetAllOnlineUserIDs()
		return int64(len(userIDs)), nil
	}

	ctx, cancel := context.WithTimeout(m.host.Context(), 2*time.Second)
	defer cancel()
	return repo.GetOnlineCount(ctx)
}

// SyncOnlineStatusToRedis 同步当前所有在线用户到存储（Hub 启动时或定期对账）
func (m *Manager) SyncOnlineStatusToRedis() error {
	repo := m.host.GetOnlineStatusRepo()
	if repo == nil {
		return models.ErrOnlineStatusRepositoryNotSet
	}
	logger := m.host.GetLogger()

	clients := m.host.GetShardedRegistry().GetAllClients()

	// 从 Hub ctx 派生以透传 trace_id 等元数据
	ctx, cancel := context.WithTimeout(m.host.Context(), 10*time.Second)
	defer cancel()

	if err := repo.BatchSetClientsOnline(ctx, clients); err != nil {
		logger.ErrorKV("批量同步在线状态到Redis失败",
			"error", err,
			"count", len(clients),
			"node_id", m.host.GetNodeID(),
		)
		return err
	}

	logger.InfoKV("批量同步在线状态到Redis成功",
		"count", len(clients),
		"node_id", m.host.GetNodeID(),
	)

	return nil
}

// IsUserOnline 检查用户是否在线（按 ctx 路由信封的 appID+namespace 隔离）
// 路由信封通过 routing 从 ctx 注入，调用方不需传 appID/namespace 参数
// 本地检查走 shardedRegistry.HasUser，跨节点检查走 onlineStatusRepo.IsUserOnline
// （bitmap 启用时 HGET→GETBIT 单次往返，未启用时回退 ZCount/全量过滤，与原 IsUserOnline 等价）
func (m *Manager) IsUserOnline(ctx context.Context, userID string) (bool, error) {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	// 检查 shardedRegistry 主存储（按 appID+namespace 信封过滤，包含 WS + SSE）
	if m.host.GetShardedRegistry().HasUser(userID, appID, ns) {
		return true, nil
	}

	// 如果有 Redis repository，检查其他节点（从 ctx 派生超时上下文，保留路由信封供 repo 过滤）
	if repo := m.host.GetOnlineStatusRepo(); repo != nil {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return repo.IsUserOnline(rctx, userID)
	}

	return false, nil
}

// CheckUserOnline 在线检查（支持分布式，按 ctx 路由信封的 appID+namespace 隔离）
// 路由信封通过 routing 从 ctx 注入；本地与 Redis 查询均按 appID+namespace 信封过滤，
// 避免同名 userID 跨 app/ns 误判在线导致不必要的跨节点路由
//
// 性能热路径：调 IsUserOnline（bitmap 启用时 HGET uid_map → GETBIT 单次 Lua 往返，
// 未启用或无路由信封时回退 ZCount/全量过滤，永远最终一致）。
// 不调 GetUserNodes（ZRANGE + GET ×N + N 次解压，热路径不可接受）；
// 跨节点路由取节点列表由 distributed.go::checkAndRouteToNode 专用路径负责
func (m *Manager) CheckUserOnline(ctx context.Context, userID string) bool {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	// 1. 先检查本地 shardedRegistry 是否在线（按 appID+namespace 信封过滤，原子读零锁开销）
	if m.host.GetShardedRegistry().HasUser(userID, appID, ns) {
		return true
	}

	// 2. 如果本地不在线，且启用了分布式，则查询 Redis（从 ctx 派生超时上下文，保留路由信封供 repo 过滤）
	if repo := m.host.GetOnlineStatusRepo(); repo != nil {
		rctx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()

		online, err := repo.IsUserOnline(rctx, userID)
		if err == nil && online {
			// 用户在其他节点在线（IsUserOnline 已按 appID+namespace 信封过滤）
			return true
		}
	}

	// 3. 本地和 Redis 都没有，用户离线
	return false
}
