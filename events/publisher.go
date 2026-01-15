/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:10:15
 * @FilePath: \go-wsc\events\publisher.go
 * @Description: 事件发布器接口
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package events

import (
	"context"

	"github.com/kamalyes/go-cachex"
	"github.com/kamalyes/go-logger"
)

// Publisher 事件发布器接口
type Publisher interface {
	// GetPubSub 获取 PubSub 实例
	GetPubSub() *cachex.PubSub

	// GetLogger 获取日志器
	GetLogger() logger.ILogger

	// GetContext 获取上下文
	GetContext() context.Context

	// GetNodeID 获取节点ID
	GetNodeID() string
}
