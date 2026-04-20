/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-18 09:00:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:23:08
 * @FilePath: \go-wsc\repository\workload_repository.go
 * @Description: 客服负载管理 - Redis + DB 双层存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/convert"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WorkloadInfo 负载信息
type WorkloadInfo struct {
	AgentID      string    `json:"agent_id"`    // 客服ID
	Workload     int64     `json:"workload"`    // 当前工作负载
	LastUpdateAt time.Time `json:"last_update"` // 最后更新时间
}

// WorkloadRepository 负载管理仓库接口
type WorkloadRepository interface {
	// ReloadAgentWorkload 重新加载客服工作负载（客服上线时调用）
	// 优先级：Redis > DB > 默认值 0
	// 从持久化存储中恢复负载，如果不存在则初始化为 0，并同步到 ZSet
	ReloadAgentWorkload(ctx context.Context, agentID string) (int64, error)

	// ForceSetAgentWorkload 强制设置客服工作负载（慎用，会覆盖现有值）
	// 直接覆盖 Redis 和 ZSet 中的负载值，异步同步到 DB
	ForceSetAgentWorkload(ctx context.Context, agentID string, workload int64) error

	// GetAgentWorkload 获取客服工作负载
	GetAgentWorkload(ctx context.Context, agentID string) (int64, error)

	// IncrementAgentWorkload 增加客服工作负载
	IncrementAgentWorkload(ctx context.Context, agentID string) error

	// DecrementAgentWorkload 减少客服工作负载
	DecrementAgentWorkload(ctx context.Context, agentID string) error

	// GetLeastLoadedAgent 获取负载最小的在线客服
	GetLeastLoadedAgent(ctx context.Context, onlineAgents []string, dimension WorkloadDimension) (string, int64, error)

	// AcquireLeastLoadedAgent 原子地选择负载最小的在线客服并将其负载 +1
	//
	// 与 GetLeastLoadedAgent 的区别：
	//   - GetLeastLoadedAgent 只读，纯查询，返回后业务侧还需单独调用 IncrementAgentWorkload，
	//     这会导致"读-改-写"之间存在 TOCTOU 窗口，多进程/多 goroutine 并发时读到同一份旧负载快照，
	//     选出同一个客服，造成工单扎堆分配到同一人（负载均衡不均）
	//   - AcquireLeastLoadedAgent 在同一段 Lua 中完成 "选中 + 所有维度原子 +1"，
	//     Redis 单线程保证跨进程原子，是分布式场景下负载均衡分配的正确做法
	//
	// 返回值:
	//   - agentID: 被选中的客服 ID
	//   - workload: 选中客服在选中前的 realtime 负载值（用于日志/监控）
	//   - error: 执行失败的错误
	//
	// 业务失败回滚：调用方应在后续业务失败时调用 DecrementAgentWorkload 回滚此次预扣减。
	AcquireLeastLoadedAgent(ctx context.Context, onlineAgents []string, dimension WorkloadDimension) (string, int64, error)

	// RemoveAgentWorkload 移除客服负载记录（客服下线时调用）
	// 只删除 ZSet 记录，保留 string key 以便重新上线时恢复
	RemoveAgentWorkload(ctx context.Context, agentID string) error

	// GetAllAgentWorkloads 获取所有客服的负载信息
	GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error)

	// Close 关闭仓库
	Close() error
}

// RedisWorkloadRepository Redis + DB 双层存储实现
type RedisWorkloadRepository struct {
	client        *redis.Client
	db            *gorm.DB
	keyPrefix     string         // key 前缀
	maxCandidates int            // 获取负载最小客服时的最大候选数量
	logger        logger.ILogger // 日志记录器
}

// NewRedisWorkloadRepository 创建 Redis 负载管理仓库
// 参数:
//   - client: Redis 客户端 (github.com/redis/go-redis/v9)
//   - config: 负载管理配置对象
//   - log: 日志记录器
func NewRedisWorkloadRepository(client *redis.Client, db *gorm.DB, config *wscconfig.Workload, log logger.ILogger) WorkloadRepository {
	keyPrefix := mathx.IF(config.KeyPrefix == "", DefaultWorkloadKeyPrefix, config.KeyPrefix)
	maxCandidates := config.GetMaxCandidates()

	repo := &RedisWorkloadRepository{
		client:        client,
		db:            db,
		keyPrefix:     keyPrefix,
		maxCandidates: maxCandidates,
		logger:        log,
	}

	return repo
}

