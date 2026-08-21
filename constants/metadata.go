/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 13:56:32
 * @FilePath: \go-wsc\constants\metadata.go
 * @Description: gRPC metadata 键名常量（跨节点传播路由元数据）
 *
 * groupIDs 以逗号分隔存入单个 metadata 值，避免 repeated header 开销
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

// gRPC metadata 键名（跨节点传播路由元数据）
const (
	MetadataKeyAppID     = "x-routing-app-id"    // 应用ID（最上层隔离维度）
	MetadataKeyNamespace = "x-routing-namespace" // 命名空间
	MetadataKeyGroupIDs  = "x-routing-group-ids" // 群组ID列表（逗号分隔）
)
