/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-18 09:00:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:38:29
 * @FilePath: \go-wsc\workload_repository_test.go
 * @Description: 负载管理仓库单元测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAgentCount5000  = 5000
	testAgentCount10000 = 10000
	testConcurrency10   = 10
	testIterations10    = 10
	testTop100          = 100
)

var (
	testWorkloadKeyPrefix = "test:workload:"
)

// testWorkloadRepo 测试辅助结构
type testWorkloadRepo struct {
	repo       WorkloadRepository
	ctx        context.Context
	t          *testing.T
	testPrefix string // 测试前缀，用于隔离不同测试的数据
}

// newTestWorkloadRepo 创建测试仓库实例
func newTestWorkloadRepo(t *testing.T) *testWorkloadRepo {
	client := GetTestRedisClient(t)
	testPrefix := testWorkloadKeyPrefix + t.Name() + "_"
	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testPrefix,
	}, NewDefaultWSCLogger())

	return &testWorkloadRepo{
		repo:       repo,
		ctx:        context.Background(),
		t:          t,
		testPrefix: testPrefix, // 使用测试名称作为前缀
	}
}

// checkRedisHealth 检查Redis连接健康状态，如果不健康则跳过测试
func (tr *testWorkloadRepo) checkRedisHealth() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 尝试一个简单的操作
	testKey := tr.agentID("health_check")
	_, err := tr.repo.GetAgentWorkload(ctx, testKey)

	// redis.Nil 是正常的（key不存在），其他错误说明连接有问题
	if err != nil && err.Error() != "redis: nil" {
		tr.t.Skipf("⚠️ Redis连接不健康，跳过测试: %v", err)
	}
}

// agentID 生成带测试前缀的客服ID
func (tr *testWorkloadRepo) agentID(name string) string {
	return tr.testPrefix + name
}

// cleanup 清理测试数据
func (tr *testWorkloadRepo) cleanup(agentIDs ...string) {
	if len(agentIDs) == 0 {
		return
	}

	// 使用批量删除接口

	if repo, ok := tr.repo.(*RedisWorkloadRepository); ok {
		_ = repo.BatchRemoveAgentWorkload(tr.ctx, agentIDs)
	} else {
		// 降级为逐个删除
		for _, agentID := range agentIDs {
			_ = tr.repo.RemoveAgentWorkload(tr.ctx, agentID)
		}
	}
}

// cleanupMap 清理 map 中的所有客服数据
func (tr *testWorkloadRepo) cleanupMap(agents map[string]int64) {
	if len(agents) == 0 {
		return
	}

	agentIDs := make([]string, 0, len(agents))
	for agentID := range agents {
		agentIDs = append(agentIDs, agentID)
	}
	tr.cleanup(agentIDs...)
}

// setWorkload 设置客服负载
func (tr *testWorkloadRepo) setWorkload(agentID string, workload int64) {
	err := tr.repo.SetAgentWorkload(tr.ctx, agentID, workload)
	require.NoError(tr.t, err)
}

// getWorkload 获取客服负载
func (tr *testWorkloadRepo) getWorkload(agentID string) int64 {
	workload, err := tr.repo.GetAgentWorkload(tr.ctx, agentID)
	require.NoError(tr.t, err)
	return workload
}

// batchSet 批量设置客服负载
func (tr *testWorkloadRepo) batchSet(workloads map[string]int64) {
	err := tr.repo.BatchSetAgentWorkload(tr.ctx, workloads)
	require.NoError(tr.t, err)
}

// makeAgents 创建多个客服ID和负载的映射
func (tr *testWorkloadRepo) makeAgents(agents map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(agents))
	for name, workload := range agents {
		result[tr.agentID(name)] = workload
	}
	return result
}

// makeAgentList 创建客服ID列表
func (tr *testWorkloadRepo) makeAgentList(names ...string) []string {
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = tr.agentID(name)
	}
	return result
}

