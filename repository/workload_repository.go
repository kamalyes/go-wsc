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
	GetLeastLoadedAgent(ctx context.Context, onlineAgents []string) (string, int64, error)

	// RemoveAgentWorkload 移除客服负载记录（客服下线时调用）
	// 只删除 ZSet 记录，保留 string key 以便重新上线时恢复
	RemoveAgentWorkload(ctx context.Context, agentID string) error

	// GetAllAgentWorkloads 获取所有客服的负载信息
	GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error)

	// BatchSetAgentWorkload 批量设置客服负载
	BatchSetAgentWorkload(ctx context.Context, workloads map[string]int64) error

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

// GetWorkloadKey 生成客服负载的 Redis key
// 格式：{keyPrefix}agent:{agentID}
func (r *RedisWorkloadRepository) GetWorkloadKey(agentID string) string {
	return fmt.Sprintf("%sagent:%s", r.keyPrefix, agentID)
}

// GetZSetKey 获取负载排序 ZSet 的 key
func (r *RedisWorkloadRepository) GetZSetKey() string {
	return fmt.Sprintf("%szset", r.keyPrefix)
}

// GetDimensionKey 生成指定维度的 Redis key
// 格式：{keyPrefix}{dimension}:{timeKey}:agent:{agentID}
// 示例：workload:hourly:2026022513:agent:agent123
func (r *RedisWorkloadRepository) GetDimensionKey(dimension WorkloadDimension, agentID string, t time.Time) string {
	timeKey := dimension.FormatTimeKey(t)
	return fmt.Sprintf("%s%s:%s:agent:%s", r.keyPrefix, dimension, timeKey, agentID)
}

