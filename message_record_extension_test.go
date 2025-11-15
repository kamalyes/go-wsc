/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-15
 * @FilePath: \go-wsc\message_record_extension_test.go
 * @Description: 消息记录扩展功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCustomFields 测试自定义字段
func TestCustomFields(t *testing.T) {
	t.Run("设置和获取自定义字段", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		msg := &HubMessage{ID: "test-1", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))

		// 设置自定义字段
		success := mrm.SetRecordCustomField("test-1", "business_type", "order")
		assert.True(t, success)

		success = mrm.SetRecordCustomField("test-1", "priority", 10)
		assert.True(t, success)

		// 获取自定义字段
		value, exists := mrm.GetRecordCustomField("test-1", "business_type")
		assert.True(t, exists)
		assert.Equal(t, "order", value.(string))

		value, exists = mrm.GetRecordCustomField("test-1", "priority")
		assert.True(t, exists)
		assert.Equal(t, 10, value.(int))

		// 不存在的字段
		_, exists = mrm.GetRecordCustomField("test-1", "not_exist")
		assert.False(t, exists)
	})

	t.Run("不存在的记录", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		success := mrm.SetRecordCustomField("not-exist", "key", "value")
		assert.False(t, success)

		_, exists := mrm.GetRecordCustomField("not-exist", "key")
		assert.False(t, exists)
	})
}

// TestTags 测试标签功能
func TestTags(t *testing.T) {
	t.Run("添加和查询标签", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		msg := &HubMessage{ID: "test-1", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))

		// 添加标签
		success := mrm.AddRecordTag("test-1", "urgent")
		assert.True(t, success)

		success = mrm.AddRecordTag("test-1", "payment")
		assert.True(t, success)

		// 重复添加
		success = mrm.AddRecordTag("test-1", "urgent")
		assert.True(t, success)

		record, _ := mrm.GetRecord("test-1")
		assert.Contains(t, record.Tags, "urgent")
		assert.Contains(t, record.Tags, "payment")
		assert.Equal(t, 2, len(record.Tags)) // 不应该重复
	})

	t.Run("移除标签", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		msg := &HubMessage{ID: "test-2", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))

		mrm.AddRecordTag("test-2", "tag1")
		mrm.AddRecordTag("test-2", "tag2")
		mrm.AddRecordTag("test-2", "tag3")

		success := mrm.RemoveRecordTag("test-2", "tag2")
		assert.True(t, success)

		record, _ := mrm.GetRecord("test-2")
		assert.NotContains(t, record.Tags, "tag2")
		assert.Contains(t, record.Tags, "tag1")
		assert.Contains(t, record.Tags, "tag3")

		// 移除不存在的标签
		success = mrm.RemoveRecordTag("test-2", "not-exist")
		assert.False(t, success)
	})

	t.Run("按标签查询", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		// 创建多条记录
		for i := 0; i < 5; i++ {
			msg := &HubMessage{ID: string(rune('a' + i)), Type: MessageTypeText}
			mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		}

		mrm.AddRecordTag("a", "urgent")
		mrm.AddRecordTag("b", "urgent")
		mrm.AddRecordTag("c", "normal")
		mrm.AddRecordTag("d", "urgent")
		mrm.AddRecordTag("e", "normal")

		urgentRecords := mrm.GetRecordsByTag("urgent")
		assert.Equal(t, 3, len(urgentRecords))

		normalRecords := mrm.GetRecordsByTag("normal")
		assert.Equal(t, 2, len(normalRecords))

		notExistRecords := mrm.GetRecordsByTag("not-exist")
		assert.Equal(t, 0, len(notExistRecords))
	})
}

// MockHooks 模拟钩子
type MockHooks struct {
	CreatedCount  int
	UpdatedCount  int
	RetryCount    int
	DeletedCount  int
	ExpiredCount  int
	LastOldStatus MessageSendStatus
	LastNewStatus MessageSendStatus
}

