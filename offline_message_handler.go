/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-20 10:55:18
 * @FilePath: \go-wsc\offline_message_handler.go
 * @Description: 离线消息处理器扩展 - 自动存储和推送离线消息
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ============================================================================
// 离线消息处理器接口
// ============================================================================

// OfflineMessageRepository 离线消息仓库接口
type OfflineMessageRepository interface {
	// StoreOfflineMessage 存储离线消息
	StoreOfflineMessage(ctx context.Context, userID string, msg *HubMessage) error

	// GetOfflineMessages 获取用户的离线消息
	// limit: 限制返回数量
	//   - > 0: 最多返回指定数量
	//   - <= 0: Redis全部读取，MySQL最多1万条
	// cursor: 游标，用于分页和保证时序
	//   - Redis: 传空字符串表示从头开始
	//   - MySQL: 传上次返回的最后一条消息ID
	// 返回: messages, nextCursor, error
	//   - nextCursor为空表示没有更多数据
	GetOfflineMessages(ctx context.Context, userID string, limit int, cursor string) ([]*HubMessage, string, error)

	// DeleteOfflineMessages 删除已推送的离线消息
	DeleteOfflineMessages(ctx context.Context, userID string, messageIDs []string) error

	// GetOfflineMessageCount 获取离线消息数量
	GetOfflineMessageCount(ctx context.Context, userID string) (int64, error)

	// ClearOfflineMessages 清空用户的所有离线消息
	ClearOfflineMessages(ctx context.Context, userID string) error

	// MarkAsPushed 标记消息为已推送
	MarkAsPushed(ctx context.Context, messageIDs []string) error
}

// ============================================================================
// 混合存储实现（Redis 队列 + MySQL 持久化）
// ============================================================================

// HybridOfflineMessageHandler 混合离线消息处理器
// 使用 Redis 队列存储短期离线消息（性能优先，快速推送）
// 使用 MySQL offline_messages 表持久化（数据安全，防止 Redis 数据丢失）
// 注意：Redis 和 MySQL 必须同时初始化，双保险存储
type HybridOfflineMessageHandler struct {
	queueRepo MessageQueueRepository     // Redis 队列仓库（必需）
	dbRepo    OfflineMessageDBRepository // MySQL 离线消息仓库（必需）
	logger    WSCLogger                  // 日志器
	keyPrefix string                     // Redis key 前缀
}

// HybridOfflineMessageConfig 混合存储配置
type HybridOfflineMessageConfig struct {
	RedisClient redis.UniversalClient // Redis 客户端（必需）
	DB          *gorm.DB              // MySQL 数据库（必需）
	KeyPrefix   string                // Redis key 前缀，默认 "wsc:offline:"
	QueueTTL    time.Duration         // Redis 队列过期时间，默认 7 天
	Logger      WSCLogger             // 日志器（可选）
}

// NewHybridOfflineMessageHandler 创建混合离线消息处理器
// 参数:
//   - redisClient: Redis 客户端（必需）
//   - db: GORM 数据库（必需）
//   - config: 离线消息配置对象
func NewHybridOfflineMessageHandler(redisClient redis.UniversalClient, db *gorm.DB, config *wscconfig.OfflineMessage) OfflineMessageRepository {
	// 强制检查必需参数
	if redisClient == nil {
		panic("HybridOfflineMessageHandler: RedisClient is required")
	}
	if db == nil {
		panic("HybridOfflineMessageHandler: DB is required")
	}

	// 设置默认值
	keyPrefix := mathx.IF(config.KeyPrefix != "", config.KeyPrefix, "wsc:offline:messages:")
	queueTTL := mathx.IF(config.QueueTTL != 0, config.QueueTTL, 7*24*time.Hour)

	handler := &HybridOfflineMessageHandler{
		queueRepo: NewRedisMessageQueueRepository(redisClient, keyPrefix, queueTTL),
		dbRepo:    NewGormOfflineMessageRepository(db),
		logger:    NewDefaultWSCLogger(),
		keyPrefix: keyPrefix,
	}

	return handler
}

// StoreOfflineMessage 存储离线消息
func (h *HybridOfflineMessageHandler) StoreOfflineMessage(ctx context.Context, userID string, msg *HubMessage) error {
	if msg == nil {
		return fmt.Errorf("message is nil")
	}

	// 过滤不需要存储的消息类型
	if h.shouldSkipOfflineStorage(userID, msg) {
		return nil
	}

	var errs []error

	// 1. 存储到 Redis 队列
	if err := h.storeToRedis(ctx, userID, msg); err != nil {
		errs = append(errs, err)
	}

	// 2. 持久化到 MySQL
	if err := h.storeToDatabase(ctx, msg); err != nil {
		errs = append(errs, err)
	}

	// 至少有一个存储成功即可
	if len(errs) >= 2 {
		return fmt.Errorf("both storage failed: %v", errs)
	}

	return nil
}

