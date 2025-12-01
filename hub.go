/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 09:24:05
 * @FilePath: \go-wsc\hub.go
 * @Description: WebSocket/SSE 服务端 Hub - 统一管理实时连接
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	goconfig "github.com/kamalyes/go-config"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/retry"
)

// SendFailureHandler 消息发送失败处理器接口 - 通用处理器
type SendFailureHandler interface {
	// HandleSendFailure 处理消息发送失败
	HandleSendFailure(msg *HubMessage, recipient string, reason string, err error)
}

// QueueFullHandler 队列满处理器
type QueueFullHandler interface {
	// HandleQueueFull 处理队列满的情况
	HandleQueueFull(msg *HubMessage, recipient string, queueType string, err error)
}

// UserOfflineHandler 用户离线处理器
type UserOfflineHandler interface {
	// HandleUserOffline 处理用户离线的情况
	HandleUserOffline(msg *HubMessage, userID string, err error)
}

// ConnectionErrorHandler 连接错误处理器
type ConnectionErrorHandler interface {
	// HandleConnectionError 处理连接错误
	HandleConnectionError(msg *HubMessage, clientID string, err error)
}

// TimeoutHandler 超时处理器
type TimeoutHandler interface {
	// HandleTimeout 处理超时情况
	HandleTimeout(msg *HubMessage, recipient string, timeoutType string, duration time.Duration, err error)
}

// HeartbeatTimeoutCallback 心跳超时回调函数
// 参数: clientID - 客户端ID, userID - 用户ID, lastHeartbeat - 最后心跳时间
type HeartbeatTimeoutCallback func(clientID string, userID string, lastHeartbeat time.Time)

// SendFailureReason 消息发送失败原因
const (
	SendFailureReasonQueueFull     = "queue_full"     // 队列满
	SendFailureReasonBroadcastFull = "broadcast_full" // 广播队列满
	SendFailureReasonPendingFull   = "pending_full"   // 待发送队列满
	SendFailureReasonUserOffline   = "user_offline"   // 用户离线
	SendFailureReasonTimeout       = "timeout"        // 超时
	SendFailureReasonSendTimeout   = "send_timeout"   // 发送超时
	SendFailureReasonAckTimeout    = "ack_timeout"    // ACK超时
	SendFailureReasonConnClosed    = "conn_closed"    // 连接关闭
	SendFailureReasonConnError     = "conn_error"     // 连接错误
	SendFailureReasonChannelClosed = "channel_closed" // 通道关闭
	SendFailureReasonUnknown       = "unknown"        // 未知错误
	SendFailureReasonValidation    = "validation"     // 验证失败
	SendFailureReasonPermission    = "permission"     // 权限不足
)

// SendAttempt 发送尝试记录
type SendAttempt struct {
	AttemptNumber int           // 尝试次数
	StartTime     time.Time     // 开始时间
	Duration      time.Duration // 耗时
	Error         error         // 错误
	Success       bool          // 是否成功
}

// SendResult 发送结果
type SendResult struct {
	Success      bool          // 最终是否成功
	Attempts     []SendAttempt // 所有尝试记录
	TotalRetries int           // 总重试次数
	TotalTime    time.Duration // 总耗时
	FinalError   error         // 最终错误
}

// ContextKey 上下文键类型
type ContextKey string

const (
	// ContextKeyUserID 用户ID上下文键
	ContextKeyUserID ContextKey = "user_id"
	// ContextKeySenderID 发送者ID上下文键
	ContextKeySenderID ContextKey = "sender_id"
)

// HubMessage Hub消息结构（复用 go-wsc 类型）
type HubMessage struct {
	ID           string                 `json:"id"`                        // 消息ID（用于ACK）
	MessageType  MessageType            `json:"message_type"`              // 消息类型
	Sender       string                 `json:"sender"`                    // 发送者ID (从上下文获取)
	SenderType   UserType               `json:"sender_type"`               // 发送者类型
	Receiver     string                 `json:"receiver"`                  // 接收者ID
	ReceiverType UserType               `json:"receiver_type"`             // 接收者类型
	SessionId    string                 `json:"session_id"`                // 会话ID
	Content      string                 `json:"content"`                   // 消息内容
	Data         map[string]interface{} `json:"data,omitempty"`            // 扩展数据
	CreateAt     time.Time              `json:"create_at"`                 // 创建时间
	MessageID    string                 `json:"message_id"`                // 业务消息ID
	SeqNo        int64                  `json:"seq_no"`                    // 消息序列号
	Priority     Priority               `json:"priority"`                  // 优先级
	ReplyToMsgID string                 `json:"reply_to_msg_id,omitempty"` // 回复的消息ID
	Status       MessageStatus          `json:"status"`                    // 消息状态
	RequireAck   bool                   `json:"require_ack,omitempty"`     // 是否需要ACK确认
}

// Client 客户端连接（服务端视角）
type Client struct {
	ID            string                 // 客户端ID
	UserID        string                 // 用户ID
	UserType      UserType               // 用户类型
	VIPLevel      VIPLevel               // VIP等级
	Role          UserRole               // 角色
	ClientIP      string                 // 客户端IP地址
	Conn          *websocket.Conn        // WebSocket连接
	LastSeen      time.Time              // 最后活跃时间
	LastHeartbeat time.Time              // 最后心跳时间
	Status        UserStatus             // 状态
	Department    Department             // 部门
	Skills        []Skill                // 技能
	MaxTickets    int                    // 最大工单数
	NodeID        string                 // 节点ID
	ClientType    ClientType             // 客户端类型
	Metadata      map[string]interface{} // 元数据
	SendChan      chan []byte            // 发送通道
	Context       context.Context        // 上下文（存储发送者ID等信息）
}

// SSEConnection SSE连接
type SSEConnection struct {
	UserID     string
	Writer     http.ResponseWriter
	Flusher    http.Flusher
	MessageCh  chan *HubMessage
	CloseCh    chan struct{}
	LastActive time.Time
	Context    context.Context // 上下文
}

// Hub WebSocket/SSE 连接管理中心
type Hub struct {
	// 节点信息
	nodeID   string
	nodeInfo *NodeInfo
	nodes    map[string]*NodeInfo

	// 客户端管理
	clients      map[string]*Client // 所有客户端 key: clientID
	userToClient map[string]*Client // 用户ID到客户端
	agentClients map[string]*Client // 客服连接

	// SSE 连接
	sseClients map[string]*SSEConnection

	// 消息通道
	register    chan *Client
	unregister  chan *Client
	broadcast   chan *HubMessage
	nodeMessage chan *DistributedMessage

	// 消息缓冲队列（队列满时预存）
	pendingMessages chan *HubMessage
	maxPendingSize  int

	// ACK管理器
	ackManager *AckManager

	// 消息记录管理器

	// 消息记录仓库（数据库持久化）
	messageRecordRepo MessageRecordRepository

	// 在线状态仓库（Redis 分布式存储）
	onlineStatusRepo OnlineStatusRepository

	// Hub 统计仓库（Redis 分布式统计，支持多节点）
	statsRepo HubStatsRepository

	// 并发控制
	wg       sync.WaitGroup
	shutdown atomic.Bool
	started  atomic.Bool
	startCh  chan struct{}

	// 欢迎消息提供者
	welcomeProvider WelcomeMessageProvider

	// 核心组件
	logger WSCLogger // 日志器

	// 并发控制
	mutex    sync.RWMutex
	sseMutex sync.RWMutex

	// 消息发送失败回调处理器
	sendFailureHandlers     []SendFailureHandler     // 通用处理器
	queueFullHandlers       []QueueFullHandler       // 队列满处理器
	userOfflineHandlers     []UserOfflineHandler     // 用户离线处理器
	connectionErrorHandlers []ConnectionErrorHandler // 连接错误处理器
	timeoutHandlers         []TimeoutHandler         // 超时处理器
	failureHandlerMutex     sync.RWMutex             // 失败处理器互斥锁

	// 心跳机制
	heartbeatInterval       time.Duration            // 心跳间隔
	heartbeatTimeout        time.Duration            // 心跳超时时间
	heartbeatTimeoutHandler HeartbeatTimeoutCallback // 心跳超时回调函数
	heartbeatTicker         *time.Ticker             // 心跳定时器

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 配置
	config *wscconfig.WSC

	// 安全配置访问器
	safeConfig *goconfig.ConfigSafe

	// 性能优化：消息字节缓存池
	msgPool sync.Pool
}

// DefaultHubConfig 创建默认Hub配置
// NodeInfo 节点信息
type NodeInfo struct {
	ID          string     `json:"id"`
	IPAddress   string     `json:"ip_address"`
	Port        int        `json:"port"`
	Status      NodeStatus `json:"status"`
	LoadScore   float64    `json:"load_score"`
	LastSeen    time.Time  `json:"last_seen"`
	Connections int        `json:"connections"`
}

// NewHub 创建新的Hub
func NewHub(config *wscconfig.WSC) *Hub {
	if config == nil {
		config = wscconfig.Default()
	}

	ctx, cancel := context.WithCancel(context.Background())
	nodeID := fmt.Sprintf("node-%s-%d-%d", config.NodeIP, config.NodePort, time.Now().UnixNano())

	hub := &Hub{
		nodeID: nodeID,
		nodeInfo: &NodeInfo{
			ID:        nodeID,
			IPAddress: config.NodeIP,
			Port:      config.NodePort,
			Status:    NodeStatusActive,
			LastSeen:  time.Now(),
		},
		nodes:           make(map[string]*NodeInfo),
		clients:         make(map[string]*Client),
		userToClient:    make(map[string]*Client),
		agentClients:    make(map[string]*Client),
		sseClients:      make(map[string]*SSEConnection),
		register:        make(chan *Client, config.MessageBufferSize),
		unregister:      make(chan *Client, config.MessageBufferSize),
		broadcast:       make(chan *HubMessage, config.MessageBufferSize*4),
		nodeMessage:     make(chan *DistributedMessage, config.MessageBufferSize*4),
		pendingMessages: make(chan *HubMessage, 1000), // 使用默认值
		maxPendingSize:  1000,
		ackManager:      NewAckManager(config.AckTimeout, config.AckMaxRetries),
		welcomeProvider: nil, // 使用默认欢迎提供者
		ctx:             ctx,
		cancel:          cancel,
		startCh:         make(chan struct{}),
		config:          config,
		safeConfig:      goconfig.SafeConfig(config),
		logger:          initLogger(config),
		msgPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, 1024) // 预分配1KB缓冲
			},
		},
	}
	return hub
}

