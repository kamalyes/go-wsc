/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\models\connection_quality.go
 * @Description: 连接质量记录模型 - 与 ConnectionRecord(connect 身份) 拆分
 *
 * 拆表动机：原 ConnectionRecord 单表同时承载 connect 身份(低频，上线写/断开更新)
 * 与质量指标(高频，心跳/消息/错误 batcher 批量写)，高频写带动整行锁竞争且语义耦合。
 * 拆分后 wsc_connection_records 留 connect 身份+会话生命周期+心跳时间戳(last_ping_at/last_pong_at，
 * 属会话活性语义，随心跳批量更新落 connect 表)，wsc_connection_qualities 承载质量指标+评分，
 * 随 batcher 高频批量更新。两表通过 connection_id 1:1 关联。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"time"
)

// ConnectionQuality 连接质量记录 - 与 ConnectionRecord 1:1 关联
// 承载运行时质量指标(心跳延迟/消息统计/错误/重连)与评分，随 batcher 高频批量更新
type ConnectionQuality struct {
	// ========== 关联标识 ==========
	ID           uint64 `gorm:"primaryKey;autoIncrement;comment:自增主键" json:"id"`
	ConnectionID string `gorm:"column:connection_id;size:64;uniqueIndex;not null;comment:连接ID(与 wsc_connection_records.connection_id 1:1 关联)" json:"connection_id"`
	UserID       string `gorm:"column:user_id;size:64;index;comment:用户ID(冗余索引，便于按用户查质量)" json:"user_id"`

	// ========== 多租户/命名空间隔离 ==========
	// 与 connect 表 + Bitmap/ZSET 分桶维度一致，支持按 app+namespace 查连接质量
	// 索引名与 connect 表区分（SQLite/PostgreSQL 索引名为库级作用域，重名会建表失败）
	AppID     string `gorm:"column:app_id;size:64;index:idx_quality_app_namespace;comment:应用ID(与 connect 表一致,便于按租户查质量)" json:"app_id"`
	Namespace string `gorm:"column:namespace;size:64;index:idx_quality_app_namespace;comment:命名空间(与 connect 表+Bitmap/ZSET 分桶一致)" json:"namespace"`

	// ========== 质量指标 ==========
	ReconnectCount   int     `gorm:"column:reconnect_count;default:0;comment:重连次数" json:"reconnect_count"`
	MessagesSent     int64   `gorm:"column:messages_sent;default:0;comment:发送消息总数" json:"messages_sent"`
	MessagesReceived int64   `gorm:"column:messages_received;default:0;comment:接收消息总数" json:"messages_received"`
	BytesSent        int64   `gorm:"column:bytes_sent;default:0;comment:发送字节数" json:"bytes_sent"`
	BytesReceived    int64   `gorm:"column:bytes_received;default:0;comment:接收字节数" json:"bytes_received"`
	AveragePingMs    float64 `gorm:"column:average_ping_ms;comment:平均Ping延迟(毫秒)" json:"average_ping_ms,omitempty"`
	MaxPingMs        float64 `gorm:"column:max_ping_ms;comment:最大Ping延迟(毫秒)" json:"max_ping_ms,omitempty"`
	MinPingMs        float64 `gorm:"column:min_ping_ms;comment:最小Ping延迟(毫秒)" json:"min_ping_ms,omitempty"`
	PacketLossRate   float64 `gorm:"column:packet_loss_rate;comment:丢包率(%)" json:"packet_loss_rate,omitempty"`

	// ========== 错误信息 ==========
	ErrorCount  int        `gorm:"column:error_count;default:0;comment:错误次数" json:"error_count"`
	LastError   string     `gorm:"column:last_error;type:text;comment:最后错误信息" json:"last_error,omitempty"`
	LastErrorAt *time.Time `gorm:"column:last_error_at;comment:最后错误时间" json:"last_error_at,omitempty"`

	// ========== 评分与活跃时间 ==========
	QualityScore float64    `gorm:"column:quality_score;index;comment:连接质量评分(0-100)" json:"quality_score"`
	LastActiveAt *time.Time `gorm:"column:last_active_at;index;comment:最后活跃时间(供清理)" json:"last_active_at,omitempty"`

	// ========== 系统字段 ==========
	CreatedAt time.Time `gorm:"autoCreateTime;comment:记录创建时间" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;comment:记录更新时间" json:"updated_at"`
}

