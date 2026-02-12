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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ============================================================================
// Redis 连接配置
// ============================================================================

const (
	// Redis 默认配置
	defaultRedisAddr     = "120.79.25.168:16389"
	defaultRedisPassword = "M5Pi9YW6u"
	defaultRedisDB       = 1
	// MySQL 默认配置
	defaultMySQLDSN = "root:idev88888@tcp(120.77.38.35:13306)/im_agent?charset=utf8mb4&parseTime=True&loc=Local&timeout=60s&readTimeout=60s&writeTimeout=60s"
)

var (
	// 单例 Redis 客户端（用于需要持久连接的测试）
	testRedisInstance *redis.Client
	testRedisOnce     sync.Once
)

// GetTestRedisClient 获取测试用 Redis 客户端（单例模式）
// 优先使用环境变量 TEST_REDIS_ADDR 和 TEST_REDIS_PASSWORD，否则使用默认配置
func GetTestRedisClient(t *testing.T) *redis.Client {
	testRedisOnce.Do(func() {
		// 从环境变量读取配置，支持 CI/CD 环境
		addr := os.Getenv("TEST_REDIS_ADDR")
		password := os.Getenv("TEST_REDIS_PASSWORD")

		if addr == "" {
			// 本地开发环境默认配置
			addr = defaultRedisAddr
			password = defaultRedisPassword
			t.Logf("📌 使用默认 Redis 配置: %s (DB:%d)", addr, defaultRedisDB)
		} else {
			t.Logf("📌 使用环境变量 Redis 配置: %s (DB:%d)", addr, defaultRedisDB)
		}

		testRedisInstance = redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       defaultRedisDB,
			// 增加连接池大小以支持高并发测试
			PoolSize:        1000,                   // 增加到1000个连接，支持500并发×2
			MinIdleConns:    100,                    // 增加最小空闲连接，减少建立新连接的开销
			MaxRetries:      5,                      // 增加重试次数，应对网络波动
			DialTimeout:     60 * time.Second,       // 大幅增加连接超时，应对网络延迟
			ReadTimeout:     30 * time.Second,       // 大幅增加读超时
			WriteTimeout:    30 * time.Second,       // 大幅增加写超时
			PoolTimeout:     60 * time.Second,       // 大幅增加连接池超时，避免高并发时获取连接超时
			MinRetryBackoff: 200 * time.Millisecond, // 重试最小间隔
			MaxRetryBackoff: 1 * time.Second,        // 重试最大间隔
			// 连接保活配置，防止长时间空闲导致连接断开
			ConnMaxIdleTime: 5 * time.Minute,
			ConnMaxLifetime: 30 * time.Minute,
		})

		// 测试连接，增加重试机制
		var err error
		for retry := 0; retry < 3; retry++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = testRedisInstance.Ping(ctx).Err()
			cancel()

			if err == nil {
				break
			}
			if retry < 2 {
				t.Logf("⚠️ Redis连接失败（重试 %d/3）: %v", retry+1, err)
				time.Sleep(time.Second * time.Duration(retry+1))
			}
		}
		require.NoError(t, err, "Redis 连接失败（已重试3次），请检查配置和网络")
	})
	// 并发安全检查: 如果初始化失败, testRedisInstance 可能为 nil
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

// NewTestRedisClient 创建新的 Redis 客户端（每次返回新实例）
// 适用于需要独立连接的测试
func NewTestRedisClient(t *testing.T) *redis.Client {
	// 从环境变量读取配置，支持 CI/CD 环境
	addr := os.Getenv("TEST_REDIS_ADDR")
	password := os.Getenv("TEST_REDIS_PASSWORD")

	if addr == "" {
		// 本地开发环境默认配置
		addr = defaultRedisAddr
		password = defaultRedisPassword
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       defaultRedisDB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 测试连接
	err := client.Ping(ctx).Err()
	require.NoError(t, err, "Redis 连接失败，请检查配置")

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
// 从环境变量读取 DSN，支持 CI/CD 环境
func GetTestDB(t *testing.T) *gorm.DB {
	testDBOnce.Do(func() {
		// 从环境变量读取 DSN，支持 CI/CD 环境
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			// 本地开发环境默认配置
			dsn = defaultMySQLDSN
			t.Logf("📌 使用默认 MySQL 配置: %s", maskPassword(dsn))
		} else {
			t.Logf("📌 使用环境变量 MySQL 配置: %s", maskPassword(dsn))
		}
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Silent), // 测试时使用静默模式
			SkipDefaultTransaction: true,                                  // 跳过默认事务，提升性能
			PrepareStmt:            true,                                  // 预编译语句，提升性能
		})
		require.NoError(t, err, "MySQL 连接失败，请检查配置和网络")

		// 配置连接池 - 增加连接数以支持并发测试
		sqlDB, err := db.DB()
		require.NoError(t, err, "获取 SQL DB 失败")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = sqlDB.PingContext(ctx)
		require.NoError(t, err, "MySQL Ping 失败")

		sqlDB.SetMaxIdleConns(20)                  // 增加空闲连接数
		sqlDB.SetMaxOpenConns(50)                  // 增加最大连接数
		sqlDB.SetConnMaxLifetime(30 * time.Minute) // 减少连接生命周期，避免长时间测试中连接失效
		sqlDB.SetConnMaxIdleTime(5 * time.Minute)  // 减少空闲连接超时时间

		t.Logf("✅ MySQL 连接成功")
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

// CleanupTestTable 清理 MySQL 测试表数据
func CleanupTestTable(t *testing.T, db *gorm.DB, tableName string) {
	err := db.Exec("TRUNCATE TABLE " + tableName).Error
	if err != nil {
		t.Logf("警告：清理表 %s 失败: %v", tableName, err)
	}
}

// maskPassword 隐藏 DSN 中的密码部分
func maskPassword(dsn string) string {
	// DSN 格式: user:password@tcp(host:port)/database?params
	if len(dsn) == 0 {
		return dsn
	}
	// 查找密码位置
	start := 0
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == ':' && start == 0 {
			start = i + 1
		}
		if dsn[i] == '@' && start > 0 {
			// 找到密码结束位置
			return dsn[:start] + "***" + dsn[i:]
		}
	}
	return dsn
}

// ============================================================================
// 兼容旧代码的函数别名
// ============================================================================

// getTestRedisClient 兼容旧测试代码（小写函数名）
func getTestRedisClient(t *testing.T) *redis.Client {
	return GetTestRedisClientWithFlush(t)
}

// getTestDB 兼容旧测试代码（小写函数名）
func getTestDB(t *testing.T) *gorm.DB {
	return GetTestDB(t)
}

// getTestOfflineDB 兼容离线消息测试
func getTestOfflineDB(t *testing.T) *gorm.DB {
	return GetTestDBWithMigration(t, &OfflineMessageRecord{})
}

// getTestHandlerDB 兼容消息处理器测试
func getTestHandlerDB(t *testing.T) *gorm.DB {
	return GetTestDBWithMigration(t, &MessageSendRecord{}, &OfflineMessageRecord{})
}

// getTestHandlerRedis 兼容消息处理器 Redis 测试
func getTestHandlerRedis(t *testing.T) redis.UniversalClient {
	return GetTestRedisUniversalClient(t)
}
