/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 11:20:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 11:20:00
 * @FilePath: \go-wsc\messaging\fake_host_test.go
 * @Description: 消息域测试基础设施 —— Host 替身与装配 helper
 *
 * 按既定约定：子包测试用局部替身，不构造真实 Hub。替身 = 嵌入接口（未覆盖的
 * 方法保留 nil，被调用即 panic，从而暴露遗漏的依赖）+ 只覆盖本包真正用到的方法。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/syncx"

	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// Host 替身
// ============================================================================

// fakeHost 嵌入 Host 接口，仅覆盖测试用到的方法。
// 任何未覆盖的方法调用会因嵌入的 nil 接口而 panic —— 这是刻意的：
// 它让「测试依赖了未声明的方法」立刻暴露，而不是静默返回零值。
type fakeHost struct {
	Host

	ctx    context.Context
	logger spi.Logger
	nodeID string
	config *wscconfig.WSC

	registry             *connection.ShardedRegistry
	messageSink          spi.MessageSink
	groupRepo            spi.GroupStore
	messageStatusUpdater *batcher.MessageStatusUpdater
	messageRecordOutbox  *batcher.MessageRecordOutbox

	// 接收方统计记账（TrackReceiverMessageStats 经 Host 端口上报，编排层转 stats 域）
	statMu       sync.Mutex
	trackedStats map[string]*trackedStat

	metrics   *overload.OverloadMetrics
	coalescer *overload.Coalescer // 高频合并器（nil=未启用；测试需覆盖高频分支时注入）
}

// trackedStat 单连接的接收统计累计
type trackedStat struct {
	messages int
	bytes    int
}

// findTrackedStat 查询指定连接的统计累计（线程安全）
func (f *fakeHost) findTrackedStat(connectionID string) (trackedStat, bool) {
	f.statMu.Lock()
	defer f.statMu.Unlock()
	e, ok := f.trackedStats[connectionID]
	if !ok {
		return trackedStat{}, false
	}
	return *e, true
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		ctx: context.Background(),
		// 日志器是消息链路的无条件依赖（投递/超时/转存路径都写日志），
		// 真实 Hub 在 NewHub 里必然填好；替身据此对齐构造后状态。
		logger:       spi.NewDefaultLogger(),
		config:       wscconfig.Default(),
		registry:     connection.NewShardedRegistry(false, false, connection.RegistryCapacity{}),
		trackedStats: make(map[string]*trackedStat),
		metrics:      &overload.OverloadMetrics{},
	}
}

func (f *fakeHost) Context() context.Context { return f.ctx }

func (f *fakeHost) GetLogger() spi.Logger { return f.logger }

func (f *fakeHost) GetNodeID() string { return f.nodeID }

func (f *fakeHost) GetConfig() *wscconfig.WSC { return f.config }

func (f *fakeHost) GetShardedRegistry() *connection.ShardedRegistry { return f.registry }

func (f *fakeHost) GetMessageSink() spi.MessageSink { return f.messageSink }

func (f *fakeHost) GetGroupRepo() spi.GroupStore { return f.groupRepo }

// GetMessageStatusUpdater 默认 nil（updateMessageStatusAsync 首个 nil 判断返回），
// 状态回报链路测试经 newStatusRecordingManager 注入真实批量更新器
func (f *fakeHost) GetMessageStatusUpdater() *batcher.MessageStatusUpdater {
	return f.messageStatusUpdater
}

// HasPubsub 单机模式：sendToUser 路径据此跳过跨节点广播兜底
func (f *fakeHost) HasPubsub() bool { return false }

// IsGRPCEnabled 单机模式：与 HasPubsub 共同构成跨节点通道开关
func (f *fakeHost) IsGRPCEnabled() bool { return false }

// GetMessageRecordOutbox 默认 nil（recordMessageToDatabase 据此降级 workerPool 单条 Create 路径）
func (f *fakeHost) GetMessageRecordOutbox() *batcher.MessageRecordOutbox {
	return f.messageRecordOutbox
}

// BatchGetUserNodes 单机模式无路由索引，返回 nil（本地 miss 即判定离线，扇出预取据此回退逐用户路径）
func (f *fakeHost) BatchGetUserNodes(_ context.Context, _ []string) map[string][]string { return nil }

