/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 11:05:07
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 11:05:07
 * @FilePath: \go-wsc\hub\self_heal_test.go
 * @Description: 跨节点死索引自愈测试 — 重路由守卫 / user_not_found 决策 / 幽灵连接回收
 *
 * 重复覆盖盘点（对照 dead_node_probe_test.go）：
 *   dead_node_probe_test.go 覆盖的是 publishToTargetedNodes 的死节点异步探测
 *   （生产代码在 cluster_dispatch.go，含 probeDeadNodeAsync 重试发布），
 *   仅借用 models.OperationTypeClientReclaim 作为控制消息类型，
 *   未触达 self_heal.go 的任何函数 —— self_heal.go 全量为缺口，此处整体补齐：
 *
 *   - 重路由守卫：markRerouteAttempted/markRerouteRejected 记账语义、
 *     TTL 懒过期重置（死索引判定与 TTL 关系）、sweepRerouteGuard 清扫
 *     （含 check-then-delete 竞态修复回归：误删刚续期条目）
 *   - user_not_found 决策：decideUserNotFoundReroute 三分支
 *     （迁移重路由 / 全拒转离线 / 查询失败保留 ACK 兜底）与
 *     handleDistributedUserNotFound 入参校验、replyUserNotFound 回告发布
 *   - 死索引清理：selfHealDeadIndexEntries（仅清指向本节点条目 + 二次确认 + 异步执行）
 *   - 幽灵连接回收：detectClientMigration / notifyClientReclaim /
 *     handleDistributedClientReclaim（时序守卫）
 *   - 自愈循环启停与清扫周期：lifecycle.go 的 EventLoop 以 90s 周期调
 *     sweepRerouteGuard（常量守护用例锁定），完整 Run() 事件循环不在此重复驱动
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 构造 helper 与仓储桩
// ============================================================================

// selfHealOnlineRepo 在线状态仓储桩（embed 接口法：未覆盖方法调用即 panic）：
// 可编程返回索引条目/节点列表/归属节点，并记录批量下线调用
type selfHealOnlineRepo struct {
	spi.OnlineStore
	mu           sync.Mutex
	clients      []*models.Client // GetUserClients 返回值
	nodes        []string         // GetUserNodes 返回值
	owner        string           // GetClientOwner 返回值
	clientsErr   error            // GetUserClients 注入错误
	nodesErr     error            // GetUserNodes 注入错误
	offlineErr   error            // BatchSetClientsOfflineWithInfo 注入错误
	offlineCalls [][]*models.Client
}

func (f *selfHealOnlineRepo) GetUserClients(_ context.Context, _ string) ([]*models.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clients, f.clientsErr
}

func (f *selfHealOnlineRepo) GetUserNodes(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes, f.nodesErr
}

func (f *selfHealOnlineRepo) GetClientOwner(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owner, nil
}

func (f *selfHealOnlineRepo) BatchSetClientsOfflineWithInfo(_ context.Context, clients []*models.Client) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offlineErr != nil {
		return f.offlineErr
	}
	f.offlineCalls = append(f.offlineCalls, append([]*models.Client(nil), clients...))
	return nil
}

// offlineCallCount 返回已发生的批量下线调用次数
func (f *selfHealOnlineRepo) offlineCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.offlineCalls)
}

// newSelfHealTestHub 构造自愈测试 Hub：nodeID 固定为 node-a，
// 单机模式（无 PubSub 且未启用 node-grpc），onlineStatusRepo 挂桩（可传 nil）
func newSelfHealTestHub(repo spi.OnlineStore) *Hub {
	hub := NewHub(wscconfig.Default())
	hub.nodeID = "node-a"
	hub.onlineStatusRepo = repo
	return hub
}

// newSelfHealClusterHub 构造接入 miniredis PubSub 的自愈测试 Hub
// （replyUserNotFound/notifyClientReclaim/重路由补投等发布路径依赖）
func newSelfHealClusterHub(t *testing.T, repo spi.OnlineStore) (*Hub, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := NewHub(wscconfig.Default())
	hub.nodeID = "node-a"
	hub.onlineStatusRepo = repo
	hub.pubsub = cachex.NewPubSub(redisClient)
	return hub, mr, redisClient
}

