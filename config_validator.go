/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:15:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 21:17:57
 * @FilePath: \go-wsc\config_validator.go
 * @Description: 配置验证和自动修复机制
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"fmt"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"strings"
	"time"
)

// ValidationLevel 验证级别
type ValidationLevel int

const (
	ValidationLevelInfo     ValidationLevel = 1 // 信息级别
	ValidationLevelWarning  ValidationLevel = 2 // 警告级别
	ValidationLevelError    ValidationLevel = 3 // 错误级别
	ValidationLevelCritical ValidationLevel = 4 // 严重级别
)

// ValidationResult 验证结果
type ValidationResult struct {
	Level       ValidationLevel            `json:"level"`
	Field       string                     `json:"field"`
	Message     string                     `json:"message"`
	Suggestion  string                     `json:"suggestion"`
	AutoFixable bool                       `json:"auto_fixable"`
	FixAction   func(*wscconfig.WSC) error `json:"-"`
}

// ConfigValidator 配置验证器
type ConfigValidator struct {
	rules []ValidationRule
}

// ValidationRule 验证规则接口
type ValidationRule interface {
	// Validate 验证配置
	Validate(config *wscconfig.WSC) []ValidationResult

	// GetName 获取规则名称
	GetName() string

	// GetDescription 获取规则描述
	GetDescription() string
}

// NewConfigValidator 创建配置验证器
func NewConfigValidator() *ConfigValidator {
	validator := &ConfigValidator{
		rules: make([]ValidationRule, 0),
	}

	// 添加默认验证规则
	validator.addDefaultRules()

	return validator
}

// addDefaultRules 添加默认验证规则
func (cv *ConfigValidator) addDefaultRules() {
	cv.rules = append(cv.rules,
		&NodeConfigRule{},
		&PerformanceConfigRule{},
		&SecurityConfigRule{},
		&RedisConfigRule{},
		&GroupConfigRule{},
		&DistributedConfigRule{},
		&EnhancementConfigRule{},
	)
}

// AddRule 添加验证规则
func (cv *ConfigValidator) AddRule(rule ValidationRule) {
	cv.rules = append(cv.rules, rule)
}

// Validate 验证配置
func (cv *ConfigValidator) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	for _, rule := range cv.rules {
		ruleResults := rule.Validate(config)
		results = append(results, ruleResults...)
	}

	return results
}

// AutoFix 自动修复配置
func (cv *ConfigValidator) AutoFix(config *wscconfig.WSC) ([]ValidationResult, error) {
	results := cv.Validate(config)
	fixed := make([]ValidationResult, 0)

	for _, result := range results {
		if result.AutoFixable && result.FixAction != nil {
			if err := result.FixAction(config); err != nil {
				return fixed, errorx.NewError(ErrTypeConfigAutoFixFailed, "failed to fix %s: %v", result.Field, err)
			}
			// 创建一个修复结果的记录
			fixedResult := ValidationResult{
				Level:       ValidationLevelInfo,
				Field:       result.Field,
				Message:     fmt.Sprintf("已自动修复: %s", result.Message),
				Suggestion:  result.Suggestion,
				AutoFixable: false, // 已经修复了，不再需要修复
			}
			fixed = append(fixed, fixedResult)
		}
	}

	return fixed, nil
}

// ValidateAndReport 验证并生成报告
func (cv *ConfigValidator) ValidateAndReport(config *wscconfig.WSC) string {
	results := cv.Validate(config)

	var report strings.Builder
	report.WriteString("配置验证报告\n")
	report.WriteString("================\n\n")

	errorCount := 0
	warningCount := 0
	infoCount := 0
	criticalCount := 0

	for _, result := range results {
		switch result.Level {
		case ValidationLevelCritical:
			criticalCount++
			report.WriteString(fmt.Sprintf("🚨 [严重] %s: %s\n", result.Field, result.Message))
		case ValidationLevelError:
			errorCount++
			report.WriteString(fmt.Sprintf("❌ [错误] %s: %s\n", result.Field, result.Message))
		case ValidationLevelWarning:
			warningCount++
			report.WriteString(fmt.Sprintf("⚠️ [警告] %s: %s\n", result.Field, result.Message))
		case ValidationLevelInfo:
			infoCount++
			report.WriteString(fmt.Sprintf("ℹ️ [信息] %s: %s\n", result.Field, result.Message))
		}

		if result.Suggestion != "" {
			report.WriteString(fmt.Sprintf("   建议: %s\n", result.Suggestion))
		}

		if result.AutoFixable {
			report.WriteString(fmt.Sprintf("   💡 可自动修复\n"))
		}

		report.WriteString("\n")
	}

	report.WriteString(fmt.Sprintf("汇总: 严重=%d, 错误=%d, 警告=%d, 信息=%d\n",
		criticalCount, errorCount, warningCount, infoCount))

	return report.String()
}

