/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:15:30
 * @FilePath: \go-wsc\hub\hub.go
 * @Description: Hub 核心结构和类型定义
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/safe"

	"github.com/kamalyes/go-wsc/handler"
	"github.com/kamalyes/go-wsc/middleware"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/protocol"
	"github.com/kamalyes/go-wsc/repository"
)

// ============================================================================
// 类型别名 - 从 models repository middleware 包导入
// ============================================================================

type (
	HubMessage                 = models.HubMessage
	AckManager                 = protocol.AckManager
	MessageRecordRepository    = repository.MessageRecordRepository
	OnlineStatusRepository     = repository.OnlineStatusRepository
	Client                     = models.Client
	HubStatsRepository         = repository.HubStatsRepository
	WorkloadRepository         = repository.WorkloadRepository
	OfflineMessageHandler      = handler.OfflineMessageHandler
	ConnectionRecordRepository = repository.ConnectionRecordRepository
	ConnectionRecord           = models.ConnectionRecord
	IDGenerator                = models.IDGenerator
	WSCLogger                  = middleware.WSCLogger
	WelcomeMessageProvider     = models.WelcomeMessageProvider
	RateLimiter                = middleware.RateLimiter
	DistributedMessage         = models.DistributedMessage
	DisconnectReason           = models.DisconnectReason
	ErrorSeverity              = models.ErrorSeverity
	UserType                   = models.UserType
	ErrorType                  = errorx.ErrorType
	MessageType                = models.MessageType
	QueueType                  = models.QueueType
	VIPLevel                   = models.VIPLevel
	UserRole                   = models.UserRole
	UserStatus                 = models.UserStatus
	Department                 = models.Department
	Skill                      = models.Skill
	NodeStatus                 = models.NodeStatus
	ClientType                 = models.ClientType
	RetryAttempt               = models.RetryAttempt
	MessageSendStatus          = models.MessageSendStatus
	FailureReason              = models.FailureReason
	MessageSendRecord          = models.MessageSendRecord
	WorkloadInfo               = repository.WorkloadInfo
	MessageClassification      = models.MessageClassification
	Priority                   = models.Priority
	AckMessage                 = protocol.AckMessage
	AckStatus                  = protocol.AckStatus
	HubStats                   = models.HubStats
	SendResult                 = models.SendResult
	NodeInfo                   = models.NodeInfo
	KickUserResult             = models.KickUserResult
	SendAttempt                = models.SendAttempt
	BroadcastResult            = models.BroadcastResult
	HubHealthInfo              = models.HubHealthInfo
	ConnectionType             = models.ConnectionType
	ObserverManagerStats       = models.ObserverManagerStats
	ObserverStats              = models.ObserverStats
	MessageRecordFilter        = repository.MessageRecordFilter
	OfflineMessageFilter       = repository.OfflineMessageFilter
	MessageRole                = repository.MessageRole
)

// 函数导入
var (
	NewAckManager      = protocol.NewAckManager
	InitLogger         = middleware.InitLogger
	IsRetryableError   = models.IsRetryableError
	IsQueueFullError   = models.IsQueueFullError
	IsUserOfflineError = models.IsUserOfflineError
	IsSendTimeoutError = models.IsSendTimeoutError
	IsAckTimeoutError  = models.IsAckTimeoutError
	GetAllVIPLevels    = models.GetAllVIPLevels
)

