/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-18 09:00:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:23:08
 * @FilePath: \go-wsc\repository\workload_repository.go
 * @Description: 客服负载管理 - 支持 Redis 分布式存储
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
	"github.com/kamalyes/go-toolbox/pkg/random"
	"github.com/redis/go-redis/v9"
)

// WorkloadInfo 负载信息
type WorkloadInfo struct {
	AgentID      string    `json:"agent_id"`    // 客服ID
	Workload     int64     `json:"workload"`    // 当前工作负载
	LastUpdateAt time.Time `json:"last_update"` // 最后更新时间
}

// WorkloadRepository 负载管理仓库接口
type WorkloadRepository interface {
	// SetAgentWorkload 设置客服工作负载
	SetAgentWorkload(ctx context.Context, agentID string, workload int64) error

	// GetAgentWorkload 获取客服工作负载
	GetAgentWorkload(ctx context.Context, agentID string) (int64, error)

	// IncrementAgentWorkload 增加客服工作负载
	IncrementAgentWorkload(ctx context.Context, agentID string) error

	// DecrementAgentWorkload 减少客服工作负载
	DecrementAgentWorkload(ctx context.Context, agentID string) error

	// GetLeastLoadedAgent 获取负载最小的在线客服
	GetLeastLoadedAgent(ctx context.Context, onlineAgents []string) (string, int64, error)

	// RemoveAgentWorkload 移除客服负载记录（客服离线时调用）
	RemoveAgentWorkload(ctx context.Context, agentID string) error

	// SyncAgentWorkloadToZSet 客服重新加入时，从单个key同步负载到ZSet
	SyncAgentWorkloadToZSet(ctx context.Context, agentID string) error

	// GetAllAgentWorkloads 获取所有客服的负载信息
	GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error)

	// BatchSetAgentWorkload 批量设置客服负载
	BatchSetAgentWorkload(ctx context.Context, workloads map[string]int64) error

	// Close 关闭仓库，停止后台任务
	Close() error
}

// RedisWorkloadRepository Redis 实现
type RedisWorkloadRepository struct {
	client    *redis.Client
	keyPrefix string         // key 前缀
	logger    logger.ILogger // 日志记录器
}

// NewRedisWorkloadRepository 创建 Redis 负载管理仓库
// 参数:
//   - client: Redis 客户端 (github.com/redis/go-redis/v9)
//   - config: 负载管理配置对象
//   - log: 日志记录器
func NewRedisWorkloadRepository(client *redis.Client, config *wscconfig.Workload, log logger.ILogger) WorkloadRepository {
	keyPrefix := mathx.IF(config.KeyPrefix == "", DefaultWorkloadKeyPrefix, config.KeyPrefix)

	repo := &RedisWorkloadRepository{
		client:    client,
		keyPrefix: keyPrefix,
		logger:    log,
	}

	return repo
}

// GetWorkloadKey 获取客服负载的 key
func (r *RedisWorkloadRepository) GetWorkloadKey(agentID string) string {
	return fmt.Sprintf("%sagent:%s", r.keyPrefix, agentID)
}

// GetZSetKey 获取 ZSet key
func (r *RedisWorkloadRepository) GetZSetKey() string {
	return fmt.Sprintf("%szset", r.keyPrefix)
}

// SetAgentWorkload 设置客服工作负载
func (r *RedisWorkloadRepository) SetAgentWorkload(ctx context.Context, agentID string, workload int64) error {
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

	_, err := r.client.Eval(ctx, luaScript, []string{workloadKey, zsetKey}, agentID, workload).Result()
	if err != nil {
		return errorx.WrapError("failed to set agent workload", err)
	}

	r.logger.Debugf("✅ 已设置客服 %s 工作负载: %d", agentID, workload)
	return nil
}