// Run 启动Hub
func (h *Hub) Run() {
	h.wg.Add(1)
	defer h.wg.Done()

	// 记录Hub启动日志
	h.logger.InfoKV("Hub启动中",
		"node_id", h.nodeID,
		"node_ip", h.config.NodeIP,
		"node_port", h.config.NodePort,
	)

	// 设置已启动标志并通知等待的goroutine
	if h.started.CompareAndSwap(false, true) {
		h.logger.InfoKV("Hub启动成功",
			"node_id", h.nodeID,
			"message_buffer", h.config.MessageBufferSize,
			"heartbeat_interval", h.config.HeartbeatInterval,
		)

		// 设置启动时间到 Redis
		if h.statsRepo != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = h.statsRepo.SetStartTime(ctx, h.nodeID, time.Now().Unix())
			}()
		}

		// 启动指标收集器（如果已配置）
		close(h.startCh)
	}

	ticker := time.NewTicker(time.Duration(h.safeConfig.GetInt("HeartbeatInterval", 30)) * time.Second)
	defer ticker.Stop()

	// 性能监控定时器 - 每5分钟报告一次
	perfTicker := time.NewTicker(5 * time.Minute)
	defer perfTicker.Stop()

	// 启动待发送消息处理goroutine
	go h.processPendingMessages()

	for {
		select {
		case <-h.ctx.Done():
			return
		case client := <-h.register:
			h.handleRegister(client)
		case client := <-h.unregister:
			h.handleUnregister(client)
		case message := <-h.broadcast:
			h.handleBroadcast(message)
		case <-ticker.C:
			h.checkHeartbeat()
		case <-perfTicker.C:
			h.reportPerformanceMetrics()
		}
	}
}

// reportPerformanceMetrics 报告性能指标
func (h *Hub) reportPerformanceMetrics() {
	h.mutex.RLock()
	activeClients := len(h.clients)
	sseClients := len(h.sseClients)
	h.mutex.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 从 Redis 获取统计信息
	stats, err := h.statsRepo.GetNodeStats(ctx, h.nodeID)
	if err != nil {
		h.logger.WarnKV("获取节点统计失败", "error", err)
		return
	}

	// 记录性能指标日志
	h.logger.InfoKV("Hub性能指标",
		"active_websocket_clients", activeClients,
		"active_sse_clients", sseClients,
		"total_connections", stats.TotalConnections,
		"total_messages_sent", stats.MessagesSent,
		"total_broadcasts_sent", stats.BroadcastsSent,
		"node_id", h.nodeID,
		"uptime_seconds", stats.Uptime,
	)
}

// WaitForStart 等待Hub启动完成
// 这个方法对于用户来说很重要，确保Hub完全启动后再进行操作
func (h *Hub) WaitForStart() {
	<-h.startCh
}

// WaitForStartWithTimeout 带超时的等待Hub启动
func (h *Hub) WaitForStartWithTimeout(timeout time.Duration) error {
	select {
	case <-h.startCh:
		return nil
	case <-time.After(timeout):
		return errorx.NewError(ErrTypeHubStartupTimeout)
	}
}

// IsStarted 检查Hub是否已启动
func (h *Hub) IsStarted() bool {
	return h.started.Load()
}

// IsShutdown 检查Hub是否已关闭
func (h *Hub) IsShutdown() bool {
	return h.shutdown.Load()
}

// SafeShutdown 安全关闭Hub，确保所有操作完成
func (h *Hub) SafeShutdown() error {
	// 检查是否已经关闭
	if h.shutdown.Load() {
		h.logger.Debug("Hub已经关闭，跳过重复关闭操作")
		return nil
	}

	// 安全获取客户端数量
	h.mutex.RLock()
	clientCount := len(h.clients)
	h.mutex.RUnlock()

	// 记录关闭开始日志
	h.logger.InfoKV("Hub开始安全关闭",
		"node_id", h.nodeID,
		"connected_clients", clientCount,
	)

	// 设置关闭标志
	if !h.shutdown.CompareAndSwap(false, true) {
		return nil // 已经在关闭中
	}

	// 取消context
	h.cancel()

	// 等待所有goroutine完成，带超时保护
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()

	// 在测试环境中使用更短的超时时间
	timeout := 30 * time.Second
	if testing.Testing() {
		timeout = 5 * time.Second
	}

	select {
	case <-done:
		// 正常关闭
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stats, _ := h.statsRepo.GetNodeStats(ctx, h.nodeID)
		cancel()

		finalStats := map[string]interface{}{
			"total_connections": int64(0),
			"messages_sent":     int64(0),
			"broadcasts_sent":   int64(0),
		}
		if stats != nil {
			finalStats["total_connections"] = stats.TotalConnections
			finalStats["messages_sent"] = stats.MessagesSent
			finalStats["broadcasts_sent"] = stats.BroadcastsSent
		}

		h.logger.InfoKV("Hub安全关闭成功",
			"node_id", h.nodeID,
			"shutdown_timeout", timeout,
			"final_stats", finalStats,
		)
	case <-time.After(timeout):
		// 强制关闭所有客户端连接
		h.logger.WarnKV("Hub关闭超时，强制关闭所有连接",
			"node_id", h.nodeID,
			"timeout", timeout,
			"remaining_clients", len(h.clients),
			"remaining_sse_clients", len(h.sseClients),
		)
		h.mutex.Lock()
		for _, client := range h.clients {
			if client.Conn != nil {
				client.Conn.Close()
			}
			select {
			case <-client.SendChan:
			default:
				close(client.SendChan)
			}
		}
		h.mutex.Unlock()
		return ErrHubShutdownTimeout
	}

	// 关闭所有客户端连接和channel
	h.mutex.Lock()
	for _, client := range h.clients {
		if client.Conn != nil {
			client.Conn.Close()
		}
		close(client.SendChan)
	}
	h.mutex.Unlock()

	// 关闭SSE连接
	h.sseMutex.Lock()
	for _, conn := range h.sseClients {
		close(conn.CloseCh)
	}
	h.sseMutex.Unlock()

	return nil
}

// Register 注册客户端
func (h *Hub) Register(client *Client) {
	h.logger.DebugKV("客户端注册请求", "client_id", client.ID, "user_id", client.UserID)
	h.register <- client
}

// Unregister 注销客户端
func (h *Hub) Unregister(client *Client) {
	h.logger.DebugKV("客户端注销请求", "client_id", client.ID, "user_id", client.UserID)
	h.unregister <- client
}

// SendToUser 发送消息给指定用户（自动填充发送者信息）
func (h *Hub) SendToUser(ctx context.Context, toUserID string, msg *HubMessage) error {
	// 直接修改原始消息对象，避免引用断裂
	if msg.Sender == "" {
		if senderID, ok := ctx.Value(ContextKeySenderID).(string); ok {
			msg.Sender = senderID
		} else if userID, ok := ctx.Value(ContextKeyUserID).(string); ok {
			msg.Sender = userID
		}
	}

	msg.Receiver = toUserID
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 确保消息ID存在
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%s-%d", toUserID, time.Now().UnixNano())
	}

	// 尝试发送到broadcast队列
	select {
	case h.broadcast <- msg:
		h.logger.DebugKV("消息已广播", "message_id", msg.ID, "from", msg.Sender, "to", msg.Receiver, "type", msg.MessageType)
		// 记录消息到数据库
		go h.recordMessageToDatabase(msg, nil)
		return nil
	default:
		// broadcast队列满，尝试放入待发送队列
		select {
		case h.pendingMessages <- msg:
			return nil
		default:
			err := ErrQueueAndPendingFull
			// 记录消息发送失败日志
			h.logger.ErrorKV("消息发送失败", "message_id", msg.ID, "from", msg.Sender, "to", msg.Receiver, "type", msg.MessageType, "error", err)
			// 通知队列满处理器
			h.notifyQueueFull(msg, toUserID, "all_queues", err)
			return err
		}
	}
}

// SendToUserWithRetry 带重试机制的发送消息给指定用户
func (h *Hub) SendToUserWithRetry(ctx context.Context, toUserID string, msg *HubMessage) *SendResult {
	result := &SendResult{
		Attempts: make([]SendAttempt, 0, h.config.MaxRetries+1),
	}

	startTime := time.Now()

	// 创建 go-toolbox retry 实例用于延迟计算和条件判断
	retryInstance := retry.NewRetryWithCtx(ctx).
		SetAttemptCount(h.config.MaxRetries + 1). // +1 因为第一次不是重试
		SetInterval(h.config.BaseDelay).
		SetConditionFunc(h.isRetryableError)

	// 执行带详细记录的重试逻辑
	finalErr := retryInstance.Do(func() error {
		attemptStart := time.Now()
		attemptNumber := len(result.Attempts) + 1

		err := h.SendToUser(ctx, toUserID, msg)
		duration := time.Since(attemptStart)

		// 记录每次尝试
		sendAttempt := SendAttempt{
			AttemptNumber: attemptNumber,
			StartTime:     attemptStart,
			Duration:      duration,
			Error:         err,
			Success:       err == nil,
		}
		result.Attempts = append(result.Attempts, sendAttempt)

		return err
	})

	// 设置最终结果
	result.Success = finalErr == nil
	result.FinalError = finalErr
	result.TotalTime = time.Since(startTime)
	result.TotalRetries = len(result.Attempts) - 1 // 减1因为第一次不算重试

	// 触发失败回调（只有在所有重试都失败后才触发）
	if finalErr != nil {
		h.notifySendFailureAfterRetries(msg, toUserID, result)
	}

	return result
}

// isRetryableError 判断错误是否可以重试 - 完全基于错误类型
func (h *Hub) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 使用errors包进行类型判断
	return IsRetryableError(err)
}

// shouldRetryBasedOnErrorPattern 基于错误模式决定是否重试（推荐使用 isRetryableError）
func (h *Hub) shouldRetryBasedOnErrorPattern(err error) bool {
	return h.isRetryableError(err)
}

