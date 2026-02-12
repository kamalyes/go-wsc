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
	// InitAgentWorkload 初始化客服工作负载（客服上线时调用）
	// 如果 key 不存在，则创建并设置为 initialWorkload；如果已存在，则同步到 ZSet
	InitAgentWorkload(ctx context.Context, agentID string, initialWorkload int64) (int64, error)

	// SetAgentWorkload 设置客服工作负载（强制覆盖）
	SetAgentWorkload(ctx context.Context, agentID string, workload int64) error

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

	// SyncAgentWorkloadToZSet 客服重新加入时，从单个key同步负载到ZSet
	SyncAgentWorkloadToZSet(ctx context.Context, agentID string) error

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

// InitAgentWorkload 初始化客服负载（优先级：Redis > DB > initialWorkload）
func (r *RedisWorkloadRepository) InitAgentWorkload(ctx context.Context, agentID string, initialWorkload int64) (int64, error) {
	// 1. 先尝试从 Redis 获取并同步到 ZSet（原子操作）
	existingWorkload, found, err := r.getWorkloadAndSync(ctx, agentID)
	if err != nil {
		return 0, errorx.WrapError("failed to get and sync workload", err)
	}
	if found {
		r.logger.Debugf("🔄 客服 %s 上线，从 Redis 恢复负载: %d", agentID, existingWorkload)
		return existingWorkload, nil
	}

	// 2. Redis 不存在，从 DB 加载
	var finalWorkload int64
	if r.db != nil {
		var dbModel AgentWorkloadModel
		if err := r.db.WithContext(ctx).Where("agent_id = ?", agentID).First(&dbModel).Error; err == nil {
			finalWorkload = dbModel.Workload
			if err := r.setWorkloadAndSync(ctx, agentID, finalWorkload); err != nil {
				return 0, errorx.WrapError("failed to sync from db to redis", err)
			}
			r.logger.Debugf("🔄 客服 %s 上线，从 DB 恢复负载: %d", agentID, finalWorkload)
			return finalWorkload, nil
		}
	}

	// 3. DB 也不存在，使用初始值
	finalWorkload = initialWorkload
	if err := r.setWorkloadAndSync(ctx, agentID, finalWorkload); err != nil {
		return 0, errorx.WrapError("failed to init agent workload", err)
	}

	// 异步创建 DB 记录（使用 Upsert 避免并发冲突）
	if r.db != nil {
		newModel := AgentWorkloadModel{
			AgentID:  agentID,
			Workload: finalWorkload,
		}
		syncx.Go().WithTimeout(2 * time.Second).OnError(func(err error) {
			r.logger.Warnf("⚠️ 创建 DB 负载记录失败: %v", err)
		}).ExecWithContext(func(ctx context.Context) error {
			return r.db.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "agent_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"workload", "updated_at"}),
			}).Create(&newModel).Error
		})
	}

	r.logger.Debugf("🆕 客服 %s 上线初始化，负载: %d", agentID, finalWorkload)
	return finalWorkload, nil
}

// getWorkloadAndSync 从 Redis 获取负载并同步到 ZSet（原子操作）
// 返回：workload, found, error
func (r *RedisWorkloadRepository) getWorkloadAndSync(ctx context.Context, agentID string) (int64, bool, error) {
	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()

	luaScript := `
		local workloadKey = KEYS[1]
		local zsetKey = KEYS[2]
		local agentID = ARGV[1]
		
		local workload = redis.call('GET', workloadKey)
		if workload then
			redis.call('ZADD', zsetKey, tonumber(workload), agentID)
			return workload
		end
		return nil
	`

	result, err := r.evalLua(ctx, luaScript, []string{workloadKey, zsetKey}, agentID)
	if err != nil && err != redis.Nil {
		return 0, false, err
	}
	if result == nil || err == redis.Nil {
		return 0, false, nil
	}

	workload := r.parseWorkloadResult(result)
	return workload, true, nil
}

// setWorkloadAndSync 设置负载并同步到 ZSet（原子操作）
func (r *RedisWorkloadRepository) setWorkloadAndSync(ctx context.Context, agentID string, workload int64) error {
	// 使用 Lua 脚本保证原子性
	luaScript := `
		local workloadKey = KEYS[1]
		local zsetKey = KEYS[2]
		local agentID = ARGV[1]
		local workload = tonumber(ARGV[2])
		
		-- 设置工作负载（永不过期）
		redis.call('SET', workloadKey, workload)
		-- 更新 ZSet
		redis.call('ZADD', zsetKey, workload, agentID)
		
		return workload
	`

	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()

	_, err := r.evalLua(ctx, luaScript, []string{workloadKey, zsetKey}, agentID, workload)
	return err
}

