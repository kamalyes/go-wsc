/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-11 10:36:00
 * @FilePath: \go-wsc\connection\capacity.go
 * @Description: 客户端容量管理 - 按 UserType 配置通道容量并初始化通道四件套
 *
 * 迁移自 hub/client_capacity.go（P2 批1 域化）：原 *Hub 方法重组为 CapacityManager 组件，
 * 依赖 config/logger 经构造注入
 *
 * channel 生命周期与 Client 一致：每连接按容量独立分配，断连后随 Client 整体交由 GC 回收；
 * 不做跨连接复用——racing sender 竞态窗口内复用会跨连接串消息（见 lifecycle.CloseChannel）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// DefaultClientSendChanCapacity 默认客户端 SendChan 缓冲区容量（当配置不存在时使用）
const DefaultClientSendChanCapacity = 256

// CapacityManager 按 UserType 配置通道容量并初始化客户端通道四件套
// （DoneCh/PongCh/CtrlCh/SendChan）；channel 与 Client 同生命周期，随连接分配、断连后由 GC 回收
type CapacityManager struct {
	cfg    *wscconfig.ClientCapacity
	logger spi.Logger
}

// NewCapacityManager 创建容量管理器
// cfg 为 nil 时使用 wscconfig.DefaultClientCapacity()；logger 为 nil 时使用默认日志器
func NewCapacityManager(cfg *wscconfig.ClientCapacity, logger spi.Logger) *CapacityManager {
	if cfg == nil {
		cfg = wscconfig.DefaultClientCapacity()
	}
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &CapacityManager{cfg: cfg, logger: logger}
}

// CapacityFor 根据客户端 UserType 获取 SendChan 缓冲区容量（配置不存在项为 0 时由上层兜底）
func (cm *CapacityManager) CapacityFor(client *models.Client) int {
	if client == nil {
		return DefaultClientSendChanCapacity
	}

	switch client.UserType {
	case models.UserTypeAgent:
		return cm.cfg.Agent
	case models.UserTypeBot:
		return cm.cfg.Bot
	case models.UserTypeCustomer:
		return cm.cfg.Customer
	case models.UserTypeObserver:
		return cm.cfg.Observer
	case models.UserTypeAdmin:
		return cm.cfg.Admin
	case models.UserTypeVIP:
		return cm.cfg.VIP
	case models.UserTypeVisitor:
		return cm.cfg.Visitor
	case models.UserTypeSystem:
		return cm.cfg.System
	default:
		return cm.cfg.Default
	}
}

// InitClientSendChan 初始化客户端的通道四件套（按 UserType 容量分配 SendChan）
// 在客户端注册时调用，确保 SendChan 使用正确的缓冲区大小
func (cm *CapacityManager) InitClientSendChan(client *models.Client) {
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
	// 容量固定（constants.CtrlChanCapacity，控制消息量级低）
	if client.ConnectionType != models.ConnectionTypeSSE && client.CtrlCh == nil {
		client.CtrlCh = make(chan []byte, constants.CtrlChanCapacity)
	}

	// 如果 SendChan 已经初始化，不再重新初始化
	if client.SendChan != nil {
		return
	}

	// 获取该客户端类型的容量
	capacity := cm.CapacityFor(client)
	if capacity <= 0 {
		capacity = DefaultClientSendChanCapacity
	}
	client.SendChan = make(chan []byte, capacity)

	cm.logger.DebugContextKV(client.Context, "初始化 SendChan",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"capacity", capacity,
	)
}
