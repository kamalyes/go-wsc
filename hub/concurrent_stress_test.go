/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 00:07:53
 * @FilePath: \go-wsc\hub\concurrent_stress_test.go
 * @Description: Hub 高并发多线程压力测试
 *
 * 4 类并发混合场景，全部用 -race 验证无数据竞争：
 *   1. 点对点发消息（SendToUserWithRetry）
 *   2. 全局广播 + 命名空间广播 + 群组广播（Broadcast/BroadcastToNamespace/SendToGroup）
 *   3. 修改消息内容（Clone+SetContent/WithOption+SetNamespace/SetGroupIDs 并发读写）
 *   4. Client 生命周期变更（Register/Unregister+SetNamespace+SetGroupID 并发）
 *
 * 辅助：1 个混合并发测试（以上4类混合同时启动），确保全局竞争暴露出来
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 公共 Setup
// ============================================================================

const (
	stressNamespace  = "stress-ns"
	stressGroupID    = "stress-gid"
	stressBufSize    = 4096
	stressClientBase = 200 // 基础在线客户端数
)

// setupStressHub 创建压力测试 Hub（启动 Run 循环、群组、注册基础客户端）
func setupStressHub(t *testing.T, withGroup bool) (*Hub, repository.GroupRepository, []*Client, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	// miniredis 为进程内实现，-race 下并发压力会放大 5~10 倍延迟，
	// 使用宽松超时避免 Redis 客户端默认 3s ReadTimeout 误判 i/o timeout
	redisClient := redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     64,
	})

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(60 * time.Second).
		WithMessageBufferSize(stressBufSize)
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0

	hub := NewHub(config)
	groupRepo := repository.NewRedisGroupRepository(redisClient, "wsc:stress:group:")
	hub.SetGroupRepository(groupRepo)

	go hub.Run()
	time.Sleep(120 * time.Millisecond)

	// 注册基础客户端（不同 ns 验证隔离，不同 group 验证广播）
	clients := make([]*Client, 0, stressClientBase)
	for i := 0; i < stressClientBase; i++ {
		ns := stressNamespace
		if i%3 == 0 {
			ns = "stress-other-ns" // 1/3 放另一个 ns 验证命名空间隔离不乱
		}
		c := makeStressClient(fmt.Sprintf("s-c-%d", i), fmt.Sprintf("s-u-%d", i), ns)
		hub.Register(c)
		clients = append(clients, c)
	}
	require.Eventually(t, func() bool {
		return hub.GetClientsCount() >= int64(stressClientBase)
	}, 5*time.Second, 10*time.Millisecond, "基础客户端注册完成")

	// 创建群组并加入一半客户端
	if withGroup {
		ctx := context.Background()
		require.NoError(t, groupRepo.CreateGroup(ctx, &repository.Group{
			GroupID: stressGroupID, Namespace: stressNamespace, OwnerID: "owner",
		}))
		members := make([]string, 0, stressClientBase/2)
		for i := 0; i < stressClientBase && i < stressClientBase/2; i += 2 {
			members = append(members, fmt.Sprintf("s-u-%d", i))
		}
		require.NoError(t, hub.AddGroupMembers(ctx, stressNamespace, stressGroupID, members))
	}

	cleanup := func() {
		hub.Shutdown()
		_ = redisClient.Close()
		mr.Close()
	}
	return hub, groupRepo, clients, cleanup
}

// makeStressClient 创建自动 drain SendChan 的测试客户端
func makeStressClient(clientID, userID, namespace string) *Client {
	c := &Client{
		ID:             clientID,
		UserID:         userID,
		UserType:       UserTypeCustomer,
		Role:           models.UserRoleCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, stressBufSize),
		Context:        context.WithValue(context.Background(), ContextKeyUserID, userID),
		ConnectedAt:    time.Now(),
		LastSeen:       time.Now(),
	}
	c.WithNamespace(namespace)
	c.SetGroupID("") // 默认空 group，群组维度不参与系统组匹配
	// 后台 drain：SendChan 满就 drop，不阻塞发送方
	go func() {
		for range c.SendChan {
			// drain only
		}
	}()
	return c
}

