/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 23:02:53
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
	"github.com/gorilla/websocket"
	goconfig "github.com/kamalyes/go-config"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/retry"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	Type         MessageType            `json:"type"`                      // 消息类型
	From         string                 `json:"from"`                      // 发送者ID (从上下文获取)
	To           string                 `json:"to"`                        // 接收者ID
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
	VIPLevel   VIPLevel               // VIP等级
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
	recordManager *MessageRecordManager

	// 并发控制
	wg       sync.WaitGroup
	shutdown atomic.Bool
	started  atomic.Bool
	startCh  chan struct{}

	// 欢迎消息提供者
	welcomeProvider WelcomeMessageProvider

	// 统计信息（使用atomic实现无锁统计，提升高并发性能）
	totalConnections  atomic.Int64 // 累计总连接数
	activeConnections atomic.Int64 // 当前活跃连接数
	messagesSent      atomic.Int64 // 已发送消息数
	messagesReceived  atomic.Int64 // 已接收消息数
	broadcastsSent    atomic.Int64 // 已发送广播数
	startTime         int64        // Hub启动时间（Unix时间戳）

	// 增强功能
	messageRouter      *MessageRouter       // 智能消息路由
	loadBalancer       *LoadBalancer        // 负载均衡器
	smartQueue         *SmartQueue          // 智能消息队列
	monitor            *HubMonitor          // 监控系统
	clusterManager     *ClusterManager      // 集群管理
	ruleEngine         *RuleEngine          // 规则引擎
	circuitBreaker     *CircuitBreaker      // 熔断器
	messageFilter      *MessageFilter       // 消息过滤器
	performanceTracker *PerformanceTracker  // 性能追踪器
	logger             WSCLogger            // 日志器
	metricsCollector   *MetricsCollector    // 高级指标收集器
	memoryGuard        *MemoryGuard         // 内存防护
	errorRecovery      *ErrorRecoverySystem // 错误恢复系统
	configValidator    *ConfigValidator     // 配置验证器
	securityManager    *SecurityManager     // 安全管理器

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

// MessageRouter 智能消息路由器
type MessageRouter struct {
	routes       map[MessageType][]RouteRule
	defaultRoute RouteRule
	mutex        sync.RWMutex
}

// RouteRule 路由规则
type RouteRule struct {
	Condition func(*HubMessage, *Client) bool
	Handler   func(*HubMessage, *Client) error
	Priority  int
	Name      string
}

// LoadBalancer 负载均衡器
type LoadBalancer struct {
	algorithm LoadBalanceAlgorithm
	agents    []*Client
	current   int
	mutex     sync.RWMutex
}

// LoadBalanceAlgorithm 负载均衡算法
type LoadBalanceAlgorithm int

const (
	RoundRobin LoadBalanceAlgorithm = iota
	LeastConnections
	WeightedRandom
	ConsistentHash
)

// ParseLoadBalanceAlgorithm 从字符串解析负载均衡算法
func ParseLoadBalanceAlgorithm(algorithm string) LoadBalanceAlgorithm {
	switch algorithm {
	case "least-connections":
		return LeastConnections
	case "weighted-random":
		return WeightedRandom
	case "consistent-hash":
		return ConsistentHash
	default:
		return RoundRobin
	}
}

// SmartQueue 智能消息队列
type SmartQueue struct {
	highPriorityQueue chan *HubMessage
	normalQueue       chan *HubMessage
	lowPriorityQueue  chan *HubMessage
	vipQueue          chan *HubMessage
	maxSize           int
	metrics           *QueueMetrics
	mutex             sync.RWMutex
}

// QueueMetrics 队列指标
type QueueMetrics struct {
	TotalEnqueued atomic.Int64
	TotalDequeued atomic.Int64
	CurrentHigh   atomic.Int64
	CurrentNormal atomic.Int64
	CurrentLow    atomic.Int64
	CurrentVIP    atomic.Int64
}

// HubMonitor 监控系统
type HubMonitor struct {
	metrics         *MonitorMetrics
	alerts          []Alert
	healthChecks    map[string]HealthCheck
	lastHealthCheck time.Time
	alertChannel    chan Alert
	mutex           sync.RWMutex
}

// MonitorMetrics 监控指标
type MonitorMetrics struct {
	CPUUsage          float64
	MemoryUsage       float64
	ConnectionsPerSec float64
	MessagesPerSec    float64
	ErrorRate         float64
	LatencyP95        time.Duration
	LastUpdated       time.Time
}

// Alert 警报
type Alert struct {
	Level     AlertLevel
	Message   string
	Component string
	Timestamp time.Time
	Resolved  bool
}

// AlertLevel 警报级别
type AlertLevel int

const (
	AlertInfo AlertLevel = iota
	AlertWarning
	AlertError
	AlertCritical
)

// HealthCheck 健康检查
type HealthCheck struct {
	Name     string
	Check    func() error
	Interval time.Duration
	Timeout  time.Duration
}

// ClusterManager 集群管理
type ClusterManager struct {
	nodes     map[string]*NodeInfo
	leader    string
	isLeader  bool
	heartbeat time.Duration
	mutex     sync.RWMutex
	election  *Election
}

// Election 选举
type Election struct {
	candidates map[string]*Candidate
	votes      map[string]string
	term       int64
	mutex      sync.RWMutex
}

// Candidate 候选人
type Candidate struct {
	NodeID   string
	Priority int
	LastSeen time.Time
	Votes    int
}

// RuleEngine 规则引擎
type RuleEngine struct {
	rules     []Rule
	rulesets  map[string][]Rule
	variables map[string]interface{}
	mutex     sync.RWMutex
}

// Rule 规则
type Rule struct {
	Name        string
	Condition   func(map[string]interface{}) bool
	Action      func(map[string]interface{}) error
	Priority    int
	Enabled     bool
	Description string
}

// CircuitBreaker 熔断器
type CircuitBreaker struct {
	name             string
	state            CircuitState
	failureCount     int
	successCount     int
	failureThreshold int
	successThreshold int
	timeout          time.Duration
	lastFailTime     time.Time
	mutex            sync.RWMutex
}

// CircuitState 熔断器状态
type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

// MessageFilter 消息过滤器
type MessageFilter struct {
	filters   []Filter
	whitelist map[string]bool
	blacklist map[string]bool
	rateLimit map[string]*RateLimit
	mutex     sync.RWMutex
}

// Filter 过滤器
type Filter struct {
	Name      string
	Condition func(*HubMessage) bool
	Action    FilterAction
	Priority  int
}

// FilterAction 过滤动作
type FilterAction int

const (
	FilterAllow FilterAction = iota
	FilterDeny
	FilterModify
	FilterDelay
)

// RateLimit 速率限制
type RateLimit struct {
	Limit     int
	Window    time.Duration
	Counter   int
	LastReset time.Time
	mutex     sync.Mutex
}

// PerformanceTracker 性能追踪器
type PerformanceTracker struct {
	spans      map[string]*Span
	samples    []Sample
	maxSamples int
	mutex      sync.RWMutex
}

// Span 性能追踪段
type Span struct {
	ID        string
	Name      string
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration
	Tags      map[string]string
	Parent    *Span
	Children  []*Span
}