// loadRerouteGuardEntry 读取守卫条目（不存在即失败终止）
func loadRerouteGuardEntry(t *testing.T, hub *Hub, messageID string) *rerouteGuardEntry {
	t.Helper()
	v, ok := hub.rerouteGuard.Load(messageID)
	require.True(t, ok, "守卫条目 %s 应存在", messageID)
	entry, ok := v.(*rerouteGuardEntry)
	require.True(t, ok)
	return entry
}

// expireRerouteGuardEntry 将守卫条目人工拨为过期（模拟 TTL 到期，持锁写）
func expireRerouteGuardEntry(t *testing.T, hub *Hub, messageID string) {
	t.Helper()
	entry := loadRerouteGuardEntry(t, hub, messageID)
	entry.mu.Lock()
	entry.expiresAt = time.Now().Add(-time.Second)
	entry.mu.Unlock()
}

// nodeSet 构造节点集合（断言辅助）
func nodeSet(nodes ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		set[n] = struct{}{}
	}
	return set
}

// subscribeNodeChannel 订阅节点专属频道（ResolveChannel 对齐发布侧物理频道名）
func subscribeNodeChannel(t *testing.T, hub *Hub, redisClient *redis.Client, nodeID string) *redis.PubSub {
	t.Helper()
	channel := hub.pubsub.ResolveChannel(
		hub.config.RedisRepository.PubSub.GetNodeChannelPrefix() + nodeID)
	sub := redisClient.Subscribe(context.Background(), channel)
	_, err := sub.ReceiveTimeout(context.Background(), time.Second)
	require.NoError(t, err, "订阅节点 %s 频道应建立成功", nodeID)
	return sub
}

// receiveNodeChannelPayload 等待并反序列化节点频道上收到的分布式消息
func receiveNodeChannelPayload(t *testing.T, sub *redis.PubSub) *models.DistributedMessage {
	t.Helper()
	select {
	case msg := <-sub.Channel():
		distMsg, err := wscpb.UnmarshalDistributedMessage([]byte(msg.Payload))
		require.NoError(t, err, "发布载荷应为 DistributedMessageProto 编码")
		return distMsg
	case <-time.After(2 * time.Second):
		t.Fatal("节点频道未在超时内收到发布")
		return nil
	}
}

// ============================================================================
// 重路由守卫 —— 记账与 TTL 语义
// ============================================================================

// TestRerouteGuardConstants 语义守护：TTL 覆盖 ACK 超时终态最大窗口，
// 清扫周期短于 TTL（过期条目最迟 TTL + 一个周期内回收，广播兜底条目仅靠清扫）
func TestRerouteGuardConstants(t *testing.T) {
	assert.Equal(t, 2*time.Minute, rerouteGuardTTL,
		"守卫 TTL 应覆盖消息发出到 ACK 超时终态的最大窗口")
	assert.Equal(t, 90*time.Second, rerouteGuardSweepInterval,
		"清扫周期应为 90s（防长期驻留内存泄漏）")
	assert.Less(t, rerouteGuardSweepInterval, rerouteGuardTTL,
		"清扫周期应短于 TTL，保证过期条目及时回收")
}

// TestMarkRerouteAttemptedAccounting 记账语义：本节点与空节点不记账、
// p2p 标记只增不减（广播兜底后 P2P 重试以 P2P 语义为准）、空 messageID 不建条目
func TestMarkRerouteAttemptedAccounting(t *testing.T) {
	hub := newSelfHealTestHub(nil)

	hub.markRerouteAttempted("m-acc-1", []string{"node-b", "node-c", "node-a", ""}, true)
	entry := loadRerouteGuardEntry(t, hub, "m-acc-1")
	entry.mu.Lock()
	assert.Equal(t, nodeSet("node-b", "node-c"), entry.attempted,
		"本节点与空节点不应计入已尝试集合")
	assert.True(t, entry.p2p)
	entry.mu.Unlock()

	// p2p 只增不减：广播兜底（p2p=false）先建条目，P2P 重试后升级
	hub.markRerouteAttempted("m-acc-2", []string{"node-b"}, false)
	hub.markRerouteAttempted("m-acc-2", []string{"node-c"}, true)
	entry2 := loadRerouteGuardEntry(t, hub, "m-acc-2")
	entry2.mu.Lock()
	assert.True(t, entry2.p2p, "P2P 重试后条目应保持 P2P 语义")
	entry2.mu.Unlock()

	// 空 messageID 不建条目
	hub.markRerouteAttempted("", []string{"node-b"}, true)
	_, exists := hub.rerouteGuard.Load("")
	assert.False(t, exists, "空 messageID 不应建守卫条目")
}