// makeStressClientWithGroup 创建自动 drain SendChan 的测试客户端（指定 groupID）
func makeStressClientWithGroup(clientID, userID, namespace, groupID string) *Client {
	c := &Client{
		ID:             clientID,
		UserID:         userID,
		UserType:       UserTypeCustomer,
		Role:           models.UserRoleCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, stressBufSize),
		Context:        context.WithValue(context.Background(), ContextKeyUserID, userID),
		ConnectedAt:    time.Now(),
		LastSeen:       time.Now(),
	}
	c.WithNamespace(namespace)
	c.SetGroupID(groupID)
	go func() {
		for range c.SendChan {
			// drain only
		}
	}()
	return c
}

// stressMsgSeq 原子序列号，保证并发消息 MessageID 唯一
var stressMsgSeq int64

// makeStressMessage 创建带 stress 标记的消息（便于断言和日志区分）
func makeStressMessage(sender, body string) *HubMessage {
	m := NewHubMessage()
	m.Sender = sender
	m.MessageID = fmt.Sprintf("stress-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&stressMsgSeq, 1))
	m.MessageType = models.MessageTypeText
	m.BroadcastType = models.BroadcastTypeNone // 默认单播（P2P）
	m.SetContent(body)
	m.WithOption("stress", "yes")
	return m
}

// ============================================================================
// 1. 并发 P2P 发消息压力测试
// ============================================================================

// TestConcurrentP2PSendStress 多 goroutine 并发向在线用户发送 P2P
// -race 下无竞争，无 panic，消息投递失败率低于阈值
//
// 注意：不加 t.Parallel()——每个压力测试内部已启动数十个 goroutine 做并发验证，
// 外部并行只会让 5 个 Hub × 200 客户端 × miniredis 在 -race 下相互饿死导致 i/o timeout。
func TestConcurrentP2PSendStress(t *testing.T) {
	hub, _, _, cleanup := setupStressHub(t, false)
	defer cleanup()
	ctx := context.Background()

	const workers = 32
	const perWorker = 200
	var errCount int64
	var totalSent int64
	wg := sync.WaitGroup{}
	wg.Add(workers)

	start := time.Now()
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				to := fmt.Sprintf("s-u-%d", (w*perWorker+i)%stressClientBase)
				from := fmt.Sprintf("sender-%d-%d", w, i)
				msg := makeStressMessage(from, fmt.Sprintf("hello from w%d-i%d", w, i))
				ctx := routing.WithNamespaceGroupIDs(ctx, stressNamespace, nil)
				res := hub.SendToUserWithRetry(ctx, to, msg)
				if res.FinalError != nil {
					atomic.AddInt64(&errCount, 1)
				} else {
					atomic.AddInt64(&totalSent, 1)
				}
			}
		}(w)
	}
	wg.Wait()
	duration := time.Since(start)
	t.Logf("并发P2P发送完成: workers=%d per=%d total=%d err=%d time=%v rate=%.0f msg/s",
		workers, perWorker, totalSent, errCount, duration, float64(totalSent)/duration.Seconds())
	// 允许少量失败（客户端 SendChan 可能满），但不应有竞争/恐慌
	total := int64(workers * perWorker)
	assert.Less(t, float64(errCount), float64(total)*0.1, "错误率应低于 10%")
}

// ============================================================================
// 2. 并发广播压力测试（全局/命名空间/群组广播并发混合）
// ============================================================================

