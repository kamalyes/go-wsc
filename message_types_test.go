/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-21
 * @FilePath: \go-wsc\message_types_test.go
 * @Description: MessageType 测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMessageType_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected bool
	}{
		{"文本消息", MessageTypeText, true},
		{"图片消息", MessageTypeImage, true},
		{"位置消息", MessageTypeLocation, true},
		{"卡片消息", MessageTypeCard, true},
		{"Markdown消息", MessageTypeMarkdown, true},
		{"自定义消息", MessageTypeCustom, true},
		{"无效类型", MessageType("invalid"), false},
		{"空类型", MessageType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.IsValid())
		})
	}
}

func TestMessageType_IsMediaType(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected bool
	}{
		{"图片", MessageTypeImage, true},
		{"音频", MessageTypeAudio, true},
		{"视频", MessageTypeVideo, true},
		{"文件", MessageTypeFile, true},
		{"语音", MessageTypeVoice, true},
		{"GIF", MessageTypeGIF, true},
		{"文档", MessageTypeDocument, true},
		{"电子表格", MessageTypeSpreadsheet, true},
		{"演示文稿", MessageTypePresentation, true},
		{"文本", MessageTypeText, false},
		{"系统", MessageTypeSystem, false},
		{"卡片", MessageTypeCard, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.IsMediaType())
		})
	}
}

func TestMessageType_IsTextType(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected bool
	}{
		{"文本", MessageTypeText, true},
		{"Markdown", MessageTypeMarkdown, true},
		{"富文本", MessageTypeRichText, true},
		{"代码", MessageTypeCode, true},
		{"图片", MessageTypeImage, false},
		{"系统", MessageTypeSystem, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.IsTextType())
		})
	}
}

func TestMessageType_IsSystemType(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected bool
	}{
		{"系统", MessageTypeSystem, true},
		{"通知", MessageTypeNotice, true},
		{"事件", MessageTypeEvent, true},
		{"公告", MessageTypeAnnouncement, true},
		{"警告", MessageTypeAlert, true},
		{"错误", MessageTypeError, true},
		{"信息", MessageTypeInfo, true},
		{"成功", MessageTypeSuccess, true},
		{"警告", MessageTypeWarning, true},
		{"心跳", MessageTypeHeartbeat, true},
		{"Ping", MessageTypePing, true},
		{"Pong", MessageTypePong, true},
		{"文本", MessageTypeText, false},
		{"图片", MessageTypeImage, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.IsSystemType())
		})
	}
}

func TestMessageType_IsInteractiveType(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected bool
	}{
		{"卡片", MessageTypeCard, true},
		{"链接", MessageTypeLink, true},
		{"引用", MessageTypeQuote, true},
		{"命令", MessageTypeCommand, true},
		{"投票", MessageTypePoll, true},
		{"表单", MessageTypeForm, true},
		{"任务", MessageTypeTask, true},
		{"邀请", MessageTypeInvite, true},
		{"文本", MessageTypeText, false},
		{"系统", MessageTypeSystem, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.IsInteractiveType())
		})
	}
}

func TestMessageType_IsStatusType(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected bool
	}{
		{"正在输入", MessageTypeTyping, true},
		{"已读", MessageTypeRead, true},
		{"已送达", MessageTypeDelivered, true},
		{"确认", MessageTypeAck, true},
		{"文本", MessageTypeText, false},
		{"系统", MessageTypeSystem, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.IsStatusType())
		})
	}
}

func TestMessageType_GetCategory(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected string
	}{
		{"图片分类", MessageTypeImage, "media"},
		{"文本分类", MessageTypeText, "text"},
		{"系统分类", MessageTypeSystem, "system"},
		{"卡片分类", MessageTypeCard, "interactive"},
		{"状态分类", MessageTypeTyping, "status"},
		{"其他分类", MessageTypeLocation, "other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.GetCategory())
		})
	}
}

func TestGetAllMessageTypes(t *testing.T) {
	allTypes := GetAllMessageTypes()
	
	// 检查总数
	assert.Greater(t, len(allTypes), 50, "应该有50+种消息类型")
	
	// 检查包含基本类型
	assert.Contains(t, allTypes, MessageTypeText)
	assert.Contains(t, allTypes, MessageTypeImage)
	assert.Contains(t, allTypes, MessageTypeVideo)
	assert.Contains(t, allTypes, MessageTypeSystem)
	
	// 检查包含新类型
	assert.Contains(t, allTypes, MessageTypeLocation)
	assert.Contains(t, allTypes, MessageTypeMarkdown)
	assert.Contains(t, allTypes, MessageTypeReaction)
	assert.Contains(t, allTypes, MessageTypeCustom)
}

