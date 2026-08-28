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

	"github.com/gorilla/websocket"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/safe"
	"github.com/kamalyes/go-toolbox/pkg/syncx"

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
	HubMessage                  = models.HubMessage
	AckManager                  = protocol.AckManager
	MessageRecordRepository     = repository.MessageRecordRepository
	OnlineStatusRepository      = repository.OnlineStatusRepository
	Client                      = models.Client
	HubStatsRepository          = repository.HubStatsRepository
	WorkloadRepository          = repository.WorkloadRepository
	OfflineMessageHandler       = handler.OfflineMessageHandler
	ConnectionRecordRepository  = repository.ConnectionRecordRepository
	ConnectionRecord            = models.ConnectionRecord
	ConnectionQualityRepository = repository.ConnectionQualityRepository
	ConnectionQuality           = models.ConnectionQuality
	IDGenerator                 = models.IDGenerator
	WSCLogger                   = middleware.WSCLogger
	WelcomeMessageProvider      = models.WelcomeMessageProvider
	RateLimiter                 = middleware.RateLimiter
	DistributedMessage          = models.DistributedMessage
	DisconnectReason            = models.DisconnectReason
	ErrorSeverity               = models.ErrorSeverity
	UserType                    = models.UserType
	ErrorType                   = errorx.ErrorType
	MessageType                 = models.MessageType
	QueueType                   = models.QueueType
	VIPLevel                    = models.VIPLevel
	UserRole                    = models.UserRole
	UserStatus                  = models.UserStatus
	Department                  = models.Department
	Skill                       = models.Skill
	NodeStatus                  = models.NodeStatus
	ClientType                  = models.ClientType
	RetryAttempt                = models.RetryAttempt
	MessageSendStatus           = models.MessageSendStatus
	FailureReason               = models.FailureReason
	MessageSendRecord           = models.MessageSendRecord
	WorkloadInfo                = repository.WorkloadInfo
	MessageClassification       = models.MessageClassification
	GroupRepository             = repository.GroupRepository
	Group                       = repository.Group
	GroupSendResult             = repository.GroupSendResult
	Priority                    = models.Priority
	AckMessage                  = protocol.AckMessage
	AckStatus                   = protocol.AckStatus
	HubStats                    = models.HubStats
	SendResult                  = models.SendResult
	NodeInfo                    = models.NodeInfo
	KickUserResult              = models.KickUserResult
	SendAttempt                 = models.SendAttempt
	BroadcastResult             = models.BroadcastResult
	DeliverResult               = models.DeliverResult
	DeliveryMode                = models.DeliveryMode
	HubHealthInfo               = models.HubHealthInfo
	ConnectionType              = models.ConnectionType
	ObserverManagerStats        = models.ObserverManagerStats
	NamespaceObserverStats      = models.NamespaceObserverStats
	GroupObserverStats          = models.GroupObserverStats
	ObserverStats               = models.ObserverStats
	MessageRecordFilter         = repository.MessageRecordFilter
	OfflineMessageFilter        = repository.OfflineMessageFilter
	MessageRole                 = repository.MessageRole
	WorkloadDimension           = models.WorkloadDimension
)