// Sample 性能样本
type Sample struct {
	Name      string
	Value     float64
	Timestamp time.Time
	Tags      map[string]string
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
	isExplicitConfig := config != nil // 检查是否传递了明确的配置

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
		ackManager:      NewAckManager(config.AckTimeoutMs, config.AckMaxRetries),
		recordManager:   nil, // 将在下面条件创建
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
		startTime: time.Now().Unix(), // 记录启动时间
	}

	// 如果启用消息记录，创建记录管理器
	if config.Group != nil && config.Group.EnableMessageRecord {
		hub.recordManager = NewMessageRecordManager(1000, time.Hour*24, nil)
	}

	// 初始化增强功能组件
	if config.Enhancement != nil && config.Enhancement.Enabled {
		hub.initEnhancementComponents()
	}

	// 初始化高级指标收集器（显式配置且启用时才创建）
	if isExplicitConfig && config.Performance != nil && config.Performance.EnableMetrics {
		interval := time.Duration(config.Performance.MetricsInterval) * time.Second
		if interval <= 0 {
			interval = 5 * time.Minute // 默认5分钟
		}
		hub.metricsCollector = NewMetricsCollector(hub, interval)
	}

	// 初始化内存防护器（默认启用，可通过配置关闭）
	if isExplicitConfig && config.Performance != nil {
		// 创建内存防护器，默认1分钟检查间隔
		hub.memoryGuard = NewMemoryGuard(hub, 1*time.Minute)

		// 如果设置了最大连接数，可以用于计算内存限制
		if config.Performance.MaxConnectionsPerNode > 0 {
			// 估算内存限制：每个连接大约使用1MB内存
			estimatedMemoryMB := int64(config.Performance.MaxConnectionsPerNode + 256) // 加256MB基础内存
			hub.memoryGuard.SetMemoryLimit(estimatedMemoryMB)
		}
	}

	// 初始化错误恢复系统
	if isExplicitConfig {
		hub.errorRecovery = NewErrorRecoverySystem(hub)
	}

	// 初始化配置验证器
	hub.configValidator = NewConfigValidator()

	// 初始化安全管理器
	if isExplicitConfig && config.Security != nil {
		hub.securityManager = NewSecurityManager(config)
	}

	// 验证配置并记录结果
	if hub.configValidator != nil {
		validationResults := hub.configValidator.Validate(config)
		if len(validationResults) > 0 {
			// 尝试自动修复
			fixed, err := hub.configValidator.AutoFix(config)
			if err == nil && len(fixed) > 0 {
				initLogger(config).InfoKV("配置自动修复完成",
					"fixed_count", len(fixed),
				)
			}

			// 记录验证结果
			for _, result := range validationResults {
				switch result.Level {
				case ValidationLevelCritical, ValidationLevelError:
					initLogger(config).ErrorKV("配置验证失败",
						"field", result.Field,
						"message", result.Message,
						"level", result.Level,
					)
				case ValidationLevelWarning:
					initLogger(config).WarnKV("配置验证警告",
						"field", result.Field,
						"message", result.Message,
					)
				case ValidationLevelInfo:
					initLogger(config).InfoKV("配置验证信息",
						"field", result.Field,
						"message", result.Message,
					)
				}
			}
		}
	}
	return hub
}

