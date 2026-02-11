/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-10 18:00:00
 * @FilePath: \go-wsc\hub\client_capacity.go
 * @Description: 客户端容量管理 - 根据客户端类型从配置获取 SendChan 缓冲区大小
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"github.com/kamalyes/go-config/pkg/wsc"
)

// 默认客户端 SendChan 缓冲区容量（当配置不存在时使用）
const DefaultClientSendChanCapacity = 256

// getClientCapacity 根据客户端类型获取 SendChan 缓冲区容量
// 从 go-config 的 ClientCapacity 配置中读取，如果配置不存在则使用默认值
func (h *Hub) getClientCapacity(client *Client) int {
	// 1. 参数验证
	if client == nil {
		return DefaultClientSendChanCapacity
	}

	// 2. 获取配置中的客户端容量
	if h.config == nil || h.config.ClientCapacity == nil {
		return DefaultClientSendChanCapacity
	}

	capacity := h.config.ClientCapacity

	// 3. 根据用户类型获取容量
	switch client.UserType {
	case UserTypeAgent:
		return capacity.Agent
	case UserTypeBot:
		return capacity.Bot
	case UserTypeCustomer:
		return capacity.Customer
	case UserTypeObserver:
		return capacity.Observer
	case UserTypeAdmin:
		return capacity.Admin
	case UserTypeVIP:
		return capacity.VIP
	case UserTypeVisitor:
		return capacity.Visitor
	case UserTypeSystem:
		return capacity.System
	default:
		return capacity.Default
	}
}

// initClientSendChan 初始化客户端的 SendChan，使用配置的容量
// 在客户端注册时调用，确保 SendChan 使用正确的缓冲区大小
func (h *Hub) initClientSendChan(client *Client) {
	if client == nil {
		return
	}

	// 如果 SendChan 已经初始化，不再重新初始化
	if client.SendChan != nil {
		return
	}

	// 获取该客户端类型的容量
	capacity := h.getClientCapacity(client)

	// 创建 SendChan
	client.SendChan = make(chan []byte, capacity)

	// 记录日志
	h.logger.DebugKV("初始化客户端 SendChan",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"capacity", capacity,
	)
}

// GetClientCapacityConfig 获取客户端容量配置（用于测试和调试）
func (h *Hub) GetClientCapacityConfig() *wsc.ClientCapacity {
	if h.config != nil && h.config.ClientCapacity != nil {
		return h.config.ClientCapacity
	}

	return wsc.DefaultClientCapacity()
}
