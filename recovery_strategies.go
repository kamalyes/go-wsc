/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:10:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 21:10:00
 * @FilePath: \go-wsc\recovery_strategies.go
 * @Description: 具体的错误恢复策略实现
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"runtime"
	"sync"
	"time"
)

// ConnectionRecoveryStrategy 连接恢复策略
type ConnectionRecoveryStrategy struct {
	ers *ErrorRecoverySystem
}

// CanHandle 判断是否能处理此错误
func (crs *ConnectionRecoveryStrategy) CanHandle(errorType ErrorType, severity ErrorSeverity) bool {
	return errorType == ErrorTypeConnection
}

// Recover 执行连接恢复操作
func (crs *ConnectionRecoveryStrategy) Recover(ctx context.Context, errorEntry *ErrorEntry) error {
	hub := crs.ers.hub

	// 检查错误上下文中是否有客户端ID
	if clientID, exists := errorEntry.Context["client_id"]; exists {
		if clientIDStr, ok := clientID.(string); ok {
			return crs.recoverClientConnection(ctx, hub, clientIDStr)
		}
	}

	// 执行通用连接恢复
	return crs.recoverGeneralConnection(ctx, hub)
}

// GetName 获取策略名称
func (crs *ConnectionRecoveryStrategy) GetName() string {
	return "ConnectionRecovery"
}