// ========== 具体验证规则实现 ==========

// NodeConfigRule 节点配置验证规则
type NodeConfigRule struct{}

func (r *NodeConfigRule) GetName() string {
	return "NodeConfig"
}

func (r *NodeConfigRule) GetDescription() string {
	return "验证节点基础配置"
}

func (r *NodeConfigRule) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	// 检查节点IP
	if config.NodeIP == "" {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "NodeIP",
			Message:     "节点IP未设置",
			Suggestion:  "设置节点IP地址，推荐使用0.0.0.0监听所有接口",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.NodeIP = "0.0.0.0"
				return nil
			},
		})
	}

	// 检查节点端口
	if config.NodePort <= 0 || config.NodePort > 65535 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "NodePort",
			Message:     fmt.Sprintf("节点端口无效: %d", config.NodePort),
			Suggestion:  "设置有效的端口号 (1-65535)，推荐使用8080",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.NodePort = 8080
				return nil
			},
		})
	} else if config.NodePort < 1024 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "NodePort",
			Message:    fmt.Sprintf("使用特权端口: %d", config.NodePort),
			Suggestion: "考虑使用非特权端口 (>1024) 以提高安全性",
		})
	}

	// 检查心跳间隔
	if config.HeartbeatInterval <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "HeartbeatInterval",
			Message:     "心跳间隔必须大于0",
			Suggestion:  "推荐设置为30秒",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.HeartbeatInterval = 30
				return nil
			},
		})
	} else if config.HeartbeatInterval < 10 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "HeartbeatInterval",
			Message:    fmt.Sprintf("心跳间隔过短: %d秒", config.HeartbeatInterval),
			Suggestion: "推荐设置为30-60秒之间",
		})
	} else if config.HeartbeatInterval > 300 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "HeartbeatInterval",
			Message:    fmt.Sprintf("心跳间隔过长: %d秒", config.HeartbeatInterval),
			Suggestion: "过长的心跳间隔可能导致连接检测不及时",
		})
	}

	// 检查客户端超时
	if config.ClientTimeout <= config.HeartbeatInterval {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "ClientTimeout",
			Message:     "客户端超时时间应该大于心跳间隔",
			Suggestion:  fmt.Sprintf("推荐设置为心跳间隔的2-3倍: %d秒", config.HeartbeatInterval*2),
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.ClientTimeout = c.HeartbeatInterval * 2
				return nil
			},
		})
	}

	return results
}

// PerformanceConfigRule 性能配置验证规则
type PerformanceConfigRule struct{}

func (r *PerformanceConfigRule) GetName() string {
	return "PerformanceConfig"
}

func (r *PerformanceConfigRule) GetDescription() string {
	return "验证性能配置参数"
}