// notifySendFailureAfterRetries 在所有重试失败后通知失败处理器
func (h *Hub) notifySendFailureAfterRetries(msg *HubMessage, recipient string, result *SendResult) {
	// 记录重试最终失败的日志
	h.logger.ErrorKV("消息发送重试失败",
		"message_id", msg.ID,
		"sender", msg.Sender,
		"receiver", recipient,
		"message_type", msg.MessageType,
		"total_retries", result.TotalRetries,
		"total_time", result.TotalTime,
		"final_error", result.FinalError,
	)

	h.failureHandlerMutex.RLock()
	handlers := make([]SendFailureHandler, len(h.sendFailureHandlers))
	copy(handlers, h.sendFailureHandlers)
	h.failureHandlerMutex.RUnlock()

	for _, handler := range handlers {
		go func(h SendFailureHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			// 使用特殊的失败原因表示这是经过重试后的失败
			reason := fmt.Sprintf("retry_exhausted_%d_attempts", result.TotalRetries+1)
			h.HandleSendFailure(msg, recipient, reason, result.FinalError)
		}(handler)
	}
}

// Broadcast 广播消息
func (h *Hub) Broadcast(ctx context.Context, msg *HubMessage) {
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	select {
	case h.broadcast <- msg:
		// 成功放入广播队列
	default:
		// broadcast队列满，尝试放入待发送队列
		h.logger.WarnKV("广播队列已满，尝试使用待发送队列",
			"message_id", msg.ID,
			"sender", msg.Sender,
			"message_type", msg.MessageType,
		)
		select {
		case h.pendingMessages <- msg:
			// 成功放入待发送队列
		default:
			// 两个队列都满，静默丢弃（广播消息不返回错误）
			h.logger.ErrorKV("所有队列已满，丢弃广播消息",
				"message_id", msg.ID,
				"sender", msg.Sender,
				"message_type", msg.MessageType,
				"content_length", len(msg.Content),
			)
		}
	}
}

// processPendingMessages 处理待发送消息队列
func (h *Hub) processPendingMessages() {
	h.wg.Add(1)
	defer h.wg.Done()

	// 记录待发送消息处理器启动
	h.logger.InfoKV("待发送消息处理器启动",
		"node_id", h.nodeID,
		"check_interval", "100ms",
	)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	processedCount := 0
	timeoutCount := 0

	for {
		select {
		case <-h.ctx.Done():
			// 记录处理器关闭统计
			h.logger.InfoKV("待发送消息处理器关闭",
				"processed_count", processedCount,
				"timeout_count", timeoutCount,
			)
			return
		case msg := <-h.pendingMessages:
			// 尝试将消息放入broadcast队列
			select {
			case h.broadcast <- msg:
				// 成功发送
				processedCount++
			case <-time.After(5 * time.Second):
				// 超时，丢弃消息
				timeoutCount++
				h.logger.WarnKV("待发送消息处理超时",
					"message_id", msg.ID,
					"sender", msg.Sender,
					"receiver", msg.Receiver,
					"message_type", msg.MessageType,
					"timeout", "5s",
				)
			}
		case <-ticker.C:
			// 定期检查，避免goroutine阻塞
			if processedCount%100 == 0 && processedCount > 0 {
				h.logger.InfoKV("待发送消息处理进度",
					"processed_count", processedCount,
					"timeout_count", timeoutCount,
					"success_rate", fmt.Sprintf("%.2f%%", float64(processedCount)/float64(processedCount+timeoutCount)*100),
				)
			}
		}
	}
}

// RegisterSSE 注册SSE连接
func (h *Hub) RegisterSSE(conn *SSEConnection) {
	h.sseMutex.Lock()
	defer h.sseMutex.Unlock()
	h.sseClients[conn.UserID] = conn

	// 记录SSE连接注册日志
	h.logger.InfoKV("SSE连接建立", "user_id", conn.UserID)
	h.logger.InfoKV("SSE连接已注册",
		"user_id", conn.UserID,
		"total_sse_clients", len(h.sseClients),
	)
}

// UnregisterSSE 注销SSE连接
func (h *Hub) UnregisterSSE(userID string) {
	h.sseMutex.Lock()
	defer h.sseMutex.Unlock()
	if conn, exists := h.sseClients[userID]; exists {
		// 记录SSE连接注销日志
		h.logger.InfoKV("SSE连接断开", "user_id", userID)
		h.logger.InfoKV("SSE连接已注销",
			"user_id", userID,
			"remaining_sse_clients", len(h.sseClients)-1,
		)

		close(conn.CloseCh)
		delete(h.sseClients, userID)
	}
}

// SendToUserViaSSE 通过SSE发送消息
func (h *Hub) SendToUserViaSSE(userID string, msg *HubMessage) bool {
	h.sseMutex.RLock()
	conn, exists := h.sseClients[userID]
	h.sseMutex.RUnlock()

	if !exists {
		h.logger.WarnKV("SSE用户不存在",
			"user_id", userID,
			"message_id", msg.ID,
			"message_type", msg.MessageType,
		)
		return false
	}

	select {
	case conn.MessageCh <- msg:
		conn.LastActive = time.Now()
		// 记录SSE消息发送成功
		h.logger.DebugKV("SSE消息发送", "message_id", msg.ID, "from", msg.Sender, "to", userID, "type", msg.MessageType)
		h.logger.InfoKV("SSE消息发送成功",
			"user_id", userID,
			"message_id", msg.ID,
			"message_type", msg.MessageType,
		)
		return true
	default:
		// SSE消息队列满
		h.logger.WarnKV("SSE消息队列已满",
			"user_id", userID,
			"message_id", msg.ID,
			"message_type", msg.MessageType,
		)
		return false
	}
}

// SendToUserWithAck 发送消息给指定用户并等待ACK确认
func (h *Hub) SendToUserWithAck(ctx context.Context, toUserID string, msg *HubMessage, timeout time.Duration, maxRetry int) (*AckMessage, error) {
	// 临时启用ACK功能 - 绕过配置检查
	enableAck := true // h.safeConfig.Field("EnableAck").Bool(false)

	if !enableAck {
		// 如果未启用ACK，直接发送
		h.logger.InfoKV("ACK未启用，使用普通发送",
			"message_id", msg.ID,
			"to_user", toUserID,
		)
		return nil, h.SendToUser(ctx, toUserID, msg)
	}

	// 生成消息ID
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%s-%d", toUserID, time.Now().UnixNano())
	}
	msg.RequireAck = true

	// 记录ACK发送开始
	h.logger.InfoKV("ACK消息发送开始",
		"message_id", msg.ID,
		"to_user", toUserID,
		"timeout", timeout,
		"max_retry", maxRetry,
		"require_ack", true,
		"enable_ack", enableAck,
	)

	// 检查用户是否在线
	h.mutex.RLock()
	_, isOnline := h.userToClient[toUserID]
	h.mutex.RUnlock()

	if !isOnline {
		// 用户离线，使用离线处理器处理消息
		if h.ackManager.offlineHandler != nil {
			if err := h.ackManager.offlineHandler.HandleOfflineMessage(msg); err != nil {
				return &AckMessage{
					MessageID: msg.ID,
					Status:    AckStatusFailed,
					Timestamp: time.Now(),
					Error:     fmt.Sprintf("用户离线且离线消息处理失败: %v", err),
				}, err
			}

			// 离线消息处理成功
			return &AckMessage{
				MessageID: msg.ID,
				Status:    AckStatusConfirmed,
				Timestamp: time.Now(),
				Error:     "用户离线，消息已存储",
			}, nil
		}

		err := errorx.NewError(ErrTypeUserOffline)
		// 通知用户离线处理器
		h.notifyUserOffline(msg, toUserID, err)

		return &AckMessage{
			MessageID: msg.ID,
			Status:    AckStatusFailed,
			Timestamp: time.Now(),
			Error:     "用户离线且未配置离线消息处理器",
		}, err
	}

	// 使用配置中的ACK超时时间，如果传入的timeout > 0则使用传入值
	ackTimeout := h.safeConfig.Field("AckTimeoutMs").Duration(500 * time.Millisecond)
	if timeout > 0 {
		ackTimeout = timeout
	}

	// 添加到待确认队列
	pm := h.ackManager.AddPendingMessage(msg, ackTimeout, maxRetry)
	defer h.ackManager.RemovePendingMessage(msg.ID)

	// 定义重试函数
	attemptNum := 0
	retryFunc := func() error {
		attemptNum++
		err := h.SendToUser(ctx, toUserID, msg)
		return err
	}

	// 首次发送
	if err := retryFunc(); err != nil {
		return &AckMessage{
			MessageID: msg.ID,
			Status:    AckStatusFailed,
			Timestamp: time.Now(),
			Error:     err.Error(),
		}, err
	}

	// 等待ACK确认并支持重试
	ackMsg, err := pm.WaitForAckWithRetry(retryFunc)

	return ackMsg, err
}

// SetOfflineMessageHandler 设置离线消息处理器
func (h *Hub) SetOfflineMessageHandler(handler OfflineMessageHandler) {
	if h.ackManager != nil {
		h.ackManager.offlineHandler = handler
	}
}

// SetOnlineStatusRepository 设置在线状态仓库（Redis）
func (h *Hub) SetOnlineStatusRepository(repo OnlineStatusRepository) {
	h.onlineStatusRepo = repo
	h.logger.InfoKV("在线状态仓库已设置", "repository_type", "redis")
}

// SetMessageRecordRepository 设置消息记录仓库（MySQL）
func (h *Hub) SetMessageRecordRepository(repo MessageRecordRepository) {
	h.messageRecordRepo = repo
	h.logger.InfoKV("消息记录仓库已设置", "repository_type", "mysql")
}

// SetHubStatsRepository 设置 Hub 统计仓库（Redis）
func (h *Hub) SetHubStatsRepository(repo HubStatsRepository) {
	h.statsRepo = repo
	h.logger.InfoKV("Hub统计仓库已设置", "repository_type", "redis")

	// 设置启动时间到 Redis
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = repo.SetStartTime(ctx, h.nodeID, time.Now().Unix())
}

// AddSendFailureHandler 添加通用消息发送失败处理器
func (h *Hub) AddSendFailureHandler(handler SendFailureHandler) {
	h.failureHandlerMutex.Lock()
	defer h.failureHandlerMutex.Unlock()
	h.sendFailureHandlers = append(h.sendFailureHandlers, handler)
}

// AddQueueFullHandler 添加队列满处理器
func (h *Hub) AddQueueFullHandler(handler QueueFullHandler) {
	h.failureHandlerMutex.Lock()
	defer h.failureHandlerMutex.Unlock()
	h.queueFullHandlers = append(h.queueFullHandlers, handler)
}