// GetDimensionKey 生成指定维度的 Redis key
// 格式：{keyPrefix}{dimension}:{timeKey}:agent:{agentID}
// realtime: workload:realtime:agent:{agentID}
// hourly: workload:hourly:2026022513:agent:agent123
func (r *RedisWorkloadRepository) GetDimensionKey(dimension WorkloadDimension, agentID string, t time.Time) string {
	return fmt.Sprintf("%s%s:agent:%s", r.keyPrefix, dimension.FormatKeyPart(t), agentID)
}

// GetDimensionZSetKey 生成指定维度的 ZSet key
// 格式：{keyPrefix}{dimension}:{timeKey}:zset
// realtime: workload:realtime:zset
// hourly: workload:hourly:2026022513:zset
func (r *RedisWorkloadRepository) GetDimensionZSetKey(dimension WorkloadDimension, t time.Time) string {
	return fmt.Sprintf("%s%s:zset", r.keyPrefix, dimension.FormatKeyPart(t))
}

// evalLua 执行 Lua 脚本的通用方法
func (r *RedisWorkloadRepository) evalLua(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return r.client.Eval(ctx, script, keys, args...).Result()
}

// parseWorkloadResult 解析 Redis 返回的负载值
func (r *RedisWorkloadRepository) parseWorkloadResult(result any) int64 {
	roundMode := convert.RoundNone
	workload, _ := convert.MustIntT[int64](result, &roundMode)
	return workload
}

// ReloadAgentWorkload 重新加载客服负载（优先级：Redis > DB > 默认值 0）
// 用于客服上线时恢复之前的工作负载，如果不存在则初始化为 0
// 返回 realtime 维度的负载值
func (r *RedisWorkloadRepository) ReloadAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	now := time.Now()

	// 1. 先尝试从 Redis realtime 维度获取并同步所有维度到 ZSet
	realtimeKey := r.GetDimensionKey(WorkloadDimensionRealtime, agentID, now)
	workloadStr, err := r.client.Get(ctx, realtimeKey).Result()

	if err == nil {
		// Redis 中存在，解析负载值
		roundNone := convert.RoundNone
		existingWorkload, parseErr := convert.MustIntT[int64](workloadStr, &roundNone)
		if parseErr != nil {
			// Redis 中值被污染（非数字），记录告警并按 0 处理，避免后续广播错误值
			r.logger.Warnf("⚠️ 客服 %s Redis 负载值解析失败(%q): %v，降级为 0", agentID, workloadStr, parseErr)
			existingWorkload = 0
		}

		// 同步所有维度到 ZSet
		if err := r.syncAllDimensionsToZSet(ctx, agentID, existingWorkload, now); err != nil {
			return 0, errorx.WrapError("failed to sync all dimensions to zset", err)
		}

		r.logger.Debugf("🔄 客服 %s 上线，从 Redis 恢复负载: %d", agentID, existingWorkload)
		return existingWorkload, nil
	}

	// Redis 错误但不是 key 不存在 —— 视为底层故障上抛，避免误覆盖为 0
	if err != redis.Nil {
		return 0, errorx.WrapError("failed to read agent workload from redis", err)
	}

	// 2. Redis 不存在，从 DB 加载 realtime 维度
	var finalWorkload int64
	if r.db != nil {
		var dbModel AgentWorkloadModel
		dbErr := r.db.WithContext(ctx).
			Where("agent_id = ? AND dimension = ? AND time_key = ?", agentID, WorkloadDimensionRealtime, "").
			First(&dbModel).Error
		if dbErr == nil {
			finalWorkload = dbModel.Workload

			// 恢复所有维度到 Redis
			if err := r.setAllDimensions(ctx, agentID, finalWorkload, now); err != nil {
				return 0, errorx.WrapError("failed to restore from db to redis", err)
			}

			r.logger.Debugf("🔄 客服 %s 上线，从 DB 恢复负载: %d", agentID, finalWorkload)
			return finalWorkload, nil
		}
		// DB 查询出错且不是“记录不存在” —— 不能当作 0 初始化，避免覆盖真实历史负载
		if !errors.Is(dbErr, gorm.ErrRecordNotFound) {
			return 0, errorx.WrapError("failed to load agent workload from db", dbErr)
		}
	}

	// 3. DB 也不存在，初始化所有维度为 0
	if err := r.setAllDimensions(ctx, agentID, finalWorkload, now); err != nil {
		return 0, errorx.WrapError("failed to init agent workload", err)
	}

	r.logger.Debugf("🆕 客服 %s 上线初始化，负载: %d", agentID, finalWorkload)
	return finalWorkload, nil
}

