/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-13 23:15:21
 * @FilePath: \go-wsc\hub\observer_test.go
 * @Description: handleBroadcast 观察者按 namespace+groupID 路由回归测试
 *
 * 覆盖场景：
 *   1. namespace 路由：命名空间级观察者命中/隔离/向后兼容（空值仅全局）
 *   2. groupID 路由：群组级观察者命中/隔离/向后兼容（空值不含群组级）
 *   3. namespace+groupID 组合与依赖（groupID 需配 namespace）
 *   4. SetNamespace/SetGroupID 链式方法
 *   5. HubMessage JSON omitempty（namespace/group_id）
 *   6. Clone 保留 namespace/groupID
 *   7. 并发安全（handleBroadcast 与观察者增删并发）
 *
 * 复用 group_test.go 中的 setupGroupTestHub / makeTestClient / makeGroupMessage 等 helper
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 测试 helper
// ============================================================================

// registerObserver 注册一个 UserTypeObserver 客户端并返回，便于断言收发
func registerObserver(hub *Hub, clientID, userID, namespace, groupID string) *Client {
	obs := makeTestClient(clientID, userID)
	obs.UserType = UserTypeObserver
	obs.WithNamespace(namespace)
	if groupID != "" {
		obs.SetGroupID(groupID)
	}
	hub.shardedRegistry.AddClient(obs)
	return obs
}

// makeObserverMsg 构造一条只走观察者通知路径、不命中任何客户端直投的消息：
// BroadcastType=Session + Receiver 不存在，确保观察者仅从 notifyObservers 收到
// namespace/groupID 不再由消息携带，而是通过 sender client 的 Namespace/GroupID 传递
func makeObserverMsg() *HubMessage {
	msg := makeGroupMessage("sender-obs")
	msg.BroadcastType = BroadcastTypeSession
	msg.Receiver = "no-such-target-user" // 不命中任何客户端，避免 handleDirectMessage 投递观察者
	return msg
}

// registerSender 注册一个发送者客户端（handleBroadcast 从 sender client 提取 namespace/groupID）
func registerSender(hub *Hub, namespace, groupID string) *Client {
	sender := makeTestClient("c-sender", "sender-obs")
	sender.WithNamespace(namespace)
	if groupID != "" {
		sender.SetGroupID(groupID)
	}
	hub.shardedRegistry.AddClient(sender)
	return sender
}

// waitForObserverMsg 在超时内从观察者 SendChan 读取一条消息，
// 并校验其为观察者投递路径（带 observer_mode=true），返回是否收到
func waitForObserverMsg(t *testing.T, c *Client, timeout time.Duration) (HubMessage, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case data := <-c.SendChan:
			var m HubMessage
			require.NoError(t, json.Unmarshal(data, &m))
			// 确认是观察者投递路径（带 observer_mode=true），而非客户端直投
			v, ok := m.GetMetadata("observer_mode")
			if !ok || v != "true" {
				t.Fatalf("观察者 %s 收到的消息应带 observer_mode=true，实际 data=%v", c.ID, m.Data)
			}
			return m, true
		case <-time.After(20 * time.Millisecond):
		}
	}
	return HubMessage{}, false
}

func assertObserverReceived(t *testing.T, c *Client, desc string) {
	t.Helper()
	if _, ok := waitForObserverMsg(t, c, 500*time.Millisecond); !ok {
		t.Fatalf("%s：观察者 %s 应收到消息但未收到", desc, c.ID)
	}
}

func assertObserverNotReceived(t *testing.T, c *Client, desc string) {
	t.Helper()
	if _, ok := waitForObserverMsg(t, c, 200*time.Millisecond); ok {
		t.Fatalf("%s：观察者 %s 不应收到消息但收到了", desc, c.ID)
	}
}

// drainObserver 清空观察者残留消息，避免串扰
func drainObserver(c *Client) {
	for {
		select {
		case <-c.SendChan:
		default:
			return
		}
	}
}

// ============================================================================
// namespace 路由测试
// ============================================================================

