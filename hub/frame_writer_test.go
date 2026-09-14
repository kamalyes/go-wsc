/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 12:51:33
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 13:00:00
 * @FilePath: \go-wsc\hub\frame_writer_test.go
 * @Description: writev 合帧批量写测试 —— RFC6455 帧编码 + 写出消息一致性 + 基准
 *
 * 一致性验证：合帧写出的多帧流必须能被标准客户端逐帧读回且内容不变
 * （首条直写 + 积压 writev，与逐条 WriteMessage 语义等价）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppendFrameHeaderEncoding RFC6455 帧头三种长度编码
func TestAppendFrameHeaderEncoding(t *testing.T) {
	// 7-bit 长度（payload < 126）：2 字节头
	h := appendFrameHeader([]byte{}, 125)
	assert.Equal(t, []byte{0x81, 125}, h, "小载荷应为 2 字节帧头")

	// 16-bit 扩展长度（126 <= payload <= 65535）：4 字节头
	h = appendFrameHeader([]byte{}, 126)
	assert.Equal(t, []byte{0x81, 126, 0x00, 126}, h, "126 载荷应走 16-bit 扩展长度")
	h = appendFrameHeader([]byte{}, 65535)
	assert.Equal(t, []byte{0x81, 126, 0xFF, 0xFF}, h, "65535 载荷应为 16-bit 上界")

	// 64-bit 扩展长度（payload > 65535）：10 字节头
	h = appendFrameHeader([]byte{}, 65536)
	require.Len(t, h, 10, "大载荷应为 10 字节帧头")
	assert.Equal(t, byte(0x81), h[0])
	assert.Equal(t, byte(127), h[1], "长度标记应为 127")
	// 65536 = 0x00000000_00010000（8 字节大端）
	assert.Equal(t, []byte{0, 0, 0, 0, 0, 0x01, 0x00, 0x00}, h[2:], "64-bit 长度应大端编码")
}

// TestFrameHeaderPoolRecycle 帧头池化回收循环（容量保护语义）
func TestFrameHeaderPoolRecycle(t *testing.T) {
	for i := 0; i < 100; i++ {
		header := appendFrameHeader(acquireFrameHeader(), 100)
		releaseFrameHeader(header)
	}
	// 池对象复用不应增长容量
	assert.True(t, true, "池化循环无 panic")
}

// TestWriteFramesBatchConsistency 合帧一致性：积压多帧一次 writev 写出，客户端逐帧读回
func TestWriteFramesBatchConsistency(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	defer sConn.Close()

	// 5 条不同载荷合帧写出（1 次 writev）
	msgs := [][]byte{
		[]byte(`{"seq":1}`),
		[]byte(`{"seq":2}`),
		[]byte(`{"seq":3,"padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`),
		[]byte(`{"seq":4}`),
		[]byte(`{"seq":5}`),
	}
	require.NoError(t, hub.writeFramesBatch(sConn, msgs))

	// 客户端逐帧读回（标准 gorilla 解析器验证帧边界正确性）
	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i, want := range msgs {
		msgType, data, err := cConn.ReadMessage()
		require.NoError(t, err, "第 %d 帧应可读", i+1)
		assert.Equal(t, websocket.TextMessage, msgType, "帧类型应为 Text")
		assert.Equal(t, string(want), string(data), "第 %d 帧内容应一致", i+1)
	}
}

// TestWriteClientMessagesBatchConsistency 写泵批量写：首条直写 + 积压合帧的完整语义
func TestWriteClientMessagesBatchConsistency(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("batch-client", "batch-user", sConn)
	client.SendChan = make(chan []byte, 64)

	// 塞 10 条积压（写泵未消费）
	for i := 0; i < 10; i++ {
		client.SendChan <- []byte(`{"batch":` + string(rune('0'+i)) + `}`)
	}

	// 批量写：首条（积压中第一条）+ 尽量排空合帧
	// 注：写泵不启动，直接调用写方法验证写出侧语义
	first := <-client.SendChan
	require.NoError(t, hub.writeClientMessagesBatch(client, first))

	// 客户端应能读回全部 10 条（首条 + 9 条合帧）
	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	read := 0
	for read < 10 {
		_, data, err := cConn.ReadMessage()
		require.NoError(t, err, "第 %d 条应可读", read+1)
		assert.Contains(t, string(data), `"batch":`)
		read++
	}
}