// recoverClientConnection 恢复特定客户端连接
func (crs *ConnectionRecoveryStrategy) recoverClientConnection(ctx context.Context, hub *Hub, clientID string) error {
	hub.mutex.Lock()
	client, exists := hub.clients[clientID]
	if !exists {
		hub.mutex.Unlock()
		return nil // 客户端已不存在，视为恢复成功
	}

	// 检查连接状态
	if client.Conn == nil {
		hub.mutex.Unlock()
		return nil // 连接已清理，视为恢复成功
	}

	// 复制客户端信息以避免持有锁
	userID := client.UserID
	userType := client.UserType
	hub.mutex.Unlock()

	// 尝试发送心跳检查连接
	select {
	case client.SendChan <- []byte(`{"type":"ping","timestamp":` + string(rune(time.Now().Unix())) + `}`):
		// 发送成功，连接可能正常
		return nil
	case <-time.After(5 * time.Second):
		// 超时，连接可能有问题，执行重连逻辑
		return crs.performReconnection(ctx, hub, clientID, userID, userType)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// performReconnection 执行重连
func (crs *ConnectionRecoveryStrategy) performReconnection(ctx context.Context, hub *Hub, clientID, userID string, userType UserType) error {
	// 清理旧连接
	hub.mutex.Lock()
	if client, exists := hub.clients[clientID]; exists {
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
		delete(hub.clients, clientID)
		delete(hub.userToClient, userID)
		if userType == UserTypeAgent {
			delete(hub.agentClients, userID)
		}
	}
	hub.mutex.Unlock()

	if hub.logger != nil {
		hub.logger.InfoKV("执行连接恢复",
			"client_id", clientID,
			"user_id", userID,
			"user_type", userType,
		)
	}

	// 注意：实际的重连需要客户端主动发起，这里只是清理状态
	return nil
}

// recoverGeneralConnection 通用连接恢复
func (crs *ConnectionRecoveryStrategy) recoverGeneralConnection(ctx context.Context, hub *Hub) error {
	// 检查所有连接的健康状态
	hub.mutex.RLock()
	clientsToCheck := make([]*Client, 0, len(hub.clients))
	for _, client := range hub.clients {
		clientsToCheck = append(clientsToCheck, client)
	}
	hub.mutex.RUnlock()

	deadConnections := 0
	for _, client := range clientsToCheck {
		select {
		case client.SendChan <- []byte(`{"type":"health_check"}`):
			// 发送成功
		case <-time.After(1 * time.Second):
			// 超时，可能是死连接
			deadConnections++
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if hub.logger != nil {
		hub.logger.InfoKV("连接健康检查完成",
			"total_connections", len(clientsToCheck),
			"dead_connections", deadConnections,
		)
	}

	return nil
}

// MemoryRecoveryStrategy 内存恢复策略
type MemoryRecoveryStrategy struct {
	ers *ErrorRecoverySystem
}

// CanHandle 判断是否能处理此错误
func (mrs *MemoryRecoveryStrategy) CanHandle(errorType ErrorType, severity ErrorSeverity) bool {
	return errorType == ErrorTypeMemory
}

// Recover 执行内存恢复操作
func (mrs *MemoryRecoveryStrategy) Recover(ctx context.Context, errorEntry *ErrorEntry) error {
	hub := mrs.ers.hub

	// 强制垃圾回收
	runtime.GC()
	runtime.GC() // 执行两次确保彻底清理

	// 如果有内存防护器，执行强制清理
	if hub.memoryGuard != nil {
		hub.memoryGuard.ForceCleanup()
	}

	// 清理消息缓冲区
	mrs.clearMessageBuffers(hub)

	// 清理对象池
	mrs.resetObjectPools(hub)

	if hub.logger != nil {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		hub.logger.InfoKV("内存恢复操作完成",
			"current_memory_mb", m.Alloc/1024/1024,
			"gc_count", m.NumGC,
		)
	}

	return nil
}

// GetName 获取策略名称
func (mrs *MemoryRecoveryStrategy) GetName() string {
	return "MemoryRecovery"
}

// clearMessageBuffers 清理消息缓冲区
func (mrs *MemoryRecoveryStrategy) clearMessageBuffers(hub *Hub) {
	// 清理广播通道
	if hub.broadcast != nil {
		select {
		case <-hub.broadcast:
		default:
		}
	}

	// 清理待发送消息通道
	if hub.pendingMessages != nil {
		for {
			select {
			case <-hub.pendingMessages:
			default:
				goto done
			}
		}
	done:
	}
}

// resetObjectPools 重置对象池
func (mrs *MemoryRecoveryStrategy) resetObjectPools(hub *Hub) {
	// 重置消息池
	hub.msgPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, 1024)
		},
	}
}

// SystemRecoveryStrategy 系统恢复策略
type SystemRecoveryStrategy struct {
	ers *ErrorRecoverySystem
}

// CanHandle 判断是否能处理此错误
func (srs *SystemRecoveryStrategy) CanHandle(errorType ErrorType, severity ErrorSeverity) bool {
	return errorType == ErrorTypeSystem || errorType == ErrorTypeConcurrency
}

// Recover 执行系统恢复操作
func (srs *SystemRecoveryStrategy) Recover(ctx context.Context, errorEntry *ErrorEntry) error {
	hub := srs.ers.hub

	// 检查goroutine数量
	goroutineCount := runtime.NumGoroutine()
	if goroutineCount > 1000 {
		// goroutine数量异常，可能有泄漏
		if hub.logger != nil {
			hub.logger.WarnKV("检测到异常goroutine数量",
				"count", goroutineCount,
			)
		}

		// 执行系统清理
		return srs.performSystemCleanup(ctx, hub)
	}

	// 检查系统负载
	return srs.checkSystemLoad(ctx, hub)
}

// GetName 获取策略名称
func (srs *SystemRecoveryStrategy) GetName() string {
	return "SystemRecovery"
}

// performSystemCleanup 执行系统清理
func (srs *SystemRecoveryStrategy) performSystemCleanup(ctx context.Context, hub *Hub) error {
	// 清理空闲连接
	srs.cleanupIdleConnections(hub)

	// 强制垃圾回收
	runtime.GC()

	// 等待一段时间让系统稳定
	select {
	case <-time.After(1 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	if hub.logger != nil {
		hub.logger.Info("系统清理完成")
	}

	return nil
}

// cleanupIdleConnections 清理空闲连接
func (srs *SystemRecoveryStrategy) cleanupIdleConnections(hub *Hub) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()

	now := time.Now()
	idleThreshold := now.Add(-30 * time.Minute) // 30分钟无活动
	cleanedCount := 0

	for clientID, client := range hub.clients {
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
			delete(hub.clients, clientID)
			delete(hub.userToClient, client.UserID)
			if client.UserType == UserTypeAgent {
				delete(hub.agentClients, client.UserID)
			}

			cleanedCount++
		}
	}

	if cleanedCount > 0 && hub.logger != nil {
		hub.logger.InfoKV("清理空闲连接",
			"cleaned_count", cleanedCount,
			"remaining_connections", len(hub.clients),
		)
	}
}

// checkSystemLoad 检查系统负载
func (srs *SystemRecoveryStrategy) checkSystemLoad(ctx context.Context, hub *Hub) error {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// 检查内存使用率
	memUsagePercent := float64(m.Alloc) / float64(m.Sys) * 100

	if memUsagePercent > 90 {
		// 内存使用率过高，执行清理
		runtime.GC()

		if hub.logger != nil {
			hub.logger.WarnKV("系统内存使用率过高，已执行垃圾回收",
				"memory_usage_percent", int(memUsagePercent),
			)
		}
	}

	return nil
}

// NetworkRecoveryStrategy 网络恢复策略
type NetworkRecoveryStrategy struct {
	ers *ErrorRecoverySystem
}

// CanHandle 判断是否能处理此错误
func (nrs *NetworkRecoveryStrategy) CanHandle(errorType ErrorType, severity ErrorSeverity) bool {
	return errorType == ErrorTypeNetwork
}

// Recover 执行网络恢复操作
func (nrs *NetworkRecoveryStrategy) Recover(ctx context.Context, errorEntry *ErrorEntry) error {
	hub := nrs.ers.hub

	// 检查网络连接状态
	activeConnections := 0
	deadConnections := 0

	hub.mutex.RLock()
	for _, client := range hub.clients {
		select {
		case client.SendChan <- []byte(`{"type":"network_check"}`):
			activeConnections++
		case <-time.After(2 * time.Second):
			deadConnections++
		case <-ctx.Done():
			hub.mutex.RUnlock()
			return ctx.Err()
		}
	}
	hub.mutex.RUnlock()

	if hub.logger != nil {
		hub.logger.InfoKV("网络状态检查完成",
			"active_connections", activeConnections,
			"dead_connections", deadConnections,
		)
	}

	// 如果死连接过多，执行网络恢复
	if deadConnections > activeConnections/2 {
		return nrs.performNetworkRecovery(ctx, hub)
	}

	return nil
}

// GetName 获取策略名称
func (nrs *NetworkRecoveryStrategy) GetName() string {
	return "NetworkRecovery"
}

// performNetworkRecovery 执行网络恢复
func (nrs *NetworkRecoveryStrategy) performNetworkRecovery(ctx context.Context, hub *Hub) error {
	// 广播网络状态检查消息
	networkCheckMsg := &HubMessage{
		MessageType: MessageTypeSystem,
		Content:     `{"type":"network_recovery","message":"Network recovery in progress"}`,
		CreateAt:    time.Now(),
	}

	select {
	case hub.broadcast <- networkCheckMsg:
	case <-time.After(5 * time.Second):
		// 广播超时
	case <-ctx.Done():
		return ctx.Err()
	}

	if hub.logger != nil {
		hub.logger.Info("网络恢复操作完成")
	}

	return nil
}
