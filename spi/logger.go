/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 15:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\spi\logger.go
 * @Description: 日志 SPI - Logger 接口契约与 go-logger 默认实现
 *
 * 日志是核心运行时能力，不是可插拔中间件，因此接口归 spi 而非 middleware
 * 默认实现直接使用 go-logger；业务侧可注入任意满足本接口的日志器
 * （zap / logrus / 自研封装等），不注入则自动使用 go-logger 默认实例
 *
 * 注意：本接口的方法集与 go-logger.ILogger 完全一致，因此
 *   - 任何实现 logger.ILogger 的具体类型（如 *logger.Logger）可直接赋给 Logger
 *   - 任何 logger.ILogger 接口值也可直接赋给 Logger
 * 无需显式转换，业务侧零迁移成本
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
)

// Logger 日志接口，等价于 go-logger.ILogger
type Logger logger.ILogger

// 默认日志器为进程内单例，经 ConsoleWriter 批量写出
//
// 必须单例，不能每次调用新建：NewDefaultLogger 在全仓 19 处作为「未注入日志器」
// 的兜底被调用（各组件构造函数，以及 adapter/memory 的 panic handler），
// 而 logger.NewConsoleWriter 无条件启动一个后台 flusher goroutine——
// 逐次新建会按调用点数量堆积 goroutine，panic handler 那处更会每次 panic 泄漏一个。
//
// 单例化在语义上安全：Logger 即 logger.ILogger，其方法集只有日志方法，
// With* 返回派生实例而非改写自身，接口不暴露任何 Set* 可变方法；
// 且全仓无一处对取得的日志器做可变调用（已核对）。
// defaultLoggerWriter 用 atomic.Value 而非裸变量：sync.Once 的 happens-before
// 只覆盖调用 Do 的 goroutine，FlushDefaultLogger 在 Once 之外读取，
// 裸读与 Do 内的写入构成数据竞争（-race 可检出）。
// defaultLoggerInst 无此问题——它只在 Do 返回后、同一函数内被读取。
var (
	defaultLoggerOnce   sync.Once
	defaultLoggerInst   Logger
	defaultLoggerWriter atomic.Value // logger.IWriter
)

// NewDefaultLogger 返回默认日志器（进程内单例）
//
// 输出经 logger.NewConsoleWriter 批量合并，而非直写 os.Stdout：
// 直写形态下 go-logger 的 isConcurrentSafeOutput 对 os.Stdout 判定为 false
// （其并发安全未受保证），outLockFree 保持 false，于是每行一次互斥 + 一次 write(2)。
// consoleLogWriter 实现 concurrentSafeMarker，WithOutput 会自动把 outLockFree
// 置为 true，互斥与逐行 syscall 一并消除。
//
// 实测（go-wsc 默认配置，真实文件，见 logger_bench_test.go）：
// 串行 1280-1460ns → 215-221ns（约 6.0×），并行 1634-1890ns → 303-381ns（约 5.2×）。
//
// ⚠️ 缓冲意味着进程退出前必须刷出，否则丢失末尾日志。Hub 的 SafeShutdown 已挂
// FlushDefaultLogger；不经 Hub 生命周期的调用方（如独立使用 adapter）须自行调用。
func NewDefaultLogger() Logger {
	defaultLoggerOnce.Do(func() {
		w := logger.NewConsoleWriter(logger.WithConsoleOutput(os.Stdout))
		defaultLoggerWriter.Store(w)
		defaultLoggerInst = logger.NewLogger().
			WithLevel(logger.DEBUG).
			WithPrefix("[WSC]").
			WithShowCaller(false).
			WithColorful(true).
			WithTimeFormat(time.RFC3339Nano).
			WithOutput(w)
	})
	return defaultLoggerInst
}

// FlushDefaultLogger 刷出默认日志器的缓冲
//
// 默认日志器未被构造过时为 no-op（不触发 Once，避免为了刷缓冲反而建出 writer
// 与其 flusher goroutine）。可重复调用。
// 关停收口见 hub.SafeShutdown；panic 路径不依赖本函数——ConsoleWriter 队列满时
// 自行降级为持锁同步直写（writeSync），故进程崩溃前的 panic 日志不经缓冲丢失。
func FlushDefaultLogger() error {
	w, ok := defaultLoggerWriter.Load().(logger.IWriter)
	if !ok || w == nil {
		return nil
	}
	return w.Flush()
}

// InitLogger 根据配置初始化日志器
// Logging 未启用时回退 NewDefaultLogger，启用时使用配置构造的 go-logger 实例
func InitLogger(config *wscconfig.WSC) Logger {
	if config == nil || config.Logging == nil || !config.Logging.Enabled {
		return NewDefaultLogger()
	}

	return config.Logging.ToLoggerInstance()
}
