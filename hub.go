/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-13 23:00:00
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
	"time"

	"github.com/gorilla/websocket"
)

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
	Type         MessageType            `json:"type"`                      // 消息类型
	From         string                 `json:"from"`                      // 发送者ID (从上下文获取)
	To           string                 `json:"to"`                        // 接收者ID
	TicketID     string                 `json:"ticket_id,omitempty"`       // 工单ID
	Content      string                 `json:"content"`                   // 消息内容
	Data         map[string]interface{} `json:"data,omitempty"`            // 扩展数据
	CreateAt     time.Time              `json:"create_at"`                 // 创建时间
	MsgID        string                 `json:"msg_id"`                    // 消息ID
	SeqNo        int64                  `json:"seq_no"`                    // 消息序列号
	Priority     Priority               `json:"priority"`                  // 优先级
	ReplyToMsgID string                 `json:"reply_to_msg_id,omitempty"` // 回复的消息ID
	Status       MessageStatus          `json:"status"`                    // 消息状态
	RequireAck   bool                   `json:"require_ack,omitempty"`     // 是否需要ACK确认
}

// Client 客户端连接（服务端视角）
type Client struct {
	ID         string                 // 客户端ID
	UserID     string                 // 用户ID
	UserType   UserType               // 用户类型
	TicketID   string                 // 工单ID
	Role       UserRole               // 角色
	Conn       *websocket.Conn        // WebSocket连接
	LastSeen   time.Time              // 最后活跃时间
	Status     UserStatus             // 状态
	Department Department             // 部门
	Skills     []Skill                // 技能
	MaxTickets int                    // 最大工单数
	NodeID     string                 // 节点ID
	ClientType ClientType             // 客户端类型
	Metadata   map[string]interface{} // 元数据
	SendChan   chan []byte            // 发送通道
	Context    context.Context        // 上下文（存储发送者ID等信息）
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
	clients       map[string]*Client   // 所有客户端 key: clientID
	userToClient  map[string]*Client   // 用户ID到客户端
	agentClients  map[string]*Client   // 客服连接
	ticketClients map[string][]*Client // 工单相关客户端

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
	recordManager *MessageRecordManager

	// 并发控制
	wg       sync.WaitGroup
	shutdown atomic.Bool

	// 欢迎消息提供者
	welcomeProvider WelcomeMessageProvider

	// 统计信息（使用atomic实现无锁统计，提升高并发性能）
	totalConnections  atomic.Int64 // 累计总连接数
	activeConnections atomic.Int64 // 当前活跃连接数
	messagesSent      atomic.Int64 // 已发送消息数
	messagesReceived  atomic.Int64 // 已接收消息数

	// 并发控制
	mutex    sync.RWMutex
	sseMutex sync.RWMutex

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 配置
	config *HubConfig
}

// HubConfig Hub配置
type HubConfig struct {
	NodeIP              string                 // 节点IP
	NodePort            int                    // 节点端口
	HeartbeatInterval   time.Duration          // 心跳间隔
	ClientTimeout       time.Duration          // 客户端超时
	MessageBufferSize   int                    // 消息缓冲区大小
	PendingQueueSize    int                    // 待发送消息队列大小
	EnableAck           bool                   // 启用ACK确认
	AckTimeout          time.Duration          // ACK超时时间
	MaxRetry            int                    // 最大重试次数
	EnableMessageRecord bool                   // 启用消息记录
	MaxRecords          int                    // 最大记录数
	RecordRetention     time.Duration          // 记录保留时间
	SSEHeartbeat        time.Duration          // SSE心跳间隔
	SSETimeout          time.Duration          // SSE超时
	SSEMessageBuffer    int                    // SSE消息缓冲
	WelcomeProvider     WelcomeMessageProvider // 欢迎消息提供者
}

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