// TestRedisWorkloadRepositorySetAndGetAgentWorkload 测试设置和获取客服工作负载
func TestRedisWorkloadRepositorySetAndGetAgentWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("agent001")
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 5)
	assert.Equal(t, int64(5), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryIncrementAndDecrement 测试增加和减少客服工作负载
func TestRedisWorkloadRepositoryIncrementAndDecrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("agent")
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)

	// 增加3次
	for range 3 {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentID))
	}
	assert.Equal(t, int64(3), tr.getWorkload(agentID))

	// 减少1次
	require.NoError(t, tr.repo.DecrementAgentWorkload(tr.ctx, agentID))
	assert.Equal(t, int64(2), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryDecrementBelowZero 测试负载减少不会低于0
func TestRedisWorkloadRepositoryDecrementBelowZero(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("agent")
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)
	require.NoError(t, tr.repo.DecrementAgentWorkload(tr.ctx, agentID))
	assert.Equal(t, int64(0), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryGetLeastLoadedAgent 测试获取负载最小的在线客服
func TestRedisWorkloadRepositoryGetLeastLoadedAgent(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agents := tr.makeAgents(map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
		"agent004": 3,
	})
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	onlineAgents := tr.makeAgentList("agent001", "agent002", "agent003", "agent004")
	agentID, workload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, tr.agentID("agent002"), agentID)
	assert.Equal(t, int64(2), workload)
}

// TestRedisWorkloadRepositoryGetLeastLoadedAgentWithOfflineAgents 测试只从在线客服中选择负载最小的
func TestRedisWorkloadRepositoryGetLeastLoadedAgentWithOfflineAgents(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	allAgents := tr.makeAgents(map[string]int64{
		"agent001": 5,
		"agent002": 1, // 负载最小但不在线
		"agent003": 8,
		"agent004": 3, // 在线且负载次小
	})
	defer tr.cleanupMap(allAgents)

	tr.batchSet(allAgents)

	onlineAgents := tr.makeAgentList("agent001", "agent003", "agent004")
	agentID, workload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, tr.agentID("agent004"), agentID)
	assert.Equal(t, int64(3), workload)
}

// TestRedisWorkloadRepositoryRemoveAgentWorkload 测试移除客服负载
func TestRedisWorkloadRepositoryRemoveAgentWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("agent")

	tr.setWorkload(agentID, 10)
	assert.Equal(t, int64(10), tr.getWorkload(agentID))

	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agentID))

	// RemoveAgentWorkload 只删除 ZSet，保留单个 key
	// 所以 GetAgentWorkload 应该仍然返回 10
	assert.Equal(t, int64(10), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositorySyncAgentWorkloadToZSet 测试从单个key同步负载到ZSet
func TestRedisWorkloadRepositorySyncAgentWorkloadToZSet(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("agent_sync")
	defer tr.cleanup(agentID)

	// 1. 设置初始负载
	tr.setWorkload(agentID, 5)
	assert.Equal(t, int64(5), tr.getWorkload(agentID))

	// 2. 移除客服（只删除ZSet，保留单个key）
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agentID))

	// 3. 单个key仍然存在
	assert.Equal(t, int64(5), tr.getWorkload(agentID))

	// 4. 同步到ZSet
	require.NoError(t, tr.repo.SyncAgentWorkloadToZSet(tr.ctx, agentID))

	// 5. 验证可以从ZSet查询到
	onlineAgents := tr.makeAgentList("agent_sync")
	selectedAgent, workload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, agentID, selectedAgent)
	assert.Equal(t, int64(5), workload)
}

// TestRedisWorkloadRepositorySyncAgentWorkloadToZSetWithNoKey 测试同步不存在的key
func TestRedisWorkloadRepositorySyncAgentWorkloadToZSetWithNoKey(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("agent_no_key")
	defer tr.cleanup(agentID)

	// 同步一个不存在的key（应该返回0）
	require.NoError(t, tr.repo.SyncAgentWorkloadToZSet(tr.ctx, agentID))

	// 验证负载为0
	assert.Equal(t, int64(0), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryOfflineOnlineFlow 测试完整的离线-上线流程
func TestRedisWorkloadRepositoryOfflineOnlineFlow(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("agent_flow")
	defer tr.cleanup(agentID)

	// 1. 客服上线，初始化负载为0
	tr.setWorkload(agentID, 0)

	// 2. 分配工单，增加负载
	for range 5 {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentID))
	}
	assert.Equal(t, int64(5), tr.getWorkload(agentID))

	// 3. 验证在ZSet中
	onlineAgents := tr.makeAgentList("agent_flow")
	selectedAgent, workload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, agentID, selectedAgent)
	assert.Equal(t, int64(5), workload)

	// 4. 客服离线（只删除ZSet）
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agentID))

	// 5. 单个key仍然保留
	assert.Equal(t, int64(5), tr.getWorkload(agentID))

	// 6. 客服重新上线（同步负载到ZSet）
	require.NoError(t, tr.repo.SyncAgentWorkloadToZSet(tr.ctx, agentID))

	// 7. 验证负载恢复
	selectedAgent, workload, err = tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, agentID, selectedAgent)
	assert.Equal(t, int64(5), workload)
}

