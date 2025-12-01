/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-01 23:00:00
 * @FilePath: \go-wsc\hub_business_test.go
 * @Description: Hub 高阶业务函数测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestSendToUserWithOfflineCallback 测试智能发送消息
func TestSendToUserWithOfflineCallback(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	t.Run("在线用户-发送成功", func(t *testing.T) {
		userID := "online-user"
		client := &Client{
			ID:       "client-1",
			UserID:   userID,
			UserType: UserTypeCustomer,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}

		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		offlineCalled := false
		msg := &HubMessage{
			MessageID: "msg-1",
			Content:   "test message",
		}

		isOnline, err := hub.SendToUserWithOfflineCallback(ctx, userID, msg, func(m *HubMessage) {
			offlineCalled = true
		})

		assert.True(t, isOnline)
		assert.NoError(t, err)
		assert.False(t, offlineCalled)

		hub.Unregister(client)
	})

	t.Run("离线用户-调用回调", func(t *testing.T) {
		userID := "offline-user"
		offlineCalled := false
		var receivedMsg *HubMessage

		msg := &HubMessage{
			MessageID: "msg-2",
			Content:   "offline message",
		}

		isOnline, err := hub.SendToUserWithOfflineCallback(ctx, userID, msg, func(m *HubMessage) {
			offlineCalled = true
			receivedMsg = m
		})

		assert.False(t, isOnline)
		assert.NoError(t, err)
		assert.True(t, offlineCalled)
		assert.Equal(t, "msg-2", receivedMsg.MessageID)
	})
}

// TestBroadcastToUsers 测试批量发送
func TestBroadcastToUsers(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 注册3个在线用户
	for i := 1; i <= 3; i++ {
		userID := "user-" + string(rune(i+'0'))
		client := &Client{
			ID:       "client-" + string(rune(i+'0')),
			UserID:   userID,
			UserType: UserTypeCustomer,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}
		hub.Register(client)
	}
	time.Sleep(100 * time.Millisecond)

	t.Run("混合在线离线用户", func(t *testing.T) {
		userIDs := []string{"user-1", "user-2", "user-3", "offline-user"}
		offlineCount := 0

		msg := &HubMessage{
			MessageID: "broadcast-msg",
			Content:   "broadcast test",
		}

		result := hub.BroadcastToUsers(ctx, userIDs, msg, func(userID string, msg *HubMessage) {
			offlineCount++
		})

		assert.Equal(t, 3, result.Success)
		assert.Equal(t, 1, result.Offline)
		assert.Equal(t, 1, offlineCount)
	})
}

// TestGetUserOnlineDetails 测试获取用户在线详情
func TestGetUserOnlineDetails(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	t.Run("在线用户详情", func(t *testing.T) {
		userID := "test-user"
		client := &Client{
			ID:       "client-1",
			UserID:   userID,
			UserType: UserTypeAgent,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}

		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		details := hub.GetUserOnlineDetails(userID)

		assert.True(t, details.IsOnline)
		assert.True(t, details.HasWebSocket)
		assert.False(t, details.HasSSE)
		assert.Equal(t, UserTypeAgent, details.UserType)
		assert.Equal(t, UserStatusOnline, details.Status)
		assert.NotNil(t, details.Client)

		hub.Unregister(client)
	})

	t.Run("离线用户详情", func(t *testing.T) {
		details := hub.GetUserOnlineDetails("non-existent-user")

		assert.False(t, details.IsOnline)
		assert.False(t, details.HasWebSocket)
		assert.False(t, details.HasSSE)
	})
}

// TestBatchGetUserOnlineStatus 测试批量获取在线状态
func TestBatchGetUserOnlineStatus(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 注册2个在线用户
	for i := 1; i <= 2; i++ {
		userID := "user-" + string(rune(i+'0'))
		client := &Client{
			ID:       "client-" + string(rune(i+'0')),
			UserID:   userID,
			UserType: UserTypeCustomer,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}
		hub.Register(client)
	}
	time.Sleep(100 * time.Millisecond)

	userIDs := []string{"user-1", "user-2", "offline-user"}
	statuses := hub.BatchGetUserOnlineStatus(userIDs)

	assert.True(t, statuses["user-1"])
	assert.True(t, statuses["user-2"])
	assert.False(t, statuses["offline-user"])
}