// DeliveryMode 投递模式常量（Deliver 决策树分派用，由 models 包统一收敛）
const (
	DeliveryModeP2P            = models.DeliveryModeP2P            // 点对点（msg.Receiver 非空）
	DeliveryModeGroupReliable  = models.DeliveryModeGroupReliable  // 群组可靠投递（RequireAck=true）
	DeliveryModeGroupBroadcast = models.DeliveryModeGroupBroadcast // 群组广播（RequireAck=false，fire-and-forget）
	DeliveryModeNamespace      = models.DeliveryModeNamespace      // 命名空间广播
	DeliveryModeGlobal         = models.DeliveryModeGlobal         // 全局广播
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
	FailureReasonUnknown     = models.FailureReasonUnknown
	FailureReasonQueueFull   = models.FailureReasonQueueFull
	FailureReasonConnError   = models.FailureReasonConnError
	FailureReasonUserOffline = models.FailureReasonUserOffline
	FailureReasonAckTimeout  = models.FailureReasonAckTimeout

	// MessageSendStatus 常量
	MessageSendStatusPending      = models.MessageSendStatusPending
	MessageSendStatusSending      = models.MessageSendStatusSending
	MessageSendStatusSuccess      = models.MessageSendStatusSuccess
	MessageSendStatusFailed       = models.MessageSendStatusFailed
	MessageSendStatusUserOffline  = models.MessageSendStatusUserOffline
	MessageSendStatusAckTimeout   = models.MessageSendStatusAckTimeout
	MessageTypeHealthCheck        = models.MessageTypeHealthCheck
	MessageTypeConnectionRejected = models.MessageTypeConnectionRejected

	// AckStatus 常量
	AckStatusFailed    = protocol.AckStatusFailed
	AckStatusConfirmed = protocol.AckStatusConfirmed

	MessageSourceOnline  = models.MessageSourceOnline
	MessageSourceOffline = models.MessageSourceOffline

	BroadcastTypeGlobal  = models.BroadcastTypeGlobal
	BroadcastTypeSession = models.BroadcastTypeSession
	BroadcastTypeNone    = models.BroadcastTypeNone
)