// syncAllDimensionsToZSet 同步所有维度到 ZSet
//
// 语义：仅用于 ReloadAgentWorkload 的"恢复"场景（客服上线/重连时将 string key 的真实负载同步到 ZSet）
// 为了避免与并发的 IncrementAgentWorkload/AcquireLeastLoadedAgent 发生 ZADD 覆盖竞态
// （调用方读到 string=9 → 另一路径 INCRBY 至 10 并 ZADD 10 → 本方法再 ZADD 9 覆盖回 9），
// 这里使用 ZADD NX：仅当 ZSet 中不存在该 agent 时才写入
// 如果 ZSet 中已经有该 agent 的 score（意味着有其他流程正在维护实时值），则保留现值不覆盖
func (r *RedisWorkloadRepository) syncAllDimensionsToZSet(ctx context.Context, agentID string, workload int64, t time.Time) error {
	luaScript := `
		local keyPrefix = ARGV[1]
		local agentID = ARGV[2]
		local workload = tonumber(ARGV[3])
		
		-- 从 ARGV[4] 开始是维度配置：dimension, timeKey
		local argIndex = 4
		
		while argIndex <= #ARGV do
			local dimension = ARGV[argIndex]
			local timeKey = ARGV[argIndex + 1]
			
			local zsetKey
			if timeKey == "" then
				zsetKey = keyPrefix .. dimension .. ":zset"
			else
				zsetKey = keyPrefix .. dimension .. ":" .. timeKey .. ":zset"
			end
			
			-- 仅在 ZSet 中不存在该 agent 时才写入，避免覆盖并发维护的真实值
			redis.call('ZADD', zsetKey, 'NX', workload, agentID)
			
			argIndex = argIndex + 2
		end
		
		return "OK"
	`

	// 准备参数
	args := []any{
		r.keyPrefix,
		agentID,
		workload,
	}

	// 遍历所有维度
	for _, dimension := range AllWorkloadDimensions {
		timeKey := dimension.FormatTimeKey(t)
		args = append(args, dimension.String(), timeKey)
	}

	_, err := r.evalLua(ctx, luaScript, []string{}, args...)
	return err
}

// setAllDimensions 设置所有维度的负载（原子操作）
func (r *RedisWorkloadRepository) setAllDimensions(ctx context.Context, agentID string, workload int64, t time.Time) error {
	luaScript := `
		local keyPrefix = ARGV[1]
		local agentID = ARGV[2]
		local workload = tonumber(ARGV[3])
		
		-- 从 ARGV[4] 开始是维度配置：dimension, timeKey, ttl(秒)
		local argIndex = 4
		
		while argIndex <= #ARGV do
			local dimension = ARGV[argIndex]
			local timeKey = ARGV[argIndex + 1]
			local ttl = tonumber(ARGV[argIndex + 2])
			
			local workloadKey, zsetKey
			if timeKey == "" then
				workloadKey = keyPrefix .. dimension .. ":agent:" .. agentID
				zsetKey = keyPrefix .. dimension .. ":zset"
			else
				workloadKey = keyPrefix .. dimension .. ":" .. timeKey .. ":agent:" .. agentID
				zsetKey = keyPrefix .. dimension .. ":" .. timeKey .. ":zset"
			end
			
			redis.call('SET', workloadKey, workload)
			redis.call('ZADD', zsetKey, workload, agentID)
			
			if ttl > 0 then
				redis.call('EXPIRE', workloadKey, ttl)
				redis.call('EXPIRE', zsetKey, ttl)
			end
			
			argIndex = argIndex + 3
		end
		
		return "OK"
	`

	// 准备参数
	args := []any{
		r.keyPrefix,
		agentID,
		workload,
	}

	// 遍历所有维度
	for _, dimension := range AllWorkloadDimensions {
		timeKey := dimension.FormatTimeKey(t)
		ttl := int64(dimension.GetTTL().Seconds())
		args = append(args, dimension.String(), timeKey, ttl)
	}

	_, err := r.evalLua(ctx, luaScript, []string{}, args...)
	if err != nil {
		return err
	}

	// 异步同步到 DB
	r.asyncSyncToDB(agentID, workload)

	return nil
}

