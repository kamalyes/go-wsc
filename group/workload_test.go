/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:44:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:44:00
 * @FilePath: \go-wsc\group\workload_test.go
 * @Description: 客服负载管理器单元测试
 *
 * 用嵌入接口的假实现（Host / spi.WorkloadStore 均为嵌入字段）：
 * 只覆盖被测路径用到的方法，未覆盖的方法保留 nil 嵌入接口，
 * 一旦被调用即 panic —— 从而暴露未声明的隐藏依赖。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"
	"errors"
	"testing"

	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 假实现
// ============================================================================

// fakeHost 嵌入 Host，只实现本域各子能力真正用到的方法
type fakeHost struct {
	Host

	ctx      context.Context
	logger   spi.Logger
	reg      *connection.ShardedRegistry
	store    spi.WorkloadStore
	groupSvc spi.GroupStore
	agents   []string
	agentsFn func() ([]string, error)
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		ctx: context.Background(),
		// 日志器与注册表是无条件依赖：几乎每个方法都要记日志或查本地连接，都不是可选存储后端，因此默认建好而非留 nil
		logger: spi.NewDefaultLogger(),
		reg:    connection.NewShardedRegistry(false, false, connection.RegistryCapacity{}),
	}
}

func (f *fakeHost) Context() context.Context { return f.ctx }
func (f *fakeHost) GetLogger() spi.Logger    { return f.logger }
func (f *fakeHost) GetShardedRegistry() *connection.ShardedRegistry {
	return f.reg
}
func (f *fakeHost) GetWorkloadStore() spi.WorkloadStore {
	return f.store
}

// GetGroupStore 默认 nil —— 群组仓储未注入时必须让调用方显式报错而非静默 no-op
func (f *fakeHost) GetGroupStore() spi.GroupStore { return f.groupSvc }

// TrySubmitCallback 同步执行，避免异步回调引入断言时序不确定性
func (f *fakeHost) TrySubmitCallback(task func()) bool {
	task()
	return true
}

func (f *fakeHost) GetOnlineUsersByType(models.UserType) ([]string, error) {
	if f.agentsFn != nil {
		return f.agentsFn()
	}
	return f.agents, nil
}

// fakeWorkloadStore 嵌入 spi.WorkloadStore，只实现被断言到的方法
type fakeWorkloadStore struct {
	spi.WorkloadStore

	calls          []string
	gotAgents      []string
	gotDimension   models.WorkloadDimension
	acquireErr     error
	acquireAgent   string
	acquireLoad    int64
	leastLoadAgent string
	leastLoadLoad  int64
	leastLoadErr   error
}

func (s *fakeWorkloadStore) record(name string) {
	s.calls = append(s.calls, name)
}

func (s *fakeWorkloadStore) ForceSetAgentWorkload(context.Context, string, int64) error {
	s.record("ForceSetAgentWorkload")
	return nil
}

func (s *fakeWorkloadStore) GetAgentWorkload(context.Context, string) (int64, error) {
	s.record("GetAgentWorkload")
	return 7, nil
}

func (s *fakeWorkloadStore) RemoveAgentWorkload(context.Context, string) error {
	s.record("RemoveAgentWorkload")
	return nil
}

func (s *fakeWorkloadStore) IncrementAgentWorkload(context.Context, string) error {
	s.record("IncrementAgentWorkload")
	return nil
}

func (s *fakeWorkloadStore) DecrementAgentWorkload(context.Context, string) error {
	s.record("DecrementAgentWorkload")
	return nil
}

func (s *fakeWorkloadStore) ReloadAgentWorkload(context.Context, string) (int64, error) {
	s.record("ReloadAgentWorkload")
	return 3, nil
}

func (s *fakeWorkloadStore) GetAllAgentWorkloads(context.Context, int64) ([]models.WorkloadInfo, error) {
	s.record("GetAllAgentWorkloads")
	return []models.WorkloadInfo{{AgentID: "a1", Workload: 2}}, nil
}

func (s *fakeWorkloadStore) GetLeastLoadedAgent(_ context.Context, agents []string, dim models.WorkloadDimension) (string, int64, error) {
	s.record("GetLeastLoadedAgent")
	s.gotAgents, s.gotDimension = agents, dim
	return s.leastLoadAgent, s.leastLoadLoad, s.leastLoadErr
}

func (s *fakeWorkloadStore) AcquireLeastLoadedAgent(_ context.Context, agents []string, dim models.WorkloadDimension) (string, int64, error) {
	s.record("AcquireLeastLoadedAgent")
	s.gotAgents, s.gotDimension = agents, dim
	return s.acquireAgent, s.acquireLoad, s.acquireErr
}