var (
	OperationTypeSendMessage     = models.OperationTypeSendMessage
	OperationTypeKickUser        = models.OperationTypeKickUser
	OperationTypeBroadcast       = models.OperationTypeBroadcast
	OperationTypeObserverNotify  = models.OperationTypeObserverNotify
	OperationTypeGroupBroadcast  = models.OperationTypeGroupBroadcast  // 单群组广播（跨节点 PubSub 兜底必需）
	OperationTypeGroupsBroadcast = models.OperationTypeGroupsBroadcast // 批量群组广播
	OperationTypeUserNotFound    = models.OperationTypeUserNotFound    // 目标节点回告：用户不在该节点（索引死条目自愈）
	OperationTypeClientReclaim   = models.OperationTypeClientReclaim   // 新节点回收旧节点同 clientID 幽灵连接
	MapDeviceTypeToClientType    = models.MapDeviceTypeToClientType
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

// 心跳批量续期并行参数（processHeartbeatRedisUpdates 的 flush 使用）
const (
	// heartbeatRenewChunkSize 单个并行块的客户端数，块内按 maxBatchSize 分批走 Lua 续期
	heartbeatRenewChunkSize = 512
	// heartbeatRenewWorkers 并行续期的最大并发块数（对 Redis 的并发 Eval 上限）
	heartbeatRenewWorkers = 8
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

	// 群组相关错误
	ErrGroupNotFound      = models.ErrGroupNotFound
	ErrGroupMemberExisted = models.ErrGroupMemberExisted
	ErrGroupFull          = models.ErrGroupFull
	ErrGroupRepoNotSet    = models.ErrGroupRepoNotSet
	ErrGroupExisted       = models.ErrGroupExisted

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
	// HeartbeatReportCallback 心跳上报回调
	HeartbeatReportCallback func(client *Client)
	// BeforeHeartbeatCallback 心跳处理前回调，返回 false 则跳过后续心跳处理
	BeforeHeartbeatCallback func(client *Client) bool
	// AfterHeartbeatCallback 心跳处理后回调
	AfterHeartbeatCallback func(client *Client)
	// ClientConnectCallback 客户端连接回调
	// record 为已构造的连接记录（内存对象，已异步落库），调用方可获取 connect 身份+会话生命周期做额外落盘
	ClientConnectCallback func(ctx context.Context, client *Client, record *ConnectionRecord) error
	// ClientDisconnectCallback 客户端断开回调
	ClientDisconnectCallback func(ctx context.Context, client *Client, reason DisconnectReason) error
	// MessageReceivedCallback 消息接收回调
	MessageReceivedCallback func(ctx context.Context, client *Client, msg *HubMessage) error
	// ErrorCallback 错误处理回调
	ErrorCallback func(ctx context.Context, err error, severity ErrorSeverity) error
	// BatchSendFailureCallback 批量发送失败回调
	BatchSendFailureCallback func(userID string, msg *HubMessage, err error)
	// GroupDisbandCallback 群组解散回调
	GroupDisbandCallback func(ctx context.Context, namespace, groupID string)
	// GroupMemberJoinCallback 群组成员加入回调
	// 在客户端连接时自动加群成功后触发（register 自动装配 + 系统组自动加入），手动 AddGroupMembers 不触发
	GroupMemberJoinCallback func(ctx context.Context, namespace, groupID string, userIDs []string)
	// GroupMemberLeaveCallback 群组成员离开回调
	GroupMemberLeaveCallback func(ctx context.Context, namespace, groupID string, userIDs []string)
)

// ============================================================================
// Hub 核心结构
// ============================================================================

// Hub WebSocket/SSE 连接管理中心
type Hub struct {
	nodeID    string
	nodeInfo  *NodeInfo
	startTime time.Time

	// upgrader 复用（避免每次连接升级时分配新对象）
	upgrader     *websocket.Upgrader
	upgraderOnce sync.Once

	nodeMessage chan *DistributedMessage

	ackManager             *AckManager
	messageRecordRepo      MessageRecordRepository
	onlineStatusRepo       OnlineStatusRepository
	statsRepo              HubStatsRepository
	workloadRepo           WorkloadRepository
	offlineMessageHandler  OfflineMessageHandler
	connectionRecordRepo   ConnectionRecordRepository
	connectionQualityRepo  ConnectionQualityRepository
	groupRepo              GroupRepository
	connectionTokenDecoder ConnectionTokenDecoder // 连接 Token 解码器（可选启用，nil 时走明文参数）
	idGenerator            IDGenerator
	temporalHasher         *safe.TemporalHasher
	workerID               int64

	// user_not_found 重路由守卫：messageID → *rerouteGuardEntry
	// 记录每条消息已被哪些节点拒绝，重路由时排除已拒绝节点，防止索引抖动引发 ping-pong 循环
	// 条目懒过期（rerouteGuardTTL），ACK 超时终态时删除（见 ack_timer.go）
	rerouteGuard sync.Map

	// 📡 事件发布订阅
	pubsub *cachex.PubSub

	// 🔗 节点间 gRPC 通信（主从直连，优先于 Redis PubSub 用于点对点路由）
	// nodeRegistry 基于 Redis 维护节点→gRPC 地址映射，支持节点发现与心跳
	// grpcServer 接收远端节点请求，grpcClientPool 复用到各节点的连接
	nodeRegistry   *NodeRegistry
	grpcServer     *GRPCServer
	grpcClientPool *GRPCClientPool

	// 🚀 性能优化组件（v2 新增）
	// workerPool 按任务类型分池控制并发，防止 goroutine 泛滥
	workerPool *HubWorkerPool
	// routerCache 用户→节点路由缓存（KVCache 三层兜底），加速分布式路由判断
	routerCache *RouterCache
	// shardedRegistry 分片注册表（64 shard），降低高并发锁竞争
	// 替代 clients/userToClients 的单 mutex 访问
	shardedRegistry *ShardedRegistry
	// statusUpdater 消息状态批量更新器
	// 收集消息状态更新请求，按 batch flush 到 DB，减少广播场景下的 DB 压力
	statusUpdater *MessageStatusUpdater

	// ⏰ 跨节点 ACK 超时时间轮（主路径，替代原 timeoutStaleSendingRecords 的 30s 全量 DB 扫描）
	// 每条跨节点消息调度一个 ACK 超时任务，收到 ACK 时 O(1) 取消，超时回调标记 AckTimeout + 转存离线
	// timeoutStaleSendingRecords 已降级为 5min 兜底安全网（节点崩溃导致 in-memory timer 丢失时接管）
	// 详见 ack_timer.go（主路径）与 node_ack_timeout.go（兜底）
	ackTimeoutTimer *syncx.HashedWheelTimer

	offlineMessagePushCallback OfflineMessagePushCallback
	messageSendCallback        MessageSendCallback
	queueFullCallback          QueueFullCallback
	heartbeatTimeoutCallback   HeartbeatTimeoutCallback
	heartbeatReportCallback    HeartbeatReportCallback
	beforeHeartbeatCallback    BeforeHeartbeatCallback
	afterHeartbeatCallback     AfterHeartbeatCallback
	clientConnectCallback      ClientConnectCallback
	clientDisconnectCallback   ClientDisconnectCallback
	messageReceivedCallback    MessageReceivedCallback
	errorCallback              ErrorCallback
	batchSendFailureCallback   BatchSendFailureCallback
	groupDisbandCallback       GroupDisbandCallback
	groupMemberJoinCallback    GroupMemberJoinCallback
	groupMemberLeaveCallback   GroupMemberLeaveCallback

	wg       sync.WaitGroup
	shutdown atomic.Bool
	started  atomic.Bool
	startCh  chan struct{}

	// 活跃连接数同步防抖
	syncActiveConnTimer   *time.Timer
	syncActiveConnMutex   sync.Mutex
	syncActiveConnPending atomic.Bool

	// 心跳统计批量更新器（基于 syncx.BatchProcessor，单事务批量 UPDATE）
	heartbeatBatcher *HeartbeatStatsUpdater

	// ⏰ 分片时间轮（心跳超时管理，替代 O(N) 全量扫描）
	// WebSocket 客户端注册时调度超时任务，收到 PING 时 Refresh，
	// 超时未刷新则触发注销。SSE 客户端由 checkHeartbeat 扫描兜底。
	heartbeatTimer *syncx.HashedWheelTimer

	// 消息统计批量更新器（替代每消息 syncx.Go() goroutine）
	messageStatsBatcher *MessageStatsBatcher

	// 观察者通知批量处理器（替代每消息 syncx.Go() 观察者投递 + 跨节点广播）
	observerBatcher *ObserverNotificationBatcher

	// 跨节点分发批量处理器（替代每消息 go func() { routeToCluster(...) }()）
	clusterBatcher *ClusterDispatchBatcher

	// 心跳 Redis 更新通道（替代每次心跳创建 goroutine）
	// 携带 *Client 而非 clientID，使 worker 在 Redis 中 client:<id> 键缺失/过期时
	// 仍能基于内存客户端重建在线索引与跨节点路由信息
	heartbeatRedisCh chan *Client

	// 消息统计原子计数器（替代每次消息创建 goroutine 更新 Redis）
	msgSentCount       atomic.Int64
	broadcastSentCount atomic.Int64
	// 跨 Pod 广播兜底触发次数（routeToClusterForOfflineUser 触发时 +1）
	// reportPerformanceMetrics 每 5min 上报后清零，用于监控"索引滞后"是否消除（治本后应趋近 0）
	broadcastFallbackCount atomic.Int64

	welcomeProvider WelcomeMessageProvider
	logger          WSCLogger
	ctx             context.Context
	cancel          context.CancelFunc
	config          *wscconfig.WSC
	msgPool         sync.Pool
	chanPools       map[int]*sync.Pool // 多级 channel 对象池，key 为容量
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
	config.TemporalHasher = mathx.IfEmpty(config.TemporalHasher, wscconfig.DefaultTemporalHasher())
	config.CapacityEstimation = mathx.IfEmpty(config.CapacityEstimation, wscconfig.DefaultCapacityEstimation())

	// 初始化时间窗口哈希生成器（用于生成 ClientID）
	thConfig := config.TemporalHasher
	temporalHasher := safe.NewTemporalHasher(
		safe.WithWindow(time.Duration(thConfig.GetWindowMinutes())*time.Minute),
		safe.WithLength(thConfig.GetHashLength()),
		safe.WithSeparator(thConfig.GetSeparator()),
	)

	// 预估初始容量，减少 map 扩容
	// CalculateCapacities 返回：(clients, userToClients, agentClients, observerClients, sseClients)
	// 主存储 userShards 用 clients（连接数）作为总容量提示
	// 各分类索引用对应类型的预估连接数
	//
	// 预分配容量联动节点最大连接数（动态扩容策略）：
	//   - CapacityEstimation.Clients 显式配置 > 0 时，按配置预分配
	//   - 未配置(<=0) 时按 Performance.MaxConnectionsPerNode 自动计算，与硬限制对齐
	//   - 两者均未配置时兜底 3000，避免预分配为 0 导致频繁扩容
	//   - 实际连接数达到 MaxConnectionsPerNode 前 map 会按需扩容，不受预分配值约束
	maxConnsPerNode := 0
	if config.Performance != nil {
		maxConnsPerNode = config.Performance.MaxConnectionsPerNode
	}
	config.CapacityEstimation.Clients = mathx.IfLeZero(config.CapacityEstimation.Clients, mathx.IfLeZero(maxConnsPerNode, 3000))
	clientsCap, _, agentClientsCap, observerClientsCap, sseClientsCap := config.CapacityEstimation.CalculateCapacities()
	registryCapacity := RegistryCapacity{
		TotalClients:    clientsCap,
		SSEClients:      sseClientsCap,
		ObserverClients: observerClientsCap,
		AgentClients:    agentClientsCap,
	}

	hub := &Hub{
		nodeID:         nodeID,
		workerID:       workerID,
		idGenerator:    idGenerator,
		temporalHasher: temporalHasher,
		startTime:      time.Now(),
		nodeInfo: &NodeInfo{
			ID:        nodeID,
			IPAddress: config.NodeIP,
			Port:      config.NodePort,
			Status:    NodeStatusActive,
			LastSeen:  time.Now(),
		},
		nodeMessage: make(chan *DistributedMessage, config.MessageBufferSize*4),
		ackManager:  NewAckManager(config.AckTimeout, config.AckMaxRetries),
		ctx:         ctx,
		cancel:      cancel,
		startCh:     make(chan struct{}),
		// 千万级连接缓冲：并行 flush（heartbeatRenewWorkers×chunk）消费速度决定容量下限，
		// 8192 覆盖一个 flush 周期内的心跳突发；满时非阻塞丢弃（下次心跳补上）
		heartbeatRedisCh: make(chan *Client, 8192),
		config:           config,
		logger:           InitLogger(config),
		msgPool: sync.Pool{
			New: func() any {
				b := make([]byte, 0, 1024)
				return &b
			},
		},
	}

	// 初始化连接 Token 解码器（若启用）
	// 此时 Redis 客户端未知，仅创建 JWT 解码能力；
	// 后续在 InitializeRepositories 中通过 SetConnectionTokenRedis 注入 Redis 客户端
	if config.Security != nil && config.Security.ConnectionToken.IsEnabled() {
		hub.connectionTokenDecoder = NewConnectionTokenDecoder(config.Security.ConnectionToken, nil, hub.logger)
		hub.logger.InfoKV("[Hub] 连接 Token 解码器已启用", "use_redis", config.Security.ConnectionToken.UseRedis)
	}

	// 初始化多级 channel 对象池
	hub.initChannelPools()

	// 🚀 初始化性能优化组件
	// 分片注册表（替代单 mutex 的 clients/userToClients map）
	// 同时内化了 SSE/Observer/Agent 三个分类分片索引（按功能开关条件化初始化）
	// 同时按预估容量预分配每 shard 内部 map，减少扩容次数
	hub.shardedRegistry = NewShardedRegistry(config.EnableAgent, config.EnableObserver, registryCapacity)
	// WorkerPool（按任务类型分池控制并发，防止 goroutine 泛滥）
	hub.workerPool = NewHubWorkerPool(mathx.IfNotZero(config.WorkerPool, wscconfig.DefaultWorkerPoolConfig()), hub.logger)

	// 批量处理器参数（从 config 读取，nil/零值时使用默认值）
	batcherCfg := config.Batcher

	// 消息状态批量更新器（广播 1 万人成功 = 1 次 UPDATE，而非 1 万次）
	msgStatus := batcherCfg.GetMessageStatusParams()
	hub.statusUpdater = NewMessageStatusUpdater(hub, msgStatus.QueueSize, msgStatus.BatchSize, msgStatus.FlushInterval)

	// 心跳统计批量更新器
	hbStats := batcherCfg.GetHeartbeatStatsParams()
	hub.heartbeatBatcher = NewHeartbeatStatsUpdater(hub, hbStats.QueueSize, hbStats.BatchSize, hbStats.FlushInterval)

	// 消息统计批量更新器（广播 939 人 = 939 次 Submit + 1 次事务，而非 939 个 goroutine + 1878 次 DB 调用）
	msgStats := batcherCfg.GetMessageStatsParams()
	hub.messageStatsBatcher = NewMessageStatsBatcher(hub, msgStats.QueueSize, msgStats.BatchSize, msgStats.FlushInterval)

	// 观察者通知批量处理器（替代每条消息 syncx.Go() 观察者投递 + syncx.Go() 跨节点广播）
	obsNotify := batcherCfg.GetObserverNotifyParams()
	hub.observerBatcher = NewObserverNotificationBatcher(hub, obsNotify.QueueSize, obsNotify.BatchSize, obsNotify.FlushInterval)

	// 跨节点分发批量处理器（替代每条广播消息 go func() { routeToCluster(...) }()）
	clusterDisp := batcherCfg.GetClusterDispatchParams()
	hub.clusterBatcher = NewClusterDispatchBatcher(hub, clusterDisp.QueueSize, clusterDisp.BatchSize, clusterDisp.FlushInterval)

	// ⏰ 构造期初始化心跳时间轮（替代 checkHeartbeat 的 O(N) 全量扫描）
	// 必须在 NewHub 完成初始化，确保 Register/Refresh/Cancel 在任何 goroutine 启动前读到非 nil 值，
	// 避免与 Run() 的延迟初始化产生数据竞争。用默认 1ms tick（极致精度）：
	// 短超时场景（测试用 200ms）不会被向上取整，百万连接下 64 分片 rounds-- 开销 <1% CPU，可接受
	hub.heartbeatTimer = syncx.NewHashedWheelTimer()

	return hub
}

// SetConnectionTokenDecoder 注入连接 Token 解码器
// 高级用法：允许业务层自定义 decoder 实现（例如自定义 Redis 客户端或自定义校验逻辑）
func (h *Hub) SetConnectionTokenDecoder(decoder ConnectionTokenDecoder) {
	h.connectionTokenDecoder = decoder
}

// GetConnectionTokenDecoder 获取连接 Token 解码器
func (h *Hub) GetConnectionTokenDecoder() ConnectionTokenDecoder {
	return h.connectionTokenDecoder
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
func (h *Hub) GetGroupRepository() GroupRepository         { return h.groupRepo }
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

	// 🚀 初始化路由缓存（需要 Redis 客户端，从 PubSub 获取）
	// KVCache 三层兜底：本地 map → Redis Hash → BatchLoader 回源
	if pubsub != nil && h.onlineStatusRepo != nil {
		h.routerCache = NewRouterCache(pubsub.GetClient(), h.onlineStatusRepo, mathx.IfNotZero(h.config.RouterCache, wscconfig.DefaultRouterCacheConfig()))
		h.logger.InfoKV("路由缓存已启用", "type", "KVCache三层兜底")
	}

	// 🔗 自动初始化节点间 gRPC 通信（若启用 node-grpc 配置）
	// 节点发现依赖 Redis，因此必须在 PubSub 设置后初始化
	h.InitNodeGRPC()

	h.logger.InfoKV("PubSub已设置", "enabled", true)
}

func (h *Hub) GetPubSub() *cachex.PubSub {
	return h.pubsub
}

// 🚀 性能优化组件 Getter 方法
func (h *Hub) GetWorkerPool() *HubWorkerPool        { return h.workerPool }
func (h *Hub) GetRouterCache() *RouterCache         { return h.routerCache }
func (h *Hub) GetShardedRegistry() *ShardedRegistry { return h.shardedRegistry }

// 🔗 gRPC 节点通信 Getter 方法
func (h *Hub) GetNodeRegistry() *NodeRegistry     { return h.nodeRegistry }
func (h *Hub) GetGRPCServer() *GRPCServer         { return h.grpcServer }
func (h *Hub) GetGRPCClientPool() *GRPCClientPool { return h.grpcClientPool }

// IsGRPCEnabled 是否启用了节点间 gRPC 直连通信
// 启用后 SendToUser/SendToGroup 会优先走 gRPC 直连，降低 Redis PubSub 延迟
func (h *Hub) IsGRPCEnabled() bool {
	return h.nodeRegistry != nil && h.grpcClientPool != nil
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
