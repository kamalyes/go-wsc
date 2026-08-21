/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\repository\connection_quality_repository.go
 * @Description: 连接质量记录仓储 - 与 ConnectionRecordRepository 拆分
 *
 * 承载 wsc_connection_qualities 表的 CRUD + batcher 批量更新 + 断开终评
 * HeartbeatUpdateEntry/StatsIncrementEntry 类型在 connection_repository.go 定义，两 repo 共用
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"fmt"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	sqlbuilder "github.com/kamalyes/go-sqlbuilder/repository"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConnectionQualityRepository 连接质量仓储接口
// 承载运行时质量指标(心跳/消息/错误/重连)与评分，随 batcher 高频批量更新
type ConnectionQualityRepository interface {
	// Upsert 创建或更新质量记录（首次连接建初始零值行 QualityScore=100，重连 reconnect_count+1）
	Upsert(ctx context.Context, quality *models.ConnectionQuality) error

	// BatchUpdateHeartbeats 批量更新 Ping 统计与活跃时间（单事务，心跳时间戳由 ConnectionRecordRepository 写 connect 表）
	BatchUpdateHeartbeats(ctx context.Context, entries []*HeartbeatUpdateEntry) error

	// BatchIncrementStats 批量递增消息/字节统计（单事务）
	BatchIncrementStats(ctx context.Context, entries []*StatsIncrementEntry) error

	// AddError 记录错误
	AddError(ctx context.Context, connectionID string, err error) error

	// FinalizeOnDisconnect 断开终评：读质量行 + connect 表 duration，算 FinalScore 写 quality_score
	FinalizeOnDisconnect(ctx context.Context, connectionID string) error

	// GetByConnectionID 根据连接ID获取质量记录
	GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionQuality, error)

	// GetByUserID 根据用户ID获取所有质量记录
	GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionQuality, error)

	// GetHighErrorRateConnections 获取高错误率连接
	GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*models.ConnectionQuality, error)

	// GetFrequentReconnectConnections 获取频繁重连的连接
	GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold int, limit int) ([]*models.ConnectionQuality, error)

	// WithTableName 设置自定义表名（用于测试隔离）
	WithTableName(tableName string) ConnectionQualityRepository

	// Close 关闭仓库
	Close() error
}

// connectionQualityRepositoryImpl 连接质量仓储实现
type connectionQualityRepositoryImpl struct {
	db         *gorm.DB
	tableName  string // 自定义表名（用于测试隔离）
	logger     logger.ILogger
	cancelFunc context.CancelFunc
}

// NewConnectionQualityRepository 创建连接质量仓储实例
// config 复用 ConnectionRecord 配置（清理等，暂不启用质量表自动清理，留空实现）
func NewConnectionQualityRepository(db *gorm.DB, config *wscconfig.ConnectionRecord, log logger.ILogger) ConnectionQualityRepository {
	_, cancel := context.WithCancel(context.Background())
	_ = config // 质量表清理策略待定，暂不启用
	return &connectionQualityRepositoryImpl{
		db:         db,
		logger:     log,
		cancelFunc: cancel,
	}
}

// WithTableName 设置自定义表名（用于测试隔离）
func (r *connectionQualityRepositoryImpl) WithTableName(tableName string) ConnectionQualityRepository {
	return &connectionQualityRepositoryImpl{
		db:         r.db,
		tableName:  tableName,
		logger:     r.logger,
		cancelFunc: r.cancelFunc,
	}
}

// getDB 获取数据库会话（如果设置了自定义表名则应用）
func (r *connectionQualityRepositoryImpl) getDB(ctx context.Context) *gorm.DB {
	db := r.db.WithContext(ctx)
	if r.tableName != "" {
		return db.Table(r.tableName)
	}
	return db.Model(&models.ConnectionQuality{})
}

// ========== 核心操作 ==========

