/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:39:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:39:00
 * @FilePath: \go-wsc\broker\envelope_test.go
 * @Description: 投递信封校验/路由恢复/去重键测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
)

func newTestEnvelope() *spi.DeliverEnvelope {
	return &spi.DeliverEnvelope{
		Intent:      spi.IntentDeliver,
		Publisher:   "order-service",
		Message: &models.HubMessage{
			MessageID: "msg-001",
			Receiver:  "u10086",
			AppID:     "app1",
			Namespace: "ns1",
			Content:   "hello",
		},
	}
}

func TestValidateEnvelope(t *testing.T) {
	assert.NoError(t, ValidateEnvelope(newTestEnvelope()), "合法信封通过")

	assert.ErrorIs(t, ValidateEnvelope(nil), ErrEnvelopeNilMessage)

	noMsg := newTestEnvelope()
	noMsg.Message = nil
	assert.ErrorIs(t, ValidateEnvelope(noMsg), ErrEnvelopeNilMessage)

	noID := newTestEnvelope()
	noID.Message.MessageID = ""
	assert.ErrorIs(t, ValidateEnvelope(noID), ErrEnvelopeMessageID)

	noPublisher := newTestEnvelope()
	noPublisher.Publisher = ""
	assert.ErrorIs(t, ValidateEnvelope(noPublisher), ErrEnvelopePublisher)

	noApp := newTestEnvelope()
	noApp.Message.AppID = ""
	assert.ErrorIs(t, ValidateEnvelope(noApp), ErrEnvelopeAppID)

	hopped := newTestEnvelope()
	hopped.Hops = DefaultMaxHops + 1
	assert.ErrorIs(t, ValidateEnvelope(hopped), ErrEnvelopeHopsExceeded)
}

func TestRestoreRouteCtx(t *testing.T) {
	env := newTestEnvelope()
	env.Message.GroupIDs = []string{"g1", "g2"}

	ctx := RestoreRouteCtx(context.Background(), env.Message)
	rc := routing.RoutingFromContext(ctx)
	require.NotNil(t, rc, "路由信封应已注入 ctx")
	assert.Equal(t, "app1", rc.AppID)
	assert.Equal(t, "ns1", rc.Namespace)
	assert.Equal(t, []string{"g1", "g2"}, rc.GroupIDs)
}

func TestResolveIntent(t *testing.T) {
	// 显式 Intent 优先
	env := newTestEnvelope()
	env.Intent = spi.IntentBroadcast
	assert.Equal(t, spi.IntentBroadcast, ResolveIntent(env))

	// 未设置：有 Receiver → 投递
	env = newTestEnvelope()
	env.Intent = spi.IntentUnset
	assert.Equal(t, spi.IntentDeliver, ResolveIntent(env))

	// 未设置：有 GroupIDs → 投递
	env = newTestEnvelope()
	env.Intent = spi.IntentUnset
	env.Message.Receiver = ""
	env.Message.GroupIDs = []string{"g1"}
	assert.Equal(t, spi.IntentDeliver, ResolveIntent(env))

	// 未设置：无 Receiver 无 GroupIDs → 广播
	env = newTestEnvelope()
	env.Intent = spi.IntentUnset
	env.Message.Receiver = ""
	assert.Equal(t, spi.IntentBroadcast, ResolveIntent(env))
}

func TestFormatDedupKey(t *testing.T) {
	env := newTestEnvelope()
	assert.Equal(t, "msg-001\x00u10086", FormatDedupKey(env), "P2P 消息按复合键")

	env.Message.Receiver = ""
	assert.Equal(t, "msg-001", FormatDedupKey(env), "广播消息按 MessageID 单键")
}

func TestEnvelopeSizeOf(t *testing.T) {
	env := newTestEnvelope()
	env.Message.Data = map[string]interface{}{"k1": "v1", "k2": "v2"}
	size := EnvelopeSizeOf(env)
	assert.Greater(t, size, len("hello"), "估算尺寸应包含 Content 与 Data 键")
	assert.Equal(t, 0, EnvelopeSizeOf(nil), "nil 信封尺寸为 0")
}