// CheckAndRouteToNode 单机模式无跨节点路由（sendToUser 的分布式分支据此走本地投递）
func (f *fakeHost) CheckAndRouteToNode(_ context.Context, _ string, _ *models.HubMessage, _ []string) (bool, []string, error) {
	return false, nil, nil
}

// GetObserverNotifier 未注入观察者通知批处理器（NotifyObservers 攒批入口据此 no-op）
func (f *fakeHost) GetObserverNotifier() *batcher.ObserverNotificationBatcher { return nil }

// DeleteRerouteGuard makeAckTimeoutCallback 无条件终态清理，no-op
func (f *fakeHost) DeleteRerouteGuard(_ string) {}

// MarkRerouteAttempted doOfflineBroadcast 记录投递目标，no-op
func (f *fakeHost) MarkRerouteAttempted(_ string, _ []string, _ bool) {}

func (f *fakeHost) GetAllClusterNodeIDs() []string { return nil }

func (f *fakeHost) GetAdmissionLevel() overload.OverloadLevel { return overload.LevelNormal }

// GetEphemeralCoalescer 默认未启用合并器（SendToClientSerialized 高频级分支据此跳过）；
// 测试可通过赋值 f.coalescer 注入非 nil 合并器以覆盖高频 latest-wins 分支
func (f *fakeHost) GetEphemeralCoalescer() *overload.Coalescer { return f.coalescer }

// GetBroadcastShaper 未启用出向整形（BroadcastToFiltered/BroadcastToUserIDs 的 shaper 分支据此跳过）
func (f *fakeHost) GetBroadcastShaper() *overload.Shaper { return nil }

// GetStatsRepo 未注入统计仓储（handleDirectMessage 的 msgSentCount 分支据此跳过）
func (f *fakeHost) GetStatsRepo() spi.HubStats { return nil }

// HandleHeartbeat 心跳 no-op（连接域时间轮续期由连接层测试自行覆盖）
func (f *fakeHost) HandleHeartbeat(_ *models.Client) {}

// AdmitMessage 消息级准入恒放行（闸门由过载域测试自行覆盖）
func (f *fakeHost) AdmitMessage(_ *models.HubMessage, _ bool) overload.AdmitVerdict {
	return overload.VerdictAdmit
}

// AdmissionOnDelivered 投递完成埋点 no-op
func (f *fakeHost) AdmissionOnDelivered() {}

// GetOverloadMetrics 零值指标器（全 atomic 无锁，可直接 Record）
func (f *fakeHost) GetOverloadMetrics() *overload.OverloadMetrics { return f.metrics }

// TrackReceiverMessageStats 接收方统计记账（断言投递方完成了 Host 端口上报）
func (f *fakeHost) TrackReceiverMessageStats(connectionID string, _ models.UserType, dataSize int) {
	f.statMu.Lock()
	defer f.statMu.Unlock()
	e := f.trackedStats[connectionID]
	if e == nil {
		e = &trackedStat{}
		f.trackedStats[connectionID] = e
	}
	e.messages++
	e.bytes += dataSize
}

// ============================================================================
// Manager 装配 helper
// ============================================================================

// newTestManager 构造最小消息域 Manager（不启动后台组件，不注入时间轮）
func newTestManager() (*Manager, *fakeHost) {
	host := newFakeHost()
	m := NewManager(host)
	// 默认注入 ID 生成器：真实编排层恒注入雪花生成器，空 ID 消息的补全/重试路径均依赖它
	// （Manager 契约：idGenerator 为 nil 时需调用方保证 msg.ID 非空，测试统一走注入语义）
	m.WithIDGenerator(&fakeIDGenerator{})
	return m, host
}

// newAckTimeoutTestManager 构造注入 ACK 超时时间轮与离线转存记账的 Manager
// （覆盖 ack_timer.go / node_ack_timeout.go 的测试装配需求）
func newAckTimeoutTestManager() (*Manager, *fakeHost, *offlineCallLog) {
	m, host := newTestManager()
	m.ackTimeoutTimer = syncx.NewHashedWheelTimer()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline
	return m, host, log
}

// ============================================================================
// 离线转存记账桩（经真实 HybridOfflineMessageHandler 验证双写链路）
// ============================================================================

