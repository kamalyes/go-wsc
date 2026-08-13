/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-26 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-26 00:00:00
 * @FilePath: \go-wsc\models\message_test.go
 * @Description: HubMessage 结构体测试
 *   - 字段对齐验证（无 padding）
 *   - JSON 序列化兼容性
 *   - Setter 链式调用
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"encoding/json"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubMessage_SizeAndAlignment 验证 HubMessage 结构体大小与字段对齐
// 优化后：3 个 bool 字段集中在末尾，避免散落导致的 padding
// 期望大小：368 字节（20 个 string*16 + map*8 + time.Time*24 + int64*8 + 3 bool + padding）
// 新增 TraceID string 字段（16 字节），从 352 → 368
func TestHubMessage_SizeAndAlignment(t *testing.T) {
	size := unsafe.Sizeof(HubMessage{})

	// 368 是新增 TraceID 后的预期大小
	// 由于 Go 版本/平台差异，允许 ±8 字节浮动，但必须 < 376
	assert.Less(t, size, uintptr(376), "HubMessage size should be optimized (< 376 bytes), got %d", size)

	// 验证 3 个 bool 字段位于结构体末尾（地址偏移接近 size）
	msg := HubMessage{}
	ackOffset := unsafe.Offsetof(msg.RequireAck)
	skipDBOffset := unsafe.Offsetof(msg.SkipDatabaseStorage)
	skipSendOffset := unsafe.Offsetof(msg.SkipSendToClient)

	// 三个 bool 字段应该是连续的（offset 相差 1）
	assert.Equal(t, ackOffset+1, skipDBOffset, "RequireAck and SkipDatabaseStorage should be adjacent")
	assert.Equal(t, skipDBOffset+1, skipSendOffset, "SkipDatabaseStorage and SkipSendToClient should be adjacent")

	// bool 字段应位于结构体尾部（3 个 bool 占 3 字节 + padding，offset 应 >= size-8）
	assert.GreaterOrEqual(t, ackOffset, uintptr(size-8), "RequireAck should be near the end of struct (offset=%d, size=%d)", ackOffset, size)
}

// TestHubMessage_JSONSerializationCompatibility 验证字段重排后 JSON 序列化兼容
func TestHubMessage_JSONSerializationCompatibility(t *testing.T) {
	msg := NewHubMessage().
		SetID("test-id").
		SetMessageType(MessageTypeText).
		SetSender("user-a").
		SetSenderName("User A").
		SetSenderType(UserTypeCustomer).
		SetReceiver("user-b").
		SetReceiverType(UserTypeCustomer).
		SetReceiverClient("client-2").
		SetReceiverNode("node-1").
		SetSessionID("session-1").
		SetContent("hello").
		SetMessageID("msg-1").
		SetSeqNo(100).
		SetPriority(PriorityHigh).
		SetReplyToMsgID("reply-1").
		SetRequireAck(true).
		SetPushType(PushTypeDirect).
		SetBroadcastType(BroadcastTypeNone).
		SetSkipDatabaseStorage(false).
		SetSkipSendToClient(false)
	msg.SenderClient = "client-1" // 直接赋值（无 setter）

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// 反序列化验证
	var decoded HubMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.ID, decoded.ID)
	assert.Equal(t, msg.MessageType, decoded.MessageType)
	assert.Equal(t, msg.Sender, decoded.Sender)
	assert.Equal(t, msg.SenderName, decoded.SenderName)
	assert.Equal(t, msg.SenderType, decoded.SenderType)
	assert.Equal(t, msg.SenderClient, decoded.SenderClient)
	assert.Equal(t, msg.Receiver, decoded.Receiver)
	assert.Equal(t, msg.ReceiverType, decoded.ReceiverType)
	assert.Equal(t, msg.ReceiverClient, decoded.ReceiverClient)
	assert.Equal(t, msg.ReceiverNode, decoded.ReceiverNode)
	assert.Equal(t, msg.SessionID, decoded.SessionID)
	assert.Equal(t, msg.Content, decoded.Content)
	assert.Equal(t, msg.MessageID, decoded.MessageID)
	assert.Equal(t, msg.SeqNo, decoded.SeqNo)
	assert.Equal(t, msg.Priority, decoded.Priority)
	assert.Equal(t, msg.ReplyToMsgID, decoded.ReplyToMsgID)
	assert.Equal(t, msg.RequireAck, decoded.RequireAck)
	assert.Equal(t, msg.PushType, decoded.PushType)
	assert.Equal(t, msg.BroadcastType, decoded.BroadcastType)
}

// TestHubMessage_JSONTagsPreserved 验证 Priority 无 omitempty（保持原行为）
// 其他策略字段（Source/PushType/BroadcastType）保持 omitempty
func TestHubMessage_JSONTagsPreserved(t *testing.T) {
	// 空消息 - Priority 应该被序列化（即使为空），其他策略字段应被省略
	msg := HubMessage{}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var m map[string]interface{}
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)

	// Priority 不带 omitempty，应始终存在
	_, ok := m["priority"]
	assert.True(t, ok, "priority field should always be serialized (no omitempty)")

	// Source 带 omitempty，空值应被省略
	_, ok = m["source"]
	assert.False(t, ok, "source field should be omitted when empty")

	// PushType 带 omitempty，空值应被省略
	_, ok = m["push_type"]
	assert.False(t, ok, "push_type field should be omitted when empty")

	// BroadcastType 带 omitempty，空值应被省略
	_, ok = m["broadcast_type"]
	assert.False(t, ok, "broadcast_type field should be omitted when empty")
}