func (r *PerformanceConfigRule) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	if config.Performance == nil {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Performance",
			Message:    "性能配置未设置，将使用默认值",
			Suggestion: "建议明确设置性能配置以优化系统性能",
		})
		return results
	}

	perf := config.Performance

	// 检查最大连接数
	if perf.MaxConnectionsPerNode <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Performance.MaxConnectionsPerNode",
			Message:     "最大连接数必须大于0",
			Suggestion:  "推荐设置为10000",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Performance.MaxConnectionsPerNode = 10000
				return nil
			},
		})
	} else if perf.MaxConnectionsPerNode > 50000 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Performance.MaxConnectionsPerNode",
			Message:    fmt.Sprintf("最大连接数过高: %d", perf.MaxConnectionsPerNode),
			Suggestion: "过高的连接数可能消耗大量系统资源",
		})
	}

	// 检查缓冲区大小
	if perf.ReadBufferSize <= 0 || perf.WriteBufferSize <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Performance.BufferSize",
			Message:     "读写缓冲区大小必须大于0",
			Suggestion:  "推荐设置读写缓冲区为4KB",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				if c.Performance.ReadBufferSize <= 0 {
					c.Performance.ReadBufferSize = 4
				}
				if c.Performance.WriteBufferSize <= 0 {
					c.Performance.WriteBufferSize = 4
				}
				return nil
			},
		})
	}

	// 检查压缩级别
	if perf.EnableCompression && (perf.CompressionLevel < 1 || perf.CompressionLevel > 9) {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Performance.CompressionLevel",
			Message:     fmt.Sprintf("压缩级别无效: %d", perf.CompressionLevel),
			Suggestion:  "压缩级别应该在1-9之间，推荐使用6",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Performance.CompressionLevel = 6
				return nil
			},
		})
	}

	// 检查指标采集间隔
	if perf.EnableMetrics && perf.MetricsInterval <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Performance.MetricsInterval",
			Message:     "启用指标采集时，采集间隔必须大于0",
			Suggestion:  "推荐设置为60秒",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Performance.MetricsInterval = 60
				return nil
			},
		})
	} else if perf.EnableMetrics && perf.MetricsInterval < 10 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Performance.MetricsInterval",
			Message:    fmt.Sprintf("指标采集间隔过短: %d秒", perf.MetricsInterval),
			Suggestion: "过短的采集间隔可能影响性能",
		})
	}

	// 检查慢日志阈值
	if perf.EnableSlowLog && perf.SlowLogThreshold <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Performance.SlowLogThreshold",
			Message:     "启用慢日志时，阈值必须大于0",
			Suggestion:  "推荐设置为1000毫秒",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Performance.SlowLogThreshold = 1000
				return nil
			},
		})
	}

	return results
}

// SecurityConfigRule 安全配置验证规则
type SecurityConfigRule struct{}

func (r *SecurityConfigRule) GetName() string {
	return "SecurityConfig"
}

func (r *SecurityConfigRule) GetDescription() string {
	return "验证安全配置参数"
}

func (r *SecurityConfigRule) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	if config.Security == nil {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Security",
			Message:    "安全配置未设置，将使用默认值",
			Suggestion: "建议明确设置安全配置以提高系统安全性",
		})
		return results
	}

	security := config.Security

	// 检查消息大小限制
	if security.MaxMessageSize <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Security.MaxMessageSize",
			Message:     "最大消息大小必须大于0",
			Suggestion:  "推荐设置为1024KB",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Security.MaxMessageSize = 1024
				return nil
			},
		})
	} else if security.MaxMessageSize > 10*1024 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Security.MaxMessageSize",
			Message:    fmt.Sprintf("最大消息大小过大: %dKB", security.MaxMessageSize),
			Suggestion: "过大的消息可能影响性能和安全性",
		})
	}

	// 检查Token过期时间
	if security.TokenExpiration <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Security.TokenExpiration",
			Message:     "Token过期时间必须大于0",
			Suggestion:  "推荐设置为3600秒（1小时）",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Security.TokenExpiration = 3600
				return nil
			},
		})
	} else if security.TokenExpiration < 300 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Security.TokenExpiration",
			Message:    fmt.Sprintf("Token过期时间过短: %d秒", security.TokenExpiration),
			Suggestion: "过短的过期时间可能影响用户体验",
		})
	} else if security.TokenExpiration > 24*3600 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Security.TokenExpiration",
			Message:    fmt.Sprintf("Token过期时间过长: %d秒", security.TokenExpiration),
			Suggestion: "过长的过期时间可能存在安全风险",
		})
	}

	// 检查登录尝试次数
	if security.MaxLoginAttempts <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Security.MaxLoginAttempts",
			Message:     "最大登录尝试次数必须大于0",
			Suggestion:  "推荐设置为5次",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Security.MaxLoginAttempts = 5
				return nil
			},
		})
	} else if security.MaxLoginAttempts > 20 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Security.MaxLoginAttempts",
			Message:    fmt.Sprintf("最大登录尝试次数过高: %d", security.MaxLoginAttempts),
			Suggestion: "过高的尝试次数可能降低安全性",
		})
	}

	// 检查用户类型配置
	if len(security.AllowedUserTypes) == 0 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Security.AllowedUserTypes",
			Message:    "未设置允许的用户类型",
			Suggestion: "建议明确设置允许的用户类型以提高安全性",
		})
	}

	return results
}

