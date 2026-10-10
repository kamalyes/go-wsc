/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-09 03:22:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 19:26:00
 * @FilePath: \go-wsc\overload\aggregator_test.go
 * @Description: 消息聚合器测试 —— 数组拼装、降级直通、群组惰性删除与并发安全
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package overload

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
)

// TestMessageAggregatorFlushJoinsArray 窗口攒批拼装：按序零重编码拼为 JSON 数组
func TestMessageAggregatorFlushJoinsArray(t *testing.T) {
	a := NewMessageAggregator(0, 0) // 零值走默认容量

	require.True(t, a.Offer("app1", "group-1", []byte(`{"seq":1}`)))
	require.True(t, a.Offer("app1", "group-1", []byte(`{"seq":2}`)))
	require.True(t, a.Offer("app1", "group-1", []byte(`{"seq":3}`)))

	batches := a.Flush()
	require.Len(t, batches, 1, "单群组单窗口应产出一批")
	assert.Equal(t, "app1", batches[0].AppID)
	assert.Equal(t, "group-1", batches[0].GroupID)
	assert.Equal(t, `[{"seq":1},{"seq":2},{"seq":3}]`, string(batches[0].Frame),
		"数组拼装应保持接收序且零重编码")

	// 窗口已清空：再次 Flush 无产出，且空群组被惰性删除
	assert.Empty(t, a.Flush())
	assert.Equal(t, int64(0), a.Stats()["groups"], "空群组应被惰性删除")
}

// TestMessageAggregatorGroupIsolation 群组隔离：同窗口多群组各自成批，appID 隔离同名群组
func TestMessageAggregatorGroupIsolation(t *testing.T) {
	a := NewMessageAggregator(0, 0)

	require.True(t, a.Offer("app1", "group-a", []byte(`"a1"`)))
	require.True(t, a.Offer("app2", "group-a", []byte(`"a2"`))) // 同 groupID 跨 app 不混批
	require.True(t, a.Offer("app1", "group-b", []byte(`"b1"`)))

	batches := a.Flush()
	require.Len(t, batches, 3, "三组 (app, group) 应各自成批")

	byGroup := make(map[string]string, len(batches))
	for _, b := range batches {
		byGroup[b.AppID+"|"+b.GroupID] = string(b.Frame)
	}
	assert.Equal(t, `["a1"]`, byGroup["app1|group-a"])
	assert.Equal(t, `["a2"]`, byGroup["app2|group-a"])
	assert.Equal(t, `["b1"]`, byGroup["app1|group-b"])
}

// TestMessageAggregatorOverflowPassthrough 窗口满直通降级：超限消息 Offer 返回 false（单发，不丢）
func TestMessageAggregatorOverflowPassthrough(t *testing.T) {
	a := NewMessageAggregator(2, 0) // 窗口上限 2

	require.True(t, a.Offer("app1", "group-1", []byte(`1`)))
	require.True(t, a.Offer("app1", "group-1", []byte(`2`)))
	assert.False(t, a.Offer("app1", "group-1", []byte(`3`)), "窗口满应直通降级")
	assert.False(t, a.Offer("app1", "group-1", []byte(`4`)), "窗口满持续直通")

	batches := a.Flush()
	require.Len(t, batches, 1)
	assert.Equal(t, `[1,2]`, string(batches[0].Frame), "窗口内消息不受降级影响")

	// Flush 腾空后恢复聚合能力
	require.True(t, a.Offer("app1", "group-1", []byte(`5`)))
	assert.Equal(t, `[5]`, string(a.Flush()[0].Frame))
}

// TestMessageAggregatorMaxGroups 群组数上限：超限新群组直通，既有群组不受影响
func TestMessageAggregatorMaxGroups(t *testing.T) {
	a := NewMessageAggregator(0, 2)

	require.True(t, a.Offer("app1", "group-1", []byte(`1`)))
	require.True(t, a.Offer("app1", "group-2", []byte(`2`)))
	assert.False(t, a.Offer("app1", "group-3", []byte(`3`)), "群组数超限应拒绝新群组")
	require.True(t, a.Offer("app1", "group-1", []byte(`4`)), "既有群组不受上限影响")

	// Flush 产出两批（窗口非空不删群组），空窗口在下一轮 Flush 惰性删除腾出名额
	require.Len(t, a.Flush(), 2)
	assert.Empty(t, a.Flush())
	require.True(t, a.Offer("app1", "group-3", []byte(`3`)), "名额释放后新群组恢复聚合")
}