// TestHubMessage_SetterChain 验证所有 setter 方法的链式调用
func TestHubMessage_SetterChain(t *testing.T) {
	msg := NewHubMessage().
		SetID("id-1").
		SetMessageType(MessageTypeText).
		SetSender("sender").
		SetSenderName("Sender Name").
		SetSenderType(UserTypeCustomer).
		SetReceiver("receiver").
		SetReceiverName("Receiver Name").
		SetReceiverType(UserTypeAdmin).
		SetReceiverClient("client-id").
		SetReceiverNode("node-id").
		SetSessionID("session").
		SetContent("content").
		SetMessageID("msg-id").
		SetSeqNo(42).
		SetPriority(PriorityNormal).
		SetReplyToMsgID("reply-id").
		SetRequireAck(true).
		SetPushType(PushTypeDirect).
		SetBroadcastType(BroadcastTypeNone).
		SetSkipDatabaseStorage(true).
		SetSkipSendToClient(true)

	assert.Equal(t, "id-1", msg.ID)
	assert.Equal(t, MessageTypeText, msg.MessageType)
	assert.Equal(t, "sender", msg.Sender)
	assert.Equal(t, "Sender Name", msg.SenderName)
	assert.Equal(t, UserTypeCustomer, msg.SenderType)
	assert.Equal(t, "receiver", msg.Receiver)
	assert.Equal(t, "Receiver Name", msg.ReceiverName)
	assert.Equal(t, UserTypeAdmin, msg.ReceiverType)
	assert.Equal(t, "client-id", msg.ReceiverClient)
	assert.Equal(t, "node-id", msg.ReceiverNode)
	assert.Equal(t, "session", msg.SessionID)
	assert.Equal(t, "content", msg.Content)
	assert.Equal(t, "msg-id", msg.MessageID)
	assert.Equal(t, int64(42), msg.SeqNo)
	assert.Equal(t, PriorityNormal, msg.Priority)
	assert.Equal(t, "reply-id", msg.ReplyToMsgID)
	assert.True(t, msg.RequireAck)
	assert.Equal(t, PushTypeDirect, msg.PushType)
	assert.Equal(t, BroadcastTypeNone, msg.BroadcastType)
	assert.True(t, msg.SkipDatabaseStorage)
	assert.True(t, msg.SkipSendToClient)
}

// TestHubMessage_Clone 验证 Clone 后字段一致性
func TestHubMessage_Clone(t *testing.T) {
	original := NewHubMessage().
		SetID("orig-id").
		SetSender("user-a").
		SetReceiver("user-b").
		SetContent("hello world").
		WithContentExtra("key", "value").
		WithMetadata("trace-id", "abc-123")

	cloned := original.Clone()

	assert.Equal(t, original.ID, cloned.ID)
	assert.Equal(t, original.Sender, cloned.Sender)
	assert.Equal(t, original.Receiver, cloned.Receiver)
	assert.Equal(t, original.Content, cloned.Content)

	// 验证 Data map 被深拷贝（修改 clone 不影响 original）
	cloned.Data["new_key"] = "new_value"
	_, exists := original.Data["new_key"]
	assert.False(t, exists, "original should not be affected by clone modification")
}

// TestHubMessage_GetMediaInfoJSON 测试 MediaInfo JSON 序列化
func TestHubMessage_GetMediaInfoJSON(t *testing.T) {
	t.Run("nil media info", func(t *testing.T) {
		msg := NewHubMessage()
		assert.Equal(t, "{}", msg.GetMediaInfoJSON())
	})

	t.Run("string media info", func(t *testing.T) {
		msg := NewHubMessage().WithMediaInfo("raw string")
		assert.Equal(t, "raw string", msg.GetMediaInfoJSON())
	})

	t.Run("object media info", func(t *testing.T) {
		obj := map[string]interface{}{"url": "http://example.com/image.png"}
		msg := NewHubMessage().WithMediaInfo(obj)
		jsonStr := msg.GetMediaInfoJSON()
		assert.Contains(t, jsonStr, "url")
		assert.Contains(t, jsonStr, "example.com")
	})
}

// TestHubMessage_ContentExtra 测试 ContentExtra 操作
func TestHubMessage_ContentExtra(t *testing.T) {
	msg := NewHubMessage().
		WithContentExtra("color", "red").
		WithContentExtra("size", "large")

	val, ok := msg.GetContentExtra("color")
	assert.True(t, ok)
	assert.Equal(t, "red", val)

	val, ok = msg.GetContentExtra("size")
	assert.True(t, ok)
	assert.Equal(t, "large", val)

	_, ok = msg.GetContentExtra("nonexistent")
	assert.False(t, ok)

	// JSON 序列化
	jsonStr := msg.GetContentExtraJSON()
	assert.Contains(t, jsonStr, "color")
	assert.Contains(t, jsonStr, "red")
	assert.Contains(t, jsonStr, "size")
	assert.Contains(t, jsonStr, "large")
}

// TestHubMessage_Metadata 测试 Metadata 操作
func TestHubMessage_Metadata(t *testing.T) {
	msg := NewHubMessage().
		WithMetadata("trace-id", "abc-123").
		WithMetadata("user-agent", "test-client")

	val, ok := msg.GetMetadata("trace-id")
	assert.True(t, ok)
	assert.Equal(t, "abc-123", val)

	val, ok = msg.GetMetadata("user-agent")
	assert.True(t, ok)
	assert.Equal(t, "test-client", val)

	// 批量设置
	msg = msg.WithAllMetadata(map[string]string{
		"key1": "val1",
		"key2": "val2",
	})
	jsonStr := msg.GetMetadataJSON()
	assert.Contains(t, jsonStr, "key1")
	assert.Contains(t, jsonStr, "val1")
}
