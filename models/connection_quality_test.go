/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\models\connection_quality_test.go
 * @Description: ConnectionQuality 模型测试
 *
 * 覆盖：
 *   - LiveScore 在线实时评分（4 维扣分：丢包/延迟/错误率/重连，保底 20）
 *   - FinalScore 断开终评（LiveScore + 时长维度，可归零）
 *   - 辅助方法 IncrementReconnect/AddError/UpdateMessageStats/UpdateBytesStats/UpdatePingStats
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestConnectionQuality_LiveScore_Default 零值连接基础分应为 100
func TestConnectionQuality_LiveScore_Default(t *testing.T) {
	q := &ConnectionQuality{}
	assert.Equal(t, 100.0, q.LiveScore(), "零值连接 LiveScore 应为满分 100")
}

// TestConnectionQuality_LiveScore_Floor 保底 20 分
// 极端丢包+高延迟+高错误率+频繁重连合计最多扣 80，保底 20
func TestConnectionQuality_LiveScore_Floor(t *testing.T) {
	q := &ConnectionQuality{
		PacketLossRate:   100, // 丢包 100% → 扣满 25
		AveragePingMs:    600, // 延迟 >500 → 扣满 25
		ErrorCount:       100,
		MessagesSent:     10, // 错误率 100/10 → 扣满 15
		MessagesReceived: 0,
		ReconnectCount:   100, // 重连 → 扣满 15
	}
	score := q.LiveScore()
	assert.Equal(t, 20.0, score, "极端连接 LiveScore 应保底 20，实际: %f", score)
}

// TestConnectionQuality_LiveScore_PacketLoss 丢包率维度扣分（最多 25）
func TestConnectionQuality_LiveScore_PacketLoss(t *testing.T) {
	tests := []struct {
		name     string
		lossRate float64
		want     float64
	}{
		{"无丢包不扣分", 0, 100},
		{"丢包 10% 扣 2.5", 10, 97.5},
		{"丢包 100% 扣满 25", 100, 75},
		{"丢包 200% 仍只扣 25", 200, 75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &ConnectionQuality{PacketLossRate: tt.lossRate}
			assert.Equal(t, tt.want, q.LiveScore())
		})
	}
}

// TestConnectionQuality_LiveScore_Latency 延迟维度扣分（最多 25）
func TestConnectionQuality_LiveScore_Latency(t *testing.T) {
	tests := []struct {
		name   string
		pingMs float64
		want   float64
	}{
		{"无延迟不扣分", 0, 100},
		{"延迟 60 扣 4", 60, 96},
		{"延迟 150 扣 8", 150, 92},
		{"延迟 300 扣 15", 300, 85},
		{"延迟 600 扣满 25", 600, 75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &ConnectionQuality{AveragePingMs: tt.pingMs}
			assert.Equal(t, tt.want, q.LiveScore())
		})
	}
}

// TestConnectionQuality_LiveScore_ErrorRate 错误率维度扣分（最多 15）
func TestConnectionQuality_LiveScore_ErrorRate(t *testing.T) {
	tests := []struct {
		name             string
		errorCount       int
		messagesSent     int64
		messagesReceived int64
		want             float64
	}{
		{"无消息不扣分(分母为0)", 5, 0, 0, 100},
		{"错误率 10% 扣 1.5", 1, 10, 0, 98.5},
		{"错误率 100% 扣满 15", 10, 10, 0, 85},
		{"错误率超 100% 仍只扣 15", 100, 10, 0, 85},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &ConnectionQuality{
				ErrorCount:       tt.errorCount,
				MessagesSent:     tt.messagesSent,
				MessagesReceived: tt.messagesReceived,
			}
			assert.Equal(t, tt.want, q.LiveScore())
		})
	}
}

// TestConnectionQuality_LiveScore_Reconnect 重连次数维度扣分（最多 15，每次扣 3）
func TestConnectionQuality_LiveScore_Reconnect(t *testing.T) {
	tests := []struct {
		name    string
		reconns int
		want    float64
	}{
		{"无重连不扣分", 0, 100},
		{"重连 1 次扣 3", 1, 97},
		{"重连 5 次扣满 15", 5, 85},
		{"重连 100 次仍只扣 15", 100, 85},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &ConnectionQuality{ReconnectCount: tt.reconns}
			assert.Equal(t, tt.want, q.LiveScore())
		})
	}
}

