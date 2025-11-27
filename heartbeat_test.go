package wsc

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"sync"
	"testing"
	"time"
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]HeartbeatTimeoutCall{}, m.timeoutCalls...)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeoutCalls = make([]HeartbeatTimeoutCall, 0)
}

// TestHeartbeatBasic 测试基本心跳功能
func TestHeartbeatBasic(t *testing.T) {
	// 创建配置
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8080,
		HeartbeatInterval: 1, // 1秒检查一次
		MessageBufferSize: 100,
	}

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
	hub.mutex.RLock()
	registeredClient, exists := hub.clients[client.ID]
	hub.mutex.RUnlock()
	assert.True(t, exists, "Client should be registered")
	assert.NotNil(t, registeredClient, "Registered client should not be nil")
	assert.False(t, registeredClient.LastHeartbeat.IsZero(), "LastHeartbeat should be initialized")

	// 验证初始化的心跳时间是最近的
	timeSinceHeartbeat := time.Since(registeredClient.LastHeartbeat)
	assert.Less(t, timeSinceHeartbeat, 1*time.Second, "Initial heartbeat should be recent")
}

// TestHeartbeatUpdate 测试心跳更新功能
func TestHeartbeatUpdate(t *testing.T) {
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8081,
		HeartbeatInterval: 1,
		MessageBufferSize: 100,
	}

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) { recorder.Record(clientID, userID, lastHeartbeat) })
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
	hub.mutex.RLock()
	initialHeartbeat := hub.clients[client.ID].LastHeartbeat
	hub.mutex.RUnlock()

	// 等待一段时间
	time.Sleep(500 * time.Millisecond)

	// 更新心跳
	hub.UpdateHeartbeat(client.ID)

	// 验证心跳时间已更新
	hub.mutex.RLock()
	updatedHeartbeat := hub.clients[client.ID].LastHeartbeat
	hub.mutex.RUnlock()

	assert.True(t, updatedHeartbeat.After(initialHeartbeat),
		"Updated heartbeat should be after initial heartbeat")

	// 验证更新后的心跳时间是最近的
	timeSinceUpdate := time.Since(updatedHeartbeat)
	assert.Less(t, timeSinceUpdate, 100*time.Millisecond,
		"Updated heartbeat should be very recent")
}

// TestHeartbeatTimeout 测试心跳超时检测
func TestHeartbeatTimeout(t *testing.T) {
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8082,
		HeartbeatInterval: 1,
		MessageBufferSize: 100,
	}

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) { recorder.Record(clientID, userID, lastHeartbeat) })

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
	hub.mutex.Lock()
	hub.clients[client.ID].LastHeartbeat = time.Now().Add(-3 * time.Second)
	hub.mutex.Unlock()

	// 等待心跳检查触发（最多2秒）
	timeoutCall := recorder.WaitForTimeout(2 * time.Second)

	// 验证超时回调被触发
	assert.NotNil(t, timeoutCall, "Timeout callback should be called")
	if timeoutCall != nil {
		assert.Equal(t, client.ID, timeoutCall.ClientID, "Client ID should match")
		assert.Equal(t, client.UserID, timeoutCall.UserID, "User ID should match")
	}

	// 验证客户端已被移除
	time.Sleep(200 * time.Millisecond)
	hub.mutex.RLock()
	_, exists := hub.clients[client.ID]
	hub.mutex.RUnlock()
	assert.False(t, exists, "Client should be removed after timeout")
}

// TestHeartbeatNoTimeout 测试正常心跳不会超时
func TestHeartbeatNoTimeout(t *testing.T) {
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8083,
		HeartbeatInterval: 1,
		MessageBufferSize: 100,
	}

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) { recorder.Record(clientID, userID, lastHeartbeat) })

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
				hub.UpdateHeartbeat(client.ID)
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
	hub.mutex.RLock()
	_, exists := hub.clients[client.ID]
	hub.mutex.RUnlock()
	assert.True(t, exists, "Client should still exist with regular heartbeats")
}