// ForceSetAgentWorkload 强制设置客服负载（慎用，会覆盖现有值）
// 直接覆盖所有维度的 Redis 和 ZSet 中的负载值，异步同步到 DB
func (r *RedisWorkloadRepository) ForceSetAgentWorkload(ctx context.Context, agentID string, workload int64) error {
	now := time.Now()

	if err := r.setAllDimensions(ctx, agentID, workload, now); err != nil {
		return errorx.WrapError("failed to force set agent workload", err)
	}

	r.logger.Debugf("✅ 已强制设置客服 %s 工作负载: %d (所有维度)", agentID, workload)
	return nil
}

// GetAgentWorkload 获取客服负载（从 realtime 维度读取）
func (r *RedisWorkloadRepository) GetAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	now := time.Now()
	realtimeKey := r.GetDimensionKey(WorkloadDimensionRealtime, agentID, now)

	// 1. 先从 Redis realtime 维度获取
	workloadStr, err := r.client.Get(ctx, realtimeKey).Result()
	if err == nil {
		roundNone := convert.RoundNone
		workload, parseErr := convert.MustIntT[int64](workloadStr, &roundNone)
		if parseErr != nil {
			r.logger.Warnf("⚠️ 客服 %s Redis 负载值解析失败(%q): %v，降级为 0", agentID, workloadStr, parseErr)
			return 0, nil
		}
		return workload, nil
	}

	// Redis 错误但不是 key 不存在 —— 视为底层故障上抛
	if err != redis.Nil {
		return 0, errorx.WrapError("failed to get agent workload", err)
	}

	// 2. Redis 未命中，从 DB 加载 realtime 维度
	if r.db != nil {
		var dbModel AgentWorkloadModel
		dbErr := r.db.WithContext(ctx).
			Where("agent_id = ? AND dimension = ? AND time_key = ?", agentID, WorkloadDimensionRealtime, "").
			First(&dbModel).Error
		if dbErr == nil {
			// 回写到 Redis
			if err := r.client.Set(ctx, realtimeKey, dbModel.Workload, 0).Err(); err != nil {
				r.logger.Warnf("⚠️ 回写 Redis 失败: %v", err)
			}
			r.logger.Debugf("📥 从 DB 加载客服 %s 负载: %d", agentID, dbModel.Workload)
			return dbModel.Workload, nil
		}
		// DB 查询出错且不是“记录不存在” —— 上抛，避免静默吞掉故障
		if !errors.Is(dbErr, gorm.ErrRecordNotFound) {
			return 0, errorx.WrapError("failed to get agent workload from db", dbErr)
		}
	}

	// 3. Redis 和 DB 均无记录，视为 0（而非错误）
	return 0, nil
}

// IncrementAgentWorkload 增加客服负载（支持多维度）
func (r *RedisWorkloadRepository) IncrementAgentWorkload(ctx context.Context, agentID string) error {
	return r.incrementMultiDimension(ctx, agentID, 1)
}

