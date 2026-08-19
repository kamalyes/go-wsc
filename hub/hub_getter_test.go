/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 10:06:20
 * @FilePath: \go-wsc\hub\hub_getter_test.go
 * @Description: Hub getter/setter 方法测试 - 覆盖所有 0% 覆盖率的 getter/setter
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/middleware"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
)

// ============================================================================
// Mock 实现
// ============================================================================

type fakeConnectionTokenDecoder struct{}

func (f *fakeConnectionTokenDecoder) Decode(_ *http.Request) (*ConnectionClaims, error) {
	return nil, nil
}

type fakeIDGenerator struct{}

func (f *fakeIDGenerator) GenerateTraceID() string       { return "trace-id" }
func (f *fakeIDGenerator) GenerateSpanID() string        { return "span-id" }
func (f *fakeIDGenerator) GenerateRequestID() string     { return "req-id" }
func (f *fakeIDGenerator) GenerateCorrelationID() string { return "corr-id" }

type fakeWelcomeProvider struct{}

func (f *fakeWelcomeProvider) GetWelcomeMessage(_ string, _ models.UserRole, _ models.UserType, _ map[string]interface{}) (*models.WelcomeMessage, bool, error) {
	return nil, false, nil
}
func (f *fakeWelcomeProvider) RefreshConfig() error { return nil }

type fakePoolManager struct{}

func (f *fakePoolManager) GetSMTPClient() interface{} { return nil }

type fakeOfflineMessageHandler struct{}

