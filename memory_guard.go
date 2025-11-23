/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 20:52:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 20:54:28
 * @FilePath: \go-wsc\memory_guard.go
 * @Description: 内存泄漏检测和自动清理机制
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

// MemoryGuard 内存泄漏防护器
type MemoryGuard struct {
	mu            sync.RWMutex
	hub           *Hub
	enabled       bool
	checkInterval time.Duration
	maxMemoryMB   int64
	gcThreshold   float64

	// 监控状态
	lastGCTime      time.Time
	lastMemUsage    uint64
	gcCount         uint32
	leakDetected    bool
	cleanupCallback func() error

	// 清理配置
	maxIdleConnections    int
	connectionIdleTimeout time.Duration
	messageBufferMaxSize  int
	orphanCleanupInterval time.Duration

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMemoryGuard 创建内存防护器
func NewMemoryGuard(hub *Hub, checkInterval time.Duration) *MemoryGuard {
	ctx, cancel := context.WithCancel(context.Background())

	return &MemoryGuard{
		hub:                   hub,
		enabled:               true,
		checkInterval:         checkInterval,
		maxMemoryMB:           512,  // 默认512MB限制
		gcThreshold:           0.8,  // 80%内存使用率触发GC
		maxIdleConnections:    1000, // 最大空闲连接数
		connectionIdleTimeout: 30 * time.Minute,
		messageBufferMaxSize:  10000,
		orphanCleanupInterval: 10 * time.Minute,
		lastGCTime:            time.Now(),
		ctx:                   ctx,
		cancel:                cancel,
		done:                  make(chan struct{}),
	}
}

// Start 启动内存防护
func (mg *MemoryGuard) Start() {
	if !mg.enabled {
		return
	}

	go mg.guardLoop()

	if mg.hub.logger != nil {
		mg.hub.logger.InfoKV("内存防护器已启动",
			"check_interval", mg.checkInterval,
			"max_memory_mb", mg.maxMemoryMB,
			"gc_threshold", mg.gcThreshold,
		)
	}
}

// Stop 停止内存防护
func (mg *MemoryGuard) Stop() {
	if mg.cancel != nil {
		mg.cancel()
	}

	// 强制等待，最多1秒
	select {
	case <-mg.done:
	case <-time.After(1 * time.Second):
		// 超时保护
	}

	if mg.hub.logger != nil {
		mg.hub.logger.Info("内存防护器已停止")
	}
}

// guardLoop 防护循环
func (mg *MemoryGuard) guardLoop() {
	defer close(mg.done)

	ticker := time.NewTicker(mg.checkInterval)
	defer ticker.Stop()

	// 立即进行一次检查
	mg.performMemoryCheck()

	for {
		select {
		case <-mg.ctx.Done():
			return
		case <-ticker.C:
			mg.performMemoryCheck()
		}
	}
}

// performMemoryCheck 执行内存检查
func (mg *MemoryGuard) performMemoryCheck() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	currentMemMB := int64(m.Alloc / 1024 / 1024)
	memUsagePercent := float64(m.Alloc) / float64(m.Sys) * 100

	// 记录当前内存使用
	mg.mu.Lock()
	mg.lastMemUsage = m.Alloc
	mg.mu.Unlock()

	// 检查内存使用是否超标
	if currentMemMB > mg.maxMemoryMB {
		mg.handleMemoryOverflow(currentMemMB, &m)
	}

	// 检查是否需要主动GC
	if memUsagePercent > mg.gcThreshold*100 {
		mg.triggerGC(&m)
	}

	// 检查潜在的内存泄漏
	if mg.detectMemoryLeak(&m) {
		mg.handleMemoryLeak(&m)
	}

	// 执行定期清理
	mg.performPeriodicCleanup()

	// 报告内存状态（如果启用详细日志）
	if mg.hub.logger != nil && currentMemMB > 50 { // 只在内存使用超过50MB时报告
		mg.hub.logger.InfoKV("内存状态检查",
			"current_memory_mb", currentMemMB,
			"memory_usage_percent", int(memUsagePercent),
			"gc_count", m.NumGC,
			"goroutine_count", runtime.NumGoroutine(),
		)
	}
}

