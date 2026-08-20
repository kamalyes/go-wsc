/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 00:00:00
 * @FilePath: \go-wsc\constants\route.go
 * @Description: 路由隔离维度默认值常量
 *
 * 隔离层次：AppID(应用) > Namespace(租户) > GroupID(平台) > UserID
 * 空值在入口层（HTTP 升级、HubMessage 注入、客户端注册）归一化为这些默认值，
 * 下游过滤层无需处理空值，避免"空=全局共享"与"空=默认"两种语义并存
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

// 路由隔离维度默认值（"__" 前缀为系统保留值，业务侧禁止使用）
const (
	DefaultAppID     = "__default_app__" // DefaultAppID 默认应用ID（最上层隔离维度）
	DefaultNamespace = "__default_ns__"  // DefaultNamespace 默认命名空间ID（类似 k8s namespace）
	DefaultGroupID   = "__default_gp__"  // DefaultGroupID 默认群组ID（P2P 消息归一化用）
)