// DefaultHubConfig 默认Hub配置
func DefaultHubConfig() *HubConfig {
	return &HubConfig{
		NodeIP:            "0.0.0.0",
		NodePort:          8080,
		HeartbeatInterval: 30 * time.Second,
		ClientTimeout:     90 * time.Second,
		MessageBufferSize: 256,
		PendingQueueSize:  1024, // 待发送队列，队列满时缓存消息
		EnableAck:         false, // 默认关闭ACK，需要时开启
		AckTimeout:        5 * time.Second,
		MaxRetry:          3,
		EnableMessageRecord: true,  // 默认启用消息记录
		MaxRecords:        10000,   // 默认最多1万条记录
		RecordRetention:   24 * time.Hour, // 默认保疙24小时
		SSEHeartbeat:      30 * time.Second,
		SSETimeout:        2 * time.Minute,
		SSEMessageBuffer:  100,
	}
}

// NewHub 创建新的Hub
func NewHub(config *HubConfig) *Hub {
	if config == nil {
		config = DefaultHubConfig()
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
		ticketClients:   make(map[string][]*Client),
		sseClients:      make(map[string]*SSEConnection),
		register:        make(chan *Client, config.MessageBufferSize),
		unregister:      make(chan *Client, config.MessageBufferSize),
		broadcast:       make(chan *HubMessage, config.MessageBufferSize*4),
		nodeMessage:     make(chan *DistributedMessage, config.MessageBufferSize*4),
		pendingMessages: make(chan *HubMessage, config.PendingQueueSize),
		maxPendingSize:  config.PendingQueueSize,
		ackManager:      NewAckManager(config.AckTimeout, config.MaxRetry),
		recordManager:   nil, // 将在下面条件创建
		welcomeProvider: config.WelcomeProvider,
		ctx:             ctx,
		cancel:          cancel,
		config:          config,
	}

	// 如果启用消息记录，创建记录管理器
	if config.EnableMessageRecord {
		hub.recordManager = NewMessageRecordManager(config.MaxRecords, config.RecordRetention, nil)
	}

	return hub
}

// Run 启动Hub
func (h *Hub) Run() {
	h.wg.Add(1)
	defer h.wg.Done()
	
	ticker := time.NewTicker(h.config.HeartbeatInterval)
	defer ticker.Stop()

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
		}
	}
}

// Register 注册客户端
func (h *Hub) Register(client *Client) {
	h.register <- client
}

// Unregister 注销客户端
func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

// SendToUser 发送消息给指定用户（自动填充发送者信息）
func (h *Hub) SendToUser(ctx context.Context, toUserID string, msg *HubMessage) error {
	// 从上下文获取发送者ID
	if msg.From == "" {
		if senderID, ok := ctx.Value(ContextKeySenderID).(string); ok {
			msg.From = senderID
		} else if userID, ok := ctx.Value(ContextKeyUserID).(string); ok {
			msg.From = userID
		}
	}

	msg.To = toUserID
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 尝试发送到broadcast队列
	select {
	case h.broadcast <- msg:
		return nil
	default:
		// broadcast队列满，尝试放入待发送队列
		select {
		case h.pendingMessages <- msg:
			return nil
		default:
			return fmt.Errorf("消息队列已满，待发送队列也已满")
		}
	}
}

// SendToTicket 发送消息到工单
func (h *Hub) SendToTicket(ctx context.Context, ticketID string, msg *HubMessage) error {
	// 从上下文获取发送者ID
	if msg.From == "" {
		if senderID, ok := ctx.Value(ContextKeySenderID).(string); ok {
			msg.From = senderID
		}
	}

	msg.TicketID = ticketID
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 尝试发送到broadcast队列
	select {
	case h.broadcast <- msg:
		return nil
	default:
		// broadcast队列满，尝试放入待发送队列
		select {
		case h.pendingMessages <- msg:
			return nil
		default:
			return fmt.Errorf("消息队列已满，待发送队列也已满")
		}
	}
}