// ============================================================================
// 未注入仓储：必须显式报错，绝不能静默 no-op
// ============================================================================

func TestWorkloadMethodsErrorWithoutStore(t *testing.T) {
	ctx := context.Background()
	mgr := NewWorkloadManager(newFakeHost()) // store == nil

	const want = "workloadRepo is not initialized"

	if err := mgr.ForceSetAgentWorkload(ctx, "a1", 1); err == nil || err.Error() != want {
		t.Errorf("ForceSetAgentWorkload err = %v, want %q", err, want)
	}
	if err := mgr.RemoveAgentWorkload(ctx, "a1"); err == nil || err.Error() != want {
		t.Errorf("RemoveAgentWorkload err = %v, want %q", err, want)
	}
	if err := mgr.IncrementAgentWorkload(ctx, "a1"); err == nil || err.Error() != want {
		t.Errorf("IncrementAgentWorkload err = %v, want %q", err, want)
	}
	if err := mgr.DecrementAgentWorkload(ctx, "a1"); err == nil || err.Error() != want {
		t.Errorf("DecrementAgentWorkload err = %v, want %q", err, want)
	}
	if _, err := mgr.GetAgentWorkload(ctx, "a1"); err == nil || err.Error() != want {
		t.Errorf("GetAgentWorkload err = %v, want %q", err, want)
	}
	if _, err := mgr.ReloadAgentWorkload(ctx, "a1"); err == nil || err.Error() != want {
		t.Errorf("ReloadAgentWorkload err = %v, want %q", err, want)
	}
	if _, err := mgr.GetAllAgentWorkloads(ctx, 10); err == nil || err.Error() != want {
		t.Errorf("GetAllAgentWorkloads err = %v, want %q", err, want)
	}
	if _, _, err := mgr.GetLeastLoadedAgent(ctx, models.WorkloadDimensionRealtime); err == nil || err.Error() != want {
		t.Errorf("GetLeastLoadedAgent err = %v, want %q", err, want)
	}
	if _, _, err := mgr.AcquireLeastLoadedAgent(ctx, nil, models.WorkloadDimensionRealtime); err == nil || err.Error() != want {
		t.Errorf("AcquireLeastLoadedAgent err = %v, want %q", err, want)
	}
}

// ============================================================================
// 注入仓储：正常透传
// ============================================================================

func TestWorkloadMethodsDelegateToStore(t *testing.T) {
	ctx := context.Background()
	host := newFakeHost()
	store := &fakeWorkloadStore{}
	host.store = store
	mgr := NewWorkloadManager(host)

	if err := mgr.ForceSetAgentWorkload(ctx, "a1", 5); err != nil {
		t.Fatalf("ForceSetAgentWorkload: %v", err)
	}
	if n, err := mgr.GetAgentWorkload(ctx, "a1"); err != nil || n != 7 {
		t.Fatalf("GetAgentWorkload = (%d, %v), want (7, nil)", n, err)
	}
	if n, err := mgr.ReloadAgentWorkload(ctx, "a1"); err != nil || n != 3 {
		t.Fatalf("ReloadAgentWorkload = (%d, %v), want (3, nil)", n, err)
	}
	if list, err := mgr.GetAllAgentWorkloads(ctx, 10); err != nil || len(list) != 1 {
		t.Fatalf("GetAllAgentWorkloads = (%v, %v), want 1 item", list, err)
	}
	if err := mgr.RemoveAgentWorkload(ctx, "a1"); err != nil {
		t.Fatalf("RemoveAgentWorkload: %v", err)
	}
	if err := mgr.IncrementAgentWorkload(ctx, "a1"); err != nil {
		t.Fatalf("IncrementAgentWorkload: %v", err)
	}
	if err := mgr.DecrementAgentWorkload(ctx, "a1"); err != nil {
		t.Fatalf("DecrementAgentWorkload: %v", err)
	}

	want := []string{
		"ForceSetAgentWorkload", "GetAgentWorkload", "ReloadAgentWorkload",
		"GetAllAgentWorkloads", "RemoveAgentWorkload", "IncrementAgentWorkload",
		"DecrementAgentWorkload",
	}
	if len(store.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", store.calls, want)
	}
	for i := range want {
		if store.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", store.calls, want)
		}
	}
}

// ============================================================================
// 在线客服列表补齐
// ============================================================================