// offlineCallLog 离线转存记账：Enqueue 到达即认为发生一次转存
// （StoreOfflineMessage 双写并行执行，仅 Redis 侧计数保持「一次转存 = 1」）
type offlineCallLog struct {
	mu    sync.Mutex
	calls int32
	keys  []string // Enqueue 队列 key（appID:ns:group:userID，离线维度断言用）
}

func (l *offlineCallLog) record(key string) {
	atomic.AddInt32(&l.calls, 1)
	l.mu.Lock()
	l.keys = append(l.keys, key)
	l.mu.Unlock()
}

func (l *offlineCallLog) getStoreCalled() int { return int(atomic.LoadInt32(&l.calls)) }

// lastKeyDimension 返回最近一次转存的 (namespace, groupID) 队列维度
func (l *offlineCallLog) lastKeyDimension() (ns, groupID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.keys) == 0 {
		return "", ""
	}
	// key = appID:ns:group:userID，ns 与 group 段即离线存储维度
	parts := strings.Split(l.keys[len(l.keys)-1], ":")
	if len(parts) >= 4 {
		return parts[1], parts[2]
	}
	return "", ""
}

// offlineCallQueue 计数 Enqueue 的队列桩（其余方法 no-op）
type offlineCallQueue struct{ log *offlineCallLog }

func (q *offlineCallQueue) Enqueue(_ context.Context, key string, _ *models.HubMessage) error {
	q.log.record(key)
	return nil
}
func (q *offlineCallQueue) Dequeue(context.Context, string, time.Duration) (*models.HubMessage, error) {
	return nil, nil
}
func (q *offlineCallQueue) DequeueBatch(context.Context, string, int) ([]*models.HubMessage, error) {
	return nil, nil
}
func (q *offlineCallQueue) GetLength(context.Context, string) (int64, error) { return 0, nil }
func (q *offlineCallQueue) Clear(context.Context, string) error              { return nil }
func (q *offlineCallQueue) Peek(context.Context, string) (*models.HubMessage, error) {
	return nil, nil
}

var _ spi.MessageQueue = (*offlineCallQueue)(nil)

// offlineCallStore 不计数的存储桩（双写另一侧，仅满足 NewHybridOfflineMessageHandler 必需参数）
type offlineCallStore struct{}

