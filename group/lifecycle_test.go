/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 15:34:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 15:34:00
 * @FilePath: \go-wsc\group\lifecycle_test.go
 * @Description: 群组生命周期单元测试
 *
 * 假存储只覆盖本域真正调用的 GroupStore 方法，其余保留 nil 嵌入接口
 * （被调用即 panic，暴露未声明的依赖）。回调通过 fakeHost 的字段注入，
 * TrySubmitCallback 同步执行以便断言回调内容。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 假群组存储
// ============================================================================

type fakeGroupStore struct {
	spi.GroupStore

	mu sync.Mutex

	groups     map[string]*models.Group // key: appID|ns|gid
	members    map[string]map[string]bool
	userGroups map[string][]string

	calls []string

	// 行为注入
	getGroupErr    error
	createGroupErr error
	addMembersErr  error
	isMemberErr    error
}

func newFakeGroupStore() *fakeGroupStore {
	return &fakeGroupStore{
		groups:     make(map[string]*models.Group),
		members:    make(map[string]map[string]bool),
		userGroups: make(map[string][]string),
	}
}

func gkey(appID, ns, gid string) string { return appID + "|" + ns + "|" + gid }

func (s *fakeGroupStore) record(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
}

func (s *fakeGroupStore) called(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (s *fakeGroupStore) GetGroup(_ context.Context, appID, ns, gid string) (*models.Group, error) {
	s.record("GetGroup")
	if s.getGroupErr != nil {
		return nil, s.getGroupErr
	}
	g, ok := s.groups[gkey(appID, ns, gid)]
	if !ok {
		return nil, models.ErrGroupNotFound
	}
	return g, nil
}

func (s *fakeGroupStore) CreateGroup(_ context.Context, g *models.Group) error {
	s.record("CreateGroup")
	if s.createGroupErr != nil {
		return s.createGroupErr
	}
	s.groups[gkey(g.AppID, g.Namespace, g.GroupID)] = g
	return nil
}

func (s *fakeGroupStore) DisbandGroup(_ context.Context, appID, ns, gid string) error {
	s.record("DisbandGroup")
	delete(s.groups, gkey(appID, ns, gid))
	delete(s.members, gkey(appID, ns, gid))
	return nil
}

func (s *fakeGroupStore) AddMembers(_ context.Context, appID, ns, gid string, userIDs []string) error {
	s.record("AddMembers")
	if s.addMembersErr != nil {
		return s.addMembersErr
	}
	k := gkey(appID, ns, gid)
	if s.members[k] == nil {
		s.members[k] = make(map[string]bool)
	}
	for _, u := range userIDs {
		s.members[k][u] = true
	}
	return nil
}

func (s *fakeGroupStore) RemoveMembers(_ context.Context, appID, ns, gid string, userIDs []string) error {
	s.record("RemoveMembers")
	k := gkey(appID, ns, gid)
	for _, u := range userIDs {
		delete(s.members[k], u)
	}
	return nil
}

func (s *fakeGroupStore) GetMembers(_ context.Context, appID, ns, gid string) ([]string, error) {
	s.record("GetMembers")
	k := gkey(appID, ns, gid)
	out := make([]string, 0, len(s.members[k]))
	for u := range s.members[k] {
		out = append(out, u)
	}
	return out, nil
}

func (s *fakeGroupStore) IsMember(_ context.Context, appID, ns, gid, userID string) (bool, error) {
	s.record("IsMember")
	if s.isMemberErr != nil {
		return false, s.isMemberErr
	}
	return s.members[gkey(appID, ns, gid)][userID], nil
}

func (s *fakeGroupStore) GetMemberCount(_ context.Context, appID, ns, gid string) (int64, error) {
	s.record("GetMemberCount")
	return int64(len(s.members[gkey(appID, ns, gid)])), nil
}

func (s *fakeGroupStore) GetUserGroups(_ context.Context, appID, ns, userID string) ([]string, error) {
	s.record("GetUserGroups")
	return s.userGroups[appID+"|"+ns+"|"+userID], nil
}

func (s *fakeGroupStore) GetNamespaceGroups(_ context.Context, appID, ns string) ([]string, error) {
	s.record("GetNamespaceGroups")
	var out []string
	for k := range s.groups {
		g := s.groups[k]
		if g.AppID == appID && g.Namespace == ns {
			out = append(out, g.GroupID)
		}
	}
	return out, nil
}

func (s *fakeGroupStore) EnsureSystemGroup(_ context.Context, appID, ns, gid string) error {
	s.record("EnsureSystemGroup")
	k := gkey(appID, ns, gid)
	if _, ok := s.groups[k]; !ok {
		s.groups[k] = &models.Group{AppID: appID, Namespace: ns, GroupID: gid}
	}
	return nil
}

// lifecycleFixture 在 fakeHost 之上装配群组存储与同步执行的回调
type lifecycleFixture struct {
	*fakeHost
	store  *fakeGroupStore
	events *callbackRecorder
}

// callbackRecorder 记录回调调用
type callbackRecorder struct {
	mu      sync.Mutex
	disband []string
	join    []string
	leave   []string
}

func (c *callbackRecorder) record(dst *[]string, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*dst = append(*dst, v)
}

func (c *callbackRecorder) counts() (int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.disband), len(c.join), len(c.leave)
}