func (f *fakeOfflineMessageHandler) StoreOfflineMessage(_ context.Context, _ string, _ *HubMessage) error {
	return nil
}
func (f *fakeOfflineMessageHandler) DrainOfflineQueue(_ context.Context, _ string, _ int) ([]*HubMessage, error) {
	return nil, nil
}
func (f *fakeOfflineMessageHandler) GetOfflineMessages(_ context.Context, _ string, _ int, _ string) ([]*HubMessage, string, error) {
	return nil, "", nil
}
func (f *fakeOfflineMessageHandler) DeleteOfflineMessages(_ context.Context, _ string, _ []string) error {
	return nil
}
func (f *fakeOfflineMessageHandler) GetOfflineMessageCount(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (f *fakeOfflineMessageHandler) ClearOfflineMessages(_ context.Context, _ string, _ []string) error {
	return nil
}
func (f *fakeOfflineMessageHandler) UpdatePushStatus(_ context.Context, _ []string, _ error) error {
	return nil
}

type fakeGroupRepository struct{}

func (f *fakeGroupRepository) CreateGroup(_ context.Context, _ *Group) error { return nil }
func (f *fakeGroupRepository) GetGroup(_ context.Context, _, _ string) (*Group, error) {
	return nil, nil
}
func (f *fakeGroupRepository) DisbandGroup(_ context.Context, _, _ string) error { return nil }
func (f *fakeGroupRepository) AddMembers(_ context.Context, _, _ string, _ []string) error {
	return nil
}
func (f *fakeGroupRepository) RemoveMembers(_ context.Context, _, _ string, _ []string) error {
	return nil
}
func (f *fakeGroupRepository) GetMembers(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeGroupRepository) GetUserGroups(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeGroupRepository) IsMember(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}
func (f *fakeGroupRepository) GetMemberCount(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}
func (f *fakeGroupRepository) GetNamespaceGroups(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeGroupRepository) GetAllNamespaces(_ context.Context) ([]string, error) { return nil, nil }
func (f *fakeGroupRepository) GetMultiGroupMembers(_ context.Context, _ string, _ []string) (map[string][]string, error) {
	return nil, nil
}
func (f *fakeGroupRepository) EnsureSystemGroup(_ context.Context, _, _ string) error { return nil }
func (f *fakeGroupRepository) GetGroupNamespace(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (f *fakeGroupRepository) GetMultiGroupNamespaces(_ context.Context, _ []string) (map[string]string, error) {
	return nil, nil
}

type fakeMessageRecordRepository struct{}

func (f *fakeMessageRecordRepository) Create(_ context.Context, _ *repository.MessageSendRecord) error {
	return nil
}
func (f *fakeMessageRecordRepository) Update(_ context.Context, _ *repository.MessageSendRecord) error {
	return nil
}
func (f *fakeMessageRecordRepository) FindByID(_ context.Context, _ uint) (*repository.MessageSendRecord, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepository) FindByMessageID(_ context.Context, _ string) (*repository.MessageSendRecord, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepository) QueryRecords(_ context.Context, _ *repository.MessageRecordFilter) ([]*repository.MessageSendRecord, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepository) FindRetryable(_ context.Context, _ int) ([]*repository.MessageSendRecord, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepository) DeleteExpired(_ context.Context) (int64, error) { return 0, nil }
func (f *fakeMessageRecordRepository) Delete(_ context.Context, _ uint) error         { return nil }
func (f *fakeMessageRecordRepository) DeleteByMessageID(_ context.Context, _ string) error {
	return nil
}
func (f *fakeMessageRecordRepository) UpdateStatus(_ context.Context, _ string, _ models.MessageSendStatus, _ models.FailureReason, _ string) error {
	return nil
}
func (f *fakeMessageRecordRepository) BatchUpdateStatus(_ context.Context, _ []string, _ models.MessageSendStatus, _ models.FailureReason, _ string) error {
	return nil
}
func (f *fakeMessageRecordRepository) ClaimStaleSending(_ context.Context, _ []string, _ models.MessageSendStatus, _ models.FailureReason, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepository) IncrementRetry(_ context.Context, _ string, _ models.RetryAttempt) error {
	return nil
}
func (f *fakeMessageRecordRepository) GetStatistics(_ context.Context) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepository) CleanupOld(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeMessageRecordRepository) GetDB() *gorm.DB { return nil }
func (f *fakeMessageRecordRepository) Close() error    { return nil }

type fakeConnectionRecordRepository struct{}

func (f *fakeConnectionRecordRepository) Close() error                                                                                                 { return nil }
func (f *fakeConnectionRecordRepository) Upsert(_ context.Context, _ *models.ConnectionRecord) error                                                 { return nil }
func (f *fakeConnectionRecordRepository) MarkDisconnected(_ context.Context, _ string, _ models.DisconnectReason, _ int, _ string) error             { return nil }
func (f *fakeConnectionRecordRepository) GetByConnectionID(_ context.Context, _ string) (*models.ConnectionRecord, error)                             { return nil, nil }
func (f *fakeConnectionRecordRepository) GetByUserID(_ context.Context, _ string) ([]*models.ConnectionRecord, error)                                 { return nil, nil }
func (f *fakeConnectionRecordRepository) GetActiveByUserID(_ context.Context, _ string) ([]*models.ConnectionRecord, error)                           { return nil, nil }
func (f *fakeConnectionRecordRepository) AddError(_ context.Context, _ string, _ error) error                                                         { return nil }
func (f *fakeConnectionRecordRepository) BatchUpdateHeartbeats(_ context.Context, _ []*repository.HeartbeatUpdateEntry) error                         { return nil }
func (f *fakeConnectionRecordRepository) BatchIncrementStats(_ context.Context, _ []*repository.StatsIncrementEntry) error                            { return nil }
func (f *fakeConnectionRecordRepository) List(_ context.Context, _ *repository.ConnectionQueryOptions) ([]*models.ConnectionRecord, error)           { return nil, nil }
func (f *fakeConnectionRecordRepository) Count(_ context.Context, _ *repository.ConnectionQueryOptions) (int64, error)                                { return 0, nil }
func (f *fakeConnectionRecordRepository) GetConnectionStats(_ context.Context, _, _ time.Time) (*repository.ConnectionStats, error)                   { return nil, nil }
func (f *fakeConnectionRecordRepository) GetConnectionStatsByID(_ context.Context, _ string) (*repository.UserConnectionStats, error)                 { return nil, nil }
func (f *fakeConnectionRecordRepository) GetUserConnectionStats(_ context.Context, _ string) (*repository.UserConnectionStats, error)                 { return nil, nil }
func (f *fakeConnectionRecordRepository) GetNodeConnectionStats(_ context.Context, _ string) (*repository.NodeConnectionStats, error)                 { return nil, nil }
func (f *fakeConnectionRecordRepository) GetHighErrorRateConnections(_ context.Context, _ int, _ int) ([]*models.ConnectionRecord, error)             { return nil, nil }
func (f *fakeConnectionRecordRepository) GetFrequentReconnectConnections(_ context.Context, _ int, _ int) ([]*models.ConnectionRecord, error)         { return nil, nil }
func (f *fakeConnectionRecordRepository) BatchUpsert(_ context.Context, _ []*models.ConnectionRecord) error                                           { return nil }
func (f *fakeConnectionRecordRepository) CleanupInactiveRecords(_ context.Context, _ time.Time) (int64, error)                                        { return 0, nil }
func (f *fakeConnectionRecordRepository) WithTableName(_ string) ConnectionRecordRepository                                                           { return f }

type fakeHubStatsRepository struct {
	nodeStats    *repository.NodeStats
	nodeStatsErr error
}

func (f *fakeHubStatsRepository) UpdateConnectionStats(_ context.Context, _ string, _ int64) error {
	return nil
}
func (f *fakeHubStatsRepository) IncrementTotalConnections(_ context.Context, _ string, _ int64) error {
	return nil
}
func (f *fakeHubStatsRepository) SetActiveConnections(_ context.Context, _ string, _ int64) error {
	return nil
}
func (f *fakeHubStatsRepository) IncrementMessagesSent(_ context.Context, _ string, _ int64) error {
	return nil
}
func (f *fakeHubStatsRepository) IncrementMessagesReceived(_ context.Context, _ string, _ int64) error {
	return nil
}
func (f *fakeHubStatsRepository) IncrementBroadcastsSent(_ context.Context, _ string, _ int64) error {
	return nil
}
func (f *fakeHubStatsRepository) RegisterNode(_ context.Context, _ string, _ int64) error { return nil }
func (f *fakeHubStatsRepository) GetNodeStats(_ context.Context, _ string) (*repository.NodeStats, error) {
	return f.nodeStats, f.nodeStatsErr
}
func (f *fakeHubStatsRepository) GetAllNodesStats(_ context.Context) (map[string]*repository.NodeStats, error) {
	return nil, nil
}
func (f *fakeHubStatsRepository) GetTotalStats(_ context.Context) (*repository.ClusterStats, error) {
	return nil, nil
}
func (f *fakeHubStatsRepository) CleanupNodeStats(_ context.Context, _ string) error    { return nil }
func (f *fakeHubStatsRepository) UpdateNodeHeartbeat(_ context.Context, _ string) error { return nil }
func (f *fakeHubStatsRepository) GetActiveNodes(_ context.Context, _ time.Duration) ([]string, error) {
	return nil, nil
}

// ============================================================================
// 测试辅助函数
// ============================================================================

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	hub := NewHub(config)
	t.Cleanup(func() {
		hub.Shutdown()
	})
	return hub
}