// GetAgentWorkload 获取客服工作负载
func (r *RedisWorkloadRepository) GetAgentWorkload(ctx context.Context, agentID string) (int64, error) {
	workloadKey := r.GetWorkloadKey(agentID)

	workloadStr, err := r.client.Get(ctx, workloadKey).Result()
	if err != nil {
		// 缓存未命中时返回0
		if err == redis.Nil {
			return 0, nil
		}
		return 0, errorx.WrapError("failed to get agent workload", err)
	}

	workload, err := convert.MustIntT[int64](workloadStr, nil)
	if err != nil {
		return 0, errorx.WrapError("failed to parse agent workload", err)
	}

	return workload, nil
}

// IncrementAgentWorkload 增加客服工作负载
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

	result, err := r.client.Eval(ctx, luaScript, []string{workloadKey, zsetKey}, agentID).Result()
	if err != nil {
		return errorx.WrapError("failed to increment agent workload", err)
	}

	var newWorkload int64
	switch v := result.(type) {
	case int64:
		newWorkload = v
	case float64:
		newWorkload = int64(v)
	}

	r.logger.Debugf("📈 客服 %s 工作负载增加至: %d", agentID, newWorkload)
	return nil
}

// DecrementAgentWorkload 减少客服工作负载
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

	result, err := r.client.Eval(ctx, luaScript, []string{workloadKey, zsetKey}, agentID).Result()
	if err != nil {
		return errorx.WrapError("failed to decrement agent workload", err)
	}

	var finalWorkload int64
	switch v := result.(type) {
	case int64:
		finalWorkload = v
	case float64:
		finalWorkload = int64(v)
	}

	r.logger.Debugf("📉 客服 %s 工作负载减少至: %d", agentID, finalWorkload)
	return nil
}

// GetLeastLoadedAgent 获取负载最小的在线客服(使用Sorted Set O(log(N)+M)复杂度)
func (r *RedisWorkloadRepository) GetLeastLoadedAgent(ctx context.Context, onlineAgents []string) (string, int64, error) {
	if len(onlineAgents) == 0 {
		return "", 0, errorx.WrapError("no online agents available")
	}

	// 使用 Lua 脚本在 Redis 端完成筛选和随机选择，减少网络传输
	// 当多个客服负载相同时，在它们之间随机选择，实现真正的负载均衡
	luaScript := `
		local zsetKey = KEYS[1]
		local onlineAgents = {}
		
		-- 构建在线客服集合
		for i = 1, #ARGV do
			onlineAgents[ARGV[i]] = true
		end
		
		-- 获取前50个最低负载的客服（平衡性能和命中率）
		local results = redis.call('ZRANGE', zsetKey, 0, 49, 'WITHSCORES')
		
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
		
		-- 如果找到候选客服，从中随机选择一个
		if #candidateAgents > 0 then
			local randomIndex = math.random(1, #candidateAgents)
			return {candidateAgents[randomIndex], minWorkload}
		end
		
		-- 如果ZSet中没有找到，返回空
		return nil
	`

	zsetKey := r.GetZSetKey()

	// 准备参数：所有在线客服ID
	args := make([]interface{}, len(onlineAgents))
	for i, agentID := range onlineAgents {
		args[i] = agentID
	}

	// 执行 Lua 脚本
	result, err := r.client.Eval(ctx, luaScript, []string{zsetKey}, args...).Result()
	if err != nil && err != redis.Nil {
		return "", 0, errorx.WrapError("failed to get least loaded agent from zset", err)
	}

	// 解析结果
	if result != nil {
		if resultArray, ok := result.([]any); ok && len(resultArray) == 2 {
			agentID := resultArray[0].(string)
			var workload int64
			// Redis Lua 返回的数字可能是 int64 或 float64
			switch v := resultArray[1].(type) {
			case int64:
				workload = v
			case float64:
				workload = int64(v)
			default:
				r.logger.Warnf("⚠️ 无法解析负载值类型: %T", v)
			}
			r.logger.Debugf("🎯 从同负载客服中随机选择: %s (负载: %d)", agentID, workload)
			return agentID, workload, nil
		}
	}

	// 如果ZSet中没有找到，可能是新客服或ZSet未同步，降级为随机选择一个在线客服
	randomIndex := random.RandInt(0, len(onlineAgents)-1)
	selectedAgent := onlineAgents[randomIndex]
	workload, _ := r.GetAgentWorkload(ctx, selectedAgent)
	r.logger.Debugf("⚠️ ZSet中未找到在线客服，降级随机选择: %s (负载: %d)", selectedAgent, workload)

	// 同步到ZSet
	if err := r.client.ZAdd(ctx, zsetKey, redis.Z{
		Score:  float64(workload),
		Member: selectedAgent,
	}).Err(); err != nil {
		r.logger.Warnf("⚠️ 同步ZSet失败: %v", err)
	}

	return selectedAgent, workload, nil
}