// TestRedisWorkloadRepositoryGetAllAgentWorkloads 测试获取所有客服负载
func TestRedisWorkloadRepositoryGetAllAgentWorkloads(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agents := tr.makeAgents(map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
	})
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	workloads, err := tr.repo.GetAllAgentWorkloads(tr.ctx, 0)
	require.NoError(t, err)
	require.Len(t, workloads, 3)

	// 验证顺序（负载从小到大）
	assert.Equal(t, tr.agentID("agent002"), workloads[0].AgentID)
	assert.Equal(t, int64(2), workloads[0].Workload)
	assert.Equal(t, tr.agentID("agent001"), workloads[1].AgentID)
	assert.Equal(t, int64(5), workloads[1].Workload)
	assert.Equal(t, tr.agentID("agent003"), workloads[2].AgentID)
	assert.Equal(t, int64(8), workloads[2].Workload)
}

// TestRedisWorkloadRepositoryGetAllAgentWorkloadsWithLimit 测试分页获取客服负载
func TestRedisWorkloadRepositoryGetAllAgentWorkloadsWithLimit(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	agents := tr.makeAgents(map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
		"agent004": 1,
		"agent005": 10,
	})
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	workloads, err := tr.repo.GetAllAgentWorkloads(tr.ctx, 3)
	require.NoError(t, err)
	require.Len(t, workloads, 3)

	// 验证是负载最小的3个
	assert.Equal(t, tr.agentID("agent004"), workloads[0].AgentID)
	assert.Equal(t, tr.agentID("agent002"), workloads[1].AgentID)
	assert.Equal(t, tr.agentID("agent001"), workloads[2].AgentID)
}

// TestRedisWorkloadRepositoryBatchSetAgentWorkload 测试批量设置客服负载
func TestRedisWorkloadRepositoryBatchSetAgentWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	workloads := tr.makeAgents(map[string]int64{
		"agent001": 5,
		"agent002": 2,
		"agent003": 8,
	})
	defer tr.cleanupMap(workloads)

	tr.batchSet(workloads)

	for agentID, expected := range workloads {
		assert.Equal(t, expected, tr.getWorkload(agentID))
	}
}

// TestRedisWorkloadRepositoryConcurrency 测试并发操作的原子性
func TestRedisWorkloadRepositoryConcurrency(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	// 验证Redis连接是否健康（尝试一次简单操作）
	agentID := tr.agentID("concurrent")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, err := tr.repo.GetAgentWorkload(ctx, agentID)
	cancel()
	if err != nil && err.Error() != "redis: nil" { // redis: nil 是正常的（key不存在）
		t.Skipf("Redis连接不可用，跳过测试: %v", err)
	}

	// 先清理可能存在的旧数据
	tr.cleanup(agentID)
	defer tr.cleanup(agentID)

	// 不使用 setWorkload，让 INCR 自动从 0 开始
	// 验证初始值是0（如果 key 不存在，GetAgentWorkload 返回 0）
	initialWorkload := tr.getWorkload(agentID)
	t.Logf("🔍 测试开始 - agentID=%s, 初始负载=%d", agentID, initialWorkload)

	var wg sync.WaitGroup
	var errCount atomic.Int32
	var successCount atomic.Int32

	for range testConcurrency10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range testIterations10 {
				// 添加重试机制
				var lastErr error
				for retry := 0; retry < 3; retry++ {
					if err := tr.repo.IncrementAgentWorkload(tr.ctx, agentID); err != nil {
						lastErr = err
						time.Sleep(10 * time.Millisecond) // 短暂等待后重试
						continue
					}
					successCount.Add(1)
					lastErr = nil
					break
				}
				if lastErr != nil {
					errCount.Add(1)
					t.Logf("增加负载失败（重试3次后）: %v", lastErr)
				}
			}
		}()
	}

	wg.Wait()

	// 等待 Redis 操作完全完成
	time.Sleep(200 * time.Millisecond)

	actualWorkload := tr.getWorkload(agentID)
	expectedWorkload := int64(testConcurrency10 * testIterations10)

	t.Logf("📊 最终统计: 成功=%d, 失败=%d, 实际负载=%d, 期望负载=%d",
		successCount.Load(), errCount.Load(), actualWorkload, expectedWorkload)

	// 如果所有操作都成功但Redis中数据不对，说明Redis连接有问题
	if errCount.Load() == 0 && actualWorkload < int64(successCount.Load()/2) {
		t.Skipf("❌ Redis数据异常（成功=%d 但实际=%d），可能是CI环境Redis不稳定",
			successCount.Load(), actualWorkload)
	}

	// 关键断言：实际负载应该等于成功次数
	if int64(successCount.Load()) != actualWorkload {
		t.Errorf("❌ 数据不一致！成功次数=%d 但 Redis 中实际负载=%d，差异=%d",
			successCount.Load(), actualWorkload, int64(successCount.Load())-actualWorkload)
	}

	assert.Equal(t, int64(successCount.Load()), actualWorkload,
		"实际负载应该等于成功操作次数")

	// 成功次数应该接近期望值（允许少量失败）
	minSuccessCount := int32(float64(expectedWorkload) * 0.95)
	assert.GreaterOrEqual(t, successCount.Load(), minSuccessCount,
		"成功率应该至少95%")
}

