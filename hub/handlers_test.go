/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-11 00:06:29
 * @FilePath: \go-wsc\hub\handlers_test.go
 * @Description: Hub 资源管理与配置查询白盒单元测试（覆盖 hub/handlers.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"

	"github.com/stretchr/testify/assert"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// stubPoolManager 用于测试的连接池管理器桩实现
type stubPoolManager struct {
	smtpValue interface{}
}

func (s *stubPoolManager) GetSMTPClient() interface{} { return s.smtpValue }

// TestGetPoolManager 验证获取连接池管理器
func TestGetPoolManager(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.SafeShutdown()

	t.Run("未设置时返回 nil", func(t *testing.T) {
		assert.Nil(t, hub.GetPoolManager())
	})

	t.Run("设置后返回同一实例", func(t *testing.T) {
		pm := &stubPoolManager{smtpValue: "smtp-stub"}
		hub.SetPoolManager(pm)
		assert.Same(t, pm, hub.GetPoolManager())
	})
}

// TestGetSMTPClient 验证从连接池管理器获取 SMTP 客户端
func TestGetSMTPClient(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.SafeShutdown()

	t.Run("poolManager 为 nil 时返回 nil", func(t *testing.T) {
		assert.Nil(t, hub.GetSMTPClient())
	})

	t.Run("poolManager 非 nil 时返回其 SMTP 客户端", func(t *testing.T) {
		hub.SetPoolManager(&stubPoolManager{smtpValue: "smtp-conn"})
		assert.Equal(t, "smtp-conn", hub.GetSMTPClient())
	})
}

// TestGetRateLimiter 验证获取消息频率限制器
func TestGetRateLimiter(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.SafeShutdown()

	// NewHub 默认未设置 rateLimiter
	assert.Nil(t, hub.GetRateLimiter())
}

// TestGetMessageQueue 验证获取消息队列长度
func TestGetMessageQueue(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.SafeShutdown()

	// 初始 pendingMessages 为空
	assert.Equal(t, 0, hub.GetMessageQueue())
}
