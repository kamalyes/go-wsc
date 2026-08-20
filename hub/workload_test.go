/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:02:15
 * @FilePath: \go-wsc\hub\workload_test.go
 * @Description: 客服工作负载管理白盒单元测试（覆盖 hub/workload.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// fakeWorkloadRepo WorkloadRepository 的内存 fake 实现
type fakeWorkloadRepo struct {
	forceSetErr          error
	getWorkload          int64
	getErr               error
	removeErr            error
	incrErr              error
	decrErr              error
	leastLoadedID        string
	leastLoadedWorkload  int64
	leastLoadedErr       error
	acquireID            string
	acquireWorkload      int64
	acquireErr           error
	reloadWorkload       int64
	reloadErr            error
	allWorkloads         []WorkloadInfo
	allErr               error
	lastNamespace        string
	lastForceSetAgent    string
	lastForceSetWorkload int64
	lastAcquireAgents    []string
	lastAcquireDim       WorkloadDimension
	lastLeastAgents      []string
	lastLeastDim         WorkloadDimension
	lastReloadAgent      string
	lastGetAgent         string
	lastRemoveAgent      string
	lastIncrAgent        string
	lastDecrAgent        string
	lastAllLimit         int64
}

func (f *fakeWorkloadRepo) ReloadAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastReloadAgent = agentID
	return f.reloadWorkload, f.reloadErr
}
func (f *fakeWorkloadRepo) ForceSetAgentWorkload(ctx context.Context, agentID string, workload int64) error {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastForceSetAgent = agentID
	f.lastForceSetWorkload = workload
	return f.forceSetErr
}
func (f *fakeWorkloadRepo) GetAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastGetAgent = agentID
	return f.getWorkload, f.getErr
}
func (f *fakeWorkloadRepo) IncrementAgentWorkload(ctx context.Context, agentID string) error {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastIncrAgent = agentID
	return f.incrErr
}
func (f *fakeWorkloadRepo) DecrementAgentWorkload(ctx context.Context, agentID string) error {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastDecrAgent = agentID
	return f.decrErr
}
func (f *fakeWorkloadRepo) GetLeastLoadedAgent(ctx context.Context, agents []string, dim WorkloadDimension) (string, int64, error) {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastLeastAgents = agents
	f.lastLeastDim = dim
	return f.leastLoadedID, f.leastLoadedWorkload, f.leastLoadedErr
}
func (f *fakeWorkloadRepo) AcquireLeastLoadedAgent(ctx context.Context, agents []string, dim WorkloadDimension) (string, int64, error) {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastAcquireAgents = agents
	f.lastAcquireDim = dim
	return f.acquireID, f.acquireWorkload, f.acquireErr
}
func (f *fakeWorkloadRepo) RemoveAgentWorkload(ctx context.Context, agentID string) error {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastRemoveAgent = agentID
	return f.removeErr
}
func (f *fakeWorkloadRepo) GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error) {
	f.lastNamespace = routing.NamespaceFromContext(ctx)
	f.lastAllLimit = limit
	return f.allWorkloads, f.allErr
}
func (f *fakeWorkloadRepo) Close() error { return nil }

// TestWorkload_NoRepo 验证所有方法在 repository 未设置时返回错误
func TestWorkload_NoRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("ForceSetAgentWorkload", func(t *testing.T) {
		err := hub.ForceSetAgentWorkload(ctx, "a1", 5)
		assert.Error(t, err)
	})
	t.Run("GetAgentWorkload", func(t *testing.T) {
		_, err := hub.GetAgentWorkload(ctx, "a1")
		assert.Error(t, err)
	})
	t.Run("RemoveAgentWorkload", func(t *testing.T) {
		err := hub.RemoveAgentWorkload(ctx, "a1")
		assert.Error(t, err)
	})
	t.Run("IncrementAgentWorkload", func(t *testing.T) {
		err := hub.IncrementAgentWorkload(ctx, "a1")
		assert.Error(t, err)
	})
	t.Run("DecrementAgentWorkload", func(t *testing.T) {
		err := hub.DecrementAgentWorkload(ctx, "a1")
		assert.Error(t, err)
	})
	t.Run("GetLeastLoadedAgent", func(t *testing.T) {
		_, _, err := hub.GetLeastLoadedAgent(ctx, models.WorkloadDimensionRealtime)
		assert.Error(t, err)
	})
	t.Run("AcquireLeastLoadedAgent", func(t *testing.T) {
		_, _, err := hub.AcquireLeastLoadedAgent(ctx, []string{"a1"}, models.WorkloadDimensionRealtime)
		assert.Error(t, err)
	})
	t.Run("ReloadAgentWorkload", func(t *testing.T) {
		_, err := hub.ReloadAgentWorkload(ctx, "a1")
		assert.Error(t, err)
	})
	t.Run("GetAllAgentWorkloads", func(t *testing.T) {
		_, err := hub.GetAllAgentWorkloads(ctx, 10)
		assert.Error(t, err)
	})
}

