/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-11 10:36:00
 * @FilePath: \go-wsc\connection\capacity.go
 * @Description: 客户端容量管理 - 按 UserType 配置 SendChan 缓冲区容量 + channel 对象池复用
 *
 * 迁移自 hub/client_capacity.go（P2 批1 域化）：原 *Hub 方法重组为 ChanPool 组件，
 * 依赖 config/logger 经构造注入；容量分派与池化语义保持不变
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"sync"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// DefaultClientSendChanCapacity 默认客户端 SendChan 缓冲区容量（当配置不存在时使用）
const DefaultClientSendChanCapacity = 256

// ChanPool 按 UserType 容量分桶的 channel 对象池
// 注册时从对应容量池取 SendChan/CtrlCh，断连时归还复用，减少连接风暴下的内存分配
type ChanPool struct {
	cfg    *wscconfig.ClientCapacity
	logger spi.Logger
	pools  map[int]*sync.Pool // 容量值 → 该容量的 channel 池
}

// NewChanPool 创建容量管理器
// cfg 为 nil 时使用 wscconfig.DefaultClientCapacity()；logger 为 nil 时使用默认日志器
func NewChanPool(cfg *wscconfig.ClientCapacity, logger spi.Logger) *ChanPool {
	if cfg == nil {
		cfg = wscconfig.DefaultClientCapacity()
	}
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	cp := &ChanPool{cfg: cfg, logger: logger}
	cp.initChannelPools()
	return cp
}

// CapacityFor 根据客户端 UserType 获取 SendChan 缓冲区容量（配置不存在项为 0 时由上层兜底）
func (cp *ChanPool) CapacityFor(client *models.Client) int {
	if client == nil {
		return DefaultClientSendChanCapacity
	}

	switch client.UserType {
	case models.UserTypeAgent:
		return cp.cfg.Agent
	case models.UserTypeBot:
		return cp.cfg.Bot
	case models.UserTypeCustomer:
		return cp.cfg.Customer
	case models.UserTypeObserver:
		return cp.cfg.Observer
	case models.UserTypeAdmin:
		return cp.cfg.Admin
	case models.UserTypeVIP:
		return cp.cfg.VIP
	case models.UserTypeVisitor:
		return cp.cfg.Visitor
	case models.UserTypeSystem:
		return cp.cfg.System
	default:
		return cp.cfg.Default
	}
}

// InitClientSendChan 初始化客户端的 SendChan，优先从对象池获取
// 在客户端注册时调用，确保 SendChan 使用正确的缓冲区大小
func (cp *ChanPool) InitClientSendChan(client *models.Client) {
	if client == nil {
		return
	}

	// 生命周期关闭信号（closeClientChannel close 它通知写泵退出，SendChan 永不 close）
	// 必须在 SendChan early-return 之前初始化：测试常手工构造带 SendChan 的客户端，
	// 若跳过则 DoneCh 为 nil → 写泵收不到退出信号导致协程泄漏
	if client.DoneCh == nil {
		client.DoneCh = make(chan struct{})
	}

	// pong 控制帧队列（仅 WebSocket 客户端；SSE 无控制帧）
	// 读协程收到协议级 PING 后非阻塞投递，写泵统一写出（gorilla 单写者模式，见 setupPingHandler）
	// 容量 1 已足够：待写未取走时新 PING 直接丢弃，客户端超时会重发
	if client.ConnectionType != models.ConnectionTypeSSE && client.PongCh == nil {
		client.PongCh = make(chan []byte, 1)
	}

	// 🚦 控制通道（必达级消息独立 lane；仅 WebSocket 客户端）
	// 写泵 select 前优先排空 CtrlCh，KickOut/ForceOffline/Ack 不被业务 SendChan 洪峰淹没；
	// 容量固定（constants.CtrlChanCapacity，控制消息量级低），走 chanPools 复用
	if client.ConnectionType != models.ConnectionTypeSSE && client.CtrlCh == nil {
		client.CtrlCh = cp.getChan(constants.CtrlChanCapacity)
	}

	// 如果 SendChan 已经初始化，不再重新初始化
	if client.SendChan != nil {
		return
	}

	// 获取该客户端类型的容量并从对应容量池取 channel
	capacity := cp.CapacityFor(client)
	if capacity <= 0 {
		capacity = DefaultClientSendChanCapacity
	}
	client.SendChan = cp.getChan(capacity)

	cp.logger.DebugContextKV(client.Context, "初始化 SendChan",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"capacity", capacity,
	)
}

