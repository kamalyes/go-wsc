/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:12:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 19:12:00
 * @FilePath: \go-wsc\hub\node_query_flight.go
 * @Description: 用户节点查询 in-flight 合并器 — 同 key 并发查询共享单次回源
 *
 * 分布式热路径场景：热点用户被多发送方并发命中（群聊刷屏、直播间弹幕、
 * 踢人分发与消息路由同用户并发），每次 P2P 投递都要一次用户节点查询
 * Redis 往返判定在线与路由目标。本组件将"进行中"的同 key 查询合并为单次回源，
 * N 个并发调用共享 1 次 RTT
 *
 * 与 TTL 缓存的本质区别（不引入 routerCache 负缓存的历史风险）：
 *   - 无 TTL、无结果缓存——仅合并正在执行的查询，等价于"最后一次直查"
 *   - 不存在"缓存持续返回过期空列表 → 跨节点投递丢失"的陈旧窗口
 *   - 回源 panic 向所有等待者以错误形式感知，当前调用方按原语义继续 panic
 *
 * 共享结果契约：Do 返回的切片在多个等待者间共享，调用方只读
 * （现有调用方仅遍历/过滤构建新切片，无原地修改）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"fmt"
	"sync"
)

// nodeQueryFlight 用户节点查询 in-flight 合并器
// 零值可用（map 惰性初始化，适配测试中 &Hub{} 直接构造）
type nodeQueryFlight struct {
	mu       sync.Mutex
	inflight map[string]*nodeQueryCall
}

// nodeQueryCall 单次进行中的查询（所有等待者共享同一份结果）
type nodeQueryCall struct {
	wg  sync.WaitGroup
	val []string
	err error
}

// Do 执行 key 对应的查询 fn：同 key 已有进行中的查询时等待并共享其结果，
// 否则由当前调用方执行 fn（fn 内的回源逻辑不变，仅外层合并）
//
// fn 的 ctx 为首个发起者的 ctx——同 key 意味着 appID+namespace+userID 三元组一致，
// 信封过滤语义对所有等待者等价
func (f *nodeQueryFlight) Do(key string, fn func() ([]string, error)) ([]string, error) {
	f.mu.Lock()
	if f.inflight == nil {
		f.inflight = make(map[string]*nodeQueryCall)
	}
	if call, ok := f.inflight[key]; ok {
		f.mu.Unlock()
		call.wg.Wait()
		return call.val, call.err
	}
	call := new(nodeQueryCall)
	call.wg.Add(1)
	f.inflight[key] = call
	f.mu.Unlock()

	// fn panic 时完成等待者（以错误形式感知）后向当前调用方继续传播，
	// 与不经合并器直接调用的行为一致（上游 goroutine 边界已有 panic 防护）；
	// 必须先清理 inflight 再 Done，否则该 key 永久残留导致后续查询共享陈旧 panic 错误
	defer func() {
		if r := recover(); r != nil {
			call.err = fmt.Errorf("查询用户节点 panic: %v", r)
			f.mu.Lock()
			delete(f.inflight, key)
			f.mu.Unlock()
			call.wg.Done()
			panic(r)
		}
	}()

	call.val, call.err = fn()
	f.mu.Lock()
	delete(f.inflight, key)
	f.mu.Unlock()
	call.wg.Done()
	return call.val, call.err
}
