/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-26 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-27 17:26:03
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

	// 432 是新增 AppID（16字节，应用隔离维度）后的预期大小：
	// 基线 416（Namespace/GroupID + mu 指针）+ AppID string(16) = 432
	// 由于 Go 版本/平台差异，允许浮动，但必须 <= 432
	assert.LessOrEqual(t, size, uintptr(432), "HubMessage size should be optimized (<= 432 bytes), got %d", size)

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

// TestMarshalJSONBytesEqualJSONMarshal 契约钉死：MarshalJSON 直调与 json.Marshal(msg) 字节严格一致
//
// 投递热路径（messaging/send.go）直调 msg.MarshalJSON() 省掉 json.Marshal 外层那一圈
// encodeState + compact 整篇复制；MarshalJSON 内部改传池化别名指针省掉整结构体装箱。
// 两处都以"字节不变"为前提——六个下游服务消费该 JSON，任何一位之差都是线上事故，
// 故此处逐形态对比而非只验证可解析性：
//   - HTML 可转义字符（< > &）与 U+2028/U+2029：内层 json.Marshal 默认 escapeHTML，
//     外层 compact 对已转义序列恒为恒等变换
//   - 嵌套 Data（map 键序、浮点格式化、nil/bool/数组）
//   - time.Time 字段：传指针后字段可取址，编码器由 marshalerEncoder 切到
//     addrMarshalerEncoder，仍是同一个 time.Time.MarshalJSON，输出一致
//   - 零值构造（mu == nil 退化无锁）与 omitempty 字段全空
func TestMarshalJSONBytesEqualJSONMarshal(t *testing.T) {
	ts := time.Date(2026, 3, 1, 2, 3, 4, 123456789, time.FixedZone("CST", 8*3600))
	cases := map[string]*HubMessage{
		"典型消息": func() *HubMessage {
			m := NewHubMessage().SetID("u-1-9527").SetMessageType(MessageTypeText).SetContent("hello group")
			m.Receiver = "u-1"
			m.CreateAt = ts
			return m
		}(),
		"HTML与行分隔符": func() *HubMessage {
			m := NewHubMessage()
			m.Content = "<script>a&b</script>  "
			m.CreateAt = ts
			return m
		}(),
		"嵌套扩展数据": func() *HubMessage {
			m := NewHubMessage()
			m.Data["k<>&"] = "v<>&"
			m.Data["nested"] = map[string]interface{}{"z": 1, "a": []interface{}{1.5, "x<", nil, true}}
			m.Data["t"] = ts
			m.CreateAt = ts
			return m
		}(),
		"零值无锁构造": {Content: "zero-value no mutex"},
		"扩展数据为nil": func() *HubMessage {
			m := NewHubMessage()
			m.Data = nil
			m.CreateAt = ts
			return m
		}(),
		"路由信封齐全": func() *HubMessage {
			m := NewHubMessage()
			m.AppID, m.Namespace, m.GroupIDs = "app-1", "ns-1", []string{"g-1", "g-2"}
			m.TraceID, m.SenderClient, m.ReceiverClient, m.ReceiverNode = "tr-1", "c-1", "c-2", "node-1"
			m.RequireAck, m.DeliveryGuarantee = true, GuaranteeEphemeral
			m.CreateAt = ts
			return m
		}(),
	}

	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			viaMarshal, errMarshal := json.Marshal(msg)
			require.NoError(t, errMarshal)
			viaMethod, errMethod := msg.MarshalJSON()
			require.NoError(t, errMethod)
			assert.Equal(t, string(viaMarshal), string(viaMethod), "直调 MarshalJSON 必须与 json.Marshal 字节一致")
		})
	}

	// 别名池复用不得串消息：交替序列化两条不同消息，各自字节应稳定
	a, b := NewHubMessage().SetContent("AAA"), NewHubMessage().SetContent("BBB")
	a.CreateAt, b.CreateAt = ts, ts
	wantA, wantB := string(mustMarshal(t, a)), string(mustMarshal(t, b))
	for i := 0; i < 64; i++ {
		assert.Equal(t, wantA, string(mustMarshal(t, a)))
		assert.Equal(t, wantB, string(mustMarshal(t, b)))
	}
}

// mustMarshal 直调 MarshalJSON 并断言无错
func mustMarshal(t *testing.T, m *HubMessage) []byte {
	t.Helper()
	data, err := m.MarshalJSON()
	require.NoError(t, err)
	return data
}

// TestCloneForFanoutIndependentLock 扇出副本的锁与消息体同体分配，但仍须是副本私有锁
// （与原对象共享锁会让扇出各成员互相阻塞，见 CloneForFanout 契约）
func TestCloneForFanoutIndependentLock(t *testing.T) {
	original := NewHubMessage().SetID("orig")
	clone := original.CloneForFanout()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			clone.SetReceiver("clone-value")
			_, _ = clone.MarshalJSON()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < concurrencyIters; i++ {
			original.SetReceiver("orig-value")
			_, _ = json.Marshal(original)
		}
	}()
	wg.Wait()

	assert.Equal(t, "clone-value", clone.Receiver)
	assert.Equal(t, "orig-value", original.Receiver)
	// Data/GroupIDs 只读共享（契约），标量字段互不影响
	assert.Equal(t, "orig", clone.ID)
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

// ============================================================================
// 序列化基准（性能路线图 M1：投递路径基线）
//
// HubMessage 序列化走 encoding/json 反射（MarshalJSON 持 RLock 后委托
// json.Marshal(alias)），是全网最热单发路径 sendToClientCore 的兜底分支与
// 广播预序列化入口的共同成本。以下基准补齐 Marshal/Unmarshal 基线，
// 子基准覆盖两种典型形态：
//   - small：纯文本短消息（无 Data 扩展、无 GroupIDs 的最小业务形态）
//   - typical：Data map 3 项（metadata/content_extra/media_info）+ GroupIDs 2 项 + 常规字段
// ============================================================================

