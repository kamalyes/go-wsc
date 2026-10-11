/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:16:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:16:00
 * @FilePath: \go-wsc\broker\manager.go
 * @Description: 消息总线消费域管理器 - 生命周期编排（随 Hub Start/Stop）
 *
 * 订阅拓扑（当前双通道，express 定向通道随 NATS 适配器接入）：
 *   - TopicDeliver   queue 语义：一条消息单节点消费 → Deliver 决策树
 *   - TopicBroadcast fanout 语义：每节点各消费一份 → 只投本地连接
 *
 * 优雅关闭顺序（drain 四步曲的第二步）：停止拉取 → 排空在途批次 →
 * 提交消费位点 → 退出订阅循环（bus.Close 承接，Hub.Stop 编排调用）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"context"
	"sync"
	"time"

	"github.com/kamalyes/go-wsc/spi"
)

// Manager 消费域管理器（订阅生命周期与消费编排）
type Manager struct {
	bus      spi.EventBus
	consumer *Consumer
	logger   spi.Logger

	startOnce sync.Once
	closeOnce sync.Once
	closed    chan struct{}
}

// NewManager 创建消费域管理器
//
// deliver 为投递收敛端口（hub.Deliver）；dedup 参数零值走默认
func NewManager(bus spi.EventBus, deliver DeliverFunc, dedupTTL time.Duration, dedupShards int, logger spi.Logger) *Manager {
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &Manager{
		bus:      bus,
		consumer: NewConsumer(deliver, dedupTTL, dedupShards, logger),
		logger:   logger,
		closed:   make(chan struct{}),
	}
}

// Start 启动订阅（幂等：重复调用无副作用）
//
// 订阅循环由适配器侧驱动（EventBus.Subscribe 阻塞或内部 goroutine 均可），
// Manager 只保证启动一次与关闭排空
func (m *Manager) Start(ctx context.Context) error {
	var startErr error
	m.startOnce.Do(func() {
		if m.bus == nil {
			m.logger.WarnKV("消息总线未装配，消费域跳过启动（直调模式）")
			return
		}
		if err := m.bus.Subscribe(ctx, spi.TopicDeliver, m.consumer.HandleDeliver()); err != nil {
			startErr = err
			m.logger.ErrorKV("投递通道订阅失败", "error", err.Error())
			return
		}
		if err := m.bus.Subscribe(ctx, spi.TopicBroadcast, m.consumer.HandleBroadcast()); err != nil {
			startErr = err
			m.logger.ErrorKV("广播通道订阅失败", "error", err.Error())
			return
		}
		m.logger.InfoKV("消息总线消费域已启动")
	})
	return startErr
}

// Close 优雅关闭（排空在途消息、退出订阅循环；幂等）
func (m *Manager) Close() error {
	var closeErr error
	m.closeOnce.Do(func() {
		close(m.closed)
		if m.bus != nil {
			if err := m.bus.Close(); err != nil {
				closeErr = err
				m.logger.ErrorKV("消息总线关闭异常", "error", err.Error())
			}
		}
		m.logger.InfoKV("消息总线消费域已关闭")
	})
	return closeErr
}

// Closed 关闭信号（Hub drain 编排可监听此通道感知消费域退出）
func (m *Manager) Closed() <-chan struct{} {
	return m.closed
}