// TestHandleBroadcastObserver_NamespaceRouting 验证 handleBroadcast 按 msg.Namespace 通知：
// namespace="ns1" 时「全局 + ns1 命名空间级」观察者收到，ns2 命名空间级观察者收不到
func TestHandleBroadcastObserver_NamespaceRouting(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	globalObs := registerObserver(hub, "c-g", "u-g", "", "")     // 全局观察者
	ns1Obs := registerObserver(hub, "c-ns1", "u-ns1", "ns1", "") // ns1 命名空间级
	ns2Obs := registerObserver(hub, "c-ns2", "u-ns2", "ns2", "") // ns2 命名空间级

	registerSender(hub, "ns1", "")
	hub.handleBroadcast(makeObserverMsg())

	assertObserverReceived(t, globalObs, "全局观察者应收到 ns1 消息")
	assertObserverReceived(t, ns1Obs, "ns1 观察者应收到 ns1 消息")
	assertObserverNotReceived(t, ns2Obs, "ns2 观察者不应收到 ns1 消息")
}

// TestHandleBroadcastObserver_EmptyNamespace_BackwardCompat 验证空 namespace 向后兼容：
// 仅全局观察者收到，命名空间级观察者收不到（修复前 handleBroadcast 硬编码 "" 的行为）
func TestHandleBroadcastObserver_EmptyNamespace_BackwardCompat(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	globalObs := registerObserver(hub, "c-g", "u-g", "", "")
	ns1Obs := registerObserver(hub, "c-ns1", "u-ns1", "ns1", "")

	registerSender(hub, "", "")
	hub.handleBroadcast(makeObserverMsg())

	assertObserverReceived(t, globalObs, "全局观察者应收到空 namespace 消息")
	assertObserverNotReceived(t, ns1Obs, "ns1 观察者不应收到空 namespace 消息")
}

// TestHandleBroadcastObserver_NamespaceIsolation 验证多租户命名空间隔离：
// ns1 消息不进 ns2 观察者，ns2 消息不进 ns1 观察者
func TestHandleBroadcastObserver_NamespaceIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ns1Obs := registerObserver(hub, "c-ns1", "u-ns1", "tenantA", "")
	ns2Obs := registerObserver(hub, "c-ns2", "u-ns2", "tenantB", "")

	// ns1 消息
	registerSender(hub, "tenantA", "")
	hub.handleBroadcast(makeObserverMsg())
	assertObserverReceived(t, ns1Obs, "tenantA 观察者应收到 tenantA 消息")
	assertObserverNotReceived(t, ns2Obs, "tenantB 观察者不应收到 tenantA 消息")

	drainObserver(ns1Obs)
	drainObserver(ns2Obs)

	// ns2 消息
	registerSender(hub, "tenantB", "")
	hub.handleBroadcast(makeObserverMsg())
	assertObserverReceived(t, ns2Obs, "tenantB 观察者应收到 tenantB 消息")
	assertObserverNotReceived(t, ns1Obs, "tenantA 观察者不应收到 tenantB 消息")
}

// ============================================================================
// groupID 路由测试
// ============================================================================

// TestHandleBroadcastObserver_GroupIDRouting 验证 groupID 命中群组级观察者：
// namespace="ns1"+groupID="g1" 时「全局 + ns1 命名空间级 + ns1:g1 群组级」收到，
// ns1:g2 群组级观察者收不到
func TestHandleBroadcastObserver_GroupIDRouting(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	globalObs := registerObserver(hub, "c-g", "u-g", "", "")
	ns1Obs := registerObserver(hub, "c-ns1", "u-ns1", "ns1", "")
	g1Obs := registerObserver(hub, "c-g1", "u-g1", "ns1", "g1") // ns1:g1 群组级
	g2Obs := registerObserver(hub, "c-g2", "u-g2", "ns1", "g2") // ns1:g2 群组级

	registerSender(hub, "ns1", "g1")
	hub.handleBroadcast(makeObserverMsg())

	assertObserverReceived(t, globalObs, "全局观察者应收到")
	assertObserverReceived(t, ns1Obs, "ns1 命名空间级观察者应收到")
	assertObserverReceived(t, g1Obs, "ns1:g1 群组级观察者应收到")
	assertObserverNotReceived(t, g2Obs, "ns1:g2 群组级观察者不应收到")
}

