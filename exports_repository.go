/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\exports_repository.go
 * @Description: Repository 模块类型导出 - 保持向后兼容
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import "github.com/kamalyes/go-wsc/repository"

// ============================================
// Connection Repository - 连接记录仓储
// ============================================

// ConnectionRecordRepository 连接记录仓储接口
type ConnectionRecordRepository = repository.ConnectionRecordRepository

// ConnectionStats 连接统计信息
type ConnectionStats = repository.ConnectionStats

// UserConnectionStats 用户连接统计
type UserConnectionStats = repository.UserConnectionStats

// NodeConnectionStats 节点连接统计
type NodeConnectionStats = repository.NodeConnectionStats

// UserReconnectStats 用户重连统计
type UserReconnectStats = repository.UserReconnectStats

// NewConnectionRecordRepository 创建连接记录仓储
var NewConnectionRecordRepository = repository.NewConnectionRecordRepository

// ============================================
// Hub Stats Repository - Hub 统计信息仓储
// ============================================

// HubStatsRepository Hub 统计信息仓储接口
type HubStatsRepository = repository.HubStatsRepository

// NodeStats 节点统计信息
type NodeStats = repository.NodeStats

// ClusterStats 集群统计信息
type ClusterStats = repository.ClusterStats

// RedisHubStatsRepository Redis Hub 统计仓储实现
type RedisHubStatsRepository = repository.RedisHubStatsRepository

// NewRedisHubStatsRepository 创建 Redis Hub 统计仓储
var NewRedisHubStatsRepository = repository.NewRedisHubStatsRepository

// ============================================
// Message Queue Repository - 消息队列仓储
// ============================================

// MessageQueueRepository 消息队列仓储接口
type MessageQueueRepository = repository.MessageQueueRepository

// RedisMessageQueueRepository Redis 消息队列仓储实现
type RedisMessageQueueRepository = repository.RedisMessageQueueRepository

// NewRedisMessageQueueRepository 创建 Redis 消息队列仓储
var NewRedisMessageQueueRepository = repository.NewRedisMessageQueueRepository

// ============================================
// Message Record Repository - 消息记录仓储
// ============================================

// MessageRecordRepository 消息记录仓储接口
type MessageRecordRepository = repository.MessageRecordRepository

// MessageRecordGormRepository Gorm 消息记录仓储实现
type MessageRecordGormRepository = repository.MessageRecordGormRepository

// NewMessageRecordRepository 创建消息记录仓储
var NewMessageRecordRepository = repository.NewMessageRecordRepository

// ============================================
// Offline Message Repository - 离线消息仓储
// ============================================

// OfflineMessageRecord 离线消息记录
type OfflineMessageRecord = repository.OfflineMessageRecord

// OfflineMessageDBRepository 离线消息数据库仓储接口
type OfflineMessageDBRepository = repository.OfflineMessageDBRepository

// GormOfflineMessageRepository Gorm 离线消息仓储实现
type GormOfflineMessageRepository = repository.GormOfflineMessageRepository

// NewGormOfflineMessageRepository 创建 Gorm 离线消息仓储
var NewGormOfflineMessageRepository = repository.NewGormOfflineMessageRepository

// ============================================
// Online Status Repository - 在线状态仓储
// ============================================

// OnlineStatusRepository 在线状态仓储接口
type OnlineStatusRepository = repository.OnlineStatusRepository

// RedisOnlineStatusRepository Redis 在线状态仓储实现
type RedisOnlineStatusRepository = repository.RedisOnlineStatusRepository

// NewRedisOnlineStatusRepository 创建 Redis 在线状态仓储
var NewRedisOnlineStatusRepository = repository.NewRedisOnlineStatusRepository

// ============================================
// Workload Repository - 工作负载仓储
// ============================================

// WorkloadInfo 工作负载信息
type WorkloadInfo = repository.WorkloadInfo

// WorkloadRepository 工作负载仓储接口
type WorkloadRepository = repository.WorkloadRepository

// RedisWorkloadRepository Redis 工作负载仓储实现
type RedisWorkloadRepository = repository.RedisWorkloadRepository

// NewRedisWorkloadRepository 创建 Redis 工作负载仓储
var NewRedisWorkloadRepository = repository.NewRedisWorkloadRepository

// ============================================================================
// Repository 方法导出 - 这些方法通过各个 Repository 实例调用
// ============================================================================

// ============================================
// WorkloadRepository 接口方法
// ============================================

// 注意：以下是 WorkloadRepository 接口的方法列表，通过实现类实例调用
// 例如：repo := wsc.NewRedisWorkloadRepository(client, config, log)

// 工作负载管理方法：
// - SetAgentWorkload(ctx context.Context, agentID string, workload int64) error: 设置客服工作负载
// - GetAgentWorkload(ctx context.Context, agentID string) (int64, error): 获取客服工作负载
// - IncrementAgentWorkload(ctx context.Context, agentID string) error: 增加客服工作负载
// - DecrementAgentWorkload(ctx context.Context, agentID string) error: 减少客服工作负载
// - GetLeastLoadedAgent(ctx context.Context, onlineAgents []string) (string, int64, error): 获取负载最低的客服
// - RemoveAgentWorkload(ctx context.Context, agentID string) error: 移除客服工作负载
// - GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error): 获取所有客服工作负载
// - BatchSetAgentWorkload(ctx context.Context, workloads map[string]int64) error: 批量设置客服工作负载

