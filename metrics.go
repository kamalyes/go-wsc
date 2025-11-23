/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 20:45:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 20:45:00
 * @FilePath: \go-wsc\metrics.go
 * @Description: 高级性能指标收集和监控系统
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// AdvancedMetrics 高级指标结构
type AdvancedMetrics struct {
	// 连接指标
	TotalConnections  int64   `json:"total_connections"`
	ActiveConnections int64   `json:"active_connections"`
	PeakConnections   int64   `json:"peak_connections"`
	ConnectionErrors  int64   `json:"connection_errors"`
	AverageLatency    float64 `json:"average_latency_ms"`
	LatencyP95        float64 `json:"latency_p95_ms"`

	// 消息指标
	MessagesSent     int64   `json:"messages_sent"`
	MessagesReceived int64   `json:"messages_received"`
	MessageErrors    int64   `json:"message_errors"`
	BytesSent        int64   `json:"bytes_sent"`
	BytesReceived    int64   `json:"bytes_received"`
	MessageRate      float64 `json:"message_rate_per_sec"`

	// 队列指标
	QueueDepth          int64   `json:"queue_depth"`
	QueueOverflows      int64   `json:"queue_overflows"`
	QueueProcessingTime float64 `json:"queue_processing_time_ms"`

	// 系统资源指标
	MemoryUsage    int64   `json:"memory_usage_bytes"`
	GoroutineCount int64   `json:"goroutine_count"`
	GCPause        float64 `json:"gc_pause_ms"`
	CPUUsage       float64 `json:"cpu_usage_percent"`

	// 时间戳
	CollectedAt time.Time `json:"collected_at"`
}

// MetricsCollector 指标收集器
type MetricsCollector struct {
	mu       sync.RWMutex
	hub      *Hub
	enabled  bool
	interval time.Duration

	// 计数器
	totalConnections int64
	messagesSent     int64
	messagesReceived int64
	messageErrors    int64
	bytesSent        int64
	bytesReceived    int64
	connectionErrors int64
	queueOverflows   int64

	// 延迟追踪
	latencies     []float64
	latencyWindow int
	latencyIndex  int

	// 历史数据
	history    []AdvancedMetrics
	maxHistory int

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMetricsCollector 创建指标收集器
func NewMetricsCollector(hub *Hub, interval time.Duration) *MetricsCollector {
	ctx, cancel := context.WithCancel(context.Background())
	return &MetricsCollector{
		hub:           hub,
		enabled:       true,
		interval:      interval,
		latencies:     make([]float64, 1000), // 保存最近1000个延迟样本
		latencyWindow: 1000,
		history:       make([]AdvancedMetrics, 0, 100), // 保存最近100个历史记录
		maxHistory:    100,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
}

// Start 启动指标收集
func (mc *MetricsCollector) Start() {
	if !mc.enabled {
		return
	}

	go mc.collectLoop()

	if mc.hub.logger != nil {
		mc.hub.logger.InfoKV("性能指标收集器已启动",
			"interval", mc.interval,
			"latency_window", mc.latencyWindow,
			"history_size", mc.maxHistory,
		)
	}
}

// Stop 停止指标收集
func (mc *MetricsCollector) Stop() {
	if mc.cancel != nil {
		mc.cancel()
	}

	// 强制等待，最多1秒
	select {
	case <-mc.done:
	case <-time.After(1 * time.Second):
		// 超时保护，强制继续
	}

	if mc.hub.logger != nil {
		mc.hub.logger.Info("性能指标收集器已停止")
	}
}

// collectLoop 收集循环
func (mc *MetricsCollector) collectLoop() {
	defer close(mc.done)

	ticker := time.NewTicker(mc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-mc.ctx.Done():
			return
		case <-ticker.C:
			// 添加超时处理，避免阻塞shutdown
			done := make(chan bool, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						// 忽略panic，确保不会阻塞
					}
				}()
				metrics := mc.collectMetrics()
				mc.storeMetrics(metrics)
				mc.reportMetrics(metrics)
				done <- true
			}()

			// 2秒超时，之后继续下一轮或退出
			select {
			case <-done:
				// 正常完成
			case <-time.After(2 * time.Second):
				// 超时，继续下一轮
			case <-mc.ctx.Done():
				return
			}
		}
	}
}