// 常量
const (
	NodeStatusActive      = models.NodeStatusActive
	ErrorSeverityInfo     = models.ErrorSeverityInfo
	ErrorSeverityWarning  = models.ErrorSeverityWarning
	ErrorSeverityError    = models.ErrorSeverityError
	ErrorSeverityCritical = models.ErrorSeverityCritical
	ErrorSeverityFatal    = models.ErrorSeverityFatal

	// ConnectionType 常量
	ConnectionTypeWebSocket = models.ConnectionTypeWebSocket
	ConnectionTypeSSE       = models.ConnectionTypeSSE

	// UserType 常量
	UserTypeVisitor  = models.UserTypeVisitor
	UserTypeCustomer = models.UserTypeCustomer
	UserTypeAgent    = models.UserTypeAgent
	UserTypeAdmin    = models.UserTypeAdmin
	UserTypeBot      = models.UserTypeBot
	UserTypeVIP      = models.UserTypeVIP
	UserTypeSystem   = models.UserTypeSystem
	UserTypeObserver = models.UserTypeObserver

	// MessageType 常量
	MessageTypeWelcome          = models.MessageTypeWelcome
	MessageTypeKickOut          = models.MessageTypeKickOut
	MessageTypeText             = models.MessageTypeText
	MessageTypePong             = models.MessageTypePong
	MessageTypePing             = models.MessageTypePing
	MessageTypeHeartbeat        = models.MessageTypeHeartbeat
	MessageTypeAck              = models.MessageTypeAck
	MessageTypeClientRegistered = models.MessageTypeClientRegistered

	// QueueType 常量
	QueueTypeAllQueues = models.QueueTypeAllQueues

	// FailureReason 常量
	FailureReasonUnknown   = models.FailureReasonUnknown
	FailureReasonQueueFull = models.FailureReasonQueueFull

	// MessageSendStatus 常量
	MessageSendStatusPending = models.MessageSendStatusPending
	MessageSendStatusSending = models.MessageSendStatusSending
	MessageSendStatusSuccess = models.MessageSendStatusSuccess
	MessageSendStatusFailed  = models.MessageSendStatusFailed

	// AckStatus 常量
	AckStatusFailed    = protocol.AckStatusFailed
	AckStatusConfirmed = protocol.AckStatusConfirmed

	MessageSourceOnline  = models.MessageSourceOnline
	MessageSourceOffline = models.MessageSourceOffline

	BroadcastTypeGlobal = models.BroadcastTypeGlobal
)

var (
	OperationTypeSendMessage    = models.OperationTypeSendMessage
	OperationTypeKickUser       = models.OperationTypeKickUser
	OperationTypeBroadcast      = models.OperationTypeBroadcast
	OperationTypeObserverNotify = models.OperationTypeObserverNotify
	MapDeviceTypeToClientType   = models.MapDeviceTypeToClientType
)

// NewHubMessage 创建新的 HubMessage
var (
	NewHubMessage = models.NewHubMessage
)

// NewClient 创建新的 Client
var (
	NewClient = models.NewClient
)

// WsCloseCodeMap WebSocket 关闭代码映射
var (
	WsCloseCodeMap = models.WsCloseCodeMap
)

// 错误常量
var (
	ErrHubShutdownTimeout           = models.ErrHubShutdownTimeout
	ErrHubStartupTimeout            = models.ErrHubStartupTimeout
	ErrRecordRepositoryNotSet       = models.ErrRecordRepositoryNotSet
	ErrOnlineStatusRepositoryNotSet = models.ErrOnlineStatusRepositoryNotSet
	ErrMessageDeliveryTimeout       = models.ErrMessageDeliveryTimeout
	ErrQueueAndPendingFull          = models.ErrQueueAndPendingFull
	ErrPubSubNotSet                 = models.ErrPubSubNotSet
	ErrPubSubPublishFailed          = models.ErrPubSubPublishFailed
	ErrClientNotFound               = models.ErrClientNotFound
	ErrClientDisconnected           = models.ErrClientDisconnected

	// ErrorType 常量
	ErrTypeUserNotFound   = models.ErrTypeUserNotFound
	ErrTypeUserOffline    = models.ErrTypeUserOffline
	ErrTypeClientNotFound = models.ErrTypeClientNotFound
)

// UserStatus 常量
const (
	UserStatusOnline    = models.UserStatusOnline
	UserStatusOffline   = models.UserStatusOffline
	UserStatusBusy      = models.UserStatusBusy
	UserStatusAway      = models.UserStatusAway
	UserStatusInvisible = models.UserStatusInvisible
)

// Priority 常量
const (
	PriorityLow      = models.PriorityLow
	PriorityNormal   = models.PriorityNormal
	PriorityHigh     = models.PriorityHigh
	PriorityCritical = models.PriorityCritical
)

// DisconnectReason 常量
const (
	DisconnectReasonReadError      = models.DisconnectReasonReadError
	DisconnectReasonWriteError     = models.DisconnectReasonWriteError
	DisconnectReasonContextDone    = models.DisconnectReasonContextDone
	DisconnectReasonCloseMessage   = models.DisconnectReasonCloseMessage
	DisconnectReasonHeartbeatFail  = models.DisconnectReasonHeartbeatFail
	DisconnectReasonKickOut        = models.DisconnectReasonKickOut
	DisconnectReasonForceOffline   = models.DisconnectReasonForceOffline
	DisconnectReasonTimeout        = models.DisconnectReasonTimeout
	DisconnectReasonClientRequest  = models.DisconnectReasonClientRequest
	DisconnectReasonServerShutdown = models.DisconnectReasonServerShutdown
	DisconnectReasonUnknown        = models.DisconnectReasonUnknown
)

