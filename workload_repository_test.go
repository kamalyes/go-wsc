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
	"gorm.io/gorm"
)

var (
	testWorkloadKeyPrefix = "test:workload:"
)

// testWorkloadRepo 测试辅助结构
type testWorkloadRepo struct {
	repo        WorkloadRepository
	db          *gorm.DB // 添加数据库连接字段
	ctx         context.Context
	t           *testing.T
	testPrefix  string // 测试前缀，用于隔离不同测试的数据
	idGenerator *idgen.ShortFlakeGenerator
}

// newTestWorkloadRepo 创建测试仓库实例
func newTestWorkloadRepo(t *testing.T) *testWorkloadRepo {
	client := GetTestRedisClient(t)
	db := GetTestDBWithMigration(t, &AgentWorkloadModel{})
	testPrefix := testWorkloadKeyPrefix + t.Name() + "_"
	repo := NewRedisWorkloadRepository(client, db, &wscconfig.Workload{
		KeyPrefix: testPrefix,
	}, NewDefaultWSCLogger())

	return &testWorkloadRepo{
		repo:        repo,
		db:          db, // 保存数据库连接
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
	for _, agentID := range agentIDs {
		_ = tr.repo.RemoveAgentWorkload(tr.ctx, agentID)
		// 同时清理数据库记录
		if tr.db != nil {
			_ = tr.db.Where("agent_id = ?", agentID).Delete(&AgentWorkloadModel{}).Error
		}
		// 清理 Redis string key
		workloadKey := tr.testPrefix + "agent:" + agentID
		_ = GetTestRedisClient(tr.t).Del(tr.ctx, workloadKey).Err()
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
	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 5)
	assert.Equal(t, int64(5), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryIncrementAndDecrement 测试增加和减少客服工作负载
func TestRedisWorkloadRepositoryIncrementAndDecrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)

	// 增加 3 次
	for range 3 {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentID))
	}
	assert.Equal(t, int64(3), tr.getWorkload(agentID))

	// 减少 1 次
	require.NoError(t, tr.repo.DecrementAgentWorkload(tr.ctx, agentID))
	assert.Equal(t, int64(2), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryDecrementBelowZero 测试负载减少不会低于 0
func TestRedisWorkloadRepositoryDecrementBelowZero(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)
	require.NoError(t, tr.repo.DecrementAgentWorkload(tr.ctx, agentID))
	assert.Equal(t, int64(0), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryGetLeastLoadedAgent 测试获取负载最小的客服
func TestRedisWorkloadRepositoryGetLeastLoadedAgent(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent3 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent4 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 5,
		agent2: 2,
		agent3: 8,
		agent4: 3,
	}
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	onlineAgents := []string{agent1, agent2, agent3, agent4}
	selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(
		tr.ctx,
		onlineAgents,
	)
	require.NoError(t, err)
	assert.Equal(t, agent2, selectedAgent, "负载最小的应该是 agent2 (负载=2)")
	assert.Equal(t, int64(2), selectedWorkload)
}

// TestRedisWorkloadRepositoryCompleteLifecycle 测试完整的客服生命周期
func TestRedisWorkloadRepositoryCompleteLifecycle(t *testing.T) {
	tr := newTestWorkloadRepo(t)

	agentA := tr.agentID(tr.idGenerator.GenerateRequestID())
	agentB := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agentA: 0,
		agentB: 0,
	}
	defer tr.cleanupMap(agents)

	// 1. 两个客服上线
	workloadA, err := tr.repo.InitAgentWorkload(tr.ctx, agentA, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), workloadA)

	workloadB, err := tr.repo.InitAgentWorkload(tr.ctx, agentB, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), workloadB)

	// 2. agentA 分配 3 个工单
	for range 3 {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentA))
	}

	// 3. agentB 分配 1 个工单
	require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentB))

	// 4. 查询负载最小的客服（应该是 agentB）
	onlineAgents := []string{agentA, agentB}
	selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(
		tr.ctx,
		onlineAgents,
	)
	require.NoError(t, err)
	assert.Equal(t, agentB, selectedAgent, "负载最小的应该是 agentB (负载=1)")
	assert.Equal(t, int64(1), selectedWorkload)

	// 5. agentB 下线
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agentB))

	// 6. 查询负载最小的客服（只剩 agentA）
	onlineAgents = []string{agentA}
	selectedAgent, selectedWorkload, err = tr.repo.GetLeastLoadedAgent(
		tr.ctx,
		onlineAgents,
	)
	require.NoError(t, err)
	assert.Equal(t, agentA, selectedAgent, "只剩 agentA 在线")
	assert.Equal(t, int64(3), selectedWorkload)

	// 7. agentB 重新上线（应该恢复负载 1）
	workloadB, err = tr.repo.InitAgentWorkload(tr.ctx, agentB, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), workloadB, "重新上线应该恢复之前的负载")

	// 8. 查询负载最小的客服（应该又是 agentB）
	onlineAgents = []string{agentA, agentB}
	selectedAgent, selectedWorkload, err = tr.repo.GetLeastLoadedAgent(
		tr.ctx,
		onlineAgents,
	)
	require.NoError(t, err)
	assert.Equal(t, agentB, selectedAgent, "agentB 重新上线后负载仍然最小")
	assert.Equal(t, int64(1), selectedWorkload)
}

