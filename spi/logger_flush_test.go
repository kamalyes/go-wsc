/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 02:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\spi\logger_flush_test.go
 * @Description: 默认日志器缓冲刷出的回归测试
 *
 * 默认日志器改为经 ConsoleWriter 批量写出后，缓冲中的日志若无人刷出即在进程
 * 退出时丢失——而关停阶段的末尾几行正是排障最需要的。hub.SafeShutdown 已挂
 * FlushDefaultLogger（defer 最先注册、按 LIFO 最后执行），本文件锁定该机制：
 *
 *   1. Flush 是承重的：不刷则行不落盘（用 1 小时刷新间隔把定时器排除，
 *      使断言确定性成立，而非赌 100ms 默认间隔的时序）
 *   2. FlushDefaultLogger 在默认日志器未被构造时为 no-op，且不得反向把它构造出来
 *      （否则为了刷缓冲反而凭空建出 writer 及其 flusher goroutine）
 *   3. 可重复调用
 *
 * FATAL 路径不在此列：go-logger 的 writeOut 在 os.Exit 前已自行 Flush
 * 实现了 Flush() error 的 output（logger.go «异步输出器…确保遗言落地»），
 * consoleLogWriter 正满足该接口，故退出路径本就安全。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package spi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamalyes/go-logger"
)

// TestConsoleWriterFlushIsLoadBearing 不刷出则日志不落盘，刷出后全部落盘
//
// 刷新间隔设为 1 小时：定时器在测试期内绝不触发，故行能出现的唯一途径是显式
// Flush——这使「Flush 承重」成为确定性断言，而非时序巧合。
func TestConsoleWriterFlushIsLoadBearing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flush.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	defer f.Close()

	w := logger.NewConsoleWriter(
		logger.WithConsoleOutput(f),
		logger.WithConsoleFlushInterval(time.Hour),
	)
	defer w.Close()

	l := logger.NewLogger().
		WithLevel(logger.DEBUG).
		WithShowCaller(false).
		WithTimeFormat(time.RFC3339Nano).
		WithOutput(w)

	const marker = "SHUTDOWN_MARKER_LINE"
	l.InfoKV(marker, "phase", "pre-shutdown")

	// 刷出前：定时器不会触发，行应仍在缓冲里
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	if strings.Contains(string(before), marker) {
		t.Fatal("刷出前行已落盘：缓冲未生效，本测试无法证明 Flush 承重（检查刷新间隔是否被忽略）")
	}

	// 刷出后：行必须落盘
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush 失败: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	if !strings.Contains(string(after), marker) {
		t.Fatalf("刷出后行仍未落盘：关停期日志会丢失\n文件内容: %q", string(after))
	}
}

// TestConsoleWriterFlushAllLines 多行场景下刷出不漏行
func TestConsoleWriterFlushAllLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flush_multi.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	defer f.Close()

	w := logger.NewConsoleWriter(
		logger.WithConsoleOutput(f),
		logger.WithConsoleFlushInterval(time.Hour),
	)
	defer w.Close()

	l := logger.NewLogger().WithShowCaller(false).WithOutput(w)

	const n = 64
	for i := 0; i < n; i++ {
		l.InfoKV("关停阶段日志", "seq", i)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush 失败: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	got := strings.Count(string(content), "关停阶段日志")
	if got != n {
		t.Fatalf("刷出后行数不符：期望 %d，实得 %d", n, got)
	}
}

// TestFlushDefaultLoggerNoopBeforeConstruction 未构造默认日志器时为 no-op
//
// 关键是它不得反向触发构造：否则一次「刷缓冲」反而凭空建出 ConsoleWriter
// 及其后台 flusher goroutine。
//
// 注意本用例依赖「默认日志器尚未被构造」，而同包其他用例可能已构造它，
// 故仅在确实未构造时断言，已构造时退化为幂等性检查（见下一个用例）。
func TestFlushDefaultLoggerNoopBeforeConstruction(t *testing.T) {
	constructed := defaultLoggerWriter.Load() != nil
	if err := FlushDefaultLogger(); err != nil {
		t.Fatalf("FlushDefaultLogger 返回错误: %v", err)
	}
	if !constructed && defaultLoggerWriter.Load() != nil {
		t.Fatal("FlushDefaultLogger 反向构造了默认日志器：会凭空引入 flusher goroutine")
	}
}

// TestFlushDefaultLoggerIdempotent 可重复调用
func TestFlushDefaultLoggerIdempotent(t *testing.T) {
	_ = NewDefaultLogger() // 确保已构造
	for i := 0; i < 3; i++ {
		if err := FlushDefaultLogger(); err != nil {
			t.Fatalf("第 %d 次 FlushDefaultLogger 返回错误: %v", i+1, err)
		}
	}
}

// TestDefaultLoggerIsSingleton 多次调用返回同一实例
//
// 单例是必须的而非优化：NewDefaultLogger 在全仓 19 处作为兜底被调用，
// 其中 adapter/memory 的 panic handler 每次 panic 都会调用一次；
// 而 NewConsoleWriter 无条件启动一个 flusher goroutine，逐次新建即按调用次数泄漏。
func TestDefaultLoggerIsSingleton(t *testing.T) {
	a := NewDefaultLogger()
	b := NewDefaultLogger()
	if a != b {
		t.Fatal("NewDefaultLogger 返回了不同实例：每次调用都会新建 ConsoleWriter 与其 flusher goroutine")
	}
}
