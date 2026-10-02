/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-02 10:00:09
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-02 10:00:09
 * @FilePath: \go-wsc\messaging\client_delivery_test.go
 * @Description: 客户端直投漏斗测试与基准（覆盖 messaging/client_delivery.go）
 *
 *   - sendToClientCore 预序列化复用 / 兜底 Marshal / 序列化失败路径
 *   - SendToAllClientsInMap 多设备遍历 + 预序列化字节共享 + 单点失败隔离
 *   - BenchmarkSendToClientCore：全网最热单发路径全链路基准（构造→序列化→入队）
 *
 * 复用 fake_host_test.go 的 newTestManager / makeTestClient / makeGroupMessage 等 helper。
 * 与 send_test.go 既有用例互补：既有用例验证公共入口行为，本文件聚焦
 * sendToClientCore 的序列化分支语义与投递隔离性。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// sendToClientCore 序列化分支测试
// ============================================================================

// TestSendToClientCore_PreSerializedSkipsMarshal 验证预序列化复用时不发生二次 Marshal
//
// 用「与 Marshal(msg) 输出必然不同的哨兵字节」作为 preSerialized 传入：
// 客户端收到的字节与哨兵逐字节一致，即证明 core 直接复用了入参字节，
// 未走兜底 json.Marshal（兜底会产出 msg 的 JSON 编码而非哨兵）
func TestSendToClientCore_PreSerializedSkipsMarshal(t *testing.T) {
	m, _ := newTestManager()
	client := makeTestClient("c-core-pre", "u-core-pre")

	msg := makeGroupMessage("sender")
	msg.MessageID = "core-pre-1"
	sentinel := []byte(`{"message_id":"core-pre-1","sentinel":true}`)

	ok := m.sendToClientCore(context.Background(), client, msg, sentinel, false)
	assert.True(t, ok, "健康客户端预序列化投递应成功")

	select {
	case data := <-client.SendChan:
		assert.Equal(t, string(sentinel), string(data),
			"应原样收到哨兵字节（若发生二次 Marshal，产出将是 msg 的 JSON 编码）")
	case <-time.After(time.Second):
		t.Fatal("客户端应收到预序列化消息")
	}
}

// TestSendToClientCore_FallbackMarshalBytes 验证 preSerialized 为 nil 时兜底 Marshal：
// 现场序列化的字节与 json.Marshal(msg) 精确一致（旁路投递 reportStatus=false 场景，
// 即多端同步 syncToSenderDevices 的 data==nil 兜底分支）
func TestSendToClientCore_FallbackMarshalBytes(t *testing.T) {
	m, _ := newTestManager()
	client := makeTestClient("c-core-fb", "u-core-fb")

	msg := makeGroupMessage("sender")
	msg.MessageID = "core-fallback-1"
	expected, err := json.Marshal(msg)
	require.NoError(t, err)

	ok := m.sendToClientCore(context.Background(), client, msg, nil, false)
	assert.True(t, ok, "健康客户端兜底序列化投递应成功")

	select {
	case data := <-client.SendChan:
		assert.Equal(t, string(expected), string(data), "兜底 Marshal 字节应与 json.Marshal(msg) 一致")
	case <-time.After(time.Second):
		t.Fatal("客户端应收到兜底序列化消息")
	}
}

// TestSendToClientCore_MarshalFailureNoDelivery 验证无 preSerialized 且 Marshal 失败：
// 返回 false、不投递任何字节、不 panic（Data 含不可序列化的 func 值触发编码失败）
func TestSendToClientCore_MarshalFailureNoDelivery(t *testing.T) {
	m, _ := newTestManager()
	client := makeTestClient("c-core-bad", "u-core-bad")

	msg := makeGroupMessage("sender")
	msg.Data = map[string]interface{}{"bad": func() {}} // func 不可被 json.Marshal

	ok := m.SendToClientSerialized(context.Background(), client, msg, nil)
	assert.False(t, ok, "序列化失败应返回 false")

	select {
	case data := <-client.SendChan:
		t.Fatalf("序列化失败不应投递任何字节，实际收到 %d 字节", len(data))
	default:
	}
}

// ============================================================================
// SendToAllClientsInMap 遍历语义测试
// ============================================================================