// TestRedisWorkloadRepositoryGetAllAgentWorkloads 测试批量获取客服负载
func TestRedisWorkloadRepositoryGetAllAgentWorkloads(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent3 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 5,
		agent2: 3,
		agent3: 8,
	}
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	workloads, err := tr.repo.GetAllAgentWorkloads(tr.ctx, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(workloads), 3, "至少应该有 3 个客服")

	// 验证负载值
	workloadMap := make(map[string]int64)
	for _, w := range workloads {
		workloadMap[w.AgentID] = w.Workload
	}

	for agentID, expectedWorkload := range agents {
		actualWorkload, exists := workloadMap[agentID]
		assert.True(t, exists, "客服 %s 应该存在", agentID)
		assert.Equal(t, expectedWorkload, actualWorkload, "客服 %s 的负载不匹配", agentID)
	}
}

// TestRedisWorkloadRepositoryInitAgentWorkload 测试初始化客服负载
func TestRedisWorkloadRepositoryInitAgentWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 场景 1：首次初始化
	workload, err := tr.repo.InitAgentWorkload(tr.ctx, agentID, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), workload)

	// 场景 2：设置负载后下线
	tr.setWorkload(agentID, 5)
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agentID))

	// 场景 3：重新上线，应该恢复之前的负载
	workload, err = tr.repo.InitAgentWorkload(tr.ctx, agentID, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(5), workload, "重新上线应该恢复之前的负载")
}

// TestRedisWorkloadRepositorySyncAgentWorkloadToZSet 测试同步负载到 ZSet
func TestRedisWorkloadRepositorySyncAgentWorkloadToZSet(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 设置负载
	tr.setWorkload(agentID, 10)

	// 从 ZSet 移除
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agentID))

	// 重新同步到 ZSet
	require.NoError(t, tr.repo.SyncAgentWorkloadToZSet(tr.ctx, agentID))

	// 验证可以从 ZSet 中查询到
	onlineAgents := []string{agentID}
	selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(
		tr.ctx,
		onlineAgents,
	)
	require.NoError(t, err)
	assert.Equal(t, agentID, selectedAgent)
	assert.Equal(t, int64(10), selectedWorkload)
}

// TestRedisWorkloadRepositoryBatchSetAgentWorkload 测试批量设置客服负载
func TestRedisWorkloadRepositoryBatchSetAgentWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent3 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 10,
		agent2: 20,
		agent3: 30,
	}
	defer tr.cleanupMap(agents)

	// 批量设置
	tr.batchSet(agents)

	// 验证每个客服的负载
	for agentID, expectedWorkload := range agents {
		actualWorkload := tr.getWorkload(agentID)
		assert.Equal(t, expectedWorkload, actualWorkload, "客服 %s 的负载不匹配", agentID)
	}
}

