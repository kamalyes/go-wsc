/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\repository.go
 * @Description: Hub 仓库管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/handler"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ============================================================================
// 仓库初始化辅助方法
// ============================================================================

// InitializeRepositories 初始化所有仓库
// 这是一个便捷方法，用于一次性初始化所有必需的仓库
//
// 参数:
//   - redisClient: Redis 客户端（必需）
//   - db: GORM 数据库实例（必需）
//
// 返回:
//   - error: 初始化失败时返回错误
func (h *Hub) InitializeRepositories(redisClient redis.UniversalClient, db *gorm.DB) error {
	if redisClient == nil {
		return ErrOnlineStatusRepositoryNotSet
	}

	if db == nil {
		return ErrRecordRepositoryNotSet
	}

	// 验证 Redis 连接
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		h.logger.ErrorKV("❌ Redis 连接测试失败", "error", err)
		return err
	}

	// 获取 Hub 的 Logger
	hubLogger := h.GetLogger()

	// 1. 在线状态仓库
	// 确保 TTL 至少为心跳间隔的 3 倍
	h.config.RedisRepository.OnlineStatus.TTL = mathx.Max(
		h.config.RedisRepository.OnlineStatus.TTL,
		h.config.HeartbeatInterval*3,
	)
	onlineStatusRepo := repository.NewRedisOnlineStatusRepository(
		redisClient,
		h.config.RedisRepository.OnlineStatus,
	)
	h.SetOnlineStatusRepository(onlineStatusRepo)

	// 2. 统计仓库
	statsRepo := repository.NewRedisHubStatsRepository(
		redisClient,
		h.config.RedisRepository.Stats,
	)
	h.SetHubStatsRepository(statsRepo)

	// 3. 负载管理仓库（仅在启用客服负载管理时初始化）
	if h.config.EnableWorkload {
		workloadRepo := repository.NewRedisWorkloadRepository(
			redisClient,
			db,
			h.config.RedisRepository.Workload,
			hubLogger,
		)
		h.SetWorkloadRepository(workloadRepo)
	} else {
		h.logger.DebugContextKV(ctx, "⏭️ 客服负载管理已禁用，跳过 workloadRepo 初始化", "enable-workload", false)
	}

	// 4. 消息记录仓库 (MySQL GORM)
	messageRecordRepo := repository.NewMessageRecordRepository(
		db,
		h.config.Database.MessageRecord,
		hubLogger,
	)
	h.SetMessageRecordRepository(messageRecordRepo)

	// 5. 连接记录仓库 (MySQL GORM)
	connectionRecordRepo := repository.NewConnectionRecordRepository(
		db,
		h.config.Database.ConnectionRecord,
		hubLogger,
	)
	h.SetConnectionRecordRepository(connectionRecordRepo)

	// 5.1 连接质量仓库 (MySQL GORM)
	// 复用 ConnectionRecord 配置（清理策略待定，暂不启用质量表自动清理）
	connectionQualityRepo := repository.NewConnectionQualityRepository(
		db,
		h.config.Database.ConnectionRecord,
		hubLogger,
	)
	h.SetConnectionQualityRepository(connectionQualityRepo)

	// 6. 群组仓库 (Redis)
	groupKeyPrefix := ""
	if h.config.RedisRepository.Group != nil {
		groupKeyPrefix = h.config.RedisRepository.Group.KeyPrefix
	}
	groupRepo := repository.NewRedisGroupRepository(redisClient, groupKeyPrefix)
	h.SetGroupRepository(groupRepo)

	// 7. 离线消息处理器
	offlineHandler := handler.NewHybridOfflineMessageHandler(
		redisClient,
		db,
		h.config.RedisRepository.OfflineMessage,
		hubLogger,
	)
	h.SetOfflineMessageHandler(offlineHandler)

	// 8. PubSub 传输层初始化已移至 SetPubSub()，由调用方显式注入
	// InitializeRepositories 仅负责数据访问层（repository）初始化，职责单一
	// SetPubSub 内部会自动触发 InitNodeGRPC（若 node-grpc.enabled=true）

	// 9. 注入 Redis 客户端到连接 Token 解码器（若启用 Redis 白名单校验）
	// NewHub 中创建 decoder 时 redisCli 为 nil，这里补齐以启用跨节点会话校验
	if h.connectionTokenDecoder != nil && h.config.Security != nil && h.config.Security.ConnectionToken.IsRedisEnabled() {
		h.connectionTokenDecoder = NewConnectionTokenDecoder(h.config.Security.ConnectionToken, redisClient, hubLogger)
		h.logger.InfoKV("[Hub] 连接 Token Redis 白名单已启用",
			"key_prefix", h.config.Security.ConnectionToken.GetRedisKeyPrefix())
	}

	// 使用 Console 展示仓库初始化信息
	h.logRepositoryInitialization()

	return nil
}