// incrementMultiDimension 多维度增加客服负载（原子操作）
func (r *RedisWorkloadRepository) incrementMultiDimension(ctx context.Context, agentID string, delta int64) error {
	now := time.Now()

	// 使用 Lua 脚本保证所有维度的原子性更新
	luaScript := `
		local keyPrefix = ARGV[1]
		local agentID = ARGV[2]
		local delta = tonumber(ARGV[3])
		local currentTime = tonumber(ARGV[4])
		
		-- 维度配置：dimension, timeKey, ttl(秒)
		local dimensions = {
			{"realtime", "", 0},
			{"hourly", ARGV[5], 604800},      -- 7天
			{"daily", ARGV[6], 7776000},      -- 90天
			{"monthly", ARGV[7], 63072000},   -- 2年
			{"yearly", ARGV[8], 157680000}    -- 5年
		}
		
		local results = {}
		
		for i, dim in ipairs(dimensions) do
			local dimension = dim[1]
			local timeKey = dim[2]
			local ttl = dim[3]
			
			-- 构建 key
			local workloadKey, zsetKey
			if timeKey == "" then
				workloadKey = keyPrefix .. dimension .. ":agent:" .. agentID
				zsetKey = keyPrefix .. dimension .. ":zset"
			else
				workloadKey = keyPrefix .. dimension .. ":" .. timeKey .. ":agent:" .. agentID
				zsetKey = keyPrefix .. dimension .. ":" .. timeKey .. ":zset"
			end
			
			-- 递增负载
			local newWorkload = redis.call('INCRBY', workloadKey, delta)
			
			-- 确保不低于0
			if newWorkload < 0 then
				newWorkload = 0
				redis.call('SET', workloadKey, 0)
			end
			
			-- 更新 ZSet
			redis.call('ZADD', zsetKey, newWorkload, agentID)
			
			-- 设置 TTL（realtime 维度除外）
			if ttl > 0 then
				redis.call('EXPIRE', workloadKey, ttl)
				redis.call('EXPIRE', zsetKey, ttl)
			end
			
			-- 记录结果
			table.insert(results, {dimension, timeKey, newWorkload})
		end
		
		return results
	`

	// 准备参数
	args := []any{
		r.keyPrefix,
		agentID,
		delta,
		now.Unix(),
		WorkloadDimensionHourly.FormatTimeKey(now),
		WorkloadDimensionDaily.FormatTimeKey(now),
		WorkloadDimensionMonthly.FormatTimeKey(now),
		WorkloadDimensionYearly.FormatTimeKey(now),
	}

	result, err := r.evalLua(ctx, luaScript, []string{}, args...)
	if err != nil {
		return errorx.WrapError("failed to increment multi-dimension workload", err)
	}

	// 解析结果并异步同步到 DB
	if resultArray, ok := result.([]any); ok {
		for _, item := range resultArray {
			if dimResult, ok := item.([]any); ok && len(dimResult) == 3 {
				dimension := dimResult[0].(string)
				timeKey := dimResult[1].(string)
				workload := r.parseWorkloadResult(dimResult[2])

				// 异步同步到 DB
				r.asyncSyncMultiDimensionToDB(agentID, WorkloadDimension(dimension), timeKey, workload)
			}
		}
	}

	r.logger.Debugf("📈 客服 %s 工作负载增加 %d (多维度)", agentID, delta)
	return nil
}

// DecrementAgentWorkload 减少客服负载（不低于 0，支持多维度）
func (r *RedisWorkloadRepository) DecrementAgentWorkload(ctx context.Context, agentID string) error {
	return r.incrementMultiDimension(ctx, agentID, -1)
}

// GetLeastLoadedAgent 获取负载最小的在线客服（支持多策略）
func (r *RedisWorkloadRepository) GetLeastLoadedAgent(ctx context.Context, onlineAgents []string, dimension WorkloadDimension) (string, int64, error) {
	if len(onlineAgents) == 0 {
		return "", 0, errorx.WrapError("no online agents available")
	}
	return r.getLeastLoadedAgentFromRedis(ctx, onlineAgents, dimension, time.Now())
}

