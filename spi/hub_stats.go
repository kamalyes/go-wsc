/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 11:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 11:00:00
 * @FilePath: \go-wsc\spi\hub_stats.go
 * @Description: Hub 统计信息 SPI - HubStats 接口契约定义
 *
 * 节点/集群级别的连接与消息统计契约
 * Redis 实现见 adapter/redis 包 HubStats
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// HubStats Hub 统计信息接口
//
// 职责：
//  1. 节点级统计：连接数、消息收发、广播计数
//  2. 集群级统计：所有节点汇总
//  3. 节点注册发现：注册/注销、心跳、TTL 管理
//
// 存储维度：
//   - 按 nodeID 隔离，key = "{prefix}node:{nodeID}"
//   - 全局节点集合 key = "{prefix}nodes"
type HubStats interface {
	// ========== 连接统计 ==========

	// UpdateConnectionStats 批量更新连接统计（总连接数+1、活跃连接数、心跳时间）
	UpdateConnectionStats(ctx context.Context, nodeID string, activeCount int64) error

	// IncrementTotalConnections 增加总连接数
	IncrementTotalConnections(ctx context.Context, nodeID string, delta int64) error

	// SetActiveConnections 设置当前活跃连接数
	SetActiveConnections(ctx context.Context, nodeID string, count int64) error

	// ========== 消息统计 ==========

	// IncrementMessagesSent 增加已发送消息数
	IncrementMessagesSent(ctx context.Context, nodeID string, delta int64) error

	// IncrementMessagesReceived 增加已接收消息数
	IncrementMessagesReceived(ctx context.Context, nodeID string, delta int64) error

	// IncrementBroadcastsSent 增加已发送广播数
	IncrementBroadcastsSent(ctx context.Context, nodeID string, delta int64) error

	// ========== 节点注册发现 ==========

	// RegisterNode 注册节点并初始化统计信息（设置启动时间、添加到节点集合）
	RegisterNode(ctx context.Context, nodeID string, startTime int64) error

	// GetNodeStats 获取指定节点的统计信息
	GetNodeStats(ctx context.Context, nodeID string) (*models.NodeStats, error)

	// GetAllNodesStats 获取所有节点的统计信息
	GetAllNodesStats(ctx context.Context) (map[string]*models.NodeStats, error)

	// GetTotalStats 获取集群总统计信息（所有节点汇总）
	GetTotalStats(ctx context.Context) (*models.ClusterStats, error)

	// CleanupNodeStats 清理已下线节点的统计数据
	CleanupNodeStats(ctx context.Context, nodeID string) error

	// UpdateNodeHeartbeat 更新节点心跳时间
	UpdateNodeHeartbeat(ctx context.Context, nodeID string) error

	// GetActiveNodes 获取活跃的节点列表（基于心跳）
	GetActiveNodes(ctx context.Context, timeout time.Duration) ([]string, error)
}