// shouldSkipOfflineStorage 判断是否应该跳过离线存储
func (h *HybridOfflineMessageHandler) shouldSkipOfflineStorage(userID string, msg *HubMessage) bool {
	if msg.MessageType.IsSystemType() {
		h.logger.DebugKV("跳过系统消息的离线存储",
			"user_id", userID,
			"message_id", msg.ID,
			"sender", msg.Sender,
			"sender_type", msg.SenderType,
			"message_type", msg.MessageType,
		)
		return true
	}
	return false
}

// storeToRedis 存储到 Redis 队列
func (h *HybridOfflineMessageHandler) storeToRedis(ctx context.Context, userID string, msg *HubMessage) error {
	if err := h.queueRepo.Enqueue(ctx, userID, msg); err != nil {
		h.logger.ErrorKV("存储离线消息到 Redis 失败",
			"user_id", userID,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return fmt.Errorf("redis queue: %w", err)
	}

	h.logger.DebugKV("离线消息已存储到 Redis",
		"user_id", userID,
		"id", msg.ID,
		"message_id", msg.MessageID,
	)
	return nil
}

// storeToDatabase 持久化到 MySQL 数据库
func (h *HybridOfflineMessageHandler) storeToDatabase(ctx context.Context, msg *HubMessage) error {
	compressedData, dataSize, err := zipx.ZlibCompressObjectWithSize(msg)
	if err != nil {
		h.logger.ErrorKV("压缩消息失败",
			"user_id", msg.Receiver,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return fmt.Errorf("compress message: %w", err)
	}

	compressedSize := len(compressedData)
	compressionRatio := float64(compressedSize) / float64(dataSize) * 100

	record := &OfflineMessageRecord{
		MessageID:      msg.MessageID, // 业务消息ID
		HubID:          msg.ID,        // Hub 内部ID
		Sender:         msg.Sender,
		Receiver:       msg.Receiver,
		SessionID:      msg.SessionID,
		CompressedData: compressedData,
		ScheduledAt:    msg.CreateAt,
		ExpireAt:       msg.CreateAt.Add(7 * 24 * time.Hour), // 7天后过期
		CreatedAt:      time.Now(),
	}

	if err := h.dbRepo.Save(ctx, record); err != nil {
		h.logger.ErrorKV("持久化离线消息到 MySQL offline_messages 表失败",
			"user_id", msg.Receiver,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return fmt.Errorf("mysql: %w", err)
	}

	h.logger.DebugKV("离线消息已持久化到 MySQL offline_messages 表",
		"user_id", msg.Receiver,
		"id", msg.ID,
		"message_id", msg.MessageID,
		"data_size", dataSize,
		"compressed_size", compressedSize,
		"compression_ratio", fmt.Sprintf("%.2f%%", compressionRatio),
	)
	return nil
}

// GetOfflineMessages 获取用户的离线消息
// 参数:
//   - userID: 用户ID
//   - limit: 限制返回数量
//   - > 0: 最多返回指定数量的消息
//   - <= 0: Redis 全部读取, MySQL 最多返回 1 万条
//   - cursor: 游标，用于分页
//   - Redis: 忽略（Redis是FIFO队列，始终从头取）
//   - MySQL: 传上次返回的最后一条消息ID，继续向后读取
//
// 返回:
//   - messages: 消息列表
//   - nextCursor: 下一页游标，空字符串表示没有更多数据
//   - error: 错误信息
func (h *HybridOfflineMessageHandler) GetOfflineMessages(ctx context.Context, userID string, limit int, cursor string) ([]*HubMessage, string, error) {
	messages := make([]*HubMessage, 0)
	nextCursor := ""

	// 1. 优先从 Redis 队列读取（性能更好）
	// Redis 是 FIFO 队列，不支持游标，始终从头取
	length, err := h.queueRepo.GetLength(ctx, userID)
	if err != nil {
		h.logger.ErrorKV("获取离线消息队列长度失败",
			"user_id", userID,
			"error", err,
		)
		return messages, nextCursor, err
	}

	// 如果 Redis 有消息，忽略 cursor，直接从队列头部读取
	if length > 0 {
		count := mathx.IF(limit > 0, min(int(length), limit), int(length))

		for i := 0; i < count; i++ {
			msg, err := h.queueRepo.Dequeue(ctx, userID, 1*time.Second)
			if err != nil {
				h.logger.ErrorKV("从队列读取离线消息失败",
					"user_id", userID,
					"error", err,
				)
				break
			}
			if msg != nil {
				messages = append(messages, msg)
			}
		}

		// Redis 队列还有剩余，返回特殊游标 "redis:continue"
		remaining := length - int64(len(messages))
		if remaining > 0 {
			nextCursor = "redis:continue"
		}

		h.logger.InfoKV("从 Redis 读取离线消息",
			"user_id", userID,
			"count", len(messages),
			"remaining", remaining,
			"next_cursor", nextCursor,
		)

		return messages, nextCursor, nil
	}

	// 2. Redis 无消息，从 MySQL offline_messages 表读取
	h.logger.DebugKV("Redis 无离线消息，尝试从 MySQL offline_messages 表读取",
		"user_id", userID,
		"cursor", cursor,
	)

	records, err := h.dbRepo.GetByReceiver(ctx, userID, limit, cursor)
	if err != nil {
		h.logger.ErrorKV("从 MySQL 读取离线消息失败",
			"user_id", userID,
			"cursor", cursor,
			"error", err,
		)
		return messages, nextCursor, err
	}

	// 转换 OfflineMessageRecord 为 HubMessage
	for _, record := range records {
		msg, err := zipx.ZlibDecompressObject[*HubMessage](record.CompressedData)
		if err != nil {
			h.logger.ErrorKV("解压离线消息失败",
				"message_id", record.MessageID,
				"user_id", userID,
				"error", err,
			)
			continue
		}
		messages = append(messages, msg)
	}

	// 如果返回数量等于 limit，说明可能还有更多数据
	if len(records) >= limit && len(messages) > 0 {
		// 使用最后一条消息的 message_id 作为下一页游标
		nextCursor = records[len(records)-1].MessageID
	}

	h.logger.InfoKV("从 MySQL 读取离线消息",
		"user_id", userID,
		"count", len(messages),
		"limit", limit,
		"cursor", cursor,
		"next_cursor", nextCursor,
	)

	return messages, nextCursor, nil
}

// DeleteOfflineMessages 删除已推送的离线消息
func (h *HybridOfflineMessageHandler) DeleteOfflineMessages(ctx context.Context, userID string, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	// Redis 队列是先进先出，已经 Dequeue 的消息自动删除
	// 这里主要处理 MySQL 的消息删除

	if err := h.dbRepo.DeleteByMessageIDs(ctx, userID, messageIDs); err != nil {
		h.logger.ErrorKV("从 MySQL offline_messages 表删除离线消息失败",
			"user_id", userID,
			"count", len(messageIDs),
			"error", err,
		)
		return err
	}

	h.logger.DebugKV("从 MySQL offline_messages 表删除离线消息成功",
		"user_id", userID,
		"count", len(messageIDs),
	)

	return nil
}

// GetOfflineMessageCount 获取离线消息数量
func (h *HybridOfflineMessageHandler) GetOfflineMessageCount(ctx context.Context, userID string) (int64, error) {
	// 优先从 Redis 获取（速度快）
	redisCount, err := h.queueRepo.GetLength(ctx, userID)
	if err == nil && redisCount > 0 {
		return redisCount, nil
	}

	// Redis 无数据时从 MySQL 获取
	mysqlCount, err := h.dbRepo.GetCountByReceiver(ctx, userID)
	if err != nil {
		h.logger.ErrorKV("从 MySQL 获取离线消息数量失败",
			"user_id", userID,
			"error", err,
		)
		return 0, err
	}

	return mysqlCount, nil
}

// ClearOfflineMessages 清空用户的所有离线消息
func (h *HybridOfflineMessageHandler) ClearOfflineMessages(ctx context.Context, userID string) error {
	var errs []error

	// 1. 清空 Redis 队列
	if err := h.queueRepo.Clear(ctx, userID); err != nil {
		errs = append(errs, fmt.Errorf("redis: %w", err))
		h.logger.ErrorKV("清空 Redis 离线消息队列失败",
			"user_id", userID,
			"error", err,
		)
	}

	// 2. 清空 MySQL offline_messages 表
	if err := h.dbRepo.ClearByReceiver(ctx, userID); err != nil {
		errs = append(errs, fmt.Errorf("mysql: %w", err))
		h.logger.ErrorKV("清空 MySQL offline_messages 表失败",
			"user_id", userID,
			"error", err,
		)
	} else {
		h.logger.DebugKV("清空 MySQL offline_messages 表成功",
			"user_id", userID,
		)
	}

	if len(errs) > 0 {
		return fmt.Errorf("clear offline messages failed: %v", errs)
	}

	return nil
}

// MarkAsPushed 标记消息为已推送
// 注意：此方法应该在消息成功推送给用户后调用
// 由于消息已通过 GetOfflineMessages 从 Redis 出队,这里只需标记 MySQL
func (h *HybridOfflineMessageHandler) MarkAsPushed(ctx context.Context, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	// 标记 MySQL 中的消息为已推送
	// 这样即使推送失败,消息仍在 MySQL 中,下次连接时可以从 MySQL 读取并重试
	if err := h.dbRepo.MarkAsPushed(ctx, messageIDs); err != nil {
		h.logger.ErrorKV("标记 MySQL 消息为已推送失败",
			"count", len(messageIDs),
			"error", err,
		)
		return fmt.Errorf("mysql: %w", err)
	}

	h.logger.DebugKV("标记消息为已推送成功",
		"count", len(messageIDs),
	)
	return nil
}
