/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 19:50:28
 * @FilePath: \go-wsc\exports_hub.go
 * @Description: Hub 包的类型和函数导出
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"github.com/kamalyes/go-wsc/hub"
)

// ============================================================================
// Hub 类型导出
// ============================================================================

type (
	Hub                        = hub.Hub
	Client                     = hub.Client
	NodeInfo                   = hub.NodeInfo
	KickUserResult             = hub.KickUserResult
	SendAttempt                = hub.SendAttempt
	SendResult                 = hub.SendResult
	BroadcastResult            = hub.BroadcastResult
	PoolManager                = hub.PoolManager
	OfflineMessagePushCallback = hub.OfflineMessagePushCallback
	MessageSendCallback        = hub.MessageSendCallback
	QueueFullCallback          = hub.QueueFullCallback
	HeartbeatTimeoutCallback   = hub.HeartbeatTimeoutCallback
	ClientConnectCallback      = hub.ClientConnectCallback
	ClientDisconnectCallback   = hub.ClientDisconnectCallback
	MessageReceivedCallback    = hub.MessageReceivedCallback
	ErrorCallback              = hub.ErrorCallback
	BatchSendFailureCallback   = hub.BatchSendFailureCallback
	ContextKey                 = hub.ContextKey
	BatchSender                = hub.BatchSender
	BatchSendResult            = hub.BatchSendResult
	UserResult                 = hub.UserResult
	HubHealthInfo              = hub.HubHealthInfo
	ObserverManagerStats       = hub.ObserverManagerStats
	ObserverStats              = hub.ObserverStats
)

// 常量导出
const (
	ContextKeyUserID   = hub.ContextKeyUserID
	ContextKeySenderID = hub.ContextKeySenderID
)

// ============================================================================
// Hub 函数导出
// ============================================================================

var (
	NewHub = hub.NewHub
)

// ============================================================================
// Hub 方法导出 - 这些方法通过 Hub 实例调用
// ============================================================================

// 注意：以下是 Hub 类型的方法列表，通过 Hub 实例调用
// 例如：hub := wsc.NewHub(config); hub.Register(client)

// HTTP WebSocket 升级方法：
// - ConfigureUpgrader() *websocket.Upgrader: 配置 WebSocket 升级器
// - CreateClientFromRequest(r *http.Request, conn *websocket.Conn) *Client: 从 HTTP 请求创建客户端
// - HandleWebSocketUpgrade(w http.ResponseWriter, r *http.Request): 处理 WebSocket 升级请求

// 仓库初始化方法：
// - InitializeRepositories(redisClient *cachex.Client, db *gorm.DB) error: 初始化所有仓库

// 客户端注册与管理方法：
// - Register(client *Client): 注册客户端
// - Unregister(client *Client): 注销客户端
// - KickUser(userID, reason string, sendNotification bool, notificationMsg string) *KickUserResult: 踢出用户
// - KickUserWithMessage(userID, reason, message string) error: 带消息踢出用户
// - KickUserSimple(userID, reason string) int: 简单踢出用户
// - GetClientsCopy() []*Client: 获取客户端列表副本
// - GetUserClientsCopy() []*Client: 获取用户客户端列表副本
// - GetUserClientsMapWithLock(userID string) (map[string]*Client, bool): 获取用户客户端映射
// - GetConnectionsByUserID(userID string) []*Client: 获取用户的所有连接
// - GetClientByIDWithLock(clientID string) (*Client, bool): 通过ID获取客户端

// 消息发送方法：
// - SendToAllClientsInMap(clientMap map[string]*Client, msg *HubMessage): 向映射中所有客户端发送
// - SendToUserWithRetry(ctx context.Context, toUserID string, msg *HubMessage) *SendResult: 带重试发送
// - SendToMultipleUsers(ctx context.Context, userIDs []string, msg *HubMessage) map[string]error: 向多用户发送
// - SendToGroupMembers(ctx context.Context, memberIDs []string, msg *HubMessage, excludeSender bool) *BroadcastResult: 向组成员发送
// - SendToClientsWithRetry(ctx context.Context, clients []*Client, msg *HubMessage, maxRetries int) map[string]*SendResult: 向客户端列表发送
// - SendWithCallback(ctx context.Context, userID string, msg *HubMessage, successCallback, failureCallback func(*HubMessage, error)): 带回调发送
// - SendPriority(ctx context.Context, userID string, msg *HubMessage, priority Priority): 优先级发送
// - SendConditional(ctx context.Context, condition func(*Client) bool, msg *HubMessage) int: 条件发送