// TableName 指定表名
func (ConnectionQuality) TableName() string {
	return "wsc_connection_qualities"
}

// TableComment 表注释
func (ConnectionQuality) TableComment() string {
	return "连接质量记录表-与 wsc_connection_records 通过 connection_id 1:1 关联-承载运行时质量指标与评分"
}

// ========== 辅助方法（内存计算，batcher 路径走 SQL Expr，二者独立） ==========

// IncrementReconnect 增加重连次数
func (q *ConnectionQuality) IncrementReconnect() {
	q.ReconnectCount++
}

// AddError 添加错误记录
func (q *ConnectionQuality) AddError(err error) {
	if err != nil {
		q.ErrorCount++
		q.LastError = err.Error()
		now := time.Now()
		q.LastErrorAt = &now
	}
}

// UpdateMessageStats 更新消息统计
func (q *ConnectionQuality) UpdateMessageStats(sent, received int64) {
	q.MessagesSent += sent
	q.MessagesReceived += received
}

// UpdateBytesStats 更新字节统计
func (q *ConnectionQuality) UpdateBytesStats(sent, received int64) {
	q.BytesSent += sent
	q.BytesReceived += received
}

// UpdatePingStats 更新Ping延迟统计（简单平均，batcher 路径用 SQL 移动平均）
func (q *ConnectionQuality) UpdatePingStats(pingMs float64) {
	if q.AveragePingMs == 0 {
		q.AveragePingMs = pingMs
	} else {
		q.AveragePingMs = (q.AveragePingMs + pingMs) / 2
	}
	if q.MaxPingMs == 0 || pingMs > q.MaxPingMs {
		q.MaxPingMs = pingMs
	}
	if q.MinPingMs == 0 || pingMs < q.MinPingMs {
		q.MinPingMs = pingMs
	}
}

// ========== 评分算法 ==========

// LiveScore 在线实时评分(0-100，保底20)
// 4 维扣分：丢包(最多25)+延迟(最多25)+错误率(最多15)+重连(最多15)，合计最多扣80，保底20
// 供 batcher flush 周期性计算（连接在线时无 duration 终值）
func (q *ConnectionQuality) LiveScore() float64 {
	score := 100.0

	// 丢包率影响(最多扣25)
	if q.PacketLossRate > 0 {
		loss := q.PacketLossRate * 0.25
		if loss > 25 {
			loss = 25
		}
		score -= loss
	}

	// 延迟影响(最多扣25)
	if q.AveragePingMs > 0 {
		switch {
		case q.AveragePingMs > 500:
			score -= 25
		case q.AveragePingMs > 200:
			score -= 15
		case q.AveragePingMs > 100:
			score -= 8
		case q.AveragePingMs > 50:
			score -= 4
		}
	}

	// 错误率影响(最多扣15)
	totalMessages := q.MessagesSent + q.MessagesReceived
	if totalMessages > 0 {
		errorRate := float64(q.ErrorCount) / float64(totalMessages)
		penalty := errorRate * 15
		if penalty > 15 {
			penalty = 15
		}
		score -= penalty
	}

	// 重连次数影响(最多扣15)
	if q.ReconnectCount > 0 {
		reconnectPenalty := float64(q.ReconnectCount) * 3
		if reconnectPenalty > 15 {
			reconnectPenalty = 15
		}
		score -= reconnectPenalty
	}

	// 保底20（在线连接基础分，不归零）
	if score < 20 {
		score = 20
	}
	return score
}

// FinalScore 断开终评(0-100，可归零)
// 在 LiveScore 基础上追加时长维度(最多扣20)，供断开时终评
// 时长梯度：<30s扣20(短连接即断视为低质量)/<5min扣10/<30min扣5/≥30min不扣
func (q *ConnectionQuality) FinalScore(durationSec int64) float64 {
	score := q.LiveScore()

	// 时长维度(最多扣20)
	switch {
	case durationSec < 30:
		score -= 20
	case durationSec < 300: // 5分钟
		score -= 10
	case durationSec < 1800: // 30分钟
		score -= 5
	}

	if score < 0 {
		score = 0
	}
	return score
}