// TestHandleBroadcastObserver_EmptyGroupID_BackwardCompat 验证空 groupID 向后兼容：
// namespace="ns1"+groupID="" 时「全局 + ns1 命名空间级」收到，群组级观察者收不到
// （修复前 handleBroadcast 硬编码 groupID="" 的行为）
func TestHandleBroadcastObserver_EmptyGroupID_BackwardCompat(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	globalObs := registerObserver(hub, "c-g", "u-g", "", "")
	ns1Obs := registerObserver(hub, "c-ns1", "u-ns1", "ns1", "")
	g1Obs := registerObserver(hub, "c-g1", "u-g1", "ns1", "g1")

	registerSender(hub, "ns1", "")
	hub.handleBroadcast(makeObserverMsg())

	assertObserverReceived(t, globalObs, "全局观察者应收到")
	assertObserverReceived(t, ns1Obs, "ns1 命名空间级观察者应收到")
	assertObserverNotReceived(t, g1Obs, "群组级观察者在 groupID 为空时不应收到")
}

// TestHandleBroadcastObserver_GroupIDRequiresNamespace 验证 groupID 依赖 namespace：
// groupID 非空但 namespace 为空时，群组级观察者不命中（byGroup 索引键为 namespace:groupID）
func TestHandleBroadcastObserver_GroupIDRequiresNamespace(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	globalObs := registerObserver(hub, "c-g", "u-g", "", "")
	// 群组级观察者需带 namespace 才能进 byGroup 索引；namespace="" 的群组级观察者语义为「全局:groupID」
	globalG1Obs := registerObserver(hub, "c-gg1", "u-gg1", "", "g1")
	ns1G1Obs := registerObserver(hub, "c-ns1g1", "u-ns1g1", "ns1", "g1")

	// namespace="" + groupID="g1"：命中「全局 + 全局:g1」，不命中 ns1:g1
	registerSender(hub, "", "g1")
	hub.handleBroadcast(makeObserverMsg())

	assertObserverReceived(t, globalObs, "全局观察者应收到")
	assertObserverReceived(t, globalG1Obs, "全局:g1 群组级观察者应收到（namespace 空匹配）")
	assertObserverNotReceived(t, ns1G1Obs, "ns1:g1 群组级观察者在消息 namespace 为空时不应收到")
}

// ============================================================================
// 组合与边界测试
// ============================================================================

// TestHandleBroadcastObserver_ThreeLevelAllHit 验证三级观察者同时命中：
// namespace+groupID 均设，全局/命名空间级/群组级观察者同时收到
func TestHandleBroadcastObserver_ThreeLevelAllHit(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	globalObs := registerObserver(hub, "c-g", "u-g", "", "")
	nsObs := registerObserver(hub, "c-ns", "u-ns", "tenantX", "")
	grpObs := registerObserver(hub, "c-grp", "u-grp", "tenantX", "session-1")

	registerSender(hub, "tenantX", "session-1")
	hub.handleBroadcast(makeObserverMsg())

	assertObserverReceived(t, globalObs, "全局")
	assertObserverReceived(t, nsObs, "命名空间级")
	assertObserverReceived(t, grpObs, "群组级")
}

// TestHandleBroadcastObserver_DifferentGroupIDsIsolation 验证同命名空间不同群组隔离
func TestHandleBroadcastObserver_DifferentGroupIDsIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	g1Obs := registerObserver(hub, "c-g1", "u-g1", "ns", "group-1")
	g2Obs := registerObserver(hub, "c-g2", "u-g2", "ns", "group-2")

	registerSender(hub, "ns", "group-1")
	hub.handleBroadcast(makeObserverMsg())
	assertObserverReceived(t, g1Obs, "group-1 观察者应收到 group-1 消息")
	assertObserverNotReceived(t, g2Obs, "group-2 观察者不应收到 group-1 消息")
}

