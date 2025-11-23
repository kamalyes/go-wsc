/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 21:56:09
 * @FilePath: \go-wsc\error_recovery.go
 * @Description: 错误恢复和容错机制
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrorSeverity 错误严重程度
type ErrorSeverity int

const (
	SeverityLow      ErrorSeverity = 1 // 低危
	SeverityMedium   ErrorSeverity = 2 // 中危
	SeverityHigh     ErrorSeverity = 3 // 高危
	SeverityCritical ErrorSeverity = 4 // 严重
)

// ErrorEntry 错误记录条目
type ErrorEntry struct {
	ID          string                 `json:"id"`
	Type        ErrorType              `json:"type"`
	Severity    ErrorSeverity          `json:"severity"`
	Message     string                 `json:"message"`
	Timestamp   time.Time              `json:"timestamp"`
	Count       int64                  `json:"count"`
	LastOccured time.Time              `json:"last_occured"`
	Context     map[string]interface{} `json:"context"`
	Recovered   bool                   `json:"recovered"`
}

// RecoveryStrategy 恢复策略接口
type RecoveryStrategy interface {
	// CanHandle 判断是否能处理此错误
	CanHandle(errorType ErrorType, severity ErrorSeverity) bool

	// Recover 执行恢复操作
	Recover(ctx context.Context, errorEntry *ErrorEntry) error

	// GetName 获取策略名称
	GetName() string
}

// ErrorRecoverySystem 错误恢复系统
type ErrorRecoverySystem struct {
	mu      sync.RWMutex
	hub     *Hub
	enabled bool

	// 错误跟踪
	errorHistory    map[string]*ErrorEntry
	maxHistorySize  int
	cleanupInterval time.Duration

	// 恢复策略
	strategies []RecoveryStrategy

	// 熔断器
	circuitBreakers map[ErrorType]*CircuitBreaker

	// 重试配置
	maxRetries      int
	retryInterval   time.Duration
	backoffMultiple float64

	// 统计信息
	totalErrors      int64
	recoveredErrors  int64
	failedRecoveries int64

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewErrorRecoverySystem 创建错误恢复系统
func NewErrorRecoverySystem(hub *Hub) *ErrorRecoverySystem {
	ctx, cancel := context.WithCancel(context.Background())

	ers := &ErrorRecoverySystem{
		hub:             hub,
		enabled:         true,
		errorHistory:    make(map[string]*ErrorEntry),
		maxHistorySize:  1000,
		cleanupInterval: 10 * time.Minute,
		strategies:      make([]RecoveryStrategy, 0),
		circuitBreakers: make(map[ErrorType]*CircuitBreaker),
		maxRetries:      3,
		retryInterval:   1 * time.Second,
		backoffMultiple: 2.0,
		ctx:             ctx,
		cancel:          cancel,
		done:            make(chan struct{}),
	}

	// 初始化默认恢复策略
	ers.initDefaultStrategies()

	// 初始化熔断器
	ers.initCircuitBreakers()

	return ers
}

// initDefaultStrategies 初始化默认恢复策略
func (ers *ErrorRecoverySystem) initDefaultStrategies() {
	// 连接恢复策略
	ers.strategies = append(ers.strategies, &ConnectionRecoveryStrategy{ers: ers})

	// 内存恢复策略
	ers.strategies = append(ers.strategies, &MemoryRecoveryStrategy{ers: ers})

	// 系统恢复策略
	ers.strategies = append(ers.strategies, &SystemRecoveryStrategy{ers: ers})

	// 网络恢复策略
	ers.strategies = append(ers.strategies, &NetworkRecoveryStrategy{ers: ers})
}

// initCircuitBreakers 初始化熔断器
func (ers *ErrorRecoverySystem) initCircuitBreakers() {
	errorTypes := []ErrorType{
		ErrorTypeConnection, ErrorTypeMessage, ErrorTypeSystem,
		ErrorTypeNetwork, ErrorTypeConcurrency, ErrorTypeMemory,
		ErrorTypeConfiguration,
	}

	for _, errorType := range errorTypes {
		ers.circuitBreakers[errorType] = &CircuitBreaker{
			state:            CircuitClosed,
			failureThreshold: 5,                // 5次失败后熔断
			successThreshold: 3,                // 3次成功后恢复
			timeout:          30 * time.Second, // 30秒后尝试半开
		}
	}
}

// Start 启动错误恢复系统
func (ers *ErrorRecoverySystem) Start() {
	if !ers.enabled {
		return
	}

	go ers.cleanupLoop()

	if ers.hub.logger != nil {
		ers.hub.logger.InfoKV("错误恢复系统已启动",
			"max_retries", ers.maxRetries,
			"retry_interval", ers.retryInterval,
			"strategies_count", len(ers.strategies),
		)
	}
}

// Stop 停止错误恢复系统
func (ers *ErrorRecoverySystem) Stop() {
	if ers.cancel != nil {
		ers.cancel()
	}

	// 强制等待，最多1秒
	select {
	case <-ers.done:
	case <-time.After(1 * time.Second):
		// 超时保护
	}

	if ers.hub.logger != nil {
		ers.hub.logger.Info("错误恢复系统已停止")
	}
}

// cleanupLoop 清理循环
func (ers *ErrorRecoverySystem) cleanupLoop() {
	defer close(ers.done)

	ticker := time.NewTicker(ers.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ers.ctx.Done():
			return
		case <-ticker.C:
			ers.cleanupOldErrors()
		}
	}
}

