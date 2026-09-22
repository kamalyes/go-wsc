/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-07-06 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 03:00:00
 * @FilePath: \go-wsc\connection\pump.go
 * @Description: 连接域 —— 读写泵（连接 I/O 半边）
 *   - 读泵：ReadMessage 循环 → 断开分类 → 文本/二进制分发回调
 *   - 写泵：双 lane 单写者（控制帧严格优先）+ 批量排空
 *   - 写失败主动 Close，让读泵立即报错退出，防半死连接泄漏
 *
 * 从 hub/message_handler.go 拆解归位：I/O 循环是连接域职责，
 * 消息语义（解析/路由/分发）经 PumpHost 端口回调消息域，不持有 *hub.Hub。
 * 数据 lane 的 writev 合帧由 BatchWriter 承担（frame_writer.go）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 常量
// ============================================================================

const (
	// clientWriteBatchSize 单次唤醒最多排空的积压消息数
	// （Slack 网关风格写合并：突发场景下 N 条消息共用一次 goroutine 唤醒 + 一次写超时，
	// 降低高并发下的调度与 deadline 设置开销；上限防止单连接长期独占写协程饿死其他连接）
	clientWriteBatchSize = 64

	// clientWriteTimeout 单条消息写入的超时时间
	clientWriteTimeout = 10 * time.Second
)

// ConnectionPump 连接读写泵（每连接一对读/写泵，由编排层以 goroutine 驱动）
type ConnectionPump struct {
	host PumpHost
}

// NewConnectionPump 创建读写泵
func NewConnectionPump(host PumpHost) *ConnectionPump {
	return &ConnectionPump{host: host}
}

// ============================================================================
// 写泵
// ============================================================================

// StartWritePump 处理客户端消息写入（双 lane 单写者循环）
//
// 🚦 控制帧严格优先：select 前先非阻塞排空 CtrlCh，
// 消除 select 伪随机调度导致控制帧（KickOut/ForceOffline/Ack）被数据帧插队的可能；
// CtrlCh 为 nil（SSE/手工构造）时跳过，天然安全
// 控制消息以 TextMessage 写出（序列化后的业务级控制消息，非 WS 协议控制帧）
func (p *ConnectionPump) StartWritePump(client *models.Client) {
	host := p.host
	log := host.GetLogger()
	defer func() {
		// 每连接 4 条生命周期日志（读写协程启动/结束），千万连接下不可忽略——降为 DEBUG
		logWithClient(log, logger.DEBUG, "客户端写入协程结束", client)
	}()

	logWithClient(log, logger.DEBUG, "客户端写入协程启动", client)

	for {
		if client.CtrlCh != nil {
			for {
				select {
				case data, ok := <-client.CtrlCh:
					if !ok {
						return
					}
					if client.Conn == nil {
						continue
					}
					if err := p.writeClientMessages(client, data); err != nil {
						logWithClient(log, logger.ERROR, "控制帧写入失败", client, "error", err)
						// 主动关闭连接，让读泵的 ReadMessage 立即报错退出（语义同数据写失败）
						_ = client.Conn.Close()
						return
					}
				default:
					goto drained
				}
			}
		}
	drained:
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				logWithClient(log, logger.INFO, "客户端发送通道关闭", client)
				return
			}

			if client.Conn == nil {
				continue
			}

			// ⚡ 首条直写 + 积压 writev 合帧（突发 N 条：N 次 syscall → 2 次；见 frame_writer.go）
			if err := host.GetBatchWriter().WriteClientMessagesBatch(client, message); err != nil {
				logWithClient(log, logger.ERROR, "客户端消息写入失败", client, "error", err)
				// 主动关闭连接，让读泵的 ReadMessage 立即报错退出
				// 否则读泵会卡在 IO wait 直到 TCP keepalive 超时，造成半死连接泄漏
				// （读泵退出后会触发 defer Unregister 完成清理）
				_ = client.Conn.Close()
				return
			}
		case data, ok := <-client.CtrlCh:
			// 空闲唤醒：数据 lane 静默期（SendChan 空、无 pong）控制消息仍需及时写出，
			// 否则 KickOut/ForceOffline/Ack 会在 CtrlCh 滞留到下一次数据帧/pong 才被捎带处理；
			// 严格优先仍由循环顶部的非阻塞排空保证（本 case 仅兜底唤醒）
			// CtrlCh 为 nil（SSE/手工构造）时该 case 永不就绪，天然安全
			if !ok {
				return
			}
			if client.Conn == nil {
				continue
			}
			if err := p.writeClientMessages(client, data); err != nil {
				logWithClient(log, logger.ERROR, "控制帧写入失败", client, "error", err)
				_ = client.Conn.Close()
				return
			}
		case pongData := <-client.PongCh:
			// 协议级 PING 的 pong 响应（单写者：控制帧也统一由写泵写出，见 setupPingHandler）
			if client.Conn == nil {
				continue
			}
			// WriteControl 自带 deadline 参数，不依赖外层写超时
			if err := client.Conn.WriteControl(websocket.PongMessage, pongData, time.Now().Add(clientWriteTimeout)); err != nil {
				logWithClient(log, logger.ERROR, "pong 控制帧写入失败", client, "error", err)
				_ = client.Conn.Close()
				return
			}
		case <-client.DoneCh:
			// 客户端注销（closeClientChannel）——数据通道不 close，仅靠此信号退出
			logWithClient(log, logger.INFO, "客户端生命周期结束，写入协程退出", client)
			return
		case <-host.Context().Done():
			logWithClient(log, logger.INFO, "客户端写入协程因Hub关闭而结束", client)
			return
		}
	}
}