func (h *MockHooks) OnRecordCreated(record *MessageSendRecord) error {
	h.CreatedCount++
	return nil
}

func (h *MockHooks) OnRecordUpdated(record *MessageSendRecord, oldStatus, newStatus MessageSendStatus) error {
	h.UpdatedCount++
	h.LastOldStatus = oldStatus
	h.LastNewStatus = newStatus
	return nil
}

func (h *MockHooks) OnRetryAttempt(record *MessageSendRecord, attempt *RetryAttempt) error {
	h.RetryCount++
	return nil
}

func (h *MockHooks) OnRecordDeleted(record *MessageSendRecord) error {
	h.DeletedCount++
	return nil
}

func (h *MockHooks) OnRecordExpired(record *MessageSendRecord) error {
	h.ExpiredCount++
	return nil
}

// TestHooks 测试钩子函数
func TestHooks(t *testing.T) {
	t.Run("创建记录钩子", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		hooks := &MockHooks{}
		mrm.SetHooks(hooks)

		msg := &HubMessage{ID: "test-1", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))

		assert.Equal(t, 1, hooks.CreatedCount)
	})

	t.Run("更新记录钩子", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		hooks := &MockHooks{}
		mrm.SetHooks(hooks)

		msg := &HubMessage{ID: "test-2", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))

		mrm.UpdateRecordStatus("test-2", MessageSendStatusSending, "", "")
		assert.Equal(t, 1, hooks.UpdatedCount)
		assert.Equal(t, MessageSendStatusPending, hooks.LastOldStatus)
		assert.Equal(t, MessageSendStatusSending, hooks.LastNewStatus)

		mrm.UpdateRecordStatus("test-2", MessageSendStatusSuccess, "", "")
		assert.Equal(t, 2, hooks.UpdatedCount)
		assert.Equal(t, MessageSendStatusSending, hooks.LastOldStatus)
		assert.Equal(t, MessageSendStatusSuccess, hooks.LastNewStatus)
	})

	t.Run("重试尝试钩子", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		hooks := &MockHooks{}
		mrm.SetHooks(hooks)

		msg := &HubMessage{ID: "test-3", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))

		mrm.RecordRetryAttempt("test-3", 1, 100*time.Millisecond, nil, false)
		assert.Equal(t, 1, hooks.RetryCount)

		mrm.RecordRetryAttempt("test-3", 2, 120*time.Millisecond, nil, true)
		assert.Equal(t, 2, hooks.RetryCount)
	})

	t.Run("过期和删除钩子", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, 10*time.Millisecond, nil)
		hooks := &MockHooks{}
		mrm.SetHooks(hooks)

		msg := &HubMessage{ID: "test-4", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(-time.Hour))

		time.Sleep(20 * time.Millisecond)
		cleaned := mrm.CleanupExpiredRecords()

		assert.Equal(t, 1, cleaned)
		assert.Equal(t, 1, hooks.ExpiredCount)
		assert.Equal(t, 1, hooks.DeletedCount)
	})
}

// MockFilter 模拟过滤器
type StatusFilter struct {
	Status MessageSendStatus
}

func (f *StatusFilter) Match(record *MessageSendRecord) bool {
	return record.Status == f.Status
}

type TagFilter struct {
	Tag string
}

func (f *TagFilter) Match(record *MessageSendRecord) bool {
	for _, tag := range record.Tags {
		if tag == f.Tag {
			return true
		}
	}
	return false
}

