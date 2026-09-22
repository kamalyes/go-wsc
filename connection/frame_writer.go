/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 22:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-17 17:56:00
 * @FilePath: \go-wsc\connection\frame_writer.go
 * @Description: writev 合帧批量写 —— 突发 N 条消息 N 次 syscall → 1 次 writev
 *
 * 迁移自 hub/frame_writer.go（P2 批1 域化）：帧编码与合帧写出为包级纯函数，
 * 写泵批量写重组为 BatchWriter 组件（onBatch 埋点回调经构造注入）
 *
 * RFC6455 帧自定界（服务端帧无掩码），net.Buffers 聚合多个 [帧头+载荷]
 * 一次 writev 落盘首条消息直写（低延迟），积压部分合帧（吞吐）：
 *   - 无积压：1 条 1 次 syscall（与原路径持平）
 *   - 突发 N 条：1 + 1 次 syscall（首条直写 + N-1 合帧一次写出）
 *
 * 写锁安全性：写泵是唯一数据写者（单写者模型，与原 WriteMessage 路径一致）；
 * WriteControl（协议 ping/pong）与数据写的理论交错窗口与原路径相同（见
 * closeClientConnection 注释——控制帧独立锁，交错时客户端走异常重连，不影响正确性）
 * 压缩扩展未启用（DefaultUpgrader 未开 EnableCompression），帧格式为标准无压缩帧
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// frameHeaderPool 帧头缓冲池（最大 10 字节：1 FIN/opcode + 1 长度标记 + 8 扩展长度）
var frameHeaderPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 0, 10)
		return &buf
	},
}

// AppendFrameHeader RFC6455 服务端帧头编码（FIN=1 + Text opcode，无掩码）
//
// 帧格式：1 字节 FIN/opcode + 1 字节长度标记 + 0/2/8 字节扩展长度
// 服务端→客户端帧不掩码（RFC 6455 §5.1：客户端帧才掩码）
func AppendFrameHeader(dst []byte, payloadLen int) []byte {
	dst = append(dst, 0x81) // FIN=1 + opcode=1（TextMessage）

	switch {
	case payloadLen < 126:
		dst = append(dst, byte(payloadLen))
	case payloadLen <= 65535:
		dst = append(dst, 126, byte(payloadLen>>8), byte(payloadLen))
	default:
		dst = append(dst, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(payloadLen))
		dst = append(dst, ext[:]...)
	}
	return dst
}

// acquireFrameHeader 从池取帧头缓冲
func acquireFrameHeader() []byte {
	return (*(frameHeaderPool.Get().(*[]byte)))[:0]
}

// releaseFrameHeader 归还帧头缓冲
func releaseFrameHeader(header []byte) {
	if cap(header) >= 10 {
		h := header[:0]
		frameHeaderPool.Put(&h)
	}
}

// WriteFramesBatch 合帧批量写出（首帧已由调用方写出，此处写剩余帧）
//
// msgs：待写出的消息载荷列表；deadline 已由调用方设置（整批共享）
// 返回错误时连接视为不可写（调用方走写失败处理——断链让读协程退出）
//
// syscall 语义：len(msgs) 条消息 → 1 次 writev（net.Buffers.WriteTo 在 Linux/Unix
// 走 writev；Windows 走 WriteFile 聚合——Go 运行时保证语义一致）
func WriteFramesBatch(conn *websocket.Conn, msgs [][]byte) error {
	if len(msgs) == 0 {
		return nil
	}

	// 聚合 [帧头, 载荷, 帧头, 载荷, ...] 的 iovec 视图（零拷贝：只引用不复制）
	bufs := make(net.Buffers, 0, len(msgs)*2)
	headers := make([][]byte, 0, len(msgs)) // 持有引用便于写后回收

	for _, payload := range msgs {
		header := AppendFrameHeader(acquireFrameHeader(), len(payload))
		headers = append(headers, header)
		bufs = append(bufs, header, payload)
	}

	// 单次 writev 落盘（绕过 gorilla 逐帧 NextWriter/Close 的逐次 flush）
	// UnderlyingConn 直写：写泵单写者模型下安全（见文件头注释）
	_, err := bufs.WriteTo(conn.UnderlyingConn())

	// 回收帧头缓冲（WriteTo 完成后数据所有权归还，可复用）
	for _, header := range headers {
		releaseFrameHeader(header)
	}
	return err
}