// 基准产物包级 sink：防止编译器死码消除（赋值目标在包级可见，不可被优化掉）
var (
	hubMessageMarshalSink   []byte
	hubMessageUnmarshalSink *HubMessage
)

// makeBenchHubMessageSmall 构造纯文本短消息（最小业务形态：常规标量字段、无扩展数据）
func makeBenchHubMessageSmall() *HubMessage {
	return NewHubMessage().
		SetID("msg-small-0001").
		SetMessageType(MessageTypeText).
		SetSender("u-1001").
		SetReceiver("u-2002").
		SetSessionID("session-0001").
		SetContent("hi")
}

// makeBenchHubMessageTypical 构造典型消息（Data map 3 项 + GroupIDs 2 项 + 常规字段全填）
// Data 3 项经 With* 链式写入：metadata / content_extra / media_info（与生产入口一致）
func makeBenchHubMessageTypical() *HubMessage {
	return NewHubMessage().
		SetID("msg-typical-0001").
		SetMessageType(MessageTypeText).
		SetSender("u-1001").SetSenderName("用户甲").SetSenderType(UserTypeCustomer).
		SetReceiver("u-2002").SetReceiverName("用户乙").SetReceiverType(UserTypeCustomer).
		SetReceiverClient("c-2002-01").SetReceiverNode("node-1").
		SetSessionID("session-0001").
		SetContent("typical benchmark payload：订单已发货，请留意物流信息").
		SetMessageID("biz-0001").SetSeqNo(42).
		SetPriority(PriorityNormal).SetReplyToMsgID("biz-0000").
		SetRequireAck(true).SetPushType(PushTypeDirect).
		SetAppID("default").SetNamespace("default").
		SetGroupIDs([]string{"g-1001", "g-1002"}).
		WithMetadata("trace-id", "bench-trace-0001").
		WithContentExtra("color", "red").
		WithMediaInfo(map[string]interface{}{"url": "http://example.com/img.png"})
}

// BenchmarkHubMessageMarshal 序列化基准（encoding/json 反射路径）
// 每轮迭代为一次完整 MarshalJSON：RLock + 别名转换 + json.Marshal 反射遍历
func BenchmarkHubMessageMarshal(b *testing.B) {
	b.Run("small", func(b *testing.B) {
		msg := makeBenchHubMessageSmall()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			data, err := json.Marshal(msg)
			if err != nil {
				b.Fatal(err)
			}
			hubMessageMarshalSink = data
		}
	})

	b.Run("typical", func(b *testing.B) {
		msg := makeBenchHubMessageTypical()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			data, err := json.Marshal(msg)
			if err != nil {
				b.Fatal(err)
			}
			hubMessageMarshalSink = data
		}
	})
}

// BenchmarkHubMessageUnmarshal 反序列化基准（入站完整解析路径）
// 与 dispatcher 入站兜底同款形态：var msg *HubMessage; json.Unmarshal(data, &msg)
// （指针目标每次迭代分配新 HubMessage，含 Data map / GroupIDs slice 重建）
func BenchmarkHubMessageUnmarshal(b *testing.B) {
	b.Run("small", func(b *testing.B) {
		data, err := json.Marshal(makeBenchHubMessageSmall())
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			var msg *HubMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				b.Fatal(err)
			}
			hubMessageUnmarshalSink = msg
		}
	})

	b.Run("typical", func(b *testing.B) {
		data, err := json.Marshal(makeBenchHubMessageTypical())
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			var msg *HubMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				b.Fatal(err)
			}
			hubMessageUnmarshalSink = msg
		}
	})
}

// ============================================================================
// M2-1：锁路径零分配回归
// ============================================================================

// TestHubMessageLockingZeroAlloc 读写锁路径零分配回归（M2-1）
// lockRead/lockWrite 曾以方法值返回解锁函数——funcval（fn+data 双字）每次 16B 堆分配：
// NewHubMessage 链式构造每个 setter 一次、每个 getter/MarshalJSON 一次，
// 全链路每消息 10+ 次无谓分配。改为分支内开码 defer 后该类分配必须保持为零
func TestHubMessageLockingZeroAlloc(t *testing.T) {
	if raceEnabled {
		t.Skip("race 探测为每次锁操作注入记账分配，零分配断言仅在非 race 构建下有效")
	}

	// mu != nil 的并发安全构造 + mu == nil 的零值构造两路径都要零分配
	msg := NewHubMessage().SetID("za-1").SetContent("c").SetMessageType(MessageTypeText)
	bare := &HubMessage{ID: "za-bare"}

	// 暖机（内联/缓存首次开销不计入）
	msg.SetSeqNo(1)
	bare.SetSeqNo(1)
	_, _ = msg.GetMediaInfo()
	_, _ = bare.GetMediaInfo()
	_, _ = msg.GetOption("warm")

	allocs := testing.AllocsPerRun(200, func() {
		msg.SetContent("c") // 写锁（mu != nil）
		msg.SetSeqNo(2)
		_, _ = msg.GetOption("k") // 读锁（map miss 本身零分配）
		_, _ = msg.GetMediaInfo()
		bare.SetContent("c") // mu == nil 裸路径
		_, _ = bare.GetOption("k")
	})
	assert.Zero(t, allocs, "锁辅助路径不得产生堆分配（funcval 回归）")
}