// ============================================================================
// Hub 独有类型定义
// ============================================================================

// PoolManager 连接池管理器接口
type PoolManager interface {
	GetSMTPClient() interface{}
}

// 回调函数类型
type (
	// OfflineMessagePushCallback 离线消息推送回调
	OfflineMessagePushCallback func(userID string, pushedMessageIDs []string, failedMessageIDs []string)
	// MessageSendCallback 消息发送回调
	MessageSendCallback func(msg *HubMessage, result *SendResult)
	// QueueFullCallback 队列满回调
	QueueFullCallback func(msg *HubMessage, recipient string, queueType QueueType, err errorx.BaseError)
	// HeartbeatTimeoutCallback 心跳超时回调
	HeartbeatTimeoutCallback func(clientID string, userID string, lastHeartbeat time.Time)
	// ClientConnectCallback 客户端连接回调
	ClientConnectCallback func(ctx context.Context, client *Client) error
	// ClientDisconnectCallback 客户端断开回调
	ClientDisconnectCallback func(ctx context.Context, client *Client, reason DisconnectReason) error
	// MessageReceivedCallback 消息接收回调
	MessageReceivedCallback func(ctx context.Context, client *Client, msg *HubMessage) error
	// ErrorCallback 错误处理回调
	ErrorCallback func(ctx context.Context, err error, severity ErrorSeverity) error
	// BatchSendFailureCallback 批量发送失败回调
	BatchSendFailureCallback func(userID string, msg *HubMessage, err error)
)

// ============================================================================
// Hub 核心结构
// ============================================================================

// Hub WebSocket/SSE 连接管理中心
type Hub struct {
	nodeID    string
	nodeInfo  *NodeInfo
	nodes     map[string]*NodeInfo
	startTime time.Time

	clients       map[string]*Client
	userToClients map[string]map[string]*Client
	agentClients  map[string]map[string]*Client

	// 观察者专用映射 - 支持多端登录 - O(1) 访问
	observerClients map[string]map[string]*Client

	// SSE 连接（使用统一的 Client 结构）- 支持多设备
	sseClients map[string]map[string]*Client

	// 原子计数器：用于快速获取连接数，避免加锁
	activeClientsCount atomic.Int64
	sseClientsCount    atomic.Int64

	register        chan *Client
	unregister      chan *Client
	broadcast       chan *HubMessage
	nodeMessage     chan *DistributedMessage
	pendingMessages chan *HubMessage

	ackManager            *AckManager
	messageRecordRepo     MessageRecordRepository
	onlineStatusRepo      OnlineStatusRepository
	statsRepo             HubStatsRepository
	workloadRepo          WorkloadRepository
	offlineMessageHandler OfflineMessageHandler
	connectionRecordRepo  ConnectionRecordRepository
	idGenerator           IDGenerator
	workerID              int64

	// 📡 事件发布订阅
	pubsub *cachex.PubSub

	offlineMessagePushCallback OfflineMessagePushCallback
	messageSendCallback        MessageSendCallback
	queueFullCallback          QueueFullCallback
	heartbeatTimeoutCallback   HeartbeatTimeoutCallback
	clientConnectCallback      ClientConnectCallback
	clientDisconnectCallback   ClientDisconnectCallback
	messageReceivedCallback    MessageReceivedCallback
	errorCallback              ErrorCallback
	batchSendFailureCallback   BatchSendFailureCallback

	wg       sync.WaitGroup
	shutdown atomic.Bool
	started  atomic.Bool
	startCh  chan struct{}

	// 活跃连接数同步防抖
	syncActiveConnTimer   *time.Timer
	syncActiveConnMutex   sync.Mutex
	syncActiveConnPending atomic.Bool

	welcomeProvider WelcomeMessageProvider
	logger          WSCLogger
	mutex           sync.RWMutex
	sseMutex        sync.RWMutex
	ctx             context.Context
	cancel          context.CancelFunc
	config          *wscconfig.WSC
	msgPool         sync.Pool
	rateLimiter     *RateLimiter
	poolManager     PoolManager
}

