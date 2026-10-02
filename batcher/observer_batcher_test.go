/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-02 16:20:01
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-02 16:20:01
 * @FilePath: \go-wsc\batcher\observer_batcher_test.go
 * @Description: 观察者通知批量处理器测试
 *   - 创建 / 非阻塞 Submit / nil 防御（nil msg、nil batcher）
 *   - Flush 空批次 no-op / 逐条按序直投
 *   - 满批自动 flush（凑批语义）
 *   - WithClone 深拷贝隔离（提交后修改原对象不影响异步投递）
 *   - 队列满丢弃 + DroppedCount（卡住直投的确定性构造）
 *   - Stop flush 剩余（停机不丢通知）
 *   - 并发 Submit 全量投递
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// observerNotifyCall 一次直投调用的记录
type observerNotifyCall struct {
	MessageID string
	Namespace string
	GroupIDs  []string
	Content   string
}

// fakeObserverNotifier 观察者直投替身（记录每次 NotifyObserversDirect 调用）
type fakeObserverNotifier struct {
	mu    sync.Mutex
	calls []observerNotifyCall
}

func (f *fakeObserverNotifier) NotifyObserversDirect(msg *models.HubMessage, namespace string, groupIDs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, observerNotifyCall{
		MessageID: msg.MessageID,
		Namespace: namespace,
		GroupIDs:  append([]string(nil), groupIDs...),
		Content:   msg.Content,
	})
}

func (f *fakeObserverNotifier) snapshot() []observerNotifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]observerNotifyCall(nil), f.calls...)
}

var _ ObserverNotifier = (*fakeObserverNotifier)(nil)

// blockingObserverNotifier 可阻塞的直投替身：
// 首次 NotifyObserversDirect 入口阻塞（卡住 worker flush 的确定性手段），
// release 关闭后放行并委托内层替身记录
type blockingObserverNotifier struct {
	*fakeObserverNotifier
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingObserverNotifier) NotifyObserversDirect(msg *models.HubMessage, namespace string, groupIDs []string) {
	f.once.Do(func() { close(f.started) })
	<-f.release
	f.fakeObserverNotifier.NotifyObserversDirect(msg, namespace, groupIDs)
}

// newObserverBenchMsg 构造基准/测试用最小消息（Data/GroupIDs 为 nil，Clone 走浅拷贝快路径）
func newObserverBenchMsg(id, content string) *models.HubMessage {
	return &models.HubMessage{MessageID: id, Content: content}
}

// TestNewObserverNotificationBatcher 验证创建后字段与访问器正确
func TestNewObserverNotificationBatcher(t *testing.T) {
	notifier := &fakeObserverNotifier{}

	b := NewObserverNotificationBatcher(notifier, 64, 8, 50*time.Millisecond)

	require.NotNil(t, b, "创建的批处理器不应为 nil")
	assert.Same(t, notifier, b.hub, "batcher.hub 应指向传入的通知端口")
	assert.NotNil(t, b.processor, "batcher.processor 不应为 nil")
	assert.Same(t, notifier, b.Hub(), "Hub() 应返回绑定的通知端口")
	assert.Same(t, b.processor, b.Processor(), "Processor() 应返回底层批处理器")
	defer b.Stop()
}

// TestObserverBatcher_SubmitSuccess 验证非阻塞提交成功
func TestObserverBatcher_SubmitSuccess(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	b := NewObserverNotificationBatcher(notifier, 100, 100, 10*time.Second)
	defer b.Stop()

	ok := b.Submit(newObserverBenchMsg("m-1", "hello"), "ns-1", []string{"g-1"})
	assert.True(t, ok, "提交观察者通知应成功")
}

// TestObserverBatcher_SubmitNilMsgRejected 验证 nil msg 直接拒绝且不入队
func TestObserverBatcher_SubmitNilMsgRejected(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	b := NewObserverNotificationBatcher(notifier, 100, 100, 10*time.Second)
	defer b.Stop()

	assert.False(t, b.Submit(nil, "ns-1", nil), "nil msg 的提交应返回 false")
}