// Broadcast 广播消息
func (h *Hub) Broadcast(ctx context.Context, msg *HubMessage) {
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	select {
	case h.broadcast <- msg:
	default:
		// broadcast队列满，尝试放入待发送队列
		select {
		case h.pendingMessages <- msg:
		default:
			// 两个队列都满，静默丢弃（广播消息不返回错误）
		}
	}
}

// processPendingMessages 处理待发送消息队列
func (h *Hub) processPendingMessages() {
	h.wg.Add(1)
	defer h.wg.Done()
	
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case msg := <-h.pendingMessages:
			// 尝试将消息放入broadcast队列
			select {
			case h.broadcast <- msg:
				// 成功发送
			case <-time.After(5 * time.Second):
				// 超时，丢弃消息
			}
		case <-ticker.C:
			// 定期检查，避免goroutine阻塞
		}
	}
}

// RegisterSSE 注册SSE连接
func (h *Hub) RegisterSSE(conn *SSEConnection) {
	h.sseMutex.Lock()
	defer h.sseMutex.Unlock()
	h.sseClients[conn.UserID] = conn
}

// UnregisterSSE 注销SSE连接
func (h *Hub) UnregisterSSE(userID string) {
	h.sseMutex.Lock()
	defer h.sseMutex.Unlock()
	if conn, exists := h.sseClients[userID]; exists {
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
		return false
	}

	select {
	case conn.MessageCh <- msg:
		conn.LastActive = time.Now()
		return true
	default:
		return false
	}
}

// SendToUserWithAck 发送消息给指定用户并等待ACK确认
func (h *Hub) SendToUserWithAck(ctx context.Context, toUserID string, msg *HubMessage, timeout time.Duration, maxRetry int) (*AckMessage, error) {
	if !h.config.EnableAck {
		// 如果未启用ACK，直接发送
		return nil, h.SendToUser(ctx, toUserID, msg)
	}

	// 生成消息ID
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%s-%d", toUserID, time.Now().UnixNano())
	}
	msg.RequireAck = true

	// 创建消息发送记录
	var record *MessageSendRecord
	if h.recordManager != nil {
		expiresAt := time.Now().Add(h.ackManager.expireDuration)
		record = h.recordManager.CreateRecord(msg, maxRetry, expiresAt)
	}

	// 检查用户是否在线
	h.mutex.RLock()
	_, isOnline := h.userToClient[toUserID]
	h.mutex.RUnlock()

	if !isOnline {
		// 记录用户离线
		if record != nil {
			h.recordManager.MarkUserOffline(msg.ID)
		}

		// 用户离线，使用离线处理器处理消息
		if h.ackManager.offlineHandler != nil {
			if err := h.ackManager.offlineHandler.HandleOfflineMessage(msg); err != nil {
				if record != nil {
					h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusFailed, FailureReasonUserOffline, err.Error())
				}
				return &AckMessage{
					MessageID: msg.ID,
					Status:    AckStatusFailed,
					Timestamp: time.Now(),
					Error:     fmt.Sprintf("用户离线且离线消息处理失败: %v", err),
				}, err
			}
			if record != nil {
				h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusSuccess, "", "用户离线，消息已存储")
			}
			return &AckMessage{
				MessageID: msg.ID,
				Status:    AckStatusConfirmed,
				Timestamp: time.Now(),
				Error:     "用户离线，消息已存储",
			}, nil
		}

		if record != nil {
			h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusFailed, FailureReasonUserOffline, "用户离线且未配置离线消息处理器")
		}
		return &AckMessage{
			MessageID: msg.ID,
			Status:    AckStatusFailed,
			Timestamp: time.Now(),
			Error:     "用户离线且未配置离线消息处理器",
		}, fmt.Errorf("用户 %s 离线", toUserID)
	}

	// 更新记录状态为发送中
	if record != nil {
		h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusSending, "", "")
	}

	// 添加到待确认队列
	pm := h.ackManager.AddPendingMessage(msg, timeout, maxRetry)
	defer h.ackManager.RemovePendingMessage(msg.ID)

	// 定义重试函数（带记录）
	attemptNum := 0
	retryFunc := func() error {
		attemptNum++
		startTime := time.Now()
		err := h.SendToUser(ctx, toUserID, msg)
		duration := time.Since(startTime)

		// 记录重试尝试
		if record != nil {
			h.recordManager.RecordRetryAttempt(msg.ID, attemptNum, duration, err, err == nil)
		}

		return err
	}

	// 首次发送
	if err := retryFunc(); err != nil {
		if record != nil {
			h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusFailed, FailureReasonSendTimeout, err.Error())
		}
		return &AckMessage{
			MessageID: msg.ID,
			Status:    AckStatusFailed,
			Timestamp: time.Now(),
			Error:     err.Error(),
		}, err
	}

	// 等待ACK确认并支持重试
	ackMsg, err := pm.WaitForAckWithRetry(retryFunc)

	// 更新最终记录状态
	if record != nil {
		if err != nil {
			if ackMsg != nil && ackMsg.Status == AckStatusTimeout {
				h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusAckTimeout, FailureReasonAckTimeout, err.Error())
			} else {
				h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusFailed, FailureReasonUnknown, err.Error())
			}
		} else {
			h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusSuccess, "", "")
		}
	}

	return ackMsg, err
}

