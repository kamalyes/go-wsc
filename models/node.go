/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 16:02:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 16:02:00
 * @FilePath: \go-wsc\models\node.go
 * @Description: 集群节点信息
 *
 * 本节点注册信息（集群注册与路由用）；跨节点统计聚合见 contract.go 的 NodeStats。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import "time"

// NodeInfo 节点信息
type NodeInfo struct {
	ID          string     `json:"id"`
	IPAddress   string     `json:"ip_address"`
	Port        int        `json:"port"`
	Status      NodeStatus `json:"status"`
	LoadScore   float64    `json:"load_score"`
	LastSeen    time.Time  `json:"last_seen"`
	Connections int64      `json:"connections"`
}