// Upsert 创建或更新质量记录
// 首次连接：建初始零值行（QualityScore=100）
// 重连：reconnect_count+1，刷新 last_active_at/user_id
func (r *connectionQualityRepositoryImpl) Upsert(ctx context.Context, quality *models.ConnectionQuality) error {
	if quality == nil {
		return fmt.Errorf("quality cannot be nil")
	}
	if quality.ConnectionID == "" {
		return fmt.Errorf("connection_id cannot be empty")
	}

	// 兜底多租户维度（与 connect 表 + Bitmap/ZSET 分桶一致，避免零值导致跨域查询错位）
	quality.AppID = constants.NormalizeAppID(quality.AppID)
	quality.Namespace = constants.NormalizeNamespace(quality.Namespace)

	if quality.QualityScore == 0 {
		quality.QualityScore = 100
	}
	now := time.Now()
	if quality.LastActiveAt == nil {
		quality.LastActiveAt = &now
	}

	// 冲突时递增重连次数并刷新活跃时间（与 ConnectionRecordRepository.BatchUpsert 的 reconnect_count 语义对齐）
	dialect := sqlbuilder.DetectDialect(r.db)
	onConflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "connection_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"reconnect_count": gorm.Expr("reconnect_count + 1"),
			"user_id":         gorm.Expr(dialect.UpsertColumnRef("user_id")),
			"last_active_at":  gorm.Expr(dialect.UpsertColumnRef("last_active_at")),
		}),
	}

	return r.getDB(ctx).
		Clauses(onConflict).
		Omit("").
		Create(quality).Error
}

// BatchUpdateHeartbeats 批量更新 Ping 统计与活跃时间（quality 表）
// 心跳时间戳(last_ping_at/last_pong_at)已切回 connect 表，由 ConnectionRecordRepository.BatchUpdateHeartbeats 写入
// 单事务包裹，单条失败跳过（与 ConnectionRecordRepository 同语义）
func (r *connectionQualityRepositoryImpl) BatchUpdateHeartbeats(ctx context.Context, entries []*HeartbeatUpdateEntry) error {
	if len(entries) == 0 {
		return nil
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx
		if r.tableName != "" {
			query = tx.Table(r.tableName)
		} else {
			query = tx.Model(&models.ConnectionQuality{})
		}

		for _, entry := range entries {
			// 刷新活跃时间（供清理任务判断，心跳时间戳本身落 connect 表）
			updates := make(map[string]any)
			if entry.PingTime != nil {
				updates["last_active_at"] = entry.PingTime
			}
			if len(updates) > 0 {
				if err := query.Where("connection_id = ?", entry.ConnectionID).Updates(updates).Error; err != nil {
					continue
				}
			}

			// 更新 Ping 统计（移动平均，与原 ConnectionRecordRepository 实现一致）
			if entry.PingMs > 0 {
				pingUpdates := map[string]any{
					"average_ping_ms": gorm.Expr("CASE WHEN average_ping_ms > 0 THEN average_ping_ms * 0.7 + ? * 0.3 ELSE ? END", entry.PingMs, entry.PingMs),
					"max_ping_ms":     gorm.Expr("CASE WHEN max_ping_ms = 0 OR max_ping_ms < ? THEN ? ELSE max_ping_ms END", entry.PingMs, entry.PingMs),
					"min_ping_ms":     gorm.Expr("CASE WHEN min_ping_ms = 0 OR min_ping_ms > ? THEN ? ELSE min_ping_ms END", entry.PingMs, entry.PingMs),
				}
				query.Where("connection_id = ?", entry.ConnectionID).Updates(pingUpdates)
			}
		}
		return nil
	})
}

