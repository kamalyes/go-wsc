/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-23 17:07:25
 * @FilePath: \go-wsc\hub\overflow.go
 * @Description: Hub 层 overflow buffer 管理 - 当客户端发送通道满时暂存消息并异步回灌
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"time"
)

// 默认 overflow buffer 容量
const defaultOverflowBufferSize = 64

// drain 重试间隔
const drainRetryInterval = 50 * time.Millisecond

// ============================================================================
// WebSocket overflow 管理
// ============================================================================

// TrySendWithOverflow 尝试向客户端发送数据，通道满时写入 overflow buffer
// 返回 true 表示消息已接收（直接发送或暂存 overflow），false 表示客户端已关闭
func (h *Hub) TrySendWithOverflow(client *Client, data []byte) bool {
	// 先尝试直接发送
	if client.TrySend(data) {
		// 直接写入成功，检查 overflow 是否有待回灌数据并触发 drain
		h.triggerOverflowDrain(client)
		return true
	}

	// TrySend 返回 false：客户端已关闭或通道已满
	// 如果客户端已关闭，不再写入 overflow
	if client.IsClosed() {
		return false
	}

	// 通道已满，写入 overflow buffer
	return h.writeToOverflow(client, data)
}

// writeToOverflow 将数据写入 overflow buffer
func (h *Hub) writeToOverflow(client *Client, data []byte) bool {
	bufSize := client.OverflowBufferSize
	if bufSize <= 0 {
		bufSize = defaultOverflowBufferSize
	}

	client.OverflowMu().Lock()
	defer client.OverflowMu().Unlock()

	buf := client.OverflowBuf()
	// overflow 也满了，丢弃最早的消息腾出空间
	if len(buf) >= bufSize {
		buf = buf[1:]
	}

	// 复制数据避免外部修改
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	buf = append(buf, dataCopy)
	client.SetOverflowBuf(buf)

	// 确保 drain goroutine 已启动
	h.ensureOverflowDrainStarted(client)
	return true
}

// ensureOverflowDrainStarted 确保 overflow drain goroutine 已启动
func (h *Hub) ensureOverflowDrainStarted(client *Client) {
	if client.OverflowStarted().Load() {
		// drain 已在运行，通知有新数据
		select {
		case client.OverflowDrainCh() <- struct{}{}:
		default:
		}
		return
	}

	client.SetOverflowDrainCh(make(chan struct{}, 1))
	client.SetOverflowStopCh(make(chan struct{}))
	client.OverflowStarted().Store(true)

	go h.drainOverflow(client)
}

// triggerOverflowDrain 触发 overflow drain（如果有待回灌数据且 drain 已启动）
func (h *Hub) triggerOverflowDrain(client *Client) {
	if !client.OverflowStarted().Load() {
		return
	}
	client.OverflowMu().Lock()
	hasData := len(client.OverflowBuf()) > 0
	client.OverflowMu().Unlock()
	if !hasData {
		return
	}
	select {
	case client.OverflowDrainCh() <- struct{}{}:
	default:
	}
}

// drainOverflow 将 overflow buffer 中的消息异步回灌到 SendChan
func (h *Hub) drainOverflow(client *Client) {
	retryTimer := time.NewTimer(0)
	retryTimer.Stop()
	defer retryTimer.Stop()

	for {
		select {
		case <-client.OverflowStopCh():
			return
		case <-client.OverflowDrainCh():
			h.drainOverflowOnce(client)
			// drain 完成后如果 overflow 仍有数据（SendChan 暂时满），安排重试
			client.OverflowMu().Lock()
			hasMore := len(client.OverflowBuf()) > 0
			client.OverflowMu().Unlock()
			if hasMore {
				retryTimer.Reset(drainRetryInterval)
			}
		case <-retryTimer.C:
			h.drainOverflowOnce(client)
			client.OverflowMu().Lock()
			hasMore := len(client.OverflowBuf()) > 0
			client.OverflowMu().Unlock()
			if hasMore {
				retryTimer.Reset(drainRetryInterval)
			}
		}
	}
}

// drainOverflowOnce 执行一次 overflow drain
func (h *Hub) drainOverflowOnce(client *Client) {
	for {
		client.OverflowMu().Lock()
		buf := client.OverflowBuf()
		if len(buf) == 0 {
			client.OverflowMu().Unlock()
			return
		}
		data := buf[0]
		client.OverflowMu().Unlock()

		// 尝试写入 SendChan（非阻塞）
		client.CloseMu.Lock()
		if client.IsClosed() || client.SendChan == nil {
			client.CloseMu.Unlock()
			return
		}

		select {
		case client.SendChan <- data:
			client.CloseMu.Unlock()
			// 成功写入，从 overflow 中移除
			client.OverflowMu().Lock()
			buf = client.OverflowBuf()
			if len(buf) > 0 {
				buf = buf[1:]
			}
			client.SetOverflowBuf(buf)
			client.OverflowMu().Unlock()
		default:
			client.CloseMu.Unlock()
			// SendChan 仍然满，等待下次触发
			return
		}
	}
}