func newLifecycleFixture() *lifecycleFixture {
	return &lifecycleFixture{
		fakeHost: newFakeHost(),
		store:    newFakeGroupStore(),
		events:   &callbackRecorder{},
	}
}

func (f *lifecycleFixture) GetGroupStore() spi.GroupStore { return f.store }

// TrySubmitCallback 同步执行：异步会引入断言时序不确定性
func (f *lifecycleFixture) TrySubmitCallback(task func()) bool {
	task()
	return true
}

func (f *lifecycleFixture) GetGroupDisbandCallback() func(context.Context, string, string) {
	return func(_ context.Context, ns, gid string) {
		f.events.record(&f.events.disband, ns+"/"+gid)
	}
}

func (f *lifecycleFixture) GetGroupMemberJoinCallback() func(context.Context, string, string, []string) {
	return func(_ context.Context, ns, gid string, userIDs []string) {
		f.events.record(&f.events.join, ns+"/"+gid+":"+joinIDs(userIDs))
	}
}

func (f *lifecycleFixture) GetGroupMemberLeaveCallback() func(context.Context, string, string, []string) {
	return func(_ context.Context, ns, gid string, userIDs []string) {
		f.events.record(&f.events.leave, ns+"/"+gid+":"+joinIDs(userIDs))
	}
}

func joinIDs(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += id
	}
	return out
}

// groupCtx 构造带 appID/namespace/单群的路由上下文
func groupCtx(namespace, groupID string) context.Context {
	r := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(namespace)
	if groupID != "" {
		r = r.WithGroup(groupID)
	}
	return r.Inject(context.Background())
}

// ============================================================================
// 仓储未注入：必须报错
// ============================================================================

func TestLifecycleErrorsWithoutStore(t *testing.T) {
	mgr := NewLifecycleManager(newFakeHost()) // store == nil
	ctx := groupCtx("ns1", "g1")

	if _, err := mgr.GetGroup(ctx); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("GetGroup err = %v, want ErrGroupRepoNotSet", err)
	}
	if err := mgr.DisbandGroup(ctx); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("DisbandGroup err = %v, want ErrGroupRepoNotSet", err)
	}
	if err := mgr.AddGroupMembers(ctx, []string{"u1"}); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("AddGroupMembers err = %v, want ErrGroupRepoNotSet", err)
	}
	if err := mgr.RemoveGroupMembers(ctx, []string{"u1"}); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("RemoveGroupMembers err = %v, want ErrGroupRepoNotSet", err)
	}
	if _, err := mgr.GetGroupMembers(ctx); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("GetGroupMembers err = %v, want ErrGroupRepoNotSet", err)
	}
	if _, err := mgr.GetUserGroups(ctx, "u1"); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("GetUserGroups err = %v, want ErrGroupRepoNotSet", err)
	}
	if _, err := mgr.IsGroupMember(ctx, "u1"); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("IsGroupMember err = %v, want ErrGroupRepoNotSet", err)
	}
	if _, err := mgr.GetGroupMemberCount(ctx); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("GetGroupMemberCount err = %v, want ErrGroupRepoNotSet", err)
	}
	if _, err := mgr.GetNamespaceGroups(ctx); !errors.Is(err, models.ErrGroupRepoNotSet) {
		t.Errorf("GetNamespaceGroups err = %v, want ErrGroupRepoNotSet", err)
	}
}

func TestAddRemoveWithEmptyUserIDsSkipsStore(t *testing.T) {
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)
	ctx := groupCtx("ns1", "g1")

	if err := mgr.AddGroupMembers(ctx, nil); err != nil {
		t.Fatalf("空 userIDs 添加应返回 nil，实际 %v", err)
	}
	if err := mgr.RemoveGroupMembers(ctx, nil); err != nil {
		t.Fatalf("空 userIDs 移除应返回 nil，实际 %v", err)
	}
	if len(f.store.calls) != 0 {
		t.Fatalf("空 userIDs 不应触碰存储，实际调用 %v", f.store.calls)
	}
}

