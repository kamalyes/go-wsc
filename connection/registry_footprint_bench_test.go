/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 02:18:11
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 22:18:56
 * @FilePath: \go-wsc\connection\registry_footprint_bench_test.go
 * @Description: 注册表常驻内存与连接生命周期基准（千万级在线的空间证据）
 *
 * 形态说明：MemoryFootprint 为采样型基准——按生产注册全链路（Client 构造 +
 * 容量四件套 + 主存储/反向索引/群组与命名空间索引）批量注册后取 GC 稳态
 * 差值，输出每连接常驻字节；AddRemoveCycle 为重连风暴形态——同 id 反复
 * 注册/注销，测量完整生命周期的分配成本
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"fmt"
	"runtime"
	"testing"
	"unsafe"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// footprintN 常驻内存采样规模（20w 连接足够外推千万级，进程内存可控）
const footprintN = 200000

// newFootprintLogger 生产日志形态（Info 级，Debug 日志的 KV 装配不进入测量）
func newFootprintLogger() spi.Logger {
	return logger.NewLogger().WithLevel(logger.INFO).WithShowCaller(false)
}

// newFootprintClient 按生产形态构造注册客户端（默认群组索引 + 容量四件套）
func newFootprintClient(i int, cm *CapacityManager) *models.Client {
	c := models.NewClient(fmt.Sprintf("c-%08d", i), fmt.Sprintf("u-%08d", i), models.UserTypeCustomer)
	c.GroupID = "g-bench" // 普通用户连接后自动加入默认群组（群组反向索引真实形态）
	cm.InitClientSendChan(c)
	return c
}

// BenchmarkRegistry_MemoryFootprint 每连接常驻内存采样（千万级在线的空间外推依据）
//
// 采样方法：GC 稳态 HeapInuse 差值 / 连接数
// 覆盖范围：Client 结构、通道四件套、注册表全部索引条目与字符串键
// 选型说明：HeapAlloc 含框架瞬时对象噪声，小差值下可能翻负
// HeapInuse 统计 in-use span 字节，20w 连接的数百 MB 增量远大于 GC 噪声
func BenchmarkRegistry_MemoryFootprint(b *testing.B) {
	reg := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: footprintN})
	cm := NewCapacityManager(nil, newFootprintLogger())

	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < footprintN; i++ {
		reg.AddClient(newFootprintClient(i, cm))
	}
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	// KeepAlive 防止编译器 liveness 判定 reg/cm 已死亡，避免 GC 连同 20w 客户端一并回收、采样值崩塌
	// 两者最后一次使用均在注册循环内
	runtime.KeepAlive(reg)
	runtime.KeepAlive(cm)

	perConn := float64(after.HeapInuse-before.HeapInuse) / float64(footprintN)
	b.ReportMetric(perConn, "B/conn")
	b.ReportMetric(float64(unsafe.Sizeof(models.Client{})), "struct-B")
	b.Logf("20w 连接常驻 %.1f MB（%.0f B/conn），千万级外推 %.1f GB",
		float64(after.HeapInuse-before.HeapInuse)/1024/1024, perConn, perConn*10_000_000/1024/1024/1024)
}

// BenchmarkRegistry_AddRemoveCycle 重连风暴（同 id 反复注册/注销）完整生命周期分配
//
// 覆盖语义：AddClient 断线重连覆盖路径 + RemoveClient 主存储/反向索引/
// 群组与命名空间索引全链路清理
func BenchmarkRegistry_AddRemoveCycle(b *testing.B) {
	reg := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 1024})
	cm := NewCapacityManager(nil, newFootprintLogger())
	c := newFootprintClient(0, cm)
	reg.AddClient(c)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reg.AddClient(c)
		reg.RemoveClient(c.ID, c.UserID)
	}
}
