/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:15:30
 * @FilePath: \go-wsc\hub\hub.go
 * @Description: Slim orchestration Hub —— 组装域管理器与过载/集群/连接组件的编排层
 *
 * 从旧 god-object Hub 拆出：域逻辑已下沉到 messaging/stats/group/overload/
 * cluster/connection 等包，本结构仅持有组件并实现各域 Host 端口做委托。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
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
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/safe"
	"github.com/kamalyes/go-toolbox/pkg/syncx"

	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/group"
	"github.com/kamalyes/go-wsc/messaging"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/kamalyes/go-wsc/stats"
)

// ============================================================================
// Hub 核心结构 —— slim 编排层
// ============================================================================
//
// 应用层回调类型与集合见 callbacks.go（HubCallbacks 单字段收敛）。

// Hub WebSocket/SSE 连接管理中心（编排层）
//
// 持有域管理器（messaging/stats/group）与运行时组件（overload/cluster/connection），
// 实现各域 Host 端口做委托。域逻辑不在本结构内，仅做组装与转发。
type Hub struct {
	// ========== 基础环境 ==========
	nodeID    string
	nodeInfo  *models.NodeInfo
	startTime time.Time

	config *wscconfig.WSC
	logger spi.Logger
	ctx    context.Context
	cancel context.CancelFunc

	workerID       int64
	idGenerator    models.IDGenerator
	temporalHasher *safe.TemporalHasher

	// ========== 域管理器 ==========
	messagingMgr *messaging.Manager
	statsMgr     *stats.Manager
	groupMgr     *group.Manager

	// ========== 连接域 ==========
	shardedRegistry *connection.ShardedRegistry
	// heartbeatMgr 心跳管理器（时间轮 O(1) 超时 + SSE 兜底扫描，时间轮为域内资产）
	heartbeatMgr *connection.HeartbeatManager
	// lifecycleMgr 连接生命周期管理器（多端登录治理 / 踢出断链 / 精简移除）
	lifecycleMgr *connection.LifecycleManager
	// recordMgr 连接记录管理器（记录构造 / 异步落库 / 停机批量终态）
	recordMgr *connection.RecordManager

	// ========== 集群域 ==========
	nodeRegistry   *cluster.NodeRegistry
	grpcClientPool *cluster.GRPCClientPool
	routerCache    *cluster.RouterCache
	// nodeQueryFlight 用户节点查询 in-flight 合并器（热点用户并发扇入共享单次回源）
	nodeQueryFlight nodeQueryFlight
	// rerouteGuard user_not_found 重路由守卫：messageID → 已拒绝节点集合
	rerouteGuard sync.Map

	// ========== 过载保护域（atomic.Pointer 支持运行期热替换） ==========
	admission           atomic.Pointer[overload.AdmissionGate]
	broadcastShaper     atomic.Pointer[overload.Shaper]
	ephemeralCoalescer  atomic.Pointer[overload.Coalescer]
	broadcastDelayQueue *overload.BroadcastDelayQueue
	overloadMetrics     overload.OverloadMetrics

	// ========== 批处理器域（攒批落库/通知抑制写放大，五组件归一管理） ==========
	batcherMgr *batcher.Manager

	// ========== 定时器（分片时间轮，O(1) 超时管理） ==========
	// 心跳超时时间轮由连接域心跳管理器持有（见 heartbeatMgr）
	ackTimeoutTimer *syncx.HashedWheelTimer

	// ========== 工作池 ==========
	workerPool *messaging.HubWorkerPool

	// ========== SPI 仓储（未注入即 nil，域内自行判空降级） ==========
	messageSink            spi.MessageSink
	groupStore             spi.GroupStore
	statsRepo              spi.HubStats
	onlineStatusRepo       spi.OnlineStore
	connectionStore        spi.ConnectionStore
	connectionQualityStore spi.ConnectionQualityStore
	workloadStore          spi.WorkloadStore

	// ========== 连接 Token 鉴权器（可选启用，nil 时走明文参数） ==========
	connectionTokenDecoder spi.ConnectionAuthenticator

	// ========== 应用层回调（连接/心跳/离线推送/群组生命周期，见 callbacks.go） ==========
	callbacks HubCallbacks

	// ========== 生命周期 ==========
	wg       sync.WaitGroup
	shutdown atomic.Bool
	started  atomic.Bool
	startCh  chan struct{}

	// ========== 基础设施 ==========
	pubsub  *cachex.PubSub
	msgPool sync.Pool
	// nodeMessage 跨节点消息通道
	nodeMessage chan *models.DistributedMessage
	// heartbeatRedisCh 心跳 Redis 更新通道（单 goroutine 消费，替代每心跳一 goroutine）
	heartbeatRedisCh chan *models.Client
	welcomeProvider  models.WelcomeMessageProvider

	// 消息统计原子计数器（编排层定时刷写到 statsRepo）
	msgSentCount           atomic.Int64
	broadcastSentCount     atomic.Int64
	broadcastFallbackCount atomic.Int64

	// 活跃连接数同步防抖
	syncActiveConnTimer   *time.Timer
	syncActiveConnMutex   sync.Mutex
	syncActiveConnPending atomic.Bool

	// upgrader 复用（避免每次连接升级时分配新对象）
	upgrader     *websocket.Upgrader
	upgraderOnce sync.Once
}

