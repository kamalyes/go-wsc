/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 10:05:19
 * @FilePath: \go-wsc\hub\events_test.go
 * @Description: Hub 事件发布订阅适配器白盒单元测试（覆盖 hub/events.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/events"
	"github.com/kamalyes/go-wsc/models"
)

// TestPublishUserOnline_NoPubSub 验证无 PubSub 时发布上线事件不 panic（no-op）
func TestPublishUserOnline_NoPubSub(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.NotPanics(t, func() {
		hub.PublishUserOnline(context.Background(), "u-ev1", models.UserTypeCustomer, "c-ev1")
	})
}

// TestPublishUserOffline_NoPubSub 验证无 PubSub 时发布下线事件不 panic（no-op）
func TestPublishUserOffline_NoPubSub(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.NotPanics(t, func() {
		hub.PublishUserOffline(context.Background(), "u-ev2", models.UserTypeAgent, "c-ev2")
	})
}

// TestSubscribeUserOnline_NoPubSub 验证无 PubSub 时订阅上线事件返回错误
func TestSubscribeUserOnline_NoPubSub(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	unsub, err := hub.SubscribeUserOnline(context.Background(), func(event *UserStatusEvent) error {
		return nil
	})
	require.ErrorIs(t, err, events.ErrPubSubNotSet)
	assert.Nil(t, unsub)
}

// TestSubscribeUserOffline_NoPubSub 验证无 PubSub 时订阅下线事件返回错误
func TestSubscribeUserOffline_NoPubSub(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	unsub, err := hub.SubscribeUserOffline(context.Background(), func(event *UserStatusEvent) error {
		return nil
	})
	require.ErrorIs(t, err, events.ErrPubSubNotSet)
	assert.Nil(t, unsub)
}