// RedisWorkloadRepository 特有方法：
// - GetTodayKey() string: 获取今日键
// - GetWorkloadKey(agentID string) string: 获取工作负载键
// - GetZSetKey() string: 获取有序集合键

// ============================================
// OnlineStatusRepository 接口方法
// ============================================

// 注意：以下是 OnlineStatusRepository 接口的方法列表
// 例如：repo := wsc.NewRedisOnlineStatusRepository(client, config)

// 在线状态管理方法：
// - SetOnline(ctx context.Context, client *Client) error: 设置在线
// - SetOffline(ctx context.Context, userID string) error: 设置离线
// - IsOnline(ctx context.Context, userID string) (bool, error): 检查是否在线
// - GetOnlineInfo(ctx context.Context, userID string) (*Client, error): 获取在线信息
// - GetAllOnlineUsers(ctx context.Context) ([]string, error): 获取所有在线用户
// - GetOnlineUsersByNode(ctx context.Context, nodeID string) ([]string, error): 获取节点在线用户
// - GetOnlineCount(ctx context.Context) (int64, error): 获取在线用户数
// - UpdateHeartbeat(ctx context.Context, userID string) error: 更新心跳
// - BatchSetOnline(ctx context.Context, clients map[string]*Client) error: 批量设置在线
// - BatchSetOffline(ctx context.Context, userIDs []string) error: 批量设置离线
// - GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error): 按类型获取在线用户
// - CleanupExpired(ctx context.Context) (int64, error): 清理过期数据

// RedisOnlineStatusRepository 特有方法：
// - GetUserKey(userID string) string: 获取用户键
// - GetNodeSetKey(nodeID string) string: 获取节点集合键
// - GetUserTypeSetKey(userType models.UserType) string: 获取用户类型集合键
// - GetAllUsersSetKey() string: 获取所有用户集合键

// ============================================
// OfflineMessageDBRepository 接口方法
// ============================================

// 注意：以下是 OfflineMessageDBRepository 接口的方法列表
// 例如：repo := wsc.NewGormOfflineMessageRepository(db)

// 离线消息管理方法：
// - Save(ctx context.Context, record *OfflineMessageRecord) error: 保存离线消息
// - BatchSave(ctx context.Context, records []*OfflineMessageRecord) error: 批量保存离线消息
// - GetByReceiver(ctx context.Context, receiverID string, limit int, cursor ...string) ([]*OfflineMessageRecord, error): 按接收者获取消息
// - GetBySender(ctx context.Context, senderID string, limit int) ([]*OfflineMessageRecord, error): 按发送者获取消息
// - DeleteByMessageIDs(ctx context.Context, receiverID string, messageIDs []string) error: 按消息ID删除
// - GetCountByReceiver(ctx context.Context, receiverID string) (int64, error): 获取接收者消息数
// - GetCountBySender(ctx context.Context, senderID string) (int64, error): 获取发送者消息数
// - ClearByReceiver(ctx context.Context, receiverID string) error: 清除接收者所有消息
// - DeleteExpired(ctx context.Context) (int64, error): 删除过期消息
// - MarkAsPushed(ctx context.Context, messageIDs []string) error: 标记为已推送

// ============================================
// MessageRecordRepository 接口方法
// ============================================

// 注意：以下是 MessageRecordRepository 接口的方法列表
// 例如：repo := wsc.NewMessageRecordRepository(db)

// 消息记录管理方法：
// - Create(record *MessageSendRecord) error: 创建消息记录
// - CreateFromMessage(msg *HubMessage, maxRetry int, expiresAt *time.Time) (*MessageSendRecord, error): 从消息创建记录
// - Update(record *MessageSendRecord) error: 更新消息记录
// - FindByID(id uint) (*MessageSendRecord, error): 按ID查找
// - FindByMessageID(messageID string) (*MessageSendRecord, error): 按消息ID查找
// - FindByStatus(status MessageSendStatus, limit int) ([]*MessageSendRecord, error): 按状态查找
// - FindBySender(sender string, limit int) ([]*MessageSendRecord, error): 按发送者查找
// - FindByReceiver(receiver string, limit int) ([]*MessageSendRecord, error): 按接收者查找
// - FindByNodeIP(nodeIP string, limit int) ([]*MessageSendRecord, error): 按节点IP查找
// - FindByClientIP(clientIP string, limit int) ([]*MessageSendRecord, error): 按客户端IP查找

// ============================================
// ConnectionRecordRepository 接口方法
// ============================================

// 注意：以下是 ConnectionRecordRepository 接口的方法列表
// 例如：repo := wsc.NewConnectionRecordRepository(db)

// 连接记录管理方法：定义在接口中，通过实现类调用

// ============================================
// HubStatsRepository 接口方法
// ============================================

// 注意：以下是 HubStatsRepository 接口的方法列表
// 例如：repo := wsc.NewRedisHubStatsRepository(client, config, log)

// Hub统计管理方法：定义在接口中，通过实现类调用

// ============================================
// MessageQueueRepository 接口方法
// ============================================

// 注意：以下是 MessageQueueRepository 接口的方法列表
// 例如：repo := wsc.NewRedisMessageQueueRepository(client, config, log)

// 消息队列管理方法：定义在接口中，通过实现类调用