// collectMetrics 收集当前指标
func (mc *MetricsCollector) collectMetrics() AdvancedMetrics {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	// 获取运行时统计
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// 计算连接指标
	activeConns := mc.hub.activeConnections.Load()
	peakConns := mc.hub.totalConnections.Load() // 使用总连接数作为峰值的近似值

	// 计算延迟指标
	avgLatency, p95Latency := mc.calculateLatencyStats()

	// 计算消息速率
	messageRate := mc.calculateMessageRate()

	// 收集队列指标
	queueDepth := mc.getQueueDepth()

	return AdvancedMetrics{
		TotalConnections:  atomic.LoadInt64(&mc.totalConnections),
		ActiveConnections: int64(activeConns),
		PeakConnections:   peakConns,
		ConnectionErrors:  atomic.LoadInt64(&mc.connectionErrors),
		AverageLatency:    avgLatency,
		LatencyP95:        p95Latency,

		MessagesSent:     atomic.LoadInt64(&mc.messagesSent),
		MessagesReceived: atomic.LoadInt64(&mc.messagesReceived),
		MessageErrors:    atomic.LoadInt64(&mc.messageErrors),
		BytesSent:        atomic.LoadInt64(&mc.bytesSent),
		BytesReceived:    atomic.LoadInt64(&mc.bytesReceived),
		MessageRate:      messageRate,

		QueueDepth:          queueDepth,
		QueueOverflows:      atomic.LoadInt64(&mc.queueOverflows),
		QueueProcessingTime: mc.getQueueProcessingTime(),

		MemoryUsage:    int64(m.Alloc),
		GoroutineCount: int64(runtime.NumGoroutine()),
		GCPause:        float64(m.PauseNs[(m.NumGC+255)%256]) / 1e6, // 转换为毫秒
		CPUUsage:       mc.getCPUUsage(),

		CollectedAt: time.Now(),
	}
}

// calculateLatencyStats 计算延迟统计
func (mc *MetricsCollector) calculateLatencyStats() (avg, p95 float64) {
	validLatencies := make([]float64, 0, mc.latencyWindow)

	for _, latency := range mc.latencies {
		if latency > 0 {
			validLatencies = append(validLatencies, latency)
		}
	}

	if len(validLatencies) == 0 {
		return 0, 0
	}

	// 计算平均延迟
	var sum float64
	for _, latency := range validLatencies {
		sum += latency
	}
	avg = sum / float64(len(validLatencies))

	// 计算P95延迟（简化实现）
	if len(validLatencies) >= 20 {
		// 排序并取95%位置的值
		sorted := make([]float64, len(validLatencies))
		copy(sorted, validLatencies)

		// 简单的冒泡排序（对于小数据集足够）
		for i := 0; i < len(sorted)-1; i++ {
			for j := 0; j < len(sorted)-i-1; j++ {
				if sorted[j] > sorted[j+1] {
					sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
				}
			}
		}

		p95Index := int(float64(len(sorted)) * 0.95)
		if p95Index >= len(sorted) {
			p95Index = len(sorted) - 1
		}
		p95 = sorted[p95Index]
	} else {
		p95 = avg // 数据不足时使用平均值
	}

	return
}

// calculateMessageRate 计算消息速率
func (mc *MetricsCollector) calculateMessageRate() float64 {
	if len(mc.history) < 2 {
		return 0
	}

	// 使用最近两个历史记录计算速率
	latest := mc.history[len(mc.history)-1]
	previous := mc.history[len(mc.history)-2]

	timeDiff := latest.CollectedAt.Sub(previous.CollectedAt).Seconds()
	if timeDiff <= 0 {
		return 0
	}

	messageDiff := latest.MessagesSent - previous.MessagesSent
	return float64(messageDiff) / timeDiff
}

// getQueueDepth 获取队列深度
func (mc *MetricsCollector) getQueueDepth() int64 {
	// 这里需要根据实际的队列实现来获取深度
	// 暂时返回0，实际实现需要访问Hub的队列状态
	return 0
}