// AddUserOfflineHandler 添加用户离线处理器
func (h *Hub) AddUserOfflineHandler(handler UserOfflineHandler) {
	h.failureHandlerMutex.Lock()
	defer h.failureHandlerMutex.Unlock()
	h.userOfflineHandlers = append(h.userOfflineHandlers, handler)
}

// AddConnectionErrorHandler 添加连接错误处理器
func (h *Hub) AddConnectionErrorHandler(handler ConnectionErrorHandler) {
	h.failureHandlerMutex.Lock()
	defer h.failureHandlerMutex.Unlock()
	h.connectionErrorHandlers = append(h.connectionErrorHandlers, handler)
}

// AddTimeoutHandler 添加超时处理器
func (h *Hub) AddTimeoutHandler(handler TimeoutHandler) {
	h.failureHandlerMutex.Lock()
	defer h.failureHandlerMutex.Unlock()
	h.timeoutHandlers = append(h.timeoutHandlers, handler)
}

// RemoveSendFailureHandler 移除消息发送失败处理器
func (h *Hub) RemoveSendFailureHandler(handler SendFailureHandler) {
	h.failureHandlerMutex.Lock()
	defer h.failureHandlerMutex.Unlock()
	for i, existingHandler := range h.sendFailureHandlers {
		if existingHandler == handler {
			h.sendFailureHandlers = append(h.sendFailureHandlers[:i], h.sendFailureHandlers[i+1:]...)
			break
		}
	}
}

// notifySendFailure 通知所有注册的发送失败处理器
func (h *Hub) notifySendFailure(msg *HubMessage, recipient string, reason string, err error) {
	// 记录发送失败通知
	h.logger.ErrorKV("触发发送失败处理器",
		"message_id", msg.ID,
		"recipient", recipient,
		"reason", reason,
		"error", err,
		"handler_count", len(h.sendFailureHandlers),
	)

	h.failureHandlerMutex.RLock()
	handlers := make([]SendFailureHandler, len(h.sendFailureHandlers))
	copy(handlers, h.sendFailureHandlers)
	h.failureHandlerMutex.RUnlock()

	for _, handler := range handlers {
		go func(h SendFailureHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			h.HandleSendFailure(msg, recipient, reason, err)
		}(handler)
	}
}

// notifyQueueFull 通知队列满处理器
func (h *Hub) notifyQueueFull(msg *HubMessage, recipient string, queueType string, err error) {
	// 记录队列满通知
	h.logger.WarnKV("触发队列满处理器",
		"message_id", msg.ID,
		"recipient", recipient,
		"queue_type", queueType,
		"error", err,
		"queue_handlers", len(h.queueFullHandlers),
		"general_handlers", len(h.sendFailureHandlers),
	)

	h.failureHandlerMutex.RLock()
	queueHandlers := make([]QueueFullHandler, len(h.queueFullHandlers))
	copy(queueHandlers, h.queueFullHandlers)
	generalHandlers := make([]SendFailureHandler, len(h.sendFailureHandlers))
	copy(generalHandlers, h.sendFailureHandlers)
	h.failureHandlerMutex.RUnlock()

	// 调用专门的队列满处理器
	for _, handler := range queueHandlers {
		go func(h QueueFullHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			h.HandleQueueFull(msg, recipient, queueType, err)
		}(handler)
	}

	// 同时调用通用处理器
	for _, handler := range generalHandlers {
		go func(h SendFailureHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			h.HandleSendFailure(msg, recipient, SendFailureReasonQueueFull, err)
		}(handler)
	}
}

// notifyUserOffline 通知用户离线处理器
func (h *Hub) notifyUserOffline(msg *HubMessage, userID string, err error) {
	h.failureHandlerMutex.RLock()
	offlineHandlers := make([]UserOfflineHandler, len(h.userOfflineHandlers))
	copy(offlineHandlers, h.userOfflineHandlers)
	generalHandlers := make([]SendFailureHandler, len(h.sendFailureHandlers))
	copy(generalHandlers, h.sendFailureHandlers)
	h.failureHandlerMutex.RUnlock()

	// 调用专门的用户离线处理器
	for _, handler := range offlineHandlers {
		go func(h UserOfflineHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			h.HandleUserOffline(msg, userID, err)
		}(handler)
	}

	// 同时调用通用处理器
	for _, handler := range generalHandlers {
		go func(h SendFailureHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			h.HandleSendFailure(msg, userID, SendFailureReasonUserOffline, err)
		}(handler)
	}
}

// SetMessageExpireDuration 设置消息过期时间
func (h *Hub) SetMessageExpireDuration(duration time.Duration) {
	if h.ackManager != nil {
		h.ackManager.expireDuration = duration
	}
}

// HandleAck 处理ACK确认消息
func (h *Hub) HandleAck(ackMsg *AckMessage) {
	// 记录ACK消息处理
	h.logger.InfoKV("收到ACK确认",
		"message_id", ackMsg.MessageID,
		"status", ackMsg.Status,
		"timestamp", ackMsg.Timestamp,
	)

	h.ackManager.ConfirmMessage(ackMsg.MessageID, ackMsg)
}

// GetOnlineUsers 获取在线用户
func (h *Hub) GetOnlineUsers() []string {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	users := make(map[string]bool)
	for userID := range h.userToClient {
		users[userID] = true
	}

	h.sseMutex.RLock()
	for userID := range h.sseClients {
		users[userID] = true
	}
	h.sseMutex.RUnlock()

	result := make([]string, 0, len(users))
	for userID := range users {
		result = append(result, userID)
	}
	return result
}

// GetStats 获取统计信息
func (h *Hub) GetStats() map[string]interface{} {
	h.mutex.RLock()
	wsCount := len(h.clients)
	h.mutex.RUnlock()

	h.sseMutex.RLock()
	sseCount := len(h.sseClients)
	h.sseMutex.RUnlock()

	return map[string]interface{}{
		"node_id":           h.nodeID,
		"websocket_count":   wsCount,
		"sse_count":         sseCount,
		"total_connections": wsCount + sseCount,
	}
}

// GetNodeID 获取节点ID
func (h *Hub) GetNodeID() string {
	return h.nodeID
}

// Shutdown 关闭Hub（保持向后兼容）
func (h *Hub) Shutdown() {
	_ = h.SafeShutdown() // 忽略错误，保持原有行为
}

// === 内部方法 ===

func (h *Hub) handleRegister(client *Client) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.ErrorKV("handleRegister panic",
				"client_id", client.ID,
				"user_id", client.UserID,
				"panic", r,
			)
		}
	}()

	h.mutex.Lock()
	defer h.mutex.Unlock()

	// 关闭旧连接
	if existingClient, exists := h.userToClient[client.UserID]; exists {
		h.logger.InfoKV("断开旧连接", "client_id", existingClient.ID, "user_id", existingClient.UserID)
		if existingClient.Conn != nil {
			existingClient.Conn.Close()
		}
		h.removeClientUnsafe(existingClient)
	}

	// 添加新客户端
	h.clients[client.ID] = client
	h.userToClient[client.UserID] = client

	// 初始化心跳时间
	now := time.Now()
	if client.LastHeartbeat.IsZero() {
		client.LastHeartbeat = now
	}
	if client.LastSeen.IsZero() {
		client.LastSeen = now
	}

	if client.UserType == UserTypeAgent || client.UserType == UserTypeBot {
		h.agentClients[client.UserID] = client
	}

	// 使用atomic无锁更新统计信息
	// 同步统计到 Redis
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.statsRepo.IncrementTotalConnections(ctx, h.nodeID, 1)
		_ = h.statsRepo.SetActiveConnections(ctx, h.nodeID, int64(len(h.clients)))
		_ = h.statsRepo.UpdateNodeHeartbeat(ctx, h.nodeID)
	}()

	// 记录成功注册日志
	h.logger.InfoKV("客户端已连接", "client_id", client.ID, "user_id", client.UserID, "user_type", client.UserType)
	h.logger.InfoKV("客户端连接成功",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"active_connections", len(h.clients),
	)

	// 同步在线状态到 Redis
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		onlineInfo := &OnlineClientInfo{
			ClientID:      client.ID,
			UserID:        client.UserID,
			UserType:      client.UserType,
			NodeID:        h.nodeID,
			NodeIP:        h.config.NodeIP,
			ClientIP:      client.ClientIP,
			ConnectTime:   time.Now(),
			LastSeen:      client.LastSeen,
			LastHeartbeat: client.LastHeartbeat,
			ClientType:    client.ClientType,
			Status:        client.Status,
			Metadata:      client.Metadata,
		}

		if err := h.onlineStatusRepo.SetOnline(ctx, client.UserID, onlineInfo, 0); err != nil {
			h.logger.ErrorKV("同步在线状态到Redis失败",
				"user_id", client.UserID,
				"error", err,
			)
		}
	}()

	// 发送欢迎消息
	h.sendWelcomeMessage(client)

	// 启动客户端读写协程（只有真实连接才需要）
	if client.Conn != nil {
		go h.handleClientWrite(client)
	}
}

func (h *Hub) handleUnregister(client *Client) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.removeClientUnsafe(client)
}

func (h *Hub) removeClientUnsafe(client *Client) {
	if _, exists := h.clients[client.ID]; !exists {
		return
	}

	// 记录客户端移除日志
	h.logger.InfoKV("客户端已断开", "client_id", client.ID, "user_id", client.UserID)
	h.logger.InfoKV("客户端断开连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"remaining_connections", len(h.clients)-1,
	)

	delete(h.clients, client.ID)
	delete(h.userToClient, client.UserID)

	if client.UserType == UserTypeAgent || client.UserType == UserTypeBot {
		delete(h.agentClients, client.UserID)
	}

	// 同步活跃连接数到 Redis
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.statsRepo.SetActiveConnections(ctx, h.nodeID, int64(len(h.clients)))
	}()

	// 从 Redis 移除在线状态
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if err := h.onlineStatusRepo.SetOffline(ctx, client.UserID); err != nil {
			h.logger.ErrorKV("从Redis移除在线状态失败",
				"user_id", client.UserID,
				"error", err,
			)
		}
	}()

	if client.SendChan != nil {
		defer func() { recover() }()
		close(client.SendChan)
	}
}

