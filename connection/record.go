/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:08:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 19:08:00
 * @FilePath: \go-wsc\connection\record.go
 * @Description: 连接域连接记录管理器 —— 记录构造 / 异步落库 / 停机批量终态
 *
 * 从 hub/registry.go 域化下沉：连接记录是连接的持久化影子（何时来、何时走、
 * 从哪来），构造与落库语义归连接域；仓储经 RecordHost 端口动态读取，
 * 支持编排层运行期注入（构造时未注入则各方法 no-op 降级）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-toolbox/pkg/syncx"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// RecordManager 连接记录管理器（连接域）
//
// 覆盖连接记录的构造（Client → ConnectionRecord 快照）与终态写路径
// （注册异步保存 / 注销异步标记断开 / 停机批量终态）
type RecordManager struct {
	host   RecordHost
	logger spi.Logger
}

// NewRecordManager 构造连接记录管理器
func NewRecordManager(host RecordHost) *RecordManager {
	logger := host.GetLogger()
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &RecordManager{host: host, logger: logger}
}

// store 返回连接记录仓储（未注入时 nil，调用方 no-op 降级）
func (m *RecordManager) store() spi.ConnectionStore {
	return m.host.GetConnectionRecordRepo()
}

// qualityStore 返回连接质量仓储（未注入时 nil，调用方 no-op 降级）
func (m *RecordManager) qualityStore() spi.ConnectionQualityStore {
	return m.host.GetConnectionQualityRepository()
}

// Create 构造连接记录（内存对象，供异步保存 + 连接回调使用）
func (m *RecordManager) Create(client *models.Client) *models.ConnectionRecord {
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

// qualitySeedFrom 从连接记录派生质量初始行：quality 表随 connect 行 1:1 同生，
// 初始零值指标 + QualityScore=100 + LastActiveAt 由仓储 Upsert 兜底；
// 重连（同 connection_id）时 ON CONFLICT 递增 reconnect_count 并刷新
// last_active_at/user_id，统计列不重置（与 connect 表重连语义对齐）
func qualitySeedFrom(record *models.ConnectionRecord) *models.ConnectionQuality {
	return &models.ConnectionQuality{
		ConnectionID: record.ConnectionID,
		UserID:       record.UserID,
		AppID:        record.AppID,
		Namespace:    record.Namespace,
	}
}

// Save 保存或更新连接记录到数据库（仓储未注入时 no-op）
// ctx 应为 client.Context（带 client 维度的 trace_id），实现异步保存的全链路追踪
// connect 身份行与 quality 初始行同批落库：batcher 的心跳/统计/错误批量 UPDATE
// 以 quality 行存在为前提，无初始行则全部空转影响 0 行（曾致质量表长期零数据）
func (m *RecordManager) Save(ctx context.Context, record *models.ConnectionRecord) {
	if m.store() == nil {
		return
	}
	syncx.Go(ctx).
		WithTimeout(10 * time.Second).
		OnError(func(err error) {
			m.logger.WarnContextKV(ctx, "保存连接记录失败",
				"connection_id", record.ConnectionID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			// 两行互不阻断：connect 失败时 quality 仍尝试写入（下次重连自愈），
			// 错误合并上报（warn 日志带 connection_id 可定位）
			errConnect := m.store().Upsert(ctx, record)
			var errQuality error
			if qs := m.qualityStore(); qs != nil {
				errQuality = qs.Upsert(ctx, qualitySeedFrom(record))
			}
			return errors.Join(errConnect, errQuality)
		})
}

// disconnectionShutdownChunkSize 停机批量直调的单块上限
// 每条 entry 展开 11 个 SQL 参数（5 列 CASE WHEN 各 2 + IN 1），500 条即 5.5k
// 参数，控制在三方言占位符上限内（PG 系 65535，MySQL max_allowed_packet 语境下亦安全）
const disconnectionShutdownChunkSize = 500

// disconnectionShutdownWorkers 停机批量直调的并行块数
// 串行 26w 连接 = 520 块单线程耗时贴 grace 边缘，与 hub 停机清理共用 8 并发
const disconnectionShutdownWorkers = 8

// MarkDisconnected 标记连接为已断开（攒批路径，batcher 未注入时 no-op）
// 构造 DisconnectionEntry 快照提交到 DisconnectionBatcher，由后台攒批合并为
// CASE WHEN 单 SQL 落库；快照在断连瞬间冻结 DisconnectedAt/ConnectedAt，
// flush 延迟不虚增 duration；队列满丢弃与原记录池可丢弃语义一致
func (m *RecordManager) MarkDisconnected(client *models.Client) {
	batcher := m.host.GetDisconnectionBatcher()
	if batcher == nil {
		return
	}
	if !batcher.Submit(&models.DisconnectionEntry{
		ConnectionID:   client.ID,
		ConnectedAt:    client.ConnectedAt,
		DisconnectedAt: time.Now(),
		Reason:         models.DisconnectReasonClientRequest,
	}) {
		m.logger.DebugKV("断连终态提交丢弃（攒批队列满）",
			"connection_id", client.ID,
		)
	}
}

// MarkDisconnectedBatch 停机批量标记连接断开（仓储未注入时返回 nil）
// 与单连接路径的差异：reason 为 ServerShutdown + 关闭码 1001（客户端据此识别服务端主动离开并重连）；
// 不走攒批队列而是分块同步直调 BatchMarkDisconnected——
// 停机路径需在本方法返回后立即被批量终评读 duration 算分，保序优先
// 返回构造的 entries 供终评直接复用 duration（与 SQL 写入值同源同公式），免去终评再把刚落库的 duration 读回来的往返
func (m *RecordManager) MarkDisconnectedBatch(clients []*models.Client) []*models.DisconnectionEntry {
	if m.store() == nil || len(clients) == 0 {
		return nil
	}
	now := time.Now()
	entries := make([]*models.DisconnectionEntry, len(clients))
	for i, client := range clients {
		entries[i] = &models.DisconnectionEntry{
			ConnectionID:   client.ID,
			ConnectedAt:    client.ConnectedAt,
			DisconnectedAt: now,
			Reason:         models.DisconnectReasonServerShutdown,
			Code:           1001,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, disconnectionShutdownWorkers)
	for start := 0; start < len(entries); start += disconnectionShutdownChunkSize {
		end := min(start+disconnectionShutdownChunkSize, len(entries))
		chunk := entries[start:end]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := m.store().BatchMarkDisconnected(ctx, chunk); err != nil {
				m.logger.WarnContextKV(ctx, "shutdown: 批量标记连接断开失败",
					"chunk_size", len(chunk),
					"error", err,
				)
			}
		}()
	}
	wg.Wait()
	return entries
}