// cleanupOldErrors 清理旧错误记录
func (ers *ErrorRecoverySystem) cleanupOldErrors() {
	ers.mu.Lock()
	defer ers.mu.Unlock()

	if len(ers.errorHistory) <= ers.maxHistorySize {
		return
	}

	// 按时间排序，删除最老的记录
	oldest := time.Now()
	var oldestKey string

	for key, entry := range ers.errorHistory {
		if entry.Timestamp.Before(oldest) {
			oldest = entry.Timestamp
			oldestKey = key
		}
	}

	if oldestKey != "" {
		delete(ers.errorHistory, oldestKey)
	}

	if ers.hub.logger != nil {
		ers.hub.logger.InfoKV("清理旧错误记录", "history_size", len(ers.errorHistory))
	}
}

// ReportError 报告错误
func (ers *ErrorRecoverySystem) ReportError(errorType ErrorType, severity ErrorSeverity, message string, context map[string]interface{}) string {
	if !ers.enabled {
		return ""
	}

	errorID := fmt.Sprintf("%d_%d_%d", int(errorType), time.Now().UnixNano(), severity)
	now := time.Now()

	ers.mu.Lock()

	// 检查是否已有相同类型的错误
	var existingEntry *ErrorEntry
	for _, entry := range ers.errorHistory {
		if entry.Type == errorType && entry.Message == message {
			existingEntry = entry
			break
		}
	}

	if existingEntry != nil {
		// 更新现有错误
		existingEntry.Count++
		existingEntry.LastOccured = now
		errorID = existingEntry.ID
	} else {
		// 创建新错误记录
		entry := &ErrorEntry{
			ID:          errorID,
			Type:        errorType,
			Severity:    severity,
			Message:     message,
			Timestamp:   now,
			Count:       1,
			LastOccured: now,
			Context:     context,
			Recovered:   false,
		}
		ers.errorHistory[errorID] = entry
	}

	// 增加错误统计
	atomic.AddInt64(&ers.totalErrors, 1)

	ers.mu.Unlock()

	// 检查熔断器状态
	circuitBreaker := ers.circuitBreakers[errorType]
	if circuitBreaker != nil {
		ers.checkCircuitBreaker(circuitBreaker, false)
	}

	// 尝试恢复
	go ers.attemptRecovery(errorID)

	if ers.hub.logger != nil {
		ers.hub.logger.ErrorKV("错误已报告",
			"error_id", errorID,
			"type", errorType,
			"severity", severity,
			"message", message,
		)
	}

	return errorID
}