// getLeastLoadedAgentFromRedis 从 Redis ZSet 获取负载最小的客服（支持多维度）
func (r *RedisWorkloadRepository) getLeastLoadedAgentFromRedis(ctx context.Context, onlineAgents []string, dimension WorkloadDimension, t time.Time) (string, int64, error) {
	// 使用 Lua 脚本在 Redis 端完成筛选、随机选择和降级处理
	// 算法：
	//   1. 遍历传入的所有在线客服，通过 ZSCORE 查询各自负载
	//   2. ZSet 中未命中的客服从 string key (GET) 读取负载并回填 ZSet
	//   3. 收集所有负载等于最小值的客服，作为候选池
	//   4. 从候选池中使用 math.random 等概率随机选择一个
	// 修复：不再使用 ZRANGE 0..maxCandidates-1 截断候选集合，避免仅在 ZSet 排序靠前的
	//       固定几个客服之间轮换（导致其他在线客服永远分配不到任务，表现为不均匀分配）
	luaScript := `
		local zsetKey = KEYS[1]
		local keyPrefix = KEYS[2]
		local dimension = KEYS[3]
		local timeKey = KEYS[4]

		-- 初始化随机数种子（使用当前时间的微秒数，避免多次调用返回相同结果）
		local t = redis.call('TIME')
		math.randomseed(tonumber(t[1]) * 1000000 + tonumber(t[2]))
		-- 预热一次，规避部分 Lua 实现第一次 random 不够随机的问题
		math.random()

		-- 构建单个客服在 realtime / 时间分片维度下的 string key
		local function buildWorkloadKey(agentID)
			if timeKey == "" then
				return keyPrefix .. dimension .. ":agent:" .. agentID
			else
				return keyPrefix .. dimension .. ":" .. timeKey .. ":agent:" .. agentID
			end
		end

		local minWorkload = nil
		local candidateAgents = {}

		-- 遍历所有传入的在线客服，确保每一个都参与候选比较
		for i = 1, #ARGV do
			local agentID = ARGV[i]
			local score = redis.call('ZSCORE', zsetKey, agentID)
			local workload

			if score then
				workload = tonumber(score)
			else
				-- ZSet 中缺失，降级从 string key 读取，并回填 ZSet
				local workloadStr = redis.call('GET', buildWorkloadKey(agentID))
				if workloadStr then
					workload = tonumber(workloadStr)
				else
					workload = 0
				end
				redis.call('ZADD', zsetKey, workload, agentID)
			end

			if minWorkload == nil or workload < minWorkload then
				minWorkload = workload
				candidateAgents = {agentID}
			elseif workload == minWorkload then
				table.insert(candidateAgents, agentID)
			end
		end

		if #candidateAgents > 0 then
			local randomIndex = math.random(1, #candidateAgents)
			return {candidateAgents[randomIndex], minWorkload}
		end

		return nil
	`

	// 获取指定维度的 ZSet key
	zsetKey := r.GetDimensionZSetKey(dimension, t)
	keyPrefix := r.keyPrefix
	timeKey := dimension.FormatTimeKey(t)

	// 参数：所有在线客服列表（不再需要 maxCandidates）
	args := make([]any, len(onlineAgents))
	for i, agentID := range onlineAgents {
		args[i] = agentID
	}

	// 执行 Lua 脚本
	result, err := r.evalLua(ctx, luaScript, []string{zsetKey, keyPrefix, dimension.String(), timeKey}, args...)
	if err != nil && err != redis.Nil {
		return "", 0, errorx.WrapError("failed to get least loaded agent", err)
	}

	// 解析结果
	if result != nil {
		if resultArray, ok := result.([]any); ok && len(resultArray) == 2 {
			agentID := resultArray[0].(string)
			workload := r.parseWorkloadResult(resultArray[1])
			r.logger.Debugf("🎯 选择负载最小的客服 [%s]: %s (负载: %d)", dimension, agentID, workload)
			return agentID, workload, nil
		}
	}

	return "", 0, errorx.WrapError("no online agents available")
}