// BatchWriter 写泵批量写组件（首条直写 + 积压合帧）
type BatchWriter struct {
	onBatch func(int) // 每批写出量埋点（overload 域水位判定用；nil 安全）
	logger  spi.Logger
}

// NewBatchWriter 创建批量写组件
// onBatch：每批实际写出条数回调（写泵统计），无埋点需求时传 nil
func NewBatchWriter(onBatch func(int), logger spi.Logger) *BatchWriter {
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &BatchWriter{onBatch: onBatch, logger: logger}
}

// WriteClientMessagesBatch 写泵批量写（首条直写 + 积压合帧）
//
// 首条 WriteMessage 直写：无积压时与原路径完全一致（1 次 syscall 低延迟）；
// 排空积压时收集 up to ClientWriteBatchSize-1 条走 writev 合帧
// 性能：突发 N 条 → 2 次 syscall（原 N 次）；GC 压力：帧头池化零分配
func (w *BatchWriter) WriteClientMessagesBatch(client *models.Client, first []byte) error {
	// 整批共享一次写超时（突发场景 N 次期限设置 → 1 次）
	_ = client.Conn.SetWriteDeadline(time.Now().Add(constants.ClientWriteTimeout))

	if err := client.Conn.WriteMessage(websocket.TextMessage, first); err != nil {
		return err
	}

	// 非阻塞排空积压：突发 N 条消息避免 N 次唤醒
	batch := 1
	frames := acquireBatchFrames()
	for i := 1; i < constants.ClientWriteBatchSize; i++ {
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				// 通道关闭：已写入的消息有效，交由外层循环感知关闭并退出
				w.logger.InfoContextKV(client.Context, "客户端发送通道关闭",
					"client_id", client.ID,
					"user_id", client.UserID,
				)
				releaseBatchFrames(frames)
				return nil
			}
			frames = append(frames, message)
			batch++
		default:
			client.SetBacklogRatio(len(client.SendChan), cap(client.SendChan))
			w.notifyBatch(batch)
			// 写出收集的积压帧（1 次 writev）后结束本批
			if err := WriteFramesBatch(client.Conn, frames); err != nil {
				releaseBatchFrames(frames)
				return err
			}
			releaseBatchFrames(frames)
			return nil
		}
	}

	// 整批收满 ClientWriteBatchSize：写出（剩余积压由下一批处理，不丢弃）
	client.SetBacklogRatio(len(client.SendChan), cap(client.SendChan))
	w.notifyBatch(batch)
	err := WriteFramesBatch(client.Conn, frames)
	releaseBatchFrames(frames)
	return err
}

// notifyBatch 批量写出埋点（nil 安全）
func (w *BatchWriter) notifyBatch(batch int) {
	if w.onBatch != nil {
		w.onBatch(batch)
	}
}

// batchFramesPool 积压帧收集切片池（writev 的载荷引用视图）
var batchFramesPool = sync.Pool{
	New: func() any {
		s := make([][]byte, 0, constants.ClientWriteBatchSize)
		return &s
	},
}

// acquireBatchFrames 取积压帧切片
func acquireBatchFrames() [][]byte {
	return (*(batchFramesPool.Get().(*[][]byte)))[:0]
}

// releaseBatchFrames 还积压帧切片（引用视图，消息载荷所有权在 channel 传递后归写泵）
func releaseBatchFrames(frames [][]byte) {
	if cap(frames) >= constants.ClientWriteBatchSize {
		f := frames[:0]
		batchFramesPool.Put(&f)
	}
}
