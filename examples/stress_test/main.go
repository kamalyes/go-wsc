/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-22 00:00:00
 * @FilePath: \go-wsc\examples\stress_test\main.go
 * @Description: 跨节点 WebSocket 消息分发压力测试
 *
 * 验证 go-wsc 多节点（模拟多 Pod）场景下的 WS 消息分发能力：
 *   1. 启动 N 个 Hub 节点共享同一 Redis（在线状态 + PubSub 跨节点路由）
 *   2. 大量 WS 客户端 round-robin 分布到不同节点（模拟 K8s Service LB 分散到 Pod）
 *   3. 测试 P2P 跨节点投递、群组广播、命名空间广播
 *   4. 验证 appID+namespace 隔离（不同信封的消息不串台）
 *
 * 用法：
 *   go run ./examples/stress_test/ [-nodes=3] [-clients=300] [-redis=localhost:6379]
 *
 * 无 -redis 参数时使用 miniredis（进程内 Redis，自包含无外部依赖）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/hub"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// 配置
// ============================================================================

const (
	defaultNodes     = 3   // 模拟节点数（Pod 数）
	defaultClients   = 300 // WS 客户端总数
	defaultBasePort  = 18081
	heartbeatTimeout = 120 * time.Second
	connectTimeout   = 10 * time.Second
	receiveWait      = 5 * time.Second // 每轮场景等待接收的窗口
)

// 隔离维度常量（3 组客户端验证 appID+namespace 隔离）
const (
	appA = "app-A"
	appB = "app-B"
	nsA  = "ns-A"
	nsB  = "ns-B"
	gA   = "group-A"
)

// clientProfile 客户端画像：定义一组连接的路由信封
type clientProfile struct {
	appID     string
	namespace string
	groupID   string
	label     string // 可读标签
}

// 三组客户端画像：
//   - Group1: app-A/ns-A/g-A   （主测试组，P2P/群组/广播目标）
//   - Group2: app-B/ns-B/g-A   （验证 appID 隔离：同 groupID 不同 appID 不应收到 app-A 消息）
//   - Group3: app-A/ns-B/g-A   （验证 namespace 隔离：同 appID 不同 namespace 不应收到 ns-A 消息）
var profiles = []clientProfile{
	{appA, nsA, gA, "appA-nsA"},
	{appB, nsB, gA, "appB-nsB"},
	{appA, nsB, gA, "appA-nsB"},
}

// ============================================================================
// 节点管理
// ============================================================================

// stressNode 模拟一个 Pod 的 Hub 节点
type stressNode struct {
	id     string
	port   int
	hub    *hub.Hub
	server *http.Server
}

// startNode 启动一个 Hub 节点（共享 Redis + PubSub）
func startNode(nodeID string, port int, redisClient *redis.Client, onlineTTL time.Duration) *stressNode {
	// 通过环境变量注入 nodeID（generateNodeID 优先级：POD_NAME > HOSTNAME > NODE_ID）
	os.Setenv("NODE_ID", nodeID)

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", port).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(1024)
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0 // 允许同一用户多连接

	h := hub.NewHub(config)

	// 启用分布式：共享 Redis PubSub
	pubsub := cachex.NewPubSub(redisClient)
	h.SetPubSub(pubsub)

	// 设置在线状态仓储（跨节点路由必需，共享同一 Redis）
	onlineStatusRepo := repository.NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:stress:online:",
		TTL:       onlineTTL,
	})
	h.SetOnlineStatusRepository(onlineStatusRepo)

	// 设置群组仓储（群组广播必需）
	groupRepo := repository.NewRedisGroupRepository(redisClient, "wsc:stress:group:")
	h.SetGroupRepository(groupRepo)

	go h.Run()
	h.WaitForStart()

	// HTTP /ws 端点（使用 Hub 内置升级处理器，自动从 query 提取 app_id/namespace/group_id）
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocketUpgrade)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("❌ 节点 %s HTTP 启动失败: %v", nodeID, err)
		}
	}()

	return &stressNode{
		id:     h.GetNodeID(),
		port:   port,
		hub:    h,
		server: srv,
	}
}

// shutdownNode 优雅关闭节点
func (n *stressNode) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = n.server.Shutdown(ctx)
	n.hub.SafeShutdown()
}

// ============================================================================
// WS 客户端
// ============================================================================

// stressClient 压测 WS 客户端：连接节点、收消息、计数
type stressClient struct {
	userID   string
	profile  clientProfile
	nodeIdx  int // 连接的节点索引
	conn     *websocket.Conn
	received atomic.Int64
	stopCh   chan struct{}
}