// attemptRecovery 尝试恢复
func (ers *ErrorRecoverySystem) attemptRecovery(errorID string) {
	ers.mu.RLock()
	entry, exists := ers.errorHistory[errorID]
	if !exists {
		ers.mu.RUnlock()
		return
	}

	// 复制错误条目以避免锁竞争
	entryCopy := *entry
	ers.mu.RUnlock()

	// 检查熔断器状态
	circuitBreaker := ers.circuitBreakers[entryCopy.Type]
	if circuitBreaker != nil && ers.isCircuitOpen(circuitBreaker) {
		return // 熔断器开启，跳过恢复
	}

	// 查找合适的恢复策略
	var selectedStrategy RecoveryStrategy
	for _, strategy := range ers.strategies {
		if strategy.CanHandle(entryCopy.Type, entryCopy.Severity) {
			selectedStrategy = strategy
			break
		}
	}

	if selectedStrategy == nil {
		return // 没有合适的恢复策略
	}

	// 执行恢复重试循环
	success := ers.executeWithRetry(selectedStrategy, &entryCopy)

	// 更新恢复状态
	ers.mu.Lock()
	if entry, exists := ers.errorHistory[errorID]; exists {
		entry.Recovered = success
	}
	ers.mu.Unlock()

	// 更新统计和熔断器状态
	if success {
		atomic.AddInt64(&ers.recoveredErrors, 1)
		if circuitBreaker != nil {
			ers.checkCircuitBreaker(circuitBreaker, true)
		}

		if ers.hub.logger != nil {
			ers.hub.logger.InfoKV("错误恢复成功",
				"error_id", errorID,
				"strategy", selectedStrategy.GetName(),
			)
		}
	} else {
		atomic.AddInt64(&ers.failedRecoveries, 1)
		if circuitBreaker != nil {
			ers.checkCircuitBreaker(circuitBreaker, false)
		}

		if ers.hub.logger != nil {
			ers.hub.logger.ErrorKV("错误恢复失败",
				"error_id", errorID,
				"strategy", selectedStrategy.GetName(),
			)
		}
	}
}

// executeWithRetry 带重试的执行
func (ers *ErrorRecoverySystem) executeWithRetry(strategy RecoveryStrategy, entry *ErrorEntry) bool {
	var lastErr error
	interval := ers.retryInterval

	for attempt := 0; attempt <= ers.maxRetries; attempt++ {
		if attempt > 0 {
			// 等待重试间隔（指数退避）
			time.Sleep(interval)
			interval = time.Duration(float64(interval) * ers.backoffMultiple)
		}

		// 创建带超时的上下文
		ctx, cancel := context.WithTimeout(ers.ctx, 30*time.Second)
		err := strategy.Recover(ctx, entry)
		cancel()

		if err == nil {
			return true // 恢复成功
		}

		lastErr = err

		if ers.hub.logger != nil {
			ers.hub.logger.WarnKV("错误恢复重试",
				"error_id", entry.ID,
				"attempt", attempt+1,
				"max_attempts", ers.maxRetries+1,
				"error", err.Error(),
			)
		}
	}

	if ers.hub.logger != nil {
		ers.hub.logger.ErrorKV("错误恢复彻底失败",
			"error_id", entry.ID,
			"final_error", lastErr.Error(),
		)
	}

	return false
}

// checkCircuitBreaker 检查和更新熔断器状态
func (ers *ErrorRecoverySystem) checkCircuitBreaker(cb *CircuitBreaker, success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()

	if success {
		cb.successCount++
		if cb.state == CircuitHalfOpen && cb.successCount >= cb.successThreshold {
			cb.state = CircuitClosed
			cb.failureCount = 0
			cb.successCount = 0
		}
	} else {
		cb.failureCount++
		cb.lastFailTime = now

		if cb.state == CircuitClosed && cb.failureCount >= cb.failureThreshold {
			cb.state = CircuitOpen
		} else if cb.state == CircuitHalfOpen {
			cb.state = CircuitOpen
		}
	}

	// 检查是否可以从Open状态转为HalfOpen
	if cb.state == CircuitOpen && now.Sub(cb.lastFailTime) >= cb.timeout {
		cb.state = CircuitHalfOpen
		cb.successCount = 0
	}
}

