/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:30:00
 * @FilePath: \go-wsc\hub_events_test.go
 * @Description: Hub Events 模块测试 - 测试事件发布订阅功能
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 测试辅助结构
// ============================================================================

// eventRecorder 事件记录器
type eventRecorder struct {
	userOnlineEvents  []UserStatusEvent
	userOfflineEvents []UserStatusEvent
	ticketEvents      []TicketQueueEvent
	mu                sync.RWMutex
	onlineCount       atomic.Int64
	offlineCount      atomic.Int64
	ticketCount       atomic.Int64
}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{
		userOnlineEvents:  make([]UserStatusEvent, 0),
		userOfflineEvents: make([]UserStatusEvent, 0),
		ticketEvents:      make([]TicketQueueEvent, 0),
	}
}

func (r *eventRecorder) recordOnline(event *UserStatusEvent) {
	syncx.WithLock(&r.mu, func() {
		r.userOnlineEvents = append(r.userOnlineEvents, *event)
	})
	r.onlineCount.Add(1)
}

func (r *eventRecorder) recordOffline(event *UserStatusEvent) {
	syncx.WithLock(&r.mu, func() {
		r.userOfflineEvents = append(r.userOfflineEvents, *event)
	})
	r.offlineCount.Add(1)
}

func (r *eventRecorder) recordTicket(event *TicketQueueEvent) {
	syncx.WithLock(&r.mu, func() {
		r.ticketEvents = append(r.ticketEvents, *event)
	})
	r.ticketCount.Add(1)
}

func (r *eventRecorder) getOnlineCount() int64 {
	return r.onlineCount.Load()
}

func (r *eventRecorder) getOfflineCount() int64 {
	return r.offlineCount.Load()
}

func (r *eventRecorder) getTicketCount() int64 {
	return r.ticketCount.Load()
}

func (r *eventRecorder) getOnlineEvents() []UserStatusEvent {
	return syncx.WithRLockReturnValue(&r.mu, func() []UserStatusEvent {
		events := make([]UserStatusEvent, len(r.userOnlineEvents))
		copy(events, r.userOnlineEvents)
		return events
	})
}

func (r *eventRecorder) getOfflineEvents() []UserStatusEvent {
	return syncx.WithRLockReturnValue(&r.mu, func() []UserStatusEvent {
		events := make([]UserStatusEvent, len(r.userOfflineEvents))
		copy(events, r.userOfflineEvents)
		return events
	})
}

func (r *eventRecorder) getTicketEvents() []TicketQueueEvent {
	return syncx.WithRLockReturnValue(&r.mu, func() []TicketQueueEvent {
		events := make([]TicketQueueEvent, len(r.ticketEvents))
		copy(events, r.ticketEvents)
		return events
	})
}

func (r *eventRecorder) reset() {
	syncx.WithLock(&r.mu, func() {
		r.userOnlineEvents = make([]UserStatusEvent, 0)
		r.userOfflineEvents = make([]UserStatusEvent, 0)
		r.ticketEvents = make([]TicketQueueEvent, 0)
	})
	r.onlineCount.Store(0)
	r.offlineCount.Store(0)
	r.ticketCount.Store(0)
}

// ============================================================================
// 用户上下线事件测试
// ============================================================================

// TestUserOnlineEvent 测试用户上线事件发布和订阅
func TestUserOnlineEvent(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	// 设置 PubSub
	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{Namespace: "wsc"})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	recorder := newEventRecorder()

	// 订阅用户上线事件
	unsubscribe, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		recorder.recordOnline(event)
		return nil
	})
	require.NoError(t, err)
	defer func() {
		if unsubscribe != nil {
			_ = unsubscribe()
		}
	}()

	// 等待订阅就绪
	time.Sleep(100 * time.Millisecond)

	// 模拟客户端上线
	client := CreateTestClient("client-001", "user-001", UserTypeCustomer)
	hub.Register(client)

	// 等待事件处理
	time.Sleep(300 * time.Millisecond)

	// 验证事件
	assert.Equal(t, int64(1), recorder.getOnlineCount(), "应该收到1个上线事件")

	events := recorder.getOnlineEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "user-001", events[0].UserID)
	assert.Equal(t, UserTypeCustomer, events[0].UserType)
	assert.Equal(t, EventTypeOnline, events[0].EventType)
	assert.Equal(t, hub.GetNodeID(), events[0].NodeID)

	hub.Unregister(client)
}