// TestRedisWorkloadRepositoryMaxCandidatesDefault 测试默认 MaxCandidates (50) 的行为
func TestRedisWorkloadRepositoryMaxCandidatesDefault(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	// 创建 60 个客服（超过默认的 50）
	const totalAgents = 60
	agents := make(map[string]int64, totalAgents)
	onlineAgents := make([]string, 0, totalAgents)

	for i := 0; i < totalAgents; i++ {
		agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
		// 前 10 个客服负载为 1（最小），其余递增
		workload := int64(1)
		if i >= 10 {
			workload = int64(i)
		}
		agents[agentID] = workload
		onlineAgents = append(onlineAgents, agentID)
	}
	defer tr.cleanupMap(agents)

	// 批量设置负载
	tr.batchSet(agents)

	// 多次查询，验证能从前 10 个最小负载的客服中随机选择
	selectedCount := make(map[string]int)
	for i := 0; i < 100; i++ {
		selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
		require.NoError(t, err)
		assert.Equal(t, int64(1), selectedWorkload, "应该选择负载为 1 的客服")
		selectedCount[selectedAgent]++
	}

	// 验证至少选择了多个不同的客服（说明是随机的）
	assert.GreaterOrEqual(t, len(selectedCount), 2, "应该从多个负载最小的客服中随机选择")
}

// TestRedisWorkloadRepositoryMaxCandidatesCustom 测试自定义 MaxCandidates 的行为
func TestRedisWorkloadRepositoryMaxCandidatesCustom(t *testing.T) {
	// 创建自定义配置的仓库（MaxCandidates = 5）
	client := GetTestRedisClient(t)
	db := GetTestDBWithMigration(t, &AgentWorkloadModel{})
	testPrefix := testWorkloadKeyPrefix + t.Name() + "_"

	customRepo := NewRedisWorkloadRepository(client, db, &wscconfig.Workload{
		KeyPrefix:     testPrefix,
		MaxCandidates: 5, // 只查询前 5 个
	}, NewDefaultWSCLogger())

	ctx := context.Background()
	idGenerator := idgen.NewShortFlakeGenerator(1)

	// 创建 10 个客服
	const totalAgents = 10
	agents := make(map[string]int64, totalAgents)
	onlineAgents := make([]string, 0, totalAgents)

	for i := 0; i < totalAgents; i++ {
		agentID := testPrefix + idGenerator.GenerateRequestID()
		// 前 3 个客服负载为 1，第 4-6 个为 2，其余递增
		workload := int64(1)
		if i >= 3 && i < 6 {
			workload = 2
		} else if i >= 6 {
			workload = int64(i)
		}
		agents[agentID] = workload
		onlineAgents = append(onlineAgents, agentID)
	}

	// 清理函数
	defer func() {
		for agentID := range agents {
			_ = customRepo.RemoveAgentWorkload(ctx, agentID)
		}
	}()

	// 批量设置负载
	err := customRepo.BatchSetAgentWorkload(ctx, agents)
	require.NoError(t, err)

	// 多次查询，验证只从前 5 个候选中选择
	selectedCount := make(map[string]int)
	for i := 0; i < 100; i++ {
		selectedAgent, selectedWorkload, err := customRepo.GetLeastLoadedAgent(ctx, onlineAgents)
		require.NoError(t, err)
		assert.LessOrEqual(t, selectedWorkload, int64(2), "应该从前 5 个候选中选择（负载 <= 2）")
		selectedCount[selectedAgent]++
	}

	// 验证选择了多个不同的客服
	assert.GreaterOrEqual(t, len(selectedCount), 2, "应该从多个候选客服中随机选择")
}

