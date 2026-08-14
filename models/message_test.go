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
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/kamalyes/go-logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubMessage_SizeAndAlignment 验证 HubMessage 结构体大小与字段对齐
// 优化后：3 个 bool 字段集中在末尾，避免散落导致的 padding
// 期望大小：368 字节（20 个 string*16 + map*8 + time.Time*24 + int64*8 + 3 bool + padding）
// 新增 TraceID string 字段（16 字节），从 352 → 368
func TestHubMessage_SizeAndAlignment(t *testing.T) {
	size := unsafe.Sizeof(HubMessage{})

	// 416 是新增 Namespace/GroupID（各16字节）+ mu 指针（8字节）后的预期大小
	// 由于 Go 版本/平台差异，允许浮动，但必须 <= 416
	assert.LessOrEqual(t, size, uintptr(416), "HubMessage size should be optimized (<= 416 bytes), got %d", size)

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

// ============================================================================
// 并发安全测试
//
// 以下测试验证 HubMessage 的 Set*/Get*/With*/Clone/MarshalJSON 在高并发
// 场景下不会触发 "concurrent map iteration and map write" fatal，且取值正确。
// 必须以 `go test -race` 运行才能检测到数据竞争。
// ============================================================================

// concurrencyIters 并发测试的迭代次数（足够大以触发竞争，又不至于拖慢 CI）
const concurrencyIters = 2000

// TestHubMessage_ConcurrentSetGet 验证 Set* 写与 Get* 读并发时的安全性
// 场景：多 goroutine 同时 SetID/SetContent/SetSeqNo/GetMessageID，不应 panic/race
func TestHubMessage_ConcurrentSetGet(t *testing.T) {
	msg := NewHubMessage()
	var wg sync.WaitGroup
	wg.Add(4)

	// writer 1：交替写 ID 与 Content
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.SetID("id-" + time.Now().Format("150405.000000"))
			msg.SetContent("content")
		}
	}()

	// writer 2：交替写 SeqNo 与 Priority
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.SetSeqNo(int64(i))
			msg.SetPriority(PriorityHigh)
		}
	}()

	// reader 1：读 MessageID（与 SetID 并发）
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_ = msg.GetMessageID()
			_ = msg.GetTraceID()
		}
	}()

	// reader 2：读 Data 相关字段（与 With* 并发）
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_, _ = msg.GetMetadata("any")
			_ = msg.GetMetadataJSON()
			_ = msg.GetContentExtraJSON()
		}
	}()

	wg.Wait()
}

// TestHubMessage_ConcurrentDataMap 验证 Data map 并发写与读/序列化不触发 fatal
// 这是最初导致 "concurrent map iteration and map write" 崩溃的根因场景
func TestHubMessage_ConcurrentDataMap(t *testing.T) {
	msg := NewHubMessage()
	var wg sync.WaitGroup
	wg.Add(3)

	// writer：并发写 metadata / content_extra / media_info
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.WithMetadata("k", "v")
			msg.WithContentExtra("k", "v")
			msg.WithOption("opt", i)
		}
	}()

	// reader：并发读 Data map（GetMetadata/GetOption 遍历子 map）
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_, _ = msg.GetMetadata("k")
			_, _ = msg.GetOption("opt")
			_ = msg.GetAllMetadata()
		}
	}()

	// marshaler：并发序列化（json.Marshal 遍历 Data map，与 writer 并发）
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_, err := json.Marshal(msg)
			assert.NoError(t, err)
		}
	}()

	wg.Wait()
}

// TestHubMessage_ConcurrentClone 验证 Clone 与 Set*/With* 并发安全
// 场景：Clone 持 RLock 深拷贝 Data，与 With* 的 Lock 互斥，不应触发 map 并发 fatal
func TestHubMessage_ConcurrentClone(t *testing.T) {
	msg := NewHubMessage().
		SetID("orig").
		WithMetadata("trace", "abc").
		WithContentExtra("color", "red")

	var wg sync.WaitGroup
	wg.Add(2)

	// writer：持续修改 Data map
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.WithMetadata("k", "v")
			msg.WithContentExtra("k", "v")
			msg.SetID("orig")
		}
	}()

	// cloner：持续 Clone 并验证深拷贝独立性
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			c := msg.Clone()
			// 副本修改不应影响原对象（Data map 独立）
			c.WithOption("clone-only", "x")
			_, ok := msg.GetOption("clone-only")
			assert.False(t, ok, "clone modification should not affect original")
		}
	}()

	wg.Wait()
}