// ============================================================================
// SSE overflow 管理
// ============================================================================

// TrySendSSEWithOverflow 尝试向SSE客户端发送消息，通道满时写入 overflow buffer
// 返回 true 表示消息已接收（直接发送或暂存 overflow），false 表示客户端已关闭
func (h *Hub) TrySendSSEWithOverflow(client *Client, msg *HubMessage) bool {
	// 先尝试直接发送
	if client.TrySendSSE(msg) {
		// 直接写入成功，检查 SSE overflow 是否有待回灌数据并触发 drain
		h.triggerSSEOverflowDrain(client)
		return true
	}

	// TrySendSSE 返回 false：客户端已关闭或通道已满
	if client.IsClosed() {
		return false
	}

	// 通道已满，写入 SSE overflow buffer
	return h.writeToSSEOverflow(client, msg)
}

// writeToSSEOverflow 将消息写入 SSE overflow buffer
func (h *Hub) writeToSSEOverflow(client *Client, msg *HubMessage) bool {
	bufSize := client.OverflowBufferSize
	if bufSize <= 0 {
		bufSize = defaultOverflowBufferSize
	}

	client.SSEOverflowMu().Lock()
	defer client.SSEOverflowMu().Unlock()

	buf := client.SSEOverflowBuf()
	// overflow 也满了，丢弃最早的消息腾出空间
	if len(buf) >= bufSize {
		buf = buf[1:]
	}

	buf = append(buf, msg.Clone())
	client.SetSSEOverflowBuf(buf)

	// 确保 SSE drain goroutine 已启动
	h.ensureSSEOverflowDrainStarted(client)
	return true
}

// ensureSSEOverflowDrainStarted 确保 SSE overflow drain goroutine 已启动
func (h *Hub) ensureSSEOverflowDrainStarted(client *Client) {
	if client.SSEOverflowStarted().Load() {
		select {
		case client.SSEOverflowDrainCh() <- struct{}{}:
		default:
		}
		return
	}

	client.SetSSEOverflowDrainCh(make(chan struct{}, 1))
	client.SetSSEOverflowStopCh(make(chan struct{}))
	client.SSEOverflowStarted().Store(true)

	go h.drainSSEOverflow(client)
}

// triggerSSEOverflowDrain 触发 SSE overflow drain（如果有待回灌数据且 drain 已启动）
func (h *Hub) triggerSSEOverflowDrain(client *Client) {
	if !client.SSEOverflowStarted().Load() {
		return
	}
	client.SSEOverflowMu().Lock()
	hasData := len(client.SSEOverflowBuf()) > 0
	client.SSEOverflowMu().Unlock()
	if !hasData {
		return
	}
	select {
	case client.SSEOverflowDrainCh() <- struct{}{}:
	default:
	}
}

// drainSSEOverflow 将 SSE overflow buffer 中的消息异步回灌到 SSEMessageCh
func (h *Hub) drainSSEOverflow(client *Client) {
	retryTimer := time.NewTimer(0)
	retryTimer.Stop()
	defer retryTimer.Stop()

	for {
		select {
		case <-client.SSEOverflowStopCh():
			return
		case <-client.SSEOverflowDrainCh():
			h.drainSSEOverflowOnce(client)
			client.SSEOverflowMu().Lock()
			hasMore := len(client.SSEOverflowBuf()) > 0
			client.SSEOverflowMu().Unlock()
			if hasMore {
				retryTimer.Reset(drainRetryInterval)
			}
		case <-retryTimer.C:
			h.drainSSEOverflowOnce(client)
			client.SSEOverflowMu().Lock()
			hasMore := len(client.SSEOverflowBuf()) > 0
			client.SSEOverflowMu().Unlock()
			if hasMore {
				retryTimer.Reset(drainRetryInterval)
			}
		}
	}
}

// drainSSEOverflowOnce 执行一次 SSE overflow drain
func (h *Hub) drainSSEOverflowOnce(client *Client) {
	for {
		client.SSEOverflowMu().Lock()
		buf := client.SSEOverflowBuf()
		if len(buf) == 0 {
			client.SSEOverflowMu().Unlock()
			return
		}
		msg := buf[0]
		client.SSEOverflowMu().Unlock()

		client.CloseMu.Lock()
		if client.IsClosed() || client.SSEMessageCh == nil {
			client.CloseMu.Unlock()
			return
		}

		select {
		case client.SSEMessageCh <- msg:
			client.CloseMu.Unlock()
			client.SSEOverflowMu().Lock()
			buf = client.SSEOverflowBuf()
			if len(buf) > 0 {
				buf = buf[1:]
			}
			client.SetSSEOverflowBuf(buf)
			client.SSEOverflowMu().Unlock()
		default:
			client.CloseMu.Unlock()
			return
		}
	}
}

// GetOverflowBufferSize 获取客户端的 overflow buffer 容量
func (h *Hub) GetOverflowBufferSize(client *Client) int {
	if client.OverflowBufferSize > 0 {
		return client.OverflowBufferSize
	}
	return defaultOverflowBufferSize
}