// GetDimensionZSetKey 生成指定维度的 ZSet key
// 格式：{keyPrefix}{dimension}:{timeKey}:zset
// 示例：workload:hourly:2026022513:zset
func (r *RedisWorkloadRepository) GetDimensionZSetKey(dimension WorkloadDimension, t time.Time) string {
	timeKey := dimension.FormatTimeKey(t)
	return fmt.Sprintf("%s%s:%s:zset", r.keyPrefix, dimension, timeKey)
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
		existingWorkload, _ := convert.MustIntT[int64](workloadStr, &roundNone)

		// 同步所有维度到 ZSet
		if err := r.syncAllDimensionsToZSet(ctx, agentID, existingWorkload, now); err != nil {
			return 0, errorx.WrapError("failed to sync all dimensions to zset", err)
		}

		r.logger.Debugf("🔄 客服 %s 上线，从 Redis 恢复负载: %d", agentID, existingWorkload)
		return existingWorkload, nil
	}

	// 2. Redis 不存在，从 DB 加载 realtime 维度
	var finalWorkload int64
	if r.db != nil && err == redis.Nil {
		var dbModel AgentWorkloadModel
		if err := r.db.WithContext(ctx).
			Where("agent_id = ? AND dimension = ? AND time_key = ?", agentID, WorkloadDimensionRealtime, "").
			First(&dbModel).Error; err == nil {
			finalWorkload = dbModel.Workload

			// 恢复所有维度到 Redis
			if err := r.setAllDimensions(ctx, agentID, finalWorkload, now); err != nil {
				return 0, errorx.WrapError("failed to restore from db to redis", err)
			}

			r.logger.Debugf("🔄 客服 %s 上线，从 DB 恢复负载: %d", agentID, finalWorkload)
			return finalWorkload, nil
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
			
			redis.call('ZADD', zsetKey, workload, agentID)
			
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
		args = append(args, string(dimension), timeKey)
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
		args = append(args, string(dimension), timeKey, ttl)
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
		workload, _ := convert.MustIntT[int64](workloadStr, &roundNone)
		return workload, nil
	}

	// 2. Redis 未命中，从 DB 加载 realtime 维度
	if err == redis.Nil && r.db != nil {
		var dbModel AgentWorkloadModel
		if err := r.db.WithContext(ctx).
			Where("agent_id = ? AND dimension = ? AND time_key = ?", agentID, WorkloadDimensionRealtime, "").
			First(&dbModel).Error; err == nil {
			// 回写到 Redis
			if err := r.client.Set(ctx, realtimeKey, dbModel.Workload, 0).Err(); err != nil {
				r.logger.Warnf("⚠️ 回写 Redis 失败: %v", err)
			}
			r.logger.Debugf("📥 从 DB 加载客服 %s 负载: %d", agentID, dbModel.Workload)
			return dbModel.Workload, nil
		}
		// DB 也没有，返回 0
		return 0, nil
	}

	return 0, errorx.WrapError("failed to get agent workload", err)
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
		WorkloadDimension("hourly").FormatTimeKey(now),
		WorkloadDimension("daily").FormatTimeKey(now),
		WorkloadDimension("monthly").FormatTimeKey(now),
		WorkloadDimension("yearly").FormatTimeKey(now),
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
func (r *RedisWorkloadRepository) GetLeastLoadedAgent(ctx context.Context, onlineAgents []string) (string, int64, error) {
	if len(onlineAgents) == 0 {
		return "", 0, errorx.WrapError("no online agents available")
	}
	return r.getLeastLoadedAgentFromRedis(ctx, onlineAgents)
}

// getLeastLoadedAgentFromRedis 从 Redis ZSet 获取负载最小的客服
func (r *RedisWorkloadRepository) getLeastLoadedAgentFromRedis(ctx context.Context, onlineAgents []string) (string, int64, error) {
	// 使用 Lua 脚本在 Redis 端完成筛选、随机选择和降级处理
	// 当多个客服负载相同时，在它们之间随机选择，实现真正的负载均衡
	luaScript := `
		local zsetKey = KEYS[1]
		local keyPrefix = KEYS[2]
		local maxCandidates = tonumber(ARGV[1])
		local onlineAgents = {}

		-- 初始化随机数种子（使用当前时间的微秒数）
		math.randomseed(tonumber(redis.call('TIME')[2]))

		-- 构建在线客服集合
		for i = 2, #ARGV do
			onlineAgents[ARGV[i]] = true
		end

		-- 获取前 N 个最低负载的客服（N 由配置决定）
		local results = redis.call('ZRANGE', zsetKey, 0, maxCandidates - 1, 'WITHSCORES')

		-- 找到最小负载值和所有具有该负载的在线客服
		local minWorkload = nil
		local candidateAgents = {}

		for i = 1, #results, 2 do
			local agentID = results[i]
			local workload = tonumber(results[i+1])
			
			if onlineAgents[agentID] then
				if minWorkload == nil or workload < minWorkload then
					-- 发现更小的负载，清空之前的候选
					minWorkload = workload
					candidateAgents = {agentID}
				elseif workload == minWorkload then
					-- 相同负载，添加到候选列表
					table.insert(candidateAgents, agentID)
				end
			end
		end

		-- 从候选客服中随机选择一个
		if #candidateAgents > 0 then
			local randomIndex = math.random(1, #candidateAgents)
			return {candidateAgents[randomIndex], minWorkload}
		end

		-- ZSet 中未找到，降级：从 string key 中随机选择一个在线客服
		-- 注意: pairs() 的遍历顺序不确定，但不影响最终的随机性
		-- 因为我们会从收集到的所有客服中使用 math.random() 随机选择
		local fallbackAgents = {}
		for agentID in pairs(onlineAgents) do
			local workloadKey = keyPrefix .. "agent:" .. agentID
			local workload = redis.call('GET', workloadKey)
			if workload then
				table.insert(fallbackAgents, {agentID, tonumber(workload)})
			end
		end

		if #fallbackAgents > 0 then
			-- 从所有候选客服中随机选择一个
			local randomIndex = math.random(1, #fallbackAgents)
			local selected = fallbackAgents[randomIndex]
			local selectedAgent = selected[1]
			local selectedWorkload = selected[2]
			
			-- 同步到 ZSet
			redis.call('ZADD', zsetKey, selectedWorkload, selectedAgent)
			
			return {selectedAgent, selectedWorkload}
		end

		return nil
	`

	zsetKey := r.GetZSetKey()
	keyPrefix := r.keyPrefix

	// 准备参数：第一个参数是 maxCandidates，后面是在线客服列表
	args := make([]any, len(onlineAgents)+1)
	args[0] = r.maxCandidates
	for i, agentID := range onlineAgents {
		args[i+1] = agentID
	}

	// 执行 Lua 脚本
	result, err := r.evalLua(ctx, luaScript, []string{zsetKey, keyPrefix}, args...)
	if err != nil && err != redis.Nil {
		return "", 0, errorx.WrapError("failed to get least loaded agent", err)
	}

	// 解析结果
	if result != nil {
		if resultArray, ok := result.([]any); ok && len(resultArray) == 2 {
			agentID := resultArray[0].(string)
			workload := r.parseWorkloadResult(resultArray[1])
			r.logger.Debugf("🎯 选择负载最小的客服: %s (负载: %d)", agentID, workload)
			return agentID, workload, nil
		}
	}

	return "", 0, errorx.WrapError("no online agents available")
}

// RemoveAgentWorkload 从 ZSet 中移除客服（保留 string key）
func (r *RedisWorkloadRepository) RemoveAgentWorkload(ctx context.Context, agentID string) error {
	zsetKey := r.GetZSetKey()

	if err := r.client.ZRem(ctx, zsetKey, agentID).Err(); err != nil {
		return errorx.WrapError("failed to remove agent from zset", err)
	}

	r.logger.Debugf("👋 客服 %s 下线，已从 ZSet 移除（保留 string key）", agentID)
	return nil
}

// GetAllAgentWorkloads 获取所有客服负载
func (r *RedisWorkloadRepository) GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error) {
	var results []redis.Z
	var err error
	zsetKey := r.GetZSetKey()

	if limit <= 0 {
		// 获取全部
		results, err = r.client.ZRangeWithScores(ctx, zsetKey, 0, -1).Result()
	} else {
		// 获取前N个
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

// BatchSetAgentWorkload 批量设置客服负载
func (r *RedisWorkloadRepository) BatchSetAgentWorkload(ctx context.Context, workloads map[string]int64) error {
	if len(workloads) == 0 {
		return nil
	}

	// 使用 Lua 脚本保证原子性
	luaScript := `
		local prefix = ARGV[1]
		local zsetKey = prefix .. "zset"
		
		-- 从 ARGV[2] 开始是 agentID:workload 对
		for i = 2, #ARGV, 2 do
			local agentID = ARGV[i]
			local workload = tonumber(ARGV[i+1])
			local workloadKey = prefix .. "agent:" .. agentID
			
			-- 设置工作负载（永不过期）
			redis.call('SET', workloadKey, workload)
			-- 更新 ZSet
			redis.call('ZADD', zsetKey, workload, agentID)
		end
		
		return (#ARGV - 1) / 2
	`

	// 准备参数
	args := []any{r.keyPrefix}

	for agentID, workload := range workloads {
		args = append(args, agentID, workload)
	}

	result, err := r.evalLua(ctx, luaScript, []string{}, args...)
	if err != nil {
		return errorx.WrapError("failed to batch set agent workloads", err)
	}

	r.logger.Debugf("✅ 批量设置 %v 个客服负载", result)
	return nil
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
