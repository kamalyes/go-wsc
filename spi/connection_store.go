/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:30:00
 * @FilePath: \go-wsc\spi\connection_store.go
 * @Description: 连接记录存储 SPI - ConnectionStore 接口契约定义
 *
 * 连接身份与会话生命周期的持久化契约（connect 维度）。
 * GORM 实现见 adapter/gorm 包 ConnectionStore。
 *
 * 与 ConnectionQualityStore 的分工：本接口只承载 connect 身份 + 会话生命周期
 * （connected_at/disconnected_at/duration/心跳时间戳），质量指标（Ping 统计、
 * 消息字节数、错误、评分）由 ConnectionQualityStore 承载。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// ConnectionStore 连接记录存储接口
type ConnectionStore interface {
	// ========== 核心操作 ==========

	// Upsert 创建或更新连接记录（首次连接创建，重连时更新）
	Upsert(ctx context.Context, record *models.ConnectionRecord) error

	// MarkDisconnected 标记连接为已断开（写 duration/disconnected_at/is_abnormal 供质量终评读）
	MarkDisconnected(ctx context.Context, connectionID string, reason models.DisconnectReason, code int) error

	// BatchUpdateHeartbeats 批量更新心跳时间戳（connect 表 last_ping_at/last_pong_at，单事务）
	BatchUpdateHeartbeats(ctx context.Context, entries []*models.HeartbeatUpdateEntry) error

	// GetByConnectionID 根据连接ID获取连接记录
	GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionRecord, error)

	// GetByUserID 根据用户ID获取所有连接记录（支持多设备）
	GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error)

	// GetActiveByUserID 根据用户ID获取所有活跃连接记录
	GetActiveByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error)

	// ========== 查询操作 ==========

	// List 通用列表查询（支持条件过滤）
	List(ctx context.Context, opts *models.ConnectionQueryOptions) ([]*models.ConnectionRecord, error)

	// Count 统计连接数（支持条件过滤）
	Count(ctx context.Context, opts *models.ConnectionQueryOptions) (int64, error)

	// ========== 统计分析操作 ==========

	// GetConnectionStats 获取连接统计信息（仅 connect 身份维度，质量维度由 ConnectionQualityStore 补充）
	GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*models.ConnectionStats, error)

	// GetConnectionStatsByID 根据连接ID获取单个连接的统计信息
	GetConnectionStatsByID(ctx context.Context, connectionID string) (*models.UserConnectionStats, error)

	// GetUserConnectionStats 获取用户所有连接的汇总统计
	GetUserConnectionStats(ctx context.Context, userID string) (*models.UserConnectionStats, error)

	// GetNodeConnectionStats 获取节点连接统计
	GetNodeConnectionStats(ctx context.Context, nodeID string) (*models.NodeConnectionStats, error)

	// ========== 批量操作 ==========

	// BatchUpsert 批量创建或更新连接记录
	BatchUpsert(ctx context.Context, records []*models.ConnectionRecord) error

	// ========== 清理操作 ==========

	// CleanupInactiveRecords 清理非活跃记录
	CleanupInactiveRecords(ctx context.Context, before time.Time) (int64, error)

	// ========== 生命周期 ==========

	// Close 关闭仓库，停止后台任务
	Close() error
}