// TestRerouteGuardLazyExpiryReset 死索引判定与 TTL 关系（懒过期重置）：
// 条目过期后再次记账视为新消息 —— 旧记忆清零防串台、TTL 重新续期；
// markRerouteRejected 的过期重置取保守 p2p=false（不主动转离线）
func TestRerouteGuardLazyExpiryReset(t *testing.T) {
	hub := newSelfHealTestHub(nil)

	// markRerouteAttempted 过期重置
	hub.markRerouteAttempted("m-ttl-1", []string{"node-old"}, true)
	expireRerouteGuardEntry(t, hub, "m-ttl-1")
	hub.markRerouteAttempted("m-ttl-1", []string{"node-new"}, true)

	entry := loadRerouteGuardEntry(t, hub, "m-ttl-1")
	entry.mu.Lock()
	assert.Equal(t, nodeSet("node-new"), entry.attempted,
		"过期条目视为新消息，旧 attempted 记忆应清零（防同 messageID 串台）")
	assert.True(t, time.Now().Before(entry.expiresAt), "过期后记账应重新续期 TTL")
	entry.mu.Unlock()

	// markRerouteRejected 过期重置：p2p 保守 false
	hub.markRerouteRejected("m-ttl-2", "node-x")
	expireRerouteGuardEntry(t, hub, "m-ttl-2")
	hub.markRerouteRejected("m-ttl-2", "node-new")

	entry2 := loadRerouteGuardEntry(t, hub, "m-ttl-2")
	entry2.mu.Lock()
	assert.Equal(t, nodeSet("node-new"), entry2.rejected, "过期重置后仅保留本次拒绝节点")
	assert.Empty(t, entry2.attempted, "过期重置应清空已尝试集合")
	assert.False(t, entry2.p2p, "过期条目按保守语义处理（不主动转离线）")
	entry2.mu.Unlock()
}

// ============================================================================
// sweepRerouteGuard —— 周期清扫与 check-then-delete 语义
// ============================================================================

// TestSweepRerouteGuardSemantics 清扫语义：过期条目回收、活跃条目保留、
// 刚续期条目（过期后经懒重置续期）不误删且记忆保留
func TestSweepRerouteGuardSemantics(t *testing.T) {
	hub := newSelfHealTestHub(nil)

	// 活跃条目：TTL 窗口内不清扫
	hub.markRerouteAttempted("m-live", []string{"node-b"}, true)

	// 过期条目：清扫回收
	hub.markRerouteAttempted("m-dead", []string{"node-b"}, true)
	expireRerouteGuardEntry(t, hub, "m-dead")

	// 刚续期条目：过期后被再次记账（懒重置续期）→ 活跃守卫
	hub.markRerouteAttempted("m-renewed", []string{"node-old"}, true)
	expireRerouteGuardEntry(t, hub, "m-renewed")
	hub.markRerouteAttempted("m-renewed", []string{"node-new"}, false)

	hub.sweepRerouteGuard()

	_, deadExists := hub.rerouteGuard.Load("m-dead")
	assert.False(t, deadExists, "过期条目应被清扫回收")
	_, liveExists := hub.rerouteGuard.Load("m-live")
	assert.True(t, liveExists, "活跃条目不得误删")
	_, renewedExists := hub.rerouteGuard.Load("m-renewed")
	assert.True(t, renewedExists, "刚续期条目不得误删（check-then-delete 修复语义）")

	// 续期条目的防重复记忆保留（误删会导致同消息重复投递）
	renewed := loadRerouteGuardEntry(t, hub, "m-renewed")
	renewed.mu.Lock()
	assert.Equal(t, nodeSet("node-new"), renewed.attempted, "续期条目的记账记忆应完整保留")
	renewed.mu.Unlock()
}

