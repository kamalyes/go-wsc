/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 00:00:00
 * @FilePath: \go-wsc\repository\sql_retry.go
 * @Description: 数据库冲突错误（SQLSTATE 40001/40P01）的驱动无关识别与自动重试
 *
 * 背景（CockroachDB Serializable 隔离的固有行为）：
 *   CRDB 默认 SERIALIZABLE 隔离，两个事务并发读写同一行且时序交叉时，
 *   数据库主动 abort 其中一个并返回 SQLSTATE 40001（RETRY_SERIALIZABLE），
 *   要求客户端重试整个事务——这不是故障，是协议约定。
 *   典型冲突点：本库的 ClaimStaleSending（ACK 超时认领，sending→ack_timeout）
 *   与目标节点的状态回报（sending→success）并发更新同一行记录。
 *   未处理 40001 的后果：状态更新静默丢失，记录停留 sending，
 *   30s 后被误判 ack_timeout 并转存离线 → 用户上线收到重复推送。
 *
 * 重试安全性：仅包裹 autocommit 单语句（无外层事务），
 *   语句失败不会污染会话状态，重试等价于重新提交，幂等安全。

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jpillora/backoff"
)

// sqlStateErrorer 驱动无关的 SQLSTATE 错误接口
// lib/pq 的 *pq.Error 与 pgx 的 *pgconn.PgError 均实现该方法，
// 无需在库内引入具体驱动依赖即可完成识别
type sqlStateErrorer interface {
	SQLState() string
}

// sqlRetryMaxAttempts 单条语句的最大尝试次数（含首次执行）
const sqlRetryMaxAttempts = 3

// newSQLRetryBackoff 创建重试退避策略
// 冲突窗口极短（并发事务的毫秒级交叉），50ms~200ms 的抖动指数退避足够错峰；
// jpillora/backoff 非并发安全，每次重试独立创建实例
func newSQLRetryBackoff() *backoff.Backoff {
	return &backoff.Backoff{
		Min:    50 * time.Millisecond,
		Max:    200 * time.Millisecond,
		Factor: 2,
		Jitter: true,
	}
}

// isRetryableSQLState 判断错误是否为数据库要求客户端重试的冲突
// 40001：CRDB/PG SERIALIZABLE 冲突（TransactionRetryError）
// 40P01：PG 死锁检测
func isRetryableSQLState(err error) bool {
	var se sqlStateErrorer
	if errors.As(err, &se) {
		switch se.SQLState() {
		case "40001", "40P01":
			return true
		}
	}
	return false
}

// execWithSQLRetry 执行数据库写操作，遇到冲突类错误时按指数退避自动重试
// fn 必须是 autocommit 单语句（内部自行完成完整的执行与错误返回）
// 重试耗尽后返回最后一次的错误，由调用方记录日志
func execWithSQLRetry(ctx context.Context, fn func() error) error {
	b := newSQLRetryBackoff()
	var err error
	for attempt := 0; attempt < sqlRetryMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(b.Duration()):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err = fn(); err == nil || !isRetryableSQLState(err) {
			return err
		}
	}
	return err
}
