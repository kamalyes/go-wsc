/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 12:51:33
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 00:00:00
 * @FilePath: \go-wsc\connection\slow_consumer_test.go
 * @Description: 慢消费者治理测试 - 三级递进（记录 → 告警 → 驱逐）+ 驱逐前消息保全 + 状态恢复清零
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// fakeEvictHook 记录驱逐回调的测试桩
type fakeEvictHook struct {
	evicted []evictedRecord
}

type evictedRecord struct {
	client      *models.Client
	consecutive int
	ratio       float64
}

func (f *fakeEvictHook) OnSlowConsumerEvicted(client *models.Client, consecutive int, ratio float64) {
	f.evicted = append(f.evicted, evictedRecord{client: client, consecutive: consecutive, ratio: ratio})
}

// newScannerRegistry 构造带 1 个指定积压率客户端的注册表与扫描器
func newScannerRegistry(t *testing.T, client *models.Client) *ShardedRegistry {
	t.Helper()
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	registry.AddClient(client)
	return registry
}

// TestSlowConsumerThreeStages 三级递进：第 1 轮记录 → 第 2 轮告警 → 第 3 轮驱逐
func TestSlowConsumerThreeStages(t *testing.T) {
	client := models.NewClient("slow-1", "u-1000", models.UserTypeCustomer)
	client.Context = context.Background()
	client.SendChan = make(chan []byte, 10)
	client.SetBacklogRatio(9, 10) // 0.9 ≥ 阈值 0.9，计一次

	hook := &fakeEvictHook{}
	scanner := NewSlowConsumerScanner(newScannerRegistry(t, client), hook, time.Hour, nil)

	scanner.ScanOnce() // 一级：记录
	require.Empty(t, hook.evicted)
	assert.Equal(t, 1, scanner.Stats(), "首轮应处于追踪状态")

	scanner.ScanOnce() // 二级：告警（warned 置位，无驱逐）
	require.Empty(t, hook.evicted)

	scanner.ScanOnce() // 三级：驱逐
	require.Len(t, hook.evicted, 1, "连续 3 次超阈值应触发驱逐")
	assert.Equal(t, 3, hook.evicted[0].consecutive)
	assert.GreaterOrEqual(t, hook.evicted[0].ratio, 0.9)
	assert.Equal(t, 0, scanner.Stats(), "驱逐后状态应清零")
}

// TestSlowConsumerEvictSalvage 驱逐前保全：SendChan 残留消息被排空（移交 ACK 链路兜底）
func TestSlowConsumerEvictSalvage(t *testing.T) {
	client := models.NewClient("slow-2", "u-2000", models.UserTypeAgent)
	client.Context = context.Background()
	client.SendChan = make(chan []byte, 8)
	// 残留 3 条未投递消息（驱逐前保全的测试对象）
	client.SendChan <- []byte(`{"msg":1}`)
	client.SendChan <- []byte(`{"msg":2}`)
	client.SendChan <- []byte(`{"msg":3}`)
	client.SetBacklogRatio(9, 10)

	hook := &fakeEvictHook{}
	scanner := NewSlowConsumerScanner(newScannerRegistry(t, client), hook, time.Hour, nil)

	// 连续 3 轮触发驱逐
	scanner.ScanOnce()
	scanner.ScanOnce()
	scanner.ScanOnce()

	require.Len(t, hook.evicted, 1)
	assert.Equal(t, 0, len(client.SendChan), "驱逐前应排空 SendChan 残留消息（消息保全）")
}

// TestSlowConsumerRecoveryReset 积压恢复：低于阈值即清零历史计数（迟滞清除防误伤）
func TestSlowConsumerRecoveryReset(t *testing.T) {
	client := models.NewClient("recover-1", "u-3000", models.UserTypeCustomer)
	client.Context = context.Background()
	client.SendChan = make(chan []byte, 10)
	client.SetBacklogRatio(9, 10) // 0.9：第 1 轮计数 1

	hook := &fakeEvictHook{}
	scanner := NewSlowConsumerScanner(newScannerRegistry(t, client), hook, time.Hour, nil)

	scanner.ScanOnce()
	assert.Equal(t, 1, scanner.Stats())

	// 消费恢复：利用率降到阈值之下
	client.SetBacklogRatio(2, 10) // 0.2 < 0.9
	scanner.ScanOnce()
	assert.Equal(t, 0, scanner.Stats(), "恢复正常应清零计数")

	// 再次积压需从头累计 3 轮才驱逐（历史计数不误伤）
	client.SetBacklogRatio(10, 10)
	scanner.ScanOnce()
	scanner.ScanOnce()
	require.Empty(t, hook.evicted, "恢复后前 2 轮不应驱逐")

	scanner.ScanOnce()
	require.Len(t, hook.evicted, 1, "重新累计满 3 轮应驱逐")
}

// TestSlowConsumerHealthyBypass 健康连接零追踪（扫描空转）
func TestSlowConsumerHealthyBypass(t *testing.T) {
	client := models.NewClient("healthy-1", "u-4000", models.UserTypeVisitor)
	client.Context = context.Background()
	client.SendChan = make(chan []byte, 10)
	client.SetBacklogRatio(1, 10) // 0.1：健康

	hook := &fakeEvictHook{}
	scanner := NewSlowConsumerScanner(newScannerRegistry(t, client), hook, time.Hour, nil)

	for i := 0; i < 5; i++ {
		scanner.ScanOnce()
	}
	assert.Equal(t, 0, scanner.Stats())
	require.Empty(t, hook.evicted, "健康连接永不驱逐")
}

// TestSlowConsumerStaleStateCleanup 惰性清理：断连客户端的状态表条目下轮被清除
func TestSlowConsumerStaleStateCleanup(t *testing.T) {
	client := models.NewClient("stale-1", "u-5000", models.UserTypeCustomer)
	client.Context = context.Background()
	client.SendChan = make(chan []byte, 10)
	client.SetBacklogRatio(9, 10)

	hook := &fakeEvictHook{}
	registry := newScannerRegistry(t, client)
	scanner := NewSlowConsumerScanner(registry, hook, time.Hour, nil)

	scanner.ScanOnce()
	assert.Equal(t, 1, scanner.Stats())

	// 客户端断连（注册表移除，下轮扫描不再活跃）
	registry.RemoveClient(client.ID, client.UserID)
	scanner.ScanOnce()
	assert.Equal(t, 0, scanner.Stats(), "已断连客户端的状态应被惰性清理")
}

// TestSlowConsumerHookNil nil hook：只告警不驱逐（组件防御，编排层未注入时安全）
func TestSlowConsumerHookNil(t *testing.T) {
	client := models.NewClient("nilhook-1", "u-6000", models.UserTypeCustomer)
	client.Context = context.Background()
	client.SendChan = make(chan []byte, 10)
	client.SetBacklogRatio(9, 10)

	scanner := NewSlowConsumerScanner(newScannerRegistry(t, client), nil, time.Hour, nil)
	for i := 0; i < 3; i++ {
		scanner.ScanOnce() // 不应 panic
	}
}