// TestConnectionQuality_FinalScore 时长维度追加扣分（最多 20，可归零）
func TestConnectionQuality_FinalScore(t *testing.T) {
	tests := []struct {
		name        string
		durationSec int64
		want        float64
	}{
		{"<30s 短连接扣 20", 10, 80},
		{"<5min 扣 10", 200, 90},
		{"<30min 扣 5", 1000, 95},
		{">=30min 不扣", 1800, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &ConnectionQuality{}
			assert.Equal(t, tt.want, q.FinalScore(tt.durationSec))
		})
	}
}

// TestConnectionQuality_FinalScore_Zero 可归零（与 LiveScore 保底 20 不同）
func TestConnectionQuality_FinalScore_Zero(t *testing.T) {
	q := &ConnectionQuality{
		PacketLossRate: 100, // LiveScore 保底 20
		AveragePingMs:  600,
		ErrorCount:     100,
		MessagesSent:   10,
		ReconnectCount: 100,
	}
	// LiveScore=20，时长<30s 再扣 20 → 0
	assert.Equal(t, 0.0, q.FinalScore(10), "FinalScore 可归零")
}

// TestConnectionQuality_IncrementReconnect 增加重连次数
func TestConnectionQuality_IncrementReconnect(t *testing.T) {
	q := &ConnectionQuality{}
	assert.Equal(t, 0, q.ReconnectCount)
	q.IncrementReconnect()
	q.IncrementReconnect()
	assert.Equal(t, 2, q.ReconnectCount)
}

// TestConnectionQuality_AddError 添加错误记录
func TestConnectionQuality_AddError(t *testing.T) {
	q := &ConnectionQuality{}
	q.AddError(nil) // nil 不记录
	assert.Equal(t, 0, q.ErrorCount)
	assert.Equal(t, "", q.LastError)
	assert.Nil(t, q.LastErrorAt)

	q.AddError(assert.AnError)
	assert.Equal(t, 1, q.ErrorCount)
	assert.Equal(t, assert.AnError.Error(), q.LastError)
	assert.NotNil(t, q.LastErrorAt)
}

// TestConnectionQuality_UpdateMessageStats 更新消息统计
func TestConnectionQuality_UpdateMessageStats(t *testing.T) {
	q := &ConnectionQuality{}
	q.UpdateMessageStats(5, 3)
	assert.Equal(t, int64(5), q.MessagesSent)
	assert.Equal(t, int64(3), q.MessagesReceived)

	q.UpdateMessageStats(2, 7)
	assert.Equal(t, int64(7), q.MessagesSent)
	assert.Equal(t, int64(10), q.MessagesReceived)
}

// TestConnectionQuality_UpdateBytesStats 更新字节统计
func TestConnectionQuality_UpdateBytesStats(t *testing.T) {
	q := &ConnectionQuality{}
	q.UpdateBytesStats(100, 200)
	assert.Equal(t, int64(100), q.BytesSent)
	assert.Equal(t, int64(200), q.BytesReceived)

	q.UpdateBytesStats(50, 300)
	assert.Equal(t, int64(150), q.BytesSent)
	assert.Equal(t, int64(500), q.BytesReceived)
}

// TestConnectionQuality_UpdatePingStats 更新Ping延迟统计
func TestConnectionQuality_UpdatePingStats(t *testing.T) {
	q := &ConnectionQuality{}

	// 首次：直接赋值
	q.UpdatePingStats(50)
	assert.Equal(t, 50.0, q.AveragePingMs)
	assert.Equal(t, 50.0, q.MaxPingMs)
	assert.Equal(t, 50.0, q.MinPingMs)

	// 第二次：平均 + 更新 max/min
	q.UpdatePingStats(100)
	assert.Equal(t, 75.0, q.AveragePingMs) // (50+100)/2
	assert.Equal(t, 100.0, q.MaxPingMs)
	assert.Equal(t, 50.0, q.MinPingMs)

	// 第三次：min 更新
	q.UpdatePingStats(20)
	// 平均 = (75 + 20) / 2 = 47.5
	assert.Equal(t, 47.5, q.AveragePingMs)
	assert.Equal(t, 100.0, q.MaxPingMs)
	assert.Equal(t, 20.0, q.MinPingMs)
}

// TestConnectionQuality_TableName 表名正确
func TestConnectionQuality_TableName(t *testing.T) {
	assert.Equal(t, "wsc_connection_qualities", ConnectionQuality{}.TableName())
}

// TestConnectionQuality_TableComment 表注释非空
func TestConnectionQuality_TableComment(t *testing.T) {
	assert.NotEmpty(t, ConnectionQuality{}.TableComment())
}
