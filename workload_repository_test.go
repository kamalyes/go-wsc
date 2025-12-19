/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-18
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 13:57:33
 * @FilePath: \go-wsc\workload_repository_test.go
 * @Description: 负载管理仓库单元测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testWorkloadKeyPrefix = "test:workload:"
)

func setupWorkloadTestRedis(t *testing.T) *redis.Client {
	return getTestRedisClient(t)
}

func TestRedisWorkloadRepositorySetAndGetAgentWorkload(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 测试设置和获取负载
	agentID := "agent001"
	workload := int64(5)

	err := repo.SetAgentWorkload(ctx, agentID, workload)
	require.NoError(t, err)

	// 获取负载
	result, err := repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, workload, result)
}

func TestRedisWorkloadRepositoryIncrementAndDecrement(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	agentID := "agent002"

	// 初始化为0
	err := repo.SetAgentWorkload(ctx, agentID, 0)
	require.NoError(t, err)

	// 增加3次
	for i := 0; i < 3; i++ {
		err = repo.IncrementAgentWorkload(ctx, agentID)
		require.NoError(t, err)
	}

	// 验证负载为3
	workload, err := repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), workload)

	// 减少1次
	err = repo.DecrementAgentWorkload(ctx, agentID)
	require.NoError(t, err)

	// 验证负载为2
	workload, err = repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), workload)
}

func TestRedisWorkloadRepositoryDecrementBelowZero(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	agentID := "agent003"

	// 初始化为0
	err := repo.SetAgentWorkload(ctx, agentID, 0)
	require.NoError(t, err)

	// 尝试减少（应该保持为0）
	err = repo.DecrementAgentWorkload(ctx, agentID)
	require.NoError(t, err)

	// 验证负载仍为0
	workload, err := repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), workload)
}

func TestRedisWorkloadRepositoryGetLeastLoadedAgent(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 设置多个客服的负载
	agents := map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
		"agent004": 3,
	}

	for agentID, workload := range agents {
		err := repo.SetAgentWorkload(ctx, agentID, workload)
		require.NoError(t, err)
	}

	// 获取负载最小的客服
	onlineAgents := []string{"agent001", "agent002", "agent003", "agent004"}
	agentID, workload, err := repo.GetLeastLoadedAgent(ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, "agent002", agentID)
	assert.Equal(t, int64(2), workload)
}

func TestRedisWorkloadRepositoryGetLeastLoadedAgentWithOfflineAgents(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 设置多个客服的负载（包括不在线的）
	allAgents := map[string]int64{
		"agent001": 5,
		"agent002": 1, // 负载最小但不在线
		"agent003": 8,
		"agent004": 3, // 在线且负载次小
	}

	for agentID, workload := range allAgents {
		err := repo.SetAgentWorkload(ctx, agentID, workload)
		require.NoError(t, err)
	}

	// 只有部分客服在线
	onlineAgents := []string{"agent001", "agent003", "agent004"}
	agentID, workload, err := repo.GetLeastLoadedAgent(ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, "agent004", agentID)
	assert.Equal(t, int64(3), workload)
}

func TestRedisWorkloadRepositoryRemoveAgentWorkload(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	agentID := "agent005"

	// 设置负载
	err := repo.SetAgentWorkload(ctx, agentID, 10)
	require.NoError(t, err)

	// 验证负载存在
	workload, err := repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(10), workload)

	// 移除负载
	err = repo.RemoveAgentWorkload(ctx, agentID)
	require.NoError(t, err)

	// 验证负载已被移除（返回0）
	workload, err = repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), workload)
}

func TestRedisWorkloadRepositoryGetAllAgentWorkloads(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 设置多个客服的负载
	agents := map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
	}

	for agentID, workload := range agents {
		err := repo.SetAgentWorkload(ctx, agentID, workload)
		require.NoError(t, err)
	}

	// 获取所有客服负载（按负载从小到大排序）
	workloads, err := repo.GetAllAgentWorkloads(ctx, 0)
	require.NoError(t, err)
	require.Len(t, workloads, 3)

	// 验证顺序（负载从小到大）
	assert.Equal(t, "agent002", workloads[0].AgentID)
	assert.Equal(t, int64(2), workloads[0].Workload)
	assert.Equal(t, "agent001", workloads[1].AgentID)
	assert.Equal(t, int64(5), workloads[1].Workload)
	assert.Equal(t, "agent003", workloads[2].AgentID)
	assert.Equal(t, int64(8), workloads[2].Workload)
}

