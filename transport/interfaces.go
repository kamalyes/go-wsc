/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 09:26:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 09:26:00
 * @FilePath: \go-wsc\transport\interfaces.go
 * @Description: 传输域端口 —— 接入流程对编排层的回调契约
 *
 * 消费者侧端口模式（与 connection.EvictHook 同款）：transport 定义最小接口，
 * hub 编排层实现并注入，transport 不持有 *Hub，避免循环依赖
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"github.com/kamalyes/go-wsc/models"
)

// Registrar 编排层注册端口 —— 传输接入流程中需要回调的编排能力
// 由 hub 编排层实现（P2 批4 装配），WS/SSE 两条接入链路共用
type Registrar interface {
	// Register 异步注册（WS 升级路径：升级后立即返回，注册在后台完成）
	Register(client *models.Client)

	// RegisterSync 同步注册（SSE 路径：注册完成才进写循环，避免首条消息竞态丢失）
	RegisterSync(client *models.Client)

	// Unregister 异步注销（SSE 写循环退出后的兜底清理，幂等）
	Unregister(client *models.Client)

	// IsShutdown 编排层是否正在关闭（关闭中拒绝新连接）
	IsShutdown() bool

	// SendRegisteredMessage 发送注册成功确认消息（配置启用时）
	SendRegisteredMessage(client *models.Client)
}

// RequestIDGenerator 请求 ID 生成器端口（健康检查响应消息 ID 用）
// 生产装配注入 ShortFlake 等实现；未注入时消息 ID 留空（健康检查响应即发即弃，无消费者依赖 ID）
type RequestIDGenerator interface {
	GenerateRequestID() string
}
