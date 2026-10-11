/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 23:02:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 23:02:00
 * @FilePath: \go-wsc\publisher\publisher_test.go
 * @Description: 发布器与信封构建器测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package publisher

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// captureBus 捕获发布的信封（测试替身）
type captureBus struct {
	mu   sync.Mutex
	envs []*spi.DeliverEnvelope
}

func (b *captureBus) Publish(_ context.Context, env *spi.DeliverEnvelope) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.envs = append(b.envs, env)
	return nil
}

func (b *captureBus) Subscribe(context.Context, spi.EventBusTopic, spi.EventHandler) error { return nil }
func (b *captureBus) Close() error                                                           { return nil }

func (b *captureBus) snapshot() []*spi.DeliverEnvelope {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*spi.DeliverEnvelope, len(b.envs))
	copy(out, b.envs)
	return out
}

func newTestMessage() *models.HubMessage {
	return &models.HubMessage{
		Receiver:  "u10086",
		AppID:     "app1",
		Namespace: "ns1",
		Content:   "hello",
	}
}

func TestEnvelopeBuilder_Deliver(t *testing.T) {
	msg := newTestMessage()
	msg.MessageID = "biz-001"

	env := NewEnvelope(msg).Deliver()
	assert.Equal(t, spi.IntentDeliver, env.Intent)
	assert.Equal(t, "biz-001", env.Message.MessageID, "已设置的 MessageID 保持不变")
	assert.False(t, env.ExcludeSender)
	assert.False(t, env.PublishedAt.IsZero(), "PublishedAt 自动填充")
}

func TestEnvelopeBuilder_BroadcastExcludeSender(t *testing.T) {
	env := NewEnvelope(newTestMessage()).ExcludeSender().Broadcast()
	assert.Equal(t, spi.IntentBroadcast, env.Intent)
	assert.True(t, env.ExcludeSender)
}

func TestEnvelopeBuilder_AutoMessageID(t *testing.T) {
	env := NewEnvelope(newTestMessage()).Deliver() // MessageID 空值
	assert.NotEmpty(t, env.Message.MessageID, "空 MessageID 应自动补随机 ID")
	assert.Len(t, env.Message.MessageID, 32, "128bit hex 编码 32 字符")

	// 两次构造生成不同 ID（无碰撞）
	env2 := NewEnvelope(newTestMessage()).Deliver()
	assert.NotEqual(t, env.Message.MessageID, env2.Message.MessageID)
}

func TestBuilder_MissingArguments(t *testing.T) {
	_, err := New(nil).WithPublisher("svc").Build()
	assert.ErrorIs(t, err, ErrBusMissing, "缺 bus 报错")

	_, err = New(&captureBus{}).Build()
	assert.ErrorIs(t, err, ErrPublisherName, "缺发布者标识报错")

	pub, err := New(&captureBus{}).WithPublisher("svc").Build()
	require.NoError(t, err)
	assert.Equal(t, "svc", pub.Name())
}

func TestPublisher_PublishStampsPublisher(t *testing.T) {
	bus := &captureBus{}
	pub, err := New(bus).WithPublisher("order-service").Build()
	require.NoError(t, err)

	msg := newTestMessage()
	require.NoError(t, pub.Publish(context.Background(), NewEnvelope(msg).Deliver()))

	envs := bus.snapshot()
	require.Len(t, envs, 1)
	assert.Equal(t, "order-service", envs[0].Publisher, "发布器统一盖戳")
	assert.NotEmpty(t, envs[0].Message.MessageID)
}

func TestPublisher_PublishAutoMessageID(t *testing.T) {
	bus := &captureBus{}
	pub, err := New(bus).WithPublisher("svc").Build()
	require.NoError(t, err)

	// 信封未走 Builder 的裸路径：Publish 终检兜底补 MessageID
	require.NoError(t, pub.Publish(context.Background(), &spi.DeliverEnvelope{
		Intent:  spi.IntentDeliver,
		Message: newTestMessage(),
	}))
	assert.NotEmpty(t, bus.snapshot()[0].Message.MessageID, "Publish 终检兜底空 MessageID")
}

func TestPublisher_PublishNilEnvelope(t *testing.T) {
	pub, err := New(&captureBus{}).WithPublisher("svc").Build()
	require.NoError(t, err)
	assert.ErrorIs(t, pub.Publish(context.Background(), nil), ErrEnvelopeInvalid)
}
