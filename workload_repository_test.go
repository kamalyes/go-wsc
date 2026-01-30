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
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAgentCount5000  = 5000
	testAgentCount10000 = 10000
	testConcurrency10  = 10
	testConcurrency500  = 500
	testIterations10   = 10
	testIterations200   = 200
	testTop100         = 100
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
	assert.Equal(t, int64(0), tr.getWorkload(agentID))
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
	agentID := tr.agentID("concurrent")
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)

	done := make(chan bool, testConcurrency10)

	for range testConcurrency10 {
		go func() {
			for range testIterations10 {
				_ = tr.repo.IncrementAgentWorkload(tr.ctx, agentID)
			}
			done <- true
		}()
	}

	for range testConcurrency10 {
		<-done
	}

	assert.Equal(t, int64(testConcurrency10*testIterations10), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryBatchSet10000Agents 测试批量设置10000个客服的性能
func TestRedisWorkloadRepositoryBatchSet10000Agents(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	workloads := make(map[string]int64, testAgentCount10000)
	for i := range testAgentCount10000 {
		agentID := fmt.Sprintf("large_agent_%05d", i)
		workloads[agentID] = int64(i % 100)
	}
	defer tr.cleanupMap(workloads)

	start := time.Now()
	tr.batchSet(workloads)
	t.Logf("✅ 批量设置 %d 个客服负载耗时: %v", testAgentCount10000, time.Since(start))

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

// TestRedisWorkloadRepositoryConcurrentOperationsStressTest 测试高并发压力场景
func TestRedisWorkloadRepositoryConcurrentOperationsStressTest(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID("stress")
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)

	done := make(chan bool, testConcurrency500)

	start := time.Now()

	for range testConcurrency500 {
		go func() {
			for range testIterations200 {
				_ = tr.repo.IncrementAgentWorkload(tr.ctx, agentID)
			}
			done <- true
		}()
	}

	for range testConcurrency500 {
		<-done
	}

	t.Logf("⚡ 并发递增操作 (%d goroutines × %d iterations) 耗时: %v",
		testConcurrency500, testIterations200, time.Since(start))

	expectedWorkload := int64(testConcurrency500 * testIterations200)
	assert.Equal(t, expectedWorkload, tr.getWorkload(agentID),
		"并发递增后负载应该为 %d", expectedWorkload)
}
