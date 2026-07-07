/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-06 21:25:39
 * @FilePath: \go-wsc\hub\router_cache.go
 * @Description: 分布式路由缓存
 *   基于 go-cachex KVCache 实现 user→nodeIDs 三层兜底缓存
 *   解决分布式路由每次查 Redis 的性能瓶颈
 *
 * 三层缓存结构：
 *   L1 本地 map（RWMutex 保护）→ 零网络开销
 *   L2 Redis Hash（HGET/HMGET）→ 1 次网络往返
 *   L3 BatchLoader（回源 online_status_repo）→ 2+ 次网络往返
 *
 * 一致性保证：
 *   - 写操作（注册/注销）后 Delete 缓存条目，PubSub 广播失效到所有节点
 *   - 在线状态仓库（online_status_repo）是 source of truth
 *   - KVCache 仅作读缓存，不存储额外状态
 *
 * 性能提升：
 *   - 本地命中：0 次网络往返（原方案 2+ 次）
 *   - Redis Hash 命中：1 次网络往返（原方案 2+ 次）
 *   - 回源场景：与原方案相同，但结果被缓存供后续使用
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-cachex"
	"github.com/redis/go-redis/v9"
)

// RouterCacheConfig 路由缓存配置
type RouterCacheConfig struct {
	// TTL 缓存过期时间（默认 5 分钟）
	TTL time.Duration
	// MaxLocalCacheSize 本地缓存最大条目数（默认 50000，约 5 万用户）
	MaxLocalCacheSize int
	// Namespace Redis 命名空间（默认 "wsc"）
	Namespace string
}

// DefaultRouterCacheConfig 默认路由缓存配置
func DefaultRouterCacheConfig() *RouterCacheConfig {
	return &RouterCacheConfig{
		TTL:               5 * time.Minute,
		MaxLocalCacheSize: 50000,
		Namespace:         "wsc",
	}
}

// RouterCache 分布式路由缓存
// 缓存 userID → []nodeIDs 映射，加速跨节点消息路由判断
type RouterCache struct {
	kv *cachex.KVCache[string, []string]
}

// NewRouterCache 创建路由缓存
// redisClient: Redis 客户端（从 PubSub.GetClient() 获取）
// onlineRepo: 在线状态仓库（用于 BatchLoader 回源查询）
// cfg: 缓存配置（nil 使用默认值）
func NewRouterCache(
	redisClient *redis.Client,
	onlineRepo OnlineStatusRepository,
	cfg *RouterCacheConfig,
) *RouterCache {
	if cfg == nil {
		cfg = DefaultRouterCacheConfig()
	}

	// BatchLoader: 缓存未命中时，回源查询 online_status_repo
	// 按 keys 批量查询用户的节点列表
	batchLoader := func(ctx context.Context, userIDs []string) (map[string][]string, error) {
		if onlineRepo == nil {
			return nil, nil
		}

		result := make(map[string][]string, len(userIDs))
		for _, userID := range userIDs {
			nodes, err := onlineRepo.GetUserNodes(ctx, userID)
			if err != nil {
				return nil, err
			}
			if len(nodes) > 0 {
				result[userID] = nodes
			}
		}
		return result, nil
	}

	// 创建 KVCache（三层兜底：本地 map → Redis Hash → BatchLoader 回源）
	kv := cachex.NewKVCache[string, []string](
		redisClient,
		"router", // 缓存名称
		nil,      // KVLoader=nil（不使用全量加载，靠 BatchLoader 按需回源）
		cachex.KVCacheConfig{
			DefaultTTL:        cfg.TTL,
			RefreshInterval:   0,             // 不自动刷新，靠 invalidation 驱动
			EnableAutoRefresh: false,         // 关闭自动刷新
			Namespace:         cfg.Namespace, // Redis 命名空间
			MaxLocalCacheSize: cfg.MaxLocalCacheSize,
			BatchLoader:       batchLoader,
		},
	)

	return &RouterCache{kv: kv}
}

// GetUserNodes 获取用户所在的所有节点
// 三层兜底：本地 map → Redis Hash → BatchLoader 回源
// 本地命中时零网络开销，大幅提升路由判断速度
func (r *RouterCache) GetUserNodes(ctx context.Context, userID string) ([]string, error) {
	if r == nil || r.kv == nil {
		return nil, nil
	}

	nodes, found, err := r.kv.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	return nodes, nil
}

// InvalidateUser 失效用户的路由缓存
// 在用户注册/注销后调用，通过 PubSub 广播到所有节点
// 下次访问时从 online_status_repo 重新加载
func (r *RouterCache) InvalidateUser(ctx context.Context, userID string) error {
	if r == nil || r.kv == nil {
		return nil
	}

	return r.kv.Delete(ctx, userID)
}

// SetUserNodes 手动设置用户的节点列表
// 用于注册时主动更新缓存，避免下次回源查询
func (r *RouterCache) SetUserNodes(ctx context.Context, userID string, nodes []string) error {
	if r == nil || r.kv == nil {
		return nil
	}

	if len(nodes) == 0 {
		return r.kv.Delete(ctx, userID)
	}

	return r.kv.Set(ctx, userID, nodes)
}

// Stop 停止路由缓存（关闭 PubSub 订阅等）
func (r *RouterCache) Stop() {
	if r == nil || r.kv == nil {
		return
	}

	r.kv.Stop()
}
