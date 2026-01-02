/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-30 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-30 00:00:00
 * @FilePath: \go-wsc\exports_handler.go
 * @Description: Handler 模块类型导出 - 保持向后兼容
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"github.com/kamalyes/go-wsc/handler"
)

// ============================================
// Offline Message Handler - 离线消息处理器
// ============================================

// OfflineMessageHandler 离线消息处理器接口
type OfflineMessageHandler = handler.OfflineMessageHandler

// HybridOfflineMessageHandler 混合离线消息处理器
type HybridOfflineMessageHandler = handler.HybridOfflineMessageHandler

// NewHybridOfflineMessageHandler 创建混合离线消息处理器
var NewHybridOfflineMessageHandler = handler.NewHybridOfflineMessageHandler

// ============================================================================
// OfflineMessageHandler 方法导出 - 这些方法通过 OfflineMessageHandler 实例调用
// ============================================================================

// 注意：以下是 OfflineMessageHandler 接口的方法列表，通过实现类实例调用
// 例如：handler := wsc.NewHybridOfflineMessageHandler(redisClient, db, config)

// 离线消息管理方法：
// - StoreOfflineMessage(ctx context.Context, userID string, msg *HubMessage) error: 存储离线消息
// - GetOfflineMessages(ctx context.Context, userID string, limit int, cursor string) ([]*HubMessage, string, error): 获取离线消息
// - DeleteOfflineMessages(ctx context.Context, userID string, messageIDs []string) error: 删除离线消息
// - GetOfflineMessageCount(ctx context.Context, userID string) (int64, error): 获取离线消息数量
// - ClearOfflineMessages(ctx context.Context, userID string) error: 清空离线消息
// - MarkAsPushed(ctx context.Context, messageIDs []string) error: 标记为已推送