// TestCustomFilters 测试自定义过滤器
func TestCustomFilters(t *testing.T) {
	t.Run("状态过滤器", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		// 创建不同状态的记录
		for i := 0; i < 5; i++ {
			msg := &HubMessage{ID: string(rune('a' + i)), Type: MessageTypeText}
			mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		}

		mrm.UpdateRecordStatus("a", MessageSendStatusSuccess, "", "")
		mrm.UpdateRecordStatus("b", MessageSendStatusFailed, FailureReasonNetworkError, "")
		mrm.UpdateRecordStatus("c", MessageSendStatusSuccess, "", "")
		mrm.UpdateRecordStatus("d", MessageSendStatusFailed, FailureReasonAckTimeout, "")
		mrm.UpdateRecordStatus("e", MessageSendStatusRetrying, "", "")

		filter := &StatusFilter{Status: MessageSendStatusSuccess}
		results := mrm.QueryRecords(filter)
		assert.Equal(t, 2, len(results))

		filter = &StatusFilter{Status: MessageSendStatusFailed}
		results = mrm.QueryRecords(filter)
		assert.Equal(t, 2, len(results))
	})

	t.Run("标签过滤器", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		for i := 0; i < 3; i++ {
			msg := &HubMessage{ID: string(rune('a' + i)), Type: MessageTypeText}
			mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		}

		mrm.AddRecordTag("a", "urgent")
		mrm.AddRecordTag("b", "normal")
		mrm.AddRecordTag("c", "urgent")

		filter := &TagFilter{Tag: "urgent"}
		results := mrm.QueryRecords(filter)
		assert.Equal(t, 2, len(results))
	})

	t.Run("添加过滤器到管理器", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		filter1 := &StatusFilter{Status: MessageSendStatusFailed}
		filter2 := &TagFilter{Tag: "urgent"}

		mrm.AddFilter(filter1)
		mrm.AddFilter(filter2)

		// 过滤器已添加（实际使用中可以通过QueryRecords使用）
		assert.NotNil(t, mrm.filters)
		assert.Equal(t, 2, len(mrm.filters))
	})
}

// TestCustomHandlers 测试自定义处理器
func TestCustomHandlers(t *testing.T) {
	t.Run("设置和获取处理器", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		type MyHandler struct {
			Name string
		}

		handler := &MyHandler{Name: "TestHandler"}
		mrm.SetCustomHandler("my_handler", handler)

		retrieved, exists := mrm.GetCustomHandler("my_handler")
		assert.True(t, exists)
		assert.NotNil(t, retrieved)

		if h, ok := retrieved.(*MyHandler); ok {
			assert.Equal(t, "TestHandler", h.Name)
		}
	})

	t.Run("不存在的处理器", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		_, exists := mrm.GetCustomHandler("not_exist")
		assert.False(t, exists)
	})

	t.Run("多个处理器", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		mrm.SetCustomHandler("handler1", "value1")
		mrm.SetCustomHandler("handler2", 123)
		mrm.SetCustomHandler("handler3", true)

		val1, exists := mrm.GetCustomHandler("handler1")
		assert.True(t, exists)
		assert.Equal(t, "value1", val1.(string))

		val2, exists := mrm.GetCustomHandler("handler2")
		assert.True(t, exists)
		assert.Equal(t, 123, val2.(int))

		val3, exists := mrm.GetCustomHandler("handler3")
		assert.True(t, exists)
		assert.Equal(t, true, val3.(bool))
	})
}

// TestExtraData 测试ExtraData字段
func TestExtraData(t *testing.T) {
	t.Run("存储复杂对象", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		type OrderInfo struct {
			OrderID   string
			Amount    float64
			ProductID string
		}

		msg := &HubMessage{ID: "test-1", Type: MessageTypeText}
		record := mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))

		// 设置ExtraData
		record.ExtraData = OrderInfo{
			OrderID:   "ORD-001",
			Amount:    99.99,
			ProductID: "PROD-123",
		}

		// 读取ExtraData
		retrievedRecord, _ := mrm.GetRecord("test-1")
		if orderInfo, ok := retrievedRecord.ExtraData.(OrderInfo); ok {
			assert.Equal(t, "ORD-001", orderInfo.OrderID)
			assert.Equal(t, 99.99, orderInfo.Amount)
			assert.Equal(t, "PROD-123", orderInfo.ProductID)
		} else {
			t.Fatal("ExtraData type assertion failed")
		}
	})
}
