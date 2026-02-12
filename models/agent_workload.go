/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-02-24 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-02-24 00:00:00
 * @FilePath: \go-wsc\models\agent_workload.go
 * @Description: 客服负载模型
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import "time"

// AgentWorkloadModel 客服负载模型（DB 持久化，支持多维度）
// 用于存储客服的工作负载信息，支持负载均衡和故障恢复
// 通过 dimension 和 time_key 字段支持多时间维度统计
type AgentWorkloadModel struct {
	ID                uint              `gorm:"primaryKey;autoIncrement;comment:自增主键" json:"id"`
	AgentID           string            `gorm:"column:agent_id;size:255;not null;index:idx_agent_dimension_time;comment:客服ID" json:"agent_id"`
	WorkloadDimension WorkloadDimension `gorm:"column:dimension;size:20;not null;default:'realtime';index:idx_agent_dimension_time;comment:统计维度" json:"dimension"`
	TimeKey           string            `gorm:"column:time_key;size:20;not null;default:'';index:idx_agent_dimension_time;comment:时间键(realtime为空,hourly如2026022513)" json:"time_key"`
	Workload          int64             `gorm:"column:workload;not null;default:0;index;comment:当前负载" json:"workload"`
	CreatedAt         time.Time         `gorm:"column:created_at;not null;comment:创建时间" json:"created_at"`
	UpdatedAt         time.Time         `gorm:"column:updated_at;not null;comment:更新时间" json:"updated_at"`
}

// TableName 表名
func (AgentWorkloadModel) TableName() string {
	return "wsc_agent_workload"
}

// TableComment 表注释
func (AgentWorkloadModel) TableComment() string {
	return "客服负载记录表-存储客服的工作负载信息(支持多维度统计)用于负载均衡和故障恢复"
}

// ============================================================================
// 多维度负载统计
// ============================================================================

// WorkloadDimension 负载统计维度
type WorkloadDimension string

// String 返回维度名称
func (d WorkloadDimension) String() string {
	return string(d)
}

const (
	WorkloadDimensionRealtime WorkloadDimension = "realtime" // 实时（无过期）
	WorkloadDimensionHourly   WorkloadDimension = "hourly"   // 小时（保留7天）
	WorkloadDimensionDaily    WorkloadDimension = "daily"    // 日（保留90天）
	WorkloadDimensionMonthly  WorkloadDimension = "monthly"  // 月（保留13个月）
	WorkloadDimensionYearly   WorkloadDimension = "yearly"   // 年（保留5年）
)

// AllWorkloadDimensions 所有维度
var AllWorkloadDimensions = []WorkloadDimension{
	WorkloadDimensionRealtime,
	WorkloadDimensionHourly,
	WorkloadDimensionDaily,
	WorkloadDimensionMonthly,
	WorkloadDimensionYearly,
}

// GetTTL 获取维度的 TTL（秒）
func (d WorkloadDimension) GetTTL() time.Duration {
	switch d {
	case WorkloadDimensionRealtime:
		return 0 // 永不过期
	case WorkloadDimensionHourly:
		return 7 * 24 * time.Hour // 7天
	case WorkloadDimensionDaily:
		return 90 * 24 * time.Hour // 90天
	case WorkloadDimensionMonthly:
		return 395 * 24 * time.Hour // 13个月
	case WorkloadDimensionYearly:
		return 5 * 365 * 24 * time.Hour // 5年
	default:
		return 0
	}
}

// FormatTimeKey 格式化时间 key
func (d WorkloadDimension) FormatTimeKey(t time.Time) string {
	switch d {
	case WorkloadDimensionHourly:
		return t.Format("2006010215") // 2026022513
	case WorkloadDimensionDaily:
		return t.Format("20060102") // 20260225
	case WorkloadDimensionMonthly:
		return t.Format("200601") // 202602
	case WorkloadDimensionYearly:
		return t.Format("2006") // 2026
	default:
		return WorkloadDimensionRealtime.String()
	}
}