// TestWorkload_NamespacePassthrough 验证 Hub 层 ctx 中的 namespace 透传到仓储层
func TestWorkload_NamespacePassthrough(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{getWorkload: 7, leastLoadedID: "a1"}
	hub.SetWorkloadRepository(repo)
	// 注入自定义 namespace 到 ctx，验证透传
	customNS := "custom-ns"
	ctx := routing.NewRoute().WithAppID("").WithNamespace(customNS).WithGroupIDs(nil).Inject(context.Background())

	t.Run("GetAgentWorkload→CustomNamespace", func(t *testing.T) {
		_, _ = hub.GetAgentWorkload(ctx, "a1")
		assert.Equal(t, customNS, repo.lastNamespace)
	})
	t.Run("IncrementAgentWorkload→CustomNamespace", func(t *testing.T) {
		_ = hub.IncrementAgentWorkload(ctx, "a1")
		assert.Equal(t, customNS, repo.lastNamespace)
	})
	t.Run("GetLeastLoadedAgent→CustomNamespace", func(t *testing.T) {
		hub.shardedRegistry.AddClient(makeAgentClient("c-a1", "a1"))
		_, _, _ = hub.GetLeastLoadedAgent(ctx, models.WorkloadDimensionRealtime)
		assert.Equal(t, customNS, repo.lastNamespace)
	})
}

// TestWorkload_DefaultNamespaceFallback 验证 ctx 无 namespace 时兜底 DefaultNamespace
func TestWorkload_DefaultNamespaceFallback(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{}
	hub.SetWorkloadRepository(repo)
	// ctx 不注入 namespace，验证 repository 内部兜底 DefaultNamespace
	// 注意：fakeWorkloadRepo 只记录从 ctx 取到的 namespace（空字符串），
	// 真实 RedisWorkloadRepository 内部会兜底 DefaultNamespace
	ctx := context.Background()

	_ = hub.ForceSetAgentWorkload(ctx, "a1", 1)
	// fakeWorkloadRepo.lastNamespace 记录的是 ctx 中的原始值（空），
	// 真实 repository 会用 mathx.IfEmpty 兜底，这里只验证 ctx 透传正确
	assert.Empty(t, repo.lastNamespace) // ctx 中无 namespace，fake 记录为空
}

// TestForceSetAgentWorkload_WithRepo 验证强制设置工作负载
func TestForceSetAgentWorkload_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{}
	hub.SetWorkloadRepository(repo)
	ctx := routing.NewRoute().WithAppID("").WithNamespace(models.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())

	require.NoError(t, hub.ForceSetAgentWorkload(ctx, "a1", 42))
	assert.Equal(t, "a1", repo.lastForceSetAgent)
	assert.Equal(t, int64(42), repo.lastForceSetWorkload)
	assert.Equal(t, models.DefaultNamespace, repo.lastNamespace)
}

// TestGetAgentWorkload_WithRepo 验证获取工作负载
func TestGetAgentWorkload_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{getWorkload: 7}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	wl, err := hub.GetAgentWorkload(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, int64(7), wl)
	assert.Equal(t, "a1", repo.lastGetAgent)
}

// TestRemoveAgentWorkload_WithRepo 验证移除工作负载
func TestRemoveAgentWorkload_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	require.NoError(t, hub.RemoveAgentWorkload(ctx, "a1"))
	assert.Equal(t, "a1", repo.lastRemoveAgent)
}