// NewHub 创建新的 Hub（编排层组装入口）
//
// 域管理器与批处理器均以 hub 作为 Host 端口构造：Go 允许在构造函数返回前
// 使用指针的方法集，因为方法集在编译期已确定。
func NewHub(config *wscconfig.WSC) *Hub {
	ctx, cancel := context.WithCancel(context.Background())

	// 生成节点 ID（支持 K8s 环境），统一使用短哈希格式
	nodeID := safe.ShortHash(generateNodeID(config))
	workerID := osx.GetWorkerIdForSnowflake()
	idGenerator := idgen.NewShortFlakeGenerator(workerID)

	// 设置默认值
	config.MessageBufferSize = mathx.IfEmpty(config.MessageBufferSize, 1024)
	config.ClientAttributes = mathx.IfEmpty(config.ClientAttributes, wscconfig.DefaultClientAttributes())
	config.TemporalHasher = mathx.IfEmpty(config.TemporalHasher, wscconfig.DefaultTemporalHasher())
	config.CapacityEstimation = mathx.IfEmpty(config.CapacityEstimation, wscconfig.DefaultCapacityEstimation())
	config.Timer = mathx.IfEmpty(config.Timer, wscconfig.DefaultTimerConfig())

	// 初始化时间窗口哈希生成器（用于生成 ClientID）
	thConfig := config.TemporalHasher
	temporalHasher := safe.NewTemporalHasher(
		safe.WithWindow(time.Duration(thConfig.GetWindowMinutes())*time.Minute),
		safe.WithLength(thConfig.GetHashLength()),
		safe.WithSeparator(thConfig.GetSeparator()),
	)

	// 预估初始容量，减少 map 扩容
	maxConnsPerNode := 0
	if config.Performance != nil {
		maxConnsPerNode = config.Performance.MaxConnectionsPerNode
	}
	config.CapacityEstimation.Clients = mathx.IfLeZero(config.CapacityEstimation.Clients, mathx.IfLeZero(maxConnsPerNode, 3000))
	clientsCap, _, agentClientsCap, observerClientsCap, sseClientsCap := config.CapacityEstimation.CalculateCapacities()
	registryCapacity := connection.RegistryCapacity{
		TotalClients:    clientsCap,
		SSEClients:      sseClientsCap,
		ObserverClients: observerClientsCap,
		AgentClients:    agentClientsCap,
	}

	logger := spi.InitLogger(config)

	hub := &Hub{
		nodeID:         nodeID,
		workerID:       workerID,
		idGenerator:    idGenerator,
		temporalHasher: temporalHasher,
		startTime:      time.Now(),
		nodeInfo: &models.NodeInfo{
			ID:        nodeID,
			IPAddress: config.NodeIP,
			Port:      config.NodePort,
			Status:    models.NodeStatusActive,
			LastSeen:  time.Now(),
		},
		nodeMessage: make(chan *models.DistributedMessage, config.MessageBufferSize*4),
		ctx:         ctx,
		cancel:      cancel,
		startCh:     make(chan struct{}),
		// 心跳 Redis 更新通道：8192 覆盖一个 flush 周期内的突发
		heartbeatRedisCh: make(chan *models.Client, 8192),
		config:           config,
		logger:           logger,
		msgPool: sync.Pool{
			New: func() any {
				b := make([]byte, 0, 1024)
				return &b
			},
		},
	}

	// 分片注册表（替代单 mutex 的 clients/userToClients map）
	hub.shardedRegistry = connection.NewShardedRegistry(config.EnableAgent, config.EnableObserver, registryCapacity)

	// ⏰ 心跳管理器（连接域：内存时间戳 + 时间轮 O(1) 超时 + SSE 兜底扫描）
	// 构造期初始化时间轮，确保 Schedule/Refresh/Cancel 在任何 goroutine 启动前可用
	hub.heartbeatMgr = connection.NewHeartbeatManager(hub, hub.shardedRegistry, config.ClientTimeout, config.Timer.GetTimerOptions()...)

	// 连接生命周期管理器（连接域：多端登录治理 + 踢出断链 + shutdown 精简移除）
	hub.lifecycleMgr = connection.NewLifecycleManager(hub, hub.shardedRegistry, hub.heartbeatMgr, connection.MultiLoginPolicy{
		AllowMultiLogin:       config.AllowMultiLogin,
		MaxConnectionsPerUser: config.MaxConnectionsPerUser,
	})

	// 连接记录管理器（连接域：仓储经端口动态读取，支持运行期注入）
	hub.recordMgr = connection.NewRecordManager(hub)

	// WorkerPool（按任务类型分池控制并发）
	hub.workerPool = messaging.NewHubWorkerPool(
		mathx.IfNotZero(config.WorkerPool, wscconfig.DefaultWorkerPoolConfig()),
		logger,
	)

	// ⏰ 定时器（构造期初始化，确保 Register/Refresh/Cancel 在任何 goroutine 启动前读到非 nil）
	// 心跳时间轮已随连接域心跳管理器构造（见上），此处仅初始化 ACK 超时时间轮
	hub.ackTimeoutTimer = syncx.NewHashedWheelTimer(config.Timer.GetTimerOptions()...)

	// ACK 管理器
	ackManager := messaging.NewAckManager(config.AckTimeout, config.AckMaxRetries)

	// 消息域管理器（链式注入域内组件）
	hub.messagingMgr = messaging.NewManager(hub).
		WithAckManager(ackManager).
		WithAckTimeoutTimer(hub.ackTimeoutTimer).
		WithWorkerPool(hub.workerPool).
		WithReplayGate(messaging.NewReplayGate()).
		WithIDGenerator(hub.idGenerator)
	// 离线消息处理器不在此注入：依赖 Redis 队列 + RDBMS 双后端，业务侧在存储就绪后
	// 经 WithOfflineMessageHandler（或 messaging.InitializeOfflineQueue 经 StoreTarget
	// 能力面）注入，内部挂接到 messagingMgr

	// 统计域与群组域管理器
	hub.statsMgr = stats.NewManager(hub)
	hub.groupMgr = group.NewManager(hub)

	// 批处理器域管理器（状态更新 / 记录 outbox / 心跳统计 / 消息统计 / 观察者通知）
	// 观察者直投由消息域 Manager 提供（ObserverNotifier 端口，flush 直连域组件）
	// 记录 outbox 复用状态更新攒批参数：flush 成功后把 sending 记录交给消息域
	// 注册 ACK 超时（注册延后 50ms 量级，秒级超时语义无损）
	hub.batcherMgr = batcher.NewManager(hub, hub.messagingMgr, config.Batcher)
	hub.batcherMgr.RecordOutbox().OnFlushed(hub.messagingMgr.ScheduleAckTimeouts)

	// 🚦 削峰填谷组件默认启用（开箱即用；SetOverloadPolicy 可覆盖/关闭）
	hub.admission.Store(overload.NewAdmissionGate(0, 0, 0))
	hub.broadcastShaper.Store(overload.NewShaper(0))
	hub.ephemeralCoalescer.Store(overload.NewCoalescer(0))
	// 广播延迟队列：填谷重投（Run() 时启动 drain 循环）
	hub.broadcastDelayQueue = overload.NewBroadcastDelayQueue(func() overload.ShaperInterval {
		if s := hub.broadcastShaper.Load(); s != nil {
			return s
		}
		return nil
	})

	return hub
}

// ============================================================================
// K8s 兼容的节点 ID 生成
// ============================================================================

// generateNodeID 生成节点 ID（支持 K8s 环境）
// 优先级：POD_NAME > HOSTNAME > NODE_ID > IP:Port
func generateNodeID(config *wscconfig.WSC) string {
	if podName := osx.Getenv("POD_NAME", ""); podName != "" {
		return podName
	}
	if hostname := osx.Getenv("HOSTNAME", ""); hostname != "" {
		return hostname
	}
	if nodeID := osx.Getenv("NODE_ID", ""); nodeID != "" {
		return nodeID
	}
	return fmt.Sprintf("%s-%d", config.NodeIP, config.NodePort)
}