// connectWS 连接到指定节点的 /ws 端点（query 参数携带路由信封）
func connectWS(nodePort int, userID, appID, namespace, groupID string) (*websocket.Conn, error) {
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws?user_id=%s&app_id=%s&namespace=%s&group_id=%s",
		nodePort, userID, appID, namespace, groupID)

	dialer := websocket.Dialer{
		HandshakeTimeout: connectTimeout,
	}
	conn, _, err := dialer.Dial(url, nil)
	return conn, err
}

// startReceiver 启动收消息 goroutine，解析 JSON 消息并计数
func (c *stressClient) startReceiver(wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(c.stopCh)
		for {
			_, data, err := c.conn.ReadMessage()
			if err != nil {
				return
			}
			// 解析消息：Hub 下发的消息是 HubMessage JSON
			var msg struct {
				MessageID string `json:"message_id"`
				Content   string `json:"content"`
				AppID     string `json:"app_id,omitempty"`
				Namespace string `json:"namespace"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.MessageID != "" {
				c.received.Add(1)
			}
		}
	}()
}

// close 关闭客户端连接
func (c *stressClient) close() {
	if c.conn != nil {
		_ = c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = c.conn.Close()
	}
}

// ============================================================================
// 压测引擎
// ============================================================================

// stressEngine 压测引擎：管理节点、客户端、场景执行
type stressEngine struct {
	nodes     []*stressNode
	clients   []*stressClient
	groupRepo repository.GroupRepository
	mu        sync.Mutex // 保护 clients 切片的并发追加
}

// setup 启动节点并连接客户端
func (e *stressEngine) setup(numNodes, numClients, basePort int, redisAddr string) {
	// 1. 启动 Redis（miniredis 或真实 Redis）
	var redisClient *redis.Client
	if redisAddr != "" {
		redisClient = redis.NewClient(&redis.Options{Addr: redisAddr})
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			log.Fatalf("❌ 无法连接 Redis %s: %v", redisAddr, err)
		}
		log.Printf("✅ 已连接真实 Redis: %s", redisAddr)
	} else {
		mr, err := miniredis.Run()
		if err != nil {
			log.Fatalf("❌ 启动 miniredis 失败: %v", err)
		}
		redisClient = redis.NewClient(&redis.Options{Addr: mr.Addr()})
		log.Printf("✅ 已启动 miniredis: %s", mr.Addr())
	}

	// 2. 启动 N 个节点
	for i := 0; i < numNodes; i++ {
		nodeID := fmt.Sprintf("stress-node-%d", i)
		port := basePort + i
		n := startNode(nodeID, port, redisClient, 10*time.Minute)
		e.nodes = append(e.nodes, n)
		log.Printf("🚀 节点 %s 启动 (port=%d, nodeID=%s)", nodeID, port, n.id)
	}
	time.Sleep(500 * time.Millisecond) // 等待节点就绪

	e.groupRepo = repository.NewRedisGroupRepository(redisClient, "wsc:stress:group:")

	// 3. 连接客户端（round-robin 分散到不同节点，模拟 LB 分发到 Pod）
	clientsPerProfile := numClients / len(profiles)
	var connected int64
	var connWG sync.WaitGroup

	for pi, prof := range profiles {
		for i := 0; i < clientsPerProfile; i++ {
			// 全局索引决定连接哪个节点（round-robin）
			globalIdx := pi*clientsPerProfile + i
			nodeIdx := globalIdx % numNodes
			userID := fmt.Sprintf("u-%s-%d", prof.label, i)

			connWG.Add(1)
			go func(nodeIdx int, userID string, prof clientProfile) {
				defer connWG.Done()
				port := basePort + nodeIdx
				conn, err := connectWS(port, userID, prof.appID, prof.namespace, prof.groupID)
				if err != nil {
					log.Printf("⚠️ 客户端 %s 连接 node-%d 失败: %v", userID, nodeIdx, err)
					return
				}
				c := &stressClient{
					userID:  userID,
					profile: prof,
					nodeIdx: nodeIdx,
					conn:    conn,
					stopCh:  make(chan struct{}),
				}
				c.startReceiver(&connWG)
				// 线程安全追加
				e.mu.Lock()
				e.clients = append(e.clients, c)
				e.mu.Unlock()
				atomic.AddInt64(&connected, 1)
			}(nodeIdx, userID, prof)
		}
	}
	connWG.Wait()

	log.Printf("✅ 已连接 %d/%d 客户端（分布在 %d 节点）", atomic.LoadInt64(&connected),
		numClients, numNodes)

	// 4. 打印每节点连接分布
	e.printNodeDistribution()

	// 5. 创建群组并添加成员（appA-nsA 组的客户端加入 group-A）
	e.setupGroups()
}

// printNodeDistribution 打印每节点的连接数分布
func (e *stressEngine) printNodeDistribution() {
	dist := make(map[int]int)
	profileDist := make(map[string]map[int]int)
	for _, c := range e.clients {
		dist[c.nodeIdx]++
		if profileDist[c.profile.label] == nil {
			profileDist[c.profile.label] = make(map[int]int)
		}
		profileDist[c.profile.label][c.nodeIdx]++
	}
	log.Println("📊 节点连接分布:")
	for i := 0; i < len(e.nodes); i++ {
		log.Printf("   node-%d: %d 连接", i, dist[i])
	}
	log.Println("📊 各画像节点分布:")
	for label, nd := range profileDist {
		log.Printf("   %s: %v", label, nd)
	}
}

// setupGroups 创建群组并添加成员
func (e *stressEngine) setupGroups() {
	ctx := context.Background()
	// 为 appA-nsA 画像创建群组 group-A
	routeCtx := routing.NewRoute().
		WithAppID(appA).
		WithNamespace(nsA).
		WithGroupIDs([]string{gA}).
		Inject(ctx)

	// 创建群组
	group := &repository.Group{
		GroupID:   gA,
		Namespace: nsA,
		OwnerID:   "stress-owner",
	}
	_ = e.groupRepo.CreateGroup(routeCtx, group)

	// 添加 appA-nsA 客户端为群组成员
	var members []string
	for _, c := range e.clients {
		if c.profile.label == "appA-nsA" {
			members = append(members, c.userID)
		}
	}
	if len(members) > 0 {
		// AddGroupMembers 从 Hub 取（需要在有 Hub 的节点上调用）
		e.nodes[0].hub.AddGroupMembers(routeCtx, members)
	}
	log.Printf("✅ 群组 %s 已创建，成员数: %d", gA, len(members))
}

// ============================================================================
// 测试场景
// ============================================================================

// scenarioResult 场景测试结果
type scenarioResult struct {
	name        string
	sent        int
	received    int64
	expected    int
	passed      bool
	description string
}

// runP2PCrossNode 场景1: P2P 跨节点投递
// 发送方在 node-0，接收方在 node-1（或最后一个节点），验证跨节点 P2P 送达
func (e *stressEngine) runP2PCrossNode() scenarioResult {
	result := scenarioResult{
		name:        "P2P 跨节点投递",
		description: "从 node-0 向 node-N 上的用户发送 P2P 消息",
	}

	// 找两个不同节点上的 appA-nsA 客户端
	var sender, receiver *stressClient
	for _, c := range e.clients {
		if c.profile.label != "appA-nsA" {
			continue
		}
		if sender == nil {
			sender = c
		} else if c.nodeIdx != sender.nodeIdx {
			receiver = c
			break
		}
	}
	if sender == nil || receiver == nil {
		result.description = "无法找到不同节点上的 appA-nsA 客户端"
		return result
	}

	// 构造消息
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetContent("P2P cross-node test").
		SetMessageID("p2p-test-001").
		SetSender(sender.userID)

	// 注入路由信封（appA/nsA），通过 SendToUserWithRetry 发送
	ctx := routing.NewRoute().
		WithAppID(appA).
		WithNamespace(nsA).
		Inject(context.Background())

	sr := e.nodes[0].hub.SendToUserWithRetry(ctx, receiver.userID, msg)
	result.sent = 1
	result.expected = 1

	if sr.Success {
		time.Sleep(receiveWait)
		result.received = receiver.received.Load()
	}
	result.passed = result.received >= 1
	return result
}

// runGroupBroadcast 场景2: 群组广播（跨节点）
// 向 group-A 发送群组消息，验证所有 appA-nsA 成员（跨节点）都收到
func (e *stressEngine) runGroupBroadcast() scenarioResult {
	result := scenarioResult{
		name:        "群组广播（跨节点）",
		description: "向 group-A 群组发送消息，验证 appA-nsA 全员跨节点收到",
	}

	// 重置所有客户端计数
	for _, c := range e.clients {
		c.received.Store(0)
	}

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeNotice).
		SetContent("Group broadcast test").
		SetMessageID("group-test-001").
		SetSender("stress-sender")
	msg.SetRequireAck(true)

	// 注入路由信封（appA/nsA/group-A）
	ctx := routing.NewRoute().
		WithAppID(appA).
		WithNamespace(nsA).
		WithGroupIDs([]string{gA}).
		Inject(context.Background())

	dr := e.nodes[0].hub.Deliver(ctx, msg, false)
	result.sent = 1

	// 统计 appA-nsA 客户端数量（预期接收数）
	for _, c := range e.clients {
		if c.profile.label == "appA-nsA" {
			result.expected++
		}
	}

	if dr != nil {
		log.Printf("   Deliver 结果: TotalMembers=%d, Online=%d, Sent=%d, StoredOffline=%d",
			dr.TotalMembers, dr.OnlineMembers, dr.Sent, dr.StoredOffline)
	}

	time.Sleep(receiveWait)

	// 统计实际收到消息的 appA-nsA 客户端数
	for _, c := range e.clients {
		if c.profile.label == "appA-nsA" && c.received.Load() > 0 {
			result.received++
		}
	}
	result.passed = result.received >= int64(result.expected)
	return result
}

// runNamespaceBroadcast 场景3: 命名空间广播（跨节点）
// 向 ns-A 命名空间广播，验证所有 appA-nsA 客户端收到
func (e *stressEngine) runNamespaceBroadcast() scenarioResult {
	result := scenarioResult{
		name:        "命名空间广播（跨节点）",
		description: "向 appA/ns-A 命名空间广播，验证 appA-nsA 全员收到",
	}

	for _, c := range e.clients {
		c.received.Store(0)
	}

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeNotice).
		SetContent("Namespace broadcast test").
		SetMessageID("ns-broadcast-001").
		SetSender("stress-sender")

	// 注入路由信封（appA/nsA），groupIDs=nil → 走命名空间广播分支
	ctx := routing.NewRoute().
		WithAppID(appA).
		WithNamespace(nsA).
		Inject(context.Background())

	e.nodes[0].hub.Deliver(ctx, msg, false)
	result.sent = 1

	for _, c := range e.clients {
		if c.profile.label == "appA-nsA" {
			result.expected++
		}
	}

	time.Sleep(receiveWait)

	for _, c := range e.clients {
		if c.profile.label == "appA-nsA" && c.received.Load() > 0 {
			result.received++
		}
	}
	// 命名空间广播是 fire-and-forget，允许部分丢失但应大部分收到
	result.passed = result.received >= int64(result.expected)*8/10
	return result
}

// runAppIDIsolation 场景4: appID 隔离验证
// 向 appA/nsA 发送消息，验证 appB 客户端不收到（不同 appID）
func (e *stressEngine) runAppIDIsolation() scenarioResult {
	result := scenarioResult{
		name:        "appID 隔离验证",
		description: "向 appA 发送消息，验证 appB 客户端不收到",
	}

	for _, c := range e.clients {
		c.received.Store(0)
	}

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeNotice).
		SetContent("AppID isolation test").
		SetMessageID("iso-app-001").
		SetSender("stress-sender")

	// 向 appA/nsA 命名空间广播
	ctx := routing.NewRoute().
		WithAppID(appA).
		WithNamespace(nsA).
		Inject(context.Background())

	e.nodes[0].hub.Deliver(ctx, msg, false)
	result.sent = 1

	time.Sleep(receiveWait)

	// 验证 appB-nsB 客户端没有收到（appID 隔离）
	var leaked int64
	for _, c := range e.clients {
		if c.profile.label == "appB-nsB" && c.received.Load() > 0 {
			leaked++
		}
	}
	// appB 客户端不应收到任何 appA 的消息
	result.expected = 0
	result.received = leaked
	result.passed = leaked == 0
	return result
}

// runNamespaceIsolation 场景5: namespace 隔离验证
// 向 appA/nsA 发送消息，验证 appA/nsB 客户端不收到（同 appID 不同 namespace）
func (e *stressEngine) runNamespaceIsolation() scenarioResult {
	result := scenarioResult{
		name:        "namespace 隔离验证",
		description: "向 appA/nsA 发送消息，验证 appA/nsB 客户端不收到",
	}

	for _, c := range e.clients {
		c.received.Store(0)
	}

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeNotice).
		SetContent("Namespace isolation test").
		SetMessageID("iso-ns-001").
		SetSender("stress-sender")

	ctx := routing.NewRoute().
		WithAppID(appA).
		WithNamespace(nsA).
		Inject(context.Background())

	e.nodes[0].hub.Deliver(ctx, msg, false)
	result.sent = 1

	time.Sleep(receiveWait)

	// 验证 appA-nsB 客户端没有收到（namespace 隔离）
	var leaked int64
	for _, c := range e.clients {
		if c.profile.label == "appA-nsB" && c.received.Load() > 0 {
			leaked++
		}
	}
	result.expected = 0
	result.received = leaked
	result.passed = leaked == 0
	return result
}

// ============================================================================
// 报告
// ============================================================================

func (e *stressEngine) printReport(results []scenarioResult) {
	fmt.Println("\n" + "================================================================")
	fmt.Println("              跨节点 WS 消息分发压测报告")
	fmt.Println("================================================================")

	// 节点信息
	fmt.Printf("\n📡 节点数: %d\n", len(e.nodes))
	for i, n := range e.nodes {
		fmt.Printf("   node-%d: id=%s, port=%d\n", i, n.id, n.port)
	}
	fmt.Printf("👥 客户端总数: %d\n", len(e.clients))

	// 各画像客户端数
	profileCount := make(map[string]int)
	for _, c := range e.clients {
		profileCount[c.profile.label]++
	}
	fmt.Println("📋 客户端画像:")
	for _, p := range profiles {
		fmt.Printf("   %s (app=%s, ns=%s, group=%s): %d 连接\n",
			p.label, p.appID, p.namespace, p.groupID, profileCount[p.label])
	}

	// 场景结果
	fmt.Println("\n🧪 测试场景结果:")
	fmt.Println("----------------------------------------------------------------")
	fmt.Printf("%-28s %-8s %-10s %-10s %-8s\n", "场景", "发送", "预期接收", "实际接收", "结果")
	fmt.Println("----------------------------------------------------------------")
	allPassed := true
	for _, r := range results {
		status := "❌ FAIL"
		if r.passed {
			status = "✅ PASS"
		} else {
			allPassed = false
		}
		fmt.Printf("%-28s %-8d %-10d %-10d %-8s\n", r.name, r.sent, r.expected, r.received, status)
	}
	fmt.Println("----------------------------------------------------------------")

	if allPassed {
		fmt.Println("\n🎉 全部场景通过！跨节点消息分发与隔离验证成功。")
	} else {
		fmt.Println("\n⚠️ 部分场景未通过，请检查上方详细数据。")
	}
	fmt.Println("================================================================")
}

// ============================================================================
// teardown
// ============================================================================

func (e *stressEngine) teardown() {
	// 关闭所有客户端
	for _, c := range e.clients {
		c.close()
	}
	// 关闭所有节点
	for _, n := range e.nodes {
		n.shutdown()
	}
}

// main 入口
func main() {
	numNodes := flag.Int("nodes", defaultNodes, "模拟节点数（Pod 数）")
	numClients := flag.Int("clients", defaultClients, "WS 客户端总数")
	basePort := flag.Int("base-port", defaultBasePort, "节点 HTTP 起始端口")
	redisAddr := flag.String("redis", "", "Redis 地址（空=使用 miniredis）")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("🧪 跨节点 WS 消息分发压测启动 (nodes=%d, clients=%d)", *numNodes, *numClients)

	engine := &stressEngine{
		clients: make([]*stressClient, 0),
	}

	// 1. 启动节点 + 连接客户端
	engine.setup(*numNodes, *numClients, *basePort, *redisAddr)

	// 等待所有客户端注册完成（心跳同步在线状态到 Redis）
	log.Println("⏳ 等待客户端在线状态同步到 Redis...")
	time.Sleep(3 * time.Second)

	// 调试：打印每节点实际注册的客户端数
	for i, n := range engine.nodes {
		log.Printf("🔍 node-%d 客户端数: %d", i, n.hub.GetClientsCount())
	}

	// 2. 执行测试场景
	var results []scenarioResult

	log.Println("\n▶ 场景 1/5: P2P 跨节点投递")
	results = append(results, engine.runP2PCrossNode())

	log.Println("\n▶ 场景 2/5: 群组广播（跨节点）")
	results = append(results, engine.runGroupBroadcast())

	log.Println("\n▶ 场景 3/5: 命名空间广播（跨节点）")
	results = append(results, engine.runNamespaceBroadcast())

	log.Println("\n▶ 场景 4/5: appID 隔离验证")
	results = append(results, engine.runAppIDIsolation())

	log.Println("\n▶ 场景 5/5: namespace 隔离验证")
	results = append(results, engine.runNamespaceIsolation())

	// 3. 打印报告
	engine.printReport(results)

	// 4. 清理
	engine.teardown()
	log.Println("✅ 压测结束")
}
