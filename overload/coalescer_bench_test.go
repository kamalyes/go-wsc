/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 21:36:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\overload\coalescer_bench_test.go
 * @Description: 高频合并器热路径基准（latest-wins 入窗 Offer）
 *
 * 形态说明：单 key 覆盖为最热竞争形态，即同用户连发正在输入/已读
 * 并行活跃 key 集形态测量 16 分片的竞争分散度与容量内覆盖稳态
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"fmt"
	"testing"

	"github.com/kamalyes/go-wsc/models"
)

// BenchmarkCoalescer_OfferSameKey 单 key 覆盖（最热竞争形态：同用户连发）
func BenchmarkCoalescer_OfferSameKey(b *testing.B) {
	c := NewCoalescer(0)
	msg := models.NewHubMessage()
	b.ReportAllocs()
	for b.Loop() {
		c.Offer(EphemeralKey{UserID: "u-1", MessageType: models.MessageTypeTyping}, msg)
	}
}

// BenchmarkCoalescer_OfferParallelActiveKeys 并行活跃 key 集（容量内覆盖稳态：分片竞争分散度）
// key 预生成：基准循环内不引入 fmt.Sprintf 分配伪影
func BenchmarkCoalescer_OfferParallelActiveKeys(b *testing.B) {
	const activeKeys = 4096
	c := NewCoalescer(activeKeys)
	msg := models.NewHubMessage()
	keys := make([]EphemeralKey, activeKeys)
	for i := range keys {
		keys[i] = EphemeralKey{UserID: fmt.Sprintf("u-%d", i), MessageType: models.MessageTypeTyping}
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Offer(keys[i%activeKeys], msg)
			i++
		}
	})
}