// TestConcurrentBroadcastStress 三类广播并发：Broadcast 全局、BroadcastToNamespace 命名空间、
// SendToGroup 群组；-race 下无数据竞争，无 panic
func TestConcurrentBroadcastStress(t *testing.T) {
	hub, _, _, cleanup := setupStressHub(t, true)
	defer cleanup()
	ctx := context.Background()

	const eachCount = 150
	var deliveredGlobal, deliveredNs, successGroup int64
	wg := sync.WaitGroup{}
	wg.Add(3)

	// Worker 1: 全局广播
	go func() {
		defer wg.Done()
		for i := 0; i < eachCount; i++ {
			msg := makeStressMessage("gb-sender", fmt.Sprintf("global-broadcast-%d", i))
			msg.SetBroadcastType(models.BroadcastTypeGlobal)
			hub.Broadcast(ctx, msg)
			atomic.AddInt64(&deliveredGlobal, int64(hub.GetClientsCount())) // 仅记录逻辑计数
		}
	}()

	// Worker 2: 命名空间广播
	go func() {
		defer wg.Done()
		for i := 0; i < eachCount; i++ {
			msg := makeStressMessage("ns-sender", fmt.Sprintf("ns-broadcast-%d", i))
			msg.SetBroadcastType(models.BroadcastTypeGlobal)
			delivered := hub.BroadcastToNamespace(ctx, stressNamespace, msg)
			atomic.AddInt64(&deliveredNs, int64(delivered))
		}
	}()

	// Worker 3: 群组广播（SendToGroup 需要群组上下文）
	go func() {
		defer wg.Done()
		for i := 0; i < eachCount; i++ {
			msg := makeStressMessage("gp-sender", fmt.Sprintf("group-broadcast-%d", i))
			gctx := routing.WithNamespaceGroupIDs(ctx, stressNamespace, []string{stressGroupID})
			res := hub.SendToGroup(gctx, msg, false)
			if len(res.Errors) == 0 {
				atomic.AddInt64(&successGroup, 1)
			}
		}
	}()

	wg.Wait()
	t.Logf("并发广播完成: 全局attempt=%d 命名空间total_delivery=%d 群组success=%d",
		deliveredGlobal, deliveredNs, successGroup)
	assert.Greater(t, deliveredGlobal, int64(0))
	assert.Greater(t, deliveredNs, int64(0))
	assert.Greater(t, successGroup, int64(0))
}

// ============================================================================
// 3. 并发修改消息内容压力测试（Clone + SetContent + WithOption + 路由读写）
// ============================================================================

// TestConcurrentHubMessageModifyStress 同一 HubMessage Clone 后的多副本并发写入，
// 原始消息只读，验证 RWMutex + Clone 无数据竞争
func TestConcurrentHubMessageModifyStress(t *testing.T) {
	const workers = 40
	const perWorker = 500
	var totalOps int64
	wg := sync.WaitGroup{}
	wg.Add(workers)

	// 共享基础消息：只 Clone 不改（只读，若 Clone 锁正确无竞争）
	base := makeStressMessage("base", "base-body")
	base.SetNamespace(stressNamespace)
	base.SetGroupIDs([]string{"g1", "g2"})
	base.CreateAt = time.Now()
	base.SessionID = "base-sess"

	start := time.Now()
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// 1) Clone 只读转写
				c := base.Clone()
				// 2) 并发写 content/data/metadata/route
				c.SetContent(fmt.Sprintf("clone-%d-%d", w, i))
				c.WithOption("k", fmt.Sprintf("v-%d-%d", w, i))
				c.WithOption("iter", fmt.Sprintf("%d", i))
				c.SetMessageID(fmt.Sprintf("clone-%d-%d-%d", w, i, atomic.AddInt64(&stressMsgSeq, 1)))
				c.SessionID = fmt.Sprintf("s-%d", w)
				c.CreateAt = time.Now()
				// 3) 写路由
				if i%2 == 0 {
					c.SetNamespace("ns-mod")
				}
				if i%3 == 0 {
					c.SetGroupIDs([]string{fmt.Sprintf("g-%d", i)})
				}
				// 4) 读路由/内容（并发读不应竞争）
				_ = c.GetNamespace()
				_ = c.GetGroupIDs()
				_ = c.Content
				_, _ = c.GetOption("k")
				// 5) 注入 context（含路由）读回
				ctx := context.Background()
				_ = c.InjectRoute(ctx)
				atomic.AddInt64(&totalOps, 1)
			}
		}(w)
	}
	wg.Wait()
	duration := time.Since(start)
	total := int64(workers * perWorker)
	t.Logf("并发消息Clone+修改完成: workers=%d per=%d ops=%d time=%v rate=%.0f ops/s",
		workers, perWorker, totalOps, duration, float64(totalOps)/duration.Seconds())
	assert.Equal(t, total, totalOps)

	// 共享 base 不应被修改
	assert.Equal(t, "base-body", base.Content, "基础消息 Content 不应因 Clone 修改被改动")
	assert.Equal(t, stressNamespace, base.GetNamespace(), "基础消息 namespace 不应因 Clone 修改被改动")
}

