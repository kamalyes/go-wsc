/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 17:10:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 17:10:02
 * @FilePath: \go-wsc\adapter\gorm\batch_delete_test.go
 * @Description: 主键分批删除循环的用例
 *
 * 覆盖：
 *   - deleteInBatches：2500 行分批边界（1000/1000/500 三条 DELETE）、
 *     不足一批单语句收敛、空表零删除、WHERE 参数透传
 *   - ctx 中断保留已提交批次（断点续删语义）：首批提交后取消，
 *     返回已删进度 + ctx 错误，续跑从断点收敛
 *   - isMySQLDialect：sqlite 方言为 false、nil 防御
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqlRecorder gorm logger 桩：记录每条执行过的 SQL（观测分批循环发了几条 DELETE）
type sqlRecorder struct {
	mu       sync.Mutex
	stmts    []string
	onDelete func(stmt string) // 每条 DELETE 完成后的回调（用于中途取消 ctx）
}

func (r *sqlRecorder) LogMode(gormlogger.LogLevel) gormlogger.Interface { return r }
func (r *sqlRecorder) Info(context.Context, string, ...interface{})     {}
func (r *sqlRecorder) Warn(context.Context, string, ...interface{})     {}
func (r *sqlRecorder) Error(context.Context, string, ...interface{})    {}

func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.mu.Lock()
	r.stmts = append(r.stmts, sql)
	hook := r.onDelete
	r.mu.Unlock()
	if strings.HasPrefix(strings.ToUpper(sql), "DELETE") && hook != nil {
		hook(sql)
	}
}

// deletes 返回已记录的 DELETE 语句数
func (r *sqlRecorder) deletes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.stmts {
		if strings.HasPrefix(strings.ToUpper(s), "DELETE") {
			n++
		}
	}
	return n
}

// seedConnRecords 批量插入 n 条连接记录（CreateInBatches 攒批，避免逐行 INSERT 拖慢测试）
func seedConnRecords(t *testing.T, db *gorm.DB, n int, appID string) {
	t.Helper()
	records := make([]*models.ConnectionRecord, 0, n)
	now := time.Now()
	for i := 0; i < n; i++ {
		records = append(records, &models.ConnectionRecord{
			ConnectionID: fmt.Sprintf("conn-%s-%04d", appID, i),
			UserID:       "user-batch",
			AppID:        appID,
			NodeID:       "node-1",
			ConnectedAt:  now,
			IsActive:     true,
		})
	}
	require.NoError(t, db.CreateInBatches(records, 500).Error)
}

// TestDeleteInBatches_BatchBoundary 2500 行 → 恰好 3 条 DELETE（1000/1000/500），全部删净
func TestDeleteInBatches_BatchBoundary(t *testing.T) {
	rec := &sqlRecorder{}
	db := openTestDB(t, &gorm.Config{Logger: rec}, &models.ConnectionRecord{})
	seedConnRecords(t, db, 2500, "batch-app")

	total, err := deleteInBatches(context.Background(), db, &models.ConnectionRecord{},
		"app_id = ?", "batch-app")
	require.NoError(t, err)
	assert.Equal(t, int64(2500), total, "总删除行数应等于种子行数")
	assert.Equal(t, 3, rec.deletes(),
		"2500 行按 1000/1000/500 分批应产生 3 条独立 DELETE（每批独立 autocommit）")

	var remain int64
	require.NoError(t, db.Model(&models.ConnectionRecord{}).Where("app_id = ?", "batch-app").Count(&remain).Error)
	assert.Zero(t, remain, "删除后应无残留")
}

// TestDeleteInBatches_SingleBatch 不足一批（999 行）单条 DELETE 收敛
func TestDeleteInBatches_SingleBatch(t *testing.T) {
	rec := &sqlRecorder{}
	db := openTestDB(t, &gorm.Config{Logger: rec}, &models.ConnectionRecord{})
	seedConnRecords(t, db, 999, "small-app")

	total, err := deleteInBatches(context.Background(), db, &models.ConnectionRecord{},
		"app_id = ?", "small-app")
	require.NoError(t, err)
	assert.Equal(t, int64(999), total)
	assert.Equal(t, 1, rec.deletes(), "不足一批时应以单条 DELETE 结束循环")
}