func (h *Hub) handleBroadcast(msg *HubMessage) {
	switch {
	case msg.Receiver != "": // 点对点消息 - 最快路径
		h.mutex.RLock()
		client := h.userToClient[msg.Receiver]
		h.mutex.RUnlock()

		if client != nil {
			h.sendToClient(client, msg)
			h.logger.DebugKV("消息已发送", "message_id", msg.ID, "from", msg.Sender, "to", msg.Receiver, "type", msg.MessageType)
		} else {
			// 客户端不在线，尝试SSE
			sent := h.SendToUserViaSSE(msg.Receiver, msg)
			if sent {
				h.logger.DebugKV("消息已发送", "message_id", msg.ID, "from", msg.Sender, "to", msg.Receiver, "type", msg.MessageType)
			} else {
				// SSE也失败，记录用户离线
				h.logger.WarnKV("用户离线", "message_id", msg.ID, "from", msg.Sender, "to", msg.Receiver, "type", msg.MessageType)
				h.logger.WarnKV("用户离线，消息发送失败",
					"message_id", msg.ID,
					"sender", msg.Sender,
					"receiver", msg.Receiver,
					"message_type", msg.MessageType,
				)
			}
		}

	default: // 广播消息
		// 统计广播数到 Redis
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			_ = h.statsRepo.IncrementBroadcastsSent(ctx, h.nodeID, 1)
		}()

		// 记录广播消息日志
		h.logger.DebugKV("广播消息已发送", "message_id", msg.ID, "from", msg.Sender, "type", msg.MessageType, "client_count", len(h.clients))
		h.logger.InfoKV("发送广播消息",
			"message_id", msg.ID,
			"sender", msg.Sender,
			"message_type", msg.MessageType,
			"content_length", len(msg.Content),
			"target_clients", len(h.clients),
		)

		// 复制客户端列表以避免在遍历时持有锁
		h.mutex.RLock()
		clients := make([]*Client, 0, len(h.clients))
		for _, client := range h.clients {
			clients = append(clients, client)
		}
		h.mutex.RUnlock()

		// 在释放锁后发送消息
		for _, client := range clients {
			h.sendToClient(client, msg)
		}

		h.sseMutex.RLock()
		for _, conn := range h.sseClients {
			select {
			case conn.MessageCh <- msg:
			default:
			}
		}
		h.sseMutex.RUnlock()
	}
}

// fastMarshalMessage 高效消息序列化方法，包含完整的HubMessage结构体字段
func (h *Hub) fastMarshalMessage(msg *HubMessage, buf []byte) ([]byte, error) {
	// 使用标准 json.Marshal 确保正确的JSON格式
	// 虽然性能稍低，但确保了数据格式的正确性和兼容性
	return json.Marshal(msg)
}

func (h *Hub) sendToClient(client *Client, msg *HubMessage) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.ErrorKV("sendToClient panic",
				"client_id", client.ID,
				"user_id", client.UserID,
				"message_id", msg.ID,
				"message_type", msg.MessageType,
				"panic", r,
			)
		}
	}()

	// 检查Hub是否已关闭
	if h.shutdown.Load() {
		return
	}

	// 使用对象池获取字节缓冲
	buf := h.msgPool.Get().([]byte)
	buf = buf[:0] // 重置长度，保留容量
	defer h.msgPool.Put(buf)

	// 高效序列化 - 避免反射和内存分配
	data, err := h.fastMarshalMessage(msg, buf)
	if err != nil {
		h.logger.ErrorKV("消息序列化失败",
			"message_id", msg.ID,
			"client_id", client.ID,
			"user_id", client.UserID,
			"error", err,
		)

		return
	}
	// 再次检查，避免在序列化过程中 shutdown
	if h.shutdown.Load() {
		return
	}

	select {
	case client.SendChan <- data:
		// 同步到 Redis
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			_ = h.statsRepo.IncrementMessagesSent(ctx, h.nodeID, 1)
		}()

		// 更新指标收集器
	case <-h.ctx.Done():
		return
	default:
		// 队列满，跳过该消息
		h.logger.WarnKV("客户端发送队列已满，跳过消息",
			"client_id", client.ID,
			"user_id", client.UserID,
			"message_id", msg.ID,
			"message_type", msg.MessageType,
		)

	}
}

func (h *Hub) handleClientWrite(client *Client) {
	h.wg.Add(1)
	defer h.wg.Done()
	defer func() {
		// 记录客户端写入协程结束
		h.logger.InfoKV("客户端写入协程结束",
			"client_id", client.ID,
			"user_id", client.UserID,
		)
		if client.Conn != nil {
			client.Conn.Close()
		}
		h.Unregister(client)
	}()

	// 记录客户端写入协程启动
	h.logger.InfoKV("客户端写入协程启动",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
	)

	messagesSent := 0
	messagesFailed := 0

	for {
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				h.logger.InfoKV("客户端发送通道关闭",
					"client_id", client.ID,
					"user_id", client.UserID,
					"messages_sent", messagesSent,
					"messages_failed", messagesFailed,
				)
				return
			}

			if client.Conn != nil {
				client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
					// 记录写入失败
					messagesFailed++
					h.logger.ErrorKV("客户端消息写入失败",
						"client_id", client.ID,
						"user_id", client.UserID,
						"error", err,
						"message_size", len(message),
					)
					return
				}
				messagesSent++
			}
		case <-h.ctx.Done():
			h.logger.InfoKV("客户端写入协程因Hub关闭而结束",
				"client_id", client.ID,
				"user_id", client.UserID,
				"messages_sent", messagesSent,
				"messages_failed", messagesFailed,
			)
			return
		}
	}
}

func (h *Hub) checkHeartbeat() {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	now := time.Now()
	timeoutClients := 0
	timeoutSSE := 0

	// 使用配置的超时时间，默认90秒
	timeoutDuration := h.heartbeatTimeout
	if timeoutDuration == 0 {
		timeoutDuration = time.Duration(h.safeConfig.GetInt("ClientTimeout", 90)) * time.Second
	}

	for _, client := range h.clients {
		// 优先使用LastHeartbeat，如果为零则使用LastSeen
		lastActive := client.LastHeartbeat
		if lastActive.IsZero() {
			lastActive = client.LastSeen
		}

		if now.Sub(lastActive) > timeoutDuration {
			h.logger.WarnKV("心跳超时", "client_id", client.ID, "user_id", client.UserID, "last_heartbeat", client.LastHeartbeat)

			// 调用心跳超时回调函数
			if h.heartbeatTimeoutHandler != nil {
				h.heartbeatTimeoutHandler(client.ID, client.UserID, lastActive)
			}

			// 关闭连接
			if client.Conn != nil {
				client.Conn.Close()
			}
			h.removeClientUnsafe(client)
			timeoutClients++
		}
	}

	// 检查SSE超时
	h.sseMutex.Lock()
	for userID, conn := range h.sseClients {
		if now.Sub(conn.LastActive) > time.Duration(h.safeConfig.GetInt("SSETimeout", 120))*time.Second {
			h.logger.WarnKV("SSE连接超时", "user_id", userID, "last_heartbeat", conn.LastActive)
			close(conn.CloseCh)
			delete(h.sseClients, userID)
			timeoutSSE++
		}
	}
	h.sseMutex.Unlock()

	// 记录心跳检查统计
	if timeoutClients > 0 || timeoutSSE > 0 {
		h.logger.InfoKV("心跳检查完成",
			"timeout_clients", timeoutClients,
			"timeout_sse", timeoutSSE,
			"remaining_clients", len(h.clients),
			"remaining_sse", len(h.sseClients),
		)
	}
}

// SetWelcomeProvider 设置欢迎消息提供者
func (h *Hub) SetWelcomeProvider(provider WelcomeMessageProvider) {
	h.welcomeProvider = provider
}

// SetHeartbeatConfig 设置心跳配置
// interval: 心跳间隔，建议30秒
// timeout: 心跳超时时间，建议90秒（interval的3倍）
func (h *Hub) SetHeartbeatConfig(interval, timeout time.Duration) {
	h.heartbeatInterval = interval
	h.heartbeatTimeout = timeout
}

// UpdateHeartbeat 更新客户端心跳时间
func (h *Hub) UpdateHeartbeat(clientID string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if client, exists := h.clients[clientID]; exists {
		now := time.Now()
		client.LastHeartbeat = now
		client.LastSeen = now
	}
}

func (h *Hub) sendWelcomeMessage(client *Client) {
	provider := h.welcomeProvider

	if provider == nil {
		return
	}

	extraData := map[string]interface{}{
		"client_id": client.ID,
		"node_id":   h.nodeID,
		"time":      time.Now().Format("2006-01-02 15:04:05"),
	}

	welcomeMsg, enabled, err := provider.GetWelcomeMessage(
		client.UserID,
		client.Role,
		client.UserType,
		extraData,
	)

	if err != nil || !enabled || welcomeMsg == nil {
		return
	}

	msg := &HubMessage{
		MessageType: welcomeMsg.MessageType,
		Sender:      "system",
		Receiver:    client.UserID,
		Content:     welcomeMsg.Content,
		Data:        welcomeMsg.Data,
		CreateAt:    time.Now(),
		Priority:    welcomeMsg.Priority,
		Status:      MessageStatusSent,
	}

	if msg.Data == nil {
		msg.Data = make(map[string]interface{})
	}
	msg.Data["title"] = welcomeMsg.Title

	h.sendToClient(client, msg)
}

// ============================================================================
// 扩展能力 - 批量操作、分组、查询等
// ============================================================================

// SendToMultipleUsers 发送消息给多个用户
func (h *Hub) SendToMultipleUsers(ctx context.Context, userIDs []string, msg *HubMessage) map[string]error {
	errors := make(map[string]error)
	for _, userID := range userIDs {
		if err := h.SendToUser(ctx, userID, msg); err != nil {
			errors[userID] = err
		}
	}
	return errors
}

// BroadcastToGroup 发送消息给特定用户组（按用户类型）
func (h *Hub) BroadcastToGroup(ctx context.Context, userType UserType, msg *HubMessage) int {
	h.mutex.RLock()
	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.UserType == userType {
			clients = append(clients, client)
		}
	}
	h.mutex.RUnlock()

	count := 0
	for _, client := range clients {
		if err := h.SendToUser(ctx, client.UserID, msg); err == nil {
			count++
		}
	}
	return count
}

// BroadcastToRole 发送消息给特定角色用户
func (h *Hub) BroadcastToRole(ctx context.Context, role UserRole, msg *HubMessage) int {
	h.mutex.RLock()
	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.Role == role {
			clients = append(clients, client)
		}
	}
	h.mutex.RUnlock()

	count := 0
	for _, client := range clients {
		if err := h.SendToUser(ctx, client.UserID, msg); err == nil {
			count++
		}
	}
	return count
}

