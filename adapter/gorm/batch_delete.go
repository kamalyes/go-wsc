/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-16 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-17 23:22:00
 * @FilePath: \go-wsc\adapter\gorm\batch_delete.go
 * @Description: 主键分批删除 - 大表过期清理统一走小批次 autocommit 循环
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"

	"github.com/kamalyes/go-sqlbuilder/constants"
	"gorm.io/gorm"
)

// batchDeleteSize 单批删除行数上限（CRDB/PG 单事务删 1000 行毫秒级完成，
// 留足调度方 10s 上下文内跑上百批的余量）
const batchDeleteSize = 1000

// deleteInBatches 按主键分批删除满足条件的全部行，替代无界 DELETE
//
// 背景：单条 DELETE 在积压大表上必然超时——生产记录表过期清理每轮扫描命中
// 数百万行，单语句远超调度方 10s 上下文，超时回滚导致 0 行删除，积压只增不减
//
// 方案：DELETE ... WHERE id IN (SELECT id ... LIMIT ?) 循环，每批独立 autocommit
//   - 进度可保留：ctx 超时中断不回滚已提交批次，下一轮调度从断点续删，积压最终收敛
//   - 删除幂等：并发批次/CRDB TTL job 交叉删除同一行无正确性问题
//   - 冲突重试：单批是 autocommit 单语句，与 ACK 状态回报并发删同行触发 40001
//     时由 execWithSQLRetry 按退避自动重试（见 sql_retry.go 的重试安全性论证）
//
// MySQL 限制：DELETE 的 FROM 子查询不能直接引用目标表（ERROR 1093），
// 包一层派生表强制物化后绕过；PG/CockroachDB/SQLite 无此限制
//
// model 必须是以 id 为主键的硬删除模型（本适配器两张记录表均满足）
func deleteInBatches(ctx context.Context, db *gorm.DB, model interface{}, where string, args ...interface{}) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var affected int64
		err := execWithSQLRetry(ctx, func() error {
			candidates := db.WithContext(ctx).Model(model).
				Select("id").Where(where, args...).Limit(batchDeleteSize)
			if isMySQLDialect(db) {
				candidates = db.WithContext(ctx).Table("(?) as tmp", candidates).Select("id")
			}
			result := db.WithContext(ctx).Where("id IN (?)", candidates).Delete(model)
			affected = result.RowsAffected
			return result.Error
		})
		if err != nil {
			return total, err
		}
		total += affected
		if affected < int64(batchDeleteSize) {
			return total, nil
		}
	}
}

// isMySQLDialect 判断当前连接是否为 MySQL 方言
func isMySQLDialect(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && constants.IsMySQLDialector(db.Dialector.Name())
}
