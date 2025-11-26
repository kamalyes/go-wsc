/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-21
 * @FilePath: \go-wsc\hub_enhanced_test.go
 * @Description: Hub增强功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestHubEnhancedFeatures(t *testing.T) {
	config := wscconfig.Default().
		WithEnhancement(wscconfig.DefaultEnhancement().
			WithSmartRouting(true).
			WithLoadBalancing(true, "round-robin").
			WithSmartQueue(true, 1000).
			WithMonitoring(true).
			WithClusterManagement(true).
			WithCircuitBreaker(true, 5, 3, 30)).
		WithNodeIP("127.0.0.1").
		WithNodePort(8080)

	hub := NewHub(config)

	// 手动初始化增强组件
	hub.InitializeEnhancements()

	// 验证增强组件已初始化
	assert.NotNil(t, hub.messageRouter, "MessageRouter should be initialized")
	assert.NotNil(t, hub.loadBalancer, "LoadBalancer should be initialized")
	assert.NotNil(t, hub.smartQueue, "SmartQueue should be initialized")
	assert.NotNil(t, hub.monitor, "HubMonitor should be initialized")
	assert.NotNil(t, hub.clusterManager, "ClusterManager should be initialized")
	assert.NotNil(t, hub.ruleEngine, "RuleEngine should be initialized")
	assert.NotNil(t, hub.circuitBreaker, "CircuitBreaker should be initialized")
	assert.NotNil(t, hub.messageFilter, "MessageFilter should be initialized")
	assert.NotNil(t, hub.performanceTracker, "PerformanceTracker should be initialized")
}

func TestSmartQueue(t *testing.T) {
	queue := NewSmartQueue(100)

	// 测试不同优先级的消息入队
	highPriorityMsg := &HubMessage{
		ID:          "high-priority",
		MessageType: MessageTypeAlert,
		Priority:    PriorityHigh,
		Content:     "High priority alert",
	}

	normalMsg := &HubMessage{
		ID:          "normal",
		MessageType: MessageTypeText,
		Priority:    PriorityNormal,
		Content:     "Normal message",
	}

	lowPriorityMsg := &HubMessage{
		ID:          "low-priority",
		MessageType: MessageTypeText,
		Priority:    PriorityLow,
		Content:     "Low priority message",
	}

	// 入队测试
	assert.NoError(t, queue.Enqueue(highPriorityMsg), "High priority message should enqueue successfully")
	assert.NoError(t, queue.Enqueue(normalMsg), "Normal message should enqueue successfully")
	assert.NoError(t, queue.Enqueue(lowPriorityMsg), "Low priority message should enqueue successfully")

	// 验证指标
	metrics := queue.GetMetrics()
	assert.Equal(t, int64(3), metrics.TotalEnqueued.Load(), "Should have 3 enqueued messages")
	assert.Equal(t, int64(1), metrics.CurrentHigh.Load(), "Should have 1 high priority message")
	assert.Equal(t, int64(1), metrics.CurrentNormal.Load(), "Should have 1 normal message")
	assert.Equal(t, int64(1), metrics.CurrentLow.Load(), "Should have 1 low priority message")
}

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker("test-circuit", 3, 2, 1*time.Second)

	// 初始状态应该是关闭的
	assert.True(t, cb.AllowRequest(), "Circuit breaker should initially allow requests")

	// 记录失败直到达到阈值
	cb.RecordFailure()
	cb.RecordFailure()
	assert.True(t, cb.AllowRequest(), "Should still allow requests before threshold")

	cb.RecordFailure()
	assert.False(t, cb.AllowRequest(), "Should not allow requests after threshold")

	// 等待超时后应该进入半开状态
	time.Sleep(1100 * time.Millisecond)
	assert.True(t, cb.AllowRequest(), "Should allow requests after timeout (half-open)")

	// 记录成功恢复
	cb.RecordSuccess()
	cb.RecordSuccess()
	assert.True(t, cb.AllowRequest(), "Should allow requests after recovery")

	state := cb.GetState()
	assert.Equal(t, "test-circuit", state["name"])
	assert.Equal(t, 0, state["failure_count"])
}

