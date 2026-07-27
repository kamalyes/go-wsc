/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 13:55:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 19:05:29
 * @FilePath: \go-wsc\base_connect_setup.go
 * @Description: 测试连接配置 - 统一管理 Redis 和 MySQL 连接
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/glebarez/sqlite"
	dbMigrator "github.com/kamalyes/go-sqlbuilder/db"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ============================================================================
// Redis 连接配置（基于 miniredis 本地内存实例，零外部依赖）
// ============================================================================

var (
	// 单例 Redis 客户端（用于需要持久连接的测试）
	testRedisInstance *redis.Client
	testRedisOnce     sync.Once
	// miniredis 实例引用，必须持有否则被 GC 后连接失效
	testMiniRedis *miniredis.Miniredis
)

// GetTestRedisClient 获取测试用 Redis 客户端（单例模式）
// 基于 miniredis 本地内存实例，零外部依赖，无需连接真实 Redis
func GetTestRedisClient(t *testing.T) *redis.Client {
	testRedisOnce.Do(func() {
		mr, err := miniredis.Run()
		require.NoError(t, err, "启动 miniredis 失败")
		testMiniRedis = mr

		testRedisInstance = redis.NewClient(&redis.Options{
			Addr: mr.Addr(),
		})

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, testRedisInstance.Ping(ctx).Err(), "miniredis ping 失败")
		t.Logf("📌 使用 miniredis 本地实例: %s", mr.Addr())
	})
	if testRedisInstance == nil {
		t.Fatal("Redis 单例未正确初始化")
	}
	return testRedisInstance
}

// GetTestRedisClientWithFlush 获取测试用 Redis 客户端并清空测试数据
// 适用于需要干净环境的测试
func GetTestRedisClientWithFlush(t *testing.T) *redis.Client {
	client := GetTestRedisClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 清空测试数据库
	err := client.FlushDB(ctx).Err()
	require.NoError(t, err, "清空 Redis 测试数据失败")

	return client
}

// NewTestRedisClient 创建新的 Redis 客户端（连同一 miniredis 实例的独立连接）
// 适用于需要独立连接的测试
func NewTestRedisClient(t *testing.T) *redis.Client {
	// 确保 miniredis 已启动
	if testMiniRedis == nil {
		GetTestRedisClient(t)
	}

	client := redis.NewClient(&redis.Options{Addr: testMiniRedis.Addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.Ping(ctx).Err()
	require.NoError(t, err, "miniredis 连接失败")

	// 清空测试数据
	_ = client.FlushDB(ctx).Err()

	return client
}

// GetTestRedisUniversalClient 获取 Redis UniversalClient（兼容旧代码）
func GetTestRedisUniversalClient(t *testing.T) redis.UniversalClient {
	return GetTestRedisClient(t)
}

// ============================================================================
// MySQL 连接配置
// ============================================================================

var (
	// 单例 MySQL 数据库连接（用于需要持久连接的测试）
	testDBInstance *gorm.DB
	testDBOnce     sync.Once

	// 已迁移的模型缓存（避免重复迁移）
	migratedModels = make(map[string]bool)
	migrateMutex   sync.Mutex

	// 迁移完成标志，用于等待首次迁移
	migrationDone     = make(chan struct{})
	migrationDoneOnce sync.Once
)

// GetTestDB 获取测试用数据库连接（单例模式）
// 基于 SQLite 内存数据库（cache=shared 共享），零外部依赖，无需连接真实 MySQL
func GetTestDB(t *testing.T) *gorm.DB {
	testDBOnce.Do(func() {
		// :memory: 内存数据库，配合 MaxOpenConns(1) 单连接复用，所有测试共享同一内存库
		// SQLite 并发写需串行，单连接避免 "database is locked"
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Silent), // 测试时使用静默模式
			SkipDefaultTransaction: true,                                  // 跳过默认事务，提升性能
		})
		require.NoError(t, err, "SQLite 连接失败")

		sqlDB, err := db.DB()
		require.NoError(t, err, "获取 SQL DB 失败")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, sqlDB.PingContext(ctx), "SQLite Ping 失败")

		// SQLite 写操作需要串行，单连接避免并发写冲突
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(0)
		sqlDB.SetConnMaxIdleTime(0)

		t.Logf("✅ SQLite 内存数据库连接成功")
		testDBInstance = db
	})
	return testDBInstance
}

// GetTestDBWithMigration 获取测试用数据库并执行迁移
// models: 需要迁移的模型列表，例如 &MessageSendRecord{}, &ConnectionRecord{}
// 使用缓存机制避免重复迁移相同的模型
func GetTestDBWithMigration(t *testing.T, models ...interface{}) *gorm.DB {
	db := GetTestDB(t)

	// 快速路径：如果没有模型需要迁移，直接返回
	if len(models) == 0 {
		return db
	}

	// 检查是否需要迁移（无锁快速检查）
	migrateMutex.Lock()
	allMigrated := true
	for _, model := range models {
		modelType := fmt.Sprintf("%T", model)
		if !migratedModels[modelType] {
			allMigrated = false
			break
		}
	}
	migrateMutex.Unlock()

	// 如果所有模型都已迁移，直接返回
	if allMigrated {
		return db
	}

	// 需要迁移：获取锁并执行
	migrateMutex.Lock()
	defer migrateMutex.Unlock()

	// 双重检查：再次过滤未迁移的模型
	var needMigrate []interface{}
	for _, model := range models {
		modelType := fmt.Sprintf("%T", model)
		if !migratedModels[modelType] {
			needMigrate = append(needMigrate, model)
			migratedModels[modelType] = true
		}
	}

	// 执行迁移
	if len(needMigrate) > 0 {
		t.Logf("🔄 开始迁移 %d 个模型", len(needMigrate))
		start := time.Now()
		err := db.AutoMigrate(needMigrate...)
		require.NoError(t, err, "数据库迁移失败")
		t.Logf("✅ 迁移完成，耗时: %v", time.Since(start))

		// 创建唯一索引（不使用 GORM uniqueIndex tag，改用 Migrator 跨方言创建）
		if db.Migrator().HasTable("wsc_agent_workload") {
			idxMigrator := dbMigrator.NewMigrator(db, &dbMigrator.MigratorConfig{
				Indexes: []dbMigrator.IndexDefinition{
					dbMigrator.NewUniqueIndex("wsc_agent_workload", "agent_id", "dimension", "time_key"),
				},
				SkipIndexOnError: true,
			})
			if err := idxMigrator.CreateIndexes(); err != nil {
				t.Logf("⚠️ 创建 AgentWorkload 唯一索引失败: %v", err)
			}
		}

		// 标记首次迁移完成
		migrationDoneOnce.Do(func() {
			close(migrationDone)
		})
	}

	return db
}

// ============================================================================
// 辅助函数
// ============================================================================

// CleanupTestRedis 清理 Redis 测试数据（可用于测试清理）
func CleanupTestRedis(t *testing.T, client *redis.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.FlushDB(ctx).Err()
	if err != nil {
		t.Logf("警告：清理 Redis 测试数据失败: %v", err)
	}
}

// CleanupTestTable 清理测试表数据（SQLite 兼容，用 DELETE 替代 TRUNCATE）
func CleanupTestTable(t *testing.T, db *gorm.DB, tableName string) {
	err := db.Exec("DELETE FROM " + tableName).Error
	if err != nil {
		t.Logf("警告：清理表 %s 失败: %v", tableName, err)
	}
}
