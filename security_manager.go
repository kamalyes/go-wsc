/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:22:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 21:22:00
 * @FilePath: \go-wsc\security_manager.go
 * @Description: 安全管理器 - 提供连接安全检查、消息验证和访问控制功能
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// SecurityLevel 安全级别
type SecurityLevel int

const (
	SecurityLevelLow    SecurityLevel = iota // 低级安全
	SecurityLevelMedium                      // 中级安全
	SecurityLevelHigh                        // 高级安全
	SecurityLevelUltra                       // 超高级安全
)

// SecurityEvent 安全事件
type SecurityEvent struct {
	Type        SecurityEventType `json:"type"`
	Level       SecurityLevel     `json:"level"`
	UserID      string            `json:"user_id"`
	ClientIP    string            `json:"client_ip"`
	Message     string            `json:"message"`
	Timestamp   time.Time         `json:"timestamp"`
	Details     map[string]string `json:"details"`
	Severity    string            `json:"severity"`
	ActionTaken string            `json:"action_taken"`
}

// SecurityEventType 安全事件类型
type SecurityEventType string

const (
	SecurityEventTypeInvalidAuth    SecurityEventType = "invalid_auth"
	SecurityEventTypeBruteForce     SecurityEventType = "brute_force"
	SecurityEventTypeMaliciousIP    SecurityEventType = "malicious_ip"
	SecurityEventTypeInvalidMessage SecurityEventType = "invalid_message"
	SecurityEventTypeRateLimit      SecurityEventType = "rate_limit"
	SecurityEventTypePermissionDeny SecurityEventType = "permission_deny"
	SecurityEventTypeDataLeak       SecurityEventType = "data_leak"
	SecurityEventTypeThreatDetected SecurityEventType = "threat_detected"
)

// AccessRule 访问规则
type AccessRule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        AccessRuleType    `json:"type"`
	Conditions  map[string]string `json:"conditions"`
	Action      AccessAction      `json:"action"`
	Priority    int               `json:"priority"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Description string            `json:"description"`
}

// AccessRuleType 访问规则类型
type AccessRuleType string

const (
	AccessRuleTypeIP       AccessRuleType = "ip"
	AccessRuleTypeUser     AccessRuleType = "user"
	AccessRuleTypeGroup    AccessRuleType = "group"
	AccessRuleTypeTime     AccessRuleType = "time"
	AccessRuleTypeLocation AccessRuleType = "location"
	AccessRuleTypeDevice   AccessRuleType = "device"
)

// AccessAction 访问动作
type AccessAction string

const (
	AccessActionAllow AccessAction = "allow"
	AccessActionDeny  AccessAction = "deny"
	AccessActionBlock AccessAction = "block"
	AccessActionAlert AccessAction = "alert"
)

// SecurityStats 安全统计
type SecurityStats struct {
	TotalEvents        int64            `json:"total_events"`
	BlockedConnections int64            `json:"blocked_connections"`
	InvalidMessages    int64            `json:"invalid_messages"`
	BruteForceAttempts int64            `json:"brute_force_attempts"`
	ThreatDetections   int64            `json:"threat_detections"`
	EventsByType       map[string]int64 `json:"events_by_type"`
	EventsByLevel      map[string]int64 `json:"events_by_level"`
	TopThreats         []ThreatInfo     `json:"top_threats"`
	LastUpdated        time.Time        `json:"last_updated"`
}

// ThreatInfo 威胁信息
type ThreatInfo struct {
	IP          string    `json:"ip"`
	UserID      string    `json:"user_id"`
	ThreatLevel string    `json:"threat_level"`
	EventCount  int64     `json:"event_count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	Description string    `json:"description"`
}

// SecurityManager 安全管理器
type SecurityManager struct {
	config         *wscconfig.WSC
	accessRules    map[string]*AccessRule
	blockedIPs     map[string]time.Time
	bruteForceMap  map[string][]time.Time
	securityEvents []SecurityEvent
	stats          SecurityStats
	mutex          sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	eventHandlers  []func(SecurityEvent)

	// 威胁检测
	threatPatterns map[string]*ThreatPattern
	whitelistIPs   map[string]bool
	blacklistIPs   map[string]bool
}

