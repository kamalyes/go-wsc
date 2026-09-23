/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:28:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 19:28:00
 * @FilePath: \go-wsc\messaging\sse.go
 * @Description: 消息域 SSE 投递 —— 点对点通道投递 / 全量 SSE 广播
 *
 * 从 hub/interfaces.go 域化下沉：SSE 通道是消息投递的一等出口之一
 *（HTTP 流式，无协议级 PING，由连接域兜底扫描清理僵尸连接）。
 * 点对点走 O(1) 用户索引 + 持读锁零拷贝遍历；广播走并行分片遍历
 *（百万级优化）+ appID/namespace 信封隔离。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
)

// SendToUserViaSSE 经 SSE 通道向用户投递（SSE 未启用或用户无订阅时返回 false）
// namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的 SSE 设备，避免跨 ns 串扰
func (m *Manager) SendToUserViaSSE(userID string, msg *models.HubMessage) bool {
	if msg == nil {
		return false
	}
	registry := m.host.GetShardedRegistry()
	if registry == nil {
		return false
	}
	// 快速检查用户是否有 SSE 连接（O(1)）
	if !registry.HasSSEUser(userID) {
		return false
	}

	logger := m.host.GetLogger()
	// 持读锁零拷贝遍历发送
	successCount := 0
	totalDevices := 0
	registry.ForEachSSEUserClient(userID, func(clientID string, client *models.Client) bool {
		// namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的设备
		if msg.Namespace != "" && client.Namespace != msg.Namespace {
			return true
		}
		totalDevices++
		if client.TrySendSSE(msg) {
			client.SetLastSeen(time.Now())
			successCount++
		} else {
			logger.WarnContextKV(msg.ContextFrom(m.host.Context()), "SSE消息队列已满",
				"user_id", userID,
				"client_id", clientID,
				"message_id", msg.MessageID,
				"message_type", msg.MessageType,
			)
		}
		return true
	})

	if successCount > 0 {
		logger.InfoContextKV(msg.ContextFrom(m.host.Context()), "SSE消息发送成功",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
			"success_devices", successCount,
			"total_devices", totalDevices,
		)
		return true
	}
	return false
}

// BroadcastToSSEClients 广播给全部 SSE 客户端（appId/namespace 信封隔离）
// 通过 ForEachSSEClientParallel 并行分片读锁遍历（百万级优化）
func (m *Manager) BroadcastToSSEClients(msg *models.HubMessage) {
	if msg == nil {
		return
	}
	registry := m.host.GetShardedRegistry()
	if registry == nil {
		return
	}
	logger := m.host.GetLogger()
	// 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	msg.InjectRoute(m.host.Context())

	start := time.Now()
	var sent, skipped int64
	registry.ForEachSSEClientParallel(0, func(_, clientID string, client *models.Client) {
		if !connection.ClientMatchesEnvelope(client, msg.AppID, msg.Namespace, msg.GroupIDs) {
			return
		}
		if client.TrySendSSE(msg) {
			client.SetLastSeen(time.Now())
			atomic.AddInt64(&sent, 1)
		} else {
			atomic.AddInt64(&skipped, 1)
			logger.WarnContextKV(msg.ContextFrom(m.host.Context()), "SSE客户端消息通道已满，跳过",
				"client_id", clientID,
				"message_id", msg.MessageID,
			)
		}
	})

	logger.DebugContextKV(msg.ContextFrom(m.host.Context()), "SSE广播完成",
		"message_id", msg.MessageID,
		"namespace", msg.Namespace,
		"total_sse_clients", registry.GetSSEClientCount(),
		"sent", atomic.LoadInt64(&sent),
		"skipped", atomic.LoadInt64(&skipped),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}