// TestRedisWorkloadRepositoryMaxCandidatesLessThanOnline 测试在线客服少于 MaxCandidates 的场景
func TestRedisWorkloadRepositoryMaxCandidatesLessThanOnline(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	// 只创建 3 个客服（远少于默认的 50）
	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent3 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 5,
		agent2: 2,
		agent3: 8,
	}
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	onlineAgents := []string{agent1, agent2, agent3}
	selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, agent2, selectedAgent, "应该选择负载最小的 agent2")
	assert.Equal(t, int64(2), selectedWorkload)
}

// TestRedisWorkloadRepositoryMaxCandidatesSameWorkload 测试多个客服负载相同时的随机选择
func TestRedisWorkloadRepositoryMaxCandidatesSameWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	// 创建 5 个负载相同的客服
	const totalAgents = 5
	agents := make(map[string]int64, totalAgents)
	onlineAgents := make([]string, 0, totalAgents)

	for i := 0; i < totalAgents; i++ {
		agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
		agents[agentID] = 10 // 所有客服负载都是 10
		onlineAgents = append(onlineAgents, agentID)
	}
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	// 多次查询，验证随机选择
	selectedCount := make(map[string]int)
	for i := 0; i < 50; i++ {
		selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
		require.NoError(t, err)
		assert.Equal(t, int64(10), selectedWorkload)
		selectedCount[selectedAgent]++
	}

	// 验证至少选择了 3 个不同的客服（说明是随机的）
	assert.GreaterOrEqual(t, len(selectedCount), 3, "应该从多个负载相同的客服中随机选择")
}

// TestRedisWorkloadRepositoryMaxCandidatesFallback 测试降级场景（ZSet 中没有在线客服）
func TestRedisWorkloadRepositoryMaxCandidatesFallback(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 5,
		agent2: 3,
	}
	defer tr.cleanupMap(agents)

	// 设置负载
	tr.batchSet(agents)

	// 从 ZSet 中移除所有客服（模拟 ZSet 数据丢失）
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agent1))
	require.NoError(t, tr.repo.RemoveAgentWorkload(tr.ctx, agent2))

	// 查询时应该降级到从 string key 中随机选择
	onlineAgents := []string{agent1, agent2}
	selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Contains(t, onlineAgents, selectedAgent, "应该从在线客服中选择")
	assert.True(t, selectedWorkload == 5 || selectedWorkload == 3, "负载应该是 5 或 3")
}

// TestRedisWorkloadRepositoryEmptyOnlineAgents 测试空的在线客服列表
func TestRedisWorkloadRepositoryEmptyOnlineAgents(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	// 传入空的在线客服列表
	_, _, err := tr.repo.GetLeastLoadedAgent(tr.ctx, []string{})
	assert.Error(t, err, "空的在线客服列表应该返回错误")
}

// TestRedisWorkloadRepositoryNonExistentAgent 测试获取不存在的客服负载
func TestRedisWorkloadRepositoryNonExistentAgent(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	nonExistentAgent := tr.agentID("non_existent")

	// 获取不存在的客服负载应该返回 0
	workload := tr.getWorkload(nonExistentAgent)
	assert.Equal(t, int64(0), workload, "不存在的客服负载应该为 0")
}

// TestRedisWorkloadRepositoryConcurrentIncrement 测试并发增加负载
func TestRedisWorkloadRepositoryConcurrentIncrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 0)

	// 并发增加 100 次
	const concurrentCount = 100
	done := make(chan bool, concurrentCount)

	for range concurrentCount {
		go func() {
			_ = tr.repo.IncrementAgentWorkload(tr.ctx, agentID)
			done <- true
		}()
	}

	// 等待所有协程完成
	for range concurrentCount {
		<-done
	}

	// 验证最终负载
	finalWorkload := tr.getWorkload(agentID)
	assert.Equal(t, int64(concurrentCount), finalWorkload, "并发增加后负载应该为 100")
}