// AcquireLeastLoadedAgent 原子地选择负载最小的在线客服并将其负载 +1（分布式安全）
//
// 算法（整个流程在单个 Lua 脚本内完成，Redis 单线程保证原子性）：
//  1. 基于 realtime 维度遍历所有传入的在线客服，通过 ZSCORE 查询各自负载；
//     ZSet 中未命中的客服从 string key (GET) 读取并回填 ZSet
//  2. 收集所有负载等于最小值的客服作为候选池，使用 math.random 等概率随机选一个
//  3. 对选中客服的所有维度（realtime/hourly/daily/monthly/yearly）执行 INCRBY +1 并更新 ZSet
//  4. 返回选中的客服 ID、选中前的 realtime 负载、以及各维度的新负载（用于异步同步到 DB）
//
// 为什么需要原子化：多副本部署/多 goroutine 并发时，若"选择"和"扣减"分两步执行，
// 多个调用会读到同一份旧负载快照，选出同一客服，导致分配严重不均
func (r *RedisWorkloadRepository) AcquireLeastLoadedAgent(ctx context.Context, onlineAgents []string, dimension WorkloadDimension) (string, int64, error) {
	if len(onlineAgents) == 0 {
		return "", 0, errorx.WrapError("no online agents available")
	}

	// 目前仅支持 realtime 维度参与分配（与 GetLeastLoadedAgent 保持一致）
	if dimension != WorkloadDimensionRealtime {
		return "", 0, errorx.WrapError(fmt.Sprintf("AcquireLeastLoadedAgent only supports realtime dimension, got: %s", dimension))
	}

	now := time.Now()

	// Lua 脚本：选择 + 多维度原子 +1
	luaScript := `
		local keyPrefix = ARGV[1]
		local realtimeTimeKey = ARGV[2]
		local hourlyTimeKey = ARGV[3]
		local dailyTimeKey = ARGV[4]
		local monthlyTimeKey = ARGV[5]
		local yearlyTimeKey = ARGV[6]
		-- ARGV[7..] 为在线客服 ID 列表

		-- 初始化随机种子
		local t = redis.call('TIME')
		math.randomseed(tonumber(t[1]) * 1000000 + tonumber(t[2]))
		math.random()

		local function buildWorkloadKey(dimension, timeKey, agentID)
			if timeKey == "" then
				return keyPrefix .. dimension .. ":agent:" .. agentID
			else
				return keyPrefix .. dimension .. ":" .. timeKey .. ":agent:" .. agentID
			end
		end

		local function buildZSetKey(dimension, timeKey)
			if timeKey == "" then
				return keyPrefix .. dimension .. ":zset"
			else
				return keyPrefix .. dimension .. ":" .. timeKey .. ":zset"
			end
		end

		-- 维度配置 {dimension, timeKey, ttl(秒)}
		local dimensions = {
			{"realtime", realtimeTimeKey, 0},
			{"hourly",   hourlyTimeKey,   604800},
			{"daily",    dailyTimeKey,    7776000},
			{"monthly",  monthlyTimeKey,  63072000},
			{"yearly",   yearlyTimeKey,   157680000}
		}

		local realtimeZSetKey = buildZSetKey("realtime", realtimeTimeKey)

		-- 1. 基于 realtime 维度，遍历在线客服收集最小负载候选集
		local minWorkload = nil
		local candidateAgents = {}

		for i = 7, #ARGV do
			local agentID = ARGV[i]
			local score = redis.call('ZSCORE', realtimeZSetKey, agentID)
			local workload

			if score then
				workload = tonumber(score)
			else
				-- ZSet 缺失，降级从 string key 读取并回填 ZSet
				local workloadStr = redis.call('GET', buildWorkloadKey("realtime", realtimeTimeKey, agentID))
				if workloadStr then
					workload = tonumber(workloadStr)
				else
					workload = 0
				end
				redis.call('ZADD', realtimeZSetKey, workload, agentID)
			end

			if minWorkload == nil or workload < minWorkload then
				minWorkload = workload
				candidateAgents = {agentID}
			elseif workload == minWorkload then
				table.insert(candidateAgents, agentID)
			end
		end

		if #candidateAgents == 0 then
			return nil
		end

		-- 2. 从候选池随机选一个
		local selected = candidateAgents[math.random(1, #candidateAgents)]

		-- 3. 对选中客服的所有维度原子 +1
		local newWorkloads = {}
		for i, dim in ipairs(dimensions) do
			local dimName = dim[1]
			local timeKey = dim[2]
			local ttl = dim[3]

			local workloadKey = buildWorkloadKey(dimName, timeKey, selected)
			local zsetKey = buildZSetKey(dimName, timeKey)

			local newWorkload = redis.call('INCRBY', workloadKey, 1)
			if newWorkload < 0 then
				newWorkload = 0
				redis.call('SET', workloadKey, 0)
			end
			redis.call('ZADD', zsetKey, newWorkload, selected)

			if ttl > 0 then
				redis.call('EXPIRE', workloadKey, ttl)
				redis.call('EXPIRE', zsetKey, ttl)
			end

			table.insert(newWorkloads, newWorkload)
		end

		-- 返回 {selectedAgentID, minWorkloadBeforeIncrement, realtimeNew, hourlyNew, dailyNew, monthlyNew, yearlyNew}
		return {selected, minWorkload, newWorkloads[1], newWorkloads[2], newWorkloads[3], newWorkloads[4], newWorkloads[5]}
	`

	args := make([]any, 0, 6+len(onlineAgents))
	args = append(args,
		r.keyPrefix,
		WorkloadDimensionRealtime.FormatTimeKey(now),
		WorkloadDimensionHourly.FormatTimeKey(now),
		WorkloadDimensionDaily.FormatTimeKey(now),
		WorkloadDimensionMonthly.FormatTimeKey(now),
		WorkloadDimensionYearly.FormatTimeKey(now),
	)
	for _, agentID := range onlineAgents {
		args = append(args, agentID)
	}

	result, err := r.evalLua(ctx, luaScript, []string{}, args...)
	if err != nil && err != redis.Nil {
		return "", 0, errorx.WrapError("failed to acquire least loaded agent", err)
	}

	if result == nil {
		return "", 0, errorx.WrapError("no online agents available")
	}

	resultArray, ok := result.([]any)
	if !ok || len(resultArray) < 7 {
		return "", 0, errorx.WrapError("unexpected lua result shape")
	}

	selected, _ := resultArray[0].(string)
	minWorkload := r.parseWorkloadResult(resultArray[1])

	// 异步同步各维度新负载到 DB（与 incrementMultiDimension 语义一致）
	dims := []WorkloadDimension{
		WorkloadDimensionRealtime,
		WorkloadDimensionHourly,
		WorkloadDimensionDaily,
		WorkloadDimensionMonthly,
		WorkloadDimensionYearly,
	}
	for i, dim := range dims {
		newWorkload := r.parseWorkloadResult(resultArray[2+i])
		r.asyncSyncMultiDimensionToDB(selected, dim, dim.FormatTimeKey(now), newWorkload)
	}

	r.logger.Debugf("🎯 原子选择并预扣减负载 [%s]: %s (选中前负载: %d)", dimension, selected, minWorkload)
	return selected, minWorkload, nil
}

