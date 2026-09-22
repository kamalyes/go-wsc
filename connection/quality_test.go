/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 13:16:00
 * @FilePath: \go-wsc\connection\quality_test.go
 * @Description: 连接质量查询测试 - 端口透传 + 存储未注入哨兵
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// passthroughQualityStore 透传存储桩（嵌入接口；回放固定数据，验证服务层透传语义）
type passthroughQualityStore struct {
	spi.ConnectionQualityStore
	byConn        *models.ConnectionQuality
	byUser        []*models.ConnectionQuality
	highError     []*models.ConnectionQuality
	freqReconnect []*models.ConnectionQuality
}

func (s *passthroughQualityStore) GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionQuality, error) {
	if s.byConn != nil {
		return s.byConn, nil
	}
	return nil, errors.New("quality not found")
}

func (s *passthroughQualityStore) GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionQuality, error) {
	return s.byUser, nil
}

func (s *passthroughQualityStore) GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*models.ConnectionQuality, error) {
	return s.highError, nil
}

func (s *passthroughQualityStore) GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold int, limit int) ([]*models.ConnectionQuality, error) {
	return s.freqReconnect, nil
}

// TestQualityServicePassthrough 端口透传：四个查询维度直达存储层
func TestQualityServicePassthrough(t *testing.T) {
	store := &passthroughQualityStore{
		byConn:        &models.ConnectionQuality{ConnectionID: "q-1", QualityScore: 87},
		byUser:        []*models.ConnectionQuality{{ConnectionID: "q-1"}, {ConnectionID: "q-2"}},
		highError:     []*models.ConnectionQuality{{ConnectionID: "q-err"}},
		freqReconnect: []*models.ConnectionQuality{{ConnectionID: "q-recon"}},
	}
	svc := NewQualityService(store)
	ctx := context.Background()

	got, err := svc.GetByConnectionID(ctx, "q-1")
	require.NoError(t, err)
	assert.Equal(t, float64(87), got.QualityScore)

	list, err := svc.GetByUserID(ctx, "u-1")
	require.NoError(t, err)
	assert.Len(t, list, 2, "多设备质量行应全量返回")

	high, err := svc.GetHighErrorRateConnections(ctx, 10, 0)
	require.NoError(t, err)
	assert.Len(t, high, 1)

	freq, err := svc.GetFrequentReconnectConnections(ctx, 5, 0)
	require.NoError(t, err)
	assert.Len(t, freq, 1)
}

// TestQualityServiceStoreNotSet 存储未注入：全部查询返回哨兵 ErrQualityStoreNotSet
func TestQualityServiceStoreNotSet(t *testing.T) {
	svc := NewQualityService(nil)
	ctx := context.Background()

	_, err := svc.GetByConnectionID(ctx, "any")
	require.ErrorIs(t, err, ErrQualityStoreNotSet)
	_, err = svc.GetByUserID(ctx, "any")
	require.ErrorIs(t, err, ErrQualityStoreNotSet)
	_, err = svc.GetHighErrorRateConnections(ctx, 1, 0)
	require.ErrorIs(t, err, ErrQualityStoreNotSet)
	_, err = svc.GetFrequentReconnectConnections(ctx, 1, 0)
	require.ErrorIs(t, err, ErrQualityStoreNotSet)
}
