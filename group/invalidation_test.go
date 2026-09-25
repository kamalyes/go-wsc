/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-25 23:50:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-25 23:50:00
 * @FilePath: \go-wsc\group\invalidation_test.go
 * @Description: 群拓扑失效广播聚合器单元测试
 *
 * 覆盖：高频标记去重合并（100 次同 key → 单消息）、跨 gid 同 app 归组、
 * 跨 app 分组、Flush 后排干（第二次 Flush 空发布）。竞态用例时序敏感不单测，
 * 依赖设计决策记录（回源/失效交错窗口接受 + TTL 兜底）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"
	"sync"
	"testing"

	"github.com/kamalyes/go-wsc/spi"
)

// fakeInvalidationHost 只实现聚合器用到的端口（GetLogger + PublishGroupInvalidations），
// 其余经嵌入 Host 接口静默（被调用即 panic，与域内其他 fakeHost 同惯例）
type fakeInvalidationHost struct {
	Host

	mu        sync.Mutex
	publishes map[string][]string // appID -> 每窗口合并后的 groupIDs（按发布顺序累积）
}

func newFakeInvalidationHost() *fakeInvalidationHost {
	return &fakeInvalidationHost{publishes: make(map[string][]string)}
}

func (f *fakeInvalidationHost) GetLogger() spi.Logger { return spi.NewDefaultLogger() }

// PublishGroupInvalidations 记录发布会话：复制排序后的 groupIDs，避免测试断言与聚合器后续改写共享切片
func (f *fakeInvalidationHost) PublishGroupInvalidations(_ context.Context, appID string, groupIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishes[appID] = append([]string(nil), groupIDs...)
	return nil
}

// TestInvalidationBroadcasterDeduplicatesMarkDirty 高频标记 → 单条合并发布（去重 + 归组）
func TestInvalidationBroadcasterDeduplicatesMarkDirty(t *testing.T) {
	host := newFakeInvalidationHost()
	b := NewInvalidationBroadcaster(host, 0)

	const appA, appB = "app-a", "app-b"
	// 同 (appA, g1) 高频标记应去重为一条；同 app 异 gid 归组；异 app 分组
	for i := 0; i < 100; i++ {
		b.MarkDirty(appA, "g1")
	}
	b.MarkDirty(appA, "g2")
	b.MarkDirty(appB, "g3")

	b.Flush(context.Background())

	host.mu.Lock()
	defer host.mu.Unlock()

	assertSortedEqual(t, []string{"g1", "g2"}, host.publishes[appA])
	assertSortedEqual(t, []string{"g3"}, host.publishes[appB])
	if len(host.publishes) != 2 {
		t.Fatalf("期望 2 个 app 分组发布，实际 %d", len(host.publishes))
	}
}

// TestInvalidationBroadcasterFlushDrains 排干语义：Flush 后残余标记清空，二次 Flush 空发布
func TestInvalidationBroadcasterFlushDrains(t *testing.T) {
	host := newFakeInvalidationHost()
	b := NewInvalidationBroadcaster(host, 0)

	b.MarkDirty("app", "g1")
	b.Flush(context.Background())
	b.Flush(context.Background())

	host.mu.Lock()
	defer host.mu.Unlock()
	// 第二次 Flush 无残余标记，发布应仍只有一次（一次一 gid）
	assertSortedEqual(t, []string{"g1"}, host.publishes["app"])
}

// assertSortedEqual 断言两字符串切片完全相等（聚合器内部已 sort，测试直接比较顺序即可）
func assertSortedEqual(t *testing.T, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("切片长度不相等：want=%v got=%v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("切片第 %d 项不相等：want=%v got=%v", i, want, got)
		}
	}
}
