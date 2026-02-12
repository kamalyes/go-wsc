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

// AgentWorkloadModel 客服负载模型（DB 持久化）
// 用于存储客服的工作负载信息，支持负载均衡和故障恢复。
type AgentWorkloadModel struct {
	ID        uint      `gorm:"primaryKey;autoIncrement;comment:自增主键" json:"id"`
	AgentID   string    `gorm:"column:agent_id;size:255;not null;uniqueIndex;comment:客服ID" json:"agent_id"`
	Workload  int64     `gorm:"column:workload;not null;default:0;index;comment:当前负载" json:"workload"`
	CreatedAt time.Time `gorm:"column:created_at;not null;comment:创建时间" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;comment:更新时间" json:"updated_at"`
}

// TableName 表名
func (AgentWorkloadModel) TableName() string {
	return "wsc_agent_workload"
}

// TableComment 表注释
func (AgentWorkloadModel) TableComment() string {
	return "客服负载记录表-存储客服的工作负载信息用于负载均衡和故障恢复"
}
