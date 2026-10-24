/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 10:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 10:05:00
 * @FilePath: \go-wsc\messaging\replay_gate.go
 * @Description: 首连离线回放门闩（重连场景的投递顺序闭环）
 *
 * 问题背景：首连触发的 PushOfflineMessages 异步执行（workerPool 回调池），
 * 期间新到的实时消息直接进 sendChan → 用户先收到新消息、后收到更早的
 * 离线消息（乱序）；drain 队列还可能把重连瞬间被误转离线的消息重复推出
 *
 * 解法：首连时 begin 门闩 → 该用户的实时投递被暂存 → 离线回放完成后
 * end 按暂存顺序补投 → 开闸。每用户投递顺序 = 消息真实时序
 *
 * 边界保护：
 *   - 每用户暂存上限 maxReplayHoldout：超出后直接投递不再暂存（宁可极小
 *     概率乱序，也不无限堆积内存）
 *   - end 由 defer 保证必达：回放 panic 也会开闸，闸门不会永久卡死
 *   - 单机模式无离线处理器时不启用门闩（PushOfflineMessages nil-safe 跳过）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package messaging

import (
	"sync"
	"sync/atomic"
)

// maxReplayHoldout 单用户回放期间最大暂存投递数（超出直接投递，防内存堆积）
const maxReplayHoldout = 1024

// replayGate 首连离线回放期间的实时投递门闩（每用户粒度）
//
// 热路径免锁：live 原子计数在 begin/end 临界区内发布
// hold 快路径 Load==0 即闸门全开（绝大多数时刻）零锁直投
// 10w 级成员扇出全程无 RLock 竞争，仅回放进行中才进入锁路径
type replayGate struct {
	mu      sync.RWMutex
	pending map[string][]func()
	live    atomic.Int64 // 回放中用户数（0 = 无任何用户处于回放，闸门全开）
}

// NewReplayGate 创建门闩（编排层构造 messaging.Manager 时经 WithReplayGate 注入）
func NewReplayGate() *replayGate {
	return &replayGate{pending: make(map[string][]func())}
}

// begin 标记用户进入离线回放中（幂等：重复 begin 保持首个回放周期）
func (g *replayGate) begin(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.pending[userID]; !ok {
		g.pending[userID] = nil
		// 同临界区发布：hold 快路径读到 0 时 pending 必然为空（与锁路径互斥保证可见性）
		g.live.Add(1)
	}
}

// hold 投递动作过闸：回放中 → 暂存；已开闸 → 立即执行
// 返回是否暂存（调用方可用于统计观测）
func (g *replayGate) hold(userID string, deliver func()) bool {
	// 快路径：无任何用户回放中，直接投递（原子读一次，无锁无 map 探测）
	if g.live.Load() == 0 {
		deliver()
		return false
	}

	g.mu.RLock()
	_, replaying := g.pending[userID]
	g.mu.RUnlock()
	if !replaying {
		deliver()
		return false
	}

	g.mu.Lock()
	// 双检：进入写锁前回放可能刚好结束（结束即直接投递）
	queue, still := g.pending[userID]
	held := still && len(queue) < maxReplayHoldout
	if held {
		g.pending[userID] = append(queue, deliver)
	}
	g.mu.Unlock()
	if !held {
		// 回放已结束或暂存超限（超限保护：直接投递，防内存堆积）
		deliver()
	}
	return held
}

// holding 用户是否处于回放中（扇出热路径预检用）
//
// 快路径 live==0 零锁直判 false——调用方据此走直接投递（零闭包分配），
// 仅回放进行中才进入 RLock 探测；与 hold 的锁路径语义一致
func (g *replayGate) holding(userID string) bool {
	if g.live.Load() == 0 {
		return false
	}
	g.mu.RLock()
	_, ok := g.pending[userID]
	g.mu.RUnlock()
	return ok
}

// end 结束回放：按暂存顺序补投全部实时投递后开闸（幂等：未 begin 时无操作）
func (g *replayGate) end(userID string) {
	g.mu.Lock()
	queue, ok := g.pending[userID]
	if ok {
		delete(g.pending, userID)
		g.live.Add(-1)
	}
	g.mu.Unlock()
	if !ok {
		return
	}
	for _, deliver := range queue {
		deliver()
	}
}

// TryBeginUserReplay 尝试标记用户进入首连离线回放，返回是否获得回放权
// registry 首连分支调用：速断速连窗口同一用户可叠加多次首连触发（上线→回落→再上线）
// 并发回放会重复 drain 同一队列（离线消息重复投递）、且首个回放的 end 会提前开闸（乱序窗口重现）；
// inflight 回放刚 drain 过队列，重复触发放弃回放权是安全的（期间新转离线的消息由下次上线兜底）
func (m *Manager) TryBeginUserReplay(userID string) bool {
	if _, inflight := m.replayInflight.LoadOrStore(userID, struct{}{}); inflight {
		return false
	}
	if m.replayGate != nil {
		m.replayGate.begin(userID)
	}
	return true
}

// EndUserReplay 结束回放并按序补投暂存的实时投递（registry 任务闭包 defer 调用；
// 放弃回放权的触发方不得调用，否则会误开 inflight 回放的门闩）
func (m *Manager) EndUserReplay(userID string) {
	m.replayInflight.Delete(userID)
	if m.replayGate != nil {
		m.replayGate.end(userID)
	}
}

// HoldUserDelivery 用户级投递过闸（回放中暂存、开闸后立即执行）
func (m *Manager) HoldUserDelivery(userID string, deliver func()) {
	if m.replayGate == nil {
		deliver()
		return
	}
	m.replayGate.hold(userID, deliver)
}

// holdingUserDelivery 用户是否处于首连回放中（扇出热路径预检）
//
// 未注入门闩或闸门全开（live==0，绝大多数时刻）恒 false 且零锁
// 调用方据此直接投递零闭包分配，仅真实回放中才构造闭包走暂存
func (m *Manager) holdingUserDelivery(userID string) bool {
	return m.replayGate != nil && m.replayGate.holding(userID)
}

// WithReplayGate 注入首连回放门闩（未注入时投递直通，行为与旧版一致）
func (m *Manager) WithReplayGate(g *replayGate) *Manager {
	m.replayGate = g
	return m
}