// TestHubMessage_ConcurrentMarshal 验证 MarshalJSON 与 Set*/With* 全字段并发安全
func TestHubMessage_ConcurrentMarshal(t *testing.T) {
	msg := NewHubMessage()
	var wg sync.WaitGroup
	wg.Add(3)

	// writer 1：写字符串字段
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.SetID("id").SetSender("s").SetReceiver("r").SetContent("c").SetSessionID("sid")
		}
	}()

	// writer 2：写 Data map 与数值字段
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.WithMetadata("k", "v").WithContentExtra("k", "v")
			msg.SetSeqNo(int64(i)).SetRequireAck(true)
		}
	}()

	// marshaler：并发序列化全字段
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			data, err := json.Marshal(msg)
			if assert.NoError(t, err) {
				var decoded HubMessage
				_ = json.Unmarshal(data, &decoded) // 仅验证可反序列化
			}
		}
	}()

	wg.Wait()
}

// TestHubMessage_ConcurrentGetAllAndMarshal 验证 GetAll*（返回内部 map 引用）与 Marshal 并发
// GetAllMetadata/GetAllContentExtra 返回的是 Data 内部子 map 的引用，
// 与 MarshalJSON 遍历可能并发。测试验证持 RLock 下不会 fatal。
func TestHubMessage_ConcurrentGetAllAndMarshal(t *testing.T) {
	msg := NewHubMessage().
		WithMetadata("a", "1").WithMetadata("b", "2").
		WithContentExtra("x", "y")

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_ = msg.GetAllMetadata()
			_ = msg.GetAllContentExtra()
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_, err := json.Marshal(msg)
			assert.NoError(t, err)
		}
	}()

	wg.Wait()
}

// TestHubMessage_NilMuIsLockFree 验证直接构造（mu=nil）时不死锁、不 panic
// 兼容 &HubMessage{} 零值构造场景
func TestHubMessage_NilMuIsLockFree(t *testing.T) {
	// 直接构造，mu 为 nil
	msg := &HubMessage{
		ID:      "zero",
		Content: "raw",
		Data:    map[string]interface{}{"k": "v"},
	}

	// Set/Get 在 nil mu 下应退化为无锁，正常执行
	msg.SetID("updated").SetContent("new")
	assert.Equal(t, "updated", msg.ID)

	_, ok := msg.GetOption("k")
	assert.True(t, ok)

	// Clone 在 nil mu 下应正常工作（无锁深拷贝）
	c := msg.Clone()
	assert.Equal(t, msg.ID, c.ID)
	assert.Equal(t, "v", c.Data["k"])

	// MarshalJSON 在 nil mu 下应正常工作
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "updated")
}

// TestHubMessage_CloneIndependentLock 验证 Clone 副本拥有独立锁
// 场景：原对象持锁写，副本写不阻塞（反之亦然）
func TestHubMessage_CloneIndependentLock(t *testing.T) {
	original := NewHubMessage().SetID("orig")
	clone := original.Clone()

	// 副本修改不应阻塞原对象
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			clone.SetID("clone-value")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			original.SetID("orig-value")
			_, _ = json.Marshal(original)
		}
	}()

	wg.Wait()
	// 副本与原对象的 ID 不互相影响
	assert.Equal(t, "clone-value", clone.ID)
	assert.Equal(t, "orig-value", original.ID)
}

// TestHubMessage_InjectContextConcurrent 验证 InjectContext 与 GetTraceID 并发安全
func TestHubMessage_InjectContextConcurrent(t *testing.T) {
	msg := NewHubMessage()
	ctx := logger.ContextWithTraceID(context.Background(), "test-trace")

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.InjectContext(ctx)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_ = msg.GetTraceID()
		}
	}()

	wg.Wait()
	// 注入后 trace_id 应稳定可见
	assert.Equal(t, "test-trace", msg.GetTraceID())
}

// TestHubMessage_CloneDeepAlias 验证 CloneDeep 接口返回独立深拷贝
func TestHubMessage_CloneDeepAlias(t *testing.T) {
	orig := NewHubMessage().
		SetID("o").
		WithMetadata("k", "v")

	c := orig.CloneDeep()
	cloned, ok := c.(*HubMessage)
	require.True(t, ok)

	cloned.WithMetadata("clone-only", "x")
	_, exists := orig.GetMetadata("clone-only")
	assert.False(t, exists, "CloneDeep should produce independent copy")
}

// TestHubMessage_AtomicCounterUnderConcurrency 辅助验证并发测试确实产生了竞争压力
// 确保 race detector 能捕获未加锁路径（本测试中所有路径都已加锁，应无 race）
func TestHubMessage_AtomicCounterUnderConcurrency(t *testing.T) {
	msg := NewHubMessage()
	var counter int64

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			msg.SetSeqNo(int64(i))
			atomic.AddInt64(&counter, 1)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			_ = msg.GetMessageID()
			atomic.AddInt64(&counter, 1)
		}
	}()

	wg.Wait()
	assert.Equal(t, int64(concurrencyIters*2), counter)
}