// GetClientsByUserType 获取特定用户类型的所有客户端
func (h *Hub) GetClientsByUserType(userType UserType) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.UserType == userType {
			clients = append(clients, client)
		}
	}
	return clients
}

// GetClientsByRole 获取特定角色的所有客户端
func (h *Hub) GetClientsByRole(role UserRole) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.Role == role {
			clients = append(clients, client)
		}
	}
	return clients
}

// GetClientByID 根据客户端ID获取客户端信息
func (h *Hub) GetClientByID(clientID string) *Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.clients[clientID]
}

// GetClientByUserID 根据用户ID获取客户端信息
func (h *Hub) GetClientByUserID(userID string) *Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.userToClient[userID]
}

// GetClientsCount 获取总客户端连接数
func (h *Hub) GetClientsCount() int {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return len(h.clients)
}

// GetUserStatus 获取用户状态
func (h *Hub) GetUserStatus(userID string) UserStatus {
	h.mutex.RLock()
	client, exists := h.userToClient[userID]
	h.mutex.RUnlock()

	if exists {
		return client.Status
	}
	return UserStatusOffline
}

// UpdateClientMetadata 更新客户端元数据
func (h *Hub) UpdateClientMetadata(clientID string, key string, value interface{}) error {
	h.mutex.RLock()
	client, exists := h.clients[clientID]
	h.mutex.RUnlock()

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	if client.Metadata == nil {
		client.Metadata = make(map[string]interface{})
	}
	client.Metadata[key] = value
	return nil
}

// GetClientMetadata 获取客户端元数据
func (h *Hub) GetClientMetadata(clientID string, key string) (interface{}, bool) {
	h.mutex.RLock()
	client, exists := h.clients[clientID]
	h.mutex.RUnlock()

	if !exists || client.Metadata == nil {
		return nil, false
	}
	val, ok := client.Metadata[key]
	return val, ok
}

// DisconnectUser 主动断开用户连接
func (h *Hub) DisconnectUser(userID string, reason string) error {
	h.mutex.RLock()
	client, exists := h.userToClient[userID]
	h.mutex.RUnlock()

	if !exists {
		return errorx.NewError(ErrTypeUserNotFound, "user_id: %s", userID)
	}

	if client.Conn != nil {
		client.Conn.Close()
	}
	return nil
}

// DisconnectClient 主动断开特定客户端
func (h *Hub) DisconnectClient(clientID string, reason string) error {
	h.mutex.RLock()
	client, exists := h.clients[clientID]
	h.mutex.RUnlock()

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	if client.Conn != nil {
		client.Conn.Close()
	}
	return nil
}

// GetDetailedStats 获取详细的统计信息
func (h *Hub) GetDetailedStats() *HubStats {
	h.mutex.RLock()
	wsCount := len(h.clients)
	agentCount := len(h.agentClients)
	h.mutex.RUnlock()

	h.sseMutex.RLock()
	sseCount := len(h.sseClients)
	h.sseMutex.RUnlock()

	stats := &HubStats{
		// 兼容性字段
		TotalClients:     wsCount + sseCount,
		WebSocketClients: wsCount,
		SSEClients:       sseCount,
		AgentConnections: agentCount,
		QueuedMessages:   len(h.pendingMessages),
		OnlineUsers:      h.GetOnlineUsersCount(),
		Uptime:           h.GetUptime(),
	}

	// 从 Redis 获取统计数据
	if h.statsRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if nodeStats, err := h.statsRepo.GetNodeStats(ctx, h.nodeID); err == nil && nodeStats != nil {
			stats.MessagesSent = nodeStats.MessagesSent
			stats.MessagesReceived = nodeStats.MessagesReceived
			stats.BroadcastsSent = nodeStats.BroadcastsSent
		}
	}

	return stats
}

// GetUptime 获取Hub运行时间（秒）
func (h *Hub) GetUptime() int64 {
	if h.statsRepo == nil {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	nodeStats, err := h.statsRepo.GetNodeStats(ctx, h.nodeID)
	if err != nil || nodeStats == nil || nodeStats.StartTime == 0 {
		return 0
	}

	return time.Now().Unix() - nodeStats.StartTime
}

// GetMessageQueue 获取消息队列长度
func (h *Hub) GetMessageQueue() int {
	return len(h.pendingMessages)
}

// GetClientsByDepartment 按部门获取客户端
func (h *Hub) GetClientsByDepartment(dept Department) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.Department == dept {
			clients = append(clients, client)
		}
	}
	return clients
}

// GetAgentStats 获取座席统计
func (h *Hub) GetAgentStats() map[string]interface{} {
	h.mutex.RLock()
	agentCount := len(h.agentClients)
	h.mutex.RUnlock()

	stats := map[string]interface{}{
		"total_agents": agentCount,
		"agents":       make([]map[string]interface{}, 0),
	}

	h.mutex.RLock()
	agents := make([]map[string]interface{}, 0)
	for _, client := range h.agentClients {
		agents = append(agents, map[string]interface{}{
			"agent_id":    client.ID,
			"user_id":     client.UserID,
			"status":      client.Status,
			"department":  client.Department,
			"max_tickets": client.MaxTickets,
			"last_seen":   client.LastSeen,
		})
	}
	h.mutex.RUnlock()

	stats["agents"] = agents
	return stats
}

// ============================================================================
// 高级扩展功能 - 消息过滤、批处理、条件推送等
// ============================================================================

// SendConditional 条件发送消息 - 根据自定义条件发送给匹配的用户
func (h *Hub) SendConditional(ctx context.Context, condition func(*Client) bool, msg *HubMessage) int {
	h.mutex.RLock()
	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if condition(client) {
			clients = append(clients, client)
		}
	}
	h.mutex.RUnlock()

	count := 0
	for _, client := range clients {
		if err := h.SendToUser(ctx, client.UserID, msg); err == nil {
			count++
		}
	}
	return count
}

// BatchSendToUsers 批量发送消息给多个用户（支持限流）
func (h *Hub) BatchSendToUsers(ctx context.Context, userIDs []string, msg *HubMessage, batchSize int) map[string]error {
	errors := make(map[string]error)

	if batchSize <= 0 {
		batchSize = 100
	}

	for i := 0; i < len(userIDs); i += batchSize {
		end := i + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}

		for _, userID := range userIDs[i:end] {
			if err := h.SendToUser(ctx, userID, msg); err != nil {
				errors[userID] = err
			}
		}

		// 批次间隔，避免堵塞
		select {
		case <-ctx.Done():
			return errors
		case <-time.After(10 * time.Millisecond):
		}
	}

	return errors
}

// GetClientsWithStatus 获取特定状态的所有客户端
func (h *Hub) GetClientsWithStatus(status UserStatus) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.Status == status {
			clients = append(clients, client)
		}
	}
	return clients
}

// GetOnlineUsersCount 获取在线用户总数
func (h *Hub) GetOnlineUsersCount() int {
	return len(h.GetOnlineUsers())
}

// GetConnectionsByUserID 根据用户ID获取所有连接（包括多端登录）
func (h *Hub) GetConnectionsByUserID(userID string) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.UserID == userID {
			clients = append(clients, client)
		}
	}
	return clients
}

// KickOffUser 踢掉用户所有连接
func (h *Hub) KickOffUser(userID string, reason string) int {
	clients := h.GetConnectionsByUserID(userID)

	// 记录踢出操作开始
	h.logger.InfoKV("开始踢出用户所有连接",
		"user_id", userID,
		"reason", reason,
		"connection_count", len(clients),
	)

	for _, client := range clients {
		h.logger.InfoKV("客户端被踢下线", "client_id", client.ID, "user_id", userID)
		if client.Conn != nil {
			client.Conn.Close()
		}
	}

	// 记录踢出结果
	h.logger.InfoKV("用户踢出操作完成",
		"user_id", userID,
		"reason", reason,
		"kicked_connections", len(clients),
	)

	return len(clients)
}

// LimitUserConnections 限制用户最大连接数，断开超出的连接
func (h *Hub) LimitUserConnections(userID string, maxConnections int) int {
	clients := h.GetConnectionsByUserID(userID)
	if len(clients) <= maxConnections {
		h.logger.DebugKV("用户连接数在限制范围内",
			"user_id", userID,
			"current_connections", len(clients),
			"max_connections", maxConnections,
		)
		return 0
	}

	// 记录连接限制操作
	h.logger.WarnKV("用户连接数超限，开始断开旧连接",
		"user_id", userID,
		"current_connections", len(clients),
		"max_connections", maxConnections,
		"to_disconnect", len(clients)-maxConnections,
	)

	// 保留最新的连接，断开旧的
	kicked := 0
	for i := 0; i < len(clients)-maxConnections; i++ {
		h.logger.WarnKV("连接数限制踢下线", "client_id", clients[i].ID, "user_id", userID, "kicked_index", i)
		if clients[i].Conn != nil {
			clients[i].Conn.Close()
			kicked++
		}
	}

	// 记录限制结果
	h.logger.InfoKV("用户连接限制完成",
		"user_id", userID,
		"max_connections", maxConnections,
		"disconnected_count", kicked,
		"remaining_connections", len(clients)-kicked,
	)

	return kicked
}

// ScheduleMessage 定时发送消息
func (h *Hub) ScheduleMessage(ctx context.Context, userID string, msg *HubMessage, delay time.Duration) {
	go func() {
		select {
		case <-time.After(delay):
			h.SendToUser(ctx, userID, msg)
		case <-ctx.Done():
		}
	}()
}

// BroadcastAfterDelay 延迟广播消息
func (h *Hub) BroadcastAfterDelay(ctx context.Context, msg *HubMessage, delay time.Duration) {
	go func() {
		select {
		case <-time.After(delay):
			h.Broadcast(ctx, msg)
		case <-ctx.Done():
		}
	}()
}

// GetClientsByClientType 按客户端类型获取客户端
func (h *Hub) GetClientsByClientType(clientType ClientType) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	clients := make([]*Client, 0)
	for _, client := range h.clients {
		if client.ClientType == clientType {
			clients = append(clients, client)
		}
	}
	return clients
}

// GetConnectionInfo 获取连接详细信息
func (h *Hub) GetConnectionInfo(clientID string) map[string]interface{} {
	h.mutex.RLock()
	client, exists := h.clients[clientID]
	h.mutex.RUnlock()

	if !exists {
		return nil
	}

	return map[string]interface{}{
		"client_id":   client.ID,
		"user_id":     client.UserID,
		"user_type":   client.UserType.String(),
		"role":        client.Role.String(),
		"status":      client.Status.String(),
		"department":  client.Department.String(),
		"client_type": client.ClientType.String(),
		"last_seen":   client.LastSeen,
		"node_id":     client.NodeID,
		"max_tickets": client.MaxTickets,
	}
}