// TestSweepRerouteGuardConcurrentRenewal check-then-delete 竞态回归（-race）：
// 清扫与懒续期记账并发竞争同一热点 messageID。修复前（读到过期即无条件 Delete）
// 会误删"首读过期与删除执行窗口内刚续期"的活跃守卫，已尝试节点记忆丢失、
// 防重复投递/防 ping-pong 防线失效；修复后 LoadAndDelete 持锁复核、未过期放回。
// 竞争窗口为纳秒级无法确定性复现，此处以高强度交错驱动放回分支 +
// 终态完整性断言（-race 同时守护数据竞争）
func TestSweepRerouteGuardConcurrentRenewal(t *testing.T) {
	hub := newSelfHealTestHub(nil)
	const hotKey = "m-hot-race"
	const iterations = 200

	var wg sync.WaitGroup
	// 记账侧：反复记账后立刻拨回"刚过期"边界，持续制造清扫与续期的交错窗口
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			hub.markRerouteAttempted(hotKey, []string{"node-b"}, true)
			if v, ok := hub.rerouteGuard.Load(hotKey); ok {
				e := v.(*rerouteGuardEntry)
				e.mu.Lock()
				e.expiresAt = time.Now().Add(-time.Millisecond)
				e.mu.Unlock()
			}
		}
	}()
	// 清扫侧：并发反复清扫
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				hub.sweepRerouteGuard()
			}
		}()
	}
	wg.Wait()

	// 终态校验：竞争静止后重新记账（活跃条目），清扫不得吞掉活跃守卫及其记忆
	hub.markRerouteAttempted(hotKey, []string{"node-final"}, true)
	hub.sweepRerouteGuard()

	entry := loadRerouteGuardEntry(t, hub, hotKey)
	entry.mu.Lock()
	_, hasFinal := entry.attempted["node-final"]
	entry.mu.Unlock()
	assert.True(t, hasFinal, "终态记账的节点记忆不得丢失（活跃守卫不被清扫吞掉）")
}

// ============================================================================
// user_not_found 回告处理（发送节点侧）
// ============================================================================

// TestHandleDistributedUserNotFoundValidation 入参校验：
// 缺消息体报错；空 TargetID / 空 MessageID 静默跳过
func TestHandleDistributedUserNotFoundValidation(t *testing.T) {
	hub := newSelfHealTestHub(nil)
	ctx := context.Background()

	err := hub.handleDistributedUserNotFound(ctx, &models.DistributedMessage{TargetID: "u-1"})
	assert.Error(t, err, "缺少原始消息体应报错")

	msg := models.NewHubMessage()
	require.NoError(t, hub.handleDistributedUserNotFound(ctx,
		&models.DistributedMessage{TargetID: "u-1", Message: msg}), "空 MessageID 静默跳过")

	msg.MessageID = "m-unf"
	require.NoError(t, hub.handleDistributedUserNotFound(ctx,
		&models.DistributedMessage{Message: msg}), "空 TargetID 静默跳过")
}

