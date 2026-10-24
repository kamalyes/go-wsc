/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-24 20:20:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-24 20:20:00
 * @FilePath: \go-wsc\broker\consumer_test.go
 * @Description: 消费入口治理回归测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConsumer_NACKRedeliveryNotBlockedByDedup NACK 重投不被去重窗拦截的回归
// 回归场景：去重标记先于投递（Seen 在 Deliver 之前），首次投递受理失败 NACK 后，
// 适配器重投同一 MessageID 被"重复投递信封已跳过"拦截并 ack——
// 消息从未成功投递却被确认消费，必达/可靠档永久丢失（At-Least-Once 破产）
func TestConsumer_NACKRedeliveryNotBlockedByDedup(t *testing.T) {
	var calls atomic.Int32
	deliver := func(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
		if calls.Add(1) == 1 {
			// 首次投递受理失败（如离线入库瞬时失败）
			return &models.DeliverResult{Errors: []error{errors.New("离线入库瞬时失败")}}
		}
		return &models.DeliverResult{}
	}
	c := NewConsumer(deliver, 0, 0, nil)

	env := &spi.DeliverEnvelope{
		Publisher: "node-1",
		Message: &models.HubMessage{
			ID: "m-1", MessageID: "mid-1", SessionID: "s-1",
			Sender: "u-1", Receiver: "u-2",
			MessageType:       models.MessageTypeText,
			DeliveryGuarantee: models.GuaranteeGuaranteed, // 必达档：过本地去重窗
			AppID:             "app-1",
		},
	}

	// 首次消费：受理失败 → NACK（error 返回触发适配器重投）
	require.Error(t, c.handle(context.Background(), env))
	assert.Equal(t, int32(1), calls.Load(), "首次投递执行一次")

	// 适配器重投同一信封：不得被去重窗拦截（修复前此处被跳过并 ack，消息丢失）
	require.NoError(t, c.handle(context.Background(), env))
	assert.Equal(t, int32(2), calls.Load(), "重投副本重新投递")

	// 第三次（成功后的真实重复副本）：去重窗正常拦截
	require.NoError(t, c.handle(context.Background(), env))
	assert.Equal(t, int32(2), calls.Load(), "成功后的重复副本被去重窗拦截")
}
