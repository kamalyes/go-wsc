/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:25:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-23 18:41:28
 * @FilePath: \go-wsc\security_manager_test.go
 * @Description: 安全管理器测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestSecurityManagerCreation(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	assert.NotNil(t, sm)
	assert.Equal(t, config, sm.config)
	assert.NotNil(t, sm.accessRules)
	assert.NotNil(t, sm.blockedIPs)
	assert.NotNil(t, sm.threatPatterns)

	// 检查默认威胁模式是否已加载
	assert.True(t, len(sm.threatPatterns) > 0)

	stats := sm.GetSecurityStats()
	assert.Equal(t, int64(0), stats.TotalEvents)
}

func TestConnectionValidation(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	// 正常连接应该通过
	err := sm.ValidateConnection("192.168.1.100", "user123", map[string]string{})
	assert.NoError(t, err)

	// 添加到黑名单
	sm.AddToBlacklist("192.168.1.200")

	// 黑名单IP应该被拒绝
	err = sm.ValidateConnection("192.168.1.200", "user456", map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ip address is in blacklist")

	// 检查安全事件
	events := sm.GetSecurityEvents(10)
	assert.True(t, len(events) > 0)

	found := false
	for _, event := range events {
		if event.Type == SecurityEventTypeMaliciousIP {
			found = true
			assert.Equal(t, "192.168.1.200", event.ClientIP)
			break
		}
	}
	assert.True(t, found, "应该记录恶意IP事件")
}

func TestBruteForceDetection(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxLoginAttempts: 3,
	}
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	clientIP := "192.168.1.300"
	userID := "bruteuser"

	// 前3次尝试应该成功
	for i := 0; i < 3; i++ {
		err := sm.ValidateConnection(clientIP, userID, map[string]string{})
		assert.NoError(t, err)
	}

	// 第4次尝试应该被检测为暴力攻击
	err := sm.ValidateConnection(clientIP, userID, map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "brute force attack detected")

	// 检查统计信息
	stats := sm.GetSecurityStats()
	assert.True(t, stats.BruteForceAttempts > 0)
}

func TestMessageValidation(t *testing.T) {
	config := wscconfig.Default()
	config.Security = &wscconfig.Security{
		MaxMessageSize: 1024,
	}
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	userID := "testuser"
	clientIP := "192.168.1.400"

	// 正常消息应该通过
	normalMessage := []byte("Hello, this is a normal message")
	err := sm.ValidateMessage(userID, clientIP, normalMessage)
	assert.NoError(t, err)

	// 超大消息应该被拒绝
	largeMessage := make([]byte, 2048)
	for i := range largeMessage {
		largeMessage[i] = 'A'
	}
	err = sm.ValidateMessage(userID, clientIP, largeMessage)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "message too large")

	// 包含威胁的消息应该被拒绝
	threatMessage := []byte("SELECT * FROM users WHERE 1=1; DROP TABLE users;")
	err = sm.ValidateMessage(userID, clientIP, threatMessage)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "threat detected")

	// 检查安全事件
	events := sm.GetSecurityEvents(10)
	invalidMsgEvents := 0
	threatEvents := 0

	for _, event := range events {
		switch event.Type {
		case SecurityEventTypeInvalidMessage:
			invalidMsgEvents++
		case SecurityEventTypeThreatDetected:
			threatEvents++
		}
	}

	assert.True(t, invalidMsgEvents > 0, "应该有无效消息事件")
	assert.True(t, threatEvents > 0, "应该有威胁检测事件")
}

func TestThreatDetection(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	testCases := []struct {
		name         string
		message      string
		shouldDetect bool
	}{
		{
			name:         "SQL注入",
			message:      "'; DROP TABLE users; --",
			shouldDetect: true,
		},
		{
			name:         "XSS攻击",
			message:      "<script>alert('xss')</script>",
			shouldDetect: true,
		},
		{
			name:         "路径遍历",
			message:      "../../../etc/passwd",
			shouldDetect: true,
		},
		{
			name:         "命令注入",
			message:      "test; rm -rf /",
			shouldDetect: false, // rm命令不在默认模式中
		},
		{
			name:         "正常消息",
			message:      "Hello, how are you today?",
			shouldDetect: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			threat := sm.detectThreat(tc.message)
			if tc.shouldDetect {
				assert.NotNil(t, threat, "应该检测到威胁")
			} else {
				assert.Nil(t, threat, "不应该检测到威胁")
			}
		})
	}
}

