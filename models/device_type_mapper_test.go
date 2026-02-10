/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-10 00:00:00
 * @FilePath: \go-wsc\models\device_type_mapper_test.go
 * @Description: 设备类型映射器测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMapDeviceTypeToClientType 测试设备类型到客户端类型的映射
func TestMapDeviceTypeToClientType(t *testing.T) {
	tests := []struct {
		name       string
		deviceType string
		want       ClientType
	}{
		{"bot映射到api", DeviceTypeBot, ClientTypeAPI},
		{"mobile映射到mobile", DeviceTypeMobile, ClientTypeMobile},
		{"tablet映射到mobile", DeviceTypeTablet, ClientTypeMobile},
		{"desktop映射到web", DeviceTypeDesktop, ClientTypeWeb},
		{"unknown映射到web", DeviceTypeUnknown, ClientTypeWeb},
		{"空字符串映射到web", "", ClientTypeWeb},
		{"未知类型映射到web", "invalid", ClientTypeWeb},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MapDeviceTypeToClientType(tt.deviceType))
		})
	}
}

// TestMapClientTypeToDeviceType 测试客户端类型到设备类型的反向映射
func TestMapClientTypeToDeviceType(t *testing.T) {
	tests := []struct {
		name       string
		clientType ClientType
		want       string
	}{
		{"api映射到bot", ClientTypeAPI, DeviceTypeBot},
		{"mobile映射到mobile", ClientTypeMobile, DeviceTypeMobile},
		{"desktop映射到desktop", ClientTypeDesktop, DeviceTypeDesktop},
		{"web映射到desktop", ClientTypeWeb, DeviceTypeDesktop},
		{"空字符串映射到unknown", "", DeviceTypeUnknown},
		{"未知类型映射到unknown", "invalid", DeviceTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MapClientTypeToDeviceType(tt.clientType))
		})
	}
}

// TestMapDeviceTypeRoundTrip 测试双向映射的一致性
func TestMapDeviceTypeRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		clientType ClientType
		want       ClientType
	}{
		{"api双向映射保持一致", ClientTypeAPI, ClientTypeAPI},
		{"mobile双向映射保持一致", ClientTypeMobile, ClientTypeMobile},
		{"desktop映射到desktop再映射回web", ClientTypeDesktop, ClientTypeWeb},
		{"web双向映射保持一致", ClientTypeWeb, ClientTypeWeb},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deviceType := MapClientTypeToDeviceType(tt.clientType)
			result := MapDeviceTypeToClientType(deviceType)
			assert.Equal(t, tt.want, result)
		})
	}
}