// SendToTicketWithAck 发送消息到工单并等待ACK确认
func (h *Hub) SendToTicketWithAck(ctx context.Context, ticketID string, msg *HubMessage, timeout time.Duration, maxRetry int) (*AckMessage, error) {
	if !h.config.EnableAck {
		// 如果未启用ACK，直接发送
		return nil, h.SendToTicket(ctx, ticketID, msg)
	}

	// 生成消息ID
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%s-%d", ticketID, time.Now().UnixNano())
	}
	msg.RequireAck = true

	// 添加到待确认队列
	pm := h.ackManager.AddPendingMessage(msg, timeout, maxRetry)
	defer h.ackManager.RemovePendingMessage(msg.ID)

	// 定义重试函数
	retryFunc := func() error {
		return h.SendToTicket(ctx, ticketID, msg)
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
	return pm.WaitForAckWithRetry(retryFunc)
}

// SetOfflineMessageHandler 设置离线消息处理器
func (h *Hub) SetOfflineMessageHandler(handler OfflineMessageHandler) {
	if h.ackManager != nil {
		h.ackManager.offlineHandler = handler
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
	h.ackManager.ConfirmMessage(ackMsg.MessageID, ackMsg)
}

// GetMessageRecord 获取消息发送记录
func (h *Hub) GetMessageRecord(messageID string) (*MessageSendRecord, bool) {
	if h.recordManager == nil {
		return nil, false
	}
	return h.recordManager.GetRecord(messageID)
}

// GetFailedMessages 获取所有失败的消息
func (h *Hub) GetFailedMessages() []*MessageSendRecord {
	if h.recordManager == nil {
		return nil
	}
	return h.recordManager.GetFailedRecords()
}

// GetRetryableMessages 获取可重试的消息
func (h *Hub) GetRetryableMessages() []*MessageSendRecord {
	if h.recordManager == nil {
		return nil
	}
	return h.recordManager.GetRetryableRecords()
}

// RetryFailedMessage 重试失败的消息
func (h *Hub) RetryFailedMessage(messageID string) error {
	if h.recordManager == nil {
		return fmt.Errorf("消息记录管理器未启用")
	}

	record, exists := h.recordManager.GetRecord(messageID)
	if !exists {
		return fmt.Errorf("消息记录不存在: %s", messageID)
	}

	if record.Status == MessageSendStatusSuccess {
		return fmt.Errorf("消息已成功发送，无需重试")
	}

	if record.RetryCount >= record.MaxRetry {
		return fmt.Errorf("已达到最大重试次数")
	}

	// 重新发送消息
	ctx := context.Background()
	if record.Message.To != "" {
		_, err := h.SendToUserWithAck(ctx, record.Message.To, record.Message, 0, record.MaxRetry-record.RetryCount)
		return err
	} else if record.Message.TicketID != "" {
		_, err := h.SendToTicketWithAck(ctx, record.Message.TicketID, record.Message, 0, record.MaxRetry-record.RetryCount)
		return err
	}

	return fmt.Errorf("消息目标未指定")
}

// RetryAllFailedMessages 批量重试所有失败的消息
func (h *Hub) RetryAllFailedMessages() (int, int) {
	if h.recordManager == nil {
		return 0, 0
	}

	retryable := h.recordManager.GetRetryableRecords()
	success := 0
	failed := 0

	for _, record := range retryable {
		if err := h.RetryFailedMessage(record.MessageID); err != nil {
			failed++
		} else {
			success++
		}
	}

	return success, failed
}

// GetMessageStatistics 获取消息统计信息
func (h *Hub) GetMessageStatistics() map[string]int {
	if h.recordManager == nil {
		return map[string]int{"enabled": 0}
	}
	stats := h.recordManager.GetStatistics()
	stats["enabled"] = 1
	return stats
}

// CleanupExpiredMessages 清理过期消息记录
func (h *Hub) CleanupExpiredMessages() int {
	if h.recordManager == nil {
		return 0
	}
	return h.recordManager.CleanupExpiredRecords()
}

// SetMessageRecordPersistence 设置消息记录持久化接口
func (h *Hub) SetMessageRecordPersistence(persistence MessageRecordPersistence) {
	if h.recordManager != nil {
		h.recordManager.persistence = persistence
	}
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
		"node_id":               h.nodeID,
		"websocket_count":       wsCount,
		"sse_count":             sseCount,
		"total_connections":     wsCount + sseCount,
		"total_lifetime":        h.totalConnections.Load(),
		"active_connections":    h.activeConnections.Load(),
		"messages_sent":         h.messagesSent.Load(),
		"messages_received":     h.messagesReceived.Load(),
	}
}