// handleMemoryOverflow 处理内存溢出
func (mg *MemoryGuard) handleMemoryOverflow(currentMemMB int64, m *runtime.MemStats) {
	if mg.hub.logger != nil {
		mg.hub.logger.WarnKV("内存使用超标，开始清理",
			"current_memory_mb", currentMemMB,
			"max_memory_mb", mg.maxMemoryMB,
			"heap_alloc_mb", m.Alloc/1024/1024,
		)
	}

	// 执行激进清理策略
	mg.performAggressiveCleanup()

	// 强制垃圾回收
	runtime.GC()

	// 再次检查内存
	runtime.ReadMemStats(m)
	afterCleanupMB := int64(m.Alloc / 1024 / 1024)

	if mg.hub.logger != nil {
		mg.hub.logger.InfoKV("内存清理完成",
			"before_cleanup_mb", currentMemMB,
			"after_cleanup_mb", afterCleanupMB,
			"freed_mb", currentMemMB-afterCleanupMB,
		)
	}
}

// triggerGC 触发垃圾回收
func (mg *MemoryGuard) triggerGC(m *runtime.MemStats) {
	now := time.Now()
	if now.Sub(mg.lastGCTime) < 30*time.Second {
		return // 避免频繁GC
	}

	beforeGC := m.Alloc
	runtime.GC()

	// 更新GC时间和计数
	mg.mu.Lock()
	mg.lastGCTime = now
	mg.gcCount++
	mg.mu.Unlock()

	// 重新读取内存统计
	runtime.ReadMemStats(m)
	afterGC := m.Alloc

	if mg.hub.logger != nil {
		mg.hub.logger.InfoKV("主动垃圾回收",
			"before_gc_mb", beforeGC/1024/1024,
			"after_gc_mb", afterGC/1024/1024,
			"freed_mb", (beforeGC-afterGC)/1024/1024,
			"gc_count", mg.gcCount,
		)
	}
}

// detectMemoryLeak 检测内存泄漏
func (mg *MemoryGuard) detectMemoryLeak(m *runtime.MemStats) bool {
	mg.mu.RLock()
	defer mg.mu.RUnlock()

	// 检查内存是否持续增长
	if m.Alloc > mg.lastMemUsage {
		growthMB := (m.Alloc - mg.lastMemUsage) / 1024 / 1024
		if growthMB > 50 { // 超过50MB增长认为可能泄漏
			return true
		}
	}

	// 检查goroutine数量是否异常
	goroutineCount := runtime.NumGoroutine()
	if goroutineCount > 1000 { // 超过1000个goroutine认为可能泄漏
		return true
	}

	return false
}

// handleMemoryLeak 处理内存泄漏
func (mg *MemoryGuard) handleMemoryLeak(m *runtime.MemStats) {
	mg.mu.Lock()
	if mg.leakDetected {
		mg.mu.Unlock()
		return // 避免重复处理
	}
	mg.leakDetected = true
	mg.mu.Unlock()

	if mg.hub.logger != nil {
		mg.hub.logger.ErrorKV("检测到疑似内存泄漏",
			"current_memory_mb", m.Alloc/1024/1024,
			"goroutine_count", runtime.NumGoroutine(),
			"gc_count", m.NumGC,
		)
	}

	// 执行泄漏修复策略
	mg.performLeakMitigation()

	// 重置泄漏检测标志（5分钟后）
	time.AfterFunc(5*time.Minute, func() {
		mg.mu.Lock()
		mg.leakDetected = false
		mg.mu.Unlock()
	})
}

// performPeriodicCleanup 执行定期清理
func (mg *MemoryGuard) performPeriodicCleanup() {
	// 清理空闲连接
	mg.cleanupIdleConnections()

	// 清理过期消息缓冲区
	mg.cleanupMessageBuffers()

	// 清理孤立资源
	mg.cleanupOrphanedResources()
}

// performAggressiveCleanup 执行激进清理
func (mg *MemoryGuard) performAggressiveCleanup() {
	// 强制清理所有空闲连接
	mg.forceCleanupIdleConnections()

	// 清空消息缓冲区
	mg.flushMessageBuffers()

	// 清理所有缓存
	mg.clearCaches()

	// 执行自定义清理回调
	if mg.cleanupCallback != nil {
		if err := mg.cleanupCallback(); err != nil && mg.hub.logger != nil {
			mg.hub.logger.ErrorKV("自定义清理回调失败", "error", err)
		}
	}
}