// RedisConfigRule Redis配置验证规则
type RedisConfigRule struct{}

func (r *RedisConfigRule) GetName() string {
	return "RedisConfig"
}

func (r *RedisConfigRule) GetDescription() string {
	return "验证Redis配置参数"
}

func (r *RedisConfigRule) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	if config.Redis == nil {
		return results // Redis是可选的
	}

	redis := config.Redis

	// 检查Redis地址
	if redis.Addr == "" {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Redis.Addr",
			Message:     "Redis地址未设置",
			Suggestion:  "设置Redis服务器地址，如: localhost:6379",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Redis.Addr = "localhost:6379"
				return nil
			},
		})
	}

	// 检查连接池大小
	if redis.PoolSize <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Redis.PoolSize",
			Message:     "Redis连接池大小必须大于0",
			Suggestion:  "推荐设置为10",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Redis.PoolSize = 10
				return nil
			},
		})
	}

	return results
}

// GroupConfigRule 群组配置验证规则
type GroupConfigRule struct{}

func (r *GroupConfigRule) GetName() string {
	return "GroupConfig"
}

func (r *GroupConfigRule) GetDescription() string {
	return "验证群组配置参数"
}

func (r *GroupConfigRule) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	if config.Group == nil {
		return results // 群组功能是可选的
	}

	group := config.Group

	// 检查最大群组大小
	if group.MaxGroupSize <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Group.MaxGroupSize",
			Message:     "最大群组大小必须大于0",
			Suggestion:  "推荐设置为1000",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Group.MaxGroupSize = 1000
				return nil
			},
		})
	} else if group.MaxGroupSize > 10000 {
		results = append(results, ValidationResult{
			Level:      ValidationLevelWarning,
			Field:      "Group.MaxGroupSize",
			Message:    fmt.Sprintf("最大群组大小过大: %d", group.MaxGroupSize),
			Suggestion: "过大的群组可能影响性能",
		})
	}

	return results
}

// DistributedConfigRule 分布式配置验证规则
type DistributedConfigRule struct{}

func (r *DistributedConfigRule) GetName() string {
	return "DistributedConfig"
}

func (r *DistributedConfigRule) GetDescription() string {
	return "验证分布式配置参数"
}

func (r *DistributedConfigRule) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	if config.Distributed == nil || !config.Distributed.Enabled {
		return results // 分布式功能未启用
	}

	distributed := config.Distributed

	// 检查集群名称
	if distributed.ClusterName == "" {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Distributed.ClusterName",
			Message:     "启用分布式时必须设置集群名称",
			Suggestion:  "设置唯一的集群名称",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Distributed.ClusterName = fmt.Sprintf("cluster-%d", time.Now().Unix())
				return nil
			},
		})
	}

	return results
}

// EnhancementConfigRule 增强功能配置验证规则
type EnhancementConfigRule struct{}

func (r *EnhancementConfigRule) GetName() string {
	return "EnhancementConfig"
}

func (r *EnhancementConfigRule) GetDescription() string {
	return "验证增强功能配置参数"
}

func (r *EnhancementConfigRule) Validate(config *wscconfig.WSC) []ValidationResult {
	var results []ValidationResult

	if config.Enhancement == nil || !config.Enhancement.Enabled {
		return results // 增强功能未启用
	}

	enhancement := config.Enhancement

	// 检查失败阈值
	if enhancement.FailureThreshold <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Enhancement.FailureThreshold",
			Message:     "失败阈值必须大于0",
			Suggestion:  "推荐设置为5",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Enhancement.FailureThreshold = 5
				return nil
			},
		})
	}

	// 检查成功阈值
	if enhancement.SuccessThreshold <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Enhancement.SuccessThreshold",
			Message:     "成功阈值必须大于0",
			Suggestion:  "推荐设置为3",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Enhancement.SuccessThreshold = 3
				return nil
			},
		})
	}

	// 检查队列大小
	if enhancement.MaxQueueSize <= 0 {
		results = append(results, ValidationResult{
			Level:       ValidationLevelError,
			Field:       "Enhancement.MaxQueueSize",
			Message:     "最大队列大小必须大于0",
			Suggestion:  "推荐设置为1000",
			AutoFixable: true,
			FixAction: func(c *wscconfig.WSC) error {
				c.Enhancement.MaxQueueSize = 1000
				return nil
			},
		})
	}

	return results
}