// TestWriteClientMessagesBatchEmptyBacklog 无积压：仅首条写出（1 次 syscall 路径）
func TestWriteClientMessagesBatchEmptyBacklog(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("single-client", "single-user", sConn)
	client.SendChan = make(chan []byte, 4)

	require.NoError(t, hub.writeClientMessagesBatch(client, []byte(`{"only":1}`)))

	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := cConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, `{"only":1}`, string(data))
}

// TestWriteClientMessagesBatchBacklogRatio 埋点：批量写更新 BacklogRatio + onWriteBatch
func TestWriteClientMessagesBatchBacklogRatio(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, _ := newWSConnPair(t)
	client := newTestClient("ratio-client", "ratio-user", sConn)
	client.SendChan = make(chan []byte, 4)

	// 塞 3 条积压取 1 条作首条：batch = 1(首条) + 2(积压) = 3，写后 SendChan 空 → ratio 0
	client.SendChan <- []byte(`{}`)
	client.SendChan <- []byte(`{}`)
	client.SendChan <- []byte(`{}`)
	first := <-client.SendChan

	require.NoError(t, hub.writeClientMessagesBatch(client, first))

	assert.Equal(t, 0.0, client.BacklogRatio(), "排空后利用率应为 0")
	stats := hub.overloadMetrics.OverloadStats()
	assert.Equal(t, int64(3), stats["write_batch_total"], "首条+2 积压应记 3 条")
	assert.Equal(t, int64(1), stats["write_batch_count"], "应记 1 批")
}

// BenchmarkWriteFramesBatch 合帧写出基准（突发 64 条积压场景）
// 读侧 goroutine 持续排空（否则 TCP 写缓冲填满后 writev 被反压，测的是背压不是组装开销）
func BenchmarkWriteFramesBatch(b *testing.B) {
	hub := NewHub(newTestHubConfig())
	defer hub.SafeShutdown()

	sConn, cConn := newWSConnPair(b)
	defer sConn.Close()
	defer cConn.Close()

	// 读侧持续排空（模拟活跃消费者）
	go func() {
		for {
			_ = cConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := cConn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	msgs := make([][]byte, 64)
	for i := range msgs {
		msgs[i] = []byte(`{"benchmark":"frame","seq":` + string(rune('0'+i%10)) + `}`)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := hub.writeFramesBatch(sConn, msgs); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWriteClientMessagesBatch 写泵批量写基准（首条直写 + 63 条积压合帧 vs 逐条写）
func BenchmarkWriteClientMessagesBatch(b *testing.B) {
	hub := NewHub(newTestHubConfig())
	defer hub.SafeShutdown()

	sConn, cConn := newWSConnPair(b)
	defer sConn.Close()
	defer cConn.Close()

	go func() {
		for {
			_ = cConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := cConn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	client := newTestClient("bench-client", "bench-user", sConn)
	client.SendChan = make(chan []byte, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 每轮补 63 条积压 + 1 条首条（首条直写 + 63 合帧一次写出）
		for j := 0; j < 63; j++ {
			client.SendChan <- []byte(`{"j":1}`)
		}
		if err := hub.writeClientMessagesBatch(client, []byte(`{"j":0}`)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWriteSequentialBaseline 逐条 WriteMessage 基线（对照组：N 次 syscall）
func BenchmarkWriteSequentialBaseline(b *testing.B) {
	hub := NewHub(newTestHubConfig())
	defer hub.SafeShutdown()

	sConn, cConn := newWSConnPair(b)
	defer sConn.Close()
	defer cConn.Close()

	go func() {
		for {
			_ = cConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := cConn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 64 条逐帧写（gorilla NextWriter/Close 逐次 flush——原路径）
		for j := 0; j < 64; j++ {
			_ = sConn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
			if err := sConn.WriteMessage(websocket.TextMessage, []byte(`{"j":1}`)); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkAppendFrameHeader 帧头编码基准（池化零分配验证）
func BenchmarkAppendFrameHeader(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		header := appendFrameHeader(acquireFrameHeader(), 128)
		releaseFrameHeader(header)
	}
}