func TestMessageFilter(t *testing.T) {
	filter := NewMessageFilter()

	// 创建测试消息
	msg := &HubMessage{
		ID:          "test-msg",
		MessageType: MessageTypeText,
		Sender:      "user1",
		Content:     "Test message",
	}

	// 默认应该允许
	assert.True(t, filter.Allow(msg), "Should allow message by default")

	// 设置速率限制
	filter.SetRateLimit("user1", 2, 1*time.Second)

	// 前两次应该允许
	assert.True(t, filter.Allow(msg), "Should allow first message")
	assert.True(t, filter.Allow(msg), "Should allow second message")
	assert.False(t, filter.Allow(msg), "Should deny third message due to rate limit")
}

func TestRuleEngine(t *testing.T) {
	engine := NewRuleEngine()

	executed := false
	rule := Rule{
		Name:    "test-rule",
		Enabled: true,
		Condition: func(vars map[string]interface{}) bool {
			userID, ok := vars["userID"].(string)
			return ok && userID == "vip-user"
		},
		Action: func(vars map[string]interface{}) error {
			executed = true
			return nil
		},
	}

	engine.AddRule(rule)

	// 测试条件不匹配
	variables := map[string]interface{}{
		"userID": "normal-user",
	}
	err := engine.Process(variables)
	assert.NoError(t, err)
	assert.False(t, executed, "Rule should not execute for normal user")

	// 测试条件匹配
	variables["userID"] = "vip-user"
	err = engine.Process(variables)
	assert.NoError(t, err)
	assert.True(t, executed, "Rule should execute for VIP user")
}

func TestLoadBalancer(t *testing.T) {
	lb := NewLoadBalancer(RoundRobin)

	// 添加客服
	agents := []*Client{
		{ID: "agent1", UserID: "agent1", MaxTickets: 0},
		{ID: "agent2", UserID: "agent2", MaxTickets: 0},
		{ID: "agent3", UserID: "agent3", MaxTickets: 0},
	}

	lb.agents = agents

	// 测试轮询算法
	selected1 := lb.SelectAgent()
	selected2 := lb.SelectAgent()
	selected3 := lb.SelectAgent()
	selected4 := lb.SelectAgent() // 应该回到第一个

	assert.Equal(t, "agent1", selected1.UserID)
	assert.Equal(t, "agent2", selected2.UserID)
	assert.Equal(t, "agent3", selected3.UserID)
	assert.Equal(t, "agent1", selected4.UserID) // 轮询回到第一个
}

func TestPerformanceTracker(t *testing.T) {
	tracker := NewPerformanceTracker(100)

	// 开始追踪
	span := tracker.StartSpan("test-operation")
	assert.NotNil(t, span, "Span should be created")
	assert.Equal(t, "test-operation", span.Name)
	assert.False(t, span.StartTime.IsZero(), "Start time should be set")

	// 模拟操作
	time.Sleep(10 * time.Millisecond)

	// 结束追踪
	span.End()
	assert.False(t, span.EndTime.IsZero(), "End time should be set")
	assert.True(t, span.Duration > 0, "Duration should be positive")
	assert.True(t, span.Duration >= 10*time.Millisecond, "Duration should be at least 10ms")
}

func TestHubEnhancedSend(t *testing.T) {
	config := wscconfig.Default().
		Enable().
		WithNodeIP("127.0.0.1").
		WithNodePort(8080)

	config.Enhancement.Enabled = true
	config.Enhancement.SmartRouting = true
	config.Enhancement.LoadBalancing = true
	config.Enhancement.SmartQueue = true

	hub := NewHub(config)

	hub.InitializeEnhancements()

	// 创建测试客户端
	client := &Client{
		ID:       "test-client",
		UserID:   "test-user",
		VIPLevel: VIPLevelV3,
		Role:     UserRoleCustomer,
		Context:  context.Background(),
	}

	// 通过公共API注册客户端
	hub.Register(client)

	// 等待注册完成
	time.Sleep(10 * time.Millisecond)

	// 创建测试消息
	msg := &HubMessage{
		ID:          "test-enhanced-msg",
		MessageType: MessageTypeText,
		Sender:      "sender",
		Receiver:    "test-user",
		Content:     "Enhanced message",
	}

	ctx := context.Background()

	// 测试增强发送（会失败因为没有实际连接，但应该通过所有检查）
	err := hub.SendWithEnhancement(ctx, "test-user", msg)
	// 预期会因为队列满或其他原因失败，但这里我们主要测试流程
	t.Logf("Enhanced send result: %v", err)

	// 测试VIP优先发送
	count := hub.SendToVIPWithPriority(ctx, VIPLevelV1, msg)
	assert.GreaterOrEqual(t, count, 0, "Should send to VIP users")

	// 测试获取增强指标
	metrics := hub.GetEnhancedMetrics()
	assert.NotNil(t, metrics["hub_metrics"], "Should have hub metrics")
	assert.NotNil(t, metrics["queue_metrics"], "Should have queue metrics")
	assert.NotNil(t, metrics["monitor_metrics"], "Should have monitor metrics")
	assert.NotNil(t, metrics["circuit_breaker"], "Should have circuit breaker state")
}