// RemoveAgentWorkload 客服下线时从参与分配的 ZSet 中移除该客服（保留 string key）
//
// 只移除 realtime 维度的 ZSet：
//  1. 参与负载均衡分配的仅是 realtime 维度（详见 GetLeastLoadedAgent）
//  2. 时间分片维度（hourly/daily/monthly/yearly）的 ZSet key 依赖当前时间戳，
//     客服跨小时/跨天下线时，time.Now() 只能命中“当前”时间片的 ZSet，
//     前一时间片 ZSet 中残留的该客服记录无法被清理；同时这些维度本就是
//     用于统计的累计数据，不应在下线时清除。
func (r *RedisWorkloadRepository) RemoveAgentWorkload(ctx context.Context, agentID string) error {
	now := time.Now()

	zsetKey := r.GetDimensionZSetKey(WorkloadDimensionRealtime, now)
	if err := r.client.ZRem(ctx, zsetKey, agentID).Err(); err != nil {
		r.logger.Errorf("❌ 从 ZSet [%s] 移除客服 %s 失败: %v", WorkloadDimensionRealtime, agentID, err)
		return errorx.WrapError("failed to remove agent from realtime zset", err)
	}

	r.logger.Debugf("👋 客服 %s 下线，已从 realtime ZSet 移除（保留 string key 及统计维度）", agentID)
	return nil
}

// GetAllAgentWorkloads 获取所有客服负载（从 realtime 维度查询）
func (r *RedisWorkloadRepository) GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error) {
	zsetKey := r.GetDimensionZSetKey(WorkloadDimensionRealtime, time.Now())

	var results []redis.Z
	var err error

	if limit <= 0 {
		results, err = r.client.ZRangeWithScores(ctx, zsetKey, 0, -1).Result()
	} else {
		results, err = r.client.ZRangeWithScores(ctx, zsetKey, 0, limit-1).Result()
	}

	if err != nil && err != redis.Nil {
		return nil, errorx.WrapError("failed to get agent workloads", err)
	}

	workloads := make([]WorkloadInfo, 0, len(results))
	for _, z := range results {
		workloads = append(workloads, WorkloadInfo{
			AgentID:      z.Member.(string),
			Workload:     int64(z.Score),
			LastUpdateAt: time.Now(),
		})
	}

	return workloads, nil
}

// Close 关闭仓库
func (r *RedisWorkloadRepository) Close() error {
	r.logger.Info("🛑 WorkloadRepository 已关闭")
	return nil
}

// asyncSyncToDB 异步同步负载到 DB（多维度）
func (r *RedisWorkloadRepository) asyncSyncToDB(agentID string, workload int64) {
	now := time.Now()

	// 同步所有维度到 DB
	for _, dimension := range AllWorkloadDimensions {
		timeKey := dimension.FormatTimeKey(now)
		r.asyncSyncMultiDimensionToDB(agentID, dimension, timeKey, workload)
	}
}

// asyncSyncMultiDimensionToDB 异步同步单个维度负载到 DB
func (r *RedisWorkloadRepository) asyncSyncMultiDimensionToDB(agentID string, dimension WorkloadDimension, timeKey string, workload int64) {
	if r.db == nil {
		return
	}

	syncx.Go().WithTimeout(2 * time.Second).OnError(func(err error) {
		r.logger.Warnf("⚠️ 同步负载到 DB 失败: %v", err)
	}).ExecWithContext(func(ctx context.Context) error {
		// 使用 Upsert 避免并发场景下的重复键错误
		return r.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "agent_id"}, {Name: "dimension"}, {Name: "time_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"workload", "updated_at"}),
		}).Create(&AgentWorkloadModel{
			AgentID:           agentID,
			WorkloadDimension: dimension,
			TimeKey:           timeKey,
			Workload:          workload,
		}).Error
	})
}
