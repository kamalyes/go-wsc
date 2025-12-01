/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 09:26:28
 * @FilePath: \go-wsc\errors.go
 * @Description: WebSocket 通信错误定义 - 基于errorx.BaseError模式
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"github.com/kamalyes/go-toolbox/pkg/errorx"
)

// 错误类型定义，基于errorx.ErrorType
type ErrorType = errorx.ErrorType

// WebSocket 通信错误码常量定义
// 使用 8xxxx 区间，避免与其他包冲突（WSC = WebSocket Client）
const (
	// 基础错误恢复类型 (80000-80099) - error_recovery.go中使用
	ErrorTypeConnection    ErrorType = 80000 // 连接错误
	ErrorTypeMessage       ErrorType = 80001 // 消息错误
	ErrorTypeSystem        ErrorType = 80002 // 系统错误
	ErrorTypeNetwork       ErrorType = 80003 // 网络错误
	ErrorTypeConcurrency   ErrorType = 80004 // 并发错误
	ErrorTypeMemory        ErrorType = 80005 // 内存错误
	ErrorTypeConfiguration ErrorType = 80006 // 配置错误

	// 连接相关错误 (80100-80199) - 可重试
	ErrTypeConnectionClosed   ErrorType = 80101 // 连接已关闭
	ErrTypeConnectionReset    ErrorType = 80102 // 连接重置
	ErrTypeConnectionTimeout  ErrorType = 80103 // 连接超时
	ErrTypeNetworkUnreachable ErrorType = 80104 // 网络不可达
	ErrTypeServiceUnavailable ErrorType = 80105 // 服务不可用

	// 队列和缓冲区错误 (80200-80299) - 可重试
	ErrTypeQueueFull           ErrorType = 80201 // 队列已满
	ErrTypeMessageBufferFull   ErrorType = 80202 // 消息缓冲区已满
	ErrTypePendingQueueFull    ErrorType = 80203 // 待处理队列已满
	ErrTypeQueueAndPendingFull ErrorType = 80204 // 队列和待处理队列均已满

	// 用户和认证错误 (80300-80399) - 不可重试
	ErrTypeUserOffline          ErrorType = 80301 // 用户离线
	ErrTypeUserNotFound         ErrorType = 80302 // 用户未找到
	ErrTypePermissionDenied     ErrorType = 80303 // 权限被拒绝
	ErrTypeAuthenticationFailed ErrorType = 80304 // 认证失败
	ErrTypeUnauthorized         ErrorType = 80305 // 未经授权的访问

	// 消息错误 (80400-80499) - 不可重试
	ErrTypeInvalidMessageFormat   ErrorType = 80401 // 无效的消息格式
	ErrTypeMessageTooLarge        ErrorType = 80402 // 消息过大
	ErrTypeMessageTargetMissing   ErrorType = 80403 // 消息目标未指定
	ErrTypeMessageFiltered        ErrorType = 80404 // 消息被规则过滤
	ErrTypeMessageDeliveryTimeout ErrorType = 80405 // 消息投递超时

	// 客户端错误 (80500-80599) - 不可重试
	ErrTypeClientNotFound     ErrorType = 80501 // 客户端未找到
	ErrTypeClientDisconnected ErrorType = 80502 // 客户端已断开连接
	ErrTypeNoAvailableAgents  ErrorType = 80503 // 没有可用的代理

	// 集线器操作错误 (80600-80699) - 混合可重试性
	ErrTypeHubStartupTimeout  ErrorType = 80601 // 集线器启动超时 - 可重试
	ErrTypeHubShutdownTimeout ErrorType = 80602 // 集线器关闭超时 - 可重试
	ErrTypeHubNotRunning      ErrorType = 80603 // 集线器未运行 - 不可重试
	ErrTypeCircuitBreakerOpen ErrorType = 80604 // 电路断路器已打开 - 可重试

	// 记录管理错误 (80700-80799) - 不可重试
	ErrTypeRecordManagerDisabled        ErrorType = 80701 // 记录管理器已禁用
	ErrTypeMessageRecordNotFound        ErrorType = 80702 // 消息记录未找到
	ErrTypeMessageAlreadySent           ErrorType = 80703 // 消息已成功发送
	ErrTypeMaxRetriesExceeded           ErrorType = 80704 // 超过最大重试次数
	ErrTypeRecordManagerNotInitialized  ErrorType = 80705 // 记录管理器未初始化
	ErrTypeMaxRetriesExceededForMessage ErrorType = 80706 // 消息重试次数超过最大限制

	// 速率限制错误 (80800-80899) - 不可重试
	ErrTypeRateLimitExceeded     ErrorType = 80801 // 超过速率限制
	ErrTypeFrequencyLimitReached ErrorType = 80802 // 达到频率限制

	// 操作错误 (80900-80999) - 可重试
	ErrTypeOperationTimeout ErrorType = 80901 // 操作超时
	ErrTypeTemporaryFailure ErrorType = 80902 // 临时故障
	ErrTypeResourceBusy     ErrorType = 80903 // 资源繁忙
	ErrTypeUnknownError     ErrorType = 80999 // 未知错误

	// ACK相关错误 (81000-81099) - 混合可重试性
	ErrTypeAckTimeout        ErrorType = 81001 // ACK超时 - 可重试
	ErrTypeAckTimeoutRetries ErrorType = 81002 // ACK经重试后超时 - 不可重试
	ErrTypeContextCancelled  ErrorType = 81003 // 上下文取消 - 不可重试

	// 配置相关错误 (81100-81199) - 不可重试
	ErrTypeConfigValidatorNotInitialized ErrorType = 81101 // 配置验证器未初始化
	ErrTypeConfigValidationFailed        ErrorType = 81102 // 配置验证失败
	ErrTypeConfigAutoFixFailed           ErrorType = 81103 // 配置自动修复失败

	// 安全相关错误 (81200-81299) - 不可重试
	ErrTypeIPInBlacklist      ErrorType = 81201 // IP在黑名单中
	ErrTypeBruteForceDetected ErrorType = 81202 // 检测到暴力攻击
	ErrTypeThreatDetected     ErrorType = 81203 // 检测到威胁内容
	ErrTypeAccessDeniedByRule ErrorType = 81204 // 被访问规则拒绝

	// 消息记录仓库相关错误 (81300-81399) - 不可重试
	ErrTypeRecordRepositoryNotSet ErrorType = 81301 // 消息记录仓库未设置
)

