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

	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// VIP用户发送方法
// ============================================================================

// SendToVIPUsers 发送消息给指定VIP等级及以上的用户
func (h *Hub) SendToVIPUsers(ctx context.Context, minVIPLevel VIPLevel, msg *HubMessage) int {
	minLevel := minVIPLevel.GetLevel()
	return h.SendConditional(ctx, func(c *Client) bool {
		return c.GetVIPLevel().GetLevel() >= minLevel
	}, msg)
}

// SendToExactVIPLevel 发送消息给指定VIP等级用户
func (h *Hub) SendToExactVIPLevel(ctx context.Context, vipLevel VIPLevel, msg *HubMessage) int {
	return h.SendConditional(ctx, func(c *Client) bool {
		return c.GetVIPLevel() == vipLevel
	}, msg)
}

// SendWithVIPPriority 根据用户VIP等级自动设置消息优先级
// 使用 ForEachUserClientFiltered 零拷贝遍历（按 ctx 信封过滤）+ 提前终止，替代 GetUserClients + 手动迭代
func (h *Hub) SendWithVIPPriority(ctx context.Context, userID string, msg *HubMessage) {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	// 零拷贝获取第一个匹配信封客户端的VIP等级（提前终止遍历）
	var vipLevel VIPLevel
	found := false
	h.shardedRegistry.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *Client) bool {
		vipLevel = client.GetVIPLevel()
		found = true
		return false // 第一个即终止
	})

	if found {
		// 根据VIP等级自动调整优先级
		level := vipLevel.GetLevel()
		if level >= 6 { // V6-V8
			msg.Priority = PriorityHigh
		} else if level >= 3 { // V3-V5
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
	level := vipLevel.GetLevel()
	if level >= 5 {
		msg.Priority = PriorityHigh
	} else if level >= 3 {
		msg.Priority = PriorityNormal
	}

	return h.SendConditional(ctx, func(c *Client) bool {
		return c.GetVIPLevel().GetLevel() >= level
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
	stats := make(map[string]int)

	// 统计各VIP等级用户数量
	for _, level := range GetAllVIPLevels() {
		stats[string(level)] = 0
	}

	// shardedRegistry 遍历所有客户端（原子读 VIPLevel，并发安全）
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		vipLevel := client.GetVIPLevel()
		if vipLevel.IsValid() {
			stats[string(vipLevel)]++
		}
		return true
	})

	stats["total_vip"] = 0
	for level, count := range stats {
		if level != "v0" && level != "total_vip" {
			stats["total_vip"] += count
		}
	}

	return stats
}

// FilterVIPClients 筛选VIP用户客户端
func (h *Hub) FilterVIPClients(minLevel VIPLevel) []*Client {
	minL := minLevel.GetLevel()
	var vipClients []*Client
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		if client.GetVIPLevel().GetLevel() >= minL {
			vipClients = append(vipClients, client)
		}
		return true
	})
	return vipClients
}

// ============================================================================
// VIP等级管理
// ============================================================================

// UpgradeVIPLevel 升级用户VIP等级（按 ctx 路由信封 appID+namespace 隔离）
// 使用 ForEachUserClientFiltered 零拷贝遍历（仅升级匹配信封的客户端）+ SetVIPLevel 原子更新，消除数据竞争
func (h *Hub) UpgradeVIPLevel(ctx context.Context, userID string, newLevel VIPLevel) bool {
	if !newLevel.IsValid() {
		return false
	}
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)

	// 快速检查同信封下用户是否存在（O(1)，避免无用户时加锁遍历）
	if !h.shardedRegistry.HasUser(userID, appID, ns) {
		return false
	}

	newLevelVal := newLevel.GetLevel()
	upgraded := false

	// 单次遍历：检查当前等级 + 升级同信封客户端（零拷贝，原子更新）
	h.shardedRegistry.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *Client) bool {
		// 只允许升级，不允许降级
		if newLevelVal > client.GetVIPLevel().GetLevel() {
			client.SetVIPLevel(newLevel)
			upgraded = true
		}
		return true
	})

	return upgraded
}