// TestRedisWorkloadRepositoryConcurrentDecrement 测试并发减少负载
func TestRedisWorkloadRepositoryConcurrentDecrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	const initialWorkload = 50
	tr.setWorkload(agentID, initialWorkload)

	// 并发减少 30 次
	const concurrentCount = 30
	done := make(chan bool, concurrentCount)

	for range concurrentCount {
		go func() {
			_ = tr.repo.DecrementAgentWorkload(tr.ctx, agentID)
			done <- true
		}()
	}

	// 等待所有协程完成
	for range concurrentCount {
		<-done
	}

	// 验证最终负载
	finalWorkload := tr.getWorkload(agentID)
	assert.Equal(t, int64(initialWorkload-concurrentCount), finalWorkload, "并发减少后负载应该为 20")
}

// TestRedisWorkloadRepositoryBatchSetEmpty 测试批量设置空 map
func TestRedisWorkloadRepositoryBatchSetEmpty(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	// 批量设置空 map 应该不报错
	err := tr.repo.BatchSetAgentWorkload(tr.ctx, map[string]int64{})
	assert.NoError(t, err, "批量设置空 map 应该成功")
}

// TestRedisWorkloadRepositoryGetAllAgentWorkloadsWithLimit 测试带限制的批量获取
func TestRedisWorkloadRepositoryGetAllAgentWorkloadsWithLimit(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	// 创建 10 个客服
	const totalAgents = 10
	agents := make(map[string]int64, totalAgents)

	for i := range totalAgents {
		agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
		agents[agentID] = int64(i)
	}
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	// 只获取前 5 个
	workloads, err := tr.repo.GetAllAgentWorkloads(tr.ctx, 5)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(workloads), 5, "应该最多返回 5 个客服")
}