// init 初始化所有错误类型注册
// 注意：在运行多个测试包时，可能会看到 "ErrorType XXX is already registered" 的警告信息
// 这是正常现象，因为每个测试包会独立加载并初始化此包
// 这些警告不影响功能，errorx包内部会忽略重复注册
func init() {
	// 注册基础错误恢复类型
	errorx.RegisterError(ErrorTypeConnection, "connection error")
	errorx.RegisterError(ErrorTypeMessage, "message error")
	errorx.RegisterError(ErrorTypeSystem, "system error")
	errorx.RegisterError(ErrorTypeNetwork, "network error")
	errorx.RegisterError(ErrorTypeConcurrency, "concurrency error")
	errorx.RegisterError(ErrorTypeMemory, "memory error")
	errorx.RegisterError(ErrorTypeConfiguration, "configuration error")

	// 注册连接相关错误
	errorx.RegisterError(ErrTypeConnectionClosed, "connection closed")
	errorx.RegisterError(ErrTypeConnectionReset, "connection reset")
	errorx.RegisterError(ErrTypeConnectionTimeout, "connection timeout")
	errorx.RegisterError(ErrTypeNetworkUnreachable, "network unreachable")
	errorx.RegisterError(ErrTypeServiceUnavailable, "service unavailable")

	// 注册队列和缓冲区错误
	errorx.RegisterError(ErrTypeQueueFull, "queue is full")
	errorx.RegisterError(ErrTypeMessageBufferFull, "message buffer is full")
	errorx.RegisterError(ErrTypePendingQueueFull, "pending queue is full")
	errorx.RegisterError(ErrTypeQueueAndPendingFull, "both queue and pending queue are full")

	// 注册用户和认证错误
	errorx.RegisterError(ErrTypeUserOffline, "user is offline")
	errorx.RegisterError(ErrTypeUserNotFound, "user not found: %s")
	errorx.RegisterError(ErrTypePermissionDenied, "permission denied")
	errorx.RegisterError(ErrTypeAuthenticationFailed, "authentication failed")
	errorx.RegisterError(ErrTypeUnauthorized, "unauthorized access")

	// 注册消息错误
	errorx.RegisterError(ErrTypeInvalidMessageFormat, "invalid message format")
	errorx.RegisterError(ErrTypeMessageTooLarge, "message too large")
	errorx.RegisterError(ErrTypeMessageTargetMissing, "message target not specified")
	errorx.RegisterError(ErrTypeMessageFiltered, "message filtered by rules")
	errorx.RegisterError(ErrTypeMessageDeliveryTimeout, "message delivery timeout")

	// 注册客户端错误
	errorx.RegisterError(ErrTypeClientNotFound, "client not found: %s")
	errorx.RegisterError(ErrTypeClientDisconnected, "client disconnected")
	errorx.RegisterError(ErrTypeNoAvailableAgents, "no available agents")

	// 注册集线器操作错误
	errorx.RegisterError(ErrTypeHubStartupTimeout, "hub startup timeout")
	errorx.RegisterError(ErrTypeHubShutdownTimeout, "hub shutdown timeout")
	errorx.RegisterError(ErrTypeHubNotRunning, "hub is not running")
	errorx.RegisterError(ErrTypeCircuitBreakerOpen, "circuit breaker is open")

	// 注册记录管理错误
	errorx.RegisterError(ErrTypeRecordManagerDisabled, "record manager is disabled")
	errorx.RegisterError(ErrTypeMessageRecordNotFound, "message record not found: %s")
	errorx.RegisterError(ErrTypeMessageAlreadySent, "message already sent successfully")
	errorx.RegisterError(ErrTypeMaxRetriesExceeded, "maximum retries exceeded")
	errorx.RegisterError(ErrTypeRecordManagerNotInitialized, "record manager not initialized")
	errorx.RegisterError(ErrTypeMaxRetriesExceededForMessage, "maximum retries exceeded for message")

	// 注册速率限制错误
	errorx.RegisterError(ErrTypeRateLimitExceeded, "rate limit exceeded")
	errorx.RegisterError(ErrTypeFrequencyLimitReached, "frequency limit reached")

	// 注册操作错误
	errorx.RegisterError(ErrTypeOperationTimeout, "operation timeout")
	errorx.RegisterError(ErrTypeTemporaryFailure, "temporary failure")
	errorx.RegisterError(ErrTypeResourceBusy, "resource busy")
	errorx.RegisterError(ErrTypeUnknownError, "unknown error")

	// 注册ACK相关错误
	errorx.RegisterError(ErrTypeAckTimeout, "ack timeout")
	errorx.RegisterError(ErrTypeAckTimeoutRetries, "ack timeout after %d retries for message %s")
	errorx.RegisterError(ErrTypeContextCancelled, "context cancelled for message %s")

	// 注册配置相关错误
	errorx.RegisterError(ErrTypeConfigValidatorNotInitialized, "configuration validator not initialized")
	errorx.RegisterError(ErrTypeConfigValidationFailed, "configuration validation failed")
	errorx.RegisterError(ErrTypeConfigAutoFixFailed, "configuration auto-fix failed")

	// 注册安全相关错误
	errorx.RegisterError(ErrTypeIPInBlacklist, "ip address is in blacklist: %s")
	errorx.RegisterError(ErrTypeBruteForceDetected, "brute force attack detected: %s")
	errorx.RegisterError(ErrTypeThreatDetected, "threat detected: %s")
	errorx.RegisterError(ErrTypeAccessDeniedByRule, "access denied by security rule: %s")

	// 注册消息记录仓库相关错误
	errorx.RegisterError(ErrTypeRecordRepositoryNotSet, "message record repository is not set")
}

