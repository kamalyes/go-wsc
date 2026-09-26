/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-30 00:11:25
 * @FilePath: \go-wsc\messaging\offline.go
 * @Description: 离线消息处理器 - 业务逻辑层，负责离线消息的存储、推送、删除等操作
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"fmt"
	"sync"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/routing"

	"github.com/kamalyes/go-wsc/models"

	"github.com/kamalyes/go-wsc/spi"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ============================================================================
// 混合存储实现（Redis 队列 + MySQL 持久化）
// 实现契约见 spi.OfflineQueue（离线消息处理器接口已迁至 spi 包）
// ============================================================================

// HybridOfflineMessageHandler 混合离线消息处理器
// 使用 Redis 队列存储短期离线消息（性能优先，快速推送）
// 使用 RDBMS offline_messages 表持久化（数据安全，防止 Redis 数据丢失）
// 注意：Redis 和 MySQL 必须同时初始化，双保险存储
type HybridOfflineMessageHandler struct {
	queueRepo  spi.MessageQueue
	dbRepo     spi.OfflineStore // MySQL 离线消息仓库（必需）
	logger     spi.Logger
	keyPrefix  string        // Redis key 前缀
	messageTTL time.Duration // 离线消息过期时间
}

// HybridOfflineMessageConfig 混合存储配置
type HybridOfflineMessageConfig struct {
	RedisClient redis.UniversalClient // Redis 客户端（必需）
	DB          *gorm.DB              // MySQL 数据库（必需）
	KeyPrefix   string                // Redis key 前缀，默认 "wsc:offline:"
	QueueTTL    time.Duration         // Redis 队列过期时间，默认 7 天
	Logger      spi.Logger            // 日志器（可选）
}