// GetNodeID 获取节点ID
func (h *Hub) GetNodeID() string {
	return h.nodeID
}

// Shutdown 关闭Hub
func (h *Hub) Shutdown() {
	// 设置 shutdown 标志
	h.shutdown.Store(true)
	
	// 取消 context
	h.cancel()
	
	// 等待所有 goroutine 完成
	h.wg.Wait()

	// 关闭所有客户端连接和 channel
	h.mutex.Lock()
	for _, client := range h.clients {
		if client.Conn != nil {
			client.Conn.Close()
		}
		close(client.SendChan)
	}
	h.mutex.Unlock()

	// 关闭 SSE 连接
	h.sseMutex.Lock()
	for _, conn := range h.sseClients {
		close(conn.CloseCh)
	}
	h.sseMutex.Unlock()
}

// === 内部方法 ===

func (h *Hub) handleRegister(client *Client) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	// 关闭旧连接
	if existingClient, exists := h.userToClient[client.UserID]; exists {
		if existingClient.Conn != nil {
			existingClient.Conn.Close()
		}
		h.removeClientUnsafe(existingClient)
	}

	// 添加新客户端
	h.clients[client.ID] = client
	h.userToClient[client.UserID] = client

	if client.UserType == UserTypeAgent || client.UserType == UserTypeBot {
		h.agentClients[client.UserID] = client
	}

	if client.TicketID != "" {
		h.ticketClients[client.TicketID] = append(h.ticketClients[client.TicketID], client)
	}

	// 使用atomic无锁更新统计信息
	h.totalConnections.Add(1)
	h.activeConnections.Store(int64(len(h.clients)))

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

	delete(h.clients, client.ID)
	delete(h.userToClient, client.UserID)

	if client.UserType == UserTypeAgent || client.UserType == UserTypeBot {
		delete(h.agentClients, client.UserID)
	}

	if client.TicketID != "" {
		if clients, exists := h.ticketClients[client.TicketID]; exists {
			for i, c := range clients {
				if c.ID == client.ID {
					h.ticketClients[client.TicketID] = append(clients[:i], clients[i+1:]...)
					break
				}
			}
		}
	}

	h.activeConnections.Store(int64(len(h.clients)))

	if client.SendChan != nil {
		defer func() { recover() }()
		close(client.SendChan)
	}
}

