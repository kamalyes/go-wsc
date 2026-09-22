/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:36:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:36:00
 * @FilePath: \go-wsc\group\manager.go
 * @Description: 群组域管理器 —— 聚合群组/成员/VIP/观察者/负载
 *
 * 域内按子能力拆分管理器，共用一个 Host 端口。Hub 只持有 Manager，
 * 通过 Workload() / Observer() 等访问器取子管理器。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

// Manager 群组域管理器
type Manager struct {
	host      Host
	workload  *WorkloadManager
	vip       *VIPManager
	observer  *ObserverManager
	lifecycle *LifecycleManager
}

// NewManager 创建群组域管理器
func NewManager(host Host) *Manager {
	return &Manager{
		host:      host,
		workload:  NewWorkloadManager(host),
		vip:       NewVIPManager(host),
		observer:  NewObserverManager(host),
		lifecycle: NewLifecycleManager(host),
	}
}

// Workload 客服负载子管理器
func (m *Manager) Workload() *WorkloadManager {
	return m.workload
}

// VIP 会员分级发送子管理器
func (m *Manager) VIP() *VIPManager {
	return m.vip
}

// Observer 观察者查询与统计子管理器
func (m *Manager) Observer() *ObserverManager {
	return m.observer
}

// Lifecycle 群组生命周期子管理器（CRUD、成员管理、系统组自动装配）
func (m *Manager) Lifecycle() *LifecycleManager {
	return m.lifecycle
}
