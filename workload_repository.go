/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-18
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-18 18:10:55
 * @FilePath: \go-wsc\workload_repository.go
 * @Description: 客服负载管理 - 支持 Redis 分布式存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/random"
	"github.com/redis/go-redis/v9"
)

// 日志消息常量
const (
	logMsgUpdateZSetFailed = "⚠️ 更新客服负载ZSet失败: %v"
)

// WorkloadInfo 负载信息
type WorkloadInfo struct {
	AgentID      string    `json:"agent_id"`     // 客服ID
	Workload     int64     `json:"workload"`     // 当前工作负载
	MaxWorkload  int       `json:"max_workload"` // 最大工作负载
	LastUpdateAt time.Time `json:"last_update"`  // 最后更新时间
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

	// GetAllAgentWorkloads 获取所有客服的负载信息
	GetAllAgentWorkloads(ctx context.Context, limit int64) ([]WorkloadInfo, error)

	// BatchSetAgentWorkload 批量设置客服负载
	BatchSetAgentWorkload(ctx context.Context, workloads map[string]int64) error
}

// RedisWorkloadRepository Redis 实现
type RedisWorkloadRepository struct {
	client     *redis.Client
	keyPrefix  string         // key 前缀
	defaultTTL time.Duration  // 默认过期时间
	logger     logger.ILogger // 日志记录器
}

// NewRedisWorkloadRepository 创建 Redis 负载管理仓库
// 参数:
//   - client: Redis 客户端 (github.com/redis/go-redis/v9)
//   - keyPrefix: key 前缀，默认为 "wsc:workload:"
//   - ttl: 过期时间，建议设置为 72 小时（保留历史数据）
func NewRedisWorkloadRepository(client *redis.Client, keyPrefix string, ttl time.Duration) WorkloadRepository {
	keyPrefix = mathx.IF(keyPrefix == "", "wsc:workload:", keyPrefix)
	ttl = mathx.IF(ttl == 0, 72*time.Hour, ttl) // 默认保留3天

	return &RedisWorkloadRepository{
		client:     client,
		keyPrefix:  keyPrefix,
		defaultTTL: ttl,
		logger:     NewDefaultWSCLogger(),
	}
}

// SetLogger 设置日志记录器
func (r *RedisWorkloadRepository) SetLogger(logger logger.ILogger) {
	r.logger = logger
}

// GetTodayKey 获取今天的日期键（格式：20251218）
func (r *RedisWorkloadRepository) GetTodayKey() string {
	return time.Now().Format("20060102")
}

// GetWorkloadKey 获取客服负载的 key（包含日期）
func (r *RedisWorkloadRepository) GetWorkloadKey(agentID string) string {
	dateKey := r.GetTodayKey()
	return fmt.Sprintf("%s%s:agent:%s", r.keyPrefix, dateKey, agentID)
}

// GetZSetKey 获取今天的 ZSet key
func (r *RedisWorkloadRepository) GetZSetKey() string {
	dateKey := r.GetTodayKey()
	return fmt.Sprintf("%s%s:zset", r.keyPrefix, dateKey)
}

