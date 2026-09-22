/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:52:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:52:00
 * @FilePath: \go-wsc\group\vip.go
 * @Description: VIP 分级发送与查询 —— 从 hub/vip.go 抽出
 *
 * 本域是「按用户分层」的发送策略：VIP 等级决定消息优先级，
 * 分类系统决定最终分值。真正的投递由 Host 的发送路径完成，
 * 这里只做「选谁发、按什么优先级发」的决策。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// VIPManager VIP 分级发送与统计管理器
type VIPManager struct {
	host Host
}

// NewVIPManager 创建 VIP 管理器
func NewVIPManager(host Host) *VIPManager {
	return &VIPManager{host: host}
}

// ============================================================================
// VIP 分级发送
// ============================================================================

// SendToVIPUsers 发送消息给指定VIP等级及以上的用户
func (m *VIPManager) SendToVIPUsers(ctx context.Context, minVIPLevel models.VIPLevel, msg *models.HubMessage) int {
	minLevel := minVIPLevel.GetLevel()
	return m.host.SendConditional(ctx, func(c *models.Client) bool {
		return c.GetVIPLevel().GetLevel() >= minLevel
	}, msg)
}

// SendToExactVIPLevel 发送消息给指定VIP等级用户
func (m *VIPManager) SendToExactVIPLevel(ctx context.Context, vipLevel models.VIPLevel, msg *models.HubMessage) int {
	return m.host.SendConditional(ctx, func(c *models.Client) bool {
		return c.GetVIPLevel() == vipLevel
	}, msg)
}

// SendWithVIPPriority 根据用户VIP等级自动设置消息优先级
// 使用 ForEachUserClientFiltered 零拷贝遍历（按 ctx 信封过滤）+ 提前终止，替代 GetUserClients + 手动迭代
func (m *VIPManager) SendWithVIPPriority(ctx context.Context, userID string, msg *models.HubMessage) {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	// 零拷贝获取第一个匹配信封客户端的VIP等级（提前终止遍历）
	var vipLevel models.VIPLevel
	found := false
	m.host.GetShardedRegistry().ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
		vipLevel = client.GetVIPLevel()
		found = true
		return false // 第一个即终止
	})

	if found {
		// 根据VIP等级自动调整优先级
		level := vipLevel.GetLevel()
		if level >= 6 { // V6-V8
			msg.Priority = models.PriorityHigh
		} else if level >= 3 { // V3-V5
			msg.Priority = models.PriorityNormal
		} else { // V0-V2
			msg.Priority = models.PriorityLow
		}
	}

	m.host.SendToUserWithRetry(ctx, userID, msg)
}

// SendToVIPWithPriority 根据VIP等级优先发送
func (m *VIPManager) SendToVIPWithPriority(ctx context.Context, vipLevel models.VIPLevel, msg *models.HubMessage) int {
	// VIP消息优先级更高
	level := vipLevel.GetLevel()
	if level >= 5 {
		msg.Priority = models.PriorityHigh
	} else if level >= 3 {
		msg.Priority = models.PriorityNormal
	}

	return m.host.SendConditional(ctx, func(c *models.Client) bool {
		return c.GetVIPLevel().GetLevel() >= level
	}, msg)
}

// ============================================================================
// 分类发送
// ============================================================================

// SendToUserWithClassification 使用完整分类系统发送消息
// 发送结果通过 OnMessageSend 回调通知
func (m *VIPManager) SendToUserWithClassification(ctx context.Context, userID string, msg *models.HubMessage, classification *models.MessageClassification) {
	// 设置消息分类信息
	if classification != nil {
		msg.MessageType = classification.Type

		// 根据分类计算优先级
		finalScore := classification.GetFinalPriority()
		if finalScore >= 80 {
			msg.Priority = models.PriorityHigh
		} else if finalScore >= 50 {
			msg.Priority = models.PriorityNormal
		} else {
			msg.Priority = models.PriorityLow
		}

		// 添加分类信息到消息数据中（WithClassification 统一处理 nil Data map）
		msg.WithClassification(classification)
		msg.Data["priority_score"] = finalScore
		msg.Data["is_critical"] = classification.IsCriticalMessage()
	}

	m.host.SendToUserWithRetry(ctx, userID, msg)
}

// ============================================================================
// VIP 统计与查询
// ============================================================================

// GetVIPStatistics 获取VIP用户统计
func (m *VIPManager) GetVIPStatistics() map[string]int {
	stats := make(map[string]int)

	// 统计各VIP等级用户数量
	for _, level := range models.GetAllVIPLevels() {
		stats[string(level)] = 0
	}

	// shardedRegistry 遍历所有客户端（原子读 VIPLevel，并发安全）
	m.host.GetShardedRegistry().ForEachClient(func(_ string, client *models.Client) bool {
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
func (m *VIPManager) FilterVIPClients(minLevel models.VIPLevel) []*models.Client {
	minL := minLevel.GetLevel()
	var vipClients []*models.Client
	m.host.GetShardedRegistry().ForEachClient(func(_ string, client *models.Client) bool {
		if client.GetVIPLevel().GetLevel() >= minL {
			vipClients = append(vipClients, client)
		}
		return true
	})
	return vipClients
}

// ============================================================================
// VIP 等级管理
// ============================================================================

// UpgradeVIPLevel 升级用户VIP等级（按 ctx 路由信封 appID+namespace 隔离）
// 使用 ForEachUserClientFiltered 零拷贝遍历（仅升级匹配信封的客户端）+ SetVIPLevel 原子更新，消除数据竞争
func (m *VIPManager) UpgradeVIPLevel(ctx context.Context, userID string, newLevel models.VIPLevel) bool {
	if !newLevel.IsValid() {
		return false
	}
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)

	// 快速检查同信封下用户是否存在（O(1)，避免无用户时加锁遍历）
	if !m.host.GetShardedRegistry().HasUser(userID, appID, ns) {
		return false
	}

	newLevelVal := newLevel.GetLevel()
	upgraded := false

	// 单次遍历：检查当前等级 + 升级同信封客户端（零拷贝，原子更新）
	m.host.GetShardedRegistry().ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
		// 只允许升级，不允许降级
		if newLevelVal > client.GetVIPLevel().GetLevel() {
			client.SetVIPLevel(newLevel)
			upgraded = true
		}
		return true
	})

	return upgraded
}