func TestGetLeastLoadedAgentUsesOnlineAgents(t *testing.T) {
	host := newFakeHost()
	host.agents = []string{"a1", "a2"}
	store := &fakeWorkloadStore{leastLoadAgent: "a2", leastLoadLoad: 1}
	host.store = store
	mgr := NewWorkloadManager(host)

	id, load, err := mgr.GetLeastLoadedAgent(context.Background(), models.WorkloadDimensionRealtime)
	if err != nil || id != "a2" || load != 1 {
		t.Fatalf("GetLeastLoadedAgent = (%q, %d, %v), want (a2, 1, nil)", id, load, err)
	}
	if len(store.gotAgents) != 2 {
		t.Fatalf("传给仓储的在线客服 = %v, want 2 个", store.gotAgents)
	}
}

func TestGetLeastLoadedAgentNoOnlineAgentsSkipsStore(t *testing.T) {
	host := newFakeHost()
	host.agents = nil
	store := &fakeWorkloadStore{}
	host.store = store
	mgr := NewWorkloadManager(host)

	id, load, err := mgr.GetLeastLoadedAgent(context.Background(), models.WorkloadDimensionRealtime)
	if err != nil || id != "" || load != 0 {
		t.Fatalf("GetLeastLoadedAgent = (%q, %d, %v), want 空结果", id, load, err)
	}
	// 无在线客服时不应触碰仓储（避免无意义的 Redis 往返）
	if len(store.calls) != 0 {
		t.Fatalf("仓储被调用 = %v, want 空", store.calls)
	}
}

func TestGetLeastLoadedAgentPropagatesRegistryError(t *testing.T) {
	host := newFakeHost()
	wantErr := errors.New("registry down")
	host.agentsFn = func() ([]string, error) { return nil, wantErr }
	host.store = &fakeWorkloadStore{}
	mgr := NewWorkloadManager(host)

	if _, _, err := mgr.GetLeastLoadedAgent(context.Background(), models.WorkloadDimensionRealtime); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestAcquireLeastLoadedAgentFallsBackToOnlineAgents(t *testing.T) {
	host := newFakeHost()
	host.agents = []string{"a1", "a9"}
	store := &fakeWorkloadStore{acquireAgent: "a9", acquireLoad: 4}
	host.store = store
	mgr := NewWorkloadManager(host)

	id, load, err := mgr.AcquireLeastLoadedAgent(context.Background(), nil, models.WorkloadDimensionDaily)
	if err != nil || id != "a9" || load != 4 {
		t.Fatalf("AcquireLeastLoadedAgent = (%q, %d, %v), want (a9, 4, nil)", id, load, err)
	}
	if len(store.gotAgents) != 2 {
		t.Fatalf("回退后的在线客服 = %v, want 2 个", store.gotAgents)
	}
	if store.gotDimension != models.WorkloadDimensionDaily {
		t.Fatalf("dimension = %v, want %v", store.gotDimension, models.WorkloadDimensionDaily)
	}
}

func TestAcquireLeastLoadedAgentUsesCallerSuppliedAgents(t *testing.T) {
	host := newFakeHost()
	// 注册表里有一个已下线的客服；调用方传入了过滤后的"可接单"列表
	host.agents = []string{"offline-agent"}
	store := &fakeWorkloadStore{acquireAgent: "a3"}
	host.store = store
	mgr := NewWorkloadManager(host)

	id, _, err := mgr.AcquireLeastLoadedAgent(context.Background(), []string{"a3"}, models.WorkloadDimensionRealtime)
	if err != nil || id != "a3" {
		t.Fatalf("AcquireLeastLoadedAgent = (%q, %v), want (a3, nil)", id, err)
	}
	// 调用方给了非空列表就不该回退查注册表
	if len(store.gotAgents) != 1 || store.gotAgents[0] != "a3" {
		t.Fatalf("传给仓储的客服 = %v, want [a3]", store.gotAgents)
	}
}

func TestAcquireLeastLoadedAgentNoOnlineAgentsSkipsStore(t *testing.T) {
	host := newFakeHost()
	host.agents = nil
	store := &fakeWorkloadStore{}
	host.store = store
	mgr := NewWorkloadManager(host)

	id, load, err := mgr.AcquireLeastLoadedAgent(context.Background(), nil, models.WorkloadDimensionRealtime)
	if err != nil || id != "" || load != 0 {
		t.Fatalf("AcquireLeastLoadedAgent = (%q, %d, %v), want 空结果", id, load, err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("仓储被调用 = %v, want 空", store.calls)
	}
}

// ============================================================================
// 域管理器组装
// ============================================================================

func TestManagerExposesWorkload(t *testing.T) {
	host := newFakeHost()
	mgr := NewManager(host)
	first := mgr.Workload()
	if first == nil {
		t.Fatal("Workload() 返回 nil")
	}
	if mgr.Workload() != first {
		t.Fatal("Workload() 每次调用应返回同一实例")
	}
}