func (s *offlineCallStore) Save(context.Context, *models.OfflineMessageRecord) error {
	return nil
}
func (s *offlineCallStore) BatchSave(context.Context, []*models.OfflineMessageRecord) error {
	return nil
}
func (s *offlineCallStore) QueryMessages(context.Context, *spi.OfflineMessageFilter) ([]*models.OfflineMessageRecord, error) {
	return nil, nil
}
func (s *offlineCallStore) DeleteByMessageIDs(context.Context, string, string, string, []string) error {
	return nil
}
func (s *offlineCallStore) GetCountByReceiver(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (s *offlineCallStore) GetCountBySender(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (s *offlineCallStore) ClearByReceiver(context.Context, string, string, string) error { return nil }
func (s *offlineCallStore) DeleteExpired(context.Context) (int64, error)                  { return 0, nil }
func (s *offlineCallStore) UpdatePushStatus(context.Context, []string, models.MessageSendStatus, string) error {
	return nil
}
func (s *offlineCallStore) CleanupOld(context.Context, time.Time) (int64, error) { return 0, nil }
func (s *offlineCallStore) Close() error                                         { return nil }

var _ spi.OfflineStore = (*offlineCallStore)(nil)

// newOfflineRecordingHandler 构造带转存记账的真实混合离线处理器
func newOfflineRecordingHandler() (*HybridOfflineMessageHandler, *offlineCallLog) {
	log := &offlineCallLog{}
	handler := NewHybridOfflineMessageHandler(
		&offlineCallQueue{log: log},
		&offlineCallStore{},
		wscconfig.DefaultOfflineMessage(),
		spi.NewDefaultLogger(),
	)
	return handler, log
}

// ============================================================================
// 群组仓储替身（messaging 群组投递路径测试用）
// ============================================================================

// fakeGroupStore 内存群组仓储：仅实现成员管理读写，其余查询 no-op
type fakeGroupStore struct {
	mu      sync.Mutex
	members map[string]map[string]struct{} // key: appID:ns:groupID → 成员集合
}

func newFakeGroupStore() *fakeGroupStore {
	return &fakeGroupStore{members: make(map[string]map[string]struct{})}
}

func (g *fakeGroupStore) groupKey(appID, namespace, groupID string) string {
	return appID + ":" + namespace + ":" + groupID
}

func (g *fakeGroupStore) CreateGroup(_ context.Context, _ *models.Group) error { return nil }

func (g *fakeGroupStore) GetGroup(_ context.Context, _, _, _ string) (*models.Group, error) {
	return nil, nil
}

func (g *fakeGroupStore) DisbandGroup(_ context.Context, _, _, _ string) error { return nil }

func (g *fakeGroupStore) AddMembers(_ context.Context, appID, namespace, groupID string, userIDs []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := g.groupKey(appID, namespace, groupID)
	set := g.members[key]
	if set == nil {
		set = make(map[string]struct{})
		g.members[key] = set
	}
	for _, uid := range userIDs {
		set[uid] = struct{}{}
	}
	return nil
}

func (g *fakeGroupStore) RemoveMembers(_ context.Context, _, _, _ string, _ []string) error {
	return nil
}

func (g *fakeGroupStore) GetMembers(_ context.Context, appID, namespace, groupID string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set := g.members[g.groupKey(appID, namespace, groupID)]
	ids := make([]string, 0, len(set))
	for uid := range set {
		ids = append(ids, uid)
	}
	return ids, nil
}

// GetMultiGroupMembers 批量获取群组成员（跨 ns 聚合，与 Redis 实现语义一致）
// 同一 gid 可在多个 ns 下各建实例（key=appID:ns:gid），此处按 (appID, gid) 聚合所有实例成员并去重
func (g *fakeGroupStore) GetMultiGroupMembers(_ context.Context, appID string, groupIDs []string) (map[string][]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	result := make(map[string][]string, len(groupIDs))
	for _, gid := range groupIDs {
		merged := make(map[string]struct{})
		for key, set := range g.members {
			parts := strings.Split(key, ":")
			if len(parts) != 3 || parts[0] != appID || parts[2] != gid {
				continue
			}
			for uid := range set {
				merged[uid] = struct{}{}
			}
		}
		if len(merged) > 0 {
			ids := make([]string, 0, len(merged))
			for uid := range merged {
				ids = append(ids, uid)
			}
			result[gid] = ids
		}
	}
	return result, nil
}

func (g *fakeGroupStore) GetUserGroups(_ context.Context, _, _, _ string) ([]string, error) {
	return nil, nil
}

func (g *fakeGroupStore) IsMember(_ context.Context, _, _, _, _ string) (bool, error) {
	return false, nil
}

func (g *fakeGroupStore) GetMemberCount(_ context.Context, _, _, _ string) (int64, error) {
	return 0, nil
}

func (g *fakeGroupStore) GetNamespaceGroups(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

func (g *fakeGroupStore) GetAllNamespaces(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (g *fakeGroupStore) EnsureSystemGroup(_ context.Context, _, _, _ string) error {
	return nil
}

func (g *fakeGroupStore) SetInvalidateNotifier(_ func(appID, groupID string)) {}

func (g *fakeGroupStore) InvalidateTopology(_, _ string) {}

var _ spi.GroupStore = (*fakeGroupStore)(nil)

// ============================================================================
// 消息构造 helper（自旧 hub 测试迁入，类型加 models 前缀）
// ============================================================================

// makeGroupMessage 构造基础文本消息（send/ack/timeout 测试共用）
func makeGroupMessage(sender string) *models.HubMessage {
	msg := models.NewHubMessage()
	msg.Sender = sender
	msg.MessageType = models.MessageTypeText
	msg.Content = "hello group"
	msg.CreateAt = time.Now()
	return msg
}

// makeTestClient 构造测试客户端（与 models.NewClient 默认值对齐，ClientMatchesEnvelope 严格匹配要求）
// opts: [0]=Namespace（多 namespace 场景覆盖）、[1]=GroupID
func makeTestClient(clientID, userID string, opts ...string) *models.Client {
	c := &models.Client{
		ID:          clientID,
		UserID:      userID,
		UserType:    models.UserTypeCustomer,
		Role:        models.UserRoleCustomer,
		Status:      models.UserStatusOnline,
		LastSeen:    time.Now(),
		SendChan:    make(chan []byte, 16),
		Context:     context.WithValue(context.Background(), ContextKeyUserID, userID),
		ConnectedAt: time.Now(),
		AppID:       constants.DefaultAppID,
		Namespace:   constants.DefaultNamespace,
	}
	if len(opts) >= 1 {
		c.Namespace = opts[0]
	}
	if len(opts) >= 2 {
		c.GroupID = opts[1]
	}
	return c
}

// makeSSEClient 构造 SSE 连接客户端（ConnectionType=SSE，含 SSEMessageCh）
func makeSSEClient(clientID, userID string) *models.Client {
	c := makeTestClient(clientID, userID)
	c.ConnectionType = models.ConnectionTypeSSE
	c.SSEMessageCh = make(chan *models.HubMessage, 16)
	return c
}

// makeAgentClient 构造客服客户端（UserType=Agent）
func makeAgentClient(clientID, userID string) *models.Client {
	c := makeTestClient(clientID, userID)
	c.UserType = models.UserTypeAgent
	return c
}

// makeVIPClient 构造指定 VIP 等级的客户端
func makeVIPClient(clientID, userID string, level models.VIPLevel) *models.Client {
	c := makeTestClient(clientID, userID)
	c.SetVIPLevel(level)
	return c
}

// fakeIDGenerator ID 生成器替身（normalizeMessageFields 的雪花 ID 分支依赖）
type fakeIDGenerator struct {
	seq atomic.Int32
}

func (g *fakeIDGenerator) GenerateTraceID() string       { return "trace-fake" }
func (g *fakeIDGenerator) GenerateSpanID() string        { return "span-fake" }
func (g *fakeIDGenerator) GenerateRequestID() string     { return fmt.Sprintf("req-%d", g.seq.Add(1)) }
func (g *fakeIDGenerator) GenerateCorrelationID() string { return "corr-fake" }

var _ models.IDGenerator = (*fakeIDGenerator)(nil)

// ============================================================================
// 状态回报链路装配（write-ahead 记录 + 状态批量回报可观测）
// ============================================================================

// statusUpdateWriter StorageBatchWriter 测试适配器：消息记录仓储走 fakeMessageRecordRepo，
// 连接类仓储用不到（MessageStatusUpdater flush 只触达 GetMessageRecordRepo），返回 nil
type statusUpdateWriter struct {
	host *fakeHost
	repo spi.MessageSink
}

func (w statusUpdateWriter) Context() context.Context                     { return w.host.Context() }
func (w statusUpdateWriter) GetLogger() spi.Logger                        { return w.host.GetLogger() }
func (w statusUpdateWriter) GetConnectionRecordRepo() spi.ConnectionStore { return nil }
func (w statusUpdateWriter) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	return nil
}
func (w statusUpdateWriter) GetMessageRecordRepo() spi.MessageSink { return w.repo }

var _ batcher.StorageBatchWriter = statusUpdateWriter{}

// newStatusRecordingManager 构造带状态回报链路的 Manager：
//   - workerPool：write-ahead 记录经 RecordPool 异步创建（recordMessageToDatabase 依赖）
//   - MessageStatusUpdater：updateMessageStatusAsync 的状态回报经批量更新器落 repo（20ms flush）
//
// cleanup 由调用方 defer 执行（停更新器 + 工作池 + 时间轮）
func newStatusRecordingManager() (*Manager, *fakeHost, *fakeMessageRecordRepo, func()) {
	m, host := newTestManager()

	wp := NewHubWorkerPool(host.GetConfig().WorkerPool, host.GetLogger())
	m.WithWorkerPool(wp)

	repo := &fakeMessageRecordRepo{}
	host.messageSink = repo

	updater := batcher.NewMessageStatusUpdater(statusUpdateWriter{host: host, repo: repo}, 64, 8, 20*time.Millisecond)
	host.messageStatusUpdater = updater

	// outbox 恒注入（与编排层对齐）：write-ahead 记录经攒批批量落库
	outbox := batcher.NewMessageRecordOutbox(host, 64, 8, 5*time.Millisecond)
	host.messageRecordOutbox = outbox

	cleanup := func() {
		outbox.Stop()
		updater.Stop()
		wp.Stop()
		m.Stop()
	}
	return m, host, repo, cleanup
}