// getQueueProcessingTime 获取队列处理时间
func (mc *MetricsCollector) getQueueProcessingTime() float64 {
	// 这里需要根据实际的队列处理时间统计来实现
	// 暂时返回0，实际实现需要在队列处理时测量时间
	return 0
}

// getCPUUsage 获取CPU使用率
func (mc *MetricsCollector) getCPUUsage() float64 {
	// 这是一个简化的CPU使用率估算
	// 实际生产环境中可能需要使用更精确的方法
	return 0
}

// storeMetrics 存储指标到历史记录
func (mc *MetricsCollector) storeMetrics(metrics AdvancedMetrics) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.history = append(mc.history, metrics)

	// 保持历史记录数量在限制内
	if len(mc.history) > mc.maxHistory {
		mc.history = mc.history[1:]
	}
}

// reportMetrics 报告指标
func (mc *MetricsCollector) reportMetrics(metrics AdvancedMetrics) {
	if mc.hub.logger == nil {
		return
	}

	// 只在值得报告时记录（例如，有活动连接时）
	if metrics.ActiveConnections > 0 {
		mc.hub.logger.LogPerformance("system_metrics",
			mc.interval.String(),
			map[string]interface{}{
				"active_connections": metrics.ActiveConnections,
				"peak_connections":   metrics.PeakConnections,
				"messages_sent":      metrics.MessagesSent,
				"message_rate":       metrics.MessageRate,
				"memory_usage_mb":    metrics.MemoryUsage / 1024 / 1024,
				"goroutines":         metrics.GoroutineCount,
				"average_latency_ms": metrics.AverageLatency,
				"p95_latency_ms":     metrics.LatencyP95,
			})
	}
}

// RecordLatency 记录延迟
func (mc *MetricsCollector) RecordLatency(latency time.Duration) {
	if !mc.enabled {
		return
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	latencyMs := float64(latency.Nanoseconds()) / 1e6
	mc.latencies[mc.latencyIndex] = latencyMs
	mc.latencyIndex = (mc.latencyIndex + 1) % mc.latencyWindow
}

// IncrementMessageSent 增加已发送消息计数
func (mc *MetricsCollector) IncrementMessageSent(bytes int) {
	if mc.enabled {
		atomic.AddInt64(&mc.messagesSent, 1)
		atomic.AddInt64(&mc.bytesSent, int64(bytes))
	}
}

// IncrementMessageReceived 增加已接收消息计数
func (mc *MetricsCollector) IncrementMessageReceived(bytes int) {
	if mc.enabled {
		atomic.AddInt64(&mc.messagesReceived, 1)
		atomic.AddInt64(&mc.bytesReceived, int64(bytes))
	}
}

// IncrementMessageError 增加消息错误计数
func (mc *MetricsCollector) IncrementMessageError() {
	if mc.enabled {
		atomic.AddInt64(&mc.messageErrors, 1)
	}
}

// IncrementConnectionError 增加连接错误计数
func (mc *MetricsCollector) IncrementConnectionError() {
	if mc.enabled {
		atomic.AddInt64(&mc.connectionErrors, 1)
	}
}

// IncrementQueueOverflow 增加队列溢出计数
func (mc *MetricsCollector) IncrementQueueOverflow() {
	if mc.enabled {
		atomic.AddInt64(&mc.queueOverflows, 1)
	}
}

// GetCurrentMetrics 获取当前指标
func (mc *MetricsCollector) GetCurrentMetrics() AdvancedMetrics {
	return mc.collectMetrics()
}

// GetHistory 获取历史指标
func (mc *MetricsCollector) GetHistory() []AdvancedMetrics {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	// 返回副本以避免并发问题
	history := make([]AdvancedMetrics, len(mc.history))
	copy(history, mc.history)
	return history
}

// Enable 启用指标收集
func (mc *MetricsCollector) Enable() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.enabled = true
}

// Disable 禁用指标收集
func (mc *MetricsCollector) Disable() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.enabled = false
}

// IsEnabled 检查是否启用
func (mc *MetricsCollector) IsEnabled() bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.enabled
}