// TestObserverBatcher_NilBatcherSafety 验证 nil batcher 的方法防御
func TestObserverBatcher_NilBatcherSafety(t *testing.T) {
	var b *ObserverNotificationBatcher

	assert.False(t, b.Submit(newObserverBenchMsg("m-1", "c"), "ns-1", nil),
		"nil batcher 的 Submit 应返回 false")
	assert.NotPanics(t, b.Stop, "nil batcher 的 Stop 不应 panic")
	assert.EqualValues(t, 0, b.DroppedCount(), "nil batcher 的 DroppedCount 应为 0")
}

// TestObserverBatcher_FlushEmptyNoop 验证 Flush 空批次不 panic 且不投递
func TestObserverBatcher_FlushEmptyNoop(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	b := NewObserverNotificationBatcher(notifier, 64, 8, time.Hour)
	defer b.Stop()

	assert.NotPanics(t, func() {
		b.Flush(nil)
		b.Flush([]*ObserverNotifyItem{})
	}, "Flush 空批次不应 panic")

	assert.Empty(t, notifier.snapshot(), "空批次不应触发任何投递")
}

// TestObserverBatcher_FlushDeliversItemsInOrder 验证 Flush 逐条按序直投
func TestObserverBatcher_FlushDeliversItemsInOrder(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	b := NewObserverNotificationBatcher(notifier, 64, 8, time.Hour)
	defer b.Stop()

	items := []*ObserverNotifyItem{
		{Msg: newObserverBenchMsg("m-1", "c1"), Namespace: "ns-1", GroupIDs: []string{"g-1", "g-2"}},
		{Msg: newObserverBenchMsg("m-2", "c2"), Namespace: "ns-2"},
	}

	b.Flush(items)

	calls := notifier.snapshot()
	require.Len(t, calls, 2, "两条通知应各投递一次")
	assert.Equal(t, "m-1", calls[0].MessageID)
	assert.Equal(t, "ns-1", calls[0].Namespace)
	assert.Equal(t, []string{"g-1", "g-2"}, calls[0].GroupIDs)
	assert.Equal(t, "c1", calls[0].Content)
	assert.Equal(t, "m-2", calls[1].MessageID)
	assert.Equal(t, "ns-2", calls[1].Namespace)
	assert.Empty(t, calls[1].GroupIDs)
}

// TestObserverBatcher_BatchFullTriggersFlush 验证满批自动触发 flush（凑批语义）
func TestObserverBatcher_BatchFullTriggersFlush(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	// flushInterval=1h 关闭定时 flush，只有满 2 条才触发
	b := NewObserverNotificationBatcher(notifier, 100, 2, time.Hour)
	defer b.Stop()

	require.True(t, b.Submit(newObserverBenchMsg("m-1", "c1"), "ns-1", nil))
	require.True(t, b.Submit(newObserverBenchMsg("m-2", "c2"), "ns-1", nil))

	require.Eventually(t, func() bool {
		return len(notifier.snapshot()) == 2
	}, 2*time.Second, 5*time.Millisecond, "满 2 条应自动 flush")

	calls := notifier.snapshot()
	assert.ElementsMatch(t, []string{"m-1", "m-2"},
		[]string{calls[0].MessageID, calls[1].MessageID})
}

// TestObserverBatcher_CloneIsolation 验证 WithClone 深拷贝隔离：
// Submit 内部同步完成深拷贝，返回后修改原 msg/groupIDs 不影响异步投递内容
func TestObserverBatcher_CloneIsolation(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	b := NewObserverNotificationBatcher(notifier, 64, 100, time.Hour)

	msg := newObserverBenchMsg("m-clone", "原始内容")
	groups := []string{"g-1"}
	require.True(t, b.Submit(msg, "ns-clone", groups))

	// 提交后修改原对象：入队的是深拷贝副本，投递内容不受影响
	msg.Content = "已被修改"
	msg.MessageID = "m-mutated"
	groups[0] = "g-mutated"

	b.Stop()

	calls := notifier.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "m-clone", calls[0].MessageID, "投递应为提交时的消息ID快照")
	assert.Equal(t, "原始内容", calls[0].Content, "投递应为提交时的内容快照")
	assert.Equal(t, []string{"g-1"}, calls[0].GroupIDs, "投递应为提交时的分组快照")
}

