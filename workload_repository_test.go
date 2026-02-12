/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-18 09:00:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:23:08
 * @FilePath: \go-wsc\workload_repository_test.go
 * @Description: 客服负载管理测试 - 多维度架构
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
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var (
	testWorkloadKeyPrefix = "test:workload:"
)

// testWorkloadRepo 测试辅助结构
type testWorkloadRepo struct {
	repo        repository.WorkloadRepository       // 使用接口类型
	repoImpl    *repository.RedisWorkloadRepository // 保存具体实现，用于访问内部方法
	client      *redis.Client                       // Redis 客户端
	db          *gorm.DB                            // 数据库连接
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

	repoInterface := repository.NewRedisWorkloadRepository(client, db, &wscconfig.Workload{
		KeyPrefix: testPrefix,
	}, NewDefaultWSCLogger())

	// 类型断言为具体类型，用于访问内部方法
	repoImpl, ok := repoInterface.(*repository.RedisWorkloadRepository)
	if !ok {
		t.Fatal("类型断言失败：无法转换为 *repository.RedisWorkloadRepository")
	}

	return &testWorkloadRepo{
		repo:        repoInterface,
		repoImpl:    repoImpl,
		client:      client,
		db:          db,
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

	// 检查是否是连接错误（非 redis.Nil）
	if err != nil && err != redis.Nil {
		tr.t.Skipf("⚠️ Redis连接不健康，跳过测试: %v", err)
	}
}

// agentID 生成带测试前缀的客服ID
func (tr *testWorkloadRepo) agentID(name string) string {
	return tr.testPrefix + name
}

// getWorkloadFromDimension 从指定维度获取客服负载
func (tr *testWorkloadRepo) getWorkloadFromDimension(agentID string, dimension repository.WorkloadDimension) int64 {
	now := time.Now()
	key := tr.repoImpl.GetDimensionKey(dimension, agentID, now)
	workloadStr, err := tr.client.Get(tr.ctx, key).Result()
	if err != nil {
		return 0
	}

	var workload int64
	_, _ = fmt.Sscanf(workloadStr, "%d", &workload)
	return workload
}

// cleanup 清理单个客服的测试数据
func (tr *testWorkloadRepo) cleanup(agentID string) {
	now := time.Now()

	// 清理所有维度的 Redis 数据
	for _, dimension := range repository.AllWorkloadDimensions {
		// 清理 string key
		key := tr.repoImpl.GetDimensionKey(dimension, agentID, now)
		_ = tr.client.Del(tr.ctx, key).Err()

		// 清理 ZSet
		zsetKey := tr.repoImpl.GetDimensionZSetKey(dimension, now)
		_ = tr.client.ZRem(tr.ctx, zsetKey, agentID).Err()
	}

	// 清理数据库记录
	_ = tr.db.Where("agent_id = ?", agentID).Delete(&AgentWorkloadModel{}).Error
}

// cleanupMap 清理多个客服的测试数据
func (tr *testWorkloadRepo) cleanupMap(agents map[string]int64) {
	for agentID := range agents {
		tr.cleanup(agentID)
	}
}

// ============================================================================
// 1. 多维度存储测试
// ============================================================================

// TestMultiDimensionStorage 测试多维度存储
func TestMultiDimensionStorage(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 设置负载
	err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, 10)
	require.NoError(t, err)

	// 验证所有维度都有数据
	for _, dimension := range repository.AllWorkloadDimensions {
		workload := tr.getWorkloadFromDimension(agentID, dimension)
		assert.Equal(t, int64(10), workload, "维度 %s 的负载应该是 10", dimension)
	}
}

// TestMultiDimensionIncrement 测试多维度增加
func TestMultiDimensionIncrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 初始化
	err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, 5)
	require.NoError(t, err)

	// 增加负载
	err = tr.repo.IncrementAgentWorkload(tr.ctx, agentID)
	require.NoError(t, err)

	// 验证所有维度都增加了
	for _, dimension := range repository.AllWorkloadDimensions {
		workload := tr.getWorkloadFromDimension(agentID, dimension)
		assert.Equal(t, int64(6), workload, "维度 %s 的负载应该是 6", dimension)
	}
}

// TestMultiDimensionDecrement 测试多维度减少
func TestMultiDimensionDecrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 初始化
	err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, 10)
	require.NoError(t, err)

	// 减少负载
	err = tr.repo.DecrementAgentWorkload(tr.ctx, agentID)
	require.NoError(t, err)

	// 验证所有维度都减少了
	for _, dimension := range repository.AllWorkloadDimensions {
		workload := tr.getWorkloadFromDimension(agentID, dimension)
		assert.Equal(t, int64(9), workload, "维度 %s 的负载应该是 9", dimension)
	}
}

// ============================================================================
// 2. 按维度查询测试
// ============================================================================