// BatchIncrementStats 批量递增消息/字节统计（单事务）
func (r *connectionQualityRepositoryImpl) BatchIncrementStats(ctx context.Context, entries []*StatsIncrementEntry) error {
	if len(entries) == 0 {
		return nil
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx
		if r.tableName != "" {
			query = tx.Table(r.tableName)
		} else {
			query = tx.Model(&models.ConnectionQuality{})
		}

		for _, entry := range entries {
			updates := make(map[string]any)
			if entry.MessagesSent > 0 {
				updates["messages_sent"] = gorm.Expr("messages_sent + ?", entry.MessagesSent)
			}
			if entry.MessagesReceived > 0 {
				updates["messages_received"] = gorm.Expr("messages_received + ?", entry.MessagesReceived)
			}
			if entry.BytesSent > 0 {
				updates["bytes_sent"] = gorm.Expr("bytes_sent + ?", entry.BytesSent)
			}
			if entry.BytesReceived > 0 {
				updates["bytes_received"] = gorm.Expr("bytes_received + ?", entry.BytesReceived)
			}
			if len(updates) > 0 {
				if err := query.Where("connection_id = ?", entry.ConnectionID).Updates(updates).Error; err != nil {
					continue
				}
			}
		}
		return nil
	})
}

// AddError 记录错误
func (r *connectionQualityRepositoryImpl) AddError(ctx context.Context, connectionID string, err error) error {
	if err == nil {
		return nil
	}

	now := time.Now()
	updates := map[string]any{
		"error_count":   gorm.Expr("error_count + ?", 1),
		"last_error":    err.Error(),
		"last_error_at": now,
	}

	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(updates).Error
}

// FinalizeOnDisconnect 断开终评
// 读质量行 + connect 表 duration，Go 算 FinalScore(duration) 写 quality_score
// 避免跨方言 SQL CASE 兼容问题，读内存计算
func (r *connectionQualityRepositoryImpl) FinalizeOnDisconnect(ctx context.Context, connectionID string) error {
	var quality models.ConnectionQuality
	if err := r.getDB(ctx).Where("connection_id = ?", connectionID).First(&quality).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// 质量记录不存在（可能已被清理），直接返回
			return nil
		}
		return fmt.Errorf("查询质量记录失败: %w", err)
	}

	// 读 connect 表 duration（断开时由 MarkDisconnected 写入）
	var duration int64
	if err := r.db.WithContext(ctx).
		Table(models.ConnectionRecord{}.TableName()).
		Where("connection_id = ?", connectionID).
		Select("duration").Scan(&duration).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			return fmt.Errorf("查询连接时长失败: %w", err)
		}
		duration = 0
	}

	finalScore := quality.FinalScore(duration)
	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		UpdateColumn("quality_score", finalScore).Error
}

// GetByConnectionID 根据连接ID获取质量记录
func (r *connectionQualityRepositoryImpl) GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionQuality, error) {
	var quality models.ConnectionQuality
	err := r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		First(&quality).Error
	if err != nil {
		return nil, err
	}
	return &quality, nil
}

// GetByUserID 根据用户ID获取所有质量记录
func (r *connectionQualityRepositoryImpl) GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionQuality, error) {
	var records []*models.ConnectionQuality
	err := r.getDB(ctx).
		Where("user_id = ?", userID).
		Find(&records).Error
	return records, err
}

// GetHighErrorRateConnections 获取高错误率连接
func (r *connectionQualityRepositoryImpl) GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*models.ConnectionQuality, error) {
	var records []*models.ConnectionQuality

	query := sqlbuilder.NewQuery().
		AddFilter(sqlbuilder.NewGteFilter("error_count", errorThreshold)).
		AddOrder("error_count", "DESC")

	if limit > 0 {
		query.Limit(limit)
	}

	gormDB := r.getDB(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)
	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
	return records, err
}

// GetFrequentReconnectConnections 获取频繁重连的连接
func (r *connectionQualityRepositoryImpl) GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold int, limit int) ([]*models.ConnectionQuality, error) {
	var records []*models.ConnectionQuality

	query := sqlbuilder.NewQuery().
		AddFilter(sqlbuilder.NewGteFilter("reconnect_count", reconnectThreshold)).
		AddOrder("reconnect_count", "DESC")

	if limit > 0 {
		query.Limit(limit)
	}

	gormDB := r.getDB(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)
	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
	return records, err
}

// Close 关闭仓库
func (r *connectionQualityRepositoryImpl) Close() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
	}
	return nil
}