// NewHybridOfflineMessageHandler 创建混合离线消息处理器
//
// 两个后端实现均由适配器注入 —— core 不构造任何具体存储实现，
// 依赖方向恒为 adapter → core。
//
// 参数:
//   - queue: Redis 队列实现（必需，由 adapter/redis 注入）
//   - store: RDBMS 持久化实现（必需，由 adapter/gorm 注入）
//   - config: 离线消息配置对象
//   - log: 日志记录器（nil 时使用默认）
func NewHybridOfflineMessageHandler(queue spi.MessageQueue, store spi.OfflineStore, config *wscconfig.OfflineMessage, log spi.Logger) *HybridOfflineMessageHandler {
	// 强制检查必需参数：双写两半都不可缺，缺任一半都会静默丢消息
	if queue == nil {
		panic("HybridOfflineMessageHandler: queue is required")
	}
	if store == nil {
		panic("HybridOfflineMessageHandler: offline store is required")
	}

	// 设置默认值
	keyPrefix := mathx.IF(config.KeyPrefix != "", config.KeyPrefix, "wsc:offline:messages:")
	queueTTL := mathx.IF(config.QueueTTL != 0, config.QueueTTL, 7*24*time.Hour)

	// 如果没有传入 logger,使用默认的
	if log == nil {
		log = spi.NewDefaultLogger()
	}

	handler := &HybridOfflineMessageHandler{
		queueRepo:  queue,
		dbRepo:     store,
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
func (h *HybridOfflineMessageHandler) StoreOfflineMessage(ctx context.Context, userID string, msg *models.HubMessage) error {
	if msg == nil {
		return errorx.WrapError("message is nil")
	}

	// 过滤不需要存储的消息类型
	if h.shouldSkipOfflineStorage(ctx, userID, msg) {
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
func (h *HybridOfflineMessageHandler) shouldSkipOfflineStorage(ctx context.Context, userID string, msg *models.HubMessage) bool {
	// 过滤系统消息
	if msg.MessageType.IsSystemType() {
		// trace 恢复：以消息信封 trace_id 为准，跳过日志可关联原始发送链路
		h.logger.DebugContextKV(msg.ContextFrom(ctx), "跳过系统消息的离线存储",
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

// queueKey 构造应用+命名空间+群组隔离的 Redis 队列名：appID:ns:group:userID
// 纯参数构造（不依赖 ctx、不做归一化），避免异步队列消费时 ctx 路由元数据丢失导致串扰
// 调用方负责归一化：appID 经 routing.AppIDFromContext（内部 constants.NormalizeAppID）补默认；
//
//	                groupID 经 constants.NormalizeGroupID 补 DefaultGroupID（P2P 空补默认组）
//	- 完整群组消息：appID:ns:group:userID
//	- 点对点消息：appID:ns:__default_gp__:userID（groupID 补默认组）
func queueKey(appID, ns, groupID, userID string) string {
	return appID + ":" + ns + ":" + groupID + ":" + userID
}

// resolveOfflineRoute 从 msg 信封提取存储路由（写入路径专用，优先 msg 信封，ctx 仅作极端兜底）
// 异步队列消费时 ctx 会丢失路由，因此 appID/ns/firstGroupID 优先从 msg 自带信封读取：
//   - appID:        msg.AppID（空补 DefaultAppID，最上层隔离维度必填）
//   - ns:           msg.Namespace（空串保持空，不补 default，与广播语义一致）
//   - firstGroupID: msg.FirstGroupID()，P2P（GroupIDs=nil/空）返回 ""，调用方后续 constants.NormalizeGroupID 补默认
//
// 归一化策略：appID 统一由末尾 constants.NormalizeAppID 收口（msg 信封这条未经 routing 的路径必须补默认）；
// ctx 兜底分支取 raw 路由（不经 routing.AppIDFromContext 二次归一化），保证"提取 raw → 末尾统一归一化"单一职责
func resolveOfflineRoute(ctx context.Context, msg *models.HubMessage) (appID, ns, firstGroupID string) {
	appID = msg.AppID
	ns = msg.Namespace
	firstGroupID = msg.FirstGroupID()
	// 兜底：msg 信封完全为空（理论上入口已注入，仅作最后防护），从 ctx 取 raw 路由
	// 取 raw 而非 constants.*FromContext，避免与末尾 constants.NormalizeAppID 形成二次归一化
	if appID == "" && ns == "" && firstGroupID == "" {
		if rc := routing.RoutingFromContext(ctx); rc != nil {
			appID = rc.AppID
			ns = rc.Namespace
			if len(rc.GroupIDs) > 0 {
				firstGroupID = rc.GroupIDs[0]
			}
		}
	}
	// appID 统一归一化收口（幂等：已归一化的值不变，msg 信封路径在此补 DefaultAppID）
	appID = constants.NormalizeAppID(appID)
	return
}

// storeToRedis 存储到 Redis 队列（按 appID:ns:group:userID 分区）
// 路由必须从 msg 信封读取（不依赖 ctx）：异步队列消费时 ctx 路由会丢失
func (h *HybridOfflineMessageHandler) storeToRedis(ctx context.Context, userID string, msg *models.HubMessage) error {
	appID, ns, firstGID := resolveOfflineRoute(ctx, msg)
	key := queueKey(appID, ns, constants.NormalizeGroupID(firstGID), userID)
	if err := h.queueRepo.Enqueue(ctx, key, msg); err != nil {
		h.logger.ErrorContextKV(msg.ContextFrom(ctx), "存储离线消息到 Redis 失败",
			"user_id", userID,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"app_id", appID,
			"namespace", ns,
			"group_id", firstGID,
			"error", err,
		)
		return errorx.WrapError("redis queue", err)
	}

	h.logger.DebugContextKV(msg.ContextFrom(ctx), "离线消息已存储到 Redis",
		"user_id", userID,
		"id", msg.ID,
		"message_id", msg.MessageID,
		"app_id", appID,
		"namespace", ns,
		"group_id", firstGID,
	)
	return nil
}

// DrainOfflineQueue 排空 Redis 队列（单组 FIFO，仅 Redis）
// 按 ctx 的单个 (appID, ns, group) 出队；limit<=0 表示一次取尽该队列
func (h *HybridOfflineMessageHandler) DrainOfflineQueue(ctx context.Context, userID string, limit int) ([]*models.HubMessage, error) {
	// 同步流程（用户上线回放）：ctx 含有完整路由（客户端注册时注入 appID+ns+group）
	appID := routing.AppIDFromContext(ctx)
	ns := routing.NamespaceFromContext(ctx)
	firstGID := routing.FirstGroupIDFromContext(ctx)
	key := queueKey(appID, ns, constants.NormalizeGroupID(firstGID), userID)

	count := limit
	if count <= 0 {
		length, err := h.queueRepo.GetLength(ctx, key)
		if err != nil {
			h.logger.ErrorContextKV(ctx, "获取离线队列长度失败",
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
		h.logger.ErrorContextKV(ctx, "排空离线队列失败",
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
// 路由必须从 msg 信封读取（不依赖 ctx）：异步队列消费时 ctx 路由会丢失
func (h *HybridOfflineMessageHandler) storeToDatabase(ctx context.Context, msg *models.HubMessage) error {
	compressedData, dataSize, err := zipx.ZlibCompressObjectWithSize(msg)
	if err != nil {
		h.logger.ErrorContextKV(msg.ContextFrom(ctx), "压缩消息失败",
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
	// appID 归一化补 DefaultAppID；namespace 直接取真实值（不做默认值归一化，没有就是空串）
	// groupID 取首个并归一化（P2P 补 DefaultGroupID，与 Redis key 维度一致）
	appID, ns, firstGID := resolveOfflineRoute(ctx, msg)
	namespace := ns
	groupID := constants.NormalizeGroupID(firstGID)

	record := &models.OfflineMessageRecord{
		MessageID:      msg.MessageID, // 业务消息ID
		Sender:         msg.Sender,
		Receiver:       msg.Receiver,
		AppID:          appID,     // 应用ID（归一化为 DefaultAppID，最上层隔离维度）
		Namespace:      namespace, // 真实命名空间（ctx 路由元数据，没有就是空串）
		GroupID:        groupID,   // 群组ID（P2P 补 DefaultGroupID，与 Redis key 维度一致）
		SessionID:      msg.SessionID,
		CompressedData: compressedData,
		ScheduledAt:    msg.CreateAt,
		ExpireAt:       msg.CreateAt.Add(h.messageTTL), // 使用配置的过期时间
		CreatedAt:      time.Now(),
	}

	if err := h.dbRepo.Save(ctx, record); err != nil {
		h.logger.ErrorContextKV(msg.ContextFrom(ctx), "持久化离线消息到 RDBMS offline_messages 表失败",
			"user_id", msg.Receiver,
			"id", msg.ID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return errorx.WrapError("mysql", err)
	}

	h.logger.DebugContextKV(msg.ContextFrom(ctx), "离线消息已持久化到 RDBMS offline_messages 表",
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
func (h *HybridOfflineMessageHandler) GetOfflineMessages(ctx context.Context, userID string, limit int, cursor string) ([]*models.HubMessage, string, error) {
	messages := make([]*models.HubMessage, 0)
	nextCursor := ""

	// appID/namespace 从 ctx 路由元数据提取（用户上线回放时由 hub 注入 client.AppID + client.Namespace）
	// appID 归一化补 DefaultAppID；namespace 直接取真实值，不做默认值归一化
	// GroupID 留空 → 跨组查询该 (appID, namespace) 内全部 group 的离线消息
	appID := routing.AppIDFromContext(ctx)
	namespace := routing.NamespaceFromContext(ctx)

	records, err := h.dbRepo.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID:    userID,
		Role:      spi.MessageRoleReceiver,
		AppID:     appID,
		Namespace: namespace,
		Limit:     limit,
		Cursor:    cursor,
	})
	if err != nil {
		h.logger.ErrorContextKV(ctx, "从 MySQL 读取离线消息失败",
			"user_id", userID,
			"app_id", appID,
			"namespace", namespace,
			"cursor", cursor,
			"error", err,
		)
		return messages, nextCursor, err
	}

	// 转换 OfflineMessageRecord 为 HubMessage
	for _, record := range records {
		msg, err := zipx.ZlibDecompressObject[*models.HubMessage](record.CompressedData)
		if err != nil {
			h.logger.ErrorContextKV(ctx, "解压离线消息失败",
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

	h.logger.InfoContextKV(ctx, "从 MySQL 读取离线消息",
		"user_id", userID,
		"app_id", appID,
		"namespace", namespace,
		"count", len(messages),
		"limit", limit,
		"cursor", cursor,
		"next_cursor", nextCursor,
	)

	return messages, nextCursor, nil
}

// DeleteOfflineMessages 删除已推送的离线消息（appID/namespace 从 ctx 提取）
func (h *HybridOfflineMessageHandler) DeleteOfflineMessages(ctx context.Context, userID string, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	// Redis 队列是先进先出，已经 Dequeue 的消息自动删除
	// 这里主要处理 MySQL 的消息删除（按应用+命名空间隔离，跨组按 message_id 删）
	appID := routing.AppIDFromContext(ctx)
	namespace := routing.NamespaceFromContext(ctx)

	if err := h.dbRepo.DeleteByMessageIDs(ctx, appID, namespace, userID, messageIDs); err != nil {
		h.logger.ErrorContextKV(ctx, "从 RDBMS offline_messages 表删除离线消息失败",
			"user_id", userID,
			"app_id", appID,
			"namespace", namespace,
			"count", len(messageIDs),
			"error", err,
		)
		return err
	}

	h.logger.DebugContextKV(ctx, "从 RDBMS offline_messages 表删除离线消息成功",
		"user_id", userID,
		"app_id", appID,
		"namespace", namespace,
		"count", len(messageIDs),
	)

	return nil
}

// GetOfflineMessageCount 获取离线消息数量（MySQL 跨组计数）
// MySQL 为双写超集（含 Redis 内 + Redis 已过期的全部待推送消息），故直接以 MySQL 计数为准
func (h *HybridOfflineMessageHandler) GetOfflineMessageCount(ctx context.Context, userID string) (int64, error) {
	appID := routing.AppIDFromContext(ctx)
	namespace := routing.NamespaceFromContext(ctx)

	count, err := h.dbRepo.GetCountByReceiver(ctx, appID, namespace, userID)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "从 MySQL 获取离线消息数量失败",
			"user_id", userID,
			"app_id", appID,
			"namespace", namespace,
			"error", err,
		)
		return 0, err
	}

	return count, nil
}

// ClearOfflineMessages 清空用户的离线消息
// groupIDs: 用户在该 (appID, namespace) 下的全部 group（含 "" 表示 P2P 队列），逐组清 Redis + 一次清 MySQL
func (h *HybridOfflineMessageHandler) ClearOfflineMessages(ctx context.Context, userID string, groupIDs []string) error {
	appID := routing.AppIDFromContext(ctx)
	namespace := routing.NamespaceFromContext(ctx)

	var errs []error

	// 1. 逐组清空 Redis 队列（Redis 按 appID:ns:group:userID 分区，P2P 的空 group 补 DefaultGroupID）
	// 群组队列（gid≠""）用 ns=""：群组投递信封 ns 跨 ns 通配，队列 key 为 app::gid:uid（与存储/drain 对称）
	for _, groupID := range groupIDs {
		clearNS := namespace
		if groupID != "" {
			clearNS = ""
		}
		key := queueKey(appID, clearNS, constants.NormalizeGroupID(groupID), userID)
		if err := h.queueRepo.Clear(ctx, key); err != nil {
			errs = append(errs, errorx.WrapError("redis", err))
			h.logger.ErrorContextKV(ctx, "清空 Redis 离线消息队列失败",
				"user_id", userID,
				"queue", key,
				"error", err,
			)
		}
	}

	// 2. 清空 RDBMS offline_messages 表（按应用+命名空间隔离，跨组一次清完）
	if err := h.dbRepo.ClearByReceiver(ctx, appID, namespace, userID); err != nil {
		errs = append(errs, errorx.WrapError("mysql", err))
		h.logger.ErrorContextKV(ctx, "清空 RDBMS offline_messages 表失败",
			"user_id", userID,
			"app_id", appID,
			"namespace", namespace,
			"error", err,
		)
	} else {
		h.logger.DebugContextKV(ctx, "清空 RDBMS offline_messages 表成功",
			"user_id", userID,
			"app_id", appID,
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
	var status models.MessageSendStatus
	var errorMsg string
	if pushErr == nil {
		status = models.MessageSendStatusSuccess
	} else {
		status = models.MessageSendStatusFailed
		errorMsg = pushErr.Error()
	}

	if err := h.dbRepo.UpdatePushStatus(ctx, messageIDs, status, errorMsg); err != nil {
		h.logger.ErrorContextKV(ctx, "更新离线消息推送状态失败",
			"count", len(messageIDs),
			"status", status,
			"error", err,
		)
		return fmt.Errorf("update push status: %w", err)
	}

	h.logger.DebugContextKV(ctx, "更新离线消息推送状态",
		"count", len(messageIDs),
		"status", status,
	)
	return nil
}

// ============================================================================
// 离线队列装配（自 wiring 包迁入）
//
// 混合离线消息处理器的具体实现在本包，装配跟随实现走；
// spi 契约层保持零本地依赖，反向引用域包会构成循环（Go 编译器不允许）
// ============================================================================

// OfflineDeps 混合离线消息处理器的两半依赖
//
// 该处理器同时需要 Redis 队列与 RDBMS 持久化，但两者分属不同适配器，
// 单个适配器的 hook 都拿不到完整依赖 —— 故由本函数在两侧 hook 跑完后统一组装。
type OfflineDeps struct {
	// Queue Redis 队列实现（如 redisadapter.NewOfflineQueue(...)）
	Queue spi.MessageQueue
	// Store RDBMS 持久化实现（如 gormadapter.NewOfflineStoreFor(...)）
	Store spi.OfflineStore
	// Config 离线消息配置
	Config *wscconfig.OfflineMessage
	// Logger 日志器（nil 使用默认）
	Logger spi.Logger
}

// InitializeOfflineQueue 组装并注入混合离线消息处理器
//
// 两半依赖任一缺失时返回错误而非静默跳过：离线消息是「用户离线期间的
// 消息不丢」这条承诺的唯一兜底，缺一半会让消息在无人察觉的情况下丢失。
// 若业务侧确实不需要离线消息能力，应显式不调用本函数。
func InitializeOfflineQueue(hub spi.StoreTarget, deps OfflineDeps) error {
	if hub == nil {
		return fmt.Errorf("messaging: hub target is nil")
	}
	if deps.Queue == nil {
		return fmt.Errorf("messaging: offline queue is nil (redis adapter not configured)")
	}
	if deps.Store == nil {
		return fmt.Errorf("messaging: offline store is nil (gorm adapter not configured)")
	}
	if deps.Config == nil {
		return fmt.Errorf("messaging: offline config is nil")
	}
	hub.SetOfflineMessageHandler(NewHybridOfflineMessageHandler(
		deps.Queue, deps.Store, deps.Config, deps.Logger,
	))
	return nil
}

// ============================================================================
// 用户上线回放（自编排层迁入：域逻辑进域，触发与首连守卫留 hub）
// ============================================================================

// PushOfflineMessages 用户上线离线消息回放（两阶段全量补发）
//
// 全量补发策略（双写存储下的两阶段推送；全部状态在注入的共享存储，
// Deployment 滚动更新 / Pod 重新调度下跨 Pod 可见，Pod 本地零依赖）：
//  1. 按组 drain Redis 队列（FIFO，短期高性能消息优先投递）：枚举用户在该
//     namespace 下加入的全部 group + P2P（空 group）队列，drain 出的消息推送
//     成功后按 message_id 删 MySQL（双写去重，避免阶段 2 重复推送）
//  2. 跨组分页查 MySQL 剩余消息（Redis 已过期 / drain 异常残留），推送后删除
//
// 降级：groupRepo 不可用时只 drain P2P 队列 + 跨组查 MySQL（MySQL 跨组覆盖
// 所有 group 的消息，不丢消息，仅 Redis 短期队列残留待自然过期）
//
// 未注入离线处理器时返回空切片（nil-safe 降级）；推送失败的离线源消息不再
// 重新转存（send.go 离线源防循环），仅更新推送状态保留待下次上线重试。
// 返回成功/失败推送的 messageID 列表（编排层据此触发应用回调）
func (m *Manager) PushOfflineMessages(ctx context.Context, client *models.Client) (pushedIDs, failedIDs []string) {
	pushedIDs = make([]string, 0)
	failedIDs = make([]string, 0)
	if m.offlineHandler == nil || client == nil {
		return pushedIDs, failedIDs
	}

	logger := m.host.GetLogger()
	userID := client.UserID

	// 60s 超时兜底：回放为逐条投递 + 分页拉取，防止单用户大量积压拖住回调池 worker
	pushCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 基础 ctx 注入 appID+namespace（groupIDs 按组动态派生，drain 时单独注入对应 group）
	pushCtx = routing.NewRoute().WithAppID(client.GetAppID()).WithNamespace(client.Namespace).Inject(pushCtx)

	// MySQL 为双写超集，count==0 表示 Redis/MySQL 均无待推送消息，直接跳过
	totalCount, err := m.offlineHandler.GetOfflineMessageCount(pushCtx, userID)
	if err != nil {
		logger.ErrorContextKV(pushCtx, "获取离线消息数量失败",
			"user_id", userID,
			"namespace", client.Namespace,
			"error", err,
		)
		return pushedIDs, failedIDs
	}
	if totalCount == 0 {
		logger.DebugContextKV(pushCtx, "用户无离线消息",
			"user_id", userID,
			"namespace", client.Namespace,
		)
		return pushedIDs, failedIDs
	}

	logger.InfoContextKV(pushCtx, "开始推送离线消息",
		"user_id", userID,
		"namespace", client.Namespace,
		"total_count", totalCount,
	)

	// ===== 阶段1: 按组 drain Redis 队列 =====
	// groupIDs 首项为 "" 表示 P2P 队列，其后追加用户加入的全部 group
	groupIDs := []string{""}
	if groupRepo := m.host.GetGroupRepo(); groupRepo != nil {
		if userGroups, err := groupRepo.GetUserGroups(pushCtx, routing.AppIDFromContext(pushCtx), client.Namespace, userID); err != nil {
			logger.WarnContextKV(pushCtx, "获取用户群组失败，降级只 drain P2P + 跨组查 MySQL",
				"user_id", userID,
				"namespace", client.Namespace,
				"error", err,
			)
		} else {
			groupIDs = append(groupIDs, userGroups...)
		}
	}

	for _, gid := range groupIDs {
		// 按组注入 (app, ns, group)，DrainOfflineQueue 据此定位 Redis 队列 app:ns:group:userID
		// 群组队列（gid≠""）注入 ns=""：存储侧群组投递信封 ns 置空（跨 ns 通配），key 为
		// app::gid:uid，drain 同 ns 保持对称；P2P 队列（gid=""）保持 client.Namespace（存储侧 P2P ns 非空）
		drainNS := client.Namespace
		if gid != "" {
			drainNS = ""
		}
		groupCtx := routing.NewRoute().WithAppID(client.GetAppID()).WithNamespace(drainNS).WithGroup(gid).Inject(pushCtx)
		msgs, err := m.offlineHandler.DrainOfflineQueue(groupCtx, userID, 0) // 0=一次取尽
		if err != nil {
			logger.WarnContextKV(groupCtx, "drain 离线队列失败",
				"user_id", userID,
				"group_id", gid,
				"error", err,
			)
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		batchPushed, batchFailed := m.pushAndDeleteOffline(groupCtx, userID, msgs)
		pushedIDs = append(pushedIDs, batchPushed...)
		failedIDs = append(failedIDs, batchFailed...)
	}

	// ===== 阶段2: 跨组分页查 MySQL 剩余消息（drain 已删的不重复）=====
	const batchSize = 100
	cursor := ""
	for {
		messages, nextCursor, err := m.offlineHandler.GetOfflineMessages(pushCtx, userID, batchSize, cursor)
		if err != nil {
			logger.ErrorContextKV(pushCtx, "获取离线消息失败",
				"user_id", userID,
				"cursor", cursor,
				"error", err,
			)
			break
		}
		if len(messages) == 0 {
			break
		}
		batchPushed, batchFailed := m.pushAndDeleteOffline(pushCtx, userID, messages)
		pushedIDs = append(pushedIDs, batchPushed...)
		failedIDs = append(failedIDs, batchFailed...)
		cursor = nextCursor
		if nextCursor == "" {
			break
		}
	}

	logger.InfoContextKV(pushCtx, "离线消息推送完成",
		"user_id", userID,
		"namespace", client.Namespace,
		"success", len(pushedIDs),
		"failed", len(failedIDs),
	)
	return pushedIDs, failedIDs
}

// pushAndDeleteOffline 推送一批离线消息：成功的按 message_id 删除（MySQL），
// 失败的更新推送状态（保留待下次上线重试）
//
// drain 路径下 Redis 已由 Dequeue 删除，本方法只删 MySQL（双写去重）；
// MySQL 路径下同样删 MySQL。返回成功/失败的 messageID 列表
func (m *Manager) pushAndDeleteOffline(ctx context.Context, userID string, messages []*models.HubMessage) (pushedIDs, failedIDs []string) {
	logger := m.host.GetLogger()
	pushedIDs = make([]string, 0, len(messages))
	failedIDs = make([]string, 0)

	// 预取目标用户节点索引一次：同一 userID 的每条离线消息原逐条走 SendToUserWithRetry，
	// 因其 presetNodes=nil 且本地在线分支不自动预取（见 sendToUserWithRetry 的 localOnline
	// 判断），每条都会在 sendToUser → checkAndRouteToNode 触发一次 queryUserNodes Redis 往返，
	// N 条积压 = N 次冗余节点查询；此处对齐 prefetchFanoutNodes 批量预取模式，回放窗口内
	// 节点索引稳定，预取一次复用消除 N+1。查询失败/无节点时返回 nil，逐条路径自动回退重建
	presetNodes := m.host.BatchGetUserNodes(ctx, []string{userID})[userID]

	for _, message := range messages {
		// 标记为离线消息来源（投递失败不再转存离线，防循环）
		message.Source = models.MessageSourceOffline
		if message.Data == nil {
			message.Data = make(map[string]interface{})
		}
		message.Data["offline"] = true

		// 强制以消息原始 trace 覆盖连接 trace：ctx 来自上线连接（带连接自己的 trace_id），
		// 换成消息信封里的原始发送链路 trace_id，使「发送→离线暂存→上线推送→投递」全程同一 trace 可查
		msgCtx := message.TraceContext(ctx)
		result := m.sendToUserWithRetry(routing.EnsureRouteDefaults(msgCtx), userID, message, presetNodes)
		pushErr := fmt.Errorf("离线消息推送失败")
		if result != nil && result.FinalError != nil {
			pushErr = result.FinalError
		}
		if result == nil || !result.Success {
			logger.ErrorContextKV(msgCtx, "离线消息推送失败",
				"user_id", userID,
				"message_id", message.MessageID,
				"error", pushErr,
			)
			failedIDs = append(failedIDs, message.MessageID)
			// 推送失败 → message_record 状态 Failed + 离线推送状态更新（保留待下次上线重试）
			m.updateMessageStatusAsync(msgCtx, message.MessageID, message.Receiver, models.MessageSendStatusFailed, models.FailureReasonConnError, pushErr.Error())
			if err := m.offlineHandler.UpdatePushStatus(msgCtx, []string{message.MessageID}, pushErr); err != nil {
				logger.ErrorContextKV(msgCtx, "更新离线消息推送失败状态失败",
					"user_id", userID,
					"message_id", message.MessageID,
					"error", err,
				)
			}
			continue
		}
		pushedIDs = append(pushedIDs, message.MessageID)
	}

	// 推送成功的按 message_id 删 MySQL（drain 路径去重 + MySQL 路径清理）
	if len(pushedIDs) > 0 {
		if err := m.offlineHandler.DeleteOfflineMessages(ctx, userID, pushedIDs); err != nil {
			logger.ErrorContextKV(ctx, "删除已推送的离线消息失败",
				"user_id", userID,
				"count", len(pushedIDs),
				"error", err,
			)
		} else {
			logger.DebugContextKV(ctx, "删除已推送的离线消息",
				"user_id", userID,
				"count", len(pushedIDs),
			)
		}
	}
	return pushedIDs, failedIDs
}
