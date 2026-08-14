/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-30 00:11:25
 * @FilePath: \go-wsc\handler\offline_message.go
 * @Description: 离线消息处理器 - 业务逻辑层，负责离线消息的存储、推送、删除等操作
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package handler

import (
	"context"
	"fmt"
	"sync"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ============================================================================
// 离线消息处理器接口
// ============================================================================

// OfflineMessageHandler 离线消息处理器接口（业务逻辑层）
//
// 存储维度：按 (namespace, groupID, userID) 三元组隔离。
//   - Redis 队列 key = "{prefix}{ns}:{groupID}:{userID}"（P2P 消息 groupID 为空）
//   - MySQL 记录带 namespace + group_id + receiver 列
//
// namespace + groupID 一律从 ctx 路由元数据提取（hub 层 WithNamespaceGroupIDs 注入）
type OfflineMessageHandler interface {
	// StoreOfflineMessage 存储离线消息（双写 Redis + MySQL）
	// 落入 ctx 的 (ns, group) 对应的分区：Redis key = ns:group:userID
	StoreOfflineMessage(ctx context.Context, userID string, msg *HubMessage) error

	// DrainOfflineQueue 排空 Redis 队列（单组 FIFO，仅 Redis，不触及 MySQL）
	// 按 ctx 的单个 (ns, group) 出队，limit<=0 表示一次取尽
	// 调用方按组循环调用（hub replay 枚举用户所有 group + P2P）
	DrainOfflineQueue(ctx context.Context, userID string, limit int) ([]*HubMessage, error)

	// GetOfflineMessages 查询 MySQL 离线消息（跨组，命名空间内该用户全部 group）
	// limit: >0 最多返回指定数量；<=0 最多 1 万条
	// cursor: 上次返回的最后一条 message_id，空串表示从头
	// 返回 nextCursor 为空表示无更多数据
	GetOfflineMessages(ctx context.Context, userID string, limit int, cursor string) ([]*HubMessage, string, error)

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

// ============================================================================
// 混合存储实现（Redis 队列 + MySQL 持久化）
// ============================================================================

// HybridOfflineMessageHandler 混合离线消息处理器
// 使用 Redis 队列存储短期离线消息（性能优先，快速推送）
// 使用 MySQL offline_messages 表持久化（数据安全，防止 Redis 数据丢失）
// 注意：Redis 和 MySQL 必须同时初始化，双保险存储
type HybridOfflineMessageHandler struct {
	queueRepo  MessageQueueRepository     // Redis 队列仓库（必需）
	dbRepo     OfflineMessageDBRepository // MySQL 离线消息仓库（必需）
	logger     WSCLogger                  // 日志器
	keyPrefix  string                     // Redis key 前缀
	messageTTL time.Duration              // 离线消息过期时间
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
//   - log: 日志记录器
func NewHybridOfflineMessageHandler(redisClient redis.UniversalClient, db *gorm.DB, config *wscconfig.OfflineMessage, log WSCLogger) OfflineMessageHandler {
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

	// 如果没有传入 logger,使用默认的
	if log == nil {
		log = NewDefaultWSCLogger()
	}

	handler := &HybridOfflineMessageHandler{
		queueRepo:  NewRedisMessageQueueRepository(redisClient, keyPrefix, queueTTL),
		dbRepo:     NewGormOfflineMessageRepository(db, config, log),
		logger:     log,
		keyPrefix:  keyPrefix,
		messageTTL: queueTTL, // 使用 QueueTTL 作为消息过期时间
	}

	return handler
}

// StoreOfflineMessage 存储离线消息
//
// 多端登录场景说明：
// 当用户有多个设备（如ABC三个设备）时：
// - 如果ABC都离线：存储离线消息，任一设备上线时推送
// - 如果AB在线C离线：消息已发送到AB，**不存储**离线消息，C上线后通过历史记录接口同步
//
// 核心原则：
// - **只有用户所有设备都离线时**，才存储离线消息并主动推送
// - 有任何设备在线，消息已通过WebSocket实时送达，其他设备通过拉取历史记录获取
// - 离线消息存储是基于用户维度的，用于在用户完全离线期间保证消息不丢失
//
// 去重机制：
// - 通过message_id保证消息唯一性（数据库unique索引）
// - 如果同一条消息重复存储，数据库层面会报错，但不影响功能
//
// 性能优化：Redis 和 MySQL 双写并行化
//   - 原实现：顺序执行 storeToRedis + storeToDatabase，延迟 = T(Redis) + T(MySQL)
//   - 现实现：并行执行，延迟 = max(T(Redis), T(MySQL))
//   - msg 为只读（两个 goroutine 仅读取字段，无并发修改）
//   - queueRepo（Redis 客户端）和 dbRepo（GORM 连接池）均为并发安全
func (h *HybridOfflineMessageHandler) StoreOfflineMessage(ctx context.Context, userID string, msg *HubMessage) error {
	if msg == nil {
		return errorx.WrapError("message is nil")
	}

	// 过滤不需要存储的消息类型
	if h.shouldSkipOfflineStorage(userID, msg) {
		return nil
	}

	// 并行执行 Redis + MySQL 双写（无共享状态，各写各的错误变量）
	var redisErr, dbErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		redisErr = h.storeToRedis(ctx, userID, msg)
	}()

	go func() {
		defer wg.Done()
		dbErr = h.storeToDatabase(ctx, msg)
	}()

	wg.Wait()

	// 至少有一个存储成功即可（与原逻辑一致）
	if redisErr != nil && dbErr != nil {
		return errorx.WrapError("both storage failed", fmt.Errorf("%v", []error{redisErr, dbErr}))
	}

	return nil
}

// shouldSkipOfflineStorage 判断是否应该跳过离线存储
func (h *HybridOfflineMessageHandler) shouldSkipOfflineStorage(userID string, msg *HubMessage) bool {
	// 过滤系统消息
	if msg.MessageType.IsSystemType() {
		h.logger.DebugKV("跳过系统消息的离线存储",
			"user_id", userID,
			"message_id", msg.MessageID,
			"sender", msg.Sender,
			"sender_type", msg.SenderType,
			"message_type", msg.MessageType,
		)
		return true
	}
	return false
}

// normalizeGroupID 归一化存储维度的 groupID：P2P（空）补 DefaultGroupID
// 保证 Redis 队列 key 与 MySQL group_id 维度一致，三段非空格式统一
// 与 client.GetGroupID() 归一化对齐（注册时空 group 已归一化为 DefaultGroupID）
func normalizeGroupID(groupID string) string {
	return mathx.IfEmpty(groupID, models.DefaultGroupID)
}

// queueKey 构造命名空间+群组隔离的 Redis 队列名：ns:group:userID
// 纯参数构造（不依赖 ctx），避免异步队列消费时 ctx 路由元数据丢失导致串扰
// groupID 为空（P2P）补 DefaultGroupID，三段非空；namespace 传真实值（不补默认）
//   - 完整群组消息：ns:group:userID
//   - 点对点消息：ns:__default_gp__:userID（groupID 补默认组）
func queueKey(ns, groupID, userID string) string {
	return ns + ":" + groupID + ":" + userID
}

// resolveOfflineRoute 从 msg 信封提取存储路由（写入路径专用，优先 msg 信封，ctx 仅作极端兜底）
// 异步队列消费时 ctx 会丢失路由，因此 ns 和 firstGroupID 优先从 msg 自带信封读取：
//   - ns:          msg.Namespace（空串保持空，不补 default）
//   - firstGroupID: msg.FirstGroupID()，P2P（GroupIDs=nil/空）返回 ""，调用方后续 normalizeGroupID 补默认
func resolveOfflineRoute(ctx context.Context, msg *HubMessage) (ns string, firstGroupID string) {
	ns = msg.Namespace
	firstGroupID = msg.FirstGroupID()
	// 兜底：如果 msg 信封完全为空（理论上入口已注入，仅作最后防护），从 ctx 恢复
	if ns == "" && firstGroupID == "" {
		ns = routing.NamespaceFromContext(ctx)
		firstGroupID = routing.FirstGroupIDFromContext(ctx)
	}
	return
}

// storeToRedis 存储到 Redis 队列（按 ns:group:userID 分区）
// ⚠️ 路由必须从 msg 信封读取（不依赖 ctx）：异步队列消费时 ctx 路由会丢失
func (h *HybridOfflineMessageHandler) storeToRedis(ctx context.Context, userID string, msg *HubMessage) error {
	ns, firstGID := resolveOfflineRoute(ctx, msg)
	key := queueKey(ns, normalizeGroupID(firstGID), userID)
	if err := h.queueRepo.Enqueue(ctx, key, msg); err != nil {
		h.logger.ErrorKV("存储离线消息到 Redis 失败",
			"user_id", userID,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"namespace", ns,
			"group_id", firstGID,
			"error", err,
		)
		return errorx.WrapError("redis queue", err)
	}

	h.logger.DebugKV("离线消息已存储到 Redis",
		"user_id", userID,
		"id", msg.ID,
		"message_id", msg.MessageID,
		"namespace", ns,
		"group_id", firstGID,
	)
	return nil
}

// DrainOfflineQueue 排空 Redis 队列（单组 FIFO，仅 Redis）
// 按 ctx 的单个 (ns, group) 出队；limit<=0 表示一次取尽该队列
func (h *HybridOfflineMessageHandler) DrainOfflineQueue(ctx context.Context, userID string, limit int) ([]*HubMessage, error) {
	// 同步流程（用户上线回放）：ctx 含有完整路由（客户端注册时注入）
	ns := routing.NamespaceFromContext(ctx)
	firstGID := routing.FirstGroupIDFromContext(ctx)
	key := queueKey(ns, normalizeGroupID(firstGID), userID)

	count := limit
	if count <= 0 {
		length, err := h.queueRepo.GetLength(ctx, key)
		if err != nil {
			h.logger.ErrorKV("获取离线队列长度失败",
				"user_id", userID,
				"queue", key,
				"error", err,
			)
			return nil, err
		}
		count = int(length)
	}
	if count <= 0 {
		return nil, nil
	}

	msgs, err := h.queueRepo.DequeueBatch(ctx, key, count)
	if err != nil {
		h.logger.ErrorKV("排空离线队列失败",
			"user_id", userID,
			"queue", key,
			"count", count,
			"error", err,
		)
		return nil, err
	}
	return msgs, nil
}

// storeToDatabase 持久化到 MySQL 数据库
// ⚠️ 路由必须从 msg 信封读取（不依赖 ctx）：异步队列消费时 ctx 路由会丢失
func (h *HybridOfflineMessageHandler) storeToDatabase(ctx context.Context, msg *HubMessage) error {
	compressedData, dataSize, err := zipx.ZlibCompressObjectWithSize(msg)
	if err != nil {
		h.logger.ErrorKV("压缩消息失败",
			"user_id", msg.Receiver,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return errorx.WrapError("compress message", err)
	}

	compressedSize := len(compressedData)
	compressionRatio := float64(compressedSize) / float64(dataSize) * 100

	// 从 msg 信封提取路由元数据（异步队列 ctx 丢路由，以 msg 信封为准）
	// namespace 直接取真实值（不做默认值归一化，没有就是空串）
	// groupID 取首个并归一化（P2P 补 DefaultGroupID，与 Redis key 维度一致）
	ns, firstGID := resolveOfflineRoute(ctx, msg)
	namespace := ns
	groupID := normalizeGroupID(firstGID)

	record := &OfflineMessageRecord{
		MessageID:      msg.MessageID, // 业务消息ID
		Sender:         msg.Sender,
		Receiver:       msg.Receiver,
		Namespace:      namespace, // 真实命名空间（ctx 路由元数据，没有就是空串）
		GroupID:        groupID,   // 群组ID（P2P 补 DefaultGroupID，与 Redis key 维度一致）
		SessionID:      msg.SessionID,
		CompressedData: compressedData,
		ScheduledAt:    msg.CreateAt,
		ExpireAt:       msg.CreateAt.Add(h.messageTTL), // 使用配置的过期时间
		CreatedAt:      time.Now(),
	}

	if err := h.dbRepo.Save(ctx, record); err != nil {
		h.logger.ErrorKV("持久化离线消息到 MySQL offline_messages 表失败",
			"user_id", msg.Receiver,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return errorx.WrapError("mysql", err)
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

// GetOfflineMessages 查询 MySQL 离线消息（跨组：命名空间内该用户全部 group）
// Redis 队列由 DrainOfflineQueue 单独排空；本方法只负责 MySQL 持久层的分页查询
//
// 参数:
//   - userID: 用户ID
//   - limit: >0 最多返回指定数量；<=0 最多 1 万条
//   - cursor: 上次返回的最后一条 message_id，空串从头开始
//
// 返回 nextCursor 为空表示无更多数据
func (h *HybridOfflineMessageHandler) GetOfflineMessages(ctx context.Context, userID string, limit int, cursor string) ([]*HubMessage, string, error) {
	messages := make([]*HubMessage, 0)
	nextCursor := ""

	// namespace 从 ctx 路由元数据提取（用户上线回放时由 hub 注入 client.Namespace）
	// 直接取真实值，不做默认值归一化；GroupID 留空 → 跨组查询该命名空间内全部 group 的离线消息
	namespace := routing.NamespaceFromContext(ctx)

	records, err := h.dbRepo.QueryMessages(ctx, &OfflineMessageFilter{
		UserID:    userID,
		Role:      MessageRoleReceiver,
		Namespace: namespace,
		Limit:     limit,
		Cursor:    cursor,
	})
	if err != nil {
		h.logger.ErrorKV("从 MySQL 读取离线消息失败",
			"user_id", userID,
			"namespace", namespace,
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

	// 返回数量达到 limit，可能还有更多数据，用最后一条 message_id 作下一页游标
	if limit > 0 && len(records) >= limit && len(records) > 0 {
		nextCursor = records[len(records)-1].MessageID
	}

	h.logger.InfoKV("从 MySQL 读取离线消息",
		"user_id", userID,
		"namespace", namespace,
		"count", len(messages),
		"limit", limit,
		"cursor", cursor,
		"next_cursor", nextCursor,
	)

	return messages, nextCursor, nil
}

// DeleteOfflineMessages 删除已推送的离线消息（namespace 从 ctx 提取）
func (h *HybridOfflineMessageHandler) DeleteOfflineMessages(ctx context.Context, userID string, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	// Redis 队列是先进先出，已经 Dequeue 的消息自动删除
	// 这里主要处理 MySQL 的消息删除（按命名空间隔离，跨组按 message_id 删）
	namespace := routing.NamespaceFromContext(ctx)

	if err := h.dbRepo.DeleteByMessageIDs(ctx, namespace, userID, messageIDs); err != nil {
		h.logger.ErrorKV("从 MySQL offline_messages 表删除离线消息失败",
			"user_id", userID,
			"namespace", namespace,
			"count", len(messageIDs),
			"error", err,
		)
		return err
	}

	h.logger.DebugKV("从 MySQL offline_messages 表删除离线消息成功",
		"user_id", userID,
		"namespace", namespace,
		"count", len(messageIDs),
	)

	return nil
}

// GetOfflineMessageCount 获取离线消息数量（MySQL 跨组计数）
// MySQL 为双写超集（含 Redis 内 + Redis 已过期的全部待推送消息），故直接以 MySQL 计数为准
func (h *HybridOfflineMessageHandler) GetOfflineMessageCount(ctx context.Context, userID string) (int64, error) {
	namespace := routing.NamespaceFromContext(ctx)

	count, err := h.dbRepo.GetCountByReceiver(ctx, namespace, userID)
	if err != nil {
		h.logger.ErrorKV("从 MySQL 获取离线消息数量失败",
			"user_id", userID,
			"namespace", namespace,
			"error", err,
		)
		return 0, err
	}

	return count, nil
}

// ClearOfflineMessages 清空用户的离线消息
// groupIDs: 用户在该命名空间下的全部 group（含 "" 表示 P2P 队列），逐组清 Redis + 一次清 MySQL
func (h *HybridOfflineMessageHandler) ClearOfflineMessages(ctx context.Context, userID string, groupIDs []string) error {
	namespace := routing.NamespaceFromContext(ctx)

	var errs []error

	// 1. 逐组清空 Redis 队列（Redis 按 ns:group:userID 分区，P2P 的空 group 补 DefaultGroupID）
	for _, groupID := range groupIDs {
		key := namespace + ":" + normalizeGroupID(groupID) + ":" + userID
		if err := h.queueRepo.Clear(ctx, key); err != nil {
			errs = append(errs, errorx.WrapError("redis", err))
			h.logger.ErrorKV("清空 Redis 离线消息队列失败",
				"user_id", userID,
				"queue", key,
				"error", err,
			)
		}
	}

	// 2. 清空 MySQL offline_messages 表（按命名空间隔离，跨组一次清完）
	if err := h.dbRepo.ClearByReceiver(ctx, namespace, userID); err != nil {
		errs = append(errs, errorx.WrapError("mysql", err))
		h.logger.ErrorKV("清空 MySQL offline_messages 表失败",
			"user_id", userID,
			"namespace", namespace,
			"error", err,
		)
	} else {
		h.logger.DebugKV("清空 MySQL offline_messages 表成功",
			"user_id", userID,
			"namespace", namespace,
		)
	}

	if len(errs) > 0 {
		return errorx.WrapError("clear offline messages failed", fmt.Errorf("%v", errs))
	}

	return nil
}

// UpdatePushStatus 更新离线消息推送状态
// pushErr为nil表示推送成功,非nil表示推送失败
func (h *HybridOfflineMessageHandler) UpdatePushStatus(ctx context.Context, messageIDs []string, pushErr error) error {
	if len(messageIDs) == 0 {
		return nil
	}

	// 根据pushErr自动判断状态
	var status MessageSendStatus
	var errorMsg string
	if pushErr == nil {
		status = MessageSendStatusSuccess
	} else {
		status = MessageSendStatusFailed
		errorMsg = pushErr.Error()
	}

	if err := h.dbRepo.UpdatePushStatus(ctx, messageIDs, status, errorMsg); err != nil {
		h.logger.ErrorKV("更新离线消息推送状态失败",
			"count", len(messageIDs),
			"status", status,
			"error", err,
		)
		return fmt.Errorf("update push status: %w", err)
	}

	h.logger.DebugKV("更新离线消息推送状态",
		"count", len(messageIDs),
		"status", status,
	)
	return nil
}