// TestUserOfflineEvent 测试用户下线事件发布和订阅
func TestUserOfflineEvent(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	// 设置 PubSub
	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	recorder := newEventRecorder()

	// 订阅用户下线事件
	unsubscribe, err := hub.SubscribeUserOffline(func(event *UserStatusEvent) error {
		recorder.recordOffline(event)
		return nil
	})
	require.NoError(t, err)
	defer func() {
		if unsubscribe != nil {
			_ = unsubscribe()
		}
	}()

	// 等待订阅就绪
	time.Sleep(100 * time.Millisecond)

	// 模拟客户端上线再下线
	client := CreateTestClient("client-002", "user-002", UserTypeAgent)
	hub.Register(client)
	time.Sleep(200 * time.Millisecond)

	hub.Unregister(client)
	time.Sleep(300 * time.Millisecond)

	// 验证事件
	assert.Equal(t, int64(1), recorder.getOfflineCount(), "应该收到1个下线事件")

	events := recorder.getOfflineEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "user-002", events[0].UserID)
	assert.Equal(t, UserTypeAgent, events[0].UserType)
	assert.Equal(t, EventTypeOffline, events[0].EventType)
	assert.Equal(t, hub.GetNodeID(), events[0].NodeID)
}

// TestMultipleSubscribers 测试多个订阅者
func TestMultipleSubscribers(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	// 设置 PubSub
	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	recorder1 := newEventRecorder()
	recorder2 := newEventRecorder()

	// 订阅者1
	unsub1, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		recorder1.recordOnline(event)
		return nil
	})
	require.NoError(t, err)
	defer func() {
		if unsub1 != nil {
			_ = unsub1()
		}
	}()

	// 订阅者2
	unsub2, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		recorder2.recordOnline(event)
		return nil
	})
	require.NoError(t, err)
	defer func() {
		if unsub2 != nil {
			_ = unsub2()
		}
	}()

	// 等待订阅就绪
	time.Sleep(100 * time.Millisecond)

	// 触发事件
	client := CreateTestClient("client-003", "user-003", UserTypeCustomer)
	hub.Register(client)

	// 等待事件处理
	time.Sleep(300 * time.Millisecond)

	// 两个订阅者都应该收到事件
	assert.Equal(t, int64(1), recorder1.getOnlineCount(), "订阅者1应该收到1个事件")
	assert.Equal(t, int64(1), recorder2.getOnlineCount(), "订阅者2应该收到1个事件")

	hub.Unregister(client)
}

// ============================================================================
// 工单入队事件测试
// ============================================================================

// TestTicketQueuePushedEvent 测试工单入队事件
func TestTicketQueuePushedEvent(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	// 设置 PubSub
	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	recorder := newEventRecorder()

	// 订阅工单入队事件
	unsubscribe, err := hub.SubscribeTicketQueuePushed(func(event *TicketQueueEvent) error {
		recorder.recordTicket(event)
		return nil
	})
	require.NoError(t, err)
	defer func() {
		if unsubscribe != nil {
			_ = unsubscribe()
		}
	}()

	// 等待订阅就绪
	time.Sleep(100 * time.Millisecond)

	// 发布工单入队事件
	hub.PublishTicketQueuePushed("ticket-001", "user-001", "session-001", 5)

	// 等待事件处理
	time.Sleep(300 * time.Millisecond)

	// 验证事件
	assert.Equal(t, int64(1), recorder.getTicketCount(), "应该收到1个工单事件")

	events := recorder.getTicketEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "ticket-001", events[0].TicketID)
	assert.Equal(t, "user-001", events[0].UserID)
	assert.Equal(t, "session-001", events[0].SessionID)
	assert.Equal(t, 5, events[0].Priority)
	assert.Equal(t, hub.GetNodeID(), events[0].NodeID)
}