// ============================================================================
// 4. 并发变更 Client 压力测试（Register/Unregister/SetNamespace/SetGroupID）
// ============================================================================

// TestConcurrentClientLifecycleStress 同时注册新客户端、注销现有客户端、
// 并对存活客户端修改 namespace/groupID，-race 下 shardedRegistry 无竞争
func TestConcurrentClientLifecycleStress(t *testing.T) {
	hub, _, clients, cleanup := setupStressHub(t, false)
	defer cleanup()

	const regWorkers = 8
	const unregWorkers = 4
	const modWorkers = 16
	const perWorker = 100

	var regOk, unregOk, modOk int64
	wg := sync.WaitGroup{}
	wg.Add(regWorkers + unregWorkers + modWorkers)

	// 保护 clients[idx] 的并发读写（多个 modWorker 可能命中同一个 idx；同时 unregWorker 在读）
	var clientsMu sync.Mutex

	// Worker 组 1：注册新客户端
	for w := 0; w < regWorkers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				cid := fmt.Sprintf("new-c-%d-%d", w, i)
				uid := fmt.Sprintf("new-u-%d-%d", w, i)
				ns := "new-ns"
				if i%2 == 0 {
					ns = stressNamespace
				}
				var gid string
				if i%4 == 0 {
					gid = fmt.Sprintf("g-%d-%d", w, i)
				}
				c := makeStressClientWithGroup(cid, uid, ns, gid)
				hub.Register(c)
				atomic.AddInt64(&regOk, 1)
			}
		}(w)
	}

	// Worker 组 2：注销随机客户端（从基础 clients 里，允许 idempotent）
	for w := 0; w < unregWorkers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				clientsMu.Lock()
				idx := (w*perWorker + i) % len(clients)
				c := clients[idx]
				clientsMu.Unlock()
				hub.Unregister(c)
				atomic.AddInt64(&unregOk, 1)
			}
		}(w)
	}

	// Worker 组 3：修改客户端的 namespace/groupID（用 Unregister+Register 新 client 实现，避免原地字段写 race）
	for w := 0; w < modWorkers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				clientsMu.Lock()
				idx := (w*perWorker + i*3 + 1) % len(clients)
				old := clients[idx]
				oldID := old.ID
				oldUID := old.UserID
				clientsMu.Unlock()

				newNS := stressNamespace
				if i%2 == 0 {
					newNS = fmt.Sprintf("dyn-ns-%d", w)
				}
				newGID := ""
				if i%3 == 0 {
					newGID = fmt.Sprintf("dyn-g-%d", i)
				}

				replacement := makeStressClientWithGroup(oldID, oldUID, newNS, newGID)
				hub.Unregister(old)
				hub.Register(replacement)

				clientsMu.Lock()
				clients[idx] = replacement
				clientsMu.Unlock()
				atomic.AddInt64(&modOk, 1)
			}
		}(w)
	}
	wg.Wait()

	t.Logf("客户端并发生命周期完成: reg=%d unreg=%d mod=%d, 当前在线=%d",
		regOk, unregOk, modOk, hub.GetClientsCount())
	assert.Greater(t, regOk, int64(0))
	assert.Greater(t, modOk, int64(0))
}

// ============================================================================
// 5. 混合并发压力测试（以上 4 类并发同时启动，全局互斥资源全面争抢）
// ============================================================================

