/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 11:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-17 13:26:00
 * @FilePath: \go-wsc\batcher\interfaces_test.go
 * @Description: 批量写器端口的测试替身
 *
 * 本包是独立子包，测试不应构造真实 Hub —— 一个最小 StorageBatchWriter 替身即可
 * 完整覆盖三个更新器的攒批/聚合/flush 逻辑，且更快更稳。
 *
 * 替身采用「嵌入接口 + 只覆盖用到的方法」的部分 mock 写法：仓储接口动辄 15 个方法，
 * 本包只读其中 Batch* 三个批量写方法，其余方法保留 nil 实现 —— 一旦被调用会立刻
 * panic，暴露测试遗漏的依赖，而非静默返回零值让断言变得可疑。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"context"
	"sync"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// fakeQualityStore 连接质量仓储替身
type fakeQualityStore struct {
	spi.ConnectionQualityStore // 未覆盖的方法调用即 panic

	mu         sync.Mutex
	increments []*models.StatsIncrementEntry
	heartbeats []*models.HeartbeatUpdateEntry
}

func (f *fakeQualityStore) BatchIncrementStats(_ context.Context, entries []*models.StatsIncrementEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.increments = append(f.increments, entries...)
	return nil
}

func (f *fakeQualityStore) BatchUpdateHeartbeats(_ context.Context, entries []*models.HeartbeatUpdateEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, entries...)
	return nil
}

func (f *fakeQualityStore) incrementsSnapshot() []*models.StatsIncrementEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*models.StatsIncrementEntry(nil), f.increments...)
}

func (f *fakeQualityStore) heartbeatsSnapshot() []*models.HeartbeatUpdateEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*models.HeartbeatUpdateEntry(nil), f.heartbeats...)
}

// fakeConnStore 连接记录仓储替身
type fakeConnStore struct {
	spi.ConnectionStore // 未覆盖的方法调用即 panic

	mu         sync.Mutex
	heartbeats []*models.HeartbeatUpdateEntry
}

func (f *fakeConnStore) BatchUpdateHeartbeats(_ context.Context, entries []*models.HeartbeatUpdateEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, entries...)
	return nil
}

func (f *fakeConnStore) heartbeatsSnapshot() []*models.HeartbeatUpdateEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*models.HeartbeatUpdateEntry(nil), f.heartbeats...)
}

// fakeMessageSink 消息记录仓储替身
type fakeMessageSink struct {
	spi.MessageSink // 未覆盖的方法调用即 panic

	mu      sync.Mutex
	batches []*fakeStatusBatch

	// block 非 nil 时在 BatchUpdateStatus 入口阻塞（测试队列满的确定性手段）
	block func()
	// err 非 nil 时作为 BatchUpdateStatus 的返回值
	err error
}

// fakeStatusBatch 一次 BatchUpdateStatus 调用的记录
type fakeStatusBatch struct {
	Keys   []models.MessageRecordKey
	Status models.MessageSendStatus
	Reason models.FailureReason
	ErrMsg string
}

func (f *fakeMessageSink) BatchUpdateStatus(_ context.Context, keys []models.MessageRecordKey, status models.MessageSendStatus, reason models.FailureReason, errMsg string) error {
	f.mu.Lock()
	block, err := f.block, f.err
	if block == nil {
		// 未阻塞时才记录：阻塞场景（队列满测试）关注的是队列容量，不是写入内容
		f.batches = append(f.batches, &fakeStatusBatch{
			Keys:   append([]models.MessageRecordKey(nil), keys...),
			Status: status,
			Reason: reason,
			ErrMsg: errMsg,
		})
	}
	f.mu.Unlock()

	if block != nil {
		block()
	}
	return err
}

func (f *fakeMessageSink) snapshot() []*fakeStatusBatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeStatusBatch(nil), f.batches...)
}

// fakeBatchWriter StorageBatchWriter 替身
//
// 日志器直接复用默认实现（不吞输出）：本包 flush 只在失败路径记日志，
// 测试正常路径下不会产生噪声；真出问题时日志反而是有用的定位信息。
type fakeBatchWriter struct {
	records *fakeConnStore
	quality *fakeQualityStore
	sink    *fakeMessageSink
	logger  spi.Logger
}

func newFakeBatchWriter() *fakeBatchWriter {
	return &fakeBatchWriter{
		records: &fakeConnStore{},
		quality: &fakeQualityStore{},
		sink:    &fakeMessageSink{},
		logger:  spi.NewDefaultLogger(),
	}
}

func (f *fakeBatchWriter) Context() context.Context { return context.Background() }
func (f *fakeBatchWriter) GetLogger() spi.Logger    { return f.logger }
func (f *fakeBatchWriter) GetConnectionRecordRepo() spi.ConnectionStore {
	return f.records
}
func (f *fakeBatchWriter) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	return f.quality
}
func (f *fakeBatchWriter) GetMessageRecordRepo() spi.MessageSink { return f.sink }

var _ StorageBatchWriter = (*fakeBatchWriter)(nil)
