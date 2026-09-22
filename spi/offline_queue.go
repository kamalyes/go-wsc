/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 09:33:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 09:33:00
 * @FilePath: \go-wsc\spi\offline_queue.go
 * @Description: 离线消息队列 SPI - OfflineQueue 接口契约定义
 *
 * 离线消息的存取/排空/推送状态维护契约
 * Redis 队列 + RDBMS 双写实现见 messaging 包 HybridOfflineMessageHandler
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
)

// OfflineQueue 离线消息处理器接口（业务逻辑层）
//
// 存储维度：按 (appID, namespace, groupID, userID) 四元组隔离
//   - Redis 队列 key = "{prefix}{appID}:{ns}:{groupID}:{userID}"（P2P 消息 groupID 为空）
//   - MySQL 记录带 app_id + namespace + group_id + receiver 列
//
// appID 是最上层隔离维度（默认 "__default_app__"），namespace + groupID 从 ctx 路由元数据提取
// （hub 层 routing.NewRoute().WithAppID(...).Inject(ctx) 注入）appID 严格隔离，跨 app 不串扰
type OfflineQueue interface {
	// StoreOfflineMessage 存储离线消息（双写 Redis + MySQL）
	// 落入 ctx 的 (ns, group) 对应的分区：Redis key = ns:group:userID
	StoreOfflineMessage(ctx context.Context, userID string, msg *models.HubMessage) error

	// DrainOfflineQueue 排空 Redis 队列（单组 FIFO，仅 Redis，不触及 MySQL）
	// 按 ctx 的单个 (ns, group) 出队，limit<=0 表示一次取尽
	// 调用方按组循环调用（hub replay 枚举用户所有 group + P2P）
	DrainOfflineQueue(ctx context.Context, userID string, limit int) ([]*models.HubMessage, error)

	// GetOfflineMessages 查询 MySQL 离线消息（跨组，命名空间内该用户全部 group）
	// limit: >0 最多返回指定数量；<=0 最多 1 万条
	// cursor: 上次返回的最后一条 message_id，空串表示从头
	// 返回 nextCursor 为空表示无更多数据
	GetOfflineMessages(ctx context.Context, userID string, limit int, cursor string) ([]*models.HubMessage, string, error)

	// DeleteOfflineMessages 按消息ID删除已推送的离线消息（MySQL，跨组按 message_id 删）
	DeleteOfflineMessages(ctx context.Context, userID string, messageIDs []string) error

	// GetOfflineMessageCount 获取离线消息数量（MySQL 跨组计数，MySQL 为双写超集故计数准确）
	GetOfflineMessageCount(ctx context.Context, userID string) (int64, error)

	// ClearOfflineMessages 清空用户的离线消息
	// groupIDs: 用户在该命名空间下的全部 group + "" (P2P)，逐组清 Redis + 一次清 MySQL
	ClearOfflineMessages(ctx context.Context, userID string, groupIDs []string) error

	// UpdatePushStatus 更新离线消息推送状态
	// pushErr: 推送结果错误,nil表示成功,非nil表示失败
	UpdatePushStatus(ctx context.Context, messageIDs []string, pushErr error) error
}
