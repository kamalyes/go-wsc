/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:32:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-23 07:11:16
 * @FilePath: \go-wsc\security_integration_test.go
 * @Description: 安全管理器与Hub集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestHubSecurityIntegration(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   1024,
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)
	defer hub.SafeShutdown()

	// 测试安全统计API
	stats := hub.GetSecurityStats()
	assert.Equal(t, int64(0), stats.TotalEvents)

	// 测试添加黑名单
	hub.AddToSecurityBlacklist("192.168.1.100")

	// 测试安全报告生成
	report := hub.GenerateSecurityReport()
	assert.NotEmpty(t, report)
	assert.Contains(t, report, "安全管理报告")
}

func TestHubSecurityEventHandling(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   1024,
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)
	defer hub.SafeShutdown()

	eventReceived := false
	var receivedEvent SecurityEvent

	// 注册安全事件处理器
	hub.OnSecurityEvent(func(event SecurityEvent) {
		eventReceived = true
		receivedEvent = event
	})

	// 添加IP到黑名单并触发事件
	hub.AddToSecurityBlacklist("192.168.1.200")

	// 模拟黑名单IP的连接验证（这会触发安全事件）
	if hub.securityManager != nil {
		err := hub.securityManager.ValidateConnection("192.168.1.200", "testuser", map[string]string{})
		assert.Error(t, err)
	}

	// 等待事件处理器执行
	time.Sleep(100 * time.Millisecond)

	assert.True(t, eventReceived)
	assert.Equal(t, SecurityEventTypeMaliciousIP, receivedEvent.Type)
}

func TestHubSecurityAccessRules(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   1024,
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)
	defer hub.SafeShutdown()

	// 添加IP拒绝规则
	rule := &AccessRule{
		Name: "Test Block Rule",
		Type: AccessRuleTypeIP,
		Conditions: map[string]string{
			"ip": "192.168.1.300",
		},
		Action:   AccessActionDeny,
		Priority: 100,
		Enabled:  true,
	}
	hub.AddSecurityAccessRule(rule)

	// 测试被拒绝的IP
	if hub.securityManager != nil {
		err := hub.securityManager.ValidateConnection("192.168.1.300", "testuser", map[string]string{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "access denied")
	}

	// 测试正常IP
	if hub.securityManager != nil {
		err := hub.securityManager.ValidateConnection("192.168.1.301", "testuser", map[string]string{})
		assert.NoError(t, err)
	}
}

func TestHubSecurityThreatPatterns(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   1024,
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)
	defer hub.SafeShutdown()

	// 添加自定义威胁模式
	pattern := &ThreatPattern{
		ID:          "test_threat",
		Name:        "测试威胁",
		Severity:    "high",
		Keywords:    []string{"malicious_content"},
		Description: "测试威胁模式",
		Enabled:     true,
	}
	hub.AddSecurityThreatPattern(pattern)

	// 测试威胁检测
	if hub.securityManager != nil {
		// 正常消息应该通过
		err := hub.securityManager.ValidateMessage("testuser", "192.168.1.400", []byte("Hello world"))
		assert.NoError(t, err)

		// 包含威胁的消息应该被拒绝
		err = hub.securityManager.ValidateMessage("testuser", "192.168.1.400", []byte("This contains malicious_content"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "威胁")
	}

	// 移除威胁模式
	hub.RemoveSecurityThreatPattern("test_threat")

	// 现在相同的消息应该通过
	if hub.securityManager != nil {
		err := hub.securityManager.ValidateMessage("testuser", "192.168.1.400", []byte("This contains malicious_content"))
		assert.NoError(t, err)
	}
}

func TestHubSecurityBruteForceDetection(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   1024,
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)
	defer hub.SafeShutdown()

	clientIP := "192.168.1.500"
	userID := "brutetest"

	// 前3次尝试应该成功
	for i := 0; i < 3; i++ {
		err := hub.securityManager.ValidateConnection(clientIP, userID, map[string]string{})
		assert.NoError(t, err)
	}

	// 第4次尝试应该被检测为暴力攻击
	err := hub.securityManager.ValidateConnection(clientIP, userID, map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "brute force attack detected")

	// 检查安全统计
	stats := hub.GetSecurityStats()
	assert.True(t, stats.BruteForceAttempts > 0)
}

func TestHubSecurityMessageValidation(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   100, // 设置较小的消息限制
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)
	defer hub.SafeShutdown()

	userID := "msgtest"
	clientIP := "192.168.1.600"

	// 正常大小的消息应该通过
	normalMessage := []byte("Hello, this is a normal message")
	err := hub.securityManager.ValidateMessage(userID, clientIP, normalMessage)
	assert.NoError(t, err)

	// 超大消息应该被拒绝
	largeMessage := make([]byte, 200)
	for i := range largeMessage {
		largeMessage[i] = 'A'
	}
	err = hub.securityManager.ValidateMessage(userID, clientIP, largeMessage)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "超出限制")

	// 检查安全事件
	events := hub.GetSecurityEvents(10)
	found := false
	for _, event := range events {
		if event.Type == SecurityEventTypeInvalidMessage {
			found = true
			assert.Equal(t, userID, event.UserID)
			assert.Equal(t, clientIP, event.ClientIP)
			break
		}
	}
	assert.True(t, found, "应该记录无效消息事件")
}

func TestHubSecurityWhitelistBlacklist(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   1024,
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)
	defer hub.SafeShutdown()

	testIP := "192.168.1.700"

	// 正常IP应该可以连接
	err := hub.securityManager.ValidateConnection(testIP, "testuser", map[string]string{})
	assert.NoError(t, err)

	// 添加到黑名单
	hub.AddToSecurityBlacklist(testIP)
	err = hub.securityManager.ValidateConnection(testIP, "testuser", map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "黑名单")

	// 添加到白名单（应该从黑名单移除）
	hub.AddToSecurityWhitelist(testIP)
	err = hub.securityManager.ValidateConnection(testIP, "testuser", map[string]string{})
	assert.NoError(t, err)
}

func TestHubSecurityShutdown(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize:   1024,
		MaxLoginAttempts: 3,
		TokenExpiration:  3600,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.securityManager)

	// 测试Hub安全关闭
	err := hub.SafeShutdown()
	assert.NoError(t, err)

	// 再次关闭应该也不会出错
	err = hub.SafeShutdown()
	assert.NoError(t, err)
}