// performLeakMitigation 执行泄漏缓解策略
func (mg *MemoryGuard) performLeakMitigation() {
	// 激进清理
	mg.performAggressiveCleanup()

	// 重置一些内部状态
	mg.resetInternalState()

	// 强制垃圾回收
	runtime.GC()
	runtime.GC() // 两次GC确保彻底清理
}

// cleanupIdleConnections 清理空闲连接
func (mg *MemoryGuard) cleanupIdleConnections() {
	mg.hub.mutex.Lock()
	defer mg.hub.mutex.Unlock()

	now := time.Now()
	idleThreshold := now.Add(-mg.connectionIdleTimeout)
	cleanedCount := 0

	for clientID, client := range mg.hub.clients {
		if client.LastSeen.Before(idleThreshold) {
			// 关闭连接
			if client.Conn != nil {
				client.Conn.Close()
			}

			// 清理资源
			select {
			case <-client.SendChan:
			default:
				close(client.SendChan)
			}

			// 从映射中删除
			delete(mg.hub.clients, clientID)
			delete(mg.hub.userToClient, client.UserID)
			if client.UserType == UserTypeAgent {
				delete(mg.hub.agentClients, client.UserID)
			}

			cleanedCount++
		}
	}

	if cleanedCount > 0 && mg.hub.logger != nil {
		mg.hub.logger.InfoKV("清理空闲连接",
			"cleaned_count", cleanedCount,
			"remaining_connections", len(mg.hub.clients),
		)
	}
}

// forceCleanupIdleConnections 强制清理空闲连接
func (mg *MemoryGuard) forceCleanupIdleConnections() {
	mg.hub.mutex.Lock()
	defer mg.hub.mutex.Unlock()

	// 保留最近活跃的前N个连接
	maxKeep := mg.maxIdleConnections / 2
	if len(mg.hub.clients) <= maxKeep {
		return
	}

	// 按最后活跃时间排序并保留最新的
	type clientWithTime struct {
		client   *Client
		clientID string
	}

	clients := make([]clientWithTime, 0, len(mg.hub.clients))
	for id, client := range mg.hub.clients {
		clients = append(clients, clientWithTime{client: client, clientID: id})
	}

	// 简单排序（按LastSeen降序）
	for i := 0; i < len(clients)-1; i++ {
		for j := i + 1; j < len(clients); j++ {
			if clients[i].client.LastSeen.Before(clients[j].client.LastSeen) {
				clients[i], clients[j] = clients[j], clients[i]
			}
		}
	}

	// 移除超出限制的连接
	removedCount := 0
	for i := maxKeep; i < len(clients); i++ {
		client := clients[i].client
		clientID := clients[i].clientID

		// 关闭连接
		if client.Conn != nil {
			client.Conn.Close()
		}

		// 清理资源
		select {
		case <-client.SendChan:
		default:
			close(client.SendChan)
		}

		// 从映射中删除
		delete(mg.hub.clients, clientID)
		delete(mg.hub.userToClient, client.UserID)
		if client.UserType == UserTypeAgent {
			delete(mg.hub.agentClients, client.UserID)
		}

		removedCount++
	}

	if removedCount > 0 && mg.hub.logger != nil {
		mg.hub.logger.WarnKV("强制清理连接",
			"removed_count", removedCount,
			"remaining_connections", len(mg.hub.clients),
		)
	}
}

// cleanupMessageBuffers 清理消息缓冲区
func (mg *MemoryGuard) cleanupMessageBuffers() {
	// 检查各个消息通道的长度
	if mg.hub.broadcast != nil {
		broadcastLen := len(mg.hub.broadcast)
		if broadcastLen > mg.messageBufferMaxSize/2 {
			// 清理一半的消息
			for i := 0; i < broadcastLen/2; i++ {
				select {
				case <-mg.hub.broadcast:
				default:
					break
				}
			}

			if mg.hub.logger != nil {
				mg.hub.logger.WarnKV("清理广播消息缓冲区",
					"original_size", broadcastLen,
					"cleaned_count", broadcastLen/2,
				)
			}
		}
	}
}