// TestDeleteInBatches_EmptyTable 空表：单条 DELETE 影响 0 行，正常收敛
func TestDeleteInBatches_EmptyTable(t *testing.T) {
	rec := &sqlRecorder{}
	db := openTestDB(t, &gorm.Config{Logger: rec}, &models.ConnectionRecord{})

	total, err := deleteInBatches(context.Background(), db, &models.ConnectionRecord{},
		"app_id = ?", "none")
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Equal(t, 1, rec.deletes())
}

// TestDeleteInBatches_WhereArgsForwarding WHERE 条件与参数正确透传：只删命中行
func TestDeleteInBatches_WhereArgsForwarding(t *testing.T) {
	db := openTestDB(t, nil, &models.ConnectionRecord{})
	seedConnRecords(t, db, 30, "keep-app")
	seedConnRecords(t, db, 20, "drop-app")

	total, err := deleteInBatches(context.Background(), db, &models.ConnectionRecord{},
		"app_id = ?", "drop-app")
	require.NoError(t, err)
	assert.Equal(t, int64(20), total)

	var remain int64
	require.NoError(t, db.Model(&models.ConnectionRecord{}).Where("app_id = ?", "keep-app").Count(&remain).Error)
	assert.Equal(t, int64(30), remain, "未命中 WHERE 的行不应被删除")
}

// TestDeleteInBatches_CtxInterruptKeepsProgress ctx 中断保留已提交批次（断点续删语义）
//
// 首批 1000 行 DELETE 完成后取消 ctx：已提交的 1000 行保留（不回滚），
// 返回 (1000, ctx.Err())；换新 ctx 续跑应从断点删净剩余 1500 行
func TestDeleteInBatches_CtxInterruptKeepsProgress(t *testing.T) {
	rec := &sqlRecorder{}
	db := openTestDB(t, &gorm.Config{Logger: rec}, &models.ConnectionRecord{})
	seedConnRecords(t, db, 2500, "resume-app")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec.onDelete = func(string) { cancel() } // 首批 DELETE 完成即取消

	total, err := deleteInBatches(ctx, db, &models.ConnectionRecord{},
		"app_id = ?", "resume-app")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(batchDeleteSize), total,
		"首批提交后中断，应返回已提交的 %d 行进度", batchDeleteSize)
	assert.Equal(t, 1, rec.deletes(), "取消后不应继续发起新一批删除")

	var remain int64
	require.NoError(t, db.Model(&models.ConnectionRecord{}).Where("app_id = ?", "resume-app").Count(&remain).Error)
	assert.Equal(t, int64(1500), remain, "已提交批次不应回滚，剩余 1500 行待下轮调度")

	// 断点续删：新 ctx 从当前积压继续，最终收敛到 0
	resumed, err := deleteInBatches(context.Background(), db, &models.ConnectionRecord{},
		"app_id = ?", "resume-app")
	require.NoError(t, err)
	assert.Equal(t, int64(1500), resumed, "续删应恰好清理剩余积压")
	require.NoError(t, db.Model(&models.ConnectionRecord{}).Where("app_id = ?", "resume-app").Count(&remain).Error)
	assert.Zero(t, remain)
}

// TestDeleteInBatches_CtxCancelledBeforeStart ctx 已取消：循环入口即退出，进度为 0
func TestDeleteInBatches_CtxCancelledBeforeStart(t *testing.T) {
	rec := &sqlRecorder{}
	db := openTestDB(t, &gorm.Config{Logger: rec}, &models.ConnectionRecord{})
	seedConnRecords(t, db, 10, "pre-app")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	total, err := deleteInBatches(ctx, db, &models.ConnectionRecord{}, "app_id = ?", "pre-app")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, total, "入口取消时不应有已提交进度")
	assert.Zero(t, rec.deletes(), "循环入口短路，不应发出任何 DELETE")
}

// TestIsMySQLDialect 方言判定：sqlite 上为 false，nil 防御不 panic
func TestIsMySQLDialect(t *testing.T) {
	db := openTestDB(t, nil, &models.ConnectionRecord{})
	assert.False(t, isMySQLDialect(db), "sqlite 方言不应走 MySQL 派生表包装分支")
	assert.False(t, isMySQLDialect(nil), "nil db 防御应返回 false")
	assert.False(t, isMySQLDialect(&gorm.DB{Config: &gorm.Config{}}), "Dialector 为 nil 应返回 false")
}