// TestMessageAggregatorInvalidParams 非法参数拒绝（空 appID/groupID/载荷）
func TestMessageAggregatorInvalidParams(t *testing.T) {
	a := NewMessageAggregator(0, 0)
	assert.False(t, a.Offer("", "group", []byte(`1`)))
	assert.False(t, a.Offer("app", "", []byte(`1`)))
	assert.False(t, a.Offer("app", "group", nil))
	assert.Empty(t, a.Flush())
}

// TestMessageAggregatorConcurrentOfferFlush 并发 Offer + Flush：无竞争、无消息丢失
// （每条 Offer true 的消息必然出现在某一批 Frame 中——按条计数对账；
// 同时回归 Offer 竞态：空窗口删除与取引用并发时不得写入孤儿群组）
func TestMessageAggregatorConcurrentOfferFlush(t *testing.T) {
	a := NewMessageAggregator(constants.DefaultAggregatorBatchSize, 0)

	const workers, perWorker = 8, 200
	var offeredCount atomic.Int64
	var wg sync.WaitGroup

	// 持续 Flush 的 drain 协程
	// 空批不入队（占满 channel 后卡在发送上、且发送不响应 stop，与
	// drainWG.Wait() 互等死锁）；空轮 Gosched 让出 CPU 避免忙循环烧核；
	// stop 时在途批经 drained 收集不丢弃（drainWG.Wait 的 happens-before 保证读安全）
	stop := make(chan struct{})
	drainWG := sync.WaitGroup{}
	drainWG.Add(1)
	var drained []AggregatedBatch
	flushedBatches := make(chan []AggregatedBatch, workers*perWorker)
	go func() {
		defer drainWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				batches := a.Flush()
				if len(batches) == 0 {
					runtime.Gosched()
					continue
				}
				select {
				case flushedBatches <- batches:
				case <-stop:
					drained = append(drained, batches...)
					return
				}
			}
		}
	}()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if a.Offer("app1", fmt.Sprintf("group-%d", w%4), []byte(`1`)) {
					offeredCount.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	// 末轮 Flush 收尾（停止 drain 后统一换出）
	close(stop)
	drainWG.Wait()
	close(flushedBatches)

	var aggregatedCount int64
	for batches := range flushedBatches {
		for _, b := range batches {
			// Frame 内元素计数：[1,1,...] 按逗号切分
			aggregatedCount += int64(countArrayElements(b.Frame))
		}
	}
	// stop 时 drain 在途批（drainWG.Wait 后读，happens-before 已建立）
	for _, b := range drained {
		aggregatedCount += int64(countArrayElements(b.Frame))
	}
	// 收尾再 Flush 一次（drain 停止后可能仍有窗口残留）
	for _, b := range a.Flush() {
		aggregatedCount += int64(countArrayElements(b.Frame))
	}

	assert.Equal(t, offeredCount.Load(), aggregatedCount,
		"聚合守恒：Offer 成功的每条消息必出现在某批数组帧中")
}

// TestMessageAggregatorFrameIsolatedAfterFlush 双缓冲隔离：Flush 产出后继续入窗不得复写已产出帧
// （Flush 锁内仅 swap、拼装锁外执行——taken 缓冲在归还前仅 Flush 持有，后续 Offer 写入 spare 侧新缓冲）
func TestMessageAggregatorFrameIsolatedAfterFlush(t *testing.T) {
	a := NewMessageAggregator(0, 0)
	require.True(t, a.Offer("app1", "group-1", []byte(`"p1"`)))

	batches := a.Flush()
	require.Len(t, batches, 1)
	frame := batches[0].Frame

	// Flush 后继续入窗（缓冲已交换复用），已产出帧字节不得被后续写入覆盖
	require.True(t, a.Offer("app1", "group-1", []byte(`"p2"`)))
	require.True(t, a.Offer("app1", "group-1", []byte(`"p3"`)))
	assert.Equal(t, `["p1"]`, string(frame), "已产出帧与后续窗口写入完全隔离")

	next := a.Flush()
	require.Len(t, next, 1)
	assert.Equal(t, `["p2","p3"]`, string(next[0].Frame), "交换后的新缓冲正常接管窗口")
}

// countArrayElements 数组帧元素计数（不依赖 JSON 解析，按顶层逗号切分——测试载荷为标量 1）
func countArrayElements(frame []byte) int {
	if len(frame) <= 2 {
		return 0
	}
	n := 1
	for _, b := range frame[1 : len(frame)-1] {
		if b == ',' {
			n++
		}
	}
	return n
}