func (h *Hub) handleBroadcast(msg *HubMessage) {
	switch {
	case msg.To != "": // 点对点消息 - 最快路径
		h.mutex.RLock()
		client := h.userToClient[msg.To]
		h.mutex.RUnlock()
		
		if client != nil {
			h.sendToClient(client, msg)
		} else {
			h.SendToUserViaSSE(msg.To, msg)
		}
		
	case msg.TicketID != "": // 工单消息
		h.mutex.RLock()
		ticketClients := h.ticketClients[msg.TicketID]
		h.mutex.RUnlock()
		
		for _, client := range ticketClients {
			if client.UserID != msg.From {
				h.sendToClient(client, msg)
			}
		}
		
	default: // 广播消息
		h.mutex.RLock()
		for _, client := range h.clients {
			h.sendToClient(client, msg)
		}
		h.mutex.RUnlock()
		
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

func (h *Hub) sendToClient(client *Client, msg *HubMessage) {
	// 检查Hub是否已关闭
	if h.shutdown.Load() {
		return
	}

	// 直接序列化，避免buffer池的额外开销
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	// 再次检查，避免在序列化过程中 shutdown
	if h.shutdown.Load() {
		return
	}

	select {
	case client.SendChan <- data:
		h.messagesSent.Add(1)
	case <-h.ctx.Done():
		return
	default:
		// 队列满，跳过该消息
	}
}

func (h *Hub) handleClientWrite(client *Client) {
	h.wg.Add(1)
	defer h.wg.Done()
	defer func() {
		if client.Conn != nil {
			client.Conn.Close()
		}
		h.Unregister(client)
	}()

	for {
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				return
			}

			if client.Conn != nil {
				client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
					return
				}
			}
		case <-h.ctx.Done():
			return
		}
	}
}

func (h *Hub) checkHeartbeat() {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	now := time.Now()
	for _, client := range h.clients {
		if now.Sub(client.LastSeen) > h.config.ClientTimeout {
			if client.Conn != nil {
				client.Conn.Close()
			}
			h.removeClientUnsafe(client)
		}
	}

	// 检查SSE超时
	h.sseMutex.Lock()
	for userID, conn := range h.sseClients {
		if now.Sub(conn.LastActive) > h.config.SSETimeout {
			close(conn.CloseCh)
			delete(h.sseClients, userID)
		}
	}
	h.sseMutex.Unlock()
}

func (h *Hub) sendWelcomeMessage(client *Client) {
	if h.welcomeProvider == nil {
		return
	}

	extraData := map[string]interface{}{
		"client_id": client.ID,
		"node_id":   h.nodeID,
		"time":      time.Now().Format("2006-01-02 15:04:05"),
	}

	welcomeMsg, enabled, err := h.welcomeProvider.GetWelcomeMessage(
		client.UserID,
		client.Role,
		client.UserType,
		client.TicketID,
		extraData,
	)

	if err != nil || !enabled || welcomeMsg == nil {
		return
	}

	msg := &HubMessage{
		Type:     welcomeMsg.MessageType,
		From:     "system",
		To:       client.UserID,
		TicketID: client.TicketID,
		Content:  welcomeMsg.Content,
		Data:     welcomeMsg.Data,
		CreateAt: time.Now(),
		Priority: welcomeMsg.Priority,
		Status:   MessageStatusSent,
	}

	if msg.Data == nil {
		msg.Data = make(map[string]interface{})
	}
	msg.Data["title"] = welcomeMsg.Title

	h.sendToClient(client, msg)
}