func TestRedisWorkloadRepositoryGetAllAgentWorkloadsWithLimit(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 设置多个客服的负载
	agents := map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
		"agent004": 1,
		"agent005": 10,
	}

	for agentID, workload := range agents {
		err := repo.SetAgentWorkload(ctx, agentID, workload)
		require.NoError(t, err)
	}

	// 只获取前3个
	workloads, err := repo.GetAllAgentWorkloads(ctx, 3)
	require.NoError(t, err)
	require.Len(t, workloads, 3)

	// 验证是负载最小的3个
	assert.Equal(t, "agent004", workloads[0].AgentID)
	assert.Equal(t, "agent002", workloads[1].AgentID)
	assert.Equal(t, "agent001", workloads[2].AgentID)
}

func TestRedisWorkloadRepositoryBatchSetAgentWorkload(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 批量设置负载
	workloads := map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
	}

	err := repo.BatchSetAgentWorkload(ctx, workloads)
	require.NoError(t, err)

	// 验证每个客服的负载
	for agentID, expectedWorkload := range workloads {
		workload, err := repo.GetAgentWorkload(ctx, agentID)
		require.NoError(t, err)
		assert.Equal(t, expectedWorkload, workload)
	}
}

func TestRedisWorkloadRepositoryConcurrency(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	agentID := "agent_concurrent"
	err := repo.SetAgentWorkload(ctx, agentID, 0)
	require.NoError(t, err)

	// 并发增加100次
	concurrency := 10
	iterations := 10
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				_ = repo.IncrementAgentWorkload(ctx, agentID)
			}
			done <- true
		}()
	}

	// 等待所有goroutine完成
	for i := 0; i < concurrency; i++ {
		<-done
	}

	// 验证最终负载为100
	workload, err := repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(concurrency*iterations), workload)
}

func TestRedisWorkloadRepositoryBatchSet1000Agents(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 构建1000个客服数据
	workloads := make(map[string]int64, 1000)
	for i := 0; i < 1000; i++ {
		agentID := fmt.Sprintf("large_agent_%03d", i)
		workloads[agentID] = int64(i % 100) // 负载在 0-99 之间
	}

	// 测试批量设置性能
	start := time.Now()
	err := repo.BatchSetAgentWorkload(ctx, workloads)
	elapsed := time.Since(start)

	require.NoError(t, err)
	t.Logf("✅ 批量设置 1000 个客服负载耗时: %v", elapsed)

	// 随机验证几个客服的负载
	testCases := []struct {
		index    int
		agentID  string
		expected int64
	}{
		{0, "large_agent_000", 0},
		{50, "large_agent_050", 50},
		{123, "large_agent_123", 23},
		{999, "large_agent_999", 99},
	}

	for _, tc := range testCases {
		workload, err := repo.GetAgentWorkload(ctx, tc.agentID)
		require.NoError(t, err, "Failed to get workload for %s", tc.agentID)
		assert.Equal(t, tc.expected, workload, "Workload mismatch for %s", tc.agentID)
	}
}

