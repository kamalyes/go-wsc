/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-17 15:12:26
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-17 15:12:26
 * @FilePath: \go-wsc\hub\node_index_cache_test.go
 * @Description: 节点索引短 TTL 缓存单测 — 命中复用 / 信封隔离 / 过期逐出 / 容量降级
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeIndexCacheHitAndExpire(t *testing.T) {
	c := &nodeIndexCache{ttl: 30 * time.Millisecond}
	key := nodeQueryKey{appID: "app-1", namespace: "ns-1", userID: "u-1"}

	_, ok := c.load(key)
	assert.False(t, ok, "空缓存未命中")

	nodes := []string{"node-a", "node-b"}
	c.store(key, nodes)

	got, ok := c.load(key)
	require.True(t, ok, "TTL 窗口内命中")
	assert.Equal(t, nodes, got)
	assert.EqualValues(t, 1, c.size.Load())

	// 窗口外过期：惰性逐出后按未命中处理，计数回落
	time.Sleep(40 * time.Millisecond)
	_, ok = c.load(key)
	assert.False(t, ok, "TTL 过期未命中")
	assert.EqualValues(t, 0, c.size.Load(), "过期条目已逐出")
}

func TestNodeIndexCacheEnvelopeIsolated(t *testing.T) {
	c := &nodeIndexCache{ttl: time.Second}
	base := nodeQueryKey{appID: "app-1", namespace: "ns-1", userID: "u-1"}
	c.store(base, []string{"node-a"})

	// 同 userID 不同 app/ns 三元组互不共享（与 in-flight 合并器同一隔离语义）
	for _, key := range []nodeQueryKey{
		{appID: "app-2", namespace: "ns-1", userID: "u-1"},
		{appID: "app-1", namespace: "ns-2", userID: "u-1"},
		{appID: "app-1", namespace: "ns-1", userID: "u-2"},
	} {
		_, ok := c.load(key)
		assert.False(t, ok, "不同信封三元组不应命中: %+v", key)
	}
	assert.EqualValues(t, 1, c.size.Load())
}

func TestNodeIndexCacheOverCapEvictsExpired(t *testing.T) {
	c := &nodeIndexCache{ttl: 10 * time.Millisecond, max: 2}
	for i := range 2 {
		c.store(nodeQueryKey{appID: "app", namespace: "ns", userID: string(rune('a' + i))}, []string{"node-x"})
	}
	time.Sleep(20 * time.Millisecond) // 既有条目全部过期

	// 第 3 次写入触达上限：全量清扫回收过期条目后写入成功
	fresh := nodeQueryKey{appID: "app", namespace: "ns", userID: "u-fresh"}
	c.store(fresh, []string{"node-y"})

	_, ok := c.load(fresh)
	assert.True(t, ok, "清扫后应写入成功")
	assert.EqualValues(t, 1, c.size.Load(), "过期条目已随清扫回收")
}

func TestNodeIndexCacheOverCapDiscardsWrite(t *testing.T) {
	c := &nodeIndexCache{ttl: time.Second, max: 2}
	for i := range 2 {
		c.store(nodeQueryKey{appID: "app", namespace: "ns", userID: string(rune('a' + i))}, []string{"node-x"})
	}

	// 活条目占满容量：清扫无法回收，本次写入降级放弃（正确性不受影响）
	overflow := nodeQueryKey{appID: "app", namespace: "ns", userID: "u-overflow"}
	c.store(overflow, []string{"node-y"})

	_, ok := c.load(overflow)
	assert.False(t, ok, "超限写入应降级放弃")
	assert.EqualValues(t, 2, c.size.Load(), "放弃写入不占用容量")
}

func TestNodeIndexCacheConcurrentAccess(t *testing.T) {
	c := &nodeIndexCache{ttl: 50 * time.Millisecond}
	key := nodeQueryKey{appID: "app-1", namespace: "ns-1", userID: "u-hot"}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				c.store(key, []string{"node-a", "node-b"})
				c.load(key)
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, c.size.Load(), int64(1), "同 key 并发覆盖后至多存活一个条目")
}