// logRepositoryInitialization 记录仓库初始化信息（一条整打的 KV 日志）
func (h *Hub) logRepositoryInitialization() {
	// PubSub 可能为 nil（单机模式），提前安全取值避免 nil 解引用
	pubsubEnabled := false
	pubsubNamespace := ""
	if h.config.RedisRepository.PubSub != nil && h.config.RedisRepository.PubSub.GetEnabled() {
		pubsubEnabled = true
		pubsubNamespace = h.config.RedisRepository.PubSub.GetNamespace()
	}

	h.logger.InfoKV("✅ WebSocket Hub 仓库初始化",
		"online_status_key_prefix", h.config.RedisRepository.OnlineStatus.KeyPrefix,
		"online_status_ttl_seconds", h.config.RedisRepository.OnlineStatus.TTL.Seconds(),
		"stats_key_prefix", h.config.RedisRepository.Stats.KeyPrefix,
		"stats_ttl_hours", h.config.RedisRepository.Stats.TTL.Hours(),
		"workload_key_prefix", h.config.RedisRepository.Workload.KeyPrefix,
		"offline_message_key_prefix", h.config.RedisRepository.OfflineMessage.KeyPrefix,
		"offline_queue_ttl_hours", h.config.RedisRepository.OfflineMessage.QueueTTL.Hours(),
		"offline_auto_store", h.config.RedisRepository.OfflineMessage.AutoStore,
		"offline_auto_push", h.config.RedisRepository.OfflineMessage.AutoPush,
		"offline_max_count", h.config.RedisRepository.OfflineMessage.MaxCount,
		"mysql_connection_record", true,
		"pubsub_enabled", pubsubEnabled,
		"pubsub_namespace", pubsubNamespace,
		"node_id", h.GetNodeID(),
		"worker_id", h.GetWorkerID(),
	)
}

// ============================================================================
// 仓库设置方法
// ============================================================================

// SetOfflineMessageHandler 设置离线消息处理器
func (h *Hub) SetOfflineMessageHandler(handler OfflineMessageHandler) {
	h.offlineMessageHandler = handler
	// TODO: ACK 管理器的离线消息接口需要重新设计
	// 同时设置到 ACK 管理器（统一离线消息处理）
	// if h.ackManager != nil {
	// 	h.ackManager.SetOfflineRepo(handler)
	// }
	h.logger.InfoKV("离线消息处理器已设置",
		"handler_type", "HybridOfflineMessageHandler",
		"ack_integration", false,
	)
}

// SetOfflineMessageRepo 设置离线消息仓库（兼容旧接口）
func (h *Hub) SetOfflineMessageRepo(repo OfflineMessageHandler) {
	h.SetOfflineMessageHandler(repo)
}

// SetOnlineStatusRepository 设置在线状态仓库（Redis）
func (h *Hub) SetOnlineStatusRepository(repo OnlineStatusRepository) {
	h.onlineStatusRepo = repo
	h.logger.InfoKV("在线状态仓库已设置", "repository_type", "redis")
}

// SetWorkloadRepository 设置负载管理仓库（Redis）
func (h *Hub) SetWorkloadRepository(repo WorkloadRepository) {
	h.workloadRepo = repo
	h.logger.InfoKV("负载管理仓库已设置", "repository_type", "redis")
}

// SetMessageRecordRepository 设置消息记录仓库（MySQL）
func (h *Hub) SetMessageRecordRepository(repo MessageRecordRepository) {
	h.messageRecordRepo = repo
	h.logger.InfoKV("消息记录仓库已设置", "repository_type", "mysql")
}

// SetConnectionRecordRepository 设置连接记录仓库（MySQL）
func (h *Hub) SetConnectionRecordRepository(repo ConnectionRecordRepository) {
	h.connectionRecordRepo = repo
	h.logger.InfoKV("连接记录仓库已设置", "repository_type", "mysql")
}

// SetConnectionQualityRepository 设置连接质量仓库（MySQL）
// 拆表后承载 wsc_connection_qualities 表，由 batcher/stats 路径写入，断开终评调用
func (h *Hub) SetConnectionQualityRepository(repo ConnectionQualityRepository) {
	h.connectionQualityRepo = repo
	h.logger.InfoKV("连接质量仓库已设置", "repository_type", "mysql")
}

// GetConnectionQualityRepository 获取连接质量仓库（供 batcher/stats 路径调用）
func (h *Hub) GetConnectionQualityRepository() ConnectionQualityRepository {
	return h.connectionQualityRepo
}

// SetGroupRepository 设置群组仓库（Redis）
func (h *Hub) SetGroupRepository(repo GroupRepository) {
	h.groupRepo = repo
	h.logger.InfoKV("群组仓库已设置", "repository_type", "redis")
}

// SetHubStatsRepository 设置 Hub 统计仓库（Redis）
func (h *Hub) SetHubStatsRepository(repo HubStatsRepository) {
	h.statsRepo = repo
	h.logger.InfoKV("Hub统计仓库已设置", "repository_type", "redis")

	// 设置启动时间到 Redis
	// ⚠️ 此处曾吞掉错误：RegisterNode 失败（Redis 抖动/超时）后 stats key 不存在，
	// reportPerformanceMetrics 周期性报 "node stats not found"，且无根因日志可查
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()
	if err := repo.RegisterNode(ctx, h.nodeID, time.Now().Unix()); err != nil {
		h.logger.ErrorKV("注册节点到Redis失败", "node_id", h.nodeID, "error", err)
	}
}

// SetMessageExpireDuration 设置ACK消息的过期时间
func (h *Hub) SetMessageExpireDuration(duration time.Duration) {
	if h.ackManager != nil {
		h.ackManager.SetExpireDuration(duration)
	}
}
