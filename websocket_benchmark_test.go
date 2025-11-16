package wsc

import (
	"fmt"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// setupBenchServer 创建测试服务器并返回连接的客户端
func setupBenchServer(b *testing.B) (*httptest.Server, *Wsc) {
	server := httptest.NewServer(http.HandlerFunc(handleConnection))
	url := fmt.Sprintf("ws://%s/ws", server.Listener.Addr().String())
	ws := New(url)

	// 设置更大的缓冲区
	config := wscconfig.Default().WithMessageBufferSize(10000)
	ws.SetConfig(config) // 等待连接建立
	connected := make(chan struct{})
	ws.OnConnected(func() {
		close(connected)
	})

	go ws.Connect()

	select {
	case <-connected:
		// 连接成功
	case <-time.After(2 * time.Second):
		b.Fatal("连接超时")
	}

	return server, ws
}

// BenchmarkSendTextMessage_Small 测试发送小文本消息的性能
func BenchmarkSendTextMessage_Small(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	// 增加缓冲区大小
	config := wscconfig.Default().WithMessageBufferSize(10000)
	ws.SetConfig(config)

	msg := "hello"
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := ws.SendTextMessage(msg); err != nil {
			// 缓冲区满时等待一下
			if err == ErrBufferFull {
				time.Sleep(time.Microsecond)
				i-- // 重试
				continue
			}
			b.Fatal(err)
		}
	}
}

// BenchmarkSendTextMessage_Medium 测试发送中等文本消息的性能
func BenchmarkSendTextMessage_Medium(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	msg := string(make([]byte, 1024)) // 1KB
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := ws.SendTextMessage(msg); err != nil {
			if err == ErrBufferFull {
				time.Sleep(time.Microsecond)
				i--
				continue
			}
			b.Fatal(err)
		}
	}
}

// BenchmarkSendTextMessage_Large 测试发送大文本消息的性能
func BenchmarkSendTextMessage_Large(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	msg := string(make([]byte, 64*1024)) // 64KB
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := ws.SendTextMessage(msg); err != nil {
			if err == ErrBufferFull {
				time.Sleep(time.Microsecond)
				i--
				continue
			}
			b.Fatal(err)
		}
	}
}

// BenchmarkSendBinaryMessage 测试发送二进制消息的性能
func BenchmarkSendBinaryMessage(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	data := make([]byte, 1024)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := ws.SendBinaryMessage(data); err != nil {
			if err == ErrBufferFull {
				time.Sleep(time.Microsecond)
				i--
				continue
			}
			b.Fatal(err)
		}
	}
}

// BenchmarkSendTextMessage_Parallel 测试并发发送消息的性能
func BenchmarkSendTextMessage_Parallel(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	msg := "concurrent message"
	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for {
				if err := ws.SendTextMessage(msg); err != nil {
					if err == ErrBufferFull {
						time.Sleep(time.Microsecond)
						continue
					}
					b.Error(err)
					return
				}
				break
			}
		}
	})
}

// BenchmarkConnect 测试连接建立的性能
func BenchmarkConnect(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(handleConnection))
	defer server.Close()
	url := fmt.Sprintf("ws://%s/ws", server.Listener.Addr().String())

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ws := New(url)
		connected := make(chan struct{})
		ws.OnConnected(func() {
			close(connected)
		})
		go ws.Connect()
		<-connected
		ws.Close()
	}
}

// BenchmarkCallback_OnConnected 测试连接成功回调的性能
func BenchmarkCallback_OnConnected(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(handleConnection))
	defer server.Close()
	url := fmt.Sprintf("ws://%s/ws", server.Listener.Addr().String())

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ws := New(url)
		var count int64
		ws.OnConnected(func() {
			atomic.AddInt64(&count, 1)
		})
		connected := make(chan struct{})
		ws.OnConnected(func() {
			close(connected)
		})
		go ws.Connect()
		<-connected
		ws.Close()
	}
}

// BenchmarkCallback_OnTextMessageReceived 测试文本消息接收回调的性能
func BenchmarkCallback_OnTextMessageReceived(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	var count int64
	ws.OnTextMessageReceived(func(message string) {
		atomic.AddInt64(&count, 1)
	})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := ws.SendTextMessage("test"); err != nil {
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond)
					continue
				}
				b.Fatal(err)
			}
			break
		}
	}

	// 等待所有消息处理完成
	time.Sleep(100 * time.Millisecond)
}

// BenchmarkCallback_OnBinaryMessageReceived 测试二进制消息接收回调的性能
func BenchmarkCallback_OnBinaryMessageReceived(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	var count int64
	ws.OnBinaryMessageReceived(func(data []byte) {
		atomic.AddInt64(&count, 1)
	})

	data := make([]byte, 256)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := ws.SendBinaryMessage(data); err != nil {
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond)
					continue
				}
				b.Fatal(err)
			}
			break
		}
	}

	// 等待所有消息处理完成
	time.Sleep(100 * time.Millisecond)
}