// VIP 相关方法：
// - SendToVIPUsers(ctx context.Context, minVIPLevel VIPLevel, msg *HubMessage) int: 向VIP用户发送
// - SendToExactVIPLevel(ctx context.Context, vipLevel VIPLevel, msg *HubMessage) int: 向特定VIP等级发送
// - SendWithVIPPriority(ctx context.Context, userID string, msg *HubMessage): 按VIP优先级发送
// - SendToVIPWithPriority(ctx context.Context, vipLevel VIPLevel, msg *HubMessage) int: 向VIP按优先级发送
// - SendToUserWithClassification(ctx context.Context, userID string, msg *HubMessage, classification *MessageClassification): 按分类发送
// - GetVIPStatistics() map[string]int: 获取VIP统计
// - FilterVIPClients(minLevel VIPLevel) []*Client: 过滤VIP客户端
// - UpgradeVIPLevel(userID string, newLevel VIPLevel) bool: 升级VIP等级

// 工作负载管理方法：
// - SetAgentWorkload(agentID string, workload int64) error: 设置客服工作负载
// - GetAgentWorkload(agentID string) (int64, error): 获取客服工作负载
// - RemoveAgentWorkload(agentID string) error: 移除客服工作负载
// - IncrementAgentWorkload(agentID string) error: 增加客服工作负载
// - DecrementAgentWorkload(agentID string) error: 减少客服工作负载
// - GetLeastLoadedAgent() (string, int64, error): 获取负载最低的客服

// SSE 相关方法：
// - RegisterSSE(userID string, w http.ResponseWriter, userType UserType) (*Client, error): 注册SSE客户端
// - UnregisterSSE(clientID string): 注销SSE客户端
// - SendToUserViaSSE(userID string, msg *HubMessage) bool: 通过SSE发送
// - GetSSEClientCount() int: 获取SSE客户端数量
// - GetSSEClients() []*Client: 获取SSE客户端列表
// - IsSSEClientOnline(userID string) bool: 检查SSE客户端是否在线

// 仓储配置方法：
// - SetOfflineMessageHandler(handler OfflineMessageHandler): 设置离线消息处理器
// - SetOfflineMessageRepo(repo OfflineMessageHandler): 设置离线消息仓储
// - SetOnlineStatusRepository(repo OnlineStatusRepository): 设置在线状态仓储
// - SetWorkloadRepository(repo WorkloadRepository): 设置工作负载仓储
// - SetMessageRecordRepository(repo MessageRecordRepository): 设置消息记录仓储
// - SetConnectionRecordRepository(repo ConnectionRecordRepository): 设置连接记录仓储
// - SetHubStatsRepository(repo HubStatsRepository): 设置Hub统计仓储
// - SetMessageExpireDuration(duration time.Duration): 设置消息过期时长

// 回调相关方法：
// - InvokeMessageReceivedCallback(ctx context.Context, client *Client, msg *HubMessage) error: 调用消息接收回调
// - InvokeErrorCallback(ctx context.Context, err error, severity ErrorSeverity) error: 调用错误回调

// 工具与查询方法：
// - CreateConnectionRecord(client *Client) *ConnectionRecord: 创建连接记录
// - UpdateUserHeartbeat(userID string) error: 更新用户心跳
// - SetClientLastHeartbeatForTest(clientID string, lastHeartbeat time.Time) bool: 设置客户端心跳（测试用）
// - GetHubHealth() *HubHealthInfo: 获取Hub健康信息
// - GetOnlineUsersByType(userType UserType) ([]string, error): 按类型获取在线用户
// - CloseAllClientsInMap(clientMap map[string]*Client): 关闭映射中所有客户端
