/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-22 00:00:00
 * @FilePath: \go-wsc\repository\online_status_repository_test.go
 * @Description: 压缩策略优化测试（sync.Pool 复用 zlib.Writer + 条件压缩）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ============================================================================
// P0: 压缩策略优化测试
// ============================================================================

// TestZlibCompressWithPool_Basic 测试 Pool 复用的压缩器基本功能
func TestZlibCompressWithPool_Basic(t *testing.T) {
	data := []byte(strings.Repeat("hello world performance optimization test data ", 100))

	compressed, err := zlibCompressWithPool(data)
	assert.NoError(t, err, "压缩不应出错")
	assert.Less(t, len(compressed), len(data), "压缩后应该更小")

	// 验证解压正确性
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	assert.NoError(t, err, "创建解压读取器不应出错")
	defer reader.Close()

	var buf bytes.Buffer
	_, err = buf.ReadFrom(reader)
	assert.NoError(t, err, "解压不应出错")
	assert.Equal(t, data, buf.Bytes(), "解压后数据应与原始数据一致")
}

// TestZlibCompressWithPool_SmallData 测试小数据压缩
func TestZlibCompressWithPool_SmallData(t *testing.T) {
	data := []byte("small")

	compressed, err := zlibCompressWithPool(data)
	assert.NoError(t, err, "小数据压缩不应出错")

	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	assert.NoError(t, err)
	defer reader.Close()

	var buf bytes.Buffer
	_, err = buf.ReadFrom(reader)
	assert.NoError(t, err)
	assert.Equal(t, data, buf.Bytes())
}

// TestZlibCompressWithPool_Concurrent 测试并发压缩安全性
func TestZlibCompressWithPool_Concurrent(t *testing.T) {
	data := []byte(strings.Repeat("concurrent compression test ", 50))

	const numGoroutines = 100
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			compressed, err := zlibCompressWithPool(data)
			if err != nil {
				errors <- err
				return
			}

			reader, err := zlib.NewReader(bytes.NewReader(compressed))
			if err != nil {
				errors <- err
				return
			}
			defer reader.Close()

			var buf bytes.Buffer
			if _, err := buf.ReadFrom(reader); err != nil {
				errors <- err
				return
			}
			if !bytes.Equal(data, buf.Bytes()) {
				errors <- fmt.Errorf("解压数据不一致")
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("并发压缩测试失败: %v", err)
	}
}

// TestZlibCompressWithPool_MemoryReuse 测试 Pool 复用减少内存分配
func TestZlibCompressWithPool_MemoryReuse(t *testing.T) {
	data := []byte(strings.Repeat("memory reuse test data ", 100))

	for i := 0; i < 1000; i++ {
		compressed, err := zlibCompressWithPool(data)
		if err != nil {
			t.Fatalf("第 %d 次压缩失败: %v", i, err)
		}
		if len(compressed) == 0 {
			t.Fatalf("第 %d 次压缩结果为空", i)
		}
	}
}

// TestCompressionThreshold 测试条件压缩阈值
func TestCompressionThreshold(t *testing.T) {
	assert.Equal(t, 512, compressionThreshold, "压缩阈值应为 512 字节")

	largeData := make([]byte, compressionThreshold+1)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	compressed, err := zlibCompressWithPool(largeData)
	assert.NoError(t, err)
	assert.NotEmpty(t, compressed)
}

// ============================================================================
// 压缩性能对比基准测试
// ============================================================================

// BenchmarkZlibCompressWithPool 基准测试：Pool 复用压缩
func BenchmarkZlibCompressWithPool(b *testing.B) {
	data := []byte(strings.Repeat("benchmark test data for compression ", 50))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = zlibCompressWithPool(data)
	}
}

// BenchmarkZlibCompressWithoutPool 基准测试：无 Pool 的传统压缩（对比用）
func BenchmarkZlibCompressWithoutPool(b *testing.B) {
	data := []byte(strings.Repeat("benchmark test data for compression ", 50))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf := new(bytes.Buffer)
		writer := zlib.NewWriter(buf)
		_, _ = writer.Write(data)
		_ = writer.Close()
	}
}
