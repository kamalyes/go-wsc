/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\classification.go
 * @Description: 消息分类相关定义
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import (
	"fmt"
)

// MessageClassification 消息综合分类
type MessageClassification struct {
	Type             MessageType      `json:"type"`
	Priority         MessagePriority  `json:"priority"`
	VIPLevel         VIPLevel         `json:"vip_level"`
	UrgencyLevel     UrgencyLevel     `json:"urgency_level"`
	BusinessCategory BusinessCategory `json:"business_category"`
	Timestamp        int64            `json:"timestamp"`
	UserID           string           `json:"user_id,omitempty"`
}

// GetFinalPriority 获取综合优先级分数 (0-100)
func (mc *MessageClassification) GetFinalPriority() int {
	// 基础优先级分数 (0-25)
	basePriority := mc.Priority.GetWeight() * 5

	// VIP等级加分 (0-40)
	vipBonus := mc.VIPLevel.GetLevel() * 5

	// 紧急等级加分 (0-20)
	urgencyBonus := mc.UrgencyLevel.GetLevel() * 10

	// 业务分类加分 (0-15)
	var categoryBonus int
	switch mc.BusinessCategory {
	case BusinessCategorySecurity:
		categoryBonus = 15
	case BusinessCategoryFinance:
		categoryBonus = 10
	case BusinessCategoryCustomer:
		categoryBonus = 8
	case BusinessCategoryTechnical:
		categoryBonus = 5
	case BusinessCategorySales:
		categoryBonus = 3
	default:
		categoryBonus = 0
	}

	totalScore := basePriority + vipBonus + urgencyBonus + categoryBonus

	// 确保分数在50-100之间
	if totalScore > 100 {
		return 100
	}
	return totalScore
}

// GetDisplayName 获取分类显示名称
func (mc *MessageClassification) GetDisplayName() string {
	return fmt.Sprintf("[%s][%s][%s][%s]",
		mc.Type,
		mc.Priority,
		mc.VIPLevel,
		mc.UrgencyLevel,
	)
}

// IsHighPriorityMessage 判断是否为高优先级消息
func (mc *MessageClassification) IsHighPriorityMessage() bool {
	return mc.GetFinalPriority() >= 70
}

// IsCriticalMessage 判断是否为关键消息
func (mc *MessageClassification) IsCriticalMessage() bool {
	return mc.GetFinalPriority() >= 90 ||
		mc.Priority == MessagePriorityCritical ||
		mc.UrgencyLevel == UrgencyLevelHigh ||
		mc.BusinessCategory.GetCategoryType() == "security"
}
