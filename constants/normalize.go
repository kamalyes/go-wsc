/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\constants\normalize.go
 * @Description: 路由隔离维度空值兜底统一入口
 *
 * 统一收口 AppID/Namespace/GroupID 三维空值归一化逻辑，
 * 各层（读取层 getter / 注册入口 handleRegister / 落库层 repository.Upsert）
 * 统一调用本文件 helper，避免散落 if 兜底导致的不一致与遗漏
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

// NormalizeAppID 归一化 appID：空值兜底为 DefaultAppID
// appID 无广播语义（必填），任何入口的空 appID 都应归一化为默认值
func NormalizeAppID(s string) string {
	if s == "" {
		return DefaultAppID
	}
	return s
}

// NormalizeNamespace 归一化 namespace：空值兜底为 DefaultNamespace
// 注意：广播场景 namespace 故意留空表示全命名空间，调用方需按语义判断是否调用本函数
func NormalizeNamespace(s string) string {
	if s == "" {
		return DefaultNamespace
	}
	return s
}

// NormalizeGroupID 归一化 groupID：空值兜底为 DefaultGroupID（P2P 场景补默认）
// 保证 Redis 队列 key 与 MySQL group_id 维度一致
func NormalizeGroupID(s string) string {
	if s == "" {
		return DefaultGroupID
	}
	return s
}