// ============================================================================
// 自动建群与人数上限
// ============================================================================

func TestAddGroupMembersAutoCreatesGroup(t *testing.T) {
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)
	ctx := groupCtx("ns1", "new-group")

	if err := mgr.AddGroupMembers(ctx, []string{"u1", "u2"}); err != nil {
		t.Fatalf("AddGroupMembers: %v", err)
	}
	if !f.store.called("CreateGroup") {
		t.Fatal("群组不存在时应自动创建")
	}
	if got := len(f.store.members[gkey(constants.DefaultAppID, "ns1", "new-group")]); got != 2 {
		t.Fatalf("成员数 = %d, want 2", got)
	}
}

func TestAddGroupMembersRejectsOverMaxMembers(t *testing.T) {
	f := newLifecycleFixture()
	f.store.groups[gkey(constants.DefaultAppID, "ns1", "full")] = &models.Group{
		AppID: constants.DefaultAppID, Namespace: "ns1", GroupID: "full", MaxMembers: 1,
	}
	f.store.members[gkey(constants.DefaultAppID, "ns1", "full")] = map[string]bool{"existing": true}
	mgr := NewLifecycleManager(f)

	err := mgr.AddGroupMembers(groupCtx("ns1", "full"), []string{"u1"})
	if !errors.Is(err, models.ErrGroupFull) {
		t.Fatalf("err = %v, want ErrGroupFull", err)
	}
}

func TestAddGroupMembersReconnectNotCountedTwice(t *testing.T) {
	// 重连用户已在群里：不应因重复计数被误判超限
	f := newLifecycleFixture()
	f.store.groups[gkey(constants.DefaultAppID, "ns1", "g")] = &models.Group{
		AppID: constants.DefaultAppID, Namespace: "ns1", GroupID: "g", MaxMembers: 2,
	}
	f.store.members[gkey(constants.DefaultAppID, "ns1", "g")] = map[string]bool{"u1": true}
	mgr := NewLifecycleManager(f)

	if err := mgr.AddGroupMembers(groupCtx("ns1", "g"), []string{"u1"}); err != nil {
		t.Fatalf("重连用户重复入群不应超限，实际 %v", err)
	}
}

// ============================================================================
// 回调触发
// ============================================================================

func TestDisbandGroupTriggersCallback(t *testing.T) {
	f := newLifecycleFixture()
	f.store.groups[gkey(constants.DefaultAppID, "ns1", "g1")] = &models.Group{
		AppID: constants.DefaultAppID, Namespace: "ns1", GroupID: "g1",
	}
	mgr := NewLifecycleManager(f)

	if err := mgr.DisbandGroup(groupCtx("ns1", "g1")); err != nil {
		t.Fatalf("DisbandGroup: %v", err)
	}
	d, _, _ := f.events.counts()
	if d != 1 {
		t.Fatalf("解散回调触发 %d 次, want 1", d)
	}
}

func TestRemoveGroupMembersTriggersLeaveCallback(t *testing.T) {
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	if err := mgr.RemoveGroupMembers(groupCtx("ns1", "g1"), []string{"u1", "u2"}); err != nil {
		t.Fatalf("RemoveGroupMembers: %v", err)
	}
	_, _, l := f.events.counts()
	if l != 1 {
		t.Fatalf("离开回调触发 %d 次, want 1", l)
	}
	if f.events.leave[0] != "ns1/g1:u1,u2" {
		t.Fatalf("回调载荷 = %q, want ns1/g1:u1,u2", f.events.leave[0])
	}
}

func TestManualAddGroupMembersDoesNotTriggerJoinCallback(t *testing.T) {
	// 手动加群不触发回调（仅 register 自动装配触发），这是既有契约
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	if err := mgr.AddGroupMembers(groupCtx("ns1", "g1"), []string{"u1"}); err != nil {
		t.Fatalf("AddGroupMembers: %v", err)
	}
	_, j, _ := f.events.counts()
	if j != 0 {
		t.Fatalf("手动 AddGroupMembers 触发了 %d 次加入回调, want 0", j)
	}
}