// ThreatPattern 威胁模式
type ThreatPattern struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Pattern     string   `json:"pattern"`
	Severity    string   `json:"severity"`
	Keywords    []string `json:"keywords"`
	Regex       string   `json:"regex"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
}

// NewSecurityManager 创建安全管理器
func NewSecurityManager(config *wscconfig.WSC) *SecurityManager {
	ctx, cancel := context.WithCancel(context.Background())

	sm := &SecurityManager{
		config:         config,
		accessRules:    make(map[string]*AccessRule),
		blockedIPs:     make(map[string]time.Time),
		bruteForceMap:  make(map[string][]time.Time),
		securityEvents: make([]SecurityEvent, 0),
		stats: SecurityStats{
			EventsByType:  make(map[string]int64),
			EventsByLevel: make(map[string]int64),
			TopThreats:    make([]ThreatInfo, 0),
			LastUpdated:   time.Now(),
		},
		ctx:            ctx,
		cancel:         cancel,
		eventHandlers:  make([]func(SecurityEvent), 0),
		threatPatterns: make(map[string]*ThreatPattern),
		whitelistIPs:   make(map[string]bool),
		blacklistIPs:   make(map[string]bool),
	}

	// 初始化默认威胁模式
	sm.initDefaultThreatPatterns()

	// 启动安全监控
	go sm.startSecurityMonitoring()

	return sm
}

// initDefaultThreatPatterns 初始化默认威胁模式
func (sm *SecurityManager) initDefaultThreatPatterns() {
	patterns := []*ThreatPattern{
		{
			ID:          "sql_injection",
			Name:        "SQL注入检测",
			Severity:    "high",
			Keywords:    []string{"union", "select", "drop", "delete", "update", "insert", "exec", "script"},
			Regex:       `(?i)(union|select|drop|delete|update|insert|exec|script)`,
			Description: "检测SQL注入攻击模式",
			Enabled:     true,
		},
		{
			ID:          "xss_attack",
			Name:        "XSS攻击检测",
			Severity:    "high",
			Keywords:    []string{"<script>", "javascript:", "onload=", "onerror=", "onclick="},
			Regex:       `(?i)(<script>|javascript:|on\w+=)`,
			Description: "检测跨站脚本攻击",
			Enabled:     true,
		},
		{
			ID:          "path_traversal",
			Name:        "路径遍历检测",
			Severity:    "medium",
			Keywords:    []string{"../", "..\\", "/etc/", "\\windows\\"},
			Regex:       `(?i)(\.\./|\.\.\\|/etc/|\\windows\\)`,
			Description: "检测路径遍历攻击",
			Enabled:     true,
		},
		{
			ID:          "command_injection",
			Name:        "命令注入检测",
			Severity:    "high",
			Keywords:    []string{"cmd.exe", "/bin/sh", "bash", "powershell", "wget", "curl"},
			Regex:       `(?i)(cmd\.exe|/bin/sh|bash|powershell|wget|curl)`,
			Description: "检测命令注入攻击",
			Enabled:     true,
		},
	}

	for _, pattern := range patterns {
		sm.threatPatterns[pattern.ID] = pattern
	}
}

// ValidateConnection 验证连接
func (sm *SecurityManager) ValidateConnection(clientIP, userID string, headers map[string]string) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// 检查黑名单
	if sm.blacklistIPs[clientIP] {
		sm.recordSecurityEvent(SecurityEvent{
			Type:        SecurityEventTypeMaliciousIP,
			Level:       SecurityLevelHigh,
			UserID:      userID,
			ClientIP:    clientIP,
			Message:     "连接被黑名单拒绝",
			Timestamp:   time.Now(),
			Severity:    "high",
			ActionTaken: "blocked",
		})
		return errorx.NewError(ErrTypeIPInBlacklist, "client_ip: %s", clientIP)
	}

	// 检查访问规则
	if err := sm.checkAccessRules(clientIP, userID, headers); err != nil {
		return err
	}

	// 检查暴力攻击
	if sm.checkBruteForce(clientIP) {
		sm.recordSecurityEvent(SecurityEvent{
			Type:        SecurityEventTypeBruteForce,
			Level:       SecurityLevelHigh,
			UserID:      userID,
			ClientIP:    clientIP,
			Message:     "检测到暴力攻击",
			Timestamp:   time.Now(),
			Severity:    "high",
			ActionTaken: "blocked",
		})
		return errorx.NewError(ErrTypeBruteForceDetected, "IP %s 被暂时阻止", clientIP)
	}

	return nil
}

// ValidateMessage 验证消息
func (sm *SecurityManager) ValidateMessage(userID, clientIP string, message []byte) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	messageStr := string(message)

	// 检查消息大小
	if sm.config.Security != nil && sm.config.Security.MaxMessageSize > 0 {
		if len(message) > sm.config.Security.MaxMessageSize {
			sm.recordSecurityEvent(SecurityEvent{
				Type:        SecurityEventTypeInvalidMessage,
				Level:       SecurityLevelMedium,
				UserID:      userID,
				ClientIP:    clientIP,
				Message:     "消息超出大小限制",
				Timestamp:   time.Now(),
				Severity:    "medium",
				ActionTaken: "rejected",
			})
			return errorx.NewError(ErrTypeMessageTooLarge, "消息大小 %d 超出限制 %d", len(message), sm.config.Security.MaxMessageSize)
		}
	}

	// 威胁检测
	if threat := sm.detectThreat(messageStr); threat != nil {
		sm.recordSecurityEvent(SecurityEvent{
			Type:        SecurityEventTypeThreatDetected,
			Level:       SecurityLevelHigh,
			UserID:      userID,
			ClientIP:    clientIP,
			Message:     fmt.Sprintf("检测到威胁: %s", threat.Name),
			Timestamp:   time.Now(),
			Details:     map[string]string{"threat_type": threat.ID, "pattern": threat.Pattern},
			Severity:    threat.Severity,
			ActionTaken: "blocked",
		})
		return errorx.NewError(ErrTypeThreatDetected, "threat: %s", threat.Name)
	}

	return nil
}

// checkAccessRules 检查访问规则
func (sm *SecurityManager) checkAccessRules(clientIP, userID string, headers map[string]string) error {
	// 按优先级排序规则
	rules := make([]*AccessRule, 0, len(sm.accessRules))
	for _, rule := range sm.accessRules {
		if rule.Enabled {
			rules = append(rules, rule)
		}
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})

	for _, rule := range rules {
		if sm.matchesRule(rule, clientIP, userID, headers) {
			switch rule.Action {
			case AccessActionDeny, AccessActionBlock:
				sm.recordSecurityEvent(SecurityEvent{
					Type:        SecurityEventTypePermissionDeny,
					Level:       SecurityLevelMedium,
					UserID:      userID,
					ClientIP:    clientIP,
					Message:     fmt.Sprintf("被规则拒绝: %s", rule.Name),
					Timestamp:   time.Now(),
					Details:     map[string]string{"rule_id": rule.ID, "rule_name": rule.Name},
					Severity:    "medium",
					ActionTaken: string(rule.Action),
				})
				return errorx.NewError(ErrTypeAccessDeniedByRule, "rule: %s", rule.Name)
			case AccessActionAlert:
				sm.recordSecurityEvent(SecurityEvent{
					Type:        SecurityEventTypePermissionDeny,
					Level:       SecurityLevelLow,
					UserID:      userID,
					ClientIP:    clientIP,
					Message:     fmt.Sprintf("触发警告规则: %s", rule.Name),
					Timestamp:   time.Now(),
					Details:     map[string]string{"rule_id": rule.ID, "rule_name": rule.Name},
					Severity:    "low",
					ActionTaken: "alert",
				})
			}
		}
	}

	return nil
}

// matchesRule 检查是否匹配规则
func (sm *SecurityManager) matchesRule(rule *AccessRule, clientIP, userID string, headers map[string]string) bool {
	switch rule.Type {
	case AccessRuleTypeIP:
		return sm.matchesIPRule(rule, clientIP)
	case AccessRuleTypeUser:
		return sm.matchesUserRule(rule, userID)
	case AccessRuleTypeTime:
		return sm.matchesTimeRule(rule)
	default:
		return false
	}
}

// matchesIPRule 检查IP规则匹配
func (sm *SecurityManager) matchesIPRule(rule *AccessRule, clientIP string) bool {
	if ipRange, ok := rule.Conditions["ip_range"]; ok {
		return sm.isIPInRange(clientIP, ipRange)
	}
	if specificIP, ok := rule.Conditions["ip"]; ok {
		return clientIP == specificIP
	}
	return false
}

// matchesUserRule 检查用户规则匹配
func (sm *SecurityManager) matchesUserRule(rule *AccessRule, userID string) bool {
	if user, ok := rule.Conditions["user_id"]; ok {
		return userID == user
	}
	if pattern, ok := rule.Conditions["user_pattern"]; ok {
		return strings.Contains(userID, pattern)
	}
	return false
}

// matchesTimeRule 检查时间规则匹配
func (sm *SecurityManager) matchesTimeRule(rule *AccessRule) bool {
	now := time.Now()

	if startTime, ok := rule.Conditions["start_time"]; ok {
		if endTime, ok2 := rule.Conditions["end_time"]; ok2 {
			start, _ := time.Parse("15:04", startTime)
			end, _ := time.Parse("15:04", endTime)
			current, _ := time.Parse("15:04", now.Format("15:04"))

			return current.After(start) && current.Before(end)
		}
	}

	return false
}

// isIPInRange 检查IP是否在指定范围内
func (sm *SecurityManager) isIPInRange(clientIP, ipRange string) bool {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}

	_, cidr, err := net.ParseCIDR(ipRange)
	if err != nil {
		// 如果不是CIDR格式，尝试单个IP比较
		return clientIP == ipRange
	}

	return cidr.Contains(ip)
}

// checkBruteForce 检查暴力攻击
func (sm *SecurityManager) checkBruteForce(clientIP string) bool {
	now := time.Now()
	attempts, exists := sm.bruteForceMap[clientIP]

	if !exists {
		sm.bruteForceMap[clientIP] = []time.Time{now}
		return false
	}

	// 清理过期的尝试记录（超过5分钟）
	validAttempts := make([]time.Time, 0)
	for _, attempt := range attempts {
		if now.Sub(attempt) <= 5*time.Minute {
			validAttempts = append(validAttempts, attempt)
		}
	}

	validAttempts = append(validAttempts, now)
	sm.bruteForceMap[clientIP] = validAttempts

	// 如果5分钟内尝试超过5次，认为是暴力攻击
	maxAttempts := 5
	if sm.config.Security != nil && sm.config.Security.MaxLoginAttempts > 0 {
		maxAttempts = sm.config.Security.MaxLoginAttempts
	}

	if len(validAttempts) > maxAttempts {
		// 暂时阻止该IP
		sm.blockedIPs[clientIP] = now.Add(30 * time.Minute)
		return true
	}

	return false
}

// detectThreat 检测威胁
func (sm *SecurityManager) detectThreat(message string) *ThreatPattern {
	for _, pattern := range sm.threatPatterns {
		if !pattern.Enabled {
			continue
		}

		// 检查关键词
		for _, keyword := range pattern.Keywords {
			if strings.Contains(strings.ToLower(message), strings.ToLower(keyword)) {
				return pattern
			}
		}
	}

	return nil
}

// recordSecurityEvent 记录安全事件
func (sm *SecurityManager) recordSecurityEvent(event SecurityEvent) {
	// 添加到事件列表
	sm.securityEvents = append(sm.securityEvents, event)

	// 更新统计信息
	sm.stats.TotalEvents++
	sm.stats.EventsByType[string(event.Type)]++
	sm.stats.EventsByLevel[event.Severity]++
	sm.stats.LastUpdated = time.Now()

	// 根据事件类型更新特定统计
	switch event.Type {
	case SecurityEventTypeMaliciousIP:
		sm.stats.BlockedConnections++
	case SecurityEventTypeInvalidMessage:
		sm.stats.InvalidMessages++
	case SecurityEventTypeBruteForce:
		sm.stats.BruteForceAttempts++
	case SecurityEventTypeThreatDetected:
		sm.stats.ThreatDetections++
	}

	// 保持最近1000个事件
	if len(sm.securityEvents) > 1000 {
		sm.securityEvents = sm.securityEvents[len(sm.securityEvents)-1000:]
	}

	// 触发事件处理器
	for _, handler := range sm.eventHandlers {
		go handler(event)
	}
}

// startSecurityMonitoring 启动安全监控
func (sm *SecurityManager) startSecurityMonitoring() {
	// 使用较短的间隔以确保测试时能及时响应shutdown
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-sm.ctx.Done():
			return
		case <-ticker.C:
			// 添加超时处理，避免阻塞
			done := make(chan bool, 1)
			go func() {
				sm.cleanupExpiredBlocks()
				sm.updateThreatStats()
				done <- true
			}()

			// 5秒超时
			select {
			case <-done:
				// 正常完成
			case <-time.After(5 * time.Second):
				// 超时，继续下一轮
			case <-sm.ctx.Done():
				return
			}
		}
	}
}

// cleanupExpiredBlocks 清理过期的IP阻止
func (sm *SecurityManager) cleanupExpiredBlocks() {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	now := time.Now()
	for ip, expireTime := range sm.blockedIPs {
		if now.After(expireTime) {
			delete(sm.blockedIPs, ip)
		}
	}
}

// updateThreatStats 更新威胁统计
func (sm *SecurityManager) updateThreatStats() {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// 统计威胁IP
	threatMap := make(map[string]*ThreatInfo)

	for _, event := range sm.securityEvents {
		if event.ClientIP != "" {
			threat, exists := threatMap[event.ClientIP]
			if !exists {
				threat = &ThreatInfo{
					IP:          event.ClientIP,
					UserID:      event.UserID,
					ThreatLevel: event.Severity,
					EventCount:  0,
					FirstSeen:   event.Timestamp,
					LastSeen:    event.Timestamp,
				}
				threatMap[event.ClientIP] = threat
			}

			threat.EventCount++
			if event.Timestamp.Before(threat.FirstSeen) {
				threat.FirstSeen = event.Timestamp
			}
			if event.Timestamp.After(threat.LastSeen) {
				threat.LastSeen = event.Timestamp
			}
		}
	}

	// 转换为切片并排序
	threats := make([]ThreatInfo, 0, len(threatMap))
	for _, threat := range threatMap {
		threats = append(threats, *threat)
	}

	sort.Slice(threats, func(i, j int) bool {
		return threats[i].EventCount > threats[j].EventCount
	})

	// 保留前10个威胁
	if len(threats) > 10 {
		threats = threats[:10]
	}

	sm.stats.TopThreats = threats
}

// AddAccessRule 添加访问规则
func (sm *SecurityManager) AddAccessRule(rule *AccessRule) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if rule.ID == "" {
		rule.ID = sm.generateRuleID()
	}
	rule.CreatedAt = time.Now()
	rule.UpdatedAt = time.Now()

	sm.accessRules[rule.ID] = rule
}

// RemoveAccessRule 移除访问规则
func (sm *SecurityManager) RemoveAccessRule(ruleID string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	delete(sm.accessRules, ruleID)
}

// AddThreatPattern 添加威胁模式
func (sm *SecurityManager) AddThreatPattern(pattern *ThreatPattern) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	sm.threatPatterns[pattern.ID] = pattern
}

// RemoveThreatPattern 移除威胁模式
func (sm *SecurityManager) RemoveThreatPattern(patternID string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	delete(sm.threatPatterns, patternID)
}

// AddToWhitelist 添加到白名单
func (sm *SecurityManager) AddToWhitelist(ip string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	sm.whitelistIPs[ip] = true
	delete(sm.blacklistIPs, ip) // 从黑名单移除
}

// AddToBlacklist 添加到黑名单
func (sm *SecurityManager) AddToBlacklist(ip string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	sm.blacklistIPs[ip] = true
	delete(sm.whitelistIPs, ip) // 从白名单移除
}

// GetSecurityStats 获取安全统计
func (sm *SecurityManager) GetSecurityStats() SecurityStats {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	return sm.stats
}

// GetSecurityEvents 获取安全事件
func (sm *SecurityManager) GetSecurityEvents(limit int) []SecurityEvent {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	if limit <= 0 || limit > len(sm.securityEvents) {
		limit = len(sm.securityEvents)
	}

	// 返回最近的事件
	start := len(sm.securityEvents) - limit
	if start < 0 {
		start = 0
	}

	events := make([]SecurityEvent, limit)
	copy(events, sm.securityEvents[start:])

	return events
}

// OnSecurityEvent 注册安全事件处理器
func (sm *SecurityManager) OnSecurityEvent(handler func(SecurityEvent)) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	sm.eventHandlers = append(sm.eventHandlers, handler)
}

// generateRuleID 生成规则ID
func (sm *SecurityManager) generateRuleID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// GenerateSecurityReport 生成安全报告
func (sm *SecurityManager) GenerateSecurityReport() string {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	var report strings.Builder

	report.WriteString("=== WSC 安全管理报告 ===\n")
	report.WriteString(fmt.Sprintf("报告生成时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	report.WriteString("\n")

	// 安全统计
	report.WriteString("--- 安全统计 ---\n")
	report.WriteString(fmt.Sprintf("总安全事件: %d\n", sm.stats.TotalEvents))
	report.WriteString(fmt.Sprintf("阻止的连接: %d\n", sm.stats.BlockedConnections))
	report.WriteString(fmt.Sprintf("无效消息: %d\n", sm.stats.InvalidMessages))
	report.WriteString(fmt.Sprintf("暴力攻击尝试: %d\n", sm.stats.BruteForceAttempts))
	report.WriteString(fmt.Sprintf("威胁检测: %d\n", sm.stats.ThreatDetections))
	report.WriteString("\n")

	// 事件类型统计
	report.WriteString("--- 事件类型统计 ---\n")
	for eventType, count := range sm.stats.EventsByType {
		report.WriteString(fmt.Sprintf("%s: %d\n", eventType, count))
	}
	report.WriteString("\n")

	// 威胁级别统计
	report.WriteString("--- 威胁级别统计 ---\n")
	for level, count := range sm.stats.EventsByLevel {
		report.WriteString(fmt.Sprintf("%s: %d\n", level, count))
	}
	report.WriteString("\n")

	// Top威胁
	if len(sm.stats.TopThreats) > 0 {
		report.WriteString("--- Top 威胁 IP ---\n")
		for i, threat := range sm.stats.TopThreats {
			report.WriteString(fmt.Sprintf("%d. IP: %s, 事件数: %d, 威胁级别: %s\n",
				i+1, threat.IP, threat.EventCount, threat.ThreatLevel))
		}
		report.WriteString("\n")
	}

	// 活跃规则
	report.WriteString("--- 活跃访问规则 ---\n")
	activeRules := 0
	for _, rule := range sm.accessRules {
		if rule.Enabled {
			activeRules++
			report.WriteString(fmt.Sprintf("- %s (%s): %s\n", rule.Name, rule.Type, rule.Action))
		}
	}
	report.WriteString(fmt.Sprintf("总计: %d 个活跃规则\n", activeRules))
	report.WriteString("\n")

	// 威胁模式
	report.WriteString("--- 威胁检测模式 ---\n")
	activePatterns := 0
	for _, pattern := range sm.threatPatterns {
		if pattern.Enabled {
			activePatterns++
			report.WriteString(fmt.Sprintf("- %s: %s (严重程度: %s)\n",
				pattern.Name, pattern.Description, pattern.Severity))
		}
	}
	report.WriteString(fmt.Sprintf("总计: %d 个活跃威胁模式\n", activePatterns))
	report.WriteString("\n")

	// 黑白名单
	report.WriteString(fmt.Sprintf("白名单IP数量: %d\n", len(sm.whitelistIPs)))
	report.WriteString(fmt.Sprintf("黑名单IP数量: %d\n", len(sm.blacklistIPs)))
	report.WriteString(fmt.Sprintf("当前被阻止IP数量: %d\n", len(sm.blockedIPs)))

	return report.String()
}

// Shutdown 关闭安全管理器
func (sm *SecurityManager) Shutdown() {
	// 立即取消context，停止所有监控goroutine
	if sm.cancel != nil {
		sm.cancel()
	}

	// 等待一小段时间确保goroutine已退出
	time.Sleep(10 * time.Millisecond)
}

// Hash 计算字符串的SHA256哈希
func (sm *SecurityManager) Hash(data string) string {
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}