// GetAllConnectionsInfo 获取所有连接详细信息
func (h *Hub) GetAllConnectionsInfo() []map[string]interface{} {
	h.mutex.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mutex.RUnlock()

	infos := make([]map[string]interface{}, 0)
	for _, client := range clients {
		infos = append(infos, h.GetConnectionInfo(client.ID))
	}
	return infos
}

// SendWithCallback 发送消息并注册回调（用于处理ACK或超时）
func (h *Hub) SendWithCallback(ctx context.Context, userID string, msg *HubMessage,
	timeout time.Duration, onSuccess func(), onError func(error)) {
	go func() {
		ackMsg, err := h.SendToUserWithAck(ctx, userID, msg, timeout, 1)

		select {
		case <-time.After(timeout):
			if onError != nil {
				onError(ErrMessageDeliveryTimeout)
			}
		case <-ctx.Done():
			if onError != nil {
				onError(ctx.Err())
			}
		default:
			if err != nil {
				if onError != nil {
					onError(err)
				}
			} else if ackMsg != nil && ackMsg.Status == "success" {
				if onSuccess != nil {
					onSuccess()
				}
			}
		}
	}()
}

// ResetClientStatus 重置客户端状态
func (h *Hub) ResetClientStatus(clientID string, status UserStatus) error {
	h.mutex.RLock()
	client, exists := h.clients[clientID]
	h.mutex.RUnlock()

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	client.Status = status
	return nil
}

// GetClientStats 获取单个客户端统计信息
func (h *Hub) GetClientStats(clientID string) map[string]interface{} {
	info := h.GetConnectionInfo(clientID)
	if info == nil {
		return nil
	}

	return map[string]interface{}{
		"connection_info":     info,
		"connection_duration": time.Since(info["last_seen"].(time.Time)),
	}
}

// FilterClients 按条件过滤客户端
func (h *Hub) FilterClients(predicate func(*Client) bool) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	result := make([]*Client, 0)
	for _, client := range h.clients {
		if predicate(client) {
			result = append(result, client)
		}
	}
	return result
}

// SendToClientsWithRetry 发送消息到客户端列表，支持失败重试
func (h *Hub) SendToClientsWithRetry(ctx context.Context, clients []*Client, msg *HubMessage,
	maxRetry int) (success int, failed int) {
	for _, client := range clients {
		var err error
		for i := 0; i < maxRetry; i++ {
			err = h.SendToUser(ctx, client.UserID, msg)
			if err == nil {
				success++
				break
			}
		}
		if err != nil {
			failed++
		}
	}
	return
}

// GetHubHealth 获取Hub健康状态
func (h *Hub) GetHubHealth() map[string]interface{} {
	h.mutex.RLock()
	wsCount := len(h.clients)
	h.mutex.RUnlock()

	h.sseMutex.RLock()
	sseCount := len(h.sseClients)
	h.sseMutex.RUnlock()

	isShutdown := h.shutdown.Load()

	return map[string]interface{}{
		"status":            "healthy",
		"is_running":        !isShutdown,
		"websocket_count":   wsCount,
		"sse_count":         sseCount,
		"total_connections": wsCount + sseCount,
	}
}

// ClearExpiredConnections 清理超时连接
func (h *Hub) ClearExpiredConnections(timeout time.Duration) int {
	h.mutex.RLock()
	now := time.Now()
	expiredClients := make([]*Client, 0)
	for _, client := range h.clients {
		if now.Sub(client.LastSeen) > timeout {
			expiredClients = append(expiredClients, client)
		}
	}
	h.mutex.RUnlock()

	for _, client := range expiredClients {
		if client.Conn != nil {
			client.Conn.Close()
		}
	}
	return len(expiredClients)
}

// SendPriority 按优先级发送消息（支持消息队列中的优先级排序）
func (h *Hub) SendPriority(ctx context.Context, userID string, msg *HubMessage, priority Priority) error {
	msg.Priority = priority
	return h.SendToUser(ctx, userID, msg)
}

// BroadcastPriority 按优先级广播消息
func (h *Hub) BroadcastPriority(ctx context.Context, msg *HubMessage, priority Priority) {
	msg.Priority = priority
	h.Broadcast(ctx, msg)
}

// ============================================================================
// 统计和监控相关方法
// ============================================================================

// GetMessageStatisticsDetailed 获取详细的消息统计
func (h *Hub) GetMessageStatisticsDetailed() map[string]interface{} {
	// 如果设置了消息记录仓库，获取数据库统计
	var messageStats interface{}
	if h.messageRecordRepo != nil {
		if dbStats, err := h.GetMessageRecordStatistics(); err == nil {
			messageStats = dbStats
		} else {
			messageStats = map[string]interface{}{"error": err.Error()}
		}
	} else {
		messageStats = map[string]interface{}{"enabled": false}
	}

	return map[string]interface{}{
		"message_stats": messageStats,
		"hub_health":    h.GetHubHealth(),
		"agent_stats":   h.GetAgentStats(),
	}
}

// WaitForCondition 等待条件满足（用于测试或同步）
func (h *Hub) WaitForCondition(condition func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if condition() {
			return true
		}

		<-ticker.C
		if time.Now().After(deadline) {
			return false
		}
	}
}

// ============================================================================
// VIP等级相关方法
// ============================================================================

// SendToVIPUsers 发送消息给指定VIP等级及以上用户
func (h *Hub) SendToVIPUsers(ctx context.Context, minVIPLevel VIPLevel, msg *HubMessage) int {
	return h.SendConditional(ctx, func(c *Client) bool {
		return c.VIPLevel.GetLevel() >= minVIPLevel.GetLevel()
	}, msg)
}

// SendToExactVIPLevel 发送消息给指定VIP等级用户
func (h *Hub) SendToExactVIPLevel(ctx context.Context, vipLevel VIPLevel, msg *HubMessage) int {
	return h.SendConditional(ctx, func(c *Client) bool {
		return c.VIPLevel == vipLevel
	}, msg)
}

// SendWithVIPPriority 根据用户VIP等级自动设置消息优先级
func (h *Hub) SendWithVIPPriority(ctx context.Context, userID string, msg *HubMessage) error {
	// 根据用户VIP等级设置消息优先级
	client, exists := h.userToClient[userID]
	if exists {
		// 根据VIP等级自动调整优先级
		vipLevel := client.VIPLevel.GetLevel()
		if vipLevel >= 6 { // V6-V8
			msg.Priority = PriorityHigh
		} else if vipLevel >= 3 { // V3-V5
			msg.Priority = PriorityNormal
		} else { // V0-V2
			msg.Priority = PriorityLow
		}
	}

	return h.SendToUser(ctx, userID, msg)
}

// SendToUserWithClassification 使用完整分类系统发送消息
func (h *Hub) SendToUserWithClassification(ctx context.Context, userID string, msg *HubMessage,
	classification *MessageClassification) error {

	// 设置消息分类信息
	if classification != nil {
		msg.MessageType = classification.Type

		// 根据分类计算优先级
		finalScore := classification.GetFinalPriority()
		if finalScore >= 80 {
			msg.Priority = PriorityHigh
		} else if finalScore >= 50 {
			msg.Priority = PriorityNormal
		} else {
			msg.Priority = PriorityLow
		}

		// 添加分类信息到消息数据中
		if msg.Data == nil {
			msg.Data = make(map[string]interface{})
		}
		msg.Data["classification"] = classification
		msg.Data["priority_score"] = finalScore
		msg.Data["is_critical"] = classification.IsCriticalMessage()
	}

	return h.SendToUser(ctx, userID, msg)
}

// GetVIPStatistics 获取VIP用户统计
func (h *Hub) GetVIPStatistics() map[string]int {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	stats := make(map[string]int)

	// 统计各VIP等级用户数量
	for _, level := range GetAllVIPLevels() {
		stats[string(level)] = 0
	}

	for _, client := range h.clients {
		if client.VIPLevel.IsValid() {
			stats[string(client.VIPLevel)]++
		}
	}

	stats["total_vip"] = 0
	for level, count := range stats {
		if level != "v0" && level != "total_vip" {
			stats["total_vip"] += count
		}
	}

	return stats
}

// FilterVIPClients 筛选VIP用户客户端
func (h *Hub) FilterVIPClients(minLevel VIPLevel) []*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	var vipClients []*Client
	for _, client := range h.clients {
		if client.VIPLevel.GetLevel() >= minLevel.GetLevel() {
			vipClients = append(vipClients, client)
		}
	}

	return vipClients
}

// UpgradeVIPLevel 升级用户VIP等级
func (h *Hub) UpgradeVIPLevel(userID string, newLevel VIPLevel) bool {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	client, exists := h.userToClient[userID]
	if !exists || !newLevel.IsValid() {
		return false
	}

	// 只允许升级，不允许降级
	if newLevel.GetLevel() > client.VIPLevel.GetLevel() {
		client.VIPLevel = newLevel
		return true
	}

	return false
}

// SendToVIPWithPriority 根据VIP等级优先发送
func (h *Hub) SendToVIPWithPriority(ctx context.Context, vipLevel VIPLevel, msg *HubMessage) int {
	// VIP消息优先级更高
	if vipLevel.GetLevel() >= 5 {
		msg.Priority = PriorityHigh
	} else if vipLevel.GetLevel() >= 3 {
		msg.Priority = PriorityNormal
	}

	return h.SendConditional(ctx, func(c *Client) bool {
		return c.VIPLevel.GetLevel() >= vipLevel.GetLevel()
	}, msg)
}

// 安全的查询方法，用于测试和监控
// GetUserClient 获取用户对应的客户端
func (h *Hub) GetUserClient(userID string) *Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	return h.userToClient[userID]
}

// GetClientCount 获取当前连接的客户端数量
func (h *Hub) GetClientCount() int {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	return len(h.clients)
}

// HasClient 检查是否存在指定ID的客户端
func (h *Hub) HasClient(clientID string) bool {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	_, exists := h.clients[clientID]
	return exists
}

// HasUserClient 检查是否存在指定用户ID的客户端
func (h *Hub) HasUserClient(userID string) bool {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	_, exists := h.userToClient[userID]
	return exists
}

