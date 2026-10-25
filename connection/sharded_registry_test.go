/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-01 22:58:03
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-25 13:15:37
 * @FilePath: \go-wsc\connection\sharded_registry_test.go
 * @Description: 分片注册表 nsIdx 反向索引白盒单元测试（覆盖 connection/sharded_registry.go）
 *
 * 覆盖场景：
 *   1. 多端引用计数（同 userID 多连接聚合为单用户，注销递减归零删除）
 *   2. appID/namespace 双维度隔离（索引键 appID:namespace 不串扰）
 *   3. 空 namespace 守卫（全局广播语义禁止从索引取数）
 *   4. 断线重连覆盖语义（同 clientID 重复注册不重复累加引用计数）
 *   5. Clear 全量清空
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kamalyes/go-wsc/models"
)

// makeNSClient 构造指定 appID+namespace 的测试客户端（nsIdx 建键维度）
// 空值保持 models.NewClient 默认（AppID/Namespace 归一化基准值）
func makeNSClient(clientID, userID, appID, namespace string) *models.Client {
	c := models.NewClient(clientID, userID, models.UserTypeCustomer)
	if appID != "" {
		c.AppID = appID
	}
	if namespace != "" {
		c.Namespace = namespace
	}
	return c
}

// assertUserListEqual 断言 userID 列表元素相等（无序，索引查询不保证顺序）
func assertUserListEqual(t *testing.T, got []string, want ...string) {
	t.Helper()
	assert.Len(t, got, len(want))
	gotSet := make(map[string]struct{}, len(got))
	for _, uid := range got {
		gotSet[uid] = struct{}{}
	}
	for _, w := range want {
		assert.Contains(t, gotSet, w, "用户 %s 应在索引结果中", w)
	}
}

// TestNsIdx_MultiDeviceRefcount 多端登录引用计数：
// 同 userID 多连接逐一累加，注销递减，归零删除用户条目
func TestNsIdx_MultiDeviceRefcount(t *testing.T) {
	r := NewShardedRegistry(false, false, RegistryCapacity{})

	r.AddClient(makeNSClient("c-1", "u-1", "", "ns-a"))
	r.AddClient(makeNSClient("c-2", "u-1", "", "ns-a"))
	r.AddClient(makeNSClient("c-3", "u-2", "", "ns-a"))

	// 3 连接聚合为 2 用户（设备去重）
	assertUserListEqual(t, r.GetLocalNamespaceUsers("", "ns-a"), "u-1", "u-2")
	assert.EqualValues(t, 3, r.GetClientCount())

	// 注销 u-1 的一个设备：引用计数 2→1，仍在索引中
	r.RemoveClient("c-1", "u-1")
	assertUserListEqual(t, r.GetLocalNamespaceUsers("", "ns-a"), "u-1", "u-2")

	// 注销 u-1 的最后一个设备：引用计数归零，用户条目删除
	r.RemoveClient("c-2", "u-1")
	assertUserListEqual(t, r.GetLocalNamespaceUsers("", "ns-a"), "u-2")
}

// TestNsIdx_AppIDIsolation 索引键 appID 维度隔离：
// 同 namespace 跨 appID 的连接互不串扰（appID 是应用隔离维度，严格匹配）
func TestNsIdx_AppIDIsolation(t *testing.T) {
	r := NewShardedRegistry(false, false, RegistryCapacity{})

	r.AddClient(makeNSClient("c-a1", "u-a1", "app-a", "ns-shared"))
	r.AddClient(makeNSClient("c-b1", "u-b1", "app-b", "ns-shared"))

	assertUserListEqual(t, r.GetLocalNamespaceUsers("app-a", "ns-shared"), "u-a1")
	assertUserListEqual(t, r.GetLocalNamespaceUsers("app-b", "ns-shared"), "u-b1")
	// DefaultAppID 维度无连接（空 appID 查询归一化为 DefaultAppID）
	assert.Empty(t, r.GetLocalNamespaceUsers("", "ns-shared"))
}

