/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 10:00:51
 * @FilePath: \go-wsc\config_test.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewDefaultConfig(t *testing.T) {
	config := NewDefaultConfig()

	assert.Equal(t, 10*time.Second, config.WriteWait)
	assert.Equal(t, int64(512), config.MaxMessageSize)
	assert.Equal(t, 2*time.Second, config.MinRecTime)
	assert.Equal(t, 60*time.Second, config.MaxRecTime)
	assert.Equal(t, 1.5, config.RecFactor)
	assert.Equal(t, 256, config.MessageBufferSize)
}

func TestConfigMethods(t *testing.T) {
	config := NewDefaultConfig()

	config.WithWriteWait(5 * time.Second)
	assert.Equal(t, 5*time.Second, config.WriteWait)

	config.WithMaxMessageSize(1024)
	assert.Equal(t, int64(1024), config.MaxMessageSize)

	config.WithMinRecTime(1 * time.Second)
	assert.Equal(t, 1*time.Second, config.MinRecTime)

	config.WithMaxRecTime(30 * time.Second)
	assert.Equal(t, 30*time.Second, config.MaxRecTime)

	config.WithRecFactor(2.0)
	assert.Equal(t, 2.0, config.RecFactor)

	config.WithMessageBufferSize(512)
	assert.Equal(t, 512, config.MessageBufferSize)
}
