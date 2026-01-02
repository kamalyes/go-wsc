/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-27 22:53:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 16:28:20
 * @FilePath: \go-wsc\hub_kick_test.go
 * @Description: Hub踢出和强制下线功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// TestKickUser 测试踢出用户
func TestKickUser(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册客户端
	client := &Client{
		ID:       "kick-client-1",
		UserID:   "kick-user-1",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 踢出用户
	result := hub.KickUser("kick-user-1", "测试踢出", true, "您已被踢出")

	assert.NotNil(t, result, "KickUserResult不应为空")
	assert.True(t, result.Success, "踢出应该成功")
	assert.Equal(t, 1, result.KickedConnections, "应该踢出1个连接")

	time.Sleep(100 * time.Millisecond)

	// 验证用户已下线
	online, _ := hub.IsUserOnline("kick-user-1")
	assert.False(t, online, "用户应该已下线")
}

// TestKickUserSimple 测试简单踢出
func TestKickUserSimple(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册客户端
	client := &Client{
		ID:       "kick-simple-client",
		UserID:   "kick-simple-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 简单踢出
	count := hub.KickUserSimple("kick-simple-user", "simple kick")
	assert.Equal(t, 1, count, "应该踢出1个连接")

	time.Sleep(50 * time.Millisecond)
}

// TestKickUserWithMessage 测试带消息的踢出
func TestKickUserWithMessage(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册客户端
	client := &Client{
		ID:       "kick-msg-client",
		UserID:   "kick-msg-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 带消息踢出
	err := hub.KickUserWithMessage("kick-msg-user", "违规", "您因违规被踢出")
	assert.NoError(t, err, "踢出应该成功")

	time.Sleep(100 * time.Millisecond)
}

// TestKickMultipleConnections 测试踢出多个连接
func TestKickMultipleConnections(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true // 允许多端登录
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册同一用户的多个连接
	userID := "multi-conn-user"
	for i := 1; i <= 3; i++ {
		client := &Client{
			ID:       "kick-multi-" + string(rune('0'+i)),
			UserID:   userID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
		}
		hub.Register(client)
	}
	time.Sleep(100 * time.Millisecond)

	// 踢出所有连接
	result := hub.KickUser(userID, "测试踢出多连接", false, "")

	assert.NotNil(t, result, "结果不应为空")
	assert.True(t, result.Success, "应该成功")
	assert.Equal(t, 3, result.KickedConnections, "应该踢出3个连接")
}

// TestKickNonExistentUser 测试踢出不存在的用户
func TestKickNonExistentUser(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 踢出不存在的用户
	result := hub.KickUser("non-existent-user", "test", false, "")

	assert.NotNil(t, result, "结果不应为空")
	assert.False(t, result.Success, "应该失败")
	assert.Equal(t, 0, result.KickedConnections, "不应该踢出任何连接")
	assert.Contains(t, result.Reason, "不在线", "原因应该包含'不在线'")
}

// TestForceOffline 测试强制下线（通过不允许多端登录）
func TestForceOffline(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = false // 不允许多端登录
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册第一个客户端
	client1 := &Client{
		ID:       "offline-client-1",
		UserID:   "offline-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client1)
	time.Sleep(100 * time.Millisecond) // 等待注册完成

	// 注册同一用户的第二个客户端（应该踢掉第一个）
	client2 := &Client{
		ID:       "offline-client-2",
		UserID:   "offline-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client2)
	time.Sleep(200 * time.Millisecond) // 增加等待时间，确保踢人操作完成

	// 验证只有第二个客户端在线
	assert.True(t, hub.HasClient("offline-client-2"), "第二个客户端应该在线")
	assert.False(t, hub.HasClient("offline-client-1"), "第一个客户端应该被踢下线")
}

// TestMaxConnectionsLimit 测试连接数限制
func TestMaxConnectionsLimit(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 2 // 限制每个用户最多2个连接
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "limit-user"

	// 注册3个连接
	for i := 1; i <= 3; i++ {
		client := &Client{
			ID:       "limit-client-" + string(rune('0'+i)),
			UserID:   userID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
		}
		hub.Register(client)
		time.Sleep(50 * time.Millisecond)
	}

	// 验证只有最新的2个连接在线
	time.Sleep(100 * time.Millisecond)
	clients := hub.GetClientsByUserID(userID)
	assert.LessOrEqual(t, len(clients), 2, "最多应该有2个连接")
}
