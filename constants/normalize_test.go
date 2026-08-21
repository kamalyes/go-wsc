/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\constants\normalize_test.go
 * @Description: 路由隔离维度空值兜底归一化单元测试
 *
 * 覆盖 NormalizeAppID / NormalizeNamespace / NormalizeGroupID 三个单一入口：
 * 空串补默认值、非空保持原值、默认值本身不二次归一化（幂等）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

import "testing"

// TestNormalizeAppID 验证 appID 归一化：空串补 DefaultAppID，非空保持原值（幂等）
func TestNormalizeAppID(t *testing.T) {
	t.Run("空串补默认应用", func(t *testing.T) {
		if got := NormalizeAppID(""); got != DefaultAppID {
			t.Errorf("NormalizeAppID(\"\") = %q, want %q", got, DefaultAppID)
		}
	})
	t.Run("非空保持原值", func(t *testing.T) {
		if got := NormalizeAppID("app-1"); got != "app-1" {
			t.Errorf("NormalizeAppID(\"app-1\") = %q, want %q", got, "app-1")
		}
	})
	t.Run("DefaultAppID本身不二次归一化", func(t *testing.T) {
		if got := NormalizeAppID(DefaultAppID); got != DefaultAppID {
			t.Errorf("NormalizeAppID(DefaultAppID) = %q, want %q", got, DefaultAppID)
		}
	})
}

// TestNormalizeNamespace 验证 namespace 归一化：空串补 DefaultNamespace，非空保持原值（幂等）
func TestNormalizeNamespace(t *testing.T) {
	t.Run("空串补默认命名空间", func(t *testing.T) {
		if got := NormalizeNamespace(""); got != DefaultNamespace {
			t.Errorf("NormalizeNamespace(\"\") = %q, want %q", got, DefaultNamespace)
		}
	})
	t.Run("非空保持原值", func(t *testing.T) {
		if got := NormalizeNamespace("ns-1"); got != "ns-1" {
			t.Errorf("NormalizeNamespace(\"ns-1\") = %q, want %q", got, "ns-1")
		}
	})
	t.Run("DefaultNamespace本身不二次归一化", func(t *testing.T) {
		if got := NormalizeNamespace(DefaultNamespace); got != DefaultNamespace {
			t.Errorf("NormalizeNamespace(DefaultNamespace) = %q, want %q", got, DefaultNamespace)
		}
	})
}

// TestNormalizeGroupID 验证 groupID 归一化：空串补 DefaultGroupID，非空保持原值（幂等）
func TestNormalizeGroupID(t *testing.T) {
	t.Run("空串补默认组", func(t *testing.T) {
		if got := NormalizeGroupID(""); got != DefaultGroupID {
			t.Errorf("NormalizeGroupID(\"\") = %q, want %q", got, DefaultGroupID)
		}
	})
	t.Run("非空保持原值", func(t *testing.T) {
		if got := NormalizeGroupID("g-100"); got != "g-100" {
			t.Errorf("NormalizeGroupID(\"g-100\") = %q, want %q", got, "g-100")
		}
	})
	t.Run("DefaultGroupID本身不二次归一化", func(t *testing.T) {
		if got := NormalizeGroupID(DefaultGroupID); got != DefaultGroupID {
			t.Errorf("NormalizeGroupID(DefaultGroupID) = %q, want %q", got, DefaultGroupID)
		}
	})
}
