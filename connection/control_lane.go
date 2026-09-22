/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-07 21:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-11 10:56:00
 * @FilePath: \go-wsc\connection\control_lane.go
 * @Description: 控制通道 —— 必达级消息的独立投递 lane（P0 分级送达基座）
 *
 * 迁移自 hub/control_lane.go（P2 批1 域化）：投递器重组为 ControlLane 组件，
 * logger 经构造注入；判定纯函数保持包级导出
 *
 * 双 lane 单写者模型（Slack async_priority 模式）：
 *   - 数据 lane：SendChan（容量按 UserType 配置，256 级）
 *   - 控制 lane：CtrlCh（容量 16，量级低）——写泵 select 前优先排空
 *   - 数据 lane 满时控制消息仍可入队：KickOut/ForceOffline/Ack 永不被业务洪峰淹没
 *
 * 降级语义（CtrlCh 满时）：
 *   - 断链类（KickOut/ForceOffline/Terminate）：直接 Conn.Close()——断链本就是目的，
 *     通知帧是锦上添花，通道满说明连接已不可用
 *   - 其余控制类：返回 false，调用方走各自兜底（ACK 有 AckManager 重试+超时转离线）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// controlMessageTypes 控制类消息集合（判定收敛于此，勿在调用方散落）
var controlMessageTypes = map[models.MessageType]struct{}{
	models.MessageTypeKickOut:            {},
	models.MessageTypeForceOffline:       {},
	models.MessageTypeTerminate:          {},
	models.MessageTypeAck:                {},
	models.MessageTypeConnectionRejected: {},
	models.MessageTypeConnectionError:    {},
	models.MessageTypeConnectionTimeout:  {},
}

// IsControlMessage 判断是否为控制类消息（控制通道 + 兜底降级的路由依据）
func IsControlMessage(mt models.MessageType) bool {
	_, ok := controlMessageTypes[mt]
	return ok
}

// IsDisconnectMessage 判断是否为断链类控制消息（CtrlCh 满时降级为直接断链）
func IsDisconnectMessage(mt models.MessageType) bool {
	switch mt {
	case models.MessageTypeKickOut, models.MessageTypeForceOffline, models.MessageTypeTerminate:
		return true
	default:
		return false
	}
}

// ControlLane 控制通道投递器（必达级消息的独立 lane）
type ControlLane struct {
	logger spi.Logger
}

// NewControlLane 创建控制通道投递器（logger 为 nil 时使用默认日志器）
func NewControlLane(logger spi.Logger) *ControlLane {
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &ControlLane{logger: logger}
}

// SendControl 向客户端控制通道发送已序列化的必达级消息
//
// 降级链（逐级兜底，控制消息尽力可达）：
//  1. CtrlCh 投递（首选：独立 lane，不受数据洪峰影响）
//  2. CtrlCh 未初始化（手工构造/SSE 早期客户端）→ 回退数据 lane TrySend
//     （无优先保障但可达——比直接断链后客户端不知原因好）
//  3. 通道满且断链类（KickOut/ForceOffline/Terminate）：直接 Conn.Close()
//     （断链本就是目的，通知帧是锦上添花）
//  4. 通道满且非断链类：返回 false，调用方走各自兜底（ACK 有重试+超时转离线）
func (l *ControlLane) SendControl(client *models.Client, data []byte, msg *models.HubMessage) bool {
	if client == nil {
		return false
	}

	if client.TrySendControl(data) {
		return true
	}

	// CtrlCh 未初始化：回退数据 lane（保持通知可达性；CtrlCh 为 nil 时 TrySendControl 恒 false）
	if client.CtrlCh == nil && client.TrySend(data) {
		return true
	}

	// 控制通道满：断链类直接断链（通知目的本就是断开）
	if msg != nil && IsDisconnectMessage(msg.MessageType) && client.Conn != nil {
		l.logger.WarnContextKV(client.Context, "控制通道满，断链类消息降级为直接断链",
			"client_id", client.ID,
			"user_id", client.UserID,
			"message_type", msg.MessageType,
		)
		_ = client.Conn.Close()
		return true // 降级语义达成（断链成功即送达目的）
	}

	return false
}

// SendControlMessage 向客户端控制通道发送控制类消息（内部序列化 + 控制通道投递一体化）
//
// 用于 KickOut/ForceOffline 等场景：消息经 Marshal 后走 CtrlCh，绕开业务 SendChan 洪峰
func (l *ControlLane) SendControlMessage(client *models.Client, msg *models.HubMessage) bool {
	if client == nil || msg == nil {
		return false
	}

	data, err := json.Marshal(msg)
	if err != nil {
		l.logger.ErrorContextKV(client.Context, "控制消息序列化失败",
			"client_id", client.ID,
			"message_type", msg.MessageType,
			"error", err,
		)
		return false
	}

	return l.SendControl(client, data, msg)
}
