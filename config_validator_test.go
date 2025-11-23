/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:20:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-23 19:06:08
 * @FilePath: \go-wsc\config_validator_test.go
 * @Description: 配置验证器测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestConfigValidatorBasicFunctionality(t *testing.T) {
	// 创建配置验证器
	validator := NewConfigValidator()
	assert.NotNil(t, validator)

	// 创建有问题的配置
	config := &wscconfig.WSC{
		NodeIP:            "", // 缺失IP
		NodePort:          0,  // 无效端口
		HeartbeatInterval: 0,  // 无效心跳间隔
		ClientTimeout:     0,  // 无效超时
	}

	// 验证配置
	results := validator.Validate(config)
	assert.NotEmpty(t, results)

	// 检查是否检测到了关键错误
	hasNodeIPError := false
	hasNodePortError := false
	hasHeartbeatError := false

	for _, result := range results {
		switch result.Field {
		case "NodeIP":
			hasNodeIPError = true
			assert.Equal(t, ValidationLevelError, result.Level)
		case "NodePort":
			hasNodePortError = true
			assert.Equal(t, ValidationLevelError, result.Level)
		case "HeartbeatInterval":
			hasHeartbeatError = true
			assert.Equal(t, ValidationLevelError, result.Level)
		}
	}

	assert.True(t, hasNodeIPError, "应该检测到NodeIP错误")
	assert.True(t, hasNodePortError, "应该检测到NodePort错误")
	assert.True(t, hasHeartbeatError, "应该检测到HeartbeatInterval错误")
}

func TestConfigValidatorAutoFix(t *testing.T) {
	validator := NewConfigValidator()

	// 创建有问题的配置
	config := &wscconfig.WSC{
		NodeIP:            "",
		NodePort:          0,
		HeartbeatInterval: 0,
		ClientTimeout:     0, // 设为0，这样会满足修复条件
	}

	// 执行自动修复
	fixed, err := validator.AutoFix(config)
	assert.NoError(t, err)
	assert.NotEmpty(t, fixed)

	// 验证修复结果
	assert.Equal(t, "0.0.0.0", config.NodeIP)
	assert.Equal(t, 8080, config.NodePort)
	assert.Equal(t, 30, config.HeartbeatInterval)
	assert.Equal(t, 60, config.ClientTimeout) // 应该是心跳间隔的2倍 (30*2=60)
}

func TestConfigValidatorWithValidConfig(t *testing.T) {
	validator := NewConfigValidator()

	// 创建有效配置
	config := wscconfig.Default()

	// 验证配置
	results := validator.Validate(config)

	// 应该没有严重错误
	for _, result := range results {
		assert.NotEqual(t, ValidationLevelCritical, result.Level)
		assert.NotEqual(t, ValidationLevelError, result.Level)
	}
}

func TestConfigValidatorReport(t *testing.T) {
	validator := NewConfigValidator()

	// 创建有问题的配置
	config := &wscconfig.WSC{
		NodeIP:   "",
		NodePort: 80, // 特权端口，应该产生警告
	}

	// 生成验证报告
	report := validator.ValidateAndReport(config)
	assert.Contains(t, report, "配置验证报告")
	assert.Contains(t, report, "NodeIP")
	assert.Contains(t, report, "NodePort")
}

func TestPerformanceConfigValidation(t *testing.T) {
	validator := NewConfigValidator()

	config := &wscconfig.WSC{
		NodeIP:            "0.0.0.0",
		NodePort:          8080,
		HeartbeatInterval: 30,
		ClientTimeout:     60,
		Performance: &wscconfig.Performance{
			MaxConnectionsPerNode: 0, // 无效值
			ReadBufferSize:        0, // 无效值
			WriteBufferSize:       0, // 无效值
			EnableCompression:     true,
			CompressionLevel:      15, // 无效值
			EnableMetrics:         true,
			MetricsInterval:       0, // 无效值
			EnableSlowLog:         true,
			SlowLogThreshold:      0, // 无效值
		},
	}

	results := validator.Validate(config)

	// 检查性能配置错误
	performanceErrors := 0
	for _, result := range results {
		if result.Level == ValidationLevelError {
			performanceErrors++
		}
	}

	assert.True(t, performanceErrors > 0, "应该检测到性能配置错误")
}

func TestSecurityConfigValidation(t *testing.T) {
	validator := NewConfigValidator()

	config := &wscconfig.WSC{
		NodeIP:            "0.0.0.0",
		NodePort:          8080,
		HeartbeatInterval: 30,
		ClientTimeout:     60,
		Security: &wscconfig.Security{
			MaxMessageSize:   0, // 无效值
			TokenExpiration:  0, // 无效值
			MaxLoginAttempts: 0, // 无效值
		},
	}

	results := validator.Validate(config)

	// 检查安全配置错误
	securityErrors := 0
	for _, result := range results {
		if result.Level == ValidationLevelError &&
			(result.Field == "Security.MaxMessageSize" ||
				result.Field == "Security.TokenExpiration" ||
				result.Field == "Security.MaxLoginAttempts") {
			securityErrors++
		}
	}

	assert.Equal(t, 3, securityErrors, "应该检测到3个安全配置错误")
}