// TestSendToAllClientsInMap_MultiDeviceSharedBytes 验证多设备遍历 + 预序列化一次共享：
// 同一用户 2 台设备 + 另一用户 1 台设备全部触达，三份投递字节完全一致
// （SendToAllClientsInMap 入口 Marshal 一次、逐客户端复用同一份字节），
// 且每台设备均经 Host 端口完成接收统计上报
func TestSendToAllClientsInMap_MultiDeviceSharedBytes(t *testing.T) {
	m, host := newTestManager()

	dev1 := makeTestClient("c-dev1", "u-multi")
	dev2 := makeTestClient("c-dev2", "u-multi")
	single := makeTestClient("c-single", "u-single")
	clientMap := map[string]*models.Client{
		dev1.ID:   dev1,
		dev2.ID:   dev2,
		single.ID: single,
	}

	msg := makeGroupMessage("sender")
	msg.MessageID = "map-share-1"
	m.SendToAllClientsInMap(host.Context(), clientMap, msg)

	var first string
	for _, c := range []*models.Client{dev1, dev2, single} {
		select {
		case data := <-c.SendChan:
			require.NotEmpty(t, data)
			if first == "" {
				first = string(data)
			} else {
				assert.Equal(t, first, string(data),
					"客户端 %s 收到的字节应与其他设备一致（预序列化一次共享）", c.ID)
			}
			// 字节可解析回消息且标识正确
			var got models.HubMessage
			require.NoErrorf(t, json.Unmarshal(data, &got), "客户端 %s 收到的字节应为合法 JSON", c.ID)
			assert.Equal(t, "map-share-1", got.MessageID)
		case <-time.After(time.Second):
			t.Fatalf("客户端 %s 应收到消息", c.ID)
		}
	}

	// 每台设备的接收统计均经 Host 端口上报（投递方完成上报的契约）
	for _, c := range []*models.Client{dev1, dev2, single} {
		e, ok := host.findTrackedStat(c.ID)
		require.Truef(t, ok, "客户端 %s 应有接收统计记录", c.ID)
		assert.Equal(t, 1, e.messages, "客户端 %s 的消息计数应为 1", c.ID)
		assert.Greater(t, e.bytes, 0, "客户端 %s 的字节计数应大于 0", c.ID)
	}
}

// TestSendToAllClientsInMap_SingleFailureIsolation 验证单客户端投递失败不影响其余客户端：
// 一台 SendChan 已满的客户端 TrySend 失败后，其余健康客户端照常收到消息
func TestSendToAllClientsInMap_SingleFailureIsolation(t *testing.T) {
	m, host := newTestManager()

	full := makeTestClient("c-full", "u-full")
	for i := 0; i < cap(full.SendChan); i++ {
		full.SendChan <- []byte("filler")
	}
	ok1 := makeTestClient("c-ok1", "u-ok1")
	ok2 := makeTestClient("c-ok2", "u-ok2")
	clientMap := map[string]*models.Client{
		full.ID: full,
		ok1.ID:  ok1,
		ok2.ID:  ok2,
	}

	assert.NotPanics(t, func() {
		m.SendToAllClientsInMap(host.Context(), clientMap, makeGroupMessage("sender"))
	})

	// 健康客户端照常收到
	for _, c := range []*models.Client{ok1, ok2} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("客户端 %s 不应受其他客户端投递失败影响", c.ID)
		}
	}

	// 满通道客户端仍只有 filler，无新消息入队（排空确认，与 TestSendToClientSerializedTrySendFail 同款）
	drained := 0
	for {
		select {
		case data := <-full.SendChan:
			assert.Equal(t, "filler", string(data), "满通道客户端应只有 filler，无新消息入队")
			drained++
		default:
			assert.Equal(t, cap(full.SendChan), drained, "满通道客户端的通道内容量应保持不变")
			return
		}
	}
}

// ============================================================================
// 投递热路径基准
// ============================================================================

// BenchmarkSendToClientCore 全网最热单发路径全链路基准（TrySend 真实入队路径）
//
// 形态说明：单 WS 客户端 + 消费协程常驻排空 SendChan（生产 WS 写协程恒消费，
// TrySend 恒成功），逐迭代测全程「构造 → 序列化 → 入队」：
//   - preserialized：入口预序列化一次后传入 core（SendToAllClientsInMap 同款形态，
//     多客户端共享同一份字节时的单客户端边际成本）
//   - fallback：preSerialized=nil，core 内部兜底 Marshal（SendToClient 单发直投形态）
//
// 日志器注入 Info 级：默认 NewDefaultLogger 为 Debug 控制台输出，控制台 IO 会污染测量
func BenchmarkSendToClientCore(b *testing.B) {
	m, host := newTestManager()
	host.logger = logger.NewLogger().WithLevel(logger.INFO).WithShowCaller(false)

	client := makeTestClient("c-bench-core", "u-bench-core")
	go func() {
		for range client.SendChan {
		}
	}()

	ctx := context.Background()

	b.Run("preserialized", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			msg := makeGroupMessage("sender-bench") // 构造
			data, err := json.Marshal(msg)          // 入口预序列化一次
			if err != nil {
				b.Fatal(err)
			}
			m.sendToClientCore(ctx, client, msg, data, false) // 入队
		}
	})

	b.Run("fallback", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			msg := makeGroupMessage("sender-bench")          // 构造
			m.sendToClientCore(ctx, client, msg, nil, false) // 兜底序列化 + 入队
		}
	})
}
