/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 09:21:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 09:21:00
 * @FilePath: \go-wsc\spi\online_store.go
 * @Description: 在线状态存储 SPI - OnlineStore 接口契约定义
 *
 * 在线/离线状态、多设备客户端索引、分布式节点归属查询的统一契约
 * Redis Bitmap 实现见 adapter/redis 的 OnlineStore，
 * 统一 bitmap 方案：位即真相，无 ZSET 兜底
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
)

// OnlineStore 在线状态仓库接口
type OnlineStore interface {
	// ========== 客户端连接管理 ==========

	// SetClientOnline 设置客户端在线（支持多设备）
	SetClientOnline(ctx context.Context, client *models.Client) error

	// SetClientOffline 设置指定客户端离线
	SetClientOffline(ctx context.Context, client *models.Client) error

	// SetOffline 设置用户所有客户端离线
	SetOffline(ctx context.Context, userID string) error

	// GetClient 获取客户端信息
	GetClient(ctx context.Context, clientID string) (*models.Client, error)

	// GetClientOwner 获取 clientID 当前归属节点（上线脚本写入 owner key）
	// 返回空串表示无归属记录（旧数据或已过期）；用于检测同 clientID 跨节点迁移
	GetClientOwner(ctx context.Context, clientID string) (string, error)

	// GetUserClients 获取用户的所有在线客户端
	GetUserClients(ctx context.Context, userID string) ([]*models.Client, error)

	// UpdateClientHeartbeat 更新客户端心跳
	UpdateClientHeartbeat(ctx context.Context, clientID string) error

	// ========== 用户在线状态查询 ==========

	// IsUserOnline 检查用户是否在线（任意设备，按 ctx 路由信封 appID+namespace 隔离）
	// 路径自适应：有路由信封时走 L1 offset 缓存 → GETBIT（命中零网络），
	// 无路由信封走 unscoped GETBIT；位即真相，无 ZSET 兜底
	IsUserOnline(ctx context.Context, userID string) (bool, error)

	// BatchIsUserOnline 批量在线判定，返回 map[userID]bool（含全部查询的 userID）
	// Pipeline 实现：offset 解析（L1+HGET）→ GETBIT，两次往返覆盖 N 个用户（全命中 L1 时一次）
	BatchIsUserOnline(ctx context.Context, userIDs []string) (map[string]bool, error)

	// GetAllOnlineUsers 获取所有在线用户ID列表
	GetAllOnlineUsers(ctx context.Context) ([]string, error)

	// GetOnlineCount 获取在线用户总数
	GetOnlineCount(ctx context.Context) (int64, error)

	// GetOnlineUsersByType 根据用户类型获取在线用户
	GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error)

	// ========== 分布式节点查询 ==========

	// GetUserNodes 获取用户所在的所有节点（支持多设备）
	GetUserNodes(ctx context.Context, userID string) ([]string, error)

	// BatchGetUserNodes 批量获取多个用户所在的所有节点（Pipeline 优化，避免 N+1 查询）
	// 返回 map[userID][]nodeIDs，未找到的 userID 不在 map 中
	BatchGetUserNodes(ctx context.Context, userIDs []string) (map[string][]string, error)

	// GetNodeClients 获取节点的所有在线客户端
	GetNodeClients(ctx context.Context, nodeID string) ([]*models.Client, error)

	// GetNodeUsers 获取节点的所有在线用户ID
	GetNodeUsers(ctx context.Context, nodeID string) ([]string, error)

	// ========== 批量操作 ==========

	// BatchSetClientsOnline 批量设置客户端在线（全量：JSON 序列化 + SETEX client 详情）
	BatchSetClientsOnline(ctx context.Context, clients []*models.Client) error

	// RenewClientsOnline 心跳批量续期（轻量路径：跳过序列化/压缩/SETEX，仅续期索引与 bitmap）
	// client:<id> 键缺失（过期/淘汰）的客户端内部自动走全量重建，自愈语义与 BatchSetClientsOnline 一致
	// 供心跳高频刷新使用，千万级连接下避免每周期全量重写 client 详情
	RenewClientsOnline(ctx context.Context, clients []*models.Client) error

	// BatchSetClientsOffline 批量设置客户端离线
	BatchSetClientsOffline(ctx context.Context, clientIDs []string) error

	// BatchSetClientsOfflineWithInfo 批量设置客户端离线（使用已知的客户端信息）
	// 客户端信息已知时使用，避免从 Redis 查询，确保即使 client key 已被删除也能清理 bitmap 位与索引
	BatchSetClientsOfflineWithInfo(ctx context.Context, clients []*models.Client) error

	// ========== 维护清理 ==========

	// CleanupExpired 清理当前节点的过期客户端
	CleanupExpired(ctx context.Context, nodeID string) (int64, error)
}