func TestRedisWorkloadRepositoryGetLeastLoadedFrom1000Agents(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 设置1000个客服
	workloads := make(map[string]int64, 1000)
	onlineAgents := make([]string, 0, 1000)

	for i := 0; i < 1000; i++ {
		agentID := fmt.Sprintf("scale_agent_%03d", i)
		workloads[agentID] = int64(i + 1) // 负载从1到1000递增，第一个负载最小
		onlineAgents = append(onlineAgents, agentID)
	}

	err := repo.BatchSetAgentWorkload(ctx, workloads)
	require.NoError(t, err)

	// 测试查询性能
	start := time.Now()
	leastLoadedAgent, workload, err := repo.GetLeastLoadedAgent(ctx, onlineAgents)
	elapsed := time.Since(start)

	require.NoError(t, err)
	t.Logf("🎯 从 1000 个客服中查询最小负载耗时: %v", elapsed)
	t.Logf("   最小负载客服: %s, 负载: %d", leastLoadedAgent, workload)

	// 验证是负载最小的客服
	assert.Equal(t, "scale_agent_000", leastLoadedAgent)
	assert.Equal(t, int64(1), workload)
}

func TestRedisWorkloadRepositoryGetAllWorkloadsPagination(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	// 设置500个客服
	workloads := make(map[string]int64, 500)
	for i := 0; i < 500; i++ {
		agentID := fmt.Sprintf("page_agent_%03d", i)
		workloads[agentID] = int64(i % 50) // 负载在 0-49 之间
	}

	err := repo.BatchSetAgentWorkload(ctx, workloads)
	require.NoError(t, err)

	// 获取前100个
	start := time.Now()
	top100, err := repo.GetAllAgentWorkloads(ctx, 100)
	elapsed := time.Since(start)

	require.NoError(t, err)
	t.Logf("📊 获取前 100 个客服负载耗时: %v", elapsed)
	assert.Equal(t, 100, len(top100))

	// 验证按负载排序（升序）
	for i := 1; i < len(top100); i++ {
		assert.GreaterOrEqual(t, top100[i].Workload, top100[i-1].Workload,
			"负载应该是升序排列: %d >= %d", top100[i].Workload, top100[i-1].Workload)
	}
}

func TestRedisWorkloadRepositoryConcurrentOperationsStressTest(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	agentID := "stress_agent"
	err := repo.SetAgentWorkload(ctx, agentID, 0)
	require.NoError(t, err)

	// 并发增加操作
	concurrency := 50
	iterations := 20
	done := make(chan bool, concurrency)

	start := time.Now()

	// 50个goroutine增加
	for i := 0; i < concurrency; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				_ = repo.IncrementAgentWorkload(ctx, agentID)
			}
			done <- true
		}()
	}

	// 等待所有goroutine完成
	for i := 0; i < concurrency; i++ {
		<-done
	}

	elapsed := time.Since(start)
	t.Logf("⚡ 并发递增操作 (%d goroutines × %d iterations) 耗时: %v",
		concurrency, iterations, elapsed)

	// 验证最终负载为 1000
	workload, err := repo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	expectedWorkload := int64(concurrency * iterations)
	assert.Equal(t, expectedWorkload, workload,
		"并发递增后负载应该为 %d，实际为 %d", expectedWorkload, workload)
}

func TestRedisWorkloadRepositoryDailyKeySeparation(t *testing.T) {
	client := setupWorkloadTestRedis(t)
	defer client.Close()

	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testWorkloadKeyPrefix,
		TTL:       5 * time.Minute,
	})
	ctx := context.Background()

	agentID := "daily_agent"
	workload := int64(10)

	err := repo.SetAgentWorkload(ctx, agentID, workload)
	require.NoError(t, err)

	// 验证key格式包含日期
	todayKey := time.Now().Format("20060102")
	expectedKey := testWorkloadKeyPrefix + todayKey + ":agent:" + agentID

	// 直接查询Redis验证key存在
	exists := client.Exists(ctx, expectedKey).Val()
	assert.Equal(t, int64(1), exists, "按天拆分的key应该存在: %s", expectedKey)

	// 验证ZSet key也包含日期
	expectedZSetKey := testWorkloadKeyPrefix + todayKey + ":zset"
	zsetExists := client.Exists(ctx, expectedZSetKey).Val()
	assert.Equal(t, int64(1), zsetExists, "按天拆分的ZSet key应该存在: %s", expectedZSetKey)

	t.Logf("✅ 验证按天拆分key格式正确:")
	t.Logf("   负载key: %s", expectedKey)
	t.Logf("   ZSet key: %s", expectedZSetKey)
}
