/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:15:30
 * @FilePath: \go-wsc\wsc.go
 * @Description: 根包门面 —— Hub 别名与构造入口
 *
 * 极简门面：仅转发 hub.NewHub，业务方直接 import 本包即可获得 Hub
 * 硬规则：只有本文件允许 type 别名，且只允许 Hub 这一个
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/hub"
)

// Hub WebSocket/SSE 连接管理中心（编排层别名，业务方直接 import 本包即可）
type Hub = hub.Hub

// NewHub 创建新的 Hub
func NewHub(config *wscconfig.WSC) *Hub { return hub.NewHub(config) }