// BenchmarkCallback_OnTextMessageSent 测试文本消息发送成功回调的性能
func BenchmarkCallback_OnTextMessageSent(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	var count int64
	ws.OnTextMessageSent(func(message string) {
		atomic.AddInt64(&count, 1)
	})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := ws.SendTextMessage("callback test"); err != nil {
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond)
					continue
				}
				b.Fatal(err)
			}
			break
		}
	}

	time.Sleep(100 * time.Millisecond)
}

// BenchmarkCallback_OnBinaryMessageSent 测试二进制消息发送成功回调的性能
func BenchmarkCallback_OnBinaryMessageSent(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	var count int64
	ws.OnBinaryMessageSent(func(data []byte) {
		atomic.AddInt64(&count, 1)
	})

	data := make([]byte, 256)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := ws.SendBinaryMessage(data); err != nil {
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond)
					continue
				}
				b.Fatal(err)
			}
			break
		}
	}

	time.Sleep(100 * time.Millisecond)
}

// BenchmarkCallback_OnClose 测试连接关闭回调的性能
func BenchmarkCallback_OnClose(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(handleConnection))
	defer server.Close()
	url := fmt.Sprintf("ws://%s/ws", server.Listener.Addr().String())

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ws := New(url)
		var count int64
		ws.OnClose(func(code int, text string) {
			atomic.AddInt64(&count, 1)
		})
		connected := make(chan struct{})
		ws.OnConnected(func() {
			close(connected)
		})
		go ws.Connect()
		<-connected
		ws.Close()
		time.Sleep(10 * time.Millisecond) // 等待关闭回调执行
	}
}

// BenchmarkCallback_OnDisconnected 测试断线重连回调的性能
func BenchmarkCallback_OnDisconnected(b *testing.B) {
	// 此测试需要模拟服务器断开，较复杂，暂时跳过
	b.Skip("需要特殊的断线场景模拟")
}

// BenchmarkCallback_OnPingReceived 测试 Ping 消息回调的性能
func BenchmarkCallback_OnPingReceived(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	var count int64
	ws.OnPingReceived(func(appData string) {
		atomic.AddInt64(&count, 1)
	})

	b.ResetTimer()
	b.ReportAllocs()

	// Ping 消息由服务器发送，这里只能间接测试
	for i := 0; i < b.N; i++ {
		_ = ws.SendTextMessage("trigger")
	}
}

// BenchmarkCallback_OnPongReceived 测试 Pong 消息回调的性能
func BenchmarkCallback_OnPongReceived(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	var count int64
	ws.OnPongReceived(func(appData string) {
		atomic.AddInt64(&count, 1)
	})

	b.ResetTimer()
	b.ReportAllocs()

	// Pong 消息由服务器发送，这里只能间接测试
	for i := 0; i < b.N; i++ {
		_ = ws.SendTextMessage("trigger")
	}
}

// BenchmarkCallback_MultipleCallbacks 测试多个回调同时触发的性能
func BenchmarkCallback_MultipleCallbacks(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	var count int64
	ws.OnTextMessageReceived(func(message string) {
		atomic.AddInt64(&count, 1)
	})
	ws.OnTextMessageSent(func(message string) {
		atomic.AddInt64(&count, 1)
	})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := ws.SendTextMessage("multi callback test"); err != nil {
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond)
					continue
				}
				b.Fatal(err)
			}
			break
		}
	}

	time.Sleep(100 * time.Millisecond)
}

// BenchmarkHighThroughput 测试高吞吐量场景
func BenchmarkHighThroughput(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	// 设置更大的缓冲区
	config := wscconfig.Default().WithMessageBufferSize(50000)
	ws.SetConfig(config)

	var wg sync.WaitGroup
	msg := "high throughput message"

	b.ResetTimer()
	b.ReportAllocs()

	// 模拟多个发送者
	numSenders := 10
	msgsPerSender := b.N / numSenders

	for i := 0; i < numSenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < msgsPerSender; j++ {
				for {
					if err := ws.SendTextMessage(msg); err != nil {
						if err == ErrBufferFull {
							time.Sleep(time.Microsecond)
							continue
						}
						return
					}
					break
				}
			}
		}()
	}

	wg.Wait()
}

// BenchmarkMemoryAllocation 专门测试内存分配情况
func BenchmarkMemoryAllocation(b *testing.B) {
	server, ws := setupBenchServer(b)
	defer server.Close()
	defer ws.Close()

	msg := "memory test"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := ws.SendTextMessage(msg); err != nil {
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond)
					continue
				}
				b.Fatal(err)
			}
			break
		}
	}

	// 输出内存分配统计
	b.StopTimer()
}
