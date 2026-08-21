/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 13:02:32
 * @FilePath: \go-wsc\constants\system_group.go
 * @Description: 系统保留组常量（`__` 前缀为系统组，业务组名禁止以此开头）
 *
 * agent 连接时自动加入 __agents__，observer 连接时自动加入 __observers__
 * 本地分片索引（agentShards/observerShards）仍保留做 O(1) 缓存，
 * Redis 系统组用于跨节点共享成员关系与显式广播
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

// 系统保留组常量
const (
	SystemGroupAgents    = "__agents__"    // SystemGroupAgents 客服系统组（每命名空间一个，ns:__agents__）
	SystemGroupObservers = "__observers__" // SystemGroupObservers 观察者系统组（全局 namespace="" 或命名空间级）
	SystemGroupPrefix    = "__"            // SystemGroupPrefix 系统组前缀，业务组名禁止以此开头
)