// TestDecideUserNotFoundRerouteMigratedToNewNode 用户已迁移分支（miniredis 真实链路）：
// 重查在线索引出现"从未尝试过的新节点" → 定向重路由补投（PubSub 发布到新节点频道），
// 返回 true（调用方跳过广播兜底防重复投递），候选节点先记账再投递
func TestDecideUserNotFoundRerouteMigratedToNewNode(t *testing.T) {
	repo := &selfHealOnlineRepo{nodes: []string{"node-new"}}
	hub, _, redisClient := newSelfHealClusterHub(t, repo)
	defer func() {
		_ = hub.pubsub.Close()
		_ = redisClient.Close()
	}()

	// 先订阅新节点频道（重路由补投的定向发布目的地）
	sub := subscribeNodeChannel(t, hub, redisClient, "node-new")
	defer func() { _ = sub.Close() }()

	msg := models.NewHubMessage().SetMessageType(models.MessageTypeText).SetReceiver("u-mig")
	msg.MessageID = "m-mig"
	msg.AppID = constants.DefaultAppID
	msg.Namespace = constants.DefaultNamespace

	// 模拟首次 P2P 定向投递已尝试 node-old
	hub.markRerouteAttempted(msg.MessageID, []string{"node-old"}, true)

	rerouted := hub.decideUserNotFoundReroute(context.Background(), msg, "u-mig",
		"node-old", constants.DefaultAppID, constants.DefaultNamespace)

	assert.True(t, rerouted, "用户已迁移到新节点时应定向补投")

	// 新节点频道收到补投消息：类型 send_message、目标用户正确、来源为本节点
	distMsg := receiveNodeChannelPayload(t, sub)
	assert.Equal(t, models.OperationTypeSendMessage, distMsg.Type)
	assert.Equal(t, "u-mig", distMsg.TargetID)
	assert.Equal(t, "node-a", distMsg.NodeID)
	assert.Equal(t, msg.MessageID, distMsg.Message.GetMessageID())

	// 先记账再投递：候选节点已计入 attempted（防回告先于记账引发同节点二次投递）
	entry := loadRerouteGuardEntry(t, hub, msg.MessageID)
	entry.mu.Lock()
	assert.Equal(t, nodeSet("node-old", "node-new"), entry.attempted,
		"重路由候选节点应先记账再投递")
	entry.mu.Unlock()
}

// TestDecideUserNotFoundRerouteAllRejected 全拒分支 + 兜底语义：
//   - 全部已尝试节点均回告不存在 + P2P 路径 → 立即转离线并终态清理守卫
//   - 广播兜底路径（p2p=false）→ 仅静默结束，守卫保留（离线已预存，防重复）
//   - 索引查询失败 → 保留 ACK 超时兜底（不转离线、不清守卫）
//   - onlineStatusRepo 未注入 → 直接 false
func TestDecideUserNotFoundRerouteAllRejected(t *testing.T) {
	ctx := context.Background()
	appID, ns := constants.DefaultAppID, constants.DefaultNamespace

	t.Run("全拒且P2P立即转离线并清理守卫", func(t *testing.T) {
		repo := &selfHealOnlineRepo{nodes: []string{"node-old"}}
		hub := newSelfHealTestHub(repo)

		msg := models.NewHubMessage().SetReceiver("u-p2p")
		msg.MessageID = "m-p2p"
		hub.markRerouteAttempted(msg.MessageID, []string{"node-old"}, true)

		rerouted := hub.decideUserNotFoundReroute(ctx, msg, "u-p2p", "node-old", appID, ns)
		assert.False(t, rerouted, "无新节点可路由时不补投")

		_, exists := hub.rerouteGuard.Load(msg.MessageID)
		assert.False(t, exists, "全拒 + P2P 转离线后守卫条目应终态清理")
	})

	t.Run("广播兜底路径仅静默结束", func(t *testing.T) {
		repo := &selfHealOnlineRepo{nodes: []string{"node-old"}}
		hub := newSelfHealTestHub(repo)

		msg := models.NewHubMessage().SetReceiver("u-bc")
		msg.MessageID = "m-bc-fallback"
		hub.markRerouteAttempted(msg.MessageID, []string{"node-old"}, false) // 广播兜底：离线已预存

		rerouted := hub.decideUserNotFoundReroute(ctx, msg, "u-bc", "node-old", appID, ns)
		assert.False(t, rerouted)

		_, exists := hub.rerouteGuard.Load(msg.MessageID)
		assert.True(t, exists, "广播兜底路径守卫不终态清理（由周期清扫回收）")
	})

	t.Run("索引查询失败保留ACK兜底", func(t *testing.T) {
		repo := &selfHealOnlineRepo{nodesErr: assert.AnError}
		hub := newSelfHealTestHub(repo)

		msg := models.NewHubMessage().SetReceiver("u-err")
		msg.MessageID = "m-idx-err"
		hub.markRerouteAttempted(msg.MessageID, []string{"node-old"}, true)

		rerouted := hub.decideUserNotFoundReroute(ctx, msg, "u-err", "node-old", appID, ns)
		assert.False(t, rerouted, "查询失败不重路由")

		_, exists := hub.rerouteGuard.Load(msg.MessageID)
		assert.True(t, exists, "查询失败保留 ACK 超时兜底，守卫不清")
	})

	t.Run("仓储未注入直接false", func(t *testing.T) {
		hub := newSelfHealTestHub(nil)
		msg := models.NewHubMessage().SetReceiver("u-nil")
		msg.MessageID = "m-nil-repo"
		assert.False(t, hub.decideUserNotFoundReroute(ctx, msg, "u-nil", "node-old", appID, ns))
	})
}