func TestAccessRules(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	// 添加IP拒绝规则
	rule := &AccessRule{
		Name: "Block test IP",
		Type: AccessRuleTypeIP,
		Conditions: map[string]string{
			"ip": "192.168.1.500",
		},
		Action:   AccessActionDeny,
		Priority: 100,
		Enabled:  true,
	}
	sm.AddAccessRule(rule)

	// 被规则拒绝的IP
	err := sm.ValidateConnection("192.168.1.500", "testuser", map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "access denied by security rule")

	// 其他IP应该正常
	err = sm.ValidateConnection("192.168.1.501", "testuser", map[string]string{})
	assert.NoError(t, err)

	// 添加用户拒绝规则
	userRule := &AccessRule{
		Name: "Block bad user",
		Type: AccessRuleTypeUser,
		Conditions: map[string]string{
			"user_id": "baduser",
		},
		Action:   AccessActionDeny,
		Priority: 90,
		Enabled:  true,
	}
	sm.AddAccessRule(userRule)

	// 被规则拒绝的用户
	err = sm.ValidateConnection("192.168.1.502", "baduser", map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "access denied by security rule")
}

func TestIPRangeMatching(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	testCases := []struct {
		clientIP    string
		ipRange     string
		shouldMatch bool
	}{
		{
			clientIP:    "192.168.1.100",
			ipRange:     "192.168.1.0/24",
			shouldMatch: true,
		},
		{
			clientIP:    "192.168.2.100",
			ipRange:     "192.168.1.0/24",
			shouldMatch: false,
		},
		{
			clientIP:    "10.0.0.1",
			ipRange:     "10.0.0.1",
			shouldMatch: true,
		},
		{
			clientIP:    "10.0.0.2",
			ipRange:     "10.0.0.1",
			shouldMatch: false,
		},
	}

	for _, tc := range testCases {
		result := sm.isIPInRange(tc.clientIP, tc.ipRange)
		assert.Equal(t, tc.shouldMatch, result,
			"IP %s in range %s", tc.clientIP, tc.ipRange)
	}
}

func TestSecurityEventHandlers(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	eventReceived := false
	var receivedEvent SecurityEvent

	// 注册事件处理器
	sm.OnSecurityEvent(func(event SecurityEvent) {
		eventReceived = true
		receivedEvent = event
	})

	// 触发安全事件
	sm.AddToBlacklist("192.168.1.600")
	err := sm.ValidateConnection("192.168.1.600", "testuser", map[string]string{})
	assert.Error(t, err)

	// 等待事件处理器执行
	time.Sleep(100 * time.Millisecond)

	assert.True(t, eventReceived, "应该收到安全事件")
	assert.Equal(t, SecurityEventTypeMaliciousIP, receivedEvent.Type)
	assert.Equal(t, "192.168.1.600", receivedEvent.ClientIP)
}

func TestWhitelistBlacklist(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	testIP := "192.168.1.700"

	// 添加到黑名单
	sm.AddToBlacklist(testIP)
	assert.True(t, sm.blacklistIPs[testIP])
	assert.False(t, sm.whitelistIPs[testIP])

	// 添加到白名单（应该从黑名单移除）
	sm.AddToWhitelist(testIP)
	assert.True(t, sm.whitelistIPs[testIP])
	assert.False(t, sm.blacklistIPs[testIP])

	// 再次添加到黑名单（应该从白名单移除）
	sm.AddToBlacklist(testIP)
	assert.True(t, sm.blacklistIPs[testIP])
	assert.False(t, sm.whitelistIPs[testIP])
}