// writeClientMessages 写入首条消息并排空积压（批量共用一次写超时）
//
// 性能：每批只 SetWriteDeadline 一次，突发 N 条消息时避免 N 次 goroutine 唤醒
// 超过 clientWriteBatchSize 的剩余积压由外层循环的下一批继续处理（不丢弃、不阻塞投递方）
// 批末以单写者 atomic store 更新 SendChan 利用率（慢消费者治理的采样源，扫描器无锁读）
func (p *ConnectionPump) writeClientMessages(client *models.Client, first []byte) error {
	client.Conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	if err := client.Conn.WriteMessage(websocket.TextMessage, first); err != nil {
		return err
	}

	// 非阻塞排空积压：突发 N 条消息时避免 N 次唤醒 + N 次超时设置
	batch := 1
	for i := 1; i < clientWriteBatchSize; i++ {
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				// 通道关闭：已写入的消息有效，交由外层循环感知关闭并退出
				logWithClient(p.host.GetLogger(), logger.INFO, "客户端发送通道关闭", client)
				return nil
			}
			if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return err
			}
			batch++
		default:
			// 写泵单写者：len(SendChan) 只被本 goroutine 消费变化 + 投递方增加，
			// 原子语义近似准确（写入侧无并发），治理采样足够
			client.SetBacklogRatio(len(client.SendChan), cap(client.SendChan))
			// 📊 准入闸门埋点：每批 1 次 atomic add（在途量 backlog = written - delivered）
			p.host.OnWriteBatch(batch)
			return nil // 无积压，结束本批
		}
	}
	client.SetBacklogRatio(len(client.SendChan), cap(client.SendChan))
	p.host.OnWriteBatch(batch)
	return nil
}

// ============================================================================
// 读泵
// ============================================================================

// StartReadPump 处理客户端消息读取（ReadMessage 循环 + 断开分类）
func (p *ConnectionPump) StartReadPump(client *models.Client) {
	host := p.host
	log := host.GetLogger()
	defer host.UnregisterClient(client)
	defer func() {
		// 每连接 4 条生命周期日志（读写协程启动/结束），千万连接下不可忽略——降为 DEBUG
		logWithClient(log, logger.DEBUG, "客户端读取协程结束", client)
	}()

	logWithClient(log, logger.DEBUG, "客户端读取协程启动", client)

	// 使用 client.Context（从 Hub 生命周期派生的连接级 ctx，Hub 关闭时自动取消）
	reqCtx := client.Context

	for {
		messageType, data, err := client.Conn.ReadMessage()
		if err != nil {
			// Hub 正在关闭（SafeShutdown 触发），连接是被服务端主动关闭的
			// 此时读循环会拿到 "use of closed network connection"，走 ClassifyCloseError
			// 会被误判为 1006 异常断开，所以这里短路掉，单独记一条 INFO 日志
			if host.IsShuttingDown() {
				logWithClient(log, logger.INFO, "服务关闭，断开客户端连接", client, "error", err.Error())
				return
			}

			// 🔍 识别断开类型和原因
			errStr := err.Error()
			closeCode, isNormal := ClassifyCloseError(err)

			// 获取关闭码描述
			codeDesc := "未知错误"
			if info, exists := models.WsCloseCodeMap[closeCode]; exists {
				codeDesc = info.Desc
			}

			// 根据错误类型记录不同级别的日志
			if isNormal {
				logWithClient(log, logger.INFO, "客户端正常断开", client, "close_code", closeCode, "code_desc", codeDesc)
			} else {
				// 异常断开 - 记录详细信息用于排查
				logWithClient(log, logger.WARN, "客户端异常断开", client, "close_code", closeCode, "code_desc", codeDesc, "error", errStr)
				// 记录错误到连接记录
				host.TrackConnectionError(client.Context, client.ID, client.UserType, err)
			}
			return
		}

		client.SetLastSeen(time.Now())

		// 控制帧（ping/pong/close）由 gorilla 在 ReadMessage 内部经 handler 分发，
		// 不会作为 messageType 返回——协议级 PING 的保活与 pong 响应见 setupPingHandler
		switch messageType {
		case websocket.TextMessage:
			host.HandleTextMessage(reqCtx, client, data)
		case websocket.BinaryMessage:
			host.HandleBinaryMessage(client, data)
		}
	}
}

// ============================================================================
// WebSocket 关闭错误分类
// ============================================================================

// ClassifyCloseError 分类关闭错误
func ClassifyCloseError(err error) (closeCode int, isNormal bool) {
	closeCode = websocket.CloseAbnormalClosure // 默认异常关闭

	// 遍历检查各种关闭错误
	for code, info := range models.WsCloseCodeMap {
		if websocket.IsCloseError(err, code) {
			return code, info.IsNormal
		}
	}

	return closeCode, false
}