func TestGetMessageTypesByCategory(t *testing.T) {
	tests := []struct {
		name             string
		category         string
		expectedContains []MessageType
		minCount         int
	}{
		{
			name:             "媒体类型",
			category:         "media",
			expectedContains: []MessageType{MessageTypeImage, MessageTypeAudio, MessageTypeVideo},
			minCount:         5,
		},
		{
			name:             "文本类型",
			category:         "text",
			expectedContains: []MessageType{MessageTypeText, MessageTypeMarkdown},
			minCount:         3,
		},
		{
			name:             "系统类型",
			category:         "system",
			expectedContains: []MessageType{MessageTypeSystem, MessageTypeNotice, MessageTypeHeartbeat},
			minCount:         10,
		},
		{
			name:             "交互类型",
			category:         "interactive",
			expectedContains: []MessageType{MessageTypeCard, MessageTypeLink, MessageTypePoll},
			minCount:         5,
		},
		{
			name:             "状态类型",
			category:         "status",
			expectedContains: []MessageType{MessageTypeTyping, MessageTypeRead, MessageTypeAck},
			minCount:         3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			types := GetMessageTypesByCategory(tt.category)
			
			assert.GreaterOrEqual(t, len(types), tt.minCount, "类型数量不足")
			
			for _, expectedType := range tt.expectedContains {
				assert.Contains(t, types, expectedType, "应该包含类型: %s", expectedType)
			}
		})
	}
}

func TestMessageType_String(t *testing.T) {
	tests := []struct {
		msgType  MessageType
		expected string
	}{
		{MessageTypeText, "text"},
		{MessageTypeImage, "image"},
		{MessageTypeLocation, "location"},
		{MessageTypeMarkdown, "markdown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.msgType), func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.String())
		})
	}
}

// 基准测试
func BenchmarkMessageType_IsValid(b *testing.B) {
	msgType := MessageTypeText
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgType.IsValid()
	}
}

func BenchmarkMessageType_GetCategory(b *testing.B) {
	msgType := MessageTypeImage
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgType.GetCategory()
	}
}

func BenchmarkGetAllMessageTypes(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GetAllMessageTypes()
	}
}

// TestMessagePriority 测试消息优先级
func TestMessagePriority_String(t *testing.T) {
	tests := []struct {
		priority MessagePriority
		expected string
	}{
		{MessagePriorityLow, "low"},
		{MessagePriorityNormal, "normal"},
		{MessagePriorityHigh, "high"},
		{MessagePriorityUrgent, "urgent"},
		{MessagePriorityCritical, "critical"},
		{MessagePriority(999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.priority.String())
		})
	}
}

func TestMessagePriority_GetWeight(t *testing.T) {
	tests := []struct {
		name     string
		priority MessagePriority
		expected int
	}{
		{"低优先级", MessagePriorityLow, 1},
		{"普通优先级", MessagePriorityNormal, 2},
		{"高优先级", MessagePriorityHigh, 3},
		{"紧急优先级", MessagePriorityUrgent, 4},
		{"关键优先级", MessagePriorityCritical, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.priority.GetWeight())
		})
	}
}

func TestMessagePriority_IsHigherThan(t *testing.T) {
	tests := []struct {
		name     string
		p1       MessagePriority
		p2       MessagePriority
		expected bool
	}{
		{"关键 > 紧急", MessagePriorityCritical, MessagePriorityUrgent, true},
		{"紧急 > 高", MessagePriorityUrgent, MessagePriorityHigh, true},
		{"高 > 普通", MessagePriorityHigh, MessagePriorityNormal, true},
		{"普通 > 低", MessagePriorityNormal, MessagePriorityLow, true},
		{"低 < 普通", MessagePriorityLow, MessagePriorityNormal, false},
		{"相同优先级", MessagePriorityHigh, MessagePriorityHigh, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.p1.IsHigherThan(tt.p2))
		})
	}
}

