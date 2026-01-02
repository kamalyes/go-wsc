/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-27 22:53:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-30 00:23:53
 * @FilePath: \go-wsc\heartbeat_test.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/stretchr/testify/assert"
)

// MockHeartbeatTimeoutRecorder 记录心跳超时回调
type MockHeartbeatTimeoutRecorder struct {
	mu              sync.Mutex
	timeoutCalls    []HeartbeatTimeoutCall
	timeoutCallChan chan HeartbeatTimeoutCall // 用于等待回调
}

// HeartbeatTimeoutCall 记录超时回调的参数
type HeartbeatTimeoutCall struct {
	ClientID      string
	UserID        string
	LastHeartbeat time.Time
}

func NewMockHeartbeatTimeoutRecorder() *MockHeartbeatTimeoutRecorder {
	return &MockHeartbeatTimeoutRecorder{
		timeoutCalls:    make([]HeartbeatTimeoutCall, 0),
		timeoutCallChan: make(chan HeartbeatTimeoutCall, 10),
	}
}

// Record 记录一次超时回调
func (m *MockHeartbeatTimeoutRecorder) Record(clientID string, userID string, lastHeartbeat time.Time) {
	m.mu.Lock()
	call := HeartbeatTimeoutCall{
		ClientID:      clientID,
		UserID:        userID,
		LastHeartbeat: lastHeartbeat,
	}
	m.timeoutCalls = append(m.timeoutCalls, call)
	m.mu.Unlock()

	// 发送到通道，用于测试等待
	select {
	case m.timeoutCallChan <- call:
	default:
	}
}

func (m *MockHeartbeatTimeoutRecorder) GetTimeoutCalls() []HeartbeatTimeoutCall {
	return syncx.WithLockReturnValue(&m.mu, func() []HeartbeatTimeoutCall {
		return append([]HeartbeatTimeoutCall{}, m.timeoutCalls...)
	})
}

func (m *MockHeartbeatTimeoutRecorder) WaitForTimeout(timeout time.Duration) *HeartbeatTimeoutCall {
	select {
	case call := <-m.timeoutCallChan:
		return &call
	case <-time.After(timeout):
		return nil
	}
}

func (m *MockHeartbeatTimeoutRecorder) Reset() {
	syncx.WithLock(&m.mu, func() {
		m.timeoutCalls = make([]HeartbeatTimeoutCall, 0)
	})
}

// TestHeartbeatBasic 测试基本心跳功能
func TestHeartbeatBasic(t *testing.T) {
	// 创建配置
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8080).
		WithHeartbeatInterval(1 * time.Second).
		WithMessageBufferSize(100)

	// 创建Hub
	hub := NewHub(config)
	assert.NotNil(t, hub, "Hub should not be nil")

	// 创建记录器并注册回调
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
		recorder.Record(clientID, userID, lastHeartbeat)
	})

	// 设置心跳配置：检查间隔1秒，超时2秒
	hub.SetHeartbeatConfig(1*time.Second, 2*time.Second)

	// 启动Hub
	go hub.Run()
	defer hub.Shutdown()

	// 等待Hub启动
	time.Sleep(100 * time.Millisecond)

	// 创建测试客户端
	client := &Client{
		ID:       "client-001",
		UserID:   "user-001",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}

	// 注册客户端
	hub.Register(client)
	time.Sleep(100 * time.Millisecond)

	// 验证客户端已注册
	registeredClient := hub.GetClientByID(client.ID)
	assert.NotNil(t, registeredClient, "Client should be registered")
	assert.False(t, registeredClient.LastHeartbeat.IsZero(), "LastHeartbeat should be initialized")

	// 验证初始化的心跳时间是最近的
	timeSinceHeartbeat := time.Since(registeredClient.LastHeartbeat)
	assert.Less(t, timeSinceHeartbeat, 1*time.Second, "Initial heartbeat should be recent")
}

// TestHeartbeatUpdate 测试心跳更新功能
func TestHeartbeatUpdate(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8081).
		WithHeartbeatInterval(1 * time.Second).
		WithMessageBufferSize(100)

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
		recorder.Record(clientID, userID, lastHeartbeat)
	})
	hub.SetHeartbeatConfig(1*time.Second, 2*time.Second)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册客户端
	client := &Client{
		ID:       "client-002",
		UserID:   "user-002",
		UserType: UserTypeAgent,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client)
	time.Sleep(100 * time.Millisecond)

	// 获取初始心跳时间
	initialClient := hub.GetClientByID(client.ID)
	initialHeartbeat := initialClient.LastHeartbeat

	// 等待一段时间
	time.Sleep(500 * time.Millisecond)

	// 更新心跳
	hub.UpdateHeartbeat(client.ID)

	// 验证心跳时间已更新
	updatedClient := hub.GetClientByID(client.ID)
	updatedHeartbeat := updatedClient.LastHeartbeat

	assert.True(t, updatedHeartbeat.After(initialHeartbeat),
		"Updated heartbeat should be after initial heartbeat")

	// 验证更新后的心跳时间是最近的
	timeSinceUpdate := time.Since(updatedHeartbeat)
	assert.Less(t, timeSinceUpdate, 100*time.Millisecond,
		"Updated heartbeat should be very recent")
}

// TestHeartbeatTimeout 测试心跳超时检测
func TestHeartbeatTimeout(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8082).
		WithHeartbeatInterval(1 * time.Second).
		WithMessageBufferSize(100)

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
		recorder.Record(clientID, userID, lastHeartbeat)
	})

	// 设置较短的超时时间便于测试：检查间隔1秒，超时1.5秒
	hub.SetHeartbeatConfig(1*time.Second, 1500*time.Millisecond)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册客户端
	client := &Client{
		ID:       "client-003",
		UserID:   "user-003",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client)
	time.Sleep(100 * time.Millisecond)

	// 手动设置一个过期的心跳时间（3秒前）
	hub.SetClientLastHeartbeatForTest(client.ID, time.Now().Add(-3*time.Second))

	// 等待心跳检查触发（最多2秒）
	timeoutCall := recorder.WaitForTimeout(2 * time.Second)

	// 验证超时回调被触发
	assert.NotNil(t, timeoutCall, "Timeout callback should be called")
	if timeoutCall != nil {
		assert.Equal(t, client.ID, timeoutCall.ClientID, "Client ID should match")
		assert.Equal(t, client.UserID, timeoutCall.UserID, "User ID should match")
	}

	// 验证客户端已被移除（使用公共 API 避免数据竞争）
	time.Sleep(200 * time.Millisecond)
	exists := hub.HasClient(client.ID)
	assert.False(t, exists, "Client should be removed after timeout")
}

// TestHeartbeatNoTimeout 测试正常心跳不会超时
func TestHeartbeatNoTimeout(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8083).
		WithHeartbeatInterval(1 * time.Second).
		WithMessageBufferSize(100)

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
		recorder.Record(clientID, userID, lastHeartbeat)
	})

	// 设置超时时间：2秒
	hub.SetHeartbeatConfig(1*time.Second, 2*time.Second)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册客户端
	client := &Client{
		ID:       "client-004",
		UserID:   "user-004",
		UserType: UserTypeAgent,
		SendChan: make(chan []byte, 10),
	}
	clientID := client.ID // 保存clientID避免竞争
	hub.Register(client)
	time.Sleep(100 * time.Millisecond)

	// 定期更新心跳（每500毫秒一次）
	done := make(chan bool)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				hub.UpdateHeartbeat(clientID)
			case <-done:
				return
			}
		}
	}()

	// 等待3秒，超过超时时间
	time.Sleep(3 * time.Second)
	close(done)

	// 验证没有触发超时回调
	calls := recorder.GetTimeoutCalls()
	assert.Equal(t, 0, len(calls), "Should not trigger timeout with regular heartbeats")

	// 验证客户端仍然存在
	client = hub.GetClientByID(clientID)
	assert.NotNil(t, client, "Client should still exist with regular heartbeats")
}

// TestMultipleClientsHeartbeat 测试多个客户端的心跳管理
func TestMultipleClientsHeartbeat(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8084).
		WithHeartbeatInterval(1 * time.Second).
		WithMessageBufferSize(100)

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
		recorder.Record(clientID, userID, lastHeartbeat)
	})
	hub.SetHeartbeatConfig(1*time.Second, 1500*time.Millisecond)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册3个客户端
	clients := []*Client{
		{
			ID:       "client-005",
			UserID:   "user-005",
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 10),
		},
		{
			ID:       "client-006",
			UserID:   "user-006",
			UserType: UserTypeAgent,
			SendChan: make(chan []byte, 10),
		},
		{
			ID:       "client-007",
			UserID:   "user-007",
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 10),
		},
	}

	for _, client := range clients {
		hub.Register(client)
	}
	time.Sleep(200 * time.Millisecond)

	// 第一个和第三个客户端保持心跳，第二个客户端超时
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; i < 6; i++ {
			<-ticker.C
			hub.UpdateHeartbeat(clients[0].ID)
			hub.UpdateHeartbeat(clients[2].ID)
		}
	}()

	// 让第二个客户端的心跳过期
	hub.SetClientLastHeartbeatForTest(clients[1].ID, time.Now().Add(-3*time.Second))

	// 等待超时检测
	time.Sleep(2 * time.Second)

	// 验证只有第二个客户端超时
	calls := recorder.GetTimeoutCalls()
	assert.Equal(t, 1, len(calls), "Should have exactly one timeout")
	if len(calls) > 0 {
		assert.Equal(t, clients[1].ID, calls[0].ClientID, "Client 006 should timeout")
	}

	// 验证客户端状态
	client1 := hub.GetClientByID(clients[0].ID)
	client2 := hub.GetClientByID(clients[1].ID)
	client3 := hub.GetClientByID(clients[2].ID)

	assert.NotNil(t, client1, "Client 005 should still exist")
	assert.Nil(t, client2, "Client 006 should be removed")
	assert.NotNil(t, client3, "Client 007 should still exist")
}