// ============================================================================
// ConnectionTokenDecoder Getter/Setter 测试
// ============================================================================

func TestHub_ConnectionTokenDecoder(t *testing.T) {
	hub := newTestHub(t)

	t.Run("初始为 nil 或默认值", func(t *testing.T) {
		decoder := hub.GetConnectionTokenDecoder()
		// 默认配置不启用 Security，所以 decoder 应为 nil
		assert.True(t, decoder == nil || hub.config.Security == nil || !hub.config.Security.ConnectionToken.IsEnabled())
	})

	t.Run("Set 后 Get 返回同一对象", func(t *testing.T) {
		fake := &fakeConnectionTokenDecoder{}
		hub.SetConnectionTokenDecoder(fake)
		assert.Same(t, fake, hub.GetConnectionTokenDecoder())
	})
}

// ============================================================================
// WorkerID Getter 测试
// ============================================================================

func TestHub_GetWorkerID(t *testing.T) {
	hub := newTestHub(t)
	workerID := hub.GetWorkerID()
	// WorkerID 应该是有效的雪花算法 worker id（>= 0）
	assert.GreaterOrEqual(t, workerID, int64(0))
}

// ============================================================================
// IDGenerator Getter/Setter 测试
// ============================================================================

func TestHub_IDGenerator(t *testing.T) {
	hub := newTestHub(t)

	t.Run("默认不为 nil 且能生成 ID", func(t *testing.T) {
		gen := hub.GetIDGenerator()
		require.NotNil(t, gen)
		assert.NotEmpty(t, gen.GenerateTraceID())
	})

	t.Run("Set 后 Get 返回同一对象", func(t *testing.T) {
		fake := &fakeIDGenerator{}
		assert.NotPanics(t, func() {
			hub.SetIDGenerator(fake)
		})
		assert.Same(t, fake, hub.GetIDGenerator())
	})
}

