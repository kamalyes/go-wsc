/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 13:16:00
 * @FilePath: \go-wsc\connection\quality.go
 * @Description: 连接质量查询服务
 *
 * 迁移自 hub/connection_quality.go（P2 批1 域化）：原 *Hub 便捷查询入口重组为
 * QualityService 组件，存储经 spi.ConnectionQualityStore 端口注入
 *
 * 拆表后 wsc_connection_qualities 由 ConnectionQualityStore 承载，
 * 本服务提供按连接/用户维度的质量与健康度查询，供调用方做连接诊断
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"errors"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ErrQualityStoreNotSet 质量存储未注入（未启用 gorm 适配器时查询会返回本错误）
var ErrQualityStoreNotSet = errors.New("connection quality store not set")

// qualityQueryTimeout 查询超时（质量查询为低频诊断路径，5s 上限）
const qualityQueryTimeout = 5 * time.Second

// QualityService 连接质量查询服务（薄封装：超时控制 + 存储未注入哨兵）
type QualityService struct {
	store spi.ConnectionQualityStore
}

// NewQualityService 创建质量查询服务（store 为 nil 时查询返回 ErrQualityStoreNotSet）
func NewQualityService(store spi.ConnectionQualityStore) *QualityService {
	return &QualityService{store: store}
}

// GetByConnectionID 根据连接ID获取连接质量记录
// connectionID 对应 wsc_connection_records.connection_id（两表 1:1 关联）
func (s *QualityService) GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionQuality, error) {
	if s.store == nil {
		return nil, ErrQualityStoreNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, qualityQueryTimeout)
	defer cancel()

	return s.store.GetByConnectionID(ctx, connectionID)
}

// GetByUserID 根据用户ID获取所有连接质量记录（支持多设备）
// 返回该用户全部连接的质量行，可用于评估用户整体连接健康度
func (s *QualityService) GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionQuality, error) {
	if s.store == nil {
		return nil, ErrQualityStoreNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, qualityQueryTimeout)
	defer cancel()

	return s.store.GetByUserID(ctx, userID)
}

// GetHighErrorRateConnections 获取高错误率连接（质量异常检测入口）
// errorThreshold: 错误次数下限；limit: 最多返回条数（<=0 不限制）
func (s *QualityService) GetHighErrorRateConnections(ctx context.Context, errorThreshold, limit int) ([]*models.ConnectionQuality, error) {
	if s.store == nil {
		return nil, ErrQualityStoreNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, qualityQueryTimeout)
	defer cancel()

	return s.store.GetHighErrorRateConnections(ctx, errorThreshold, limit)
}

// GetFrequentReconnectConnections 获取频繁重连的连接（质量异常检测入口）
// reconnectThreshold: 重连次数下限；limit: 最多返回条数（<=0 不限制）
func (s *QualityService) GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold, limit int) ([]*models.ConnectionQuality, error) {
	if s.store == nil {
		return nil, ErrQualityStoreNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, qualityQueryTimeout)
	defer cancel()

	return s.store.GetFrequentReconnectConnections(ctx, reconnectThreshold, limit)
}
