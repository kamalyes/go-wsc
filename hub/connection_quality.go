/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\hub\connection_quality.go
 * @Description: Hub 连接质量查询 API
 *
 * 拆表后 wsc_connection_qualities 由 ConnectionQualityRepository 承载，
 * 本文件提供 Hub 层便捷查询入口，供调用方按连接/用户维度查连接健康度与评分
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"
)

// GetConnectionQuality 根据连接ID获取连接质量记录
// 返回 wsc_connection_qualities 表中的质量指标(心跳/消息/错误/重连)+评分，供调用方查询连接健康度
// connectionID 对应 wsc_connection_records.connection_id（两表 1:1 关联）
func (h *Hub) GetConnectionQuality(ctx context.Context, connectionID string) (*ConnectionQuality, error) {
	if h.connectionQualityRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return h.connectionQualityRepo.GetByConnectionID(ctx, connectionID)
}

// GetConnectionQualityByUserID 根据用户ID获取所有连接质量记录（支持多设备）
// 返回该用户全部连接的质量行，可用于评估用户整体连接健康度
func (h *Hub) GetConnectionQualityByUserID(ctx context.Context, userID string) ([]*ConnectionQuality, error) {
	if h.connectionQualityRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return h.connectionQualityRepo.GetByUserID(ctx, userID)
}

// GetHighErrorRateConnections 获取高错误率连接（质量异常检测入口）
// errorThreshold: 错误次数下限；limit: 最多返回条数（<=0 不限制）
func (h *Hub) GetHighErrorRateConnections(ctx context.Context, errorThreshold, limit int) ([]*ConnectionQuality, error) {
	if h.connectionQualityRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return h.connectionQualityRepo.GetHighErrorRateConnections(ctx, errorThreshold, limit)
}

// GetFrequentReconnectConnections 获取频繁重连的连接（质量异常检测入口）
// reconnectThreshold: 重连次数下限；limit: 最多返回条数（<=0 不限制）
func (h *Hub) GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold, limit int) ([]*ConnectionQuality, error) {
	if h.connectionQualityRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return h.connectionQualityRepo.GetFrequentReconnectConnections(ctx, reconnectThreshold, limit)
}