// TestFilterOnlineUsers 测试过滤在线用户
func TestFilterOnlineUsers(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 注册2个在线用户
	for i := 1; i <= 2; i++ {
		userID := "user-" + string(rune(i+'0'))
		client := &Client{
			ID:       "client-" + string(rune(i+'0')),
			UserID:   userID,
			UserType: UserTypeCustomer,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}
		hub.Register(client)
	}
	time.Sleep(100 * time.Millisecond)

	userIDs := []string{"user-1", "user-2", "offline-1", "offline-2"}
	
	onlineUsers := hub.FilterOnlineUsers(userIDs)
	assert.Len(t, onlineUsers, 2)
	assert.Contains(t, onlineUsers, "user-1")
	assert.Contains(t, onlineUsers, "user-2")

	offlineUsers := hub.FilterOfflineUsers(userIDs)
	assert.Len(t, offlineUsers, 2)
	assert.Contains(t, offlineUsers, "offline-1")
	assert.Contains(t, offlineUsers, "offline-2")
}

// TestPartitionUsersByOnlineStatus 测试用户分区
func TestPartitionUsersByOnlineStatus(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 注册2个在线用户
	for i := 1; i <= 2; i++ {
		userID := "user-" + string(rune(i+'0'))
		client := &Client{
			ID:       "client-" + string(rune(i+'0')),
			UserID:   userID,
			UserType: UserTypeCustomer,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}
		hub.Register(client)
	}
	time.Sleep(100 * time.Millisecond)

	userIDs := []string{"user-1", "user-2", "offline-1", "offline-2"}
	
	online, offline := hub.PartitionUsersByOnlineStatus(userIDs)

	assert.Len(t, online, 2)
	assert.Len(t, offline, 2)
	assert.Contains(t, online, "user-1")
	assert.Contains(t, online, "user-2")
	assert.Contains(t, offline, "offline-1")
	assert.Contains(t, offline, "offline-2")
}

// TestCountOnlineUsersByType 测试统计各类型在线用户
func TestCountOnlineUsersByType(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 注册3个客服，2个客户
	for i := 1; i <= 3; i++ {
		userID := "agent-" + string(rune(i+'0'))
		client := &Client{
			ID:       "agent-client-" + string(rune(i+'0')),
			UserID:   userID,
			UserType: UserTypeAgent,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}
		hub.Register(client)
	}

	for i := 1; i <= 2; i++ {
		userID := "customer-" + string(rune(i+'0'))
		client := &Client{
			ID:       "customer-client-" + string(rune(i+'0')),
			UserID:   userID,
			UserType: UserTypeCustomer,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}
		hub.Register(client)
	}
	time.Sleep(100 * time.Millisecond)

	counts := hub.CountOnlineUsersByType()

	assert.Equal(t, 3, counts[UserTypeAgent])
	assert.Equal(t, 2, counts[UserTypeCustomer])
}

// TestSendToGroupMembers 测试会话成员发送
func TestSendToGroupMembers(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 注册3个会话成员
	memberIDs := []string{"member-1", "member-2", "member-3"}
	for _, userID := range memberIDs {
		client := &Client{
			ID:       "client-" + userID,
			UserID:   userID,
			UserType: UserTypeCustomer,
			Status:   UserStatusOnline,
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(ctx, ContextKeyUserID, userID),
		}
		hub.Register(client)
	}
	time.Sleep(100 * time.Millisecond)

	msg := &HubMessage{
		MessageID: "group-msg",
		Sender:    "member-1",
		Content:   "test message",
	}

	t.Run("排除发送者", func(t *testing.T) {
		result := hub.SendToGroupMembers(ctx, memberIDs, msg, true, nil)

		// 发送者被排除，所以只发送给2个成员
		assert.Equal(t, 2, result.Success)
	})

	t.Run("包含发送者", func(t *testing.T) {
		result := hub.SendToGroupMembers(ctx, memberIDs, msg, false, nil)

		// 包含发送者，发送给3个成员
		assert.Equal(t, 3, result.Success)
	})
}