// TestNsIdx_NamespaceIsolation 索引键 namespace 维度隔离：
// 同 appID 不同 namespace 的连接互不串扰
func TestNsIdx_NamespaceIsolation(t *testing.T) {
	r := NewShardedRegistry(false, false, RegistryCapacity{})

	r.AddClient(makeNSClient("c-1", "u-1", "", "ns-alpha"))
	r.AddClient(makeNSClient("c-2", "u-2", "", "ns-beta"))

	assertUserListEqual(t, r.GetLocalNamespaceUsers("", "ns-alpha"), "u-1")
	assertUserListEqual(t, r.GetLocalNamespaceUsers("", "ns-beta"), "u-2")
}

// TestNsIdx_EmptyNamespaceGuard 空 namespace 守卫：
// 空 ns = 全局广播语义（目标集=全部连接，全量扫描最优），禁止从索引取数
func TestNsIdx_EmptyNamespaceGuard(t *testing.T) {
	r := NewShardedRegistry(false, false, RegistryCapacity{})

	r.AddClient(makeNSClient("c-1", "u-1", "", "default"))

	assert.Nil(t, r.GetLocalNamespaceUsers("", ""))
}

// TestNsIdx_ReconnectOverwriteNoDoubleCount 断线重连覆盖语义：
// 同 clientID 重复注册（isNew=false）不重复累加引用计数，一次注销即归零
func TestNsIdx_ReconnectOverwriteNoDoubleCount(t *testing.T) {
	r := NewShardedRegistry(false, false, RegistryCapacity{})

	r.AddClient(makeNSClient("c-1", "u-1", "", "ns-a"))
	// 覆盖场景：同 clientID 再次注册，仅刷新指针，引用计数不变
	r.AddClient(makeNSClient("c-1", "u-1", "", "ns-a"))

	assertUserListEqual(t, r.GetLocalNamespaceUsers("", "ns-a"), "u-1")

	// 若覆盖场景错误地重复累加，此处会残留幽灵用户
	r.RemoveClient("c-1", "u-1")
	assert.Empty(t, r.GetLocalNamespaceUsers("", "ns-a"))
}

// TestNsIdx_ClearResetsIndex Clear 全量清空索引（防内存泄漏）
func TestNsIdx_ClearResetsIndex(t *testing.T) {
	r := NewShardedRegistry(false, false, RegistryCapacity{})

	r.AddClient(makeNSClient("c-1", "u-1", "", "ns-a"))
	r.AddClient(makeNSClient("c-2", "u-2", "", "ns-b"))

	r.Clear()

	assert.Empty(t, r.GetLocalNamespaceUsers("", "ns-a"))
	assert.Empty(t, r.GetLocalNamespaceUsers("", "ns-b"))
}

func traverseRegistry(b *testing.B, users int) *ShardedRegistry {
	b.Helper()
	r := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: users})
	for i := 0; i < users; i++ {
		uid := fmt.Sprintf("u-%06d", i)
		c := models.NewClient(fmt.Sprintf("c-%06d", i), uid, models.UserTypeCustomer)
		r.AddClient(c)
	}
	return r
}

// BenchmarkTraverse_Serial 串行遍历基线
func BenchmarkTraverse_Serial(b *testing.B) {
	r := traverseRegistry(b, 50000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n := 0
		r.ForEachClient(func(_ string, c *models.Client) bool {
			if !c.IsClosed() {
				n++
			}
			return true
		})
		if n == 0 {
			b.Fatal("zero scanned")
		}
	}
}

// BenchmarkTraverse_Parallel 并行遍历（期望显著快于串行；若持平说明退化为串行）
func BenchmarkTraverse_Parallel(b *testing.B) {
	r := traverseRegistry(b, 50000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var n int64
		r.ForEachClientParallel(0, func(_ string, c *models.Client) {
			if !c.IsClosed() {
				n++ // 故意非原子：只测遍历骨架开销，不引入计数争用
			}
		})
	}
}

// BenchmarkTraverse_UserShardLen 确认 Len() 语义（用户数，决定是否过并行阈值）
func BenchmarkTraverse_UserShardLen(b *testing.B) {
	r := traverseRegistry(b, 50000)
	b.Logf("userShards.Len()=%d parallelThreshold=%d 并行分支=%v",
		r.userShards.Len(), parallelThreshold, r.userShards.Len() > parallelThreshold)
	b.ResetTimer()
	for b.Loop() {
		_ = r.userShards.Len()
	}
}