// TestHandleBroadcastObserver_MultipleMessagesNoCrossTalk 验证连续多条不同 namespace 消息不串扰
func TestHandleBroadcastObserver_MultipleMessagesNoCrossTalk(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ns1Obs := registerObserver(hub, "c-ns1", "u-ns1", "ns1", "")
	ns2Obs := registerObserver(hub, "c-ns2", "u-ns2", "ns2", "")

	// 交替发 ns1/ns2 消息
	for i := 0; i < 3; i++ {
		registerSender(hub, "ns1", "")
		hub.handleBroadcast(makeObserverMsg())
		registerSender(hub, "ns2", "")
		hub.handleBroadcast(makeObserverMsg())
	}

	// ns1 观察者应收到 3 条，ns2 观察者应收到 3 条
	ns1Count := countObserverMsgs(t, ns1Obs, time.Second)
	ns2Count := countObserverMsgs(t, ns2Obs, time.Second)
	assert.Equal(t, 3, ns1Count, "ns1 观察者应收到 3 条消息")
	assert.Equal(t, 3, ns2Count, "ns2 观察者应收到 3 条消息")
}

// countObserverMsgs 在超时内统计观察者收到的消息数（均须带 observer_mode=true）
func countObserverMsgs(t *testing.T, c *Client, timeout time.Duration) int {
	t.Helper()
	count := 0
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case data := <-c.SendChan:
			var m HubMessage
			require.NoError(t, json.Unmarshal(data, &m))
			v, ok := m.GetMetadata("observer_mode")
			if ok && v == "true" {
				count++
			}
		default:
			// 队列空时短暂等待再试，给 batcher flush 时间
			time.Sleep(10 * time.Millisecond)
		}
	}
	return count
}

// ============================================================================
// JSON 序列化测试
// ============================================================================

// TestHubMessage_NamespaceGroupID_JSONRoundTrip 验证序列化往返保留内容字段（namespace/groupID 已从 HubMessage 移至 context）
func TestHubMessage_NamespaceGroupID_JSONRoundTrip(t *testing.T) {
	orig := NewHubMessage().SetContent("hello")
	data, err := json.Marshal(orig)
	require.NoError(t, err)

	var restored HubMessage
	require.NoError(t, json.Unmarshal(data, &restored))
	assert.Equal(t, "hello", restored.Content)
}

// ============================================================================
// Clone 测试
// ============================================================================

// TestHubMessage_Clone_NamespaceGroupID 验证 Clone 保留内容字段且原对象修改不影响副本（namespace/groupID 已从 HubMessage 移至 context）
func TestHubMessage_Clone_NamespaceGroupID(t *testing.T) {
	orig := NewHubMessage().SetContent("hello")
	clone := orig.Clone()

	assert.Equal(t, "hello", clone.Content, "Clone 应保留 content")

	// 修改原对象不影响副本
	orig.SetContent("world")
	assert.Equal(t, "hello", clone.Content, "Clone 副本不受原对象修改影响")
}

// ============================================================================
// 并发安全测试
// ============================================================================

// TestHandleBroadcastObserver_Concurrent 验证并发 handleBroadcast 与观察者增删不产生数据竞争或 fatal
// 建议运行：go test -race -run TestHandleBroadcastObserver_Concurrent
func TestHandleBroadcastObserver_Concurrent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 预注册一批观察者
	var observers []*Client
	for i := 0; i < 10; i++ {
		obs := registerObserver(hub,
			"c-"+itoa(i), "u-"+itoa(i),
			"tenant"+itoa(i%3), "group-"+itoa(i%2))
		observers = append(observers, obs)
	}

	var wg sync.WaitGroup
	wg.Add(3)

	// 1. 并发 handleBroadcast（不同 namespace/groupID）
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			ns := "tenant" + itoa(i%3)
			gid := "group-" + itoa(i%2)
			registerSender(hub, ns, gid)
			hub.handleBroadcast(makeObserverMsg())
		}
	}()

	// 2. 并发注册/注销观察者
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			obs := makeTestClient("c-dyn-"+itoa(i), "u-dyn-"+itoa(i))
			obs.UserType = UserTypeObserver
			obs.WithNamespace("tenant" + itoa(i%3))
			obs.SetGroupID("group-" + itoa(i%2))
			hub.shardedRegistry.AddClient(obs)
			hub.shardedRegistry.RemoveClient(obs.ID, obs.UserID)
		}
	}()

	// 3. 并发消费观察者消息，防止 SendChan 阻塞
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, obs := range observers {
				select {
				case <-obs.SendChan:
				default:
				}
			}
		}
	}()

	wg.Wait()
}

// itoa 简易整数转字符串，避免引入 strconv
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	buf := [20]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
