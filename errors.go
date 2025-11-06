/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 10:05:09
 * @FilePath: \go-wsc\errors.go
 * @Description: 定义错误变量
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import "errors"

var (
	ErrClose      = errors.New("connection closed")      // 连接关闭错误
	ErrBufferFull = errors.New("message buffer is full") // 消息缓冲区满错误
)
