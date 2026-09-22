/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:02:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:02:00
 * @FilePath: \go-wsc\stats\fake_host_test.go
 * @Description: 统计域测试用的局部 Host 替身
 *
 * 按既定约定：子包测试用局部替身，不构造真实 Hub。替身 = 嵌入接口（未覆盖的
 * 方法保留 nil，被调用即 panic，从而暴露遗漏的依赖）+ 只覆盖本包真正用到的方法。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package stats

import (
	"context"

	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/spi"
)

// fakeHost 嵌入 Host 接口，仅覆盖测试用到的方法。
// 任何未覆盖的方法调用会因嵌入的 nil 接口而 panic —— 这是刻意的：
// 它让「测试依赖了未声明的方法」立刻暴露，而不是静默返回零值。
type fakeHost struct {
	Host

	ctx      context.Context
	logger   spi.Logger
	nodeID   string
	registry *connection.ShardedRegistry

	statsRepo   spi.HubStats
	onlineRepo  spi.OnlineStore
	qualityRepo spi.ConnectionQualityStore
	connRecRepo spi.ConnectionStore
	msgBatcher  *batcher.MessageStatsBatcher
	hbBatcher   *batcher.HeartbeatStatsUpdater
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		ctx: context.Background(),
		// 注册表与日志器是追踪方法的无条件依赖（LogClientConnection /
		// SyncClientStats / TrackHeartbeatStats 都直接读连接数并写日志），
		// 不是可选的存储后端 —— 真实 Hub 在 NewHub 里必然填好这两项。
		// 替身据此对齐构造后状态，只有「存储后端」才是可缺省的。
		logger:   spi.NewDefaultLogger(),
		registry: connection.NewShardedRegistry(false, false, connection.RegistryCapacity{}),
	}
}

func (f *fakeHost) Context() context.Context { return f.ctx }

func (f *fakeHost) GetLogger() spi.Logger { return f.logger }

func (f *fakeHost) GetNodeID() string { return f.nodeID }

func (f *fakeHost) GetShardedRegistry() *connection.ShardedRegistry { return f.registry }

func (f *fakeHost) GetStatsRepo() spi.HubStats { return f.statsRepo }

func (f *fakeHost) GetOnlineStatusRepo() spi.OnlineStore { return f.onlineRepo }

func (f *fakeHost) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	return f.qualityRepo
}

func (f *fakeHost) GetConnectionRecordRepo() spi.ConnectionStore { return f.connRecRepo }

func (f *fakeHost) GetMessageStatsBatcher() *batcher.MessageStatsBatcher { return f.msgBatcher }

func (f *fakeHost) GetHeartbeatBatcher() *batcher.HeartbeatStatsUpdater { return f.hbBatcher }