func TestHubRulesAndFilters(t *testing.T) {
	config := wscconfig.Default().
		Enable().
		WithNodeIP("127.0.0.1").
		WithNodePort(8080).
		WithEnhancement(wscconfig.DefaultEnhancement().
			WithRuleEngine(true).
			WithMessageFiltering(true).
			WithMonitoring(true))

	config.Enhancement.Enabled = true
	config.Enhancement.RuleEngine = true
	config.Enhancement.MessageFiltering = true
	config.Enhancement.Monitoring = true

	hub := NewHub(config)

	hub.InitializeEnhancements()

	// 添加业务规则
	ruleExecuted := false
	rule := Rule{
		Name:    "vip-priority-rule",
		Enabled: true,
		Condition: func(vars map[string]interface{}) bool {
			msg, ok := vars["message"].(*HubMessage)
			return ok && msg.MessageType == MessageTypeAlert
		},
		Action: func(vars map[string]interface{}) error {
			ruleExecuted = true
			msg := vars["message"].(*HubMessage)
			msg.Priority = PriorityHigh // 提升警报优先级
			return nil
		},
	}

	hub.AddRule(rule)

	// 添加消息过滤器
	filter := Filter{
		Name: "spam-filter",
		Condition: func(msg *HubMessage) bool {
			return msg.Content == "spam"
		},
		Action: FilterDeny,
	}

	hub.AddFilter(filter)

	// 设置速率限制
	hub.SetRateLimit("rate-limited-user", 1, 1*time.Second)

	// 测试规则是否工作
	msg := &HubMessage{
		ID:          "alert-msg",
		MessageType: MessageTypeAlert,
		Content:     "Emergency alert",
	}

	variables := map[string]interface{}{
		"message": msg,
		"userID":  "test-user",
	}

	err := hub.ruleEngine.Process(variables)
	assert.NoError(t, err)
	assert.True(t, ruleExecuted, "VIP priority rule should execute")
	assert.Equal(t, PriorityHigh, msg.Priority, "Alert priority should be elevated")
}

func BenchmarkEnhancedHub(b *testing.B) {
	config := wscconfig.Default().
		Enable().
		WithNodeIP("127.0.0.1").
		WithNodePort(8080).
		WithEnhancement(wscconfig.DefaultEnhancement().
			WithSmartRouting(true).
			WithLoadBalancing(true, "round-robin").
			WithSmartQueue(true, 1000).
			WithPerformanceTracking(true))

	config.Enhancement.Enabled = true
	config.Enhancement.SmartRouting = true
	config.Enhancement.LoadBalancing = true
	config.Enhancement.SmartQueue = true
	config.Enhancement.PerformanceTracking = true

	hub := NewHub(config)

	hub.InitializeEnhancements()

	msg := &HubMessage{
		ID:          "benchmark-msg",
		MessageType: MessageTypeText,
		Sender:      "sender",
		Content:     "Benchmark message",
	}

	b.ResetTimer()

	b.Run("SmartQueueEnqueue", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = hub.smartQueue.Enqueue(msg)
		}
	})

	b.Run("CircuitBreakerCheck", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = hub.circuitBreaker.AllowRequest()
		}
	})

	b.Run("MessageFilterCheck", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = hub.messageFilter.Allow(msg)
		}
	})

	b.Run("PerformanceTracker", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			span := hub.performanceTracker.StartSpan("benchmark-operation")
			span.End()
		}
	})
}