// ============================================================================
// Context() / GetContext() 测试
// ============================================================================

func TestHub_Context(t *testing.T) {
	hub := newTestHub(t)

	t.Run("Context() 和 GetContext() 返回同一对象", func(t *testing.T) {
		ctx1 := hub.Context()
		ctx2 := hub.GetContext()
		assert.Same(t, ctx1, ctx2)
		require.NotNil(t, ctx1)
	})

	t.Run("Context 不为 nil 且未 Done", func(t *testing.T) {
		ctx := hub.GetContext()
		require.NotNil(t, ctx)
		select {
		case <-ctx.Done():
			t.Fatal("Context 不应已取消")
		default:
		}
	})
}

// ============================================================================
// GetConfig 测试
// ============================================================================

func TestHub_GetConfig(t *testing.T) {
	hub := newTestHub(t)
	cfg := hub.GetConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, "127.0.0.1", cfg.NodeIP)
	assert.Equal(t, 18080, cfg.NodePort)
}

// ============================================================================
// GetGroupRepository 测试
// ============================================================================

func TestHub_GetGroupRepository(t *testing.T) {
	hub := newTestHub(t)

	t.Run("初始为 nil", func(t *testing.T) {
		assert.Nil(t, hub.GetGroupRepository())
	})

	t.Run("Set 后 Get 返回同一对象", func(t *testing.T) {
		fake := &fakeGroupRepository{}
		hub.SetGroupRepository(fake)
		assert.Same(t, fake, hub.GetGroupRepository())
	})
}

// ============================================================================
// SetWelcomeProvider 测试
// ============================================================================

func TestHub_SetWelcomeProvider(t *testing.T) {
	hub := newTestHub(t)
	fake := &fakeWelcomeProvider{}
	assert.NotPanics(t, func() {
		hub.SetWelcomeProvider(fake)
	})
	assert.Same(t, fake, hub.welcomeProvider)
}

// ============================================================================
// SetRateLimiter 测试
// ============================================================================

func TestHub_SetRateLimiter(t *testing.T) {
	hub := newTestHub(t)
	limiter := &middleware.RateLimiter{}
	assert.NotPanics(t, func() {
		hub.SetRateLimiter(limiter)
	})
	assert.Same(t, limiter, hub.rateLimiter)
}

// ============================================================================
// SetPoolManager 测试
// ============================================================================

func TestHub_SetPoolManager(t *testing.T) {
	hub := newTestHub(t)
	fake := &fakePoolManager{}
	assert.NotPanics(t, func() {
		hub.SetPoolManager(fake)
	})
	assert.Same(t, fake, hub.poolManager)
}

// ============================================================================
// SetPubSub / GetPubSub 测试（不真正启动 gRPC）
// ============================================================================