// TestReplyUserNotFoundPublishes 回告发布（目标节点侧）：
// 定向投递扑空后向发送节点发布 user_not_found；自身回告与 PubSub 未注入时静默跳过
func TestReplyUserNotFoundPublishes(t *testing.T) {
	hub, _, redisClient := newSelfHealClusterHub(t, nil)
	defer func() {
		_ = hub.pubsub.Close()
		_ = redisClient.Close()
	}()

	// 订阅发送节点 node-b 的专属频道（回告目的地）
	sub := subscribeNodeChannel(t, hub, redisClient, "node-b")
	defer func() { _ = sub.Close() }()

	msg := models.NewHubMessage().SetReceiver("u-9")
	msg.MessageID = "m-reply"
	distMsg := &models.DistributedMessage{
		Type:      models.OperationTypeSendMessage,
		NodeID:    "node-b",
		TargetID:  "u-9",
		Message:   msg,
		AppID:     constants.DefaultAppID,
		Namespace: constants.DefaultNamespace,
	}

	// 自身回告（NodeID == 本节点）：防回环，静默跳过
	selfReply := &models.DistributedMessage{NodeID: hub.nodeID, TargetID: "u-9", Message: msg}
	require.NotPanics(t, func() { hub.replyUserNotFound(context.Background(), selfReply) })

	// 正常回告：发布 user_not_found 到发送节点频道
	hub.replyUserNotFound(context.Background(), distMsg)
	reply := receiveNodeChannelPayload(t, sub)
	assert.Equal(t, models.OperationTypeUserNotFound, reply.Type, "回告类型应为 user_not_found")
	assert.Equal(t, "node-a", reply.NodeID, "回告来源为本节点")
	assert.Equal(t, "u-9", reply.TargetID)
	assert.Equal(t, "target_user_not_on_node", reply.Reason)
	assert.Equal(t, "m-reply", reply.Message.GetMessageID(), "回告应携带原始消息体")

	// PubSub 未注入（gRPC-only 集群）：响应体已即时携带，无需回告
	bare := newSelfHealTestHub(nil)
	require.NotPanics(t, func() { bare.replyUserNotFound(context.Background(), distMsg) })
}

// ============================================================================
// 死索引清理（目标节点侧）
// ============================================================================