func TestThreatPatterns(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	// 添加自定义威胁模式
	customPattern := &ThreatPattern{
		ID:          "custom_test",
		Name:        "测试威胁",
		Severity:    "medium",
		Keywords:    []string{"testThreat", "malicious"},
		Description: "测试用威胁模式",
		Enabled:     true,
	}
	sm.AddThreatPattern(customPattern)

	// 测试自定义威胁检测
	threat := sm.detectThreat("This message contains testThreat keyword")
	assert.NotNil(t, threat)
	assert.Equal(t, "custom_test", threat.ID)

	// 移除威胁模式
	sm.RemoveThreatPattern("custom_test")
	threat = sm.detectThreat("This message contains testThreat keyword")
	assert.Nil(t, threat)
}

func TestSecurityReport(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	// 生成一些安全事件
	sm.AddToBlacklist("192.168.1.800")
	sm.ValidateConnection("192.168.1.800", "testuser", map[string]string{})

	// 生成报告
	report := sm.GenerateSecurityReport()
	assert.NotEmpty(t, report)
	assert.Contains(t, report, "安全管理报告")
	assert.Contains(t, report, "总安全事件")
	assert.Contains(t, report, "活跃访问规则")
	assert.Contains(t, report, "威胁检测模式")
}

func TestSecurityStatsUpdate(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	initialStats := sm.GetSecurityStats()
	assert.Equal(t, int64(0), initialStats.TotalEvents)

	// 触发一些安全事件
	sm.AddToBlacklist("192.168.1.900")
	sm.ValidateConnection("192.168.1.900", "testuser", map[string]string{})

	// 检查统计更新
	updatedStats := sm.GetSecurityStats()
	assert.True(t, updatedStats.TotalEvents > initialStats.TotalEvents)
	assert.True(t, updatedStats.BlockedConnections > 0)
	assert.True(t, len(updatedStats.EventsByType) > 0)
}

func TestRuleGeneration(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	// 测试规则ID生成
	id1 := sm.generateRuleID()
	id2 := sm.generateRuleID()

	assert.NotEmpty(t, id1)
	assert.NotEmpty(t, id2)
	assert.NotEqual(t, id1, id2)
	assert.Equal(t, 32, len(id1)) // 16字节的十六进制字符串
}

func TestHashFunction(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	// 测试哈希函数
	data := "test data"
	hash1 := sm.Hash(data)
	hash2 := sm.Hash(data)
	hash3 := sm.Hash("different data")

	assert.Equal(t, hash1, hash2, "相同数据应该产生相同哈希")
	assert.NotEqual(t, hash1, hash3, "不同数据应该产生不同哈希")
	assert.Equal(t, 64, len(hash1)) // SHA256产生64字符的十六进制字符串
}

func TestTimeRuleMatching(t *testing.T) {
	config := wscconfig.Default()
	sm := NewSecurityManager(config)
	defer sm.Shutdown()

	now := time.Now()

	// 创建时间规则（当前时间的前后1小时）
	startTime := now.Add(-1 * time.Hour).Format("15:04")
	endTime := now.Add(1 * time.Hour).Format("15:04")

	rule := &AccessRule{
		Type: AccessRuleTypeTime,
		Conditions: map[string]string{
			"start_time": startTime,
			"end_time":   endTime,
		},
	}

	// 当前时间应该匹配
	matches := sm.matchesTimeRule(rule)
	assert.True(t, matches)

	// 创建不匹配的时间规则
	pastStartTime := now.Add(-5 * time.Hour).Format("15:04")
	pastEndTime := now.Add(-3 * time.Hour).Format("15:04")

	pastRule := &AccessRule{
		Type: AccessRuleTypeTime,
		Conditions: map[string]string{
			"start_time": pastStartTime,
			"end_time":   pastEndTime,
		},
	}

	// 过去的时间不应该匹配
	matches = sm.matchesTimeRule(pastRule)
	assert.False(t, matches)
}