// ============================================================================
// 取消订阅测试
// ============================================================================

// TestUnsubscribe 测试取消订阅
func TestUnsubscribe(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	// 设置 PubSub
	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	recorder := newEventRecorder()

	// 订阅事件
	unsubscribe, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		recorder.recordOnline(event)
		return nil
	})
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// 触发第一个事件
	client1 := CreateTestClient("client-004", "user-004", UserTypeCustomer)
	hub.Register(client1)
	time.Sleep(300 * time.Millisecond)

	assert.Equal(t, int64(1), recorder.getOnlineCount(), "取消订阅前应该收到1个事件")

	// 取消订阅
	err = unsubscribe()
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// 再次触发事件
	client2 := CreateTestClient("client-005", "user-005", UserTypeCustomer)
	hub.Register(client2)
	time.Sleep(300 * time.Millisecond)

	// 应该还是只有1个事件（取消订阅后不再接收）
	assert.Equal(t, int64(1), recorder.getOnlineCount(), "取消订阅后不应该收到新事件")

	hub.Unregister(client1)
	hub.Unregister(client2)
}

// ============================================================================
// PubSub 未设置测试
// ============================================================================

// TestEventsWithoutPubSub 测试未设置 PubSub 时的行为
func TestEventsWithoutPubSub(t *testing.T) {
	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	StartTestHub(t, hub)

	// 不设置 PubSub，直接订阅应该返回错误
	_, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		return nil
	})
	assert.Error(t, err, "未设置 PubSub 应该返回错误")
	assert.Equal(t, ErrPubSubNotSet, err)

	// 发布事件不应该panic（应该静默处理）
	client := CreateTestClient("client-006", "user-006", UserTypeCustomer)
	assert.NotPanics(t, func() {
		hub.Register(client)
		time.Sleep(100 * time.Millisecond)
		hub.Unregister(client)
	}, "未设置 PubSub 时发布事件不应该 panic")
}

// ============================================================================
// 跨节点事件测试
// ============================================================================

// TestCrossNodeEvents 测试跨节点事件通信
func TestCrossNodeEvents(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	// 创建两个 Hub 实例模拟不同节点
	config1 := wscconfig.Default()
	hub1 := CreateTestHub(t, config1)
	defer hub1.Shutdown()

	config2 := wscconfig.Default()
	hub2 := CreateTestHub(t, config2)
	defer hub2.Shutdown()

	// 为两个 Hub 设置相同的 PubSub（模拟共享 Redis）
	pubsubConfig := cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	}
	pubsub1 := cachex.NewPubSub(redisClient, pubsubConfig)
	pubsub2 := cachex.NewPubSub(redisClient, pubsubConfig)

	hub1.SetPubSub(pubsub1)
	hub2.SetPubSub(pubsub2)

	StartTestHub(t, hub1)
	StartTestHub(t, hub2)

	recorder := newEventRecorder()

	// Hub2 订阅事件
	unsubscribe, err := hub2.SubscribeUserOnline(func(event *UserStatusEvent) error {
		recorder.recordOnline(event)
		return nil
	})
	require.NoError(t, err)
	defer func() {
		if unsubscribe != nil {
			_ = unsubscribe()
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Hub1 发布事件（客户端上线）
	client := CreateTestClient("client-007", "user-007", UserTypeAgent)
	hub1.Register(client)

	time.Sleep(300 * time.Millisecond)

	// Hub2 应该收到 Hub1 发布的事件
	assert.Equal(t, int64(1), recorder.getOnlineCount(), "Hub2 应该收到 Hub1 发布的事件")

	events := recorder.getOnlineEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "user-007", events[0].UserID)
	assert.Equal(t, hub1.GetNodeID(), events[0].NodeID, "事件应该来自 Hub1")

	hub1.Unregister(client)
}

// ============================================================================
// 性能和并发测试
// ============================================================================