// TestGetLeastLoadedAgentByDimension 测试按不同维度查询负载最小的客服
func TestGetLeastLoadedAgentByDimension(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agent1 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent2 := tr.agentID(tr.idGenerator.GenerateRequestID())
	agent3 := tr.agentID(tr.idGenerator.GenerateRequestID())

	agents := map[string]int64{
		agent1: 5,
		agent2: 2,
		agent3: 8,
	}
	defer tr.cleanupMap(agents)

	// 批量设置
	for agentID, workload := range agents {
		err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, workload)
		require.NoError(t, err)
	}

	onlineAgents := []string{agent1, agent2, agent3}

	// 测试所有维度
	for _, dimension := range repository.AllWorkloadDimensions {
		selectedAgent, selectedWorkload, err := tr.repo.GetLeastLoadedAgent(
			tr.ctx,
			onlineAgents,
			dimension,
		)
		require.NoError(t, err)
		assert.Equal(t, agent2, selectedAgent, "维度 %s: 负载最小的应该是 agent2", dimension)
		assert.Equal(t, int64(2), selectedWorkload, "维度 %s: 负载应该是 2", dimension)
	}
}

// ============================================================================
// 3. 下线清理测试
// ============================================================================

// TestRemoveAgentWorkloadAllDimensions 测试移除客服时清理所有维度
func TestRemoveAgentWorkloadAllDimensions(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 设置负载
	err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, 10)
	require.NoError(t, err)

	// 验证所有维度都有数据
	for _, dimension := range repository.AllWorkloadDimensions {
		workload := tr.getWorkloadFromDimension(agentID, dimension)
		assert.Equal(t, int64(10), workload)
	}

	// 移除客服
	err = tr.repo.RemoveAgentWorkload(tr.ctx, agentID)
	require.NoError(t, err)

	// 验证所有维度的 ZSet 都已移除（string key 保留）
	now := time.Now()
	for _, dimension := range repository.AllWorkloadDimensions {
		zsetKey := tr.repoImpl.GetDimensionZSetKey(dimension, now)
		score := tr.client.ZScore(tr.ctx, zsetKey, agentID).Val()
		assert.Equal(t, float64(0), score, "维度 %s 的 ZSet 应该已移除", dimension)

		// string key 应该保留
		workload := tr.getWorkloadFromDimension(agentID, dimension)
		assert.Equal(t, int64(10), workload, "维度 %s 的 string key 应该保留", dimension)
	}
}

// ============================================================================
// 4. 故障恢复测试
// ============================================================================

// TestReloadAgentWorkload 测试重新加载客服负载
func TestReloadAgentWorkload(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 设置初始负载
	err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, 15)
	require.NoError(t, err)

	// 模拟客服下线（移除 ZSet）
	err = tr.repo.RemoveAgentWorkload(tr.ctx, agentID)
	require.NoError(t, err)

	// 重新加载（模拟客服上线）
	workload, err := tr.repo.ReloadAgentWorkload(tr.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(15), workload, "应该从 Redis 恢复负载")

	// 验证所有维度的 ZSet 都已恢复
	now := time.Now()
	for _, dimension := range repository.AllWorkloadDimensions {
		zsetKey := tr.repoImpl.GetDimensionZSetKey(dimension, now)
		score := tr.client.ZScore(tr.ctx, zsetKey, agentID).Val()
		assert.Equal(t, float64(15), score, "维度 %s 的 ZSet 应该已恢复", dimension)
	}
}

// ============================================================================
// 5. 边界条件测试
// ============================================================================

// TestDecrementBelowZero 测试负载不会减到负数
func TestDecrementBelowZero(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 初始化为 0
	err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, 0)
	require.NoError(t, err)

	// 尝试减少
	err = tr.repo.DecrementAgentWorkload(tr.ctx, agentID)
	require.NoError(t, err)

	// 验证所有维度都是 0（不会变成负数）
	for _, dimension := range repository.AllWorkloadDimensions {
		workload := tr.getWorkloadFromDimension(agentID, dimension)
		assert.Equal(t, int64(0), workload, "维度 %s 的负载不应该是负数", dimension)
	}
}

// TestEmptyOnlineAgents 测试空的在线客服列表
func TestEmptyOnlineAgents(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	_, _, err := tr.repo.GetLeastLoadedAgent(tr.ctx, []string{}, repository.WorkloadDimensionRealtime)
	assert.Error(t, err, "空的在线客服列表应该返回错误")
}

// ============================================================================
// 6. 并发测试
// ============================================================================

// TestConcurrentIncrement 测试并发增加
func TestConcurrentIncrement(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	agentID := tr.agentID(tr.idGenerator.GenerateRequestID())
	defer tr.cleanup(agentID)

	// 初始化
	err := tr.repo.ForceSetAgentWorkload(tr.ctx, agentID, 0)
	require.NoError(t, err)

	// 并发增加 10 次
	const concurrentCount = 10
	done := make(chan bool, concurrentCount)

	for range concurrentCount {
		go func() {
			_ = tr.repo.IncrementAgentWorkload(tr.ctx, agentID)
			done <- true
		}()
	}

	// 等待完成
	for range concurrentCount {
		<-done
	}

	// 验证最终负载
	for _, dimension := range repository.AllWorkloadDimensions {
		workload := tr.getWorkloadFromDimension(agentID, dimension)
		assert.Equal(t, int64(concurrentCount), workload, "维度 %s 的负载应该是 %d", dimension, concurrentCount)
	}
}

// TestClose 测试关闭仓库
func TestClose(t *testing.T) {
	tr := newTestWorkloadRepo(t)
	tr.checkRedisHealth()

	err := tr.repo.Close()
	assert.NoError(t, err)
}