// ============================================================================
// 错误变量定义（向后兼容）
// ============================================================================

// 连接相关错误变量
var (
	ErrConnectionClosed       = errorx.NewError(ErrTypeConnectionClosed)
	ErrMessageBufferFull      = errorx.NewError(ErrTypeMessageBufferFull)
	ErrHubStartupTimeout      = errorx.NewError(ErrTypeHubStartupTimeout)
	ErrHubShutdownTimeout     = errorx.NewError(ErrTypeHubShutdownTimeout)
	ErrQueueAndPendingFull    = errorx.NewError(ErrTypeQueueAndPendingFull)
	ErrMessageTargetMissing   = errorx.NewError(ErrTypeMessageTargetMissing)
	ErrUserOffline            = errorx.NewError(ErrTypeUserOffline)
	ErrMessageDeliveryTimeout = errorx.NewError(ErrTypeMessageDeliveryTimeout)
	ErrCircuitBreakerOpen     = errorx.NewError(ErrTypeCircuitBreakerOpen)
)

// ACK相关错误变量
var (
	ErrAckTimeout        = errorx.NewError(ErrTypeAckTimeout)
	ErrAckTimeoutRetries = errorx.NewError(ErrTypeAckTimeoutRetries)
	ErrContextCancelled  = errorx.NewError(ErrTypeContextCancelled)
)