// TestEventsConcurrency 测试事件并发处理
func TestEventsConcurrency(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	recorder := newEventRecorder()

	// 订阅事件
	unsubscribe, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		recorder.recordOnline(event)
		return nil
	})
	require.NoError(t, err)
	defer func() {
		if unsubscribe != nil {
			_ = unsubscribe()
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// 并发注册多个客户端
	clientCount := 10
	var wg sync.WaitGroup
	wg.Add(clientCount)

	for i := 0; i < clientCount; i++ {
		go func(index int) {
			defer wg.Done()
			clientID := "client-" + string(rune('A'+index))
			userID := "user-" + string(rune('A'+index))
			client := CreateTestClient(clientID, userID, UserTypeCustomer)
			hub.Register(client)
			time.Sleep(50 * time.Millisecond)
			hub.Unregister(client)
		}(i)
	}

	wg.Wait()
	time.Sleep(500 * time.Millisecond)

	// 验证所有事件都被接收
	count := recorder.getOnlineCount()
	assert.Equal(t, int64(clientCount), count, "应该收到所有上线事件")
}

// ============================================================================
// 错误处理测试
// ============================================================================

// TestEventHandlerError 测试事件处理器返回错误
func TestEventHandlerError(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	var errorCount atomic.Int32

	// 订阅事件，处理器返回错误
	unsubscribe, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		errorCount.Add(1)
		return assert.AnError // 模拟处理失败
	})
	require.NoError(t, err)
	defer func() {
		if unsubscribe != nil {
			_ = unsubscribe()
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// 触发事件
	client := CreateTestClient("client-008", "user-008", UserTypeCustomer)
	hub.Register(client)

	time.Sleep(300 * time.Millisecond)

	// 即使处理器返回错误，事件也应该被处理
	assert.Greater(t, errorCount.Load(), int32(0), "事件处理器应该被调用")

	hub.Unregister(client)
}

// ============================================================================
// 事件内容验证测试
// ============================================================================

// TestEventContent 测试事件内容完整性
func TestEventContent(t *testing.T) {
	redisClient := GetTestRedisClient(t)
	defer CleanupRedisKeys(t, redisClient, "wsc:pubsub:", "test:")

	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	defer hub.Shutdown()

	pubsub := cachex.NewPubSub(redisClient, cachex.PubSubConfig{
		Namespace:     "wsc",
		EnableLogging: false,
	})
	hub.SetPubSub(pubsub)

	StartTestHub(t, hub)

	recorder := newEventRecorder()

	// 订阅所有类型的事件
	unsubOnline, _ := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
		recorder.recordOnline(event)
		return nil
	})
	defer func() {
		if unsubOnline != nil {
			_ = unsubOnline()
		}
	}()

	unsubOffline, _ := hub.SubscribeUserOffline(func(event *UserStatusEvent) error {
		recorder.recordOffline(event)
		return nil
	})
	defer func() {
		if unsubOffline != nil {
			_ = unsubOffline()
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// 测试不同用户类型
	testCases := []struct {
		name     string
		userType UserType
	}{
		{"Customer", UserTypeCustomer},
		{"Agent", UserTypeAgent},
		{"Admin", UserTypeAdmin},
		{"Bot", UserTypeBot},
	}

	for i, tc := range testCases {
		clientID := "client-" + tc.name
		userID := "user-" + tc.name

		client := CreateTestClient(clientID, userID, tc.userType)
		hub.Register(client)
		time.Sleep(200 * time.Millisecond)
		hub.Unregister(client)
		time.Sleep(200 * time.Millisecond)

		// 验证上线事件
		onlineEvents := recorder.getOnlineEvents()
		require.Greater(t, len(onlineEvents), i, "应该收到上线事件")
		event := onlineEvents[i]
		assert.Equal(t, userID, event.UserID)
		assert.Equal(t, tc.userType, event.UserType)
		assert.Equal(t, EventTypeOnline, event.EventType)
		assert.NotEmpty(t, event.NodeID)
		assert.NotZero(t, event.Timestamp)

		// 验证下线事件
		offlineEvents := recorder.getOfflineEvents()
		require.Greater(t, len(offlineEvents), i, "应该收到下线事件")
		event = offlineEvents[i]
		assert.Equal(t, userID, event.UserID)
		assert.Equal(t, tc.userType, event.UserType)
		assert.Equal(t, EventTypeOffline, event.EventType)
	}
}
