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

	"github.com/kamalyes/go-cachex"
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
func (h *Hub) InitializeRepositories(redisClient *redis.Client, db *gorm.DB) error {
	if redisClient == nil {
		return ErrOnlineStatusRepositoryNotSet
	}

	if db == nil {
		return ErrRecordRepositoryNotSet
	}

	// 验证 Redis 连接
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		h.logger.ErrorKV("❌ Redis 连接测试失败", "error", err)
		return err
	}

	// 获取 Hub 的 Logger
	hubLogger := h.GetLogger()

	// 1. 在线状态仓库 (TTL固定为心跳间隔的3倍)
	h.config.RedisRepository.OnlineStatus.TTL = time.Duration(h.config.HeartbeatInterval) * time.Second * 3
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

	// 3. 负载管理仓库
	workloadRepo := repository.NewRedisWorkloadRepository(
		redisClient,
		h.config.RedisRepository.Workload,
		hubLogger,
	)
	h.SetWorkloadRepository(workloadRepo)

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

	// 6. 离线消息处理器
	offlineHandler := handler.NewHybridOfflineMessageHandler(
		redisClient,
		db,
		h.config.RedisRepository.OfflineMessage,
		hubLogger,
	)
	h.SetOfflineMessageHandler(offlineHandler)

	// 7. 初始化 PubSub（分布式消息订阅）
	if h.config.RedisRepository.PubSub.GetEnabled() {
		pubsubCfg := cachex.PubSubConfig{
			Namespace:          h.config.RedisRepository.PubSub.GetNamespace(),
			MaxRetries:         h.config.RedisRepository.PubSub.GetMaxRetries(),
			RetryDelay:         h.config.RedisRepository.PubSub.GetRetryDelay(),
			BufferSize:         h.config.RedisRepository.PubSub.GetBufferSize(),
			Logger:             hubLogger,
			PingInterval:       h.config.RedisRepository.PubSub.GetPingInterval(),
			EnableCompression:  h.config.RedisRepository.PubSub.GetEnableCompression(),
			CompressionMinSize: h.config.RedisRepository.PubSub.GetCompressionMinSize(),
		}
		pubsub := cachex.NewPubSub(redisClient, pubsubCfg)
		h.SetPubSub(pubsub)
	}

	// 使用 Console 展示仓库初始化信息
	h.logRepositoryInitialization()

	return nil
}

// logRepositoryInitialization 记录仓库初始化信息
func (h *Hub) logRepositoryInitialization() {
	cg := h.logger.NewConsoleGroup()
	cg.Group("✅ WebSocket Hub 仓库初始化")

	// Redis 仓库配置
	redisConfig := []map[string]interface{}{
		{
			"仓库类型":   "在线状态",
			"Key前缀":  h.config.RedisRepository.OnlineStatus.KeyPrefix,
			"TTL(秒)": h.config.RedisRepository.OnlineStatus.TTL.Seconds(),
		},
		{
			"仓库类型":    "统计数据",
			"Key前缀":   h.config.RedisRepository.Stats.KeyPrefix,
			"TTL(小时)": h.config.RedisRepository.Stats.TTL.Hours(),
		},
		{
			"仓库类型":  "工作负载",
			"Key前缀": h.config.RedisRepository.Workload.KeyPrefix,
		},
	}
	cg.Table(redisConfig)

	// 离线消息配置
	offlineConfig := map[string]interface{}{
		"Key前缀":     h.config.RedisRepository.OfflineMessage.KeyPrefix,
		"队列TTL(小时)": h.config.RedisRepository.OfflineMessage.QueueTTL.Hours(),
		"自动存储":      h.config.RedisRepository.OfflineMessage.AutoStore,
		"自动推送":      h.config.RedisRepository.OfflineMessage.AutoPush,
		"最大消息数":     h.config.RedisRepository.OfflineMessage.MaxCount,
	}
	cg.Table(offlineConfig)

	cg.Info("✅ MySQL 连接记录仓库已初始化")

	if h.config.RedisRepository.PubSub != nil && h.config.RedisRepository.PubSub.GetEnabled() {
		cg.Info("✅ 分布式 PubSub 已初始化 (Namespace: %s)", h.config.RedisRepository.PubSub.GetNamespace())
	} else {
		cg.Warn("⚠️  分布式 PubSub 未启用，运行在单机模式")
	}

	cg.Info("✅ ShortFlake ID 生成器已初始化 (Hub NodeID: %s, WorkerID: %d)", h.GetNodeID(), h.GetWorkerID())
	cg.GroupEnd()
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

// SetHubStatsRepository 设置 Hub 统计仓库（Redis）
func (h *Hub) SetHubStatsRepository(repo HubStatsRepository) {
	h.statsRepo = repo
	h.logger.InfoKV("Hub统计仓库已设置", "repository_type", "redis")

	// 设置启动时间到 Redis
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = repo.RegisterNode(ctx, h.nodeID, time.Now().Unix())
}

// SetMessageExpireDuration 设置ACK消息的过期时间
func (h *Hub) SetMessageExpireDuration(duration time.Duration) {
	if h.ackManager != nil {
		h.ackManager.SetExpireDuration(duration)
	}
}