func TestRedisConfigValidation(t *testing.T) {
	validator := NewConfigValidator()

	config := &wscconfig.WSC{
		NodeIP:            "0.0.0.0",
		NodePort:          8080,
		HeartbeatInterval: 30,
		ClientTimeout:     60,
	}

	results := validator.Validate(config)

	// 检查Redis配置错误
	redisErrors := 0
	for _, result := range results {
		if result.Level == ValidationLevelError &&
			(result.Field == "Redis.PoolSize") {
			redisErrors++
		}
	}
}

func TestCustomValidationRule(t *testing.T) {
	validator := NewConfigValidator()

	// 添加自定义验证规则
	customRule := &TestValidationRule{
		name:        "CustomRule",
		description: "自定义测试规则",
		validateFunc: func(config *wscconfig.WSC) []ValidationResult {
			return []ValidationResult{
				{
					Level:      ValidationLevelWarning,
					Field:      "CustomField",
					Message:    "自定义验证消息",
					Suggestion: "自定义建议",
				},
			}
		},
	}

	validator.AddRule(customRule)

	config := wscconfig.Default()
	results := validator.Validate(config)

	// 检查是否包含自定义规则的结果
	hasCustomResult := false
	for _, result := range results {
		if result.Field == "CustomField" {
			hasCustomResult = true
			assert.Equal(t, ValidationLevelWarning, result.Level)
			assert.Equal(t, "自定义验证消息", result.Message)
			break
		}
	}

	assert.True(t, hasCustomResult, "应该包含自定义验证规则的结果")
}

func TestHubConfigValidation(t *testing.T) {
	// 创建带有配置的Hub，但先不初始化AutoFix
	config := &wscconfig.WSC{
		NodeIP:            "",
		NodePort:          0,
		HeartbeatInterval: 0,
		Performance: &wscconfig.Performance{
			MaxConnectionsPerNode: 100,
			EnableMetrics:         true,
			MetricsInterval:       1,
		},
	}

	// 直接创建验证器进行测试，而不是通过Hub（Hub会自动修复）
	validator := NewConfigValidator()

	// 获取验证结果
	results := validator.Validate(config)
	assert.NotEmpty(t, results)
	t.Logf("验证结果数量: %d", len(results))

	// 生成验证报告
	report := validator.ValidateAndReport(config)
	assert.Contains(t, report, "配置验证报告")

	// 尝试自动修复
	fixed, err := validator.AutoFix(config)
	assert.NoError(t, err)
	assert.NotEmpty(t, fixed)

	// 验证修复后的配置确实改善了
	afterResults := validator.Validate(config)
	assert.True(t, len(afterResults) < len(results), "修复后应该减少验证错误数量")
}

// TestValidationRule 测试用的验证规则
type TestValidationRule struct {
	name         string
	description  string
	validateFunc func(*wscconfig.WSC) []ValidationResult
}

func (r *TestValidationRule) GetName() string {
	return r.name
}

func (r *TestValidationRule) GetDescription() string {
	return r.description
}

func (r *TestValidationRule) Validate(config *wscconfig.WSC) []ValidationResult {
	if r.validateFunc != nil {
		return r.validateFunc(config)
	}
	return nil
}

func TestGroupConfigValidation(t *testing.T) {
	validator := NewConfigValidator()

	config := &wscconfig.WSC{
		NodeIP:            "0.0.0.0",
		NodePort:          8080,
		HeartbeatInterval: 30,
		ClientTimeout:     60,
		Group: &wscconfig.Group{
			MaxGroupSize: 0, // 无效值
		},
	}

	results := validator.Validate(config)

	// 检查群组配置错误
	hasGroupError := false
	for _, result := range results {
		if result.Field == "Group.MaxGroupSize" && result.Level == ValidationLevelError {
			hasGroupError = true
			break
		}
	}

	assert.True(t, hasGroupError, "应该检测到群组配置错误")
}

func TestDistributedConfigValidation(t *testing.T) {
	validator := NewConfigValidator()

	config := &wscconfig.WSC{
		NodeIP:            "0.0.0.0",
		NodePort:          8080,
		HeartbeatInterval: 30,
		ClientTimeout:     60,
		Distributed: &wscconfig.Distributed{
			Enabled:     true,
			ClusterName: "", // 启用分布式但未设置集群名称
		},
	}

	results := validator.Validate(config)

	// 检查分布式配置错误
	hasDistributedError := false
	for _, result := range results {
		if result.Field == "Distributed.ClusterName" && result.Level == ValidationLevelError {
			hasDistributedError = true
			break
		}
	}

	assert.True(t, hasDistributedError, "应该检测到分布式配置错误")
}

func TestEnhancementConfigValidation(t *testing.T) {
	validator := NewConfigValidator()

	config := &wscconfig.WSC{
		NodeIP:            "0.0.0.0",
		NodePort:          8080,
		HeartbeatInterval: 30,
		ClientTimeout:     60,
		Enhancement: &wscconfig.Enhancement{
			Enabled:          true,
			FailureThreshold: 0, // 无效值
			SuccessThreshold: 0, // 无效值
			MaxQueueSize:     0, // 无效值
		},
	}

	results := validator.Validate(config)

	// 检查增强功能配置错误
	enhancementErrors := 0
	for _, result := range results {
		if result.Level == ValidationLevelError &&
			(result.Field == "Enhancement.FailureThreshold" ||
				result.Field == "Enhancement.SuccessThreshold" ||
				result.Field == "Enhancement.MaxQueueSize") {
			enhancementErrors++
		}
	}

	assert.Equal(t, 3, enhancementErrors, "应该检测到3个增强功能配置错误")
}