// TestSelfHealDeadIndexEntries 死索引清理语义：仅清理指向本节点的条目、
// 清理前二次确认本地无该用户连接（防误清刚重连的活跃索引）、
// 索引无条目静默跳过、清理失败不 panic、异步执行不阻塞
func TestSelfHealDeadIndexEntries(t *testing.T) {
	ctx := context.Background()
	appID, ns := constants.DefaultAppID, constants.DefaultNamespace

	// 指向本节点/他节点的索引条目混合
	localDead := models.NewClient("c-local-dead", "u-heal", models.UserTypeCustomer)
	localDead.NodeID = "node-a"
	remoteAlive := models.NewClient("c-remote-alive", "u-heal", models.UserTypeCustomer)
	remoteAlive.NodeID = "node-b"

	t.Run("仅清理指向本节点的死条目", func(t *testing.T) {
		repo := &selfHealOnlineRepo{clients: []*models.Client{localDead, remoteAlive, nil}}
		hub := newSelfHealTestHub(repo)

		hub.doSelfHealDeadIndexEntries(ctx, "u-heal", appID, ns)

		require.Equal(t, 1, repo.offlineCallCount(), "应恰好触发一次批量下线")
		repo.mu.Lock()
		defer repo.mu.Unlock()
		require.Len(t, repo.offlineCalls, 1)
		require.Len(t, repo.offlineCalls[0], 1, "他节点活跃条目与 nil 条目不清理")
		assert.Equal(t, "c-local-dead", repo.offlineCalls[0][0].ID,
			"仅清理 NodeID 指向本节点的条目")
	})

	t.Run("清理前二次确认用户已重连本节点则跳过", func(t *testing.T) {
		repo := &selfHealOnlineRepo{clients: []*models.Client{localDead}}
		hub := newSelfHealTestHub(repo)
		// 用户重连回本节点（HasUser 二次确认命中）
		newServerTestClient(hub, "c-heal-new", "u-heal")

		hub.doSelfHealDeadIndexEntries(ctx, "u-heal", appID, ns)

		assert.Zero(t, repo.offlineCallCount(), "用户已重连本节点时不得清理其索引")
	})

	t.Run("索引无条目静默跳过", func(t *testing.T) {
		repo := &selfHealOnlineRepo{clientsErr: models.ErrUserNotFound}
		hub := newSelfHealTestHub(repo)

		require.NotPanics(t, func() {
			hub.doSelfHealDeadIndexEntries(ctx, "u-none", appID, ns)
		})
		assert.Zero(t, repo.offlineCallCount(), "用户无索引条目无需自愈")
	})

	t.Run("清理失败仅记日志不panic", func(t *testing.T) {
		repo := &selfHealOnlineRepo{
			clients:    []*models.Client{localDead},
			offlineErr: assert.AnError,
		}
		hub := newSelfHealTestHub(repo)

		require.NotPanics(t, func() {
			hub.doSelfHealDeadIndexEntries(ctx, "u-heal", appID, ns)
		})
	})

	t.Run("仓储未注入与异步执行冒烟", func(t *testing.T) {
		// onlineStatusRepo 未注入：异步包装直接返回
		bare := newSelfHealTestHub(nil)
		require.NotPanics(t, func() {
			bare.selfHealDeadIndexEntries(ctx, "u-heal", appID, ns)
		})

		// 异步执行：syncx.Go 派发，不阻塞调用方，最终完成清理
		repo := &selfHealOnlineRepo{clients: []*models.Client{localDead}}
		hub := newSelfHealTestHub(repo)
		hub.selfHealDeadIndexEntries(ctx, "u-heal", appID, ns)
		require.Eventually(t, func() bool {
			return repo.offlineCallCount() == 1
		}, 2*time.Second, 10*time.Millisecond, "异步自愈应最终完成死索引清理")
	})
}

// ============================================================================
// 幽灵连接回收（client_reclaim）
// ============================================================================

// TestDetectClientMigration 迁移检测：nil/空 clientID、单机模式、
// 仓储查询失败均返回空串；集群模式下返回旧归属节点
func TestDetectClientMigration(t *testing.T) {
	ctx := context.Background()
	client := models.NewClient("c-mig", "u-mig", models.UserTypeCustomer)

	// nil client / 空 clientID
	bare := newSelfHealTestHub(&selfHealOnlineRepo{owner: "node-old"})
	assert.Empty(t, bare.detectClientMigration(ctx, nil))
	assert.Empty(t, bare.detectClientMigration(ctx, models.NewClient("", "u", models.UserTypeCustomer)))

	// 单机模式（无 PubSub 且未启用 node-grpc）：无跨节点迁移语义
	assert.Empty(t, bare.detectClientMigration(ctx, client),
		"单机模式不检测跨节点迁移")

	// 集群模式：返回旧归属节点
	hub, _, redisClient := newSelfHealClusterHub(t, &selfHealOnlineRepo{owner: "node-old"})
	defer func() {
		_ = hub.pubsub.Close()
		_ = redisClient.Close()
	}()
	assert.Equal(t, "node-old", hub.detectClientMigration(ctx, client))

	// 仓储查询失败：返回空串（记 warn 日志）
	hub.onlineStatusRepo = &selfHealOnlineRepo{}
	assert.Empty(t, hub.detectClientMigration(ctx, client),
		"归属查询失败时应返回空串")
}