// TestConcurrentMixedAllStress 所有并发场景同时启动，
// 模拟生产中多租户、广播与连接变更混合、消息读写交错场景
func TestConcurrentMixedAllStress(t *testing.T) {
	hub, _, _, cleanup := setupStressHub(t, true)
	defer cleanup()
	ctx := context.Background()

	var totalOps int64
	wg := sync.WaitGroup{}

	// --- 场景 A：P2P 发送（24 goroutine）---
	for w := 0; w < 24; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 80; i++ {
				to := fmt.Sprintf("s-u-%d", (w*80+i)%stressClientBase)
				msg := makeStressMessage(fmt.Sprintf("m-a-%d", w), fmt.Sprintf("b%d", i))
				ctx := routing.WithNamespaceGroupIDs(ctx, stressNamespace, nil)
				_ = hub.SendToUserWithRetry(ctx, to, msg)
				atomic.AddInt64(&totalOps, 1)
			}
		}(w)
	}

	// --- 场景 B：广播（全局/命名空间/群组，共 3 goroutine）---
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			msg := makeStressMessage("mixed-gb", fmt.Sprintf("m-gb-%d", i))
			msg.SetBroadcastType(models.BroadcastTypeGlobal)
			hub.Broadcast(ctx, msg)
			atomic.AddInt64(&totalOps, 1)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			msg := makeStressMessage("mixed-ns", fmt.Sprintf("m-ns-%d", i))
			msg.SetBroadcastType(models.BroadcastTypeGlobal)
			hub.BroadcastToNamespace(ctx, stressNamespace, msg)
			atomic.AddInt64(&totalOps, 1)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			msg := makeStressMessage("mixed-gp", fmt.Sprintf("m-gp-%d", i))
			gctx := routing.WithNamespaceGroupIDs(ctx, stressNamespace, []string{stressGroupID})
			_ = hub.SendToGroup(gctx, msg, true)
			atomic.AddInt64(&totalOps, 1)
		}
	}()

	// --- 场景 C：消息 Clone + 修改（16 goroutine）---
	base := makeStressMessage("mixed-base", "base")
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				c := base.Clone()
				c.SetContent(fmt.Sprintf("b-%d-%d", w, i))
				c.WithOption("x", fmt.Sprintf("%d", i))
				c.SetNamespace(fmt.Sprintf("x-ns-%d", w%3))
				if i%2 == 0 {
					c.SetGroupIDs([]string{fmt.Sprintf("xg-%d", i)})
				}
				_ = c.Content
				_ = c.GetNamespace()
				_ = c.GetGroupIDs()
				atomic.AddInt64(&totalOps, 1)
			}
		}(w)
	}

	// --- 场景 D：Client 注册/注销/修改（12 goroutine）---
	for w := 0; w < 12; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				switch {
				case i%3 == 0:
					c := makeStressClient(
						fmt.Sprintf("mx-c-%d-%d", w, i),
						fmt.Sprintf("mx-u-%d-%d", w, i),
						"mixed-ns",
					)
					hub.Register(c)
				case i%3 == 1:
					idx := (w*60 + i) % stressClientBase
					cid := fmt.Sprintf("s-c-%d", idx)
					hub.shardedRegistry.ForEachClient(func(_ string, c *Client) bool {
						if c.ID == cid {
							hub.Unregister(c)
							return false
						}
						return true
					})
				default:
					idx := (w*60 + i*5) % stressClientBase
					cid := fmt.Sprintf("s-c-%d", idx)
					uid := fmt.Sprintf("s-u-%d", idx)
					// 正确的 Client 变更语义：创建一个新的 Client（新 namespace / groupID）
					// 先注销旧 client，再以相同 clientID + userID 注册新 client，
					// 从而模拟线上"变更 client 路由信息"的场景，避免原地修改指针字段导致 race。
					newNS := fmt.Sprintf("d-ns-%d", w)
					newGID := fmt.Sprintf("d-g-%d", i)
					replacement := makeStressClientWithGroup(cid, uid, newNS, newGID)
					// 先尝试 unregister 旧的（如果存在）
					hub.shardedRegistry.ForEachClient(func(_ string, c *Client) bool {
						if c.ID == cid {
							hub.Unregister(c)
							return false
						}
						return true
					})
					hub.Register(replacement)
				}
				atomic.AddInt64(&totalOps, 1)
			}
		}(w)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Logf("混合并发压力测试完成: total_ops=%d", atomic.LoadInt64(&totalOps))
	case <-time.After(180 * time.Second):
		t.Fatalf("混合并发压力测试 180s 超时，total_ops=%d", atomic.LoadInt64(&totalOps))
	}
}