// TestHeartbeatWithoutHandler 测试没有设置处理器的情况
func TestHeartbeatWithoutHandler(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8085).
		WithHeartbeatInterval(1 * time.Second).
		WithMessageBufferSize(100)

	hub := NewHub(config)
	// 不设置心跳处理器
	hub.SetHeartbeatConfig(1*time.Second, 1*time.Second)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册客户端
	client := &Client{
		ID:       "client-008",
		UserID:   "user-008",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client)
	time.Sleep(100 * time.Millisecond)

	// 设置过期心跳
	hub.SetClientLastHeartbeatForTest(client.ID, time.Now().Add(-2*time.Second))

	// 等待检查
	time.Sleep(1500 * time.Millisecond)

	// 即使没有处理器，客户端也应该被移除
	client = hub.GetClientByID(client.ID)
	assert.Nil(t, client, "Client should be removed even without timeout handler")
}

// TestHeartbeatConfigDefaults 测试默认配置
func TestHeartbeatConfigDefaults(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8086).
		WithHeartbeatInterval(1 * time.Second). // 使用1秒的检查间隔方便测试
		WithClientTimeout(3 * time.Second).     // 使用3秒的超时时间
		WithMessageBufferSize(100)

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
		recorder.Record(clientID, userID, lastHeartbeat)
	})
	// 不调用 SetHeartbeatConfig，使用默认值

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 验证使用配置中的默认超时时间
	assert.Equal(t, 1*time.Second, hub.GetConfig().HeartbeatInterval,
		"Should use config value when not explicitly set")

	client := &Client{
		ID:       "client-009",
		UserID:   "user-009",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client)
	time.Sleep(100 * time.Millisecond) // 等待注册完成

	// 测试1: 设置心跳为1.5秒前（小于3秒超时），等待1秒检查，总共2.5秒，应该不超时
	hub.SetClientLastHeartbeatForTest(client.ID, time.Now().Add(-1500*time.Millisecond))
	time.Sleep(1100 * time.Millisecond) // 等待心跳检查触发（心跳间隔1秒）

	clientCheck := hub.GetClientByID(client.ID)
	assert.NotNil(t, clientCheck, "Client should not timeout (1.5s + 1.1s = 2.6s < 3s)")

	// 测试2: 设置心跳为2.5秒前，等待1秒检查，总共3.5秒，应该超时
	hub.SetClientLastHeartbeatForTest(client.ID, time.Now().Add(-2500*time.Millisecond))
	time.Sleep(1100 * time.Millisecond) // 等待心跳检查触发

	clientCheck = hub.GetClientByID(client.ID)
	assert.Nil(t, clientCheck, "Client should timeout (2.5s + 1.1s = 3.6s > 3s)")

}

// BenchmarkHeartbeatUpdate 性能测试：心跳更新
func BenchmarkHeartbeatUpdate(b *testing.B) {
	config := wscconfig.Default()
	config.NodeIP = "127.0.0.1"
	config.NodePort = 8087
	config.HeartbeatInterval = 30 * time.Second
	config.MessageBufferSize = 100

	hub := NewHub(config)
	hub.SetHeartbeatConfig(30*time.Second, 90*time.Second)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册客户端
	client := &Client{
		ID:       "bench-client",
		UserID:   "bench-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.UpdateHeartbeat(client.ID)
	}
}

// BenchmarkHeartbeatCheck 性能测试：心跳检查
func BenchmarkHeartbeatCheck(b *testing.B) {
	config := wscconfig.Default()
	config.NodeIP = "127.0.0.1"
	config.NodePort = 8088
	config.HeartbeatInterval = 30 * time.Second
	config.MessageBufferSize = 100

	hub := NewHub(config)
	hub.SetHeartbeatConfig(30*time.Second, 90*time.Second)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册1000个客户端
	for i := 0; i < 1000; i++ {
		client := &Client{
			ID:       "bench-client-" + string(rune(i)),
			UserID:   "bench-user-" + string(rune(i)),
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 10),
		}
		hub.Register(client)
	}
	time.Sleep(500 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 基准测试：更新所有客户端的心跳
		for j := 0; j < 100; j++ {
			hub.UpdateHeartbeat("bench-client-" + string(rune(j)))
		}
	}
}