// RemoveAgentWorkload 从负载ZSet中移除客服（客服离线时调用）
// 只移除ZSet记录，保留单个key以便重新上线时恢复
func (r *RedisWorkloadRepository) RemoveAgentWorkload(ctx context.Context, agentID string) error {
	// 使用 Lua 脚本保证原子性
	luaScript := `
		local zsetKey = KEYS[1]
		local agentID = ARGV[1]
		
		-- 只从ZSet中移除,保留单个key
		redis.call('ZREM', zsetKey, agentID)
		return 1
	`

	zsetKey := r.GetZSetKey()

	_, err := r.client.Eval(ctx, luaScript, []string{zsetKey}, agentID).Result()
	if err != nil {
		return errorx.WrapError("failed to remove agent from zset", err)
	}

	r.logger.Debugf("🗑️ 已从负载ZSet移除客服（保留单个key以便恢复）: %s", agentID)
	return nil
}

// SyncAgentWorkloadToZSet 客服重新加入时，从单个key同步负载到ZSet
// 场景：客服离线后删除了单个key和ZSet记录，重新上线时需要恢复
// 如果单个key不存在，则初始化为0并添加到ZSet
func (r *RedisWorkloadRepository) SyncAgentWorkloadToZSet(ctx context.Context, agentID string) error {
	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()

	// 使用 Lua 脚本保证原子性
	luaScript := `
		local workloadKey = KEYS[1]
		local zsetKey = KEYS[2]
		local agentID = ARGV[1]
		
		-- 获取单个key中的负载值
		local workload = redis.call('GET', workloadKey)
		
		if workload then
			-- 如果单个key存在,同步到ZSet
			redis.call('ZADD', zsetKey, tonumber(workload), agentID)
			return tonumber(workload)
		end
		
		-- 如果单个key不存在,初始化为0并添加到ZSet
		redis.call('ZADD', zsetKey, 0, agentID)
		return 0
	`

	result, err := r.client.Eval(ctx, luaScript, []string{workloadKey, zsetKey}, agentID).Result()
	if err != nil {
		return errorx.WrapError("failed to sync agent workload to zset", err)
	}

	var workload int64
	switch v := result.(type) {
	case int64:
		workload = v
	case float64:
		workload = int64(v)
	}

	r.logger.Debugf("🔄 客服 %s 重新加入,从单个key同步负载到ZSet: %d", agentID, workload)

	return nil
}

// BatchRemoveAgentWorkload 批量移除客服负载
func (r *RedisWorkloadRepository) BatchRemoveAgentWorkload(ctx context.Context, agentIDs []string) error {
	if len(agentIDs) == 0 {
		return nil
	}

	// 使用 Lua 脚本批量删除
	luaScript := `
		local prefix = ARGV[1]
		local zsetKey = prefix .. "zset"
		
		for i = 2, #ARGV do
			local agentID = ARGV[i]
			local workloadKey = prefix .. "agent:" .. agentID
			redis.call('DEL', workloadKey)
			redis.call('ZREM', zsetKey, agentID)
		end
		
		return #ARGV - 1
	`

	args := []any{r.keyPrefix}
	for _, agentID := range agentIDs {
		args = append(args, agentID)
	}

	result, err := r.client.Eval(ctx, luaScript, []string{}, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to batch remove agent workloads", err)
	}

	r.logger.Debugf("🗑️ 批量移除 %v 个客服负载", result)
	return nil
}

// GetAllAgentWorkloads 获取所有客服的负载信息
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

	// 执行 Lua 脚本
	result, err := r.client.Eval(ctx, luaScript, []string{}, args...).Result()
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