// HasSSEClient 检查是否存在指定用户ID的SSE连接
func (h *Hub) HasSSEClient(userID string) bool {
	h.sseMutex.RLock()
	defer h.sseMutex.RUnlock()

	_, exists := h.sseClients[userID]
	return exists
}

// HasAgentClient 检查是否存在指定用户ID的代理客户端
func (h *Hub) HasAgentClient(userID string) bool {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	_, exists := h.agentClients[userID]
	return exists
}

// ============================================================================
// 回调注册方法 - Hub 级别的事件回调 (统一管理所有 OnXxx 方法)
// ============================================================================
// 注意：这些是服务端 Hub 级别的回调，用于处理全局事件
// 客户端级别的回调请参考 wsc.go 中的 Wsc 结构体

// OnHeartbeatTimeout 注册心跳超时回调函数
// 当客户端心跳超时时会调用此回调
//
// 参数:
//   - clientID: 超时的客户端ID
//   - userID: 超时的用户ID
//   - lastHeartbeat: 最后一次心跳时间
//
// 示例:
//
//	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
//	    log.Printf("客户端 %s 心跳超时", clientID)
//	    更新数据库、清理缓存等
//	})
func (h *Hub) OnHeartbeatTimeout(callback HeartbeatTimeoutCallback) {
	h.heartbeatTimeoutHandler = callback
}

// IsUserOnline 检查用户是否在线（从 Redis 查询)
// 参数:
//   - userID: 用户ID
//
// 返回:
//   - bool: 是否在线
//   - error: 错误信息
func (h *Hub) IsUserOnline(userID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.onlineStatusRepo.IsOnline(ctx, userID)
}

// GetUserOnlineInfo 获取用户在线信息
// 参数:
//   - userID: 用户ID
//
// 返回:
//   - *OnlineClientInfo: 在线信息
//   - error: 错误信息
func (h *Hub) GetUserOnlineInfo(userID string) (*OnlineClientInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetOnlineInfo(ctx, userID)
}

// GetAllOnlineUserIDs 获取所有在线用户ID列表
// 返回:
//   - []string: 用户ID列表
//   - error: 错误信息
func (h *Hub) GetAllOnlineUserIDs() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetAllOnlineUsers(ctx)
}

// GetOnlineUsersByNode 获取指定节点的在线用户
// 参数:
//   - nodeID: 节点ID
//
// 返回:
//   - []string: 用户ID列表
//   - error: 错误信息
func (h *Hub) GetOnlineUsersByNode(nodeID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetOnlineUsersByNode(ctx, nodeID)
}

// GetOnlineUsersByType 根据用户类型获取在线用户
// 参数:
//   - userType: 用户类型
//
// 返回:
//   - []string: 用户ID列表
//   - error: 错误信息
func (h *Hub) GetOnlineUsersByType(userType UserType) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetOnlineUsersByType(ctx, userType)
}

// GetOnlineUserCount 获取在线用户总数
// 返回:
//   - int64: 在线用户数量
//   - error: 错误信息
func (h *Hub) GetOnlineUserCount() (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.onlineStatusRepo.GetOnlineCount(ctx)
}

// UpdateUserHeartbeat 更新用户心跳时间
// 参数:
//   - userID: 用户ID
//
// 返回:
//   - error: 错误信息
func (h *Hub) UpdateUserHeartbeat(userID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.onlineStatusRepo.UpdateHeartbeat(ctx, userID)
}

// SyncOnlineStatusToRedis 同步当前所有在线用户到 Redis
// 用于 Hub 启动时或定期同步
func (h *Hub) SyncOnlineStatusToRedis() error {
	h.mutex.RLock()
	clients := make(map[string]*Client, len(h.clients))
	for id, client := range h.clients {
		clients[id] = client
	}
	h.mutex.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	users := make(map[string]*OnlineClientInfo, len(clients))
	for _, client := range clients {
		users[client.UserID] = &OnlineClientInfo{
			ClientID:      client.ID,
			UserID:        client.UserID,
			UserType:      client.UserType,
			NodeID:        h.nodeID,
			NodeIP:        h.config.NodeIP,
			ClientIP:      client.ClientIP,
			ConnectTime:   time.Now(),
			LastSeen:      client.LastSeen,
			LastHeartbeat: client.LastHeartbeat,
			ClientType:    client.ClientType,
			Status:        client.Status,
			Metadata:      client.Metadata,
		}
	}

	return h.onlineStatusRepo.BatchSetOnline(ctx, users, 0)
}

// recordMessageToDatabase 将消息记录到数据库（内部方法）
func (h *Hub) recordMessageToDatabase(msg *HubMessage, err error) {
	// 异步记录，避免阻塞主流程
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.logger.ErrorKV("消息记录失败",
					"message_id", msg.ID,
					"panic", r,
				)
			}
		}()

		// 确定初始状态
		status := MessageSendStatusPending
		failureReason := FailureReason("")
		errorMsg := ""

		if err != nil {
			status = MessageSendStatusFailed
			errorMsg = err.Error()

			// 根据错误类型设置失败原因
			switch {
			case IsQueueFullError(err):
				failureReason = FailureReasonQueueFull
			case IsUserOfflineError(err):
				failureReason = FailureReasonUserOffline
			case IsSendTimeoutError(err):
				failureReason = FailureReasonSendTimeout
			case IsAckTimeoutError(err):
				failureReason = FailureReasonAckTimeout
			default:
				failureReason = FailureReasonUnknown
			}
		}

		// 获取客户端IP（如果存在）
		clientIP := ""
		if msg.Sender != "" {
			h.mutex.RLock()
			if client, exists := h.userToClient[msg.Sender]; exists {
				clientIP = client.ClientIP
				if clientIP == "" && client.Conn != nil {
					// 如果ClientIP未设置，从连接中获取
					if remoteAddr := client.Conn.RemoteAddr(); remoteAddr != nil {
						clientIP = remoteAddr.String()
					}
				}
			}
			h.mutex.RUnlock()
		}

		// 创建消息记录
		record := &MessageSendRecord{
			Status:        status,
			CreateTime:    time.Now(),
			MaxRetry:      h.config.MaxRetries,
			FailureReason: failureReason,
			ErrorMessage:  errorMsg,
			NodeIP:        h.config.NodeIP, // 记录服务器节点IP
			ClientIP:      clientIP,        // 记录客户端IP
		}

		// 设置消息数据
		if setErr := record.SetMessage(msg); setErr != nil {
			h.logger.ErrorKV("序列化消息失败",
				"message_id", msg.ID,
				"error", setErr,
			)
			return
		}

		// 保存到数据库
		if createErr := h.messageRecordRepo.Create(record); createErr != nil {
			h.logger.ErrorKV("保存消息记录失败",
				"message_id", msg.ID,
				"error", createErr,
			)
		} else {
			h.logger.DebugKV("消息记录已保存",
				"message_id", msg.ID,
				"status", status,
				"record_id", record.ID,
			)
		}
	}()
}

// ============================================================================
// 消息记录查询接口 - 暴露给外部使用
// ============================================================================

// QueryMessageRecord 查询消息记录
// 参数:
//   - messageID: 消息ID
//
// 返回:
//   - *MessageSendRecord: 消息记录
//   - error: 错误信息
func (h *Hub) QueryMessageRecord(messageID string) (*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindByMessageID(messageID)
}

// QueryMessageRecordsBySender 根据发送者查询消息记录
// 参数:
//   - sender: 发送者ID
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsBySender(sender string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindBySender(sender, limit)
}

// QueryMessageRecordsByReceiver 根据接收者查询消息记录
// 参数:
//   - receiver: 接收者ID
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByReceiver(receiver string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindByReceiver(receiver, limit)
}

// QueryMessageRecordsByNodeIP 根据节点IP查询消息记录
// 参数:
//   - nodeIP: 服务器节点IP
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByNodeIP(nodeIP string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindByNodeIP(nodeIP, limit)
}

// QueryMessageRecordsByClientIP 根据客户端IP查询消息记录
// 参数:
//   - clientIP: 客户端IP地址
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByClientIP(clientIP string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindByClientIP(clientIP, limit)
}

// QueryMessageRecordsByStatus 根据状态查询消息记录
// 参数:
//   - status: 消息状态
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByStatus(status MessageSendStatus, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindByStatus(status, limit)
}

// QueryRetryableMessageRecords 查询可重试的消息记录
// 参数:
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryRetryableMessageRecords(limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindRetryable(limit)
}

// QueryExpiredMessageRecords 查询过期的消息记录
// 参数:
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryExpiredMessageRecords(limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindExpired(limit)
}

// ============================================================================
// 消息记录更新接口
// ============================================================================

// UpdateMessageRecordStatus 更新消息记录状态
// 参数:
//   - messageID: 消息ID
//   - status: 新状态
//   - reason: 失败原因（可选）
//   - errorMsg: 错误消息（可选）
func (h *Hub) UpdateMessageRecordStatus(messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.UpdateStatus(messageID, status, reason, errorMsg)
}

// UpdateMessageRecord 更新消息记录
// 参数:
//   - record: 要更新的消息记录
func (h *Hub) UpdateMessageRecord(record *MessageSendRecord) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.Update(record)
}

// ============================================================================
// 消息记录删除接口
// ============================================================================

// DeleteMessageRecord 删除消息记录
// 参数:
//   - id: 记录ID
func (h *Hub) DeleteMessageRecord(id uint) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.Delete(id)
}

// DeleteMessageRecordByMessageID 根据消息ID删除消息记录
// 参数:
//   - messageID: 消息ID
func (h *Hub) DeleteMessageRecordByMessageID(messageID string) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.DeleteByMessageID(messageID)
}

// CleanupOldMessageRecords 清理旧的消息记录
// 参数:
//   - before: 在此时间之前的记录会被清理
//
// 返回:
//   - int64: 被清理的记录数量
//   - error: 错误信息
func (h *Hub) CleanupOldMessageRecords(before time.Time) (int64, error) {
	if h.messageRecordRepo == nil {
		return 0, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.CleanupOld(before)
}

// ============================================================================
// 消息记录统计接口
// ============================================================================

// GetMessageRecordStatistics 获取消息记录统计信息
// 返回:
//   - map[string]int64: 统计数据，包含各种状态的消息数量
//   - error: 错误信息
func (h *Hub) GetMessageRecordStatistics() (map[string]int64, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.GetStatistics()
}
