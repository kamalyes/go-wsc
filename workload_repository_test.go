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
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/idgen"
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
	repo        WorkloadRepository
	ctx         context.Context
	t           *testing.T
	testPrefix  string // 测试前缀，用于隔离不同测试的数据
	idGenerator *idgen.ShortFlakeGenerator
}

// newTestWorkloadRepo 创建测试仓库实例
func newTestWorkloadRepo(t *testing.T) *testWorkloadRepo {
	client := GetTestRedisClient(t)
	testPrefix := testWorkloadKeyPrefix + t.Name() + "_"
	repo := NewRedisWorkloadRepository(client, &wscconfig.Workload{
		KeyPrefix: testPrefix,
	}, NewDefaultWSCLogger())

	return &testWorkloadRepo{
		repo:        repo,
		ctx:         context.Background(),
		t:           t,
		testPrefix:  testPrefix,
		idGenerator: idgen.NewShortFlakeGenerator(1),
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

// TestRedisWorkloadRepositorySetAndGetAgentWorkload 测试设置和获取客服工作负载
func TestRedisWorkloadRepositorySetAndGetAgentWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentIdKey := tr.idGenerator.GenerateRequestID()
	agentID := tr.agentID(agentIdKey)
	defer tr.cleanup(agentID)

	tr.setWorkload(agentIdKey, 5)
	assert.Equal(t, int64(5), tr.getWorkload(agentIdKey))
}

// TestRedisWorkloadRepositoryIncrementAndDecrement 测试增加和减少客服工作负载
func TestRedisWorkloadRepositoryIncrementAndDecrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentIdKey := tr.idGenerator.GenerateRequestID()
	agentID := tr.agentID(agentIdKey)
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)

	// 增加3次
	for range 3 {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentIdKey))
	}
	assert.Equal(t, int64(3), tr.getWorkload(agentIdKey))

	// 减少1次
	require.NoError(t, tr.repo.DecrementAgentWorkload(tr.ctx, agentIdKey))
	assert.Equal(t, int64(2), tr.getWorkload(agentIdKey))
}

// TestRedisWorkloadRepositoryDecrementBelowZero 测试负载减少不会低于0
func TestRedisWorkloadRepositoryDecrementBelowZero(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentIdKey := tr.idGenerator.GenerateRequestID()
	agentID := tr.agentID(agentIdKey)
	defer tr.cleanup(agentID)

	tr.setWorkload(agentIdKey, 0)
	require.NoError(t, tr.repo.DecrementAgentWorkload(tr.ctx, agentIdKey))
	assert.Equal(t, int64(0), tr.getWorkload(agentIdKey))
}

// TestRedisWorkloadRepositoryCompleteLifecycle 测试完整的客服生命周期和负载管理
func TestRedisWorkloadRepositoryCompleteLifecycle(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	// 阶段1: 批量设置多个客服负载，测试获取最小负载客服
	var (
		agent1 = tr.idGenerator.GenerateRequestID()
		agent2 = tr.idGenerator.GenerateRequestID()
		agent3 = tr.idGenerator.GenerateRequestID()
		agent4 = tr.idGenerator.GenerateRequestID()
	)

	agentsPhase1 := map[string]int64{
		tr.agentID(agent1): 5,
		tr.agentID(agent2): 2,
		tr.agentID(agent3): 8,
		tr.agentID(agent4): 3,
	}
	defer tr.cleanupMap(agentsPhase1)

	tr.batchSet(agentsPhase1)

	onlineAgents := []string{
		tr.agentID(agent1),
		tr.agentID(agent2),
		tr.agentID(agent3),
		tr.agentID(agent4),
	}
	selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, tr.agentID(agent2), selectedAgent, "负载最小的应该是 agent2 (负载=2)")
	assert.Equal(t, int64(2), selectedWorkload)

	// 阶段2: 测试完整生命周期（使用新的客服ID避免状态污染）
	var (
		agentA = tr.idGenerator.GenerateRequestID()
		agentB = tr.idGenerator.GenerateRequestID()
	)

	agentsPhase2 := map[string]int64{
		tr.agentID(agentA): 0,
		tr.agentID(agentB): 0,
	}
	defer tr.cleanupMap(agentsPhase2)

	var (
		initWorkload         = int64(0)
		agentAAssignWorkload = int64(3)
		agentBAssignWorkload = int64(1)
	)

	// 2.1 两个客服上线
	workloadA, err := tr.repo.InitAgentWorkload(tr.ctx, tr.agentID(agentA), initWorkload)
	require.NoError(t, err)
	assert.Equal(t, initWorkload, workloadA)

	workloadB, err := tr.repo.InitAgentWorkload(tr.ctx, tr.agentID(agentB), initWorkload)
	require.NoError(t, err)
	assert.Equal(t, initWorkload, workloadB)

	// 2.2 agentA 分配 3 个工单
	for range agentAAssignWorkload {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, tr.agentID(agentA)))
	}

	// 2.3 agentB 分配 1 个工单
	require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, tr.agentID(agentB)))

	// 2.4 查询负载最小的客服（应该是 agentB）
	onlineAgents = []string{tr.agentID(agentA), tr.agentID(agentB)}
	selectedAgent, selectedWorkload, err = tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, tr.agentID(agentB), selectedAgent, "负载最小的应该是 agentB (负载=1)")
	assert.Equal(t, agentBAssignWorkload, selectedWorkload)

	// 2.5 agentB 下线
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, tr.agentID(agentB)))

	// 2.6 查询负载最小的客服（只剩 agentA）
	onlineAgents = []string{tr.agentID(agentA)}
	selectedAgent, selectedWorkload, err = tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, tr.agentID(agentA), selectedAgent, "只剩 agentA 在线")
	assert.Equal(t, agentAAssignWorkload, selectedWorkload)

	// 2.7 agentB 重新上线（应该恢复负载 1）
	workloadB, err = tr.repo.InitAgentWorkload(tr.ctx, tr.agentID(agentB), 0)
	require.NoError(t, err)
	assert.Equal(t, agentBAssignWorkload, workloadB, "重新上线应该恢复之前的负载")

	// 2.8 查询负载最小的客服（应该又是 agentB）
	onlineAgents = []string{tr.agentID(agentA), tr.agentID(agentB)}
	selectedAgent, selectedWorkload, err = tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, tr.agentID(agentB), selectedAgent, "agentB 重新上线后负载仍然最小")
	assert.Equal(t, agentBAssignWorkload, selectedWorkload)
}