// TestIncrementDecrementAgentWorkload_WithRepo 验证增减工作负载
func TestIncrementDecrementAgentWorkload_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	require.NoError(t, hub.IncrementAgentWorkload(ctx, "a1"))
	assert.Equal(t, "a1", repo.lastIncrAgent)

	require.NoError(t, hub.DecrementAgentWorkload(ctx, "a1"))
	assert.Equal(t, "a1", repo.lastDecrAgent)
}

// TestGetLeastLoadedAgent_WithRepo 验证获取负载最小的客服
func TestGetLeastLoadedAgent_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 注册在线客服
	hub.shardedRegistry.AddClient(makeAgentClient("c-a1", "a1"))
	hub.shardedRegistry.AddClient(makeAgentClient("c-a2", "a2"))

	repo := &fakeWorkloadRepo{leastLoadedID: "a1", leastLoadedWorkload: 3}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	id, wl, err := hub.GetLeastLoadedAgent(ctx, models.WorkloadDimensionRealtime)
	require.NoError(t, err)
	assert.Equal(t, "a1", id)
	assert.Equal(t, int64(3), wl)
	assert.ElementsMatch(t, []string{"a1", "a2"}, repo.lastLeastAgents)
	assert.Equal(t, models.WorkloadDimensionRealtime, repo.lastLeastDim)
}

// TestGetLeastLoadedAgent_NoOnlineAgent 验证无在线客服时返回空
func TestGetLeastLoadedAgent_NoOnlineAgent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	id, wl, err := hub.GetLeastLoadedAgent(ctx, models.WorkloadDimensionRealtime)
	require.NoError(t, err)
	assert.Empty(t, id)
	assert.Equal(t, int64(0), wl)
}

// TestAcquireLeastLoadedAgent_WithProvidedAgents 验证传入非空在线客服列表
func TestAcquireLeastLoadedAgent_WithProvidedAgents(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{acquireID: "a2", acquireWorkload: 1}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	id, wl, err := hub.AcquireLeastLoadedAgent(ctx, []string{"a1", "a2"}, models.WorkloadDimensionHourly)
	require.NoError(t, err)
	assert.Equal(t, "a2", id)
	assert.Equal(t, int64(1), wl)
	assert.ElementsMatch(t, []string{"a1", "a2"}, repo.lastAcquireAgents)
	assert.Equal(t, models.WorkloadDimensionHourly, repo.lastAcquireDim)
}

// TestAcquireLeastLoadedAgent_EmptyAgentsAutoFetch 验证传入空列表时自动获取在线客服
func TestAcquireLeastLoadedAgent_EmptyAgentsAutoFetch(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeAgentClient("c-a1", "a1"))
	repo := &fakeWorkloadRepo{acquireID: "a1", acquireWorkload: 0}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	id, _, err := hub.AcquireLeastLoadedAgent(ctx, nil, models.WorkloadDimensionDaily)
	require.NoError(t, err)
	assert.Equal(t, "a1", id)
	assert.Contains(t, repo.lastAcquireAgents, "a1")
}

// TestAcquireLeastLoadedAgent_NoOnlineAgent 验证空列表且无在线客服时返回空
func TestAcquireLeastLoadedAgent_NoOnlineAgent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	id, wl, err := hub.AcquireLeastLoadedAgent(ctx, nil, models.WorkloadDimensionRealtime)
	require.NoError(t, err)
	assert.Empty(t, id)
	assert.Equal(t, int64(0), wl)
}

// TestReloadAgentWorkload_WithRepo 验证重新加载工作负载
func TestReloadAgentWorkload_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeWorkloadRepo{reloadWorkload: 9}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	wl, err := hub.ReloadAgentWorkload(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, int64(9), wl)
	assert.Equal(t, "a1", repo.lastReloadAgent)
}

// TestGetAllAgentWorkloads_WithRepo 验证获取所有客服负载
func TestGetAllAgentWorkloads_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	expected := []WorkloadInfo{{AgentID: "a1", Workload: 5}}
	repo := &fakeWorkloadRepo{allWorkloads: expected}
	hub.SetWorkloadRepository(repo)
	ctx := context.Background()

	got, err := hub.GetAllAgentWorkloads(ctx, 20)
	require.NoError(t, err)
	assert.Equal(t, expected, got)
	assert.Equal(t, int64(20), repo.lastAllLimit)
}
