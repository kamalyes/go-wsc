/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-13 22:55:53
 * @FilePath: \go-wsc\models\client_test.go
 * @Description: Client.MarshalJSON 并发安全与取值正确性回归测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"
)

// TestClientJSONMarshalConcurrentWithMetadataWrite 回归测试：
// json.Marshal 活 *Client 与 SetMetadataValue 并发不得触发
// "fatal error: concurrent map iteration and map write"
// 修复前该用例会 fatal 掉整个测试进程（非 t.Fatal，不可 recover）；修复后应稳定通过
// 建议运行：go test -race -run TestClientJSONMarshalConcurrentWithMetadataWrite
func TestClientJSONMarshalConcurrentWithMetadataWrite(t *testing.T) {
	client := NewClient("c-race", "u-race", UserTypeObserver)
	client.NodeID = "node-1"

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			client.SetMetadataValue("k", i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			if _, err := json.Marshal(client); err != nil {
				t.Errorf("json.Marshal: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// TestClientJSONMarshalConcurrentWithGroupIDAndStatusWrite 扩展并发面：
// MarshalJSON 在读 Metadata 快照的同时，另一 goroutine 并发改 GroupID/Status/VIPLevel，
// 这些字段同样受 mu / 原子镜像保护，不得产生数据竞争或 fatal
func TestClientJSONMarshalConcurrentWithGroupIDAndStatusWrite(t *testing.T) {
	client := NewClient("c-race2", "u-race2", UserTypeCustomer)
	client.NodeID = "node-2"

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			client.SetGroupID("g-1")
			client.SetStatus(UserStatusBusy)
			client.SetVIPLevel(VIPLevelV3)
			client.SetLastHeartbeat(time.Now())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if _, err := json.Marshal(client); err != nil {
				t.Errorf("json.Marshal: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// TestClientJSONMarshalUsesSafeSnapshotAndAtomicReads 验证 MarshalJSON 取值路径：
//   - metadata 取自快照（不直接遍历活 map）
//   - last_heartbeat 取自原子镜像（SetLastHeartbeat 仅更新原子镜像、不更新 time.Time 字段，
//     修复前 marshal 得到的是 NewClient 时的过期值，marshal 输出不随 SetLastHeartbeat 变化）
//   - group_id/status/vip_level 取自加锁/原子读
//   - JSON 字段集合与原 json.Marshal(client) 一致
func TestClientJSONMarshalUsesSafeSnapshotAndAtomicReads(t *testing.T) {
	client := NewClient("c-marshal", "u-marshal", UserTypeCustomer)
	client.NodeID = "node-1"
	client.ClientIP = "1.2.3.4"

	client.SetMetadataValue("platform", "web")
	client.SetGroupID("g-100")
	client.SetStatus(UserStatusBusy)
	client.SetVIPLevel(VIPLevelV3)

	hb := time.Now().Add(-30 * time.Second).Truncate(time.Millisecond)
	client.SetLastHeartbeat(hb)
	client.SetLastSeen(hb)
	client.SetLastPong(hb)

	data, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// 字段集合不变：核心字段都在
	for _, key := range []string{
		"id", "user_id", "node_id", "client_ip", "metadata",
		"group_id", "status", "vip_level",
		"last_heartbeat", "last_seen", "last_pong",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing json field: %s", key)
		}
	}

	// metadata 走快照
	md, _ := m["metadata"].(map[string]interface{})
	if md == nil || md["platform"] != "web" {
		t.Errorf("metadata snapshot mismatch: %v", m["metadata"])
	}
	// group_id 走 GetGroupIDRaw
	if m["group_id"] != "g-100" {
		t.Errorf("group_id mismatch: %v", m["group_id"])
	}
	// status 走原子读
	if m["status"] != string(UserStatusBusy) {
		t.Errorf("status mismatch: %v", m["status"])
	}
	// vip_level 走原子读
	if m["vip_level"] != string(VIPLevelV3) {
		t.Errorf("vip_level mismatch: %v", m["vip_level"])
	}

	// last_heartbeat 走原子读：SetLastHeartbeat 后 marshal 输出应随之变化
	// （修复前直接读 LastHeartbeat 字段，而 SetLastHeartbeat 从不更新它，两次 marshal 完全相同）
	data1, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("json.Marshal #1: %v", err)
	}
	client.SetLastHeartbeat(time.Now().Add(-1 * time.Hour))
	data2, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("json.Marshal #2: %v", err)
	}
	var m1, m2 map[string]interface{}
	_ = json.Unmarshal(data1, &m1)
	_ = json.Unmarshal(data2, &m2)
	if m1["last_heartbeat"] == m2["last_heartbeat"] {
		t.Error("last_heartbeat should reflect atomic SetLastHeartbeat update, but marshal output unchanged")
	}
}

// TestClientJSONMarshalNilMetadataEmptyGroupID 边界：空 metadata 与空 group_id 的序列化稳定性
func TestClientJSONMarshalNilMetadataEmptyGroupID(t *testing.T) {
	client := NewClient("c-nil", "u-nil", UserTypeVisitor)
	client.NodeID = "node-1"

	data, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	// 空 metadata（快照返回 nil）应序列化为 null
	if _, ok := m["metadata"]; !ok {
		t.Error("metadata field should always be present (null when empty)")
	}
	// 空 group_id 因 omitempty 应缺席
	if _, ok := m["group_id"]; ok {
		t.Error("group_id should be omitted when empty")
	}
}

// TestClientJSONMarshalConcurrentWithOtherFields 验证“其它字段”也安全：
// MarshalJSON 持 mu.RLock 贯穿 json.Marshal，与所有 With* 写入器（持 mu.Lock）互斥
// 并发改 Role/Department/Skills/MaxTickets/NodeInfo/ClientType/Namespace/ClientIP/GroupID，
// 同时 json.Marshal，不得产生数据竞争或 fatal
func TestClientJSONMarshalConcurrentWithOtherFields(t *testing.T) {
	client := NewClient("c-other", "u-other", UserTypeAgent)
	client.NodeID = "node-init"
	client.WithRole(UserRoleAgent).
		WithDepartment(DepartmentTechnical).
		WithSkills([]Skill{SkillTechnical}).
		WithMaxTickets(5).
		WithClientType(ClientTypeDesktop).
		WithNamespace("tenantA").
		WithClientIP("10.0.0.1")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			client.WithRole(UserRoleAdmin)
			client.WithDepartment(DepartmentSales)
			client.WithSkills([]Skill{SkillSales, SkillLanguageEN})
			client.WithMaxTickets(i % 10)
			client.WithClientType(ClientTypeMobile)
			client.WithNamespace("tenantB")
			client.WithClientIP("10.0.0.2")
			client.WithNodeInfo("node-x", "10.0.0.9", 8080)
			client.WithGroupID("g-200")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if _, err := json.Marshal(client); err != nil {
				t.Errorf("json.Marshal: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// defaultClient 是 Client 的别名类型，用于剥离 *Client.MarshalJSON 方法，
// 取得“未自定义序列化”时的默认 JSON 输出，作为字段集合的基准
type defaultClient Client

func jsonTopLevelKeys(t *testing.T, data []byte) []string {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestClientJSONMarshalFieldSetUnchanged 验收标准（文档要求）：
// 自定义 MarshalJSON 产出的 JSON 顶层字段集合必须与“默认序列化”完全一致，
// 保证 Redis 在线状态里的 client JSON 字段集合不变（SetClientOnline 往返兼容）
// 仅取值路径变为并发安全，字段集合不增不减
func TestClientJSONMarshalFieldSetUnchanged(t *testing.T) {
	cases := []struct {
		name  string
		setup func(c *Client)
	}{
		{
			name:  "nil metadata + empty group_id",
			setup: func(c *Client) { /* 不写 metadata / group_id */ },
		},
		{
			name: "populated metadata + non-empty group_id",
			setup: func(c *Client) {
				c.SetMetadataValue("platform", "web")
				c.SetMetadataValue("ver", 3)
				c.SetGroupID("g-100")
				c.SetStatus(UserStatusBusy)
				c.SetVIPLevel(VIPLevelV3)
				c.WithRole(UserRoleAgent).WithDepartment(DepartmentTechnical).
					WithSkills([]Skill{SkillTechnical}).WithMaxTickets(7).
					WithClientType(ClientTypeDesktop).WithNamespace("tenantA")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("c-fs", "u-fs", UserTypeCustomer)
			client.NodeID = "node-1"
			client.ClientIP = "1.2.3.4"
			tc.setup(client)

			customData, err := json.Marshal(client)
			if err != nil {
				t.Fatalf("custom json.Marshal: %v", err)
			}
			// 默认序列化（剥离 MarshalJSON）测试单线程，直接读 Metadata 活 map 不会 fatal
			defaultData, err := json.Marshal((*defaultClient)(client))
			if err != nil {
				t.Fatalf("default json.Marshal: %v", err)
			}

			customKeys := jsonTopLevelKeys(t, customData)
			defaultKeys := jsonTopLevelKeys(t, defaultData)

			if !equalStringSlice(customKeys, defaultKeys) {
				t.Errorf("field set changed:\n  default=%v\n  custom =%v", defaultKeys, customKeys)
			}
		})
	}
}