func TestHub_PubSub(t *testing.T) {
	hub := newTestHub(t)

	t.Run("初始为 nil", func(t *testing.T) {
		assert.Nil(t, hub.GetPubSub())
	})

	t.Run("Set nil PubSub 不 panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			hub.SetPubSub(nil)
		})
		assert.Nil(t, hub.GetPubSub())
	})
}

// ============================================================================
// 性能优化组件 Getter 测试
// ============================================================================

func TestHub_PerformanceComponents(t *testing.T) {
	hub := newTestHub(t)

	t.Run("GetWorkerPool 不为 nil", func(t *testing.T) {
		assert.NotNil(t, hub.GetWorkerPool())
	})

	t.Run("GetShardedRegistry 不为 nil", func(t *testing.T) {
		assert.NotNil(t, hub.GetShardedRegistry())
	})

	t.Run("GetRouterCache 初始为 nil（需要 PubSub + OnlineStatusRepo）", func(t *testing.T) {
		assert.Nil(t, hub.GetRouterCache())
	})
}

// ============================================================================
// gRPC 节点通信 Getter 测试
// ============================================================================

func TestHub_GRPCComponents(t *testing.T) {
	hub := newTestHub(t)

	t.Run("GetNodeRegistry 初始为 nil", func(t *testing.T) {
		assert.Nil(t, hub.GetNodeRegistry())
	})

	t.Run("GetGRPCServer 初始为 nil", func(t *testing.T) {
		assert.Nil(t, hub.GetGRPCServer())
	})

	t.Run("GetGRPCClientPool 初始为 nil", func(t *testing.T) {
		assert.Nil(t, hub.GetGRPCClientPool())
	})

	t.Run("未初始化时 IsGRPCEnabled 返回 false", func(t *testing.T) {
		assert.False(t, hub.IsGRPCEnabled())
	})
}

// ============================================================================
// generateNodeID 环境变量优先级测试
// ============================================================================

func TestGenerateNodeID(t *testing.T) {
	baseConfig := wscconfig.Default().WithNodeInfo("192.168.1.100", 9090)

	t.Run("优先级1: POD_NAME 最高优先级", func(t *testing.T) {
		t.Setenv("POD_NAME", "my-pod-abc123")
		t.Setenv("HOSTNAME", "container-host")
		t.Setenv("NODE_ID", "custom-node-id")
		assert.Equal(t, "my-pod-abc123", generateNodeID(baseConfig))
	})

	t.Run("优先级2: HOSTNAME（无 POD_NAME 时）", func(t *testing.T) {
		t.Setenv("POD_NAME", "")
		t.Setenv("HOSTNAME", "container-host")
		t.Setenv("NODE_ID", "custom-node-id")
		assert.Equal(t, "container-host", generateNodeID(baseConfig))
	})

	t.Run("优先级3: NODE_ID（无 POD_NAME 和 HOSTNAME 时）", func(t *testing.T) {
		t.Setenv("POD_NAME", "")
		t.Setenv("HOSTNAME", "")
		t.Setenv("NODE_ID", "custom-node-id")
		assert.Equal(t, "custom-node-id", generateNodeID(baseConfig))
	})

	t.Run("优先级4: IP:Port 兜底（所有环境变量为空）", func(t *testing.T) {
		t.Setenv("POD_NAME", "")
		t.Setenv("HOSTNAME", "")
		t.Setenv("NODE_ID", "")
		assert.Equal(t, "192.168.1.100-9090", generateNodeID(baseConfig))
	})

	t.Run("POD_NAME 显式空字符串走 HOSTNAME", func(t *testing.T) {
		t.Setenv("POD_NAME", "")
		t.Setenv("HOSTNAME", "fallback-host")
		t.Setenv("NODE_ID", "")
		assert.Equal(t, "fallback-host", generateNodeID(baseConfig))
	})
}

// ============================================================================
// SetMessageExpireDuration 测试
// ============================================================================

