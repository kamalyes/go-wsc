/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 01:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 17:26:00
 * @FilePath: \go-wsc\connection\record.go
 * @Description: 连接记录 —— 记录的创建与持久化（创建/落库/断开更新）
 *
 * 迁移自 hub/connection_record.go（P2 批1 域化）：记录 CRUD 重组为
 * ConnectionRecorder 组件（存储经 spi 端口注入）；欢迎消息与离线推送属
 * 上线编排（依赖 messaging 投递），留在 hub 编排层不迁移
 *
 *   - 连接记录的创建与持久化（CreateConnectionRecord/SaveConnectionRecord）
 *   - 连接质量初始行落库（SaveConnectionQuality）
 *   - 断开连接记录更新 + 质量终评（UpdateOnDisconnect）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// recordWriteTimeout 记录写入的异步任务超时
const recordWriteTimeout = 10 * time.Second

// CreateConnectionRecord 从 Client 创建连接记录
// 拆表后只填充 connect 身份+会话生命周期字段，质量指标由 SaveConnectionQuality 落到 wsc_connection_qualities
func CreateConnectionRecord(client *models.Client) *models.ConnectionRecord {
	record := &models.ConnectionRecord{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		AppID:        client.GetAppID(),
		Namespace:    client.GetNamespace(),
		NodeID:       client.NodeID,
		NodeIP:       client.NodeIP,
		NodePort:     client.NodePort,
		ClientIP:     client.GetClientIP(),
		Protocol:     client.ConnectionType,
		ClientType:   client.ClientType,
		ConnectedAt:  client.ConnectedAt,
		IsActive:     true,
	}

	// 设置 metadata（线程安全读取快照）
	record.Metadata = sqlbuilder.MapAny(client.GetMetadataSnapshot())

	return record
}

// ConnectionRecorder 连接记录持久化器（异步落库 + panic 隔离）
// records/qualities 任一未注入时对应写入静默跳过（未启用 gorm 适配器的零成本）
type ConnectionRecorder struct {
	records   spi.ConnectionStore
	qualities spi.ConnectionQualityStore
	logger    spi.Logger
}

// NewConnectionRecorder 创建连接记录持久化器
func NewConnectionRecorder(records spi.ConnectionStore, qualities spi.ConnectionQualityStore, logger spi.Logger) *ConnectionRecorder {
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &ConnectionRecorder{records: records, qualities: qualities, logger: logger}
}

// SaveConnectionRecord 保存或更新连接记录到数据库（异步）
// ctx 应为 client.Context（带 client 维度的 trace_id），实现异步保存的全链路追踪
func (r *ConnectionRecorder) SaveConnectionRecord(ctx context.Context, record *models.ConnectionRecord) {
	if r.records == nil {
		return
	}

	syncx.Go(ctx).
		WithTimeout(recordWriteTimeout).
		OnPanic(func(rcv interface{}) {
			r.logger.ErrorContextKV(ctx, "保存连接记录崩溃", "panic", rcv, "stack", string(debug.Stack()), "user_id", record.UserID)
		}).
		OnError(func(err error) {
			r.logger.ErrorContextKV(ctx, "保存连接记录失败",
				"user_id", record.UserID,
				"connection_id", record.ConnectionID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return r.records.Upsert(ctx, record)
		})
}

// SaveConnectionQuality 保存连接质量初始行到数据库（异步）
// 首次连接建零值行(QualityScore=100)，重连 reconnect_count+1（由 Upsert 内部 OnConflict 处理）
// ctx 应为 client.Context（带 client 维度的 trace_id），实现异步保存的全链路追踪
func (r *ConnectionRecorder) SaveConnectionQuality(ctx context.Context, client *models.Client) {
	if r.qualities == nil {
		return
	}

	quality := &models.ConnectionQuality{
		ConnectionID: client.ID,
		UserID:       client.UserID,
		AppID:        client.GetAppID(),
		Namespace:    client.GetNamespace(),
	}

	syncx.Go(ctx).
		WithTimeout(recordWriteTimeout).
		OnPanic(func(rcv interface{}) {
			r.logger.ErrorContextKV(ctx, "保存连接质量崩溃", "panic", rcv, "stack", string(debug.Stack()), "user_id", client.UserID)
		}).
		OnError(func(err error) {
			r.logger.ErrorContextKV(ctx, "保存连接质量失败",
				"user_id", client.UserID,
				"connection_id", client.ID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return r.qualities.Upsert(ctx, quality)
		})
}

// UpdateOnDisconnect 更新连接断开信息 + 质量终评（异步）
// 顺序：先 MarkDisconnected 写 duration/disconnected_at，再 FinalizeOnDisconnect 读 duration 算 FinalScore
// 用 client.Context 派生异步任务 ctx，保留 client 维度的 trace_id 实现全链路追踪
func (r *ConnectionRecorder) UpdateOnDisconnect(client *models.Client, reason models.DisconnectReason) {
	if r.records == nil && r.qualities == nil {
		return
	}

	syncx.Go(client.Context).
		WithTimeout(recordWriteTimeout).
		OnPanic(func(rcv interface{}) {
			r.logger.ErrorContextKV(client.Context, "更新连接断开记录崩溃", "panic", rcv, "stack", string(debug.Stack()), "user_id", client.UserID)
		}).
		OnError(func(err error) {
			r.logger.ErrorContextKV(client.Context, "更新连接断开记录失败",
				"client_id", client.ID,
				"user_id", client.UserID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			// 1. 先写 connect 表 duration/disconnected_at（终评依赖 duration）
			if r.records != nil {
				if err := r.records.MarkDisconnected(ctx, client.ID, reason, 1000); err != nil {
					return err
				}
			}
			// 2. 读 quality 行 + connect.duration，算 FinalScore 写 quality_score
			if r.qualities != nil {
				if err := r.qualities.FinalizeOnDisconnect(ctx, client.ID); err != nil {
					// 终评失败不中断（质量行可能已被清理）
					r.logger.WarnContextKV(ctx, "质量终评失败",
						"client_id", client.ID,
						"user_id", client.UserID,
						"error", err,
					)
				}
			}
			return nil
		})
}
