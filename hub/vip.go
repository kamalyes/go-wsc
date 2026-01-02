/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\vip.go
 * @Description: Hub VIP功能
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// VIP用户发送方法
// ============================================================================

// SendToVIPUsers 发送消息给指定VIP等级及以上的用户
func (h *Hub) SendToVIPUsers(ctx context.Context, minVIPLevel VIPLevel, msg *HubMessage) int {
	return h.SendConditional(ctx, func(c *Client) bool {
		return c.VIPLevel.GetLevel() >= minVIPLevel.GetLevel()
	}, msg)
}

// SendToExactVIPLevel 发送消息给指定VIP等级用户
func (h *Hub) SendToExactVIPLevel(ctx context.Context, vipLevel VIPLevel, msg *HubMessage) int {
	return h.SendConditional(ctx, func(c *Client) bool {
		return c.VIPLevel == vipLevel
	}, msg)
}

// SendWithVIPPriority 根据用户VIP等级自动设置消息优先级
func (h *Hub) SendWithVIPPriority(ctx context.Context, userID string, msg *HubMessage) {
	// 根据用户VIP等级设置消息优先级
	clientMap := syncx.WithRLockReturnValue(&h.mutex, func() map[string]*Client {
		return h.userToClients[userID]
	})

	if len(clientMap) > 0 {
		// 获取第一个客户端的VIP等级
		var client *Client
		for _, c := range clientMap {
			client = c
			break
		}
		// 根据VIP等级自动调整优先级
		vipLevel := client.VIPLevel.GetLevel()
		if vipLevel >= 6 { // V6-V8
			msg.Priority = PriorityHigh
		} else if vipLevel >= 3 { // V3-V5
			msg.Priority = PriorityNormal
		} else { // V0-V2
			msg.Priority = PriorityLow
		}
	}

	h.SendToUserWithRetry(ctx, userID, msg)
}

// SendToVIPWithPriority 根据VIP等级优先发送
func (h *Hub) SendToVIPWithPriority(ctx context.Context, vipLevel VIPLevel, msg *HubMessage) int {
	// VIP消息优先级更高
	if vipLevel.GetLevel() >= 5 {
		msg.Priority = PriorityHigh
	} else if vipLevel.GetLevel() >= 3 {
		msg.Priority = PriorityNormal
	}

	return h.SendConditional(ctx, func(c *Client) bool {
		return c.VIPLevel.GetLevel() >= vipLevel.GetLevel()
	}, msg)
}

// ============================================================================
// VIP分类发送
// ============================================================================

// SendToUserWithClassification 使用完整分类系统发送消息
// 发送结果通过 OnMessageSend 回调通知
func (h *Hub) SendToUserWithClassification(ctx context.Context, userID string, msg *HubMessage, classification *MessageClassification) {
	// 设置消息分类信息
	if classification != nil {
		msg.MessageType = classification.Type

		// 根据分类计算优先级
		finalScore := classification.GetFinalPriority()
		if finalScore >= 80 {
			msg.Priority = PriorityHigh
		} else if finalScore >= 50 {
			msg.Priority = PriorityNormal
		} else {
			msg.Priority = PriorityLow
		}

		// 添加分类信息到消息数据中
		if msg.Data == nil {
			msg.Data = make(map[string]interface{})
		}
		msg.Data["classification"] = classification
		msg.Data["priority_score"] = finalScore
		msg.Data["is_critical"] = classification.IsCriticalMessage()
	}

	h.SendToUserWithRetry(ctx, userID, msg)
}

// ============================================================================
// VIP统计和查询
// ============================================================================

// GetVIPStatistics 获取VIP用户统计
func (h *Hub) GetVIPStatistics() map[string]int {
	return syncx.WithRLockReturnValue(&h.mutex, func() map[string]int {
		stats := make(map[string]int)

		// 统计各VIP等级用户数量
		for _, level := range GetAllVIPLevels() {
			stats[string(level)] = 0
		}

		for _, client := range h.clients {
			if client.VIPLevel.IsValid() {
				stats[string(client.VIPLevel)]++
			}
		}

		stats["total_vip"] = 0
		for level, count := range stats {
			if level != "v0" && level != "total_vip" {
				stats["total_vip"] += count
			}
		}

		return stats
	})
}

// FilterVIPClients 筛选VIP用户客户端
func (h *Hub) FilterVIPClients(minLevel VIPLevel) []*Client {
	return syncx.WithRLockReturnValue(&h.mutex, func() []*Client {
		var vipClients []*Client
		for _, client := range h.clients {
			if client.VIPLevel.GetLevel() >= minLevel.GetLevel() {
				vipClients = append(vipClients, client)
			}
		}
		return vipClients
	})
}

// ============================================================================
// VIP等级管理
// ============================================================================

// UpgradeVIPLevel 升级用户VIP等级
func (h *Hub) UpgradeVIPLevel(userID string, newLevel VIPLevel) bool {
	return syncx.WithLockReturnValue(&h.mutex, func() bool {
		clientMap, exists := h.userToClients[userID]
		if !exists || len(clientMap) == 0 || !newLevel.IsValid() {
			return false
		}

		// 获取任意一个客户端的当前等级(所有客户端等级一致)
		var currentLevel VIPLevel
		for _, client := range clientMap {
			currentLevel = client.VIPLevel
			break
		}

		// 只允许升级，不允许降级
		if newLevel.GetLevel() > currentLevel.GetLevel() {
			// 升级所有客户端的VIP等级
			for _, client := range clientMap {
				client.VIPLevel = newLevel
			}
			return true
		}

		return false
	})
}