func TestCallbackNilSafe(t *testing.T) {
	// 业务方未注册任何回调时不得 panic
	f := newLifecycleFixture()
	host := &noCallbackHost{fakeHost: f.fakeHost, GetGroupStoreFn: func() spi.GroupStore { return f.store }}
	mgr := NewLifecycleManager(host)

	if err := mgr.AddGroupMembers(groupCtx("ns1", "g1"), []string{"u1"}); err != nil {
		t.Fatalf("AddGroupMembers: %v", err)
	}
	if err := mgr.RemoveGroupMembers(groupCtx("ns1", "g1"), []string{"u1"}); err != nil {
		t.Fatalf("RemoveGroupMembers: %v", err)
	}
	if err := mgr.DisbandGroup(groupCtx("ns1", "g1")); err != nil {
		t.Fatalf("DisbandGroup: %v", err)
	}
}

// noCallbackHost 所有群组回调 getter 返回 nil
type noCallbackHost struct {
	*fakeHost
	GetGroupStoreFn func() spi.GroupStore
}

func (h *noCallbackHost) GetGroupStore() spi.GroupStore      { return h.GetGroupStoreFn() }
func (h *noCallbackHost) TrySubmitCallback(task func()) bool { task(); return true }
func (h *noCallbackHost) GetGroupDisbandCallback() func(context.Context, string, string) {
	return nil
}
func (h *noCallbackHost) GetGroupMemberJoinCallback() func(context.Context, string, string, []string) {
	return nil
}
func (h *noCallbackHost) GetGroupMemberLeaveCallback() func(context.Context, string, string, []string) {
	return nil
}

// ============================================================================
// 系统组自动装配
// ============================================================================

func TestJoinSystemGroupsOnConnect(t *testing.T) {
	cases := []struct {
		name   string
		ut     models.UserType
		wantID string
	}{
		{"agent 入 __agents__", models.UserTypeAgent, constants.SystemGroupAgents},
		{"bot 入 __agents__", models.UserTypeBot, constants.SystemGroupAgents},
		{"observer 入 __observers__", models.UserTypeObserver, constants.SystemGroupObservers},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleFixture()

			mgr := NewLifecycleManager(f)

			client := models.NewClient("c1", "u1", tc.ut)
			client.AppID = constants.DefaultAppID
			client.Namespace = "ns1"
			mgr.JoinSystemGroupsOnConnect(context.Background(), client)

			k := gkey(constants.DefaultAppID, "ns1", tc.wantID)
			if !f.store.members[k]["u1"] {
				t.Fatalf("用户未加入系统组 %s，成员表 = %v", tc.wantID, f.store.members)
			}
		})
	}
}

func TestJoinSystemGroupsOnConnectSkipsNormalUser(t *testing.T) {
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeCustomer)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"
	mgr.JoinSystemGroupsOnConnect(context.Background(), client)

	if len(f.store.calls) != 0 {
		t.Fatalf("普通用户不应入系统组，实际调用 %v", f.store.calls)
	}
}

func TestJoinSystemGroupsOnConnectNilCtxDoesNotPanic(t *testing.T) {
	// registry 传入的 ctx 来自 client.Context，未设置时为 nil
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeAgent)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"
	mgr.JoinSystemGroupsOnConnect(nil, client)

	if f.store.calls == nil {
		t.Fatal("nil ctx 应兜底为 Background 并正常入组")
	}
}

func TestGlobalObserverUsesRawNamespace(t *testing.T) {
	// 全局观察者 namespace=""：不得被归一化成 DefaultNamespace，
	// 否则与命名空间级观察者的系统组 key 混同
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeObserver)
	client.AppID = constants.DefaultAppID
	client.Namespace = "" // 全局观察者
	mgr.JoinSystemGroupsOnConnect(context.Background(), client)

	k := gkey(constants.DefaultAppID, "", constants.SystemGroupObservers)
	if !f.store.members[k]["u1"] {
		t.Fatalf("全局观察者应入 namespace 为空原值的系统组，成员表 = %v", f.store.members)
	}
	if f.store.members[gkey(constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupObservers)]["u1"] {
		t.Fatal("全局观察者的系统组 key 不应被归一化到 DefaultNamespace")
	}
}

func TestJoinMemberGroupOnConnectSkipsObserver(t *testing.T) {
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeObserver)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"
	client.GroupID = "biz-group"
	mgr.JoinMemberGroupOnConnect(context.Background(), client)

	if len(f.store.calls) != 0 {
		t.Fatalf("观察者不应作为成员入群，实际调用 %v", f.store.calls)
	}
}