// TestMultipleClientsHeartbeat 测试多个客户端的心跳管理
func TestMultipleClientsHeartbeat(t *testing.T) {
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8084,
		HeartbeatInterval: 1,
		MessageBufferSize: 100,
	}

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) { recorder.Record(clientID, userID, lastHeartbeat) })
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
	hub.mutex.Lock()
	hub.clients[clients[1].ID].LastHeartbeat = time.Now().Add(-3 * time.Second)
	hub.mutex.Unlock()

	// 等待超时检测
	time.Sleep(2 * time.Second)

	// 验证只有第二个客户端超时
	calls := recorder.GetTimeoutCalls()
	assert.Equal(t, 1, len(calls), "Should have exactly one timeout")
	if len(calls) > 0 {
		assert.Equal(t, clients[1].ID, calls[0].ClientID, "Client 006 should timeout")
	}

	// 验证客户端状态
	hub.mutex.RLock()
	_, exists1 := hub.clients[clients[0].ID]
	_, exists2 := hub.clients[clients[1].ID]
	_, exists3 := hub.clients[clients[2].ID]
	hub.mutex.RUnlock()

	assert.True(t, exists1, "Client 005 should still exist")
	assert.False(t, exists2, "Client 006 should be removed")
	assert.True(t, exists3, "Client 007 should still exist")
}

// TestHeartbeatWithoutHandler 测试没有设置处理器的情况
func TestHeartbeatWithoutHandler(t *testing.T) {
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8085,
		HeartbeatInterval: 1,
		MessageBufferSize: 100,
	}

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
	hub.mutex.Lock()
	hub.clients[client.ID].LastHeartbeat = time.Now().Add(-2 * time.Second)
	hub.mutex.Unlock()

	// 等待检查
	time.Sleep(1500 * time.Millisecond)

	// 即使没有处理器，客户端也应该被移除
	hub.mutex.RLock()
	_, exists := hub.clients[client.ID]
	hub.mutex.RUnlock()
	assert.False(t, exists, "Client should be removed even without handler")
}

// TestHeartbeatConfigDefaults 测试默认配置
func TestHeartbeatConfigDefaults(t *testing.T) {
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8086,
		HeartbeatInterval: 30,
		ClientTimeout:     90, // 默认90秒
		MessageBufferSize: 100,
	}

	hub := NewHub(config)
	recorder := NewMockHeartbeatTimeoutRecorder()
	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) { recorder.Record(clientID, userID, lastHeartbeat) })
	// 不调用 SetHeartbeatConfig，使用默认值

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 验证使用配置中的默认超时时间
	assert.Equal(t, time.Duration(0), hub.heartbeatTimeout,
		"Should use zero when not configured, falling back to config")

	client := &Client{
		ID:       "client-009",
		UserID:   "user-009",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client)
	time.Sleep(100 * time.Millisecond)

	// 设置一个刚好不超时的心跳（89秒前）
	hub.mutex.Lock()
	hub.clients[client.ID].LastHeartbeat = time.Now().Add(-89 * time.Second)
	hub.mutex.Unlock()

	// 手动触发一次心跳检查
	hub.checkHeartbeat()

	// 应该还存在
	hub.mutex.RLock()
	_, exists := hub.clients[client.ID]
	hub.mutex.RUnlock()
	assert.True(t, exists, "Client should not timeout at 89 seconds")

	// 设置超时的心跳（91秒前）
	hub.mutex.Lock()
	hub.clients[client.ID].LastHeartbeat = time.Now().Add(-91 * time.Second)
	hub.mutex.Unlock()

	// 再次检查
	hub.checkHeartbeat()

	// 应该被移除
	time.Sleep(100 * time.Millisecond)
	hub.mutex.RLock()
	_, exists = hub.clients[client.ID]
	hub.mutex.RUnlock()
	assert.False(t, exists, "Client should timeout at 91 seconds")
}

// BenchmarkHeartbeatUpdate 性能测试：心跳更新
func BenchmarkHeartbeatUpdate(b *testing.B) {
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8087,
		HeartbeatInterval: 30,
		MessageBufferSize: 100,
	}

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
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8088,
		HeartbeatInterval: 30,
		MessageBufferSize: 100,
	}

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
		hub.checkHeartbeat()
	}
}
