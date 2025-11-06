/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 10:07:17
 * @FilePath: \go-wsc\config.go
 * @Description: Config 结构体
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import "time"

// Config 结构体表示 WebSocket 客户端的配置
type Config struct {
	WriteWait         time.Duration // 写超时
	MaxMessageSize    int64         // 最大消息长度
	MinRecTime        time.Duration // 最小重连时间
	MaxRecTime        time.Duration // 最大重连时间
	RecFactor         float64       // 重连因子
	MessageBufferSize int           // 消息缓冲池大小
}

// NewDefaultConfig 创建默认配置
func NewDefaultConfig() *Config {
	return &Config{
		WriteWait:         10 * time.Second,
		MaxMessageSize:    512,
		MinRecTime:        2 * time.Second,
		MaxRecTime:        60 * time.Second,
		RecFactor:         1.5,
		MessageBufferSize: 256,
	}
}

// WithWriteWait 设置写超时并返回当前配置对象
func (c *Config) WithWriteWait(d time.Duration) *Config {
	c.WriteWait = d
	return c
}

// WithMaxMessageSize 设置最大消息长度并返回当前配置对象
func (c *Config) WithMaxMessageSize(size int64) *Config {
	c.MaxMessageSize = size
	return c
}

// WithMinRecTime 设置最小重连时间并返回当前配置对象
func (c *Config) WithMinRecTime(d time.Duration) *Config {
	c.MinRecTime = d
	return c
}

// WithMaxRecTime 设置最大重连时间并返回当前配置对象
func (c *Config) WithMaxRecTime(d time.Duration) *Config {
	c.MaxRecTime = d
	return c
}

// WithRecFactor 设置重连因子并返回当前配置对象
func (c *Config) WithRecFactor(factor float64) *Config {
	c.RecFactor = factor
	return c
}

// WithMessageBufferSize 设置消息缓冲池大小并返回当前配置对象
func (c *Config) WithMessageBufferSize(size int) *Config {
	c.MessageBufferSize = size
	return c
}