// TestNotifyClientReclaimPublishes 回收通知发布：
// 通知旧节点回收幽灵连接（client_reclaim 携带新连接建立时间戳）；
// PubSub 未注入 / 归属为空或本节点时静默跳过
func TestNotifyClientReclaimPublishes(t *testing.T) {
	hub, _, redisClient := newSelfHealClusterHub(t, nil)
	defer func() {
		_ = hub.pubsub.Close()
		_ = redisClient.Close()
	}()

	sub := subscribeNodeChannel(t, hub, redisClient, "node-old")
	defer func() { _ = sub.Close() }()

	client := models.NewClient("c-reclaim", "u-reclaim", models.UserTypeCustomer)
	client.ConnectedAt = time.Now().Add(-time.Minute)

	// 静默分支：归属为空 / 归属为本节点 / PubSub 未注入
	require.NotPanics(t, func() {
		hub.notifyClientReclaim(context.Background(), client, "")
		hub.notifyClientReclaim(context.Background(), client, hub.nodeID)
	})
	bare := newSelfHealTestHub(nil)
	require.NotPanics(t, func() {
		bare.notifyClientReclaim(context.Background(), client, "node-old")
	})

	// 正常发布：client_reclaim 定向投递到旧节点
	hub.notifyClientReclaim(context.Background(), client, "node-old")
	dispatch := receiveNodeChannelPayload(t, sub)
	assert.Equal(t, models.OperationTypeClientReclaim, dispatch.Type)
	assert.Equal(t, "c-reclaim", dispatch.TargetID, "回收目标应为 clientID")
	assert.Equal(t, "node-a", dispatch.NodeID)
	// 时间戳经 PubSub JSON 往返后时区归一为 UTC，断言比较"时刻"而非时区表示
	assert.True(t, client.ConnectedAt.Equal(dispatch.Timestamp),
		"时间戳应为新节点连接建立时间（旧节点据此判定本地连接是否为幽灵）")
	assert.Equal(t, "client_migrated", dispatch.Reason)
}

// TestHandleDistributedClientReclaim 幽灵回收（旧节点侧）：
// 空 clientID / 本地无该连接静默跳过；时序守卫（本地连接不早于新节点连接
// —— 漂移又漂回场景 —— 不回收）；本地为旧连接时异步踢出回收
func TestHandleDistributedClientReclaim(t *testing.T) {
	hub := newSelfHealTestHub(nil)
	ctx := context.Background()

	// 空 clientID
	require.NoError(t, hub.handleDistributedClientReclaim(ctx, &models.DistributedMessage{}))

	// 本地无该 clientID 连接
	require.NoError(t, hub.handleDistributedClientReclaim(ctx,
		&models.DistributedMessage{TargetID: "c-unknown", Timestamp: time.Now()}))

	ghost := models.NewClient("c-ghost", "u-ghost", models.UserTypeCustomer).
		WithSendChan(make(chan []byte, 1))
	ghost.ConnectedAt = time.Now().Add(-time.Minute) // 旧连接（早于新节点）
	hub.shardedRegistry.AddClient(ghost)

	// 时序守卫：新节点连接时间早于本地（漂移又漂回）→ 本地才是最新连接，不回收
	require.NoError(t, hub.handleDistributedClientReclaim(ctx, &models.DistributedMessage{
		NodeID:    "node-new",
		TargetID:  "c-ghost",
		Timestamp: ghost.ConnectedAt.Add(-time.Hour), // 更早的新节点连接时间
	}))
	if _, ok := hub.shardedRegistry.GetClient("c-ghost"); !ok {
		t.Fatal("本地连接不早于新节点连接时不应回收")
	}

	// 正常回收：本地连接早于新节点连接 → 幽灵连接被踢出（异步注销）
	require.NoError(t, hub.handleDistributedClientReclaim(ctx, &models.DistributedMessage{
		Type:      models.OperationTypeClientReclaim,
		NodeID:    "node-new",
		TargetID:  "c-ghost",
		Timestamp: time.Now(),
	}))
	require.Eventually(t, func() bool {
		_, ok := hub.shardedRegistry.GetClient("c-ghost")
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "幽灵连接应被异步回收（KickClient → Unregister）")
}
