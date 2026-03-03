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
	"sync"

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

// initChannelPools 初始化多级 channel 对象池
// 从配置中获取所有容量值，为每个容量创建对象池
func (h *Hub) initChannelPools() {
	h.chanPools = make(map[int]*sync.Pool)

	// 从配置中获取所有容量值
	capacities := h.getUniqueCapacities()

	for _, capacity := range capacities {
		cap := capacity // 捕获循环变量
		h.chanPools[cap] = &sync.Pool{
			New: func() any {
				return make(chan []byte, cap)
			},
		}
	}

	h.logger.InfoKV("多级 channel 对象池已初始化",
		"capacities", capacities,
	)
}

// getUniqueCapacities 从配置中获取所有唯一的容量值
func (h *Hub) getUniqueCapacities() []int {
	capacityMap := make(map[int]struct{})

	// 获取配置，如果不存在则使用默认配置
	capacity := h.config.ClientCapacity
	if capacity == nil {
		capacity = h.GetClientCapacityConfig()
	}

	// 收集所有容量值
	capacityMap[capacity.Agent] = struct{}{}
	capacityMap[capacity.Bot] = struct{}{}
	capacityMap[capacity.Customer] = struct{}{}
	capacityMap[capacity.Observer] = struct{}{}
	capacityMap[capacity.Admin] = struct{}{}
	capacityMap[capacity.VIP] = struct{}{}
	capacityMap[capacity.Visitor] = struct{}{}
	capacityMap[capacity.System] = struct{}{}
	capacityMap[capacity.Default] = struct{}{}

	// 转换为切片
	capacities := make([]int, 0, len(capacityMap))
	for cap := range capacityMap {
		capacities = append(capacities, cap)
	}

	return capacities
}

// getChan 从对象池获取指定容量的 channel
// 如果对象池中没有该容量的池，则创建新 channel
func (h *Hub) getChan(capacity int) chan []byte {
	// 尝试从对应容量的对象池获取
	if pool, exists := h.chanPools[capacity]; exists {
		if ch := pool.Get(); ch != nil {
			return ch.(chan []byte)
		}
	}

	// 对象池为空或不存在该容量的池，创建新 channel
	return make(chan []byte, capacity)
}

// releaseChan 释放 channel 回对象池
func (h *Hub) releaseChan(ch chan []byte, capacity int) {
	if ch == nil {
		return
	}

	// 清空 channel 中的数据
	for len(ch) > 0 {
		<-ch
	}

	// 如果存在该容量的对象池，放回对象池
	if pool, exists := h.chanPools[capacity]; exists {
		pool.Put(ch)
	}
}

// initClientSendChan 初始化客户端的 SendChan，优先从对象池获取
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

	// 从对应容量的对象池获取 channel
	client.SendChan = h.getChan(capacity)

	h.logger.DebugKV("初始化 SendChan",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"capacity", capacity,
	)
}

// releaseClientSendChan 释放客户端的 SendChan 回对象池
// 在客户端断开连接时调用，复用 channel 减少内存分配
func (h *Hub) releaseClientSendChan(client *Client) {
	if client == nil || client.SendChan == nil {
		return
	}

	capacity := h.getClientCapacity(client)

	// 释放到对应容量的对象池
	h.releaseChan(client.SendChan, capacity)

	h.logger.DebugKV("SendChan 已释放",
		"client_id", client.ID,
		"user_id", client.UserID,
		"capacity", capacity,
	)

	client.SendChan = nil
}

// GetClientCapacityConfig 获取客户端容量配置（用于测试和调试）
func (h *Hub) GetClientCapacityConfig() *wsc.ClientCapacity {
	if h.config != nil && h.config.ClientCapacity != nil {
		return h.config.ClientCapacity
	}

	return wsc.DefaultClientCapacity()
}