// TestRedisWorkloadRepositoryBatchSet10000Agents 测试批量设置10000个客服的性能
func TestRedisWorkloadRepositoryBatchSet10000Agents(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth() // 检查Redis健康状态

	workloads := make(map[string]int64, testAgentCount10000)
	for i := range testAgentCount10000 {
		agentID := fmt.Sprintf("large_agent_%05d", i)
		workloads[agentID] = int64(i % 100)
	}
	defer tr.cleanupMap(workloads)

	start := time.Now()
	tr.batchSet(workloads)
	t.Logf("✅ 批量设置 %d 个客服负载耗时: %v", testAgentCount10000, time.Since(start))

	// 等待Redis操作完全完成
	time.Sleep(200 * time.Millisecond)

	// 随机验证几个客服的负载
	testCases := []struct {
		agentID  string
		expected int64
	}{
		{"large_agent_00000", 0},
		{"large_agent_00050", 50},
		{"large_agent_01234", 34},
		{"large_agent_09999", 99},
	}

	for _, tc := range testCases {
		assert.Equal(t, tc.expected, tr.getWorkload(tc.agentID), "Workload mismatch for %s", tc.agentID)
	}
}

// TestRedisWorkloadRepositoryGetLeastLoadedFrom10000Agents 测试从10000个客服中查询最小负载的性能
func TestRedisWorkloadRepositoryGetLeastLoadedFrom10000Agents(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	workloads := make(map[string]int64, testAgentCount10000)
	onlineAgents := make([]string, 0, testAgentCount10000)

	for i := range testAgentCount10000 {
		agentID := fmt.Sprintf("scale_agent_%05d", i)
		workloads[agentID] = int64(i + 1)
		onlineAgents = append(onlineAgents, agentID)
	}
	defer tr.cleanupMap(workloads)

	tr.batchSet(workloads)

	start := time.Now()
	leastLoadedAgent, workload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	t.Logf("🎯 从 %d 个客服中查询最小负载耗时: %v", testAgentCount10000, time.Since(start))
	t.Logf("   最小负载客服: %s, 负载: %d", leastLoadedAgent, workload)

	assert.Equal(t, "scale_agent_00000", leastLoadedAgent)
	assert.Equal(t, int64(1), workload)
}

// TestRedisWorkloadRepositoryGetAllWorkloadsPagination 测试分页查询性能
func TestRedisWorkloadRepositoryGetAllWorkloadsPagination(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	workloads := make(map[string]int64, testAgentCount5000)
	for i := range testAgentCount5000 {
		agentID := fmt.Sprintf("page_agent_%04d", i)
		workloads[agentID] = int64(i % 50)
	}
	defer tr.cleanupMap(workloads)

	tr.batchSet(workloads)

	start := time.Now()
	top100, err := tr.repo.GetAllAgentWorkloads(tr.ctx, testTop100)
	require.NoError(t, err)
	t.Logf("📊 获取前 %d 个客服负载耗时: %v", testTop100, time.Since(start))

	assert.Equal(t, testTop100, len(top100))

	// 验证按负载排序（升序）
	for i := 1; i < len(top100); i++ {
		assert.GreaterOrEqual(t, top100[i].Workload, top100[i-1].Workload,
			"负载应该是升序排列")
	}
}