// TestObserverBatcher_StopFlushesPending 验证 Stop 前置 flush 剩余通知（停机不丢）
func TestObserverBatcher_StopFlushesPending(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	// 长 flushInterval 确保只能靠 Stop 触发 flush
	b := NewObserverNotificationBatcher(notifier, 100, 100, time.Hour)

	for i := 1; i <= 3; i++ {
		require.True(t, b.Submit(newObserverBenchMsg(fmt.Sprintf("m-stop-%d", i), "c"), "ns-1", nil))
	}

	b.Stop()

	calls := notifier.snapshot()
	require.Len(t, calls, 3, "Stop 应 flush 全部 3 条残留通知")
	assert.Equal(t, []string{"m-stop-1", "m-stop-2", "m-stop-3"},
		[]string{calls[0].MessageID, calls[1].MessageID, calls[2].MessageID},
		"投递顺序应保持批次顺序")
}

// TestObserverBatcher_QueueFullDrops 验证队列满背压：Submit 丢弃并计数
// 确定性构造：queueSize=1 / batchSize=1，卡住 worker 的直投调用，
// 队列恢复 1 格空闲后第 2 条入队、第 3 条必然丢弃
func TestObserverBatcher_QueueFullDrops(t *testing.T) {
	inner := &fakeObserverNotifier{}
	blockNotifier := &blockingObserverNotifier{
		fakeObserverNotifier: inner,
		started:              make(chan struct{}),
		release:              make(chan struct{}),
	}

	// flushInterval=1h：worker 只在满批（1 条）时 flush 并阻塞在直投中
	b := NewObserverNotificationBatcher(blockNotifier, 1, 1, time.Hour)

	require.True(t, b.Submit(newObserverBenchMsg("m-block", "c"), "ns-1", nil))
	<-blockNotifier.started

	require.True(t, b.Submit(newObserverBenchMsg("m-2", "c"), "ns-1", nil))
	assert.False(t, b.Submit(newObserverBenchMsg("m-3", "c"), "ns-1", nil),
		"队列满时 Submit 应返回 false")
	assert.EqualValues(t, 1, b.DroppedCount(), "丢弃应被计数")

	close(blockNotifier.release)
	b.Stop()

	calls := inner.snapshot()
	require.Len(t, calls, 2, "首条 + Stop drain 的第 2 条应投递，被丢弃的第 3 条不投递")
	assert.Equal(t, "m-block", calls[0].MessageID)
	assert.Equal(t, "m-2", calls[1].MessageID)
}

// TestObserverBatcher_ConcurrentSubmit 验证并发 Submit 安全且 Stop 后全量投递
func TestObserverBatcher_ConcurrentSubmit(t *testing.T) {
	notifier := &fakeObserverNotifier{}
	b := NewObserverNotificationBatcher(notifier, 4096, 100, time.Hour)

	var accepted int64
	var wg sync.WaitGroup
	for g := 0; g < 25; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if b.Submit(newObserverBenchMsg(fmt.Sprintf("m-%d-%d", g, i), "c"), "ns-1", nil) {
					atomic.AddInt64(&accepted, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	b.Stop()

	calls := notifier.snapshot()
	require.EqualValues(t, atomic.LoadInt64(&accepted), int64(len(calls)),
		"Stop 后投递条数应等于被接受条数")

	ids := make(map[string]struct{}, len(calls))
	for _, c := range calls {
		ids[c.MessageID] = struct{}{}
	}
	require.Len(t, ids, len(calls), "所有投递的消息ID应唯一无丢失")
}

// BenchmarkObserverBatcher_SubmitFlush 观察者通知提交+直投管线基准
// batchSize=200 / interval=1h：满批由 worker 自动 flush（关闭定时器，无 ticker 等待），
// Submit 含 WithClone 深拷贝（消息 + 分组切片），flush 逐条直投
func BenchmarkObserverBatcher_SubmitFlush(b *testing.B) {
	notifier := &fakeObserverNotifier{}
	batcher := NewObserverNotificationBatcher(notifier, 1<<14, 200, time.Hour)

	msg := newObserverBenchMsg("bench-msg", "bench-content")
	groups := []string{"bench-group"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkBool = batcher.Submit(msg, "bench-ns", groups)
	}
	b.StopTimer()
	batcher.Stop()
	benchSinkInt = len(notifier.snapshot())
}
