/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 10:20:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 10:20:00
 * @FilePath: \go-wsc\transport\testhelpers_test.go
 * @Description: 传输域测试基建 - Registrar 端口桩 / 升级配置样例
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"sync"
	"sync/atomic"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
)

// fakeRegistrar Registrar 端口桩：记录全部回调供断言，支持注册同步钩子
type fakeRegistrar struct {
	mu             sync.Mutex
	registered     []*models.Client // Register 异步注册回调记录
	syncRegistered []*models.Client // RegisterSync 同步注册回调记录
	unregistered   []*models.Client // Unregister 注销回调记录
	confirmed      []*models.Client // SendRegisteredMessage 确认消息回调记录
	shutdown       atomic.Bool
	onRegisterSync func(*models.Client) // 同步注册后的钩子（SSE 集成测里向通道投递消息）
}

func (f *fakeRegistrar) Register(client *models.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, client)
}

func (f *fakeRegistrar) RegisterSync(client *models.Client) {
	f.mu.Lock()
	f.syncRegistered = append(f.syncRegistered, client)
	hook := f.onRegisterSync
	f.mu.Unlock()

	if hook != nil {
		hook(client)
	}
}

func (f *fakeRegistrar) Unregister(client *models.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregistered = append(f.unregistered, client)
}

func (f *fakeRegistrar) IsShutdown() bool {
	return f.shutdown.Load()
}

func (f *fakeRegistrar) SendRegisteredMessage(client *models.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmed = append(f.confirmed, client)
}

// counts 返回各回调计数（快照）
func (f *fakeRegistrar) counts() (reg, syncReg, unreg, confirm int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registered), len(f.syncRegistered), len(f.unregistered), len(f.confirmed)
}

// lastRegistered 返回最近一次异步注册的客户端
func (f *fakeRegistrar) lastRegistered() *models.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.registered) == 0 {
		return nil
	}
	return f.registered[len(f.registered)-1]
}

// newUpgraderTestConfig 升级链路测试配置（健康检查/验证/响应头默认关闭，用例内按需开启）
func newUpgraderTestConfig() *wscconfig.WSC {
	cfg := wscconfig.Default()
	cfg.MessageBufferSize = 1024
	cfg.ClientAttributes = wscconfig.DefaultClientAttributes()
	cfg.TemporalHasher = wscconfig.DefaultTemporalHasher()
	cfg.HealthCheck = wscconfig.DefaultHealthCheck()
	cfg.HealthCheck.Enabled = false
	cfg.ConnectionValidation = wscconfig.DefaultConnectionValidation()
	cfg.ConnectionValidation.Enabled = false
	cfg.ResponseHeaders = wscconfig.DefaultResponseHeaders()
	return cfg
}
