/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 02:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\spi\logger_bench_test.go
 * @Description: 默认日志器输出路径基准
 *
 * 对比 go-wsc 默认日志器的两种输出形态，两侧均写真实临时文件：
 *   - direct：*os.File 直写。*os.File 不实现 concurrentSafeWriter，故 go-logger 的
 *     isConcurrentSafeOutput 判定为 false，outLockFree 保持 false——
 *     与默认的 os.Stdout 完全同构（每行一次互斥 + 一次 write(2)）
 *   - console：经 NewConsoleWriter 批量合并。consoleLogWriter 实现
 *     concurrentSafeMarker，故 outLockFree 自动变为 true，互斥一并消除
 *
 * 唯一变量是 writer 层，即本次改动本身；destination 同为真实文件，
 * 不用 io.Discard 以免把 syscall 成本测没了。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package spi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kamalyes/go-logger"
)

// benchLoggerTo 按 go-wsc 默认日志器的配置构造，仅替换输出目标
func benchLoggerTo(out any) *logger.Logger {
	l := logger.NewLogger().
		WithLevel(logger.DEBUG).
		WithPrefix("[WSC]").
		WithShowCaller(false).
		WithColorful(true).
		WithTimeFormat(time.RFC3339Nano)
	switch w := out.(type) {
	case *os.File:
		return l.WithOutput(w)
	case logger.IWriter:
		return l.WithOutput(w)
	}
	return l
}

func tempFile(b *testing.B) *os.File {
	b.Helper()
	f, err := os.Create(filepath.Join(b.TempDir(), "bench.log"))
	if err != nil {
		b.Fatalf("建临时文件失败: %v", err)
	}
	b.Cleanup(func() { _ = f.Close() })
	return f
}

// ── direct：当前默认形态（加锁 + 每行一次 syscall）──

func BenchmarkDefaultOut_Direct(b *testing.B) {
	l := benchLoggerTo(tempFile(b))
	b.ReportAllocs()
	for b.Loop() {
		l.InfoKV("连接已建立", "client_id", "c-1", "user_id", "u-1")
	}
}

func BenchmarkDefaultOut_Direct_Parallel(b *testing.B) {
	l := benchLoggerTo(tempFile(b))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.InfoKV("连接已建立", "client_id", "c-1", "user_id", "u-1")
		}
	})
}

// ── console：批量合并形态（无锁 + 合并 syscall）──

func BenchmarkDefaultOut_Console(b *testing.B) {
	w := logger.NewConsoleWriter(logger.WithConsoleOutput(tempFile(b)))
	b.Cleanup(func() { _ = w.Close() })
	l := benchLoggerTo(w)
	b.ReportAllocs()
	for b.Loop() {
		l.InfoKV("连接已建立", "client_id", "c-1", "user_id", "u-1")
	}
}

func BenchmarkDefaultOut_Console_Parallel(b *testing.B) {
	w := logger.NewConsoleWriter(logger.WithConsoleOutput(tempFile(b)))
	b.Cleanup(func() { _ = w.Close() })
	l := benchLoggerTo(w)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.InfoKV("连接已建立", "client_id", "c-1", "user_id", "u-1")
		}
	})
}
