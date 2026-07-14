/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\broadcast.go
 * @Description: Hub 广播功能
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 基础广播方法
// ============================================================================

// Broadcast 广播消息给所有客户端
// 自动支持分布式：会同时广播到所有节点
func (h *Hub) Broadcast(ctx context.Context, msg *HubMessage) {
	// 创建消息副本，避免并发修改
	msg = msg.Clone()

	// 自动设置为全局广播类型
	msg.BroadcastType = mathx.IfEmpty(msg.BroadcastType, BroadcastTypeGlobal)

	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 增加广播发送统计（原子计数器，由 flushStatsCounters 定时刷写到 Redis）
	if h.statsRepo != nil {
		h.broadcastSentCount.Add(1)
	}

	// 🌐 分布式广播：发送到所有节点
	if h.pubsub != nil {
		go func() {
			// 用独立超时 context，避免调用方 ctx 无超时时 pubsub 慢导致 goroutine 堆积
			publishCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := h.broadcastToAllNodes(publishCtx, msg); err != nil {
				h.logger.ErrorKV("跨节点广播失败", "error", err, "message_id", msg.MessageID)
			}
		}()
	}

	// 本地广播
	select {
	case h.broadcast <- msg:
		// 成功放入广播队列
	default:
		// broadcast队列满，尝试放入待发送队列
		h.logger.WarnKV("广播队列已满，尝试使用待发送队列",
			"message_id", msg.MessageID,
			"sender", msg.Sender,
			"message_type", msg.MessageType,
		)
		select {
		case h.pendingMessages <- msg:
			// 成功放入待发送队列
		default:
			// 两个队列都满，静默丢弃（广播消息不返回错误）
			h.logger.ErrorKV("所有队列已满，丢弃广播消息",
				"message_id", msg.MessageID,
				"sender", msg.Sender,
				"message_type", msg.MessageType,
				"content_length", len(msg.Content),
			)
		}
	}
}

// ============================================================================
// 分组广播方法
// ============================================================================

// broadcastToFiltered 预序列化消息并直接发送给符合条件的客户端
// 消除逐客户端 Clone/序列化/入队/DB 记录开销：
//   - 消息只 json.Marshal 1 次（原方案每客户端 1 次）
//   - 不走 SendToUserWithRetry（原方案每客户端 Clone×2 + 在线检查 + 入队 + DB 记录）
//   - 零拷贝遍历（原方案 GetClientsCopy + FilterSlice 双重拷贝）
func (h *Hub) broadcastToFiltered(condition func(*Client) bool, msg *HubMessage) int {
	// 预序列化 WebSocket 消息（仅一次）
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorKV("分组广播消息序列化失败", "error", err)
		return 0
	}

	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	dataLen := len(data)
	var successCount int32

	// WebSocket 客户端：直接 TrySend 预序列化数据
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		if client.IsClosed() || client.ConnectionType == ConnectionTypeSSE {
			return true
		}
		if !condition(client) {
			return true
		}
		if client.TrySend(data) {
			atomic.AddInt32(&successCount, 1)
			h.trackReceiverMessageStats(client.ID, client.UserType, dataLen)
		}
		return true
	})

	// SSE 客户端：通过专用通道发送 msg 对象（无需序列化）
	h.shardedRegistry.ForEachSSEClient(func(_, _ string, client *Client) bool {
		if client.IsClosed() || !condition(client) {
			return true
		}
		if client.TrySendSSE(msg) {
			atomic.AddInt32(&successCount, 1)
		}
		return true
	})

	// 消息记录状态只更新一次（同一 msgID）
	totalSuccess := atomic.LoadInt32(&successCount)
	if totalSuccess > 0 {
		h.updateMessageStatusAsync(msgID, MessageSendStatusSuccess, "", "")
	}

	return int(totalSuccess)
}

// BroadcastToGroup 发送消息给特定用户类型的所有客户端
func (h *Hub) BroadcastToGroup(ctx context.Context, userType UserType, msg *HubMessage) int {
	return h.broadcastToFiltered(func(c *Client) bool {
		return c.UserType == userType
	}, msg)
}

// BroadcastToRole 发送消息给特定角色的所有用户
func (h *Hub) BroadcastToRole(ctx context.Context, role UserRole, msg *HubMessage) int {
	return h.broadcastToFiltered(func(c *Client) bool {
		return c.Role == role
	}, msg)
}

// BroadcastToClientType 发送消息给特定客户端类型
func (h *Hub) BroadcastToClientType(ctx context.Context, clientType ClientType, msg *HubMessage) int {
	return h.broadcastToFiltered(func(c *Client) bool {
		return c.ClientType == clientType
	}, msg)
}

// BroadcastToDepartment 发送消息给特定部门的所有用户
func (h *Hub) BroadcastToDepartment(ctx context.Context, department Department, msg *HubMessage) int {
	return h.broadcastToFiltered(func(c *Client) bool {
		return c.Department == department
	}, msg)
}

// ============================================================================
// 高级广播方法
// ============================================================================

// BroadcastPriority 根据优先级广播消息
func (h *Hub) BroadcastPriority(ctx context.Context, msg *HubMessage, priority Priority) {
	msg.Priority = priority
	h.Broadcast(ctx, msg)
}

// BroadcastAfterDelay 延迟广播消息
func (h *Hub) BroadcastAfterDelay(ctx context.Context, msg *HubMessage, delay time.Duration) {
	syncx.Go(ctx).
		WithDelay(delay).
		Exec(func() {
			h.Broadcast(ctx, msg)
		})
}

// BroadcastExclude 广播消息给所有客户端，但排除指定用户
func (h *Hub) BroadcastExclude(ctx context.Context, msg *HubMessage, excludeUserIDs []string) int {
	excludeMap := make(map[string]bool, len(excludeUserIDs))
	for _, userID := range excludeUserIDs {
		excludeMap[userID] = true
	}

	return h.SendConditional(ctx, func(c *Client) bool {
		return !excludeMap[c.UserID]
	}, msg)
}

// ============================================================================
// 获取客户端列表方法
// ============================================================================

// GetClientsByUserType 获取特定用户类型的所有客户端
func (h *Hub) GetClientsByUserType(userType UserType) []*Client {
	allClients := h.GetClientsCopy()
	return mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.UserType == userType
	})
}

// GetClientsByRole 获取特定角色的所有客户端
func (h *Hub) GetClientsByRole(role UserRole) []*Client {
	allClients := h.GetClientsCopy()
	return mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.Role == role
	})
}

// GetClientsByClientType 按客户端类型获取客户端
func (h *Hub) GetClientsByClientType(clientType ClientType) []*Client {
	allClients := h.GetClientsCopy()
	return mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.ClientType == clientType
	})
}

// GetClientsByDepartment 获取特定部门的所有客户端
func (h *Hub) GetClientsByDepartment(department Department) []*Client {
	allClients := h.GetClientsCopy()
	return mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.Department == department
	})
}

// GetClientsByVIPLevel 获取特定VIP等级及以上的客户端
func (h *Hub) GetClientsByVIPLevel(minVIPLevel VIPLevel) []*Client {
	allClients := h.GetClientsCopy()
	return mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.VIPLevel.GetLevel() >= minVIPLevel.GetLevel()
	})
}