// flushMessageBuffers 清空消息缓冲区
func (mg *MemoryGuard) flushMessageBuffers() {
	// 清空广播通道
	if mg.hub.broadcast != nil {
		flushedCount := 0
		for {
			select {
			case <-mg.hub.broadcast:
				flushedCount++
			default:
				goto done
			}
		}
	done:
		if flushedCount > 0 && mg.hub.logger != nil {
			mg.hub.logger.WarnKV("强制清空广播缓冲区", "flushed_count", flushedCount)
		}
	}

	// 清空其他通道...
}

// clearCaches 清理缓存
func (mg *MemoryGuard) clearCaches() {
	// 清理消息池
	if mg.hub.msgPool.New != nil {
		// 重置对象池
		mg.hub.msgPool = sync.Pool{
			New: func() interface{} {
				return make([]byte, 0, 1024)
			},
		}
	}
}

// cleanupOrphanedResources 清理孤立资源
func (mg *MemoryGuard) cleanupOrphanedResources() {
	// 清理SSE连接中的孤立资源
	mg.hub.sseMutex.Lock()
	defer mg.hub.sseMutex.Unlock()

	now := time.Now()
	orphanThreshold := now.Add(-mg.orphanCleanupInterval)
	cleanedSSE := 0

	for userID, sseConn := range mg.hub.sseClients {
		if sseConn.LastActive.Before(orphanThreshold) {
			close(sseConn.CloseCh)
			delete(mg.hub.sseClients, userID)
			cleanedSSE++
		}
	}

	if cleanedSSE > 0 && mg.hub.logger != nil {
		mg.hub.logger.InfoKV("清理孤立SSE连接",
			"cleaned_count", cleanedSSE,
			"remaining_sse", len(mg.hub.sseClients),
		)
	}
}

// resetInternalState 重置内部状态
func (mg *MemoryGuard) resetInternalState() {
	// 这里可以重置一些可能导致内存泄漏的内部状态
	// 例如清理统计数据、重置计数器等
}

// SetCleanupCallback 设置自定义清理回调
func (mg *MemoryGuard) SetCleanupCallback(callback func() error) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	mg.cleanupCallback = callback
}

// SetMemoryLimit 设置内存限制
func (mg *MemoryGuard) SetMemoryLimit(limitMB int64) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	mg.maxMemoryMB = limitMB
}

// SetGCThreshold 设置GC触发阈值
func (mg *MemoryGuard) SetGCThreshold(threshold float64) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	mg.gcThreshold = threshold
}

// GetMemoryStats 获取内存统计信息
func (mg *MemoryGuard) GetMemoryStats() map[string]interface{} {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	mg.mu.RLock()
	defer mg.mu.RUnlock()

	return map[string]interface{}{
		"alloc_mb":        m.Alloc / 1024 / 1024,
		"total_alloc_mb":  m.TotalAlloc / 1024 / 1024,
		"sys_mb":          m.Sys / 1024 / 1024,
		"gc_count":        m.NumGC,
		"last_gc_time":    mg.lastGCTime,
		"goroutine_count": runtime.NumGoroutine(),
		"memory_limit_mb": mg.maxMemoryMB,
		"gc_threshold":    mg.gcThreshold,
		"leak_detected":   mg.leakDetected,
		"guard_gc_count":  mg.gcCount,
	}
}

// Enable 启用内存防护
func (mg *MemoryGuard) Enable() {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	mg.enabled = true
}

// Disable 禁用内存防护
func (mg *MemoryGuard) Disable() {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	mg.enabled = false
}

// IsEnabled 检查是否启用
func (mg *MemoryGuard) IsEnabled() bool {
	mg.mu.RLock()
	defer mg.mu.RUnlock()
	return mg.enabled
}

// ForceCleanup 强制执行清理
func (mg *MemoryGuard) ForceCleanup() {
	mg.performAggressiveCleanup()
	runtime.GC()
}

// GetUnsafePointerCount 获取不安全指针计数（用于调试）
func (mg *MemoryGuard) GetUnsafePointerCount() int {
	// 这是一个简化的实现，实际中可以统计unsafe.Pointer的使用
	return int(uintptr(unsafe.Pointer(&mg.hub)) % 1000)
}