// initEnhancementComponents 初始化增强功能组件
func (h *Hub) initEnhancementComponents() {
	enhancement := h.config.Enhancement
	if enhancement == nil {
		return
	}

	// 智能路由
	if enhancement.SmartRouting {
		h.messageRouter = NewMessageRouter()
	}

	// 负载均衡
	if enhancement.LoadBalancing {
		h.loadBalancer = NewLoadBalancer(ParseLoadBalanceAlgorithm(enhancement.LoadBalanceAlgorithm))
	}

	// 智能队列
	if enhancement.SmartQueue {
		h.smartQueue = NewSmartQueue(enhancement.MaxQueueSize)
	}

	// 监控系统
	if enhancement.Monitoring {
		h.monitor = NewHubMonitor()
	}

	// 集群管理
	if enhancement.ClusterManagement {
		h.clusterManager = NewClusterManager(h.nodeID)
	}

	// 规则引擎
	if enhancement.RuleEngine {
		h.ruleEngine = NewRuleEngine()
	}

	// 熔断器
	if enhancement.CircuitBreaker {
		h.circuitBreaker = NewCircuitBreaker(
			"hub-circuit-breaker",
			enhancement.FailureThreshold,
			enhancement.SuccessThreshold,
			time.Duration(enhancement.CircuitTimeout)*time.Second,
		)
	}

	// 消息过滤器
	if enhancement.MessageFiltering {
		h.messageFilter = NewMessageFilter()
	}

	// 性能追踪器
	if enhancement.PerformanceTracking {
		h.performanceTracker = NewPerformanceTracker(enhancement.MaxSamples)
	}
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

		// 启动指标收集器（如果已配置）
		if h.metricsCollector != nil {
			h.metricsCollector.Start()
		}

		// 启动内存防护器（如果已配置）
		if h.memoryGuard != nil {
			h.memoryGuard.Start()
		}

		// 启动错误恢复系统（如果已配置）
		if h.errorRecovery != nil {
			h.errorRecovery.Start()
		}

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

	totalConnections := h.totalConnections.Load()
	totalMessages := h.messagesSent.Load()
	totalBroadcasts := h.broadcastsSent.Load()

	// 记录性能指标日志
	h.logger.LogPerformance("hub_metrics", "5m", map[string]interface{}{
		"active_websocket_clients": activeClients,
		"active_sse_clients":       sseClients,
		"total_connections":        totalConnections,
		"total_messages_sent":      totalMessages,
		"total_broadcasts_sent":    totalBroadcasts,
		"node_id":                  h.nodeID,
		"uptime_seconds":           time.Now().Unix() - h.startTime,
	})

	// 如果有性能追踪器，记录额外的性能数据
	if h.performanceTracker != nil {
		samples := h.performanceTracker.GetSamples()
		if len(samples) > 0 {
			h.logger.InfoKV("性能样本统计",
				"sample_count", len(samples),
				"latest_samples", func() interface{} {
					if len(samples) > 5 {
						return samples[len(samples)-5:]
					}
					return samples
				}(),
			)
		}
	}
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

	// 停止指标收集器（如果已配置）
	if h.metricsCollector != nil {
		h.metricsCollector.Stop()
	}

	// 停止内存防护器（如果已配置）
	if h.memoryGuard != nil {
		h.memoryGuard.Stop()
	}

	// 停止错误恢复系统（如果已配置）
	if h.errorRecovery != nil {
		h.errorRecovery.Stop()
	}

	// 停止安全管理器（如果已配置）
	if h.securityManager != nil {
		h.securityManager.Shutdown()
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
		h.logger.InfoKV("Hub安全关闭成功",
			"node_id", h.nodeID,
			"shutdown_timeout", timeout,
			"final_stats", map[string]interface{}{
				"total_connections": h.totalConnections.Load(),
				"messages_sent":     h.messagesSent.Load(),
				"broadcasts_sent":   h.broadcastsSent.Load(),
			},
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
	h.logger.LogConnection(client.ID, client.UserID, "register_request")
	h.register <- client
}

// Unregister 注销客户端
func (h *Hub) Unregister(client *Client) {
	h.logger.LogConnection(client.ID, client.UserID, "unregister_request")
	h.unregister <- client
}

// SendToUser 发送消息给指定用户（自动填充发送者信息）
func (h *Hub) SendToUser(ctx context.Context, toUserID string, msg *HubMessage) error {
	// 创建消息副本以避免竞态条件
	msgCopy := *msg

	// 从上下文获取发送者ID
	if msgCopy.From == "" {
		if senderID, ok := ctx.Value(ContextKeySenderID).(string); ok {
			msgCopy.From = senderID
		} else if userID, ok := ctx.Value(ContextKeyUserID).(string); ok {
			msgCopy.From = userID
		}
	}

	msgCopy.To = toUserID
	if msgCopy.CreateAt.IsZero() {
		msgCopy.CreateAt = time.Now()
	}

	// 尝试发送到broadcast队列
	select {
	case h.broadcast <- &msgCopy:
		h.logger.LogMessage(msgCopy.ID, msgCopy.From, msgCopy.To, msgCopy.Type, true, nil)
		return nil
	default:
		// broadcast队列满，尝试放入待发送队列
		select {
		case h.pendingMessages <- &msgCopy:
			return nil
		default:
			err := ErrQueueAndPendingFull
			// 记录消息发送失败日志
			h.logger.LogMessage(msgCopy.ID, msgCopy.From, msgCopy.To, msgCopy.Type, false, err)
			// 通知队列满处理器
			h.notifyQueueFull(&msgCopy, toUserID, "all_queues", err)
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
		"from", msg.From,
		"to", recipient,
		"type", msg.Type,
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
			"from", msg.From,
			"type", msg.Type,
		)
		select {
		case h.pendingMessages <- msg:
			// 成功放入待发送队列
		default:
			// 两个队列都满，静默丢弃（广播消息不返回错误）
			h.logger.ErrorKV("所有队列已满，丢弃广播消息",
				"message_id", msg.ID,
				"from", msg.From,
				"type", msg.Type,
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
					"from", msg.From,
					"to", msg.To,
					"type", msg.Type,
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
	h.logger.LogConnection("sse_"+conn.UserID, conn.UserID, "sse_connected")
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
		h.logger.LogConnection("sse_"+userID, userID, "sse_disconnected")
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
			"message_type", msg.Type,
		)
		return false
	}

	select {
	case conn.MessageCh <- msg:
		conn.LastActive = time.Now()
		// 记录SSE消息发送成功
		h.logger.LogMessage(msg.ID, msg.From, userID, msg.Type, true, nil)
		h.logger.InfoKV("SSE消息发送成功",
			"user_id", userID,
			"message_id", msg.ID,
			"message_type", msg.Type,
		)
		return true
	default:
		// SSE消息队列满
		h.logger.WarnKV("SSE消息队列已满",
			"user_id", userID,
			"message_id", msg.ID,
			"message_type", msg.Type,
		)
		return false
	}
}

// SendToUserWithAck 发送消息给指定用户并等待ACK确认
func (h *Hub) SendToUserWithAck(ctx context.Context, toUserID string, msg *HubMessage, timeout time.Duration, maxRetry int) (*AckMessage, error) {
	// 检查是否启用ACK
	if !h.safeConfig.Field("EnableAck").Bool(false) {
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
	)

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

			// 离线消息处理成功
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

	// 更新记录状态为发送中
	if record != nil {
		h.recordManager.UpdateRecordStatus(msg.ID, MessageSendStatusSending, "", "")
	}

	// 使用配置中的ACK超时时间，如果传入的timeout > 0则使用传入值
	ackTimeout := h.safeConfig.Field("AckTimeoutMs").Duration(500 * time.Millisecond)
	if timeout > 0 {
		ackTimeout = timeout
	}

	// 添加到待确认队列
	pm := h.ackManager.AddPendingMessage(msg, ackTimeout, maxRetry)
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

// SetOfflineMessageHandler 设置离线消息处理器
func (h *Hub) SetOfflineMessageHandler(handler OfflineMessageHandler) {
	if h.ackManager != nil {
		h.ackManager.offlineHandler = handler
	}
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

// notifyConnectionError 通知连接错误处理器
func (h *Hub) notifyConnectionError(msg *HubMessage, clientID string, err error) {
	h.failureHandlerMutex.RLock()
	connHandlers := make([]ConnectionErrorHandler, len(h.connectionErrorHandlers))
	copy(connHandlers, h.connectionErrorHandlers)
	generalHandlers := make([]SendFailureHandler, len(h.sendFailureHandlers))
	copy(generalHandlers, h.sendFailureHandlers)
	h.failureHandlerMutex.RUnlock()

	// 调用专门的连接错误处理器
	for _, handler := range connHandlers {
		go func(h ConnectionErrorHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			h.HandleConnectionError(msg, clientID, err)
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
			h.HandleSendFailure(msg, clientID, SendFailureReasonConnError, err)
		}(handler)
	}
}

// notifyTimeout 通知超时处理器
func (h *Hub) notifyTimeout(msg *HubMessage, recipient string, timeoutType string, duration time.Duration, err error) {
	h.failureHandlerMutex.RLock()
	timeoutHandlers := make([]TimeoutHandler, len(h.timeoutHandlers))
	copy(timeoutHandlers, h.timeoutHandlers)
	generalHandlers := make([]SendFailureHandler, len(h.sendFailureHandlers))
	copy(generalHandlers, h.sendFailureHandlers)
	h.failureHandlerMutex.RUnlock()

	// 调用专门的超时处理器
	for _, handler := range timeoutHandlers {
		go func(h TimeoutHandler) {
			defer func() {
				if r := recover(); r != nil {
					// 防止处理器panic影响主流程
				}
			}()
			h.HandleTimeout(msg, recipient, timeoutType, duration, err)
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
			h.HandleSendFailure(msg, recipient, SendFailureReasonTimeout, err)
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
		h.logger.WarnKV("消息记录管理器未初始化，无法重试",
			"message_id", messageID,
		)
		return errorx.NewError(ErrTypeRecordManagerNotInitialized)
	}

	// 获取消息记录
	record, exists := h.recordManager.GetRecord(messageID)
	if !exists {
		h.logger.WarnKV("消息记录不存在，无法重试",
			"message_id", messageID,
		)
		return errorx.NewError(ErrTypeMessageRecordNotFound, "message_id: %s", messageID)
	}

	// 检查是否可重试
	if record.RetryCount >= record.MaxRetry {
		h.logger.WarnKV("消息已达最大重试次数，无法重试",
			"message_id", messageID,
			"retry_count", record.RetryCount,
			"max_retries", record.MaxRetry,
		)
		return errorx.NewError(ErrTypeMaxRetriesExceeded)
	}

	// 记录重试开始
	h.logger.InfoKV("开始重试失败消息",
		"message_id", messageID,
		"retry_count", record.RetryCount+1,
		"max_retries", record.MaxRetry,
		"status", record.Status,
	)

	// 重新发送消息
	ctx := context.Background()
	if record.Message.To != "" {
		_, err := h.SendToUserWithAck(ctx, record.Message.To, record.Message, 0, record.MaxRetry-record.RetryCount)
		return err
	}

	return ErrMessageTargetMissing
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
		"node_id":            h.nodeID,
		"websocket_count":    wsCount,
		"sse_count":          sseCount,
		"total_connections":  wsCount + sseCount,
		"total_lifetime":     h.totalConnections.Load(),
		"active_connections": h.activeConnections.Load(),
		"messages_sent":      h.messagesSent.Load(),
		"messages_received":  h.messagesReceived.Load(),
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
			// 捕获panic，报告错误
			if h.errorRecovery != nil {
				ctx := map[string]interface{}{
					"client_id": client.ID,
					"user_id":   client.UserID,
					"panic":     r,
				}
				h.errorRecovery.ReportError(ErrorTypeSystem, SeverityCritical,
					fmt.Sprintf("handleRegister panic: %v", r), ctx)
			}
		}
	}()

	// 安全验证 - 验证客户端连接
	if h.securityManager != nil {
		clientIP := ""
		headers := make(map[string]string)

		// 从客户端连接中获取IP地址
		if client.Conn != nil {
			if remoteAddr := client.Conn.RemoteAddr(); remoteAddr != nil {
				clientIP = remoteAddr.String()
			}
		}

		// 验证连接
		if err := h.securityManager.ValidateConnection(clientIP, client.UserID, headers); err != nil {
			h.logger.WarnKV("客户端连接被安全策略拒绝",
				"client_id", client.ID,
				"user_id", client.UserID,
				"client_ip", clientIP,
				"reason", err.Error(),
			)

			// 关闭连接
			if client.Conn != nil {
				client.Conn.Close()
			}
			return
		}
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

	// 关闭旧连接
	if existingClient, exists := h.userToClient[client.UserID]; exists {
		h.logger.LogConnection(existingClient.ID, existingClient.UserID, "disconnect_old_connection")
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

	// 使用atomic无锁更新统计信息
	h.totalConnections.Add(1)
	h.activeConnections.Store(int64(len(h.clients)))

	// 更新指标收集器
	if h.metricsCollector != nil {
		atomic.AddInt64(&h.metricsCollector.totalConnections, 1)
	}

	// 记录成功注册日志
	h.logger.LogConnection(client.ID, client.UserID, "connected")
	h.logger.InfoKV("客户端连接成功",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"total_connections", h.totalConnections.Load(),
		"active_connections", len(h.clients),
	)

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
	h.logger.LogConnection(client.ID, client.UserID, "disconnected")
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

	h.activeConnections.Store(int64(len(h.clients)))

	if client.SendChan != nil {
		defer func() { recover() }()
		close(client.SendChan)
	}
}

func (h *Hub) handleBroadcast(msg *HubMessage) {
	// 安全验证 - 验证消息内容
	if h.securityManager != nil && msg != nil {
		// 获取发送者的客户端信息以获取IP
		var clientIP string
		h.mutex.RLock()
		if senderClient, exists := h.userToClient[msg.From]; exists {
			if senderClient.Conn != nil && senderClient.Conn.RemoteAddr() != nil {
				clientIP = senderClient.Conn.RemoteAddr().String()
			}
		}
		h.mutex.RUnlock()

		// 验证消息内容
		if err := h.securityManager.ValidateMessage(msg.From, clientIP, []byte(msg.Content)); err != nil {
			h.logger.WarnKV("消息被安全策略拒绝",
				"message_id", msg.ID,
				"from", msg.From,
				"to", msg.To,
				"type", msg.Type,
				"client_ip", clientIP,
				"reason", err.Error(),
			)
			// 记录消息发送失败
			h.logger.LogMessage(msg.ID, msg.From, msg.To, msg.Type, false, err)
			return
		}
	}

	switch {
	case msg.To != "": // 点对点消息 - 最快路径
		h.mutex.RLock()
		client := h.userToClient[msg.To]
		h.mutex.RUnlock()

		if client != nil {
			h.sendToClient(client, msg)
			h.logger.LogMessage(msg.ID, msg.From, msg.To, msg.Type, true, nil)
		} else {
			// 客户端不在线，尝试SSE
			sent := h.SendToUserViaSSE(msg.To, msg)
			if sent {
				h.logger.LogMessage(msg.ID, msg.From, msg.To, msg.Type, true, nil)
			} else {
				// SSE也失败，记录用户离线
				h.logger.LogMessage(msg.ID, msg.From, msg.To, msg.Type, false, ErrUserOffline)
				h.logger.WarnKV("用户离线，消息发送失败",
					"message_id", msg.ID,
					"from", msg.From,
					"to", msg.To,
					"type", msg.Type,
				)
			}
		}

	default: // 广播消息
		// 统计广播数
		h.broadcastsSent.Add(1)

		// 记录广播消息日志
		h.logger.LogMessage(msg.ID, msg.From, "broadcast", msg.Type, true, nil)
		h.logger.InfoKV("发送广播消息",
			"message_id", msg.ID,
			"from", msg.From,
			"type", msg.Type,
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

// fastMarshalMessage 高效消息序列化方法，避免反射和多次内存分配
func (h *Hub) fastMarshalMessage(msg *HubMessage, buf []byte) ([]byte, error) {
	// 手动构建 JSON，避免 reflect 的开销
	buf = append(buf, `{"id":"`...)
	buf = append(buf, msg.ID...)
	buf = append(buf, `","type":"`...)
	buf = append(buf, string(msg.Type)...)
	buf = append(buf, `","content":"`...)

	// 转义JSON字符
	for _, r := range msg.Content {
		switch r {
		case '"':
			buf = append(buf, '\\', '"')
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		default:
			if r < 32 {
				buf = append(buf, fmt.Sprintf("\\u%04x", r)...)
			} else {
				buf = append(buf, string(r)...)
			}
		}
	}

	buf = append(buf, `","from":"`...)
	buf = append(buf, msg.From...)
	buf = append(buf, `","to":"`...)
	buf = append(buf, msg.To...)

	buf = append(buf, `","create_at":"`...)
	buf = append(buf, msg.CreateAt.Format(time.RFC3339)...)
	buf = append(buf, `"`...)

	// 添加 Data 字段（如果存在）
	if len(msg.Data) > 0 {
		buf = append(buf, `,"data":{`...)
		first := true
		for key, value := range msg.Data {
			if !first {
				buf = append(buf, `,`...)
			}
			first = false

			// 添加键
			buf = append(buf, `"`...)
			buf = append(buf, key...)
			buf = append(buf, `":`...)

			// 添加值（简单类型处理）
			switch v := value.(type) {
			case string:
				buf = append(buf, `"`...)
				// 转义字符串值
				for _, r := range v {
					switch r {
					case '"':
						buf = append(buf, '\\', '"')
					case '\\':
						buf = append(buf, '\\', '\\')
					case '\n':
						buf = append(buf, '\\', 'n')
					case '\r':
						buf = append(buf, '\\', 'r')
					case '\t':
						buf = append(buf, '\\', 't')
					default:
						if r < 32 {
							buf = append(buf, fmt.Sprintf("\\u%04x", r)...)
						} else {
							buf = append(buf, string(r)...)
						}
					}
				}
				buf = append(buf, `"`...)
			case int:
				buf = append(buf, fmt.Sprintf("%d", v)...)
			case int64:
				buf = append(buf, fmt.Sprintf("%d", v)...)
			case float64:
				buf = append(buf, fmt.Sprintf("%g", v)...)
			case bool:
				if v {
					buf = append(buf, `true`...)
				} else {
					buf = append(buf, `false`...)
				}
			default:
				// 对于复杂类型，使用 JSON 序列化
				if valueBytes, err := json.Marshal(v); err == nil {
					buf = append(buf, valueBytes...)
				} else {
					buf = append(buf, `null`...)
				}
			}
		}
		buf = append(buf, `}`...)
	}

	buf = append(buf, `}`...)

	// 返回复制的数据，避免共享底层数组
	result := make([]byte, len(buf))
	copy(result, buf)
	return result, nil
}

func (h *Hub) sendToClient(client *Client, msg *HubMessage) {
	defer func() {
		if r := recover(); r != nil {
			// 捕获panic，报告错误
			if h.errorRecovery != nil {
				ctx := map[string]interface{}{
					"client_id":    client.ID,
					"user_id":      client.UserID,
					"message_id":   msg.ID,
					"message_type": msg.Type,
					"panic":        r,
				}
				h.errorRecovery.ReportError(ErrorTypeMessage, SeverityCritical,
					fmt.Sprintf("sendToClient panic: %v", r), ctx)
			}
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

		// 记录消息错误指标
		if h.metricsCollector != nil {
			h.metricsCollector.IncrementMessageError()
		}

		// 报告序列化错误
		if h.errorRecovery != nil {
			ctx := map[string]interface{}{
				"client_id":    client.ID,
				"user_id":      client.UserID,
				"message_id":   msg.ID,
				"message_type": msg.Type,
			}
			h.errorRecovery.ReportError(ErrorTypeMessage, SeverityMedium,
				"消息序列化失败: "+err.Error(), ctx)
		}

		return
	}

	// 再次检查，避免在序列化过程中 shutdown
	if h.shutdown.Load() {
		return
	}

	select {
	case client.SendChan <- data:
		h.messagesSent.Add(1)

		// 更新指标收集器
		if h.metricsCollector != nil {
			h.metricsCollector.IncrementMessageSent(len(data))
		}
	case <-h.ctx.Done():
		return
	default:
		// 队列满，跳过该消息
		h.logger.WarnKV("客户端发送队列已满，跳过消息",
			"client_id", client.ID,
			"user_id", client.UserID,
			"message_id", msg.ID,
			"message_type", msg.Type,
		)

		// 记录队列溢出指标
		if h.metricsCollector != nil {
			h.metricsCollector.IncrementQueueOverflow()
		}

		// 报告队列满错误
		if h.errorRecovery != nil {
			ctx := map[string]interface{}{
				"client_id":    client.ID,
				"user_id":      client.UserID,
				"message_id":   msg.ID,
				"message_type": msg.Type,
			}
			h.errorRecovery.ReportError(ErrorTypeConcurrency, SeverityMedium,
				"客户端发送队列已满", ctx)
		}
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

	for _, client := range h.clients {
		if now.Sub(client.LastSeen) > time.Duration(h.safeConfig.GetInt("ClientTimeout", 90))*time.Second {
			h.logger.LogConnection(client.ID, client.UserID, "heartbeat_timeout")
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
			h.logger.LogConnection("sse_"+userID, userID, "sse_timeout")
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
		Type:     welcomeMsg.MessageType,
		From:     "system",
		To:       client.UserID,
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

// GetOnlineUsersByType 获取特定类型的在线用户
func (h *Hub) GetOnlineUsersByType(userType UserType) []string {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	users := make([]string, 0)
	for _, client := range h.clients {
		if client.UserType == userType {
			users = append(users, client.UserID)
		}
	}
	return users
}

// IsUserOnline 检查用户是否在线
func (h *Hub) IsUserOnline(userID string) bool {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	if _, exists := h.userToClient[userID]; exists {
		return true
	}

	h.sseMutex.RLock()
	defer h.sseMutex.RUnlock()
	_, exists := h.sseClients[userID]
	return exists
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
		MessagesSent:     h.messagesSent.Load(),
		MessagesReceived: h.messagesReceived.Load(),
		BroadcastsSent:   h.broadcastsSent.Load(),
		QueuedMessages:   len(h.pendingMessages),
		OnlineUsers:      h.GetOnlineUsersCount(),
		Uptime:           h.GetUptime(),
	}

	return stats
}

// GetUptime 获取Hub运行时间（秒）
func (h *Hub) GetUptime() int64 {
	if h.startTime == 0 {
		return 0
	}
	return time.Now().Unix() - h.startTime
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
		h.logger.LogConnection(client.ID, userID, "kicked_off")
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
		h.logger.LogConnection(clients[i].ID, userID, "connection_limited")
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
		"status":             "healthy",
		"is_running":         !isShutdown,
		"websocket_count":    wsCount,
		"sse_count":          sseCount,
		"total_connections":  wsCount + sseCount,
		"messages_sent":      h.messagesSent.Load(),
		"messages_received":  h.messagesReceived.Load(),
		"active_connections": h.activeConnections.Load(),
		"total_lifetime":     h.totalConnections.Load(),
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
	stats := h.GetMessageStatistics()

	return map[string]interface{}{
		"message_stats": stats,
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

		select {
		case <-ticker.C:
			if time.Now().After(deadline) {
				return false
			}
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
		msg.Type = classification.Type

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

// ============================================================================
// 增强功能实现
// ============================================================================

// InitializeEnhancements 初始化增强功能
func (h *Hub) InitializeEnhancements() {
	h.messageRouter = NewMessageRouter()
	h.loadBalancer = NewLoadBalancer(RoundRobin)
	h.smartQueue = NewSmartQueue(1000)
	h.monitor = NewHubMonitor()
	h.clusterManager = NewClusterManager(h.nodeID)
	h.ruleEngine = NewRuleEngine()
	h.circuitBreaker = NewCircuitBreaker("hub-main", 10, 5, 30*time.Second)
	h.messageFilter = NewMessageFilter()
	h.performanceTracker = NewPerformanceTracker(10000)
}

// SendWithEnhancement 使用增强功能发送消息
func (h *Hub) SendWithEnhancement(ctx context.Context, userID string, msg *HubMessage) error {
	span := h.performanceTracker.StartSpan("send_enhanced_message")
	defer span.End()

	// 断路器检查
	if !h.circuitBreaker.AllowRequest() {
		return ErrCircuitBreakerOpen
	}

	// 消息过滤
	if !h.messageFilter.Allow(msg) {
		return ErrMessageFiltered
	}

	// 规则引擎处理
	variables := map[string]interface{}{
		"userID":  userID,
		"message": msg,
		"hub":     h,
	}
	if err := h.ruleEngine.Process(variables); err != nil {
		h.circuitBreaker.RecordFailure()
		return err
	}

	// 智能路由
	client := h.userToClient[userID]
	if client != nil {
		if err := h.messageRouter.Route(msg, client); err != nil {
			h.circuitBreaker.RecordFailure()
			return err
		}
	}

	// 加入智能队列
	if err := h.smartQueue.Enqueue(msg); err != nil {
		h.circuitBreaker.RecordFailure()
		return err
	}

	h.circuitBreaker.RecordSuccess()
	return nil
}

// SendWithLoadBalance 使用负载均衡发送到客服
func (h *Hub) SendWithLoadBalance(ctx context.Context, msg *HubMessage) error {
	agent := h.loadBalancer.SelectAgent()
	if agent == nil {
		return ErrNoAvailableAgents
	}

	return h.SendToUser(ctx, agent.UserID, msg)
}

// BroadcastWithPriority 按优先级广播消息
func (h *Hub) BroadcastWithPriority(ctx context.Context, msg *HubMessage) error {
	// 根据消息优先级选择队列
	return h.smartQueue.Enqueue(msg)
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

// GetEnhancedMetrics 获取增强指标
func (h *Hub) GetEnhancedMetrics() map[string]interface{} {
	return map[string]interface{}{
		"hub_metrics":         h.GetMessageStatistics(),
		"queue_metrics":       h.smartQueue.GetMetrics(),
		"monitor_metrics":     h.monitor.GetMetrics(),
		"circuit_breaker":     h.circuitBreaker.GetState(),
		"performance_samples": h.performanceTracker.GetSamples(),
		"cluster_status":      h.clusterManager.GetStatus(),
	}
}

// AddRule 添加业务规则
func (h *Hub) AddRule(rule Rule) {
	h.ruleEngine.AddRule(rule)
}

// AddFilter 添加消息过滤器
func (h *Hub) AddFilter(filter Filter) {
	h.messageFilter.AddFilter(filter)
}

// SetRateLimit 设置速率限制
func (h *Hub) SetRateLimit(userID string, limit int, window time.Duration) {
	h.messageFilter.SetRateLimit(userID, limit, window)
}

// ProcessHealthCheck 执行健康检查
func (h *Hub) ProcessHealthCheck() map[string]error {
	return h.monitor.RunHealthChecks()
}

// GetAlerts 获取警报
func (h *Hub) GetAlerts() []Alert {
	return h.monitor.GetAlerts()
}

// ============================================================================
// 构造函数
// ============================================================================

// NewMessageRouter 创建消息路由器
func NewMessageRouter() *MessageRouter {
	return &MessageRouter{
		routes: make(map[MessageType][]RouteRule),
		defaultRoute: RouteRule{
			Name: "default",
			Handler: func(msg *HubMessage, client *Client) error {
				return nil
			},
		},
	}
}

// NewLoadBalancer 创建负载均衡器
func NewLoadBalancer(algorithm LoadBalanceAlgorithm) *LoadBalancer {
	return &LoadBalancer{
		algorithm: algorithm,
		agents:    make([]*Client, 0),
	}
}

// NewSmartQueue 创建智能队列
func NewSmartQueue(maxSize int) *SmartQueue {
	return &SmartQueue{
		highPriorityQueue: make(chan *HubMessage, maxSize/4),
		normalQueue:       make(chan *HubMessage, maxSize/2),
		lowPriorityQueue:  make(chan *HubMessage, maxSize/4),
		vipQueue:          make(chan *HubMessage, maxSize/4),
		maxSize:           maxSize,
		metrics:           &QueueMetrics{},
	}
}

// NewHubMonitor 创建监控系统
func NewHubMonitor() *HubMonitor {
	return &HubMonitor{
		metrics:      &MonitorMetrics{},
		alerts:       make([]Alert, 0),
		healthChecks: make(map[string]HealthCheck),
		alertChannel: make(chan Alert, 100),
	}
}

// NewClusterManager 创建集群管理器
func NewClusterManager(nodeID string) *ClusterManager {
	return &ClusterManager{
		nodes:     make(map[string]*NodeInfo),
		leader:    "",
		isLeader:  false,
		heartbeat: 30 * time.Second,
		election: &Election{
			candidates: make(map[string]*Candidate),
			votes:      make(map[string]string),
		},
	}
}

// NewRuleEngine 创建规则引擎
func NewRuleEngine() *RuleEngine {
	return &RuleEngine{
		rules:     make([]Rule, 0),
		rulesets:  make(map[string][]Rule),
		variables: make(map[string]interface{}),
	}
}

// NewCircuitBreaker 创建熔断器
func NewCircuitBreaker(name string, failureThreshold, successThreshold int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		name:             name,
		state:            CircuitClosed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		timeout:          timeout,
	}
}

// NewMessageFilter 创建消息过滤器
func NewMessageFilter() *MessageFilter {
	return &MessageFilter{
		filters:   make([]Filter, 0),
		whitelist: make(map[string]bool),
		blacklist: make(map[string]bool),
		rateLimit: make(map[string]*RateLimit),
	}
}

// NewPerformanceTracker 创建性能追踪器
func NewPerformanceTracker(maxSamples int) *PerformanceTracker {
	return &PerformanceTracker{
		spans:      make(map[string]*Span),
		samples:    make([]Sample, 0),
		maxSamples: maxSamples,
	}
}

// ============================================================================
// 增强功能方法实现
// ============================================================================

// MessageRouter methods
func (mr *MessageRouter) Route(msg *HubMessage, client *Client) error {
	mr.mutex.RLock()
	defer mr.mutex.RUnlock()

	if rules, exists := mr.routes[msg.Type]; exists {
		for _, rule := range rules {
			if rule.Condition(msg, client) {
				return rule.Handler(msg, client)
			}
		}
	}

	return mr.defaultRoute.Handler(msg, client)
}

// LoadBalancer methods
func (lb *LoadBalancer) SelectAgent() *Client {
	lb.mutex.RLock()
	defer lb.mutex.RUnlock()

	if len(lb.agents) == 0 {
		return nil
	}

	switch lb.algorithm {
	case RoundRobin:
		agent := lb.agents[lb.current%len(lb.agents)]
		lb.current++
		return agent
	case LeastConnections:
		// 选择连接数最少的客服
		var selected *Client
		minConnections := int(^uint(0) >> 1) // Max int
		for _, agent := range lb.agents {
			if connections := agent.MaxTickets; connections < minConnections {
				minConnections = connections
				selected = agent
			}
		}
		return selected
	default:
		return lb.agents[0]
	}
}

// SmartQueue methods
func (sq *SmartQueue) Enqueue(msg *HubMessage) error {
	sq.mutex.Lock()
	defer sq.mutex.Unlock()

	var targetQueue chan *HubMessage

	// 根据消息类型和优先级选择队列
	switch {
	case msg.Priority == PriorityHigh:
		targetQueue = sq.highPriorityQueue
		sq.metrics.CurrentHigh.Add(1)
	case msg.Type == MessageTypeAlert || msg.Type == MessageTypeSystem:
		targetQueue = sq.vipQueue
		sq.metrics.CurrentVIP.Add(1)
	case msg.Priority == PriorityLow:
		targetQueue = sq.lowPriorityQueue
		sq.metrics.CurrentLow.Add(1)
	default:
		targetQueue = sq.normalQueue
		sq.metrics.CurrentNormal.Add(1)
	}

	select {
	case targetQueue <- msg:
		sq.metrics.TotalEnqueued.Add(1)
		return nil
	default:
		return ErrQueueFull
	}
}

func (sq *SmartQueue) GetMetrics() *QueueMetrics {
	return sq.metrics
}

// CircuitBreaker methods
func (cb *CircuitBreaker) AllowRequest() bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(cb.lastFailTime) > cb.timeout {
			cb.state = CircuitHalfOpen
			cb.successCount = 0
			return true
		}
		return false
	case CircuitHalfOpen:
		return true
	default:
		return false
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.failureCount = 0
	if cb.state == CircuitHalfOpen {
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.state = CircuitClosed
		}
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.failureCount++
	cb.lastFailTime = time.Now()

	if cb.state == CircuitClosed && cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitOpen
	} else if cb.state == CircuitHalfOpen {
		cb.state = CircuitOpen
	}
}

func (cb *CircuitBreaker) GetState() map[string]interface{} {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	return map[string]interface{}{
		"name":          cb.name,
		"state":         cb.state,
		"failure_count": cb.failureCount,
		"success_count": cb.successCount,
		"last_fail":     cb.lastFailTime,
	}
}

// MessageFilter methods
func (mf *MessageFilter) Allow(msg *HubMessage) bool {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	// 黑名单检查
	if mf.blacklist[msg.From] {
		return false
	}

	// 白名单检查
	if len(mf.whitelist) > 0 && !mf.whitelist[msg.From] {
		return false
	}

	// 速率限制检查
	if rateLimit, exists := mf.rateLimit[msg.From]; exists {
		if !rateLimit.Allow() {
			return false
		}
	}

	// 过滤器检查
	for _, filter := range mf.filters {
		if filter.Condition(msg) {
			switch filter.Action {
			case FilterDeny:
				return false
			case FilterAllow:
				return true
			}
		}
	}

	return true
}

func (mf *MessageFilter) AddFilter(filter Filter) {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()
	mf.filters = append(mf.filters, filter)
}

func (mf *MessageFilter) SetRateLimit(userID string, limit int, window time.Duration) {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()
	mf.rateLimit[userID] = &RateLimit{
		Limit:     limit,
		Window:    window,
		LastReset: time.Now(),
	}
}

// RateLimit methods
func (rl *RateLimit) Allow() bool {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	now := time.Now()
	if now.Sub(rl.LastReset) > rl.Window {
		rl.Counter = 0
		rl.LastReset = now
	}

	if rl.Counter < rl.Limit {
		rl.Counter++
		return true
	}

	return false
}

// RuleEngine methods
func (re *RuleEngine) AddRule(rule Rule) {
	re.mutex.Lock()
	defer re.mutex.Unlock()
	re.rules = append(re.rules, rule)
}

func (re *RuleEngine) Process(variables map[string]interface{}) error {
	re.mutex.RLock()
	defer re.mutex.RUnlock()

	for _, rule := range re.rules {
		if rule.Enabled && rule.Condition(variables) {
			if err := rule.Action(variables); err != nil {
				return err
			}
		}
	}

	return nil
}

// HubMonitor methods
func (hm *HubMonitor) GetMetrics() *MonitorMetrics {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()
	return hm.metrics
}

func (hm *HubMonitor) GetAlerts() []Alert {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()
	return hm.alerts
}

func (hm *HubMonitor) RunHealthChecks() map[string]error {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()

	results := make(map[string]error)
	for name, check := range hm.healthChecks {
		results[name] = check.Check()
	}

	return results
}

// ClusterManager methods
func (cm *ClusterManager) GetStatus() map[string]interface{} {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	return map[string]interface{}{
		"nodes":     len(cm.nodes),
		"leader":    cm.leader,
		"is_leader": cm.isLeader,
		"heartbeat": cm.heartbeat,
	}
}

// PerformanceTracker methods
func (pt *PerformanceTracker) StartSpan(name string) *Span {
	span := &Span{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Name:      name,
		StartTime: time.Now(),
		Tags:      make(map[string]string),
		Children:  make([]*Span, 0),
	}

	pt.mutex.Lock()
	pt.spans[span.ID] = span
	pt.mutex.Unlock()

	return span
}

func (pt *PerformanceTracker) GetSamples() []Sample {
	pt.mutex.RLock()
	defer pt.mutex.RUnlock()
	return pt.samples
}

// Span methods
func (s *Span) End() {
	s.EndTime = time.Now()
	s.Duration = s.EndTime.Sub(s.StartTime)
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

// initLogger 根据配置初始化日志器
func initLogger(config *wscconfig.WSC) WSCLogger {
	// 如果配置中有日志配置且启用，使用配置中的
	if config.Logging != nil && config.Logging.Enabled {
		// 转换配置到 go-logger 的配置
		loggerConfig := logger.DefaultConfig().
			WithLevel(parseLogLevel(config.Logging.Level)).
			WithPrefix("[WSC] ").
			WithShowCaller(false).
			WithColorful(true).
			WithTimeFormat(time.DateTime)

		// 根据输出类型配置输出
		switch config.Logging.Output {
		case "file":
			if config.Logging.FilePath != "" {
				loggerConfig = loggerConfig.WithOutput(logger.NewFileWriterFromConfig(&logger.FileConfig{
					Filename:   config.Logging.FilePath,
					MaxSize:    config.Logging.MaxSize,
					MaxBackups: config.Logging.MaxBackups,
					MaxAge:     config.Logging.MaxAge,
					Compress:   config.Logging.Compress,
				}))
			}
		default:
			// 默认使用控制台输出
			loggerConfig = loggerConfig.WithOutput(logger.NewConsoleWriter(os.Stdout))
		}

		return NewWSCLogger(loggerConfig)
	}

	// 使用默认配置
	return NewDefaultWSCLogger()
}

// parseLogLevel 解析日志级别字符串
func parseLogLevel(level string) logger.LogLevel {
	switch level {
	case "debug", "DEBUG":
		return logger.DEBUG
	case "info", "INFO":
		return logger.INFO
	case "warn", "WARN", "warning", "WARNING":
		return logger.WARN
	case "error", "ERROR":
		return logger.ERROR
	case "fatal", "FATAL":
		return logger.FATAL
	default:
		return logger.INFO // 默认级别
	}
}

// GetAdvancedMetrics 获取高级指标信息
func (h *Hub) GetAdvancedMetrics() *AdvancedMetrics {
	if h.metricsCollector == nil {
		return nil
	}

	metrics := h.metricsCollector.GetCurrentMetrics()
	return &metrics
}

// GetMetricsHistory 获取指标历史
func (h *Hub) GetMetricsHistory() []AdvancedMetrics {
	if h.metricsCollector == nil {
		return nil
	}

	return h.metricsCollector.GetHistory()
}

// EnableMetrics 启用指标收集
func (h *Hub) EnableMetrics() {
	if h.metricsCollector != nil {
		h.metricsCollector.Enable()
	}
}

// DisableMetrics 禁用指标收集
func (h *Hub) DisableMetrics() {
	if h.metricsCollector != nil {
		h.metricsCollector.Disable()
	}
}

// recordMessageLatency 记录消息延迟
func (h *Hub) recordMessageLatency(startTime time.Time) {
	if h.metricsCollector != nil {
		latency := time.Since(startTime)
		h.metricsCollector.RecordLatency(latency)
	}
}

// ========== 内存防护相关方法 ==========

// GetMemoryStats 获取内存统计信息
func (h *Hub) GetMemoryStats() map[string]interface{} {
	if h.memoryGuard == nil {
		return nil
	}
	return h.memoryGuard.GetMemoryStats()
}

// SetMemoryLimit 设置内存限制
func (h *Hub) SetMemoryLimit(limitMB int64) {
	if h.memoryGuard != nil {
		h.memoryGuard.SetMemoryLimit(limitMB)
	}
}

// ForceMemoryCleanup 强制执行内存清理
func (h *Hub) ForceMemoryCleanup() {
	if h.memoryGuard != nil {
		h.memoryGuard.ForceCleanup()
	}
}

// EnableMemoryGuard 启用内存防护
func (h *Hub) EnableMemoryGuard() {
	if h.memoryGuard != nil {
		h.memoryGuard.Enable()
	}
}

// DisableMemoryGuard 禁用内存防护
func (h *Hub) DisableMemoryGuard() {
	if h.memoryGuard != nil {
		h.memoryGuard.Disable()
	}
}

// IsMemoryGuardEnabled 检查内存防护是否启用
func (h *Hub) IsMemoryGuardEnabled() bool {
	if h.memoryGuard == nil {
		return false
	}
	return h.memoryGuard.IsEnabled()
}

// ========== 错误恢复相关方法 ==========

// ReportError 报告错误
func (h *Hub) ReportError(errorType ErrorType, severity ErrorSeverity, message string, context map[string]interface{}) string {
	if h.errorRecovery == nil {
		return ""
	}
	return h.errorRecovery.ReportError(errorType, severity, message, context)
}

// GetErrorHistory 获取错误历史
func (h *Hub) GetErrorHistory() map[string]*ErrorEntry {
	if h.errorRecovery == nil {
		return nil
	}
	return h.errorRecovery.GetErrorHistory()
}

// GetCircuitBreakerStates 获取熔断器状态
func (h *Hub) GetCircuitBreakerStates() map[ErrorType]CircuitState {
	if h.errorRecovery == nil {
		return nil
	}
	return h.errorRecovery.GetCircuitBreakerStates()
}

// GetErrorRecoveryStatistics 获取错误恢复统计信息
func (h *Hub) GetErrorRecoveryStatistics() map[string]interface{} {
	if h.errorRecovery == nil {
		return nil
	}
	return h.errorRecovery.GetStatistics()
}

// SetErrorRecoveryRetryConfig 设置错误恢复重试配置
func (h *Hub) SetErrorRecoveryRetryConfig(maxRetries int, interval time.Duration, backoffMultiple float64) {
	if h.errorRecovery != nil {
		h.errorRecovery.SetRetryConfig(maxRetries, interval, backoffMultiple)
	}
}

// SetCircuitBreakerConfig 设置熔断器配置
func (h *Hub) SetCircuitBreakerConfig(errorType ErrorType, failureThreshold, successThreshold int64, timeout time.Duration) {
	if h.errorRecovery != nil {
		h.errorRecovery.SetCircuitBreakerConfig(errorType, failureThreshold, successThreshold, timeout)
	}
}

// EnableErrorRecovery 启用错误恢复
func (h *Hub) EnableErrorRecovery() {
	if h.errorRecovery != nil {
		h.errorRecovery.Enable()
	}
}

// DisableErrorRecovery 禁用错误恢复
func (h *Hub) DisableErrorRecovery() {
	if h.errorRecovery != nil {
		h.errorRecovery.Disable()
	}
}

// IsErrorRecoveryEnabled 检查错误恢复是否启用
func (h *Hub) IsErrorRecoveryEnabled() bool {
	if h.errorRecovery == nil {
		return false
	}
	return h.errorRecovery.IsEnabled()
}

// AddRecoveryStrategy 添加自定义恢复策略
func (h *Hub) AddRecoveryStrategy(strategy RecoveryStrategy) {
	if h.errorRecovery != nil {
		h.errorRecovery.AddRecoveryStrategy(strategy)
	}
}

// ========== 配置验证相关方法 ==========

// ValidateConfig 验证当前配置
func (h *Hub) ValidateConfig() []ValidationResult {
	if h.configValidator == nil {
		return nil
	}
	return h.configValidator.Validate(h.config)
}

// GetConfigValidationReport 获取配置验证报告
func (h *Hub) GetConfigValidationReport() string {
	if h.configValidator == nil {
		return "配置验证器未初始化"
	}
	return h.configValidator.ValidateAndReport(h.config)
}

// AutoFixConfig 自动修复配置问题
func (h *Hub) AutoFixConfig() ([]ValidationResult, error) {
	if h.configValidator == nil {
		return nil, errorx.NewError(ErrTypeConfigValidatorNotInitialized)
	}
	return h.configValidator.AutoFix(h.config)
}

// AddValidationRule 添加自定义验证规则
func (h *Hub) AddValidationRule(rule ValidationRule) {
	if h.configValidator != nil {
		h.configValidator.AddRule(rule)
	}
}

// ============ 安全管理器公共API ============

// GetSecurityStats 获取安全统计信息
func (h *Hub) GetSecurityStats() SecurityStats {
	if h.securityManager == nil {
		return SecurityStats{}
	}
	return h.securityManager.GetSecurityStats()
}

// GetSecurityEvents 获取安全事件列表
func (h *Hub) GetSecurityEvents(limit int) []SecurityEvent {
	if h.securityManager == nil {
		return nil
	}
	return h.securityManager.GetSecurityEvents(limit)
}

// GenerateSecurityReport 生成安全报告
func (h *Hub) GenerateSecurityReport() string {
	if h.securityManager == nil {
		return "安全管理器未启用"
	}
	return h.securityManager.GenerateSecurityReport()
}

// AddSecurityAccessRule 添加访问规则
func (h *Hub) AddSecurityAccessRule(rule *AccessRule) {
	if h.securityManager != nil {
		h.securityManager.AddAccessRule(rule)
	}
}

// RemoveSecurityAccessRule 移除访问规则
func (h *Hub) RemoveSecurityAccessRule(ruleID string) {
	if h.securityManager != nil {
		h.securityManager.RemoveAccessRule(ruleID)
	}
}

// AddSecurityThreatPattern 添加威胁模式
func (h *Hub) AddSecurityThreatPattern(pattern *ThreatPattern) {
	if h.securityManager != nil {
		h.securityManager.AddThreatPattern(pattern)
	}
}

// RemoveSecurityThreatPattern 移除威胁模式
func (h *Hub) RemoveSecurityThreatPattern(patternID string) {
	if h.securityManager != nil {
		h.securityManager.RemoveThreatPattern(patternID)
	}
}

// AddToSecurityWhitelist 添加IP到白名单
func (h *Hub) AddToSecurityWhitelist(ip string) {
	if h.securityManager != nil {
		h.securityManager.AddToWhitelist(ip)
	}
}

// AddToSecurityBlacklist 添加IP到黑名单
func (h *Hub) AddToSecurityBlacklist(ip string) {
	if h.securityManager != nil {
		h.securityManager.AddToBlacklist(ip)
	}
}

// OnSecurityEvent 注册安全事件处理器
func (h *Hub) OnSecurityEvent(handler func(SecurityEvent)) {
	if h.securityManager != nil {
		h.securityManager.OnSecurityEvent(handler)
	}
}
