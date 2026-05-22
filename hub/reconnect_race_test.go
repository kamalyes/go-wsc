/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-22 00:00:00
 * @FilePath: \go-wsc\hub\reconnect_race_test.go
 * @Description: 断线重连竞态条件测试
 *
 * 测试场景：
 * - TemporalHasher 在时间窗口内为相同用户+设备生成相同 ClientID
 * - 断线重连时新客户端覆盖旧客户端的 map 条目
 * - 旧客户端的读协程退出时调用 Unregister 不应误删新客户端
 * - 重连后心跳 pong 响应应正常工作
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// TestReconnectSameClientID_OldUnregisterDoesNotDeleteNew 测试断线重连时旧客户端的 Unregister 不会误删新客户端
// 这是核心修复的测试：removeClientUnsafe 中的指针验证
func TestReconnectSameClientID_OldUnregisterDoesNotDeleteNew(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	userID := "reconnect-user-001"
	clientID := "same-client-id" // 模拟 TemporalHasher 生成相同 ClientID

	// 1. 注册旧客户端
	oldClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(oldClient)
	time.Sleep(50 * time.Millisecond)

	assert.True(t, hub.HasClient(clientID), "旧客户端应该在线")

	// 2. 模拟断线重连：注册新客户端（相同 ClientID）
	newClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(newClient)
	time.Sleep(50 * time.Millisecond)

	// 3. 验证新客户端在线（map 中是新客户端对象）
	assert.True(t, hub.HasClient(clientID), "新客户端应该在线")

	// 验证 map 中存储的是新客户端指针
	existingClient, exists := hub.GetClientByIDWithLock(clientID)
	assert.True(t, exists, "客户端应该存在")
	assert.Equal(t, newClient, existingClient, "map 中应该是新客户端对象")

	// 4. 旧客户端的读协程退出，调用 Unregister
	hub.Unregister(oldClient)
	time.Sleep(50 * time.Millisecond)

	// 5. 关键断言：新客户端不应该被误删
	assert.True(t, hub.HasClient(clientID), "新客户端不应该被旧客户端的 Unregister 误删")

	// 验证 map 中仍然是新客户端
	existingClient2, exists2 := hub.GetClientByIDWithLock(clientID)
	assert.True(t, exists2, "新客户端应该仍然存在")
	assert.Equal(t, newClient, existingClient2, "map 中应该仍然是新客户端对象")
}

// TestReconnectSameClientID_HeartbeatWorksAfterReconnect 测试断线重连后心跳 pong 响应正常
func TestReconnectSameClientID_HeartbeatWorksAfterReconnect(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	userID := "reconnect-heartbeat-user"
	clientID := "same-client-id-heartbeat"

	// 1. 注册旧客户端
	oldClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(oldClient)
	time.Sleep(50 * time.Millisecond)

	// 2. 注册新客户端（相同 ClientID，模拟重连）
	newClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(newClient)
	time.Sleep(50 * time.Millisecond)

	// 3. 旧客户端 Unregister（模拟旧读协程退出）
	hub.Unregister(oldClient)
	time.Sleep(50 * time.Millisecond)

	// 4. 新客户端发送心跳，应该能正常收到 pong
	hub.handleHeartbeatMessage(newClient)
	time.Sleep(50 * time.Millisecond)

	// 5. 验证新客户端的 SendChan 收到了 pong 响应
	select {
	case data := <-newClient.SendChan:
		assert.NotEmpty(t, data, "新客户端应该收到 pong 响应")
		assert.Contains(t, string(data), "pong", "应该是 pong 消息")
	case <-time.After(1 * time.Second):
		t.Fatal("新客户端未收到心跳 pong 响应")
	}
}

// TestReconnectSameClientID_OldClientAlreadyClosed 测试旧客户端已关闭时心跳被正确忽略
func TestReconnectSameClientID_OldClientAlreadyClosed(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	userID := "reconnect-closed-user"
	clientID := "same-client-id-closed"

	// 1. 注册旧客户端
	oldClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(oldClient)
	time.Sleep(50 * time.Millisecond)

	// 2. 标记旧客户端为已关闭（模拟旧连接断开）
	oldClient.MarkClosed()

	// 3. 旧客户端的心跳消息应该被忽略
	hub.handleHeartbeatMessage(oldClient)
	// 不应该 panic，也不应该发送 pong

	// 4. 注册新客户端
	newClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(newClient)
	time.Sleep(50 * time.Millisecond)

	// 5. 新客户端心跳正常
	hub.handleHeartbeatMessage(newClient)

	select {
	case data := <-newClient.SendChan:
		assert.Contains(t, string(data), "pong", "新客户端应该收到 pong")
	case <-time.After(1 * time.Second):
		t.Fatal("新客户端未收到 pong 响应")
	}
}