// TestRedisWorkloadRepositoryMultipleIncrementDecrement 测试多次增减操作
func TestRedisWorkloadRepositoryMultipleIncrementDecrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	tr.setWorkload(agentID, 10)

	// 增加 5 次
	for range 5 {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentID))
	}
	assert.Equal(t, int64(15), tr.getWorkload(agentID))

	// 减少 3 次
	for range 3 {
		require.NoError(t, tr.repo.DecrementAgentWorkload(tr.ctx, agentID))
	}
	assert.Equal(t, int64(12), tr.getWorkload(agentID))

	// 再增加 2 次
	for range 2 {
		require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentID))
	}
	assert.Equal(t, int64(14), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositorySetWorkloadOverwrite 测试覆盖设置负载
func TestRedisWorkloadRepositorySetWorkloadOverwrite(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 设置初始负载
	tr.setWorkload(agentID, 10)
	assert.Equal(t, int64(10), tr.getWorkload(agentID))

	// 覆盖设置
	tr.setWorkload(agentID, 5)
	assert.Equal(t, int64(5), tr.getWorkload(agentID))

	// 再次覆盖
	tr.setWorkload(agentID, 0)
	assert.Equal(t, int64(0), tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryRemoveNonExistentAgent 测试移除不存在的客服
func TestRedisWorkloadRepositoryRemoveNonExistentAgent(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	nonExistentAgent := tr.agentID("non_existent")

	// 移除不存在的客服应该不报错
	err := tr.repo.RemoveAgentWorkload(tr.ctx, nonExistentAgent)
	assert.NoError(t, err, "移除不存在的客服应该成功")
}

// TestRedisWorkloadRepositorySyncNonExistentAgent 测试同步不存在的客服
func TestRedisWorkloadRepositorySyncNonExistentAgent(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	nonExistentAgent := tr.agentID("non_existent")
	defer tr.cleanup(nonExistentAgent)

	// 同步不存在的客服应该设置为 0
	err := tr.repo.SyncAgentWorkloadToZSet(tr.ctx, nonExistentAgent)
	require.NoError(t, err)

	// 验证负载为 0
	workload := tr.getWorkload(nonExistentAgent)
	assert.Equal(t, int64(0), workload, "不存在的客服同步后负载应该为 0")
}

// TestRedisWorkloadRepositoryGetLeastLoadedAgentPartialOnline 测试部分客服在线
func TestRedisWorkloadRepositoryGetLeastLoadedAgentPartialOnline(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent3 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent4 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 5,
		agent2: 2,
		agent3: 8,
		agent4: 3,
	}
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	// 只有 agent1 和 agent3 在线
	onlineAgents := []string{agent1, agent3}
	selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(tr.ctx, onlineAgents)
	require.NoError(t, err)
	assert.Equal(t, agent1, selectedAgent, "在线客服中负载最小的应该是 agent1 (负载=5)")
	assert.Equal(t, int64(5), selectedWorkload)
}

// TestRedisWorkloadRepositoryInitAgentWorkloadFromDB 测试从 DB 恢复负载
func TestRedisWorkloadRepositoryInitAgentWorkloadFromDB(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 首次初始化
	workload, err := tr.repo.InitAgentWorkload(tr.ctx, agentID, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), workload)

	// 设置负载
	tr.setWorkload(agentID, 15)

	// 等待异步同步到 DB
	time.Sleep(500 * time.Millisecond)

	// 模拟 Redis 数据丢失（删除 Redis key 和 ZSet）
	client := GetTestRedisClient(t)
	workloadKey := tr.testPrefix + "agent:" + agentID
	zsetKey := tr.testPrefix + "zset"
	require.NoError(t, client.Del(tr.ctx, workloadKey).Err())
	require.NoError(t, client.ZRem(tr.ctx, zsetKey, agentID).Err())

	// 重新初始化，应该从 DB 恢复
	workload, err = tr.repo.InitAgentWorkload(tr.ctx, agentID, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(15), workload, "应该从 DB 恢复负载")
}

// TestRedisWorkloadRepositoryLargeWorkloadValue 测试大负载值
func TestRedisWorkloadRepositoryLargeWorkloadValue(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 设置一个很大的负载值
	const largeWorkload = int64(1000000)
	tr.setWorkload(agentID, largeWorkload)
	assert.Equal(t, largeWorkload, tr.getWorkload(agentID))

	// 增加后应该正确
	require.NoError(t, tr.repo.IncrementAgentWorkload(tr.ctx, agentID))
	assert.Equal(t, largeWorkload+1, tr.getWorkload(agentID))
}

// TestRedisWorkloadRepositoryZeroWorkloadSelection 测试负载为 0 的客服选择
func TestRedisWorkloadRepositoryZeroWorkloadSelection(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent3 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 0,
		agent2: 0,
		agent3: 5,
	}
	defer tr.cleanupMap(agents)

	tr.batchSet(agents)

	// 多次查询，应该从负载为 0 的客服中随机选择
	selectedCount := make(map[string]int)
	for range 20 {
		selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(
			tr.ctx,
			[]string{agent1, agent2, agent3},
		)
		require.NoError(t, err)
		assert.Equal(t, int64(0), selectedWorkload, "应该选择负载为 0 的客服")
		selectedCount[selectedAgent]++
	}

	// 验证选择了多个负载为 0 的客服
	assert.Contains(t, selectedCount, agent1, "应该选择过 agent1")
	assert.Contains(t, selectedCount, agent2, "应该选择过 agent2")
	assert.NotContains(t, selectedCount, agent3, "不应该选择 agent3（负载不是最小）")
}

// TestRedisWorkloadRepositoryContextCancellation 测试上下文取消
func TestRedisWorkloadRepositoryContextCancellation(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 创建一个已取消的上下文
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// 操作应该失败
	err := tr.repo.SetAgentWorkload(ctx, agentID, 10)
	assert.Error(t, err, "已取消的上下文应该导致操作失败")
}

// TestRedisWorkloadRepositoryClose 测试关闭仓库
func TestRedisWorkloadRepositoryClose(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	// 关闭仓库应该不报错
	err := tr.repo.Close()
	assert.NoError(t, err, "关闭仓库应该成功")
}