// NewHub 创建新的Hub
func NewHub(config *wscconfig.WSC) *Hub {
	ctx, cancel := context.WithCancel(context.Background())

	// 生成节点ID（支持K8s环境），统一使用短哈希格式
	nodeID := safe.ShortHash(generateNodeID(config))

	workerID := osx.GetWorkerIdForSnowflake()
	idGenerator := idgen.NewShortFlakeGenerator(workerID)
	// 设置默认值
	config.MessageBufferSize = mathx.IfEmpty(config.MessageBufferSize, 1024)
	config.ClientAttributes = mathx.IfEmpty(config.ClientAttributes, wscconfig.DefaultClientAttributes())

	hub := &Hub{
		nodeID:      nodeID,
		workerID:    workerID,
		idGenerator: idGenerator,
		startTime:   time.Now(),
		nodeInfo: &NodeInfo{
			ID:        nodeID,
			IPAddress: config.NodeIP,
			Port:      config.NodePort,
			Status:    NodeStatusActive,
			LastSeen:  time.Now(),
		},
		nodes:           make(map[string]*NodeInfo),
		clients:         make(map[string]*Client),
		userToClients:   make(map[string]map[string]*Client),
		agentClients:    make(map[string]map[string]*Client),
		observerClients: make(map[string]map[string]*Client),
		sseClients:      make(map[string]map[string]*Client),
		register:        make(chan *Client, config.MessageBufferSize),
		unregister:      make(chan *Client, config.MessageBufferSize),
		broadcast:       make(chan *HubMessage, config.MessageBufferSize*4),
		nodeMessage:     make(chan *DistributedMessage, config.MessageBufferSize*4),
		pendingMessages: make(chan *HubMessage, config.MaxPendingQueueSize),
		ackManager:      NewAckManager(config.AckTimeout, config.AckMaxRetries),
		ctx:             ctx,
		cancel:          cancel,
		startCh:         make(chan struct{}),
		config:          config,
		logger:          InitLogger(config),
		msgPool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, 0, 1024)
				return &b
			},
		},
	}
	return hub
}

// ============================================================================
// 基础 Getter/Setter 方法
// ============================================================================

func (h *Hub) GetNodeID() string                           { return h.nodeID }
func (h *Hub) GetWorkerID() int64                          { return h.workerID }
func (h *Hub) GetIDGenerator() IDGenerator                 { return h.idGenerator }
func (h *Hub) GetLogger() WSCLogger                        { return h.logger }
func (h *Hub) GetContext() context.Context                 { return h.ctx }
func (h *Hub) IsStarted() bool                             { return h.started.Load() }
func (h *Hub) IsShutdown() bool                            { return h.shutdown.Load() }
func (h *Hub) GetConfig() *wscconfig.WSC                   { return h.config }
func (h *Hub) GetOnlineStatusRepo() OnlineStatusRepository { return h.onlineStatusRepo }
func (h *Hub) Context() context.Context                    { return h.ctx }

func (h *Hub) SetIDGenerator(generator IDGenerator) {
	h.idGenerator = generator
	h.logger.InfoKV("ID生成器已设置", "generator_type", "idgen")
}

func (h *Hub) SetWelcomeProvider(provider WelcomeMessageProvider) {
	h.welcomeProvider = provider
}

func (h *Hub) SetRateLimiter(limiter *RateLimiter) {
	h.rateLimiter = limiter
}

func (h *Hub) SetPoolManager(manager PoolManager) {
	h.poolManager = manager
}

func (h *Hub) SetPubSub(pubsub *cachex.PubSub) {
	h.pubsub = pubsub
	h.logger.InfoKV("PubSub已设置", "enabled", true)
}

func (h *Hub) GetPubSub() *cachex.PubSub {
	return h.pubsub
}

// ============================================================================
// K8s 兼容的节点ID生成
// ============================================================================

// generateNodeID 生成节点ID（支持K8s环境）
// 优先级：
// 1. 环境变量 POD_NAME（K8s推荐）
// 2. 环境变量 HOSTNAME（容器环境）
// 3. 环境变量 NODE_ID（自定义）
// 4. IP:Port（传统方式）
func generateNodeID(config *wscconfig.WSC) string {
	// 1. 优先使用 K8s Pod Name
	if podName := osx.Getenv("POD_NAME", ""); podName != "" {
		return podName
	}

	// 2. 使用 Hostname（容器环境）
	if hostname := osx.Getenv("HOSTNAME", ""); hostname != "" {
		return hostname
	}

	// 3. 使用自定义 NODE_ID
	if nodeID := osx.Getenv("NODE_ID", ""); nodeID != "" {
		return nodeID
	}

	// 4. 回退到 IP:Port（传统方式）
	return fmt.Sprintf("%s-%d", config.NodeIP, config.NodePort)
}