func TestHub_SetMessageExpireDuration(t *testing.T) {
	hub := newTestHub(t)

	t.Run("设置有效过期时间不 panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			hub.SetMessageExpireDuration(30 * time.Second)
		})
	})

	t.Run("设置零值不 panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			hub.SetMessageExpireDuration(0)
		})
	})

	t.Run("设置负值不 panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			hub.SetMessageExpireDuration(-1 * time.Second)
		})
	})
}

// ============================================================================
// InitializeRepositories 参数校验测试
// ============================================================================

func TestHub_InitializeRepositories_ParamValidation(t *testing.T) {
	hub := newTestHub(t)

	t.Run("redisClient=nil 返回 ErrOnlineStatusRepositoryNotSet", func(t *testing.T) {
		err := hub.InitializeRepositories(nil, &gorm.DB{})
		assert.ErrorIs(t, err, ErrOnlineStatusRepositoryNotSet)
	})

	t.Run("两个都为 nil 时优先返回 redisClient 错误", func(t *testing.T) {
		err := hub.InitializeRepositories(nil, nil)
		assert.ErrorIs(t, err, ErrOnlineStatusRepositoryNotSet)
	})
}

// ============================================================================
// SetOfflineMessageHandler / SetOfflineMessageRepo 测试
// ============================================================================

func TestHub_OfflineMessage(t *testing.T) {
	hub := newTestHub(t)
	fake := &fakeOfflineMessageHandler{}

	t.Run("SetOfflineMessageHandler 不 panic 并正确设置", func(t *testing.T) {
		assert.NotPanics(t, func() {
			hub.SetOfflineMessageHandler(fake)
		})
		assert.Same(t, fake, hub.offlineMessageHandler)
	})

	t.Run("SetOfflineMessageRepo 兼容旧接口，效果相同", func(t *testing.T) {
		fake2 := &fakeOfflineMessageHandler{}
		assert.NotPanics(t, func() {
			hub.SetOfflineMessageRepo(fake2)
		})
		assert.Same(t, fake2, hub.offlineMessageHandler)
	})
}

// ============================================================================
// Repository Setter 测试（Group / MessageRecord / ConnectionRecord）
// ============================================================================

func TestHub_RepositorySetters(t *testing.T) {
	hub := newTestHub(t)

	t.Run("SetGroupRepository 不 panic 并正确设置", func(t *testing.T) {
		fake := &fakeGroupRepository{}
		assert.NotPanics(t, func() {
			hub.SetGroupRepository(fake)
		})
		assert.Same(t, fake, hub.groupRepo)
	})

	t.Run("SetMessageRecordRepository 不 panic 并正确设置", func(t *testing.T) {
		fake := &fakeMessageRecordRepository{}
		assert.NotPanics(t, func() {
			hub.SetMessageRecordRepository(fake)
		})
		assert.Same(t, fake, hub.messageRecordRepo)
	})

	t.Run("SetConnectionRecordRepository 不 panic 并正确设置", func(t *testing.T) {
		fake := &fakeConnectionRecordRepository{}
		assert.NotPanics(t, func() {
			hub.SetConnectionRecordRepository(fake)
		})
		assert.Same(t, fake, hub.connectionRecordRepo)
	})
}

// ============================================================================
// SetHubStatsRepository 测试（不需要真实 Redis，验证不 panic 和日志输出）
// ============================================================================

func TestHub_SetHubStatsRepository(t *testing.T) {
	hub := newTestHub(t)

	t.Run("设置 fake HubStatsRepository 不 panic", func(t *testing.T) {
		fake := &fakeHubStatsRepository{}
		assert.NotPanics(t, func() {
			hub.SetHubStatsRepository(fake)
		})
		assert.Same(t, fake, hub.statsRepo)
	})

	t.Run("连续设置不 panic", func(t *testing.T) {
		fake1 := &fakeHubStatsRepository{}
		fake2 := &fakeHubStatsRepository{}
		assert.NotPanics(t, func() {
			hub.SetHubStatsRepository(fake1)
			hub.SetHubStatsRepository(fake2)
		})
		assert.Same(t, fake2, hub.statsRepo)
	})
}