// isCircuitOpen 检查熔断器是否打开
func (ers *ErrorRecoverySystem) isCircuitOpen(cb *CircuitBreaker) bool {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.state == CircuitOpen
}

// AddRecoveryStrategy 添加恢复策略
func (ers *ErrorRecoverySystem) AddRecoveryStrategy(strategy RecoveryStrategy) {
	ers.mu.Lock()
	defer ers.mu.Unlock()
	ers.strategies = append(ers.strategies, strategy)
}

// GetErrorHistory 获取错误历史
func (ers *ErrorRecoverySystem) GetErrorHistory() map[string]*ErrorEntry {
	ers.mu.RLock()
	defer ers.mu.RUnlock()

	result := make(map[string]*ErrorEntry)
	for k, v := range ers.errorHistory {
		entryCopy := *v
		result[k] = &entryCopy
	}
	return result
}

// GetCircuitBreakerStates 获取所有熔断器状态
func (ers *ErrorRecoverySystem) GetCircuitBreakerStates() map[ErrorType]CircuitState {
	result := make(map[ErrorType]CircuitState)
	for errorType, cb := range ers.circuitBreakers {
		cb.mutex.RLock()
		result[errorType] = cb.state
		cb.mutex.RUnlock()
	}
	return result
}

// GetStatistics 获取统计信息
func (ers *ErrorRecoverySystem) GetStatistics() map[string]interface{} {
	ers.mu.RLock()
	historySize := len(ers.errorHistory)
	ers.mu.RUnlock()

	return map[string]interface{}{
		"total_errors":       atomic.LoadInt64(&ers.totalErrors),
		"recovered_errors":   atomic.LoadInt64(&ers.recoveredErrors),
		"failed_recoveries":  atomic.LoadInt64(&ers.failedRecoveries),
		"error_history_size": historySize,
		"strategies_count":   len(ers.strategies),
		"max_retries":        ers.maxRetries,
		"retry_interval":     ers.retryInterval,
		"enabled":            ers.enabled,
	}
}

// SetRetryConfig 设置重试配置
func (ers *ErrorRecoverySystem) SetRetryConfig(maxRetries int, interval time.Duration, backoffMultiple float64) {
	ers.mu.Lock()
	defer ers.mu.Unlock()

	ers.maxRetries = maxRetries
	ers.retryInterval = interval
	ers.backoffMultiple = backoffMultiple
}

// SetCircuitBreakerConfig 设置熔断器配置
func (ers *ErrorRecoverySystem) SetCircuitBreakerConfig(errorType ErrorType, failureThreshold, successThreshold int64, timeout time.Duration) {
	if cb, exists := ers.circuitBreakers[errorType]; exists {
		cb.mutex.Lock()
		cb.failureThreshold = int(failureThreshold)
		cb.successThreshold = int(successThreshold)
		cb.timeout = timeout
		cb.mutex.Unlock()
	}
}

// Enable 启用错误恢复系统
func (ers *ErrorRecoverySystem) Enable() {
	ers.mu.Lock()
	defer ers.mu.Unlock()
	ers.enabled = true
}

// Disable 禁用错误恢复系统
func (ers *ErrorRecoverySystem) Disable() {
	ers.mu.Lock()
	defer ers.mu.Unlock()
	ers.enabled = false
}

// IsEnabled 检查是否启用
func (ers *ErrorRecoverySystem) IsEnabled() bool {
	ers.mu.RLock()
	defer ers.mu.RUnlock()
	return ers.enabled
}
