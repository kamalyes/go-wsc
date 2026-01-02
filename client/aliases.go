/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 21:22:37
 * @FilePath: \go-wsc\client\aliases.go
 * @Description: Client 类型别名 - 为 hub 和 models 包中的类型创建别名，便于在 client 层使用
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package client

import (
	"github.com/kamalyes/go-wsc/hub"
	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 类型别名 - 从 hub 和 models 包导入
// ============================================================================

type (
	// Hub 相关类型
	Client = hub.Client

	// Models 相关类型
	ConnectionStatus = models.ConnectionStatus
)

// 常量别名
const (
	ConnectionStatusConnecting   = models.ConnectionStatusConnecting
	ConnectionStatusConnected    = models.ConnectionStatusConnected
	ConnectionStatusDisconnected = models.ConnectionStatusDisconnected
	ConnectionStatusReconnecting = models.ConnectionStatusReconnecting
	ConnectionStatusError        = models.ConnectionStatusError
)

// 错误别名
var (
	ErrConnectionClosed  = models.ErrConnectionClosed
	ErrMessageBufferFull = models.ErrMessageBufferFull
	ErrSendChannelFull   = models.ErrSendChannelFull
)