// SetAgentWorkload 设置客服负载（强制覆盖，同步到 Redis 和 DB）
func (r *RedisWorkloadRepository) SetAgentWorkload(ctx context.Context, agentID string, workload int64) error {
	if err := r.setWorkloadAndSync(ctx, agentID, workload); err != nil {
		return errorx.WrapError("failed to set agent workload in redis", err)
	}

	// 异步同步到 DB
	r.asyncSyncToDB(agentID, workload)

	r.logger.Debugf("✅ 已设置客服 %s 工作负载: %d", agentID, workload)
	return nil
}

// GetAgentWorkload 获取客服负载（优先从 Redis 读取）
func (r *RedisWorkloadRepository) GetAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	workloadKey := r.GetWorkloadKey(agentID)

	// 1. 先从 Redis 获取
	workloadStr, err := r.client.Get(ctx, workloadKey).Result()
	if err == nil {
		roundNone := convert.RoundNone
		workload, _ := convert.MustIntT[int64](workloadStr, &roundNone)
		return workload, nil
	}

	// 2. Redis 未命中，从 DB 加载
	if err == redis.Nil && r.db != nil {
		var dbModel AgentWorkloadModel
		if err := r.db.WithContext(ctx).Where("agent_id = ?", agentID).First(&dbModel).Error; err == nil {
			// 回写到 Redis
			if err := r.client.Set(ctx, workloadKey, dbModel.Workload, 0).Err(); err != nil {
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

// IncrementAgentWorkload 增加客服负载
func (r *RedisWorkloadRepository) IncrementAgentWorkload(ctx context.Context, agentID string) error {
	// 使用 Lua 脚本保证原子性
	luaScript := `
		local workloadKey = KEYS[1]
		local zsetKey = KEYS[2]
		local agentID = ARGV[1]
		
		-- 递增工作负载
		local newWorkload = redis.call('INCR', workloadKey)
		-- 更新 ZSet
		redis.call('ZINCRBY', zsetKey, 1, agentID)

		return newWorkload
	`

	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()

	result, err := r.evalLua(ctx, luaScript, []string{workloadKey, zsetKey}, agentID)
	if err != nil {
		return errorx.WrapError("failed to increment agent workload", err)
	}

	newWorkload := r.parseWorkloadResult(result)

	// 异步同步到 DB
	r.asyncSyncToDB(agentID, newWorkload)

	r.logger.Debugf("📈 客服 %s 工作负载增加至: %d", agentID, newWorkload)
	return nil
}

// DecrementAgentWorkload 减少客服负载（不低于 0）
func (r *RedisWorkloadRepository) DecrementAgentWorkload(ctx context.Context, agentID string) error {
	// 使用 Lua 脚本保证原子性，且不低于0
	luaScript := `
		local workloadKey = KEYS[1]
		local zsetKey = KEYS[2]
		local agentID = ARGV[1]
		
		-- 递减工作负载
		local newWorkload = redis.call('DECR', workloadKey)
		
		-- 如果小于0，重置为0
		if newWorkload < 0 then
			newWorkload = 0
			redis.call('SET', workloadKey, 0)
			redis.call('ZADD', zsetKey, 0, agentID)
		else
			-- 更新 ZSet
			redis.call('ZINCRBY', zsetKey, -1, agentID)
		end
		
		return newWorkload
	`

	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()

	result, err := r.evalLua(ctx, luaScript, []string{workloadKey, zsetKey}, agentID)
	if err != nil {
		return errorx.WrapError("failed to decrement agent workload", err)
	}

	finalWorkload := r.parseWorkloadResult(result)

	// 异步同步到 DB
	r.asyncSyncToDB(agentID, finalWorkload)

	r.logger.Debugf("📉 客服 %s 工作负载减少至: %d", agentID, finalWorkload)
	return nil
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

// SyncAgentWorkloadToZSet 从 string key 同步负载到 ZSet
func (r *RedisWorkloadRepository) SyncAgentWorkloadToZSet(ctx context.Context, agentID string) error {
	workload, found, err := r.getWorkloadAndSync(ctx, agentID)
	if err != nil {
		return errorx.WrapError("failed to sync agent workload to zset", err)
	}

	// 如果 Redis 中没有，设置为 0
	if !found {
		workload = 0
		if err := r.setWorkloadAndSync(ctx, agentID, workload); err != nil {
			return errorx.WrapError("failed to set default workload", err)
		}
	}

	r.logger.Debugf("🔄 客服 %s 重新加入，同步负载到 ZSet: %d", agentID, workload)
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

// asyncSyncToDB 异步同步负载到 DB
func (r *RedisWorkloadRepository) asyncSyncToDB(agentID string, workload int64) {
	if r.db == nil {
		return
	}

	syncx.Go().WithTimeout(2 * time.Second).OnError(func(err error) {
		r.logger.Warnf("⚠️ 同步负载到 DB 失败: %v", err)
	}).ExecWithContext(func(ctx context.Context) error {
		// 使用 Upsert 避免并发场景下的重复键错误
		return r.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "agent_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"workload", "updated_at"}),
		}).Create(&AgentWorkloadModel{
			AgentID:  agentID,
			Workload: workload,
		}).Error
	})
}