// SetAgentWorkload 设置客服工作负载
func (r *RedisWorkloadRepository) SetAgentWorkload(ctx context.Context, agentID string, workload int64) error {
	// 使用 Lua 脚本保证原子性
	luaScript := `
		local workloadKey = KEYS[1]
		local zsetKey = KEYS[2]
		local agentID = ARGV[1]
		local workload = tonumber(ARGV[2])
		local ttl = tonumber(ARGV[3])
		
		-- 设置工作负载
		redis.call('SET', workloadKey, workload, 'EX', ttl)
		-- 更新 ZSet
		redis.call('ZADD', zsetKey, workload, agentID)
		
		return workload
	`

	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()
	ttlSeconds := int64(r.defaultTTL.Seconds())

	_, err := r.client.Eval(ctx, luaScript, []string{workloadKey, zsetKey}, agentID, workload, ttlSeconds).Result()
	if err != nil {
		return fmt.Errorf("failed to set agent workload: %w", err)
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
		return 0, fmt.Errorf("failed to get agent workload: %w", err)
	}

	workload, err := strconv.ParseInt(workloadStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse agent workload: %w", err)
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
		local ttl = tonumber(ARGV[2])
		
		-- 递增工作负载
		local newWorkload = redis.call('INCR', workloadKey)
		-- 刷新TTL
		redis.call('EXPIRE', workloadKey, ttl)
		-- 更新 ZSet
		redis.call('ZINCRBY', zsetKey, 1, agentID)
		
		return newWorkload
	`

	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()
	ttlSeconds := int64(r.defaultTTL.Seconds())

	result, err := r.client.Eval(ctx, luaScript, []string{workloadKey, zsetKey}, agentID, ttlSeconds).Result()
	if err != nil {
		return fmt.Errorf("failed to increment agent workload: %w", err)
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
		local ttl = tonumber(ARGV[2])
		
		-- 递减工作负载
		local newWorkload = redis.call('DECR', workloadKey)
		
		-- 如果小于0，重置为0
		if newWorkload < 0 then
			newWorkload = 0
			redis.call('SET', workloadKey, 0, 'EX', ttl)
			redis.call('ZADD', zsetKey, 0, agentID)
		else
			-- 更新 ZSet
			redis.call('ZINCRBY', zsetKey, -1, agentID)
		end
		
		return newWorkload
	`

	workloadKey := r.GetWorkloadKey(agentID)
	zsetKey := r.GetZSetKey()
	ttlSeconds := int64(r.defaultTTL.Seconds())

	result, err := r.client.Eval(ctx, luaScript, []string{workloadKey, zsetKey}, agentID, ttlSeconds).Result()
	if err != nil {
		return fmt.Errorf("failed to decrement agent workload: %w", err)
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
		return "", 0, fmt.Errorf("no online agents available")
	}

	// 使用 Lua 脚本在 Redis 端完成筛选，减少网络传输
	luaScript := `
		local zsetKey = KEYS[1]
		local onlineAgents = {}
		
		-- 构建在线客服集合
		for i = 1, #ARGV do
			onlineAgents[ARGV[i]] = true
		end
		
		-- 获取前50个最低负载的客服（平衡性能和命中率）
		local results = redis.call('ZRANGE', zsetKey, 0, 49, 'WITHSCORES')
		
		-- 遍历结果，找到第一个在线的客服
		for i = 1, #results, 2 do
			local agentID = results[i]
			local workload = tonumber(results[i+1])
			
			if onlineAgents[agentID] then
				return {agentID, workload}
			end
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
		return "", 0, fmt.Errorf("failed to get least loaded agent from zset: %w", err)
	}

	// 解析结果
	if result != nil {
		if resultArray, ok := result.([]interface{}); ok && len(resultArray) == 2 {
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
			r.logger.Debugf("🎯 通过Lua脚本快速找到负载最小的在线客服: %s (负载: %d)", agentID, workload)
			return agentID, workload, nil
		}
	}

	// 如果ZSet中没有找到，可能是新客服或ZSet未同步，降级为随机选择一个在线客服
	randomIndex := random.RandInt(0, len(onlineAgents)-1)
	selectedAgent := onlineAgents[randomIndex]
	workload, _ := r.GetAgentWorkload(ctx, selectedAgent)
	r.logger.Debugf("⚠️ ZSet中未找到在线客服，随机选择: %s (负载: %d)", selectedAgent, workload)

	// 同步到ZSet
	r.client.ZAdd(ctx, zsetKey, redis.Z{
		Score:  float64(workload),
		Member: selectedAgent,
	})

	return selectedAgent, workload, nil
}

// RemoveAgentWorkload 从负载ZSet中移除客服并删除工作负载key(客服离线时调用)
func (r *RedisWorkloadRepository) RemoveAgentWorkload(ctx context.Context, agentID string) error {
	// 删除工作负载key
	workloadKey := r.GetWorkloadKey(agentID)
	if err := r.client.Del(ctx, workloadKey).Err(); err != nil {
		r.logger.Warnf("⚠️ 删除客服工作负载key失败: %v", err)
	}

	// 从ZSet中移除
	zsetKey := r.GetZSetKey()
	err := r.client.ZRem(ctx, zsetKey, agentID).Err()
	if err != nil {
		return fmt.Errorf("failed to remove agent from workload zset: %w", err)
	}
	r.logger.Debugf("🗑️ 已从负载ZSet移除客服并清理工作负载: %s", agentID)
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
		return nil, fmt.Errorf("failed to get agent workloads: %w", err)
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
		local dateKey = ARGV[2]
		local ttl = tonumber(ARGV[3])
		local zsetKey = prefix .. dateKey .. ":zset"
		
		-- 从 ARGV[4] 开始是 agentID:workload 对
		for i = 4, #ARGV, 2 do
			local agentID = ARGV[i]
			local workload = tonumber(ARGV[i+1])
			local workloadKey = prefix .. dateKey .. ":agent:" .. agentID
			
			-- 设置工作负载
			redis.call('SET', workloadKey, workload, 'EX', ttl)
			-- 更新 ZSet
			redis.call('ZADD', zsetKey, workload, agentID)
		end
		
		return #ARGV / 2 - 1
	`

	// 准备参数
	dateKey := r.GetTodayKey()
	ttlSeconds := int64(r.defaultTTL.Seconds())
	args := []interface{}{r.keyPrefix, dateKey, ttlSeconds}

	for agentID, workload := range workloads {
		args = append(args, agentID, workload)
	}

	// 执行 Lua 脚本
	result, err := r.client.Eval(ctx, luaScript, []string{}, args...).Result()
	if err != nil {
		return fmt.Errorf("failed to batch set agent workloads: %w", err)
	}

	r.logger.Debugf("✅ 批量设置 %v 个客服负载", result)
	return nil
}