// 记录管理相关错误变量
var (
	ErrRecordManagerNotInitialized = errorx.NewError(ErrTypeRecordManagerNotInitialized)
	ErrMaxRetriesExceeded          = errorx.NewError(ErrTypeMaxRetriesExceeded)
)

// 配置相关错误变量
var (
	ErrConfigValidatorNotInitialized = errorx.NewError(ErrTypeConfigValidatorNotInitialized)
)

// 业务逻辑错误变量
var (
	ErrMessageFiltered        = errorx.NewError(ErrTypeMessageFiltered)
	ErrNoAvailableAgents      = errorx.NewError(ErrTypeNoAvailableAgents)
	ErrQueueFull              = errorx.NewError(ErrTypeQueueFull)
	ErrRecordRepositoryNotSet = errorx.NewError(ErrTypeRecordRepositoryNotSet)
)

// IsRetryableError 判断错误是否可以重试
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 如果是 errorx.Error 类型，检查其错误类型
	if errxErr, ok := err.(interface{ Type() ErrorType }); ok {
		return IsRetryableErrorType(errxErr.Type())
	}

	// 对于定义的错误变量，直接检查可重试性
	switch err {
	case ErrMessageBufferFull, ErrQueueAndPendingFull,
		ErrAckTimeout, ErrMessageDeliveryTimeout, ErrCircuitBreakerOpen,
		ErrQueueFull, ErrHubStartupTimeout, ErrHubShutdownTimeout:
		return true
	default:
		return false
	}
}

// IsRetryableErrorType 判断错误类型是否可以重试
func IsRetryableErrorType(errType ErrorType) bool {
	switch errType {
	// 可重试的错误类型
	case ErrTypeConnectionTimeout, ErrTypeTemporaryFailure,
		ErrTypeMessageBufferFull, ErrTypeQueueAndPendingFull,
		ErrTypeResourceBusy, ErrTypeOperationTimeout,
		ErrTypeAckTimeout, ErrTypeMessageDeliveryTimeout, ErrTypeCircuitBreakerOpen,
		ErrTypeQueueFull, ErrTypePendingQueueFull, ErrTypeHubStartupTimeout,
		ErrTypeHubShutdownTimeout:
		return true
	// 不可重试的错误类型
	default:
		return false
	}
}

// ============================================================================
// 错误类型判断辅助函数
// ============================================================================

// IsQueueFullError 判断是否为队列满错误
func IsQueueFullError(err error) bool {
	if err == nil {
		return false
	}
	if errxErr, ok := err.(interface{ Type() ErrorType }); ok {
		errType := errxErr.Type()
		return errType == ErrTypeQueueFull ||
			errType == ErrTypeMessageBufferFull ||
			errType == ErrTypePendingQueueFull ||
			errType == ErrTypeQueueAndPendingFull
	}
	return err == ErrQueueFull || err == ErrMessageBufferFull || err == ErrQueueAndPendingFull
}

// IsUserOfflineError 判断是否为用户离线错误
func IsUserOfflineError(err error) bool {
	if err == nil {
		return false
	}
	if errxErr, ok := err.(interface{ Type() ErrorType }); ok {
		return errxErr.Type() == ErrTypeUserOffline
	}
	return err == ErrUserOffline
}

// IsSendTimeoutError 判断是否为发送超时错误
func IsSendTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errxErr, ok := err.(interface{ Type() ErrorType }); ok {
		errType := errxErr.Type()
		return errType == ErrTypeOperationTimeout ||
			errType == ErrTypeMessageDeliveryTimeout ||
			errType == ErrTypeConnectionTimeout
	}
	return err == ErrMessageDeliveryTimeout
}

// IsAckTimeoutError 判断是否为ACK超时错误
func IsAckTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errxErr, ok := err.(interface{ Type() ErrorType }); ok {
		errType := errxErr.Type()
		return errType == ErrTypeAckTimeout || errType == ErrTypeAckTimeoutRetries
	}
	return err == ErrAckTimeout || err == ErrAckTimeoutRetries
}
