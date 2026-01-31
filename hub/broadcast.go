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

	// 🌐 分布式广播：发送到所有节点
	if h.pubsub != nil {
		go func() {
			if err := h.broadcastToAllNodes(ctx, msg); err != nil {
				h.logger.ErrorKV("跨节点广播失败", "error", err, "message_id", msg.ID)
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
			"message_id", msg.ID,
			"sender", msg.Sender,
			"message_type", msg.MessageType,
		)
		select {
		case h.pendingMessages <- msg:
			// 成功放入待发送队列
		default:
			// 两个队列都满，静默丢弃（广播消息不返回错误）
			h.logger.ErrorKV("所有队列已满，丢弃广播消息",
				"message_id", msg.ID,
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

// BroadcastToGroup 发送消息给特定用户类型的所有客户端
func (h *Hub) BroadcastToGroup(ctx context.Context, userType UserType, msg *HubMessage) int {
	// 获取所有客户端副本
	allClients := h.GetClientsCopy()

	// 过滤指定类型的客户端
	clients := mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.UserType == userType
	})

	var successCount int32
	syncx.NewParallelSliceExecutor[*Client, *SendResult](clients).
		OnSuccess(func(idx int, client *Client, result *SendResult) {
			if result.Success {
				atomic.AddInt32(&successCount, 1)
			}
		}).
		Execute(func(idx int, client *Client) (*SendResult, error) {
			return h.SendToUserWithRetry(ctx, client.UserID, msg), nil
		})

	return int(successCount)
}

// BroadcastToRole 发送消息给特定角色的所有用户
func (h *Hub) BroadcastToRole(ctx context.Context, role UserRole, msg *HubMessage) int {
	// 获取所有客户端副本
	allClients := h.GetClientsCopy()

	// 过滤指定角色的客户端
	clients := mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.Role == role
	})

	var successCount int32
	syncx.NewParallelSliceExecutor[*Client, *SendResult](clients).
		OnSuccess(func(idx int, client *Client, result *SendResult) {
			if result.Success {
				atomic.AddInt32(&successCount, 1)
			}
		}).
		Execute(func(idx int, client *Client) (*SendResult, error) {
			return h.SendToUserWithRetry(ctx, client.UserID, msg), nil
		})

	return int(successCount)
}

// BroadcastToClientType 发送消息给特定客户端类型
func (h *Hub) BroadcastToClientType(ctx context.Context, clientType ClientType, msg *HubMessage) int {
	// 获取所有客户端副本
	allClients := h.GetClientsCopy()

	// 过滤指定客户端类型
	clients := mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.ClientType == clientType
	})

	var successCount int32
	syncx.NewParallelSliceExecutor[*Client, *SendResult](clients).
		OnSuccess(func(idx int, client *Client, result *SendResult) {
			if result.Success {
				atomic.AddInt32(&successCount, 1)
			}
		}).
		Execute(func(idx int, client *Client) (*SendResult, error) {
			return h.SendToUserWithRetry(ctx, client.UserID, msg), nil
		})

	return int(successCount)
}

// BroadcastToDepartment 发送消息给特定部门的所有用户
func (h *Hub) BroadcastToDepartment(ctx context.Context, department Department, msg *HubMessage) int {
	// 获取所有客户端副本
	allClients := h.GetClientsCopy()

	// 过滤指定部门的客户端
	clients := mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.Department == department
	})

	var successCount int32
	syncx.NewParallelSliceExecutor[*Client, *SendResult](clients).
		OnSuccess(func(idx int, client *Client, result *SendResult) {
			if result.Success {
				atomic.AddInt32(&successCount, 1)
			}
		}).
		Execute(func(idx int, client *Client) (*SendResult, error) {
			return h.SendToUserWithRetry(ctx, client.UserID, msg), nil
		})

	return int(successCount)
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