// TestReconnectConcurrentUnregisterAndRegister 测试并发场景下重连不会导致数据竞争
func TestReconnectConcurrentUnregisterAndRegister(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	const iterations = 50

	for i := 0; i < iterations; i++ {
		userID := "concurrent-reconnect-user"
		clientID := "concurrent-same-client-id"

		// 注册旧客户端
		oldClient := &Client{
			ID:            clientID,
			UserID:        userID,
			UserType:      UserTypeAgent,
			Status:        UserStatusOnline,
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			SendChan:      make(chan []byte, 10),
			Context:       context.Background(),
		}
		hub.Register(oldClient)
		time.Sleep(10 * time.Millisecond)

		// 注册新客户端（相同 ClientID）
		newClient := &Client{
			ID:            clientID,
			UserID:        userID,
			UserType:      UserTypeAgent,
			Status:        UserStatusOnline,
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			SendChan:      make(chan []byte, 10),
			Context:       context.Background(),
		}
		hub.Register(newClient)
		time.Sleep(10 * time.Millisecond)

		var wg sync.WaitGroup
		wg.Add(2)

		// 并发：旧客户端 Unregister
		go func() {
			defer wg.Done()
			hub.Unregister(oldClient)
		}()

		// 并发：新客户端发送心跳
		go func() {
			defer wg.Done()
			hub.handleHeartbeatMessage(newClient)
		}()

		wg.Wait()

		// 新客户端应该仍然在线
		assert.True(t, hub.HasClient(clientID), "迭代 %d: 新客户端应该在线", i)

		// 清理
		hub.Unregister(newClient)
		time.Sleep(10 * time.Millisecond)
	}
}

// TestReconnectOldClientCleanup 测试重连时旧客户端的通道和连接被正确清理
func TestReconnectOldClientCleanup(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	userID := "reconnect-cleanup-user"
	clientID := "same-client-id-cleanup"

	// 1. 注册旧客户端
	oldClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(oldClient)
	time.Sleep(50 * time.Millisecond)

	assert.False(t, oldClient.IsClosed(), "旧客户端初始应该未关闭")

	// 2. 注册新客户端（相同 ClientID，触发旧客户端清理）
	newClient := &Client{
		ID:            clientID,
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(newClient)
	time.Sleep(50 * time.Millisecond)

	// 3. 验证旧客户端已被标记为关闭
	assert.True(t, oldClient.IsClosed(), "旧客户端应该已被标记为关闭")

	// 4. 验证新客户端未被关闭
	assert.False(t, newClient.IsClosed(), "新客户端不应该被关闭")

	// 5. 验证新客户端在线
	assert.True(t, hub.HasClient(clientID), "新客户端应该在线")
}

// TestReconnectDifferentClientID_NormalFlow 测试不同 ClientID 的重连（正常多端登录场景）
func TestReconnectDifferentClientID_NormalFlow(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	userID := "normal-reconnect-user"

	// 1. 注册旧客户端（不同 ClientID）
	oldClient := &Client{
		ID:            "old-client-id",
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(oldClient)
	time.Sleep(50 * time.Millisecond)

	// 2. 注册新客户端（不同 ClientID，不同设备）
	newClient := &Client{
		ID:            "new-client-id",
		UserID:        userID,
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(newClient)
	time.Sleep(50 * time.Millisecond)

	// 3. 两个客户端都应该在线
	assert.True(t, hub.HasClient("old-client-id"), "旧客户端应该在线")
	assert.True(t, hub.HasClient("new-client-id"), "新客户端应该在线")

	// 4. 旧客户端 Unregister 不影响新客户端
	hub.Unregister(oldClient)
	time.Sleep(50 * time.Millisecond)

	assert.False(t, hub.HasClient("old-client-id"), "旧客户端应该已下线")
	assert.True(t, hub.HasClient("new-client-id"), "新客户端应该仍然在线")
}

// TestSendPongResponseRetryOnFullChannel 测试 SendPongResponse 在通道满时重试
func TestSendPongResponseRetryOnFullChannel(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	// 创建一个 SendChan 容量很小的客户端
	client := &Client{
		ID:            "full-channel-client",
		UserID:        "full-channel-user",
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 1), // 容量为1
		Context:       context.Background(),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 填满 SendChan
	client.SendChan <- []byte("filler-message")

	// 在另一个 goroutine 中消费消息，让重试有机会成功
	go func() {
		time.Sleep(100 * time.Millisecond)
		<-client.SendChan
	}()

	// 发送 pong，应该通过重试机制成功
	err := hub.SendPongResponse(client)
	assert.NoError(t, err, "SendPongResponse 应该通过重试成功发送")
}

// TestSendPongResponseClosedClient 测试 SendPongResponse 对已关闭客户端返回错误
func TestSendPongResponseClosedClient(t *testing.T) {
	config := wscconfig.Default()

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "closed-pong-client",
		UserID:        "closed-pong-user",
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 关闭客户端
	client.CloseMu.Lock()
	client.MarkClosed()
	close(client.SendChan)
	client.CloseMu.Unlock()

	// 发送 pong 应该返回错误
	err := hub.SendPongResponse(client)
	assert.Error(t, err, "向已关闭客户端发送 pong 应该返回错误")
}