func TestJoinMemberGroupOnConnectSystemGroupPath(t *testing.T) {
	// 系统保留组名走 EnsureSystemGroup（CreateGroup 会拒绝 __ 前缀）
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeCustomer)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"
	client.GroupID = constants.DefaultGroupID
	mgr.JoinMemberGroupOnConnect(context.Background(), client)

	if !f.store.called("EnsureSystemGroup") {
		t.Fatal("系统保留组名应走 EnsureSystemGroup 路径")
	}
	if f.store.called("CreateGroup") {
		t.Fatal("系统保留组名不应走 CreateGroup（会拒绝 __ 前缀）")
	}
}

func TestLeaveSystemGroupsKeepsIdentityWhenOtherConnectionsExist(t *testing.T) {
	// 同一用户还有其他同信封在线连接：保留系统组成员身份，
	// 否则多端场景下其他端收不到系统组广播
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeAgent)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"
	f.reg.AddClient(client) // 注册表里仍有该用户 → 视为还有其他端在线

	k := gkey(constants.DefaultAppID, "ns1", constants.SystemGroupAgents)
	f.store.members[k] = map[string]bool{"u1": true}

	mgr.LeaveSystemGroupsOnDisconnect(context.Background(), client)

	if !f.store.members[k]["u1"] {
		t.Fatal("仍有其他同信封在线连接时不应移除系统组成员身份")
	}
}

func TestLeaveSystemGroupsRemovesWhenLastConnection(t *testing.T) {
	// 注册表已无该用户（注册表先移除连接再调用本方法）→ 应移除系统组成员身份
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeAgent)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"

	k := gkey(constants.DefaultAppID, "ns1", constants.SystemGroupAgents)
	f.store.members[k] = map[string]bool{"u1": true}

	mgr.LeaveSystemGroupsOnDisconnect(context.Background(), client)

	if f.store.members[k]["u1"] {
		t.Fatal("无其他同信封在线连接时应移除系统组成员身份")
	}
}

func TestLeaveSystemGroupsKeepsIdentityAcrossDifferentEnvelope(t *testing.T) {
	// 同 appID 但不同 namespace 的在线连接不算「同信封」，
	// 各应用/命名空间各自管理系统组成员生命周期
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeAgent)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"

	other := models.NewClient("c2", "u1", models.UserTypeAgent)
	other.AppID = constants.DefaultAppID
	other.Namespace = "ns2" // 不同命名空间
	f.reg.AddClient(other)

	k := gkey(constants.DefaultAppID, "ns1", constants.SystemGroupAgents)
	f.store.members[k] = map[string]bool{"u1": true}

	mgr.LeaveSystemGroupsOnDisconnect(context.Background(), client)

	if f.store.members[k]["u1"] {
		t.Fatal("跨命名空间的连接不算同信封，应移除 ns1 的系统组成员身份")
	}
}

func TestLeaveSystemGroupsSkipsNormalUser(t *testing.T) {
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)

	client := models.NewClient("c1", "u1", models.UserTypeCustomer)
	client.AppID = constants.DefaultAppID
	client.Namespace = "ns1"
	mgr.LeaveSystemGroupsOnDisconnect(context.Background(), client)

	if len(f.store.calls) != 0 {
		t.Fatalf("普通用户离开不应触碰存储，实际调用 %v", f.store.calls)
	}
}

// ============================================================================
// 查询透传
// ============================================================================

func TestGroupQueriesDelegateToStore(t *testing.T) {
	f := newLifecycleFixture()
	mgr := NewLifecycleManager(f)
	ctx := groupCtx("ns1", "g1")

	if err := mgr.AddGroupMembers(ctx, []string{"u1"}); err != nil {
		t.Fatalf("AddGroupMembers: %v", err)
	}
	ok, err := mgr.IsGroupMember(ctx, "u1")
	if err != nil || !ok {
		t.Fatalf("IsGroupMember = (%v, %v), want (true, nil)", ok, err)
	}
	n, err := mgr.GetGroupMemberCount(ctx)
	if err != nil || n != 1 {
		t.Fatalf("GetGroupMemberCount = (%d, %v), want (1, nil)", n, err)
	}
	members, err := mgr.GetGroupMembers(ctx)
	if err != nil || len(members) != 1 {
		t.Fatalf("GetGroupMembers = (%v, %v), want 1 个成员", members, err)
	}
	if _, err := mgr.GetGroup(ctx); err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
}

func TestManagerExposesLifecycle(t *testing.T) {
	mgr := NewManager(newLifecycleFixture())
	first := mgr.Lifecycle()
	if first == nil {
		t.Fatal("Lifecycle() 返回 nil")
	}
	if mgr.Lifecycle() != first {
		t.Fatal("Lifecycle() 每次调用应返回同一实例")
	}
}