// ReleaseClientSendChan 释放客户端的 SendChan/CtrlCh 回对象池
// 在客户端断开连接时调用，复用 channel 减少内存分配
func (cp *ChanPool) ReleaseClientSendChan(client *models.Client) {
	if client == nil {
		return
	}

	// 🚦 控制通道回收（与 SendChan 同池复用；closeClientChannel 不 close 数据/控制通道，此处置 nil）
	if client.CtrlCh != nil {
		if client.ConnectionType != models.ConnectionTypeSSE {
			cp.releaseChan(client.CtrlCh, constants.CtrlChanCapacity)
		}
		client.CtrlCh = nil
	}

	if client.SendChan == nil {
		return
	}

	capacity := cp.CapacityFor(client)
	if capacity <= 0 {
		capacity = DefaultClientSendChanCapacity
	}

	// 释放到对应容量的对象池
	cp.releaseChan(client.SendChan, capacity)

	cp.logger.DebugContextKV(client.Context, "SendChan 已释放",
		"client_id", client.ID,
		"user_id", client.UserID,
		"capacity", capacity,
	)

	client.SendChan = nil
}

// initChannelPools 初始化多级 channel 对象池
// 从配置中获取所有容量值，为每个容量创建对象池
func (cp *ChanPool) initChannelPools() {
	cp.pools = make(map[int]*sync.Pool)

	for _, capacity := range cp.uniqueCapacities() {
		cap := capacity // 捕获循环变量
		cp.pools[cap] = &sync.Pool{
			New: func() any {
				return make(chan []byte, cap)
			},
		}
	}

	cp.logger.DebugKV("多级 channel 对象池已初始化",
		"capacities", cp.uniqueCapacities(),
	)
}

// uniqueCapacities 从配置中获取所有唯一的容量值
func (cp *ChanPool) uniqueCapacities() []int {
	capacityMap := make(map[int]struct{})

	capacityMap[cp.cfg.Agent] = struct{}{}
	capacityMap[cp.cfg.Bot] = struct{}{}
	capacityMap[cp.cfg.Customer] = struct{}{}
	capacityMap[cp.cfg.Observer] = struct{}{}
	capacityMap[cp.cfg.Admin] = struct{}{}
	capacityMap[cp.cfg.VIP] = struct{}{}
	capacityMap[cp.cfg.Visitor] = struct{}{}
	capacityMap[cp.cfg.System] = struct{}{}
	capacityMap[cp.cfg.Default] = struct{}{}

	capacities := make([]int, 0, len(capacityMap))
	for cap := range capacityMap {
		capacities = append(capacities, cap)
	}
	return capacities
}

// getChan 从对象池获取指定容量的 channel
// 如果对象池中没有该容量的池，则创建新 channel
func (cp *ChanPool) getChan(capacity int) chan []byte {
	if capacity <= 0 {
		capacity = DefaultClientSendChanCapacity
	}
	if pool, exists := cp.pools[capacity]; exists {
		if ch := pool.Get(); ch != nil {
			return ch.(chan []byte)
		}
	}
	return make(chan []byte, capacity)
}

// releaseChan 释放 channel 回对象池
func (cp *ChanPool) releaseChan(ch chan []byte, capacity int) {
	if ch == nil {
		return
	}

	// 清空 channel 中的数据
	for len(ch) > 0 {
		<-ch
	}

	if pool, exists := cp.pools[capacity]; exists {
		pool.Put(ch)
	}
}
