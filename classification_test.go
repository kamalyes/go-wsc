/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-21
 * @FilePath: \go-wsc\classification_test.go
 * @Description: 简化的消息分类系统测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"testing"
	"time"
)

// VIPLevel 测试 (V0-V8)
func TestVIPLevel_V_Format(t *testing.T) {
	tests := []struct {
		level    VIPLevel
		valid    bool
		expected int
		name     string
	}{
		{VIPLevelV0, true, 0, "V0-普通用户"},
		{VIPLevelV1, true, 1, "V1"},
		{VIPLevelV2, true, 2, "V2"},
		{VIPLevelV3, true, 3, "V3"},
		{VIPLevelV4, true, 4, "V4"},
		{VIPLevelV5, true, 5, "V5"},
		{VIPLevelV6, true, 6, "V6"},
		{VIPLevelV7, true, 7, "V7"},
		{VIPLevelV8, true, 8, "V8-最高级"},
		{VIPLevel("invalid"), false, 0, "无效等级"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.level.IsValid() != test.valid {
				t.Errorf("VIPLevel.IsValid() = %v, want %v", test.level.IsValid(), test.valid)
			}

			if test.level.GetLevel() != test.expected {
				t.Errorf("VIPLevel.GetLevel() = %d, want %d", test.level.GetLevel(), test.expected)
			}
		})
	}
}

func TestUrgencyLevel_Simplified(t *testing.T) {
	tests := []struct {
		level    UrgencyLevel
		valid    bool
		expected int
		name     string
	}{
		{UrgencyLevelLow, true, 0, "低紧急"},
		{UrgencyLevelNormal, true, 1, "正常"},
		{UrgencyLevelHigh, true, 2, "高紧急"},
		{UrgencyLevel("invalid"), false, 1, "无效等级-默认正常"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.level.IsValid() != test.valid {
				t.Errorf("UrgencyLevel.IsValid() = %v, want %v", test.level.IsValid(), test.valid)
			}

			if test.level.GetLevel() != test.expected {
				t.Errorf("UrgencyLevel.GetLevel() = %d, want %d", test.level.GetLevel(), test.expected)
			}
		})
	}
}

func TestBusinessCategory_Simplified(t *testing.T) {
	tests := []struct {
		category BusinessCategory
		valid    bool
		name     string
	}{
		{BusinessCategoryGeneral, true, "通用"},
		{BusinessCategoryCustomer, true, "客户服务"},
		{BusinessCategorySales, true, "销售"},
		{BusinessCategoryTechnical, true, "技术"},
		{BusinessCategoryFinance, true, "财务"},
		{BusinessCategorySecurity, true, "安全"},
		{BusinessCategoryOperations, true, "运营"},
		{BusinessCategory("invalid"), false, "无效分类"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.category.IsValid() != test.valid {
				t.Errorf("BusinessCategory.IsValid() = %v, want %v", 
					test.category.IsValid(), test.valid)
			}
		})
	}
}

func TestMessageClassification_Simplified(t *testing.T) {
	tests := []struct {
		classification MessageClassification
		expectedScore  int
		isCritical     bool
		name           string
	}{
		{
			MessageClassification{
				Type:             MessageTypeText,
				Priority:         MessagePriorityNormal, // 2*5=10
				VIPLevel:         VIPLevelV0,            // 0*5=0  
				UrgencyLevel:     UrgencyLevelNormal,    // 1*10=10
				BusinessCategory: BusinessCategoryGeneral, // 0
			},
			20, // 10+0+10+0=20
			false,
			"普通消息",
		},
		{
			MessageClassification{
				Type:             MessageTypeAlert,
				Priority:         MessagePriorityCritical, // 5*5=25
				VIPLevel:         VIPLevelV8,              // 8*5=40
				UrgencyLevel:     UrgencyLevelHigh,        // 2*10=20
				BusinessCategory: BusinessCategorySecurity, // 15
			},
			100, // 25+40+20+15=100
			true,
			"V8安全警报",
		},
		{
			MessageClassification{
				Type:             MessageTypeOrder,
				Priority:         MessagePriorityHigh,   // 3*5=15
				VIPLevel:         VIPLevelV5,            // 5*5=25
				UrgencyLevel:     UrgencyLevelNormal,    // 1*10=10
				BusinessCategory: BusinessCategoryFinance, // 10
			},
			60, // 15+25+10+10=60
			false,
			"V5财务订单",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score := test.classification.GetFinalPriority()
			if score != test.expectedScore {
				t.Errorf("GetFinalPriority() = %d, want %d", score, test.expectedScore)
			}

			isCritical := test.classification.IsCriticalMessage()
			if isCritical != test.isCritical {
				t.Errorf("IsCriticalMessage() = %v, want %v", isCritical, test.isCritical)
			}
		})
	}
}

func TestGetAllMethods_Simplified(t *testing.T) {
	// 测试VIP等级数量 (V0-V8 = 9个)
	vipLevels := GetAllVIPLevels()
	if len(vipLevels) != 9 {
		t.Errorf("GetAllVIPLevels() returned %d levels, want 9", len(vipLevels))
	}

	// 测试紧急等级数量 (3个)  
	urgencyLevels := GetAllUrgencyLevels()
	if len(urgencyLevels) != 3 {
		t.Errorf("GetAllUrgencyLevels() returned %d levels, want 3", len(urgencyLevels))
	}

	// 测试业务分类数量 (7个)
	businessCategories := GetAllBusinessCategories()
	if len(businessCategories) != 7 {
		t.Errorf("GetAllBusinessCategories() returned %d categories, want 7", len(businessCategories))
	}
}

func TestVIPLevelComparison_V_Format(t *testing.T) {
	tests := []struct {
		level1   VIPLevel
		level2   VIPLevel
		expected bool
		name     string
	}{
		{VIPLevelV8, VIPLevelV0, true, "V8 > V0"},
		{VIPLevelV5, VIPLevelV3, true, "V5 > V3"},
		{VIPLevelV3, VIPLevelV3, false, "V3 = V3"},
		{VIPLevelV1, VIPLevelV7, false, "V1 < V7"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.level1.IsHigherThan(test.level2)
			if result != test.expected {
				t.Errorf("%s.IsHigherThan(%s) = %v, want %v", 
					test.level1, test.level2, result, test.expected)
			}
		})
	}
}

func BenchmarkSimplifiedClassification(b *testing.B) {
	classification := MessageClassification{
		Type:             MessageTypeAlert,
		Priority:         MessagePriorityUrgent,
		VIPLevel:         VIPLevelV5,
		UrgencyLevel:     UrgencyLevelHigh,
		BusinessCategory: BusinessCategorySecurity,
		Timestamp:        time.Now().Unix(),
		UserID:           "v5_user",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = classification.GetFinalPriority()
	}
}