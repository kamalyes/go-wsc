/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 12:51:33
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 00:00:00
 * @FilePath: \go-wsc\connection\frame_writer_test.go
 * @Description: writev 合帧批量写测试 —— RFC6455 帧编码 + 写出消息一致性 + 埋点回调
 *
 * 迁移自 hub/frame_writer_test.go（P2 批1 测试随源归位）：合帧一致性验证 ——
 * 合帧写出的多帧流必须能被标准客户端逐帧读回且内容不变（首条直写 + 积压
 * writev，与逐条 WriteMessage 语义等价）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

// TestAppendFrameHeaderEncoding RFC6455 帧头三种长度编码
func TestAppendFrameHeaderEncoding(t *testing.T) {
	// 7-bit 长度（payload < 126）：2 字节头
	h := AppendFrameHeader([]byte{}, 125)
	assert.Equal(t, []byte{0x81, 125}, h, "小载荷应为 2 字节帧头")

	// 16-bit 扩展长度（126 <= payload <= 65535）：4 字节头
	h = AppendFrameHeader([]byte{}, 126)
	assert.Equal(t, []byte{0x81, 126, 0x00, 126}, h, "126 载荷应走 16-bit 扩展长度")
	h = AppendFrameHeader([]byte{}, 65535)
	assert.Equal(t, []byte{0x81, 126, 0xFF, 0xFF}, h, "65535 载荷应为 16-bit 上界")

	// 64-bit 扩展长度（payload > 65535）：10 字节头
	h = AppendFrameHeader([]byte{}, 65536)
	require.Len(t, h, 10, "大载荷应为 10 字节帧头")
	assert.Equal(t, byte(0x81), h[0])
	assert.Equal(t, byte(127), h[1], "长度标记应为 127")
	// 65536 = 0x00000000_00010000（8 字节大端）
	assert.Equal(t, []byte{0, 0, 0, 0, 0, 0x01, 0x00, 0x00}, h[2:], "64-bit 长度应大端编码")
}

// TestFrameHeaderPoolRecycle 帧头池化回收循环（容量保护语义）
func TestFrameHeaderPoolRecycle(t *testing.T) {
	for i := 0; i < 100; i++ {
		header := AppendFrameHeader(acquireFrameHeader(), 100)
		releaseFrameHeader(header)
	}
	assert.True(t, true, "池化循环无 panic")
}

// TestBatchFramesPoolRecycle 积压帧切片池回收循环
func TestBatchFramesPoolRecycle(t *testing.T) {
	for i := 0; i < 100; i++ {
		frames := acquireBatchFrames()
		frames = append(frames, []byte("m"))
		releaseBatchFrames(frames)
	}
	assert.True(t, true, "切片池化循环无 panic")
}

// TestWriteFramesBatchConsistency 合帧一致性：积压多帧一次 writev 写出，客户端逐帧读回
func TestWriteFramesBatchConsistency(t *testing.T) {
	sConn, cConn := newWSConnPair(t)
	defer sConn.Close()

	// 5 条不同载荷合帧写出（1 次 writev），含长载荷覆盖 16-bit 编码路径
	longPadding := make([]byte, 200)
	for i := range longPadding {
		longPadding[i] = 'x'
	}
	msgs := [][]byte{
		[]byte(`{"seq":1}`),
		[]byte(`{"seq":2}`),
		append([]byte(`{"seq":3,"padding":"`), append(longPadding, []byte(`"}`)...)...),
		[]byte(`{"seq":4}`),
		[]byte(`{"seq":5}`),
	}
	require.NoError(t, WriteFramesBatch(sConn, msgs))

	// 客户端逐帧读回（标准 gorilla 解析器验证帧边界正确性）
	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i, want := range msgs {
		msgType, data, err := cConn.ReadMessage()
		require.NoError(t, err, "第 %d 帧应可读", i+1)
		assert.Equal(t, websocket.TextMessage, msgType, "帧类型应为 Text")
		assert.Equal(t, string(want), string(data), "第 %d 帧内容应一致", i+1)
	}
}

// TestWriteFramesBatchEmpty 空积压为 no-op
func TestWriteFramesBatchEmpty(t *testing.T) {
	sConn, _ := newWSConnPair(t)
	defer sConn.Close()
	require.NoError(t, WriteFramesBatch(sConn, nil))
}

// TestWriteClientMessagesBatchConsistency 写泵批量写：首条直写 + 积压合帧 + 埋点回调的完整语义
func TestWriteClientMessagesBatchConsistency(t *testing.T) {
	sConn, cConn := newWSConnPair(t)
	defer sConn.Close()

	client := models.NewClient("batch-client", "batch-user", models.UserTypeCustomer)
	client.Conn = sConn
	client.SendChan = make(chan []byte, 64)

	// 塞 10 条积压（写泵未消费）
	backlog := make([][]byte, 10)
	for i := range backlog {
		backlog[i] = []byte(`{"backlog":` + string(rune('0'+i)) + `}`)
		client.SendChan <- backlog[i]
	}

	var batches []int
	writer := NewBatchWriter(func(n int) { batches = append(batches, n) }, nil)

	first := []byte(`{"first":true}`)
	require.NoError(t, writer.WriteClientMessagesBatch(client, first))

	// 客户端读回：首条 + 10 条积压逐帧一致
	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	all := append([][]byte{first}, backlog...)
	for i, want := range all {
		_, data, err := cConn.ReadMessage()
		require.NoError(t, err, "第 %d 帧应可读", i+1)
		assert.Equal(t, string(want), string(data), "第 %d 帧内容应一致", i+1)
	}

	// 本批一次 writev 写出 11 条（首条直写后整批排空）→ 埋点恰好一次、计数 11
	require.Len(t, batches, 1, "一次批量写应只埋点一次")
	assert.Equal(t, 11, batches[0], "埋点应含首条 + 积压共 11 条")

	// 批量写后 BacklogRatio 应更新为排空后的利用率
	assert.LessOrEqual(t, client.BacklogRatio(), float64(0), "排空后利用率应为 0")
}

// TestWriteClientMessagesBatchChannelClosed SendChan 关闭：已写消息有效，静默返回 nil
func TestWriteClientMessagesBatchChannelClosed(t *testing.T) {
	sConn, cConn := newWSConnPair(t)
	defer sConn.Close()

	client := models.NewClient("closed-client", "closed-user", models.UserTypeCustomer)
	client.Conn = sConn
	closed := make(chan []byte, 4)
	close(closed)
	client.SendChan = closed

	writer := NewBatchWriter(nil, nil)
	require.NoError(t, writer.WriteClientMessagesBatch(client, []byte(`{"first":1}`)),
		"通道关闭应静默返回（已写消息有效，外层循环感知退出）")

	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := cConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, `{"first":1}`, string(data))
}

// TestWriteClientMessagesBatchBatchSizeCap 整批收满 ClientWriteBatchSize：分批写出不丢弃
func TestWriteClientMessagesBatchBatchSizeCap(t *testing.T) {
	sConn, cConn := newWSConnPair(t)
	defer sConn.Close()

	client := models.NewClient("cap-client", "cap-user", models.UserTypeCustomer)
	client.Conn = sConn
	client.SendChan = make(chan []byte, constants.ClientWriteBatchSize+8)

	// 塞满整批 + 2 条残余（验证批满后剩余积压不丢、由下一批处理）
	total := constants.ClientWriteBatchSize - 1 + 2 // 首条直写占 1 个名额
	for i := 0; i < total; i++ {
		client.SendChan <- []byte(`{"i":` + string(rune('0'+i%10)) + `}`)
	}

	writer := NewBatchWriter(nil, nil)
	require.NoError(t, writer.WriteClientMessagesBatch(client, []byte(`{"first":1}`)))

	// 本批写出 = 首条 + 63 条（共 64 上限），剩余 2 条留在通道由下一批处理
	assert.Equal(t, 2, len(client.SendChan), "批满后剩余积压应保留不丢弃")

	cConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	read := 0
	for read < constants.ClientWriteBatchSize {
		_, _, err := cConn.ReadMessage()
		require.NoError(t, err, "第 %d 帧应可读", read+1)
		read++
	}
}