func TestMessageType_GetDefaultPriority(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected MessagePriority
	}{
		// 关键优先级
		{"错误消息", MessageTypeError, MessagePriorityCritical},
		{"警告消息", MessageTypeAlert, MessagePriorityCritical},
		
		// 紧急优先级
		{"系统消息", MessageTypeSystem, MessagePriorityUrgent},
		{"公告消息", MessageTypeAnnouncement, MessagePriorityUrgent},
		{"警告消息", MessageTypeWarning, MessagePriorityUrgent},
		
		// 高优先级
		{"通知消息", MessageTypeNotice, MessagePriorityHigh},
		{"事件消息", MessageTypeEvent, MessagePriorityHigh},
		{"成功消息", MessageTypeSuccess, MessagePriorityHigh},
		{"支付消息", MessageTypePayment, MessagePriorityHigh},
		{"订单消息", MessageTypeOrder, MessagePriorityHigh},
		{"邀请消息", MessageTypeInvite, MessagePriorityHigh},
		{"任务消息", MessageTypeTask, MessagePriorityHigh},
		{"撤回消息", MessageTypeRecall, MessagePriorityHigh},
		
		// 普通优先级
		{"文本消息", MessageTypeText, MessagePriorityNormal},
		{"图片消息", MessageTypeImage, MessagePriorityNormal},
		{"音频消息", MessageTypeAudio, MessagePriorityNormal},
		{"视频消息", MessageTypeVideo, MessagePriorityNormal},
		{"文件消息", MessageTypeFile, MessagePriorityNormal},
		{"卡片消息", MessageTypeCard, MessagePriorityNormal},
		{"链接消息", MessageTypeLink, MessagePriorityNormal},
		{"位置消息", MessageTypeLocation, MessagePriorityNormal},
		
		// 低优先级
		{"正在输入", MessageTypeTyping, MessagePriorityLow},
		{"已读消息", MessageTypeRead, MessagePriorityLow},
		{"已送达", MessageTypeDelivered, MessagePriorityLow},
		{"确认消息", MessageTypeAck, MessagePriorityLow},
		{"心跳消息", MessageTypeHeartbeat, MessagePriorityLow},
		{"Ping消息", MessageTypePing, MessagePriorityLow},
		{"Pong消息", MessageTypePong, MessagePriorityLow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.msgType.GetDefaultPriority())
		})
	}
}

func TestGetMessageTypesByPriority(t *testing.T) {
	tests := []struct {
		name             string
		priority         MessagePriority
		expectedContains []MessageType
		minCount         int
	}{
		{
			name:             "关键优先级",
			priority:         MessagePriorityCritical,
			expectedContains: []MessageType{MessageTypeError, MessageTypeAlert},
			minCount:         2,
		},
		{
			name:             "紧急优先级",
			priority:         MessagePriorityUrgent,
			expectedContains: []MessageType{MessageTypeSystem, MessageTypeAnnouncement, MessageTypeWarning},
			minCount:         3,
		},
		{
			name:             "高优先级",
			priority:         MessagePriorityHigh,
			expectedContains: []MessageType{MessageTypeNotice, MessageTypePayment, MessageTypeOrder},
			minCount:         5,
		},
		{
			name:             "普通优先级",
			priority:         MessagePriorityNormal,
			expectedContains: []MessageType{MessageTypeText, MessageTypeImage, MessageTypeVideo},
			minCount:         15,
		},
		{
			name:             "低优先级",
			priority:         MessagePriorityLow,
			expectedContains: []MessageType{MessageTypeTyping, MessageTypeRead, MessageTypeHeartbeat},
			minCount:         10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			types := GetMessageTypesByPriority(tt.priority)
			
			assert.GreaterOrEqual(t, len(types), tt.minCount, "类型数量不足")
			
			for _, expectedType := range tt.expectedContains {
				assert.Contains(t, types, expectedType, "应该包含类型: %s", expectedType)
			}

			// 验证所有返回的类型确实属于指定优先级
			for _, msgType := range types {
				assert.Equal(t, tt.priority, msgType.GetDefaultPriority(),
					"类型 %s 的优先级不匹配", msgType)
			}
		})
	}
}

func TestGetPriorityStats(t *testing.T) {
	stats := GetPriorityStats()

	// 基本验证
	assert.NotNil(t, stats)
	assert.Greater(t, stats.Total, 0, "总数应该大于0")
	
	// 验证各优先级都有消息类型
	assert.Greater(t, stats.Critical, 0, "应该有关键优先级消息")
	assert.Greater(t, stats.Urgent, 0, "应该有紧急优先级消息")
	assert.Greater(t, stats.High, 0, "应该有高优先级消息")
	assert.Greater(t, stats.Normal, 0, "应该有普通优先级消息")
	assert.Greater(t, stats.Low, 0, "应该有低优先级消息")

	// 验证总数等于各优先级之和
	calculatedTotal := stats.Critical + stats.Urgent + stats.High + stats.Normal + stats.Low
	assert.Equal(t, calculatedTotal, stats.Total, "总数应该等于各优先级之和")

	// 验证总数与所有消息类型数量一致
	allTypes := GetAllMessageTypes()
	assert.Equal(t, len(allTypes), stats.Total, "总数应该与所有消息类型数量一致")

	t.Logf("优先级统计: Critical=%d, Urgent=%d, High=%d, Normal=%d, Low=%d, Total=%d",
		stats.Critical, stats.Urgent, stats.High, stats.Normal, stats.Low, stats.Total)
}

// 基准测试 - 优先级相关
func BenchmarkMessageType_GetDefaultPriority(b *testing.B) {
	msgType := MessageTypeText
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgType.GetDefaultPriority()
	}
}

func BenchmarkGetMessageTypesByPriority(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GetMessageTypesByPriority(MessagePriorityNormal)
	}
}

func BenchmarkGetPriorityStats(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GetPriorityStats()
	}
}
