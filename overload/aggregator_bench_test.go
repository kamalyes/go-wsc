/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 21:36:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 21:36:00
 * @FilePath: \go-wsc\overload\aggregator_bench_test.go
 * @Description: 消息聚合器热路径基准（弹幕入窗 Offer 与窗口换出 Flush）
 *
 * 形态说明：单组入窗稳态为窗口满后的溢出直通分支
 * 即 shard 锁 + map 探测 + group 锁 + 上限判定，与生产洪峰下入窗速率压满 drain 节拍时一致
 * 并行多组形态测量 16 分片的竞争分散度
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"fmt"
	"testing"
)

// benchPayload 模拟弹幕负载（约 30B，弹幕典型量级）
var benchPayload = []byte(`{"content":"hello danmaku"}`)

// BenchmarkMessageAggregator_Offer 单组入窗（窗口满后溢出直通稳态：锁与判定路径）
func BenchmarkMessageAggregator_Offer(b *testing.B) {
	a := NewMessageAggregator(0, 0)
	b.ReportAllocs()
	for b.Loop() {
		a.Offer("app1", "group-1", benchPayload)
	}
}

// BenchmarkMessageAggregator_OfferParallelMultiGroup 并行入窗 64 组（分片竞争分散度）
// 组名预生成：基准循环内不引入 fmt.Sprintf 分配伪影
func BenchmarkMessageAggregator_OfferParallelMultiGroup(b *testing.B) {
	a := NewMessageAggregator(0, 0)
	groups := make([]string, 64)
	for i := range groups {
		groups[i] = fmt.Sprintf("group-%d", i)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			a.Offer("app1", groups[i%64], benchPayload)
			i++
		}
	})
}

// BenchmarkMessageAggregator_Flush 满窗换出拼装（256 条入窗 + 一次 Flush join）
func BenchmarkMessageAggregator_Flush(b *testing.B) {
	const window = 256
	a := NewMessageAggregator(window, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < window; j++ {
			a.Offer("app1", "group-1", benchPayload)
		}
		a.Flush()
	}
}
