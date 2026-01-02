/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-05 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\middleware\rate_limit_alert.go
 * @Description: WebSocket消息风控预警服务 - 邮件通知（接口定义）
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package middleware

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
)

// EmailSender 邮件发送接口（由外部实现）
type EmailSender interface {
	SendEmailWithHTML(ctx context.Context, to []string, subject, htmlBody string) error
}

// RateLimitAlertService 风控预警服务
type RateLimitAlertService struct {
	emailSender   EmailSender
	alertEmails   []string
	appName       string
	subjectAlert  string
	subjectBlock  string
	templateAlert *template.Template
	templateBlock *template.Template
	logger        WSCLogger
}

// AlertTemplateData 邮件模板数据
type AlertTemplateData struct {
	AppName      string
	UserID       string
	UserType     string
	MinuteCount  int64
	HourCount    int64
	TriggerTime  string
	GenerateTime string
}

// NewRateLimitAlertService 创建风控预警服务
func NewRateLimitAlertService(
	emailSender EmailSender,
	alertEmails []string,
	appName, subjectAlert, subjectBlock string,
	templateAlertHTML, templateBlockHTML string,
	logger WSCLogger,
) (*RateLimitAlertService, error) {
	// 解析预警邮件模板
	tmplAlert, err := template.New("alert").Parse(templateAlertHTML)
	if err != nil {
		return nil, fmt.Errorf("解析预警邮件模板失败: %w", err)
	}

	// 解析封禁邮件模板
	tmplBlock, err := template.New("block").Parse(templateBlockHTML)
	if err != nil {
		return nil, fmt.Errorf("解析封禁邮件模板失败: %w", err)
	}

	// 如果未提供 logger,使用默认 logger
	if logger == nil {
		logger = DefaultLogger
	}

	return &RateLimitAlertService{
		emailSender:   emailSender,
		alertEmails:   alertEmails,
		appName:       appName,
		subjectAlert:  subjectAlert,
		subjectBlock:  subjectBlock,
		templateAlert: tmplAlert,
		templateBlock: tmplBlock,
		logger:        logger,
	}, nil
}

// SendAlert 发送预警邮件（达到阈值但未封禁）
func (s *RateLimitAlertService) SendAlert(ctx context.Context, userId, userType string, minuteCount, hourCount int64) {
	if s.emailSender == nil || len(s.alertEmails) == 0 {
		return
	}

	subject := s.subjectAlert
	body, err := s.renderTemplate(s.templateAlert, userId, userType, minuteCount, hourCount)
	if err != nil {
		s.logger.ErrorKV("渲染预警邮件模板失败",
			"user_id", userId,
			"user_type", userType,
			"error", err,
		)
		return
	}

	err = s.emailSender.SendEmailWithHTML(ctx, s.alertEmails, subject, body)
	mathx.When(err != nil).
		Then(func() {
			s.logger.ErrorKV("发送风控预警邮件失败",
				"user_id", userId,
				"user_type", userType,
				"minute_count", minuteCount,
				"hour_count", hourCount,
				"error", err,
			)
		}).
		Else(func() {
			s.logger.InfoKV("已发送风控预警邮件",
				"user_id", userId,
				"user_type", userType,
				"minute_count", minuteCount,
				"hour_count", hourCount,
			)
		}).
		Do()
}

// SendBlockAlert 发送封禁预警邮件（已触发封禁）
func (s *RateLimitAlertService) SendBlockAlert(ctx context.Context, userId, userType string, minuteCount, hourCount int64) {
	if s.emailSender == nil || len(s.alertEmails) == 0 {
		return
	}

	subject := s.subjectBlock
	body, err := s.renderTemplate(s.templateBlock, userId, userType, minuteCount, hourCount)
	if err != nil {
		s.logger.ErrorKV("渲染封禁邮件模板失败",
			"user_id", userId,
			"user_type", userType,
			"error", err,
		)
		return
	}

	err = s.emailSender.SendEmailWithHTML(ctx, s.alertEmails, subject, body)
	mathx.When(err != nil).
		Then(func() {
			s.logger.ErrorKV("发送封禁预警邮件失败",
				"user_id", userId,
				"user_type", userType,
				"minute_count", minuteCount,
				"hour_count", hourCount,
				"error", err,
			)
		}).
		Else(func() {
			s.logger.WarnKV("已发送封禁预警邮件",
				"user_id", userId,
				"user_type", userType,
				"minute_count", minuteCount,
				"hour_count", hourCount,
			)
		}).
		Do()
}

// renderTemplate 渲染邮件模板
func (s *RateLimitAlertService) renderTemplate(tmpl *template.Template, userId, userType string, minuteCount, hourCount int64) (string, error) {
	data := AlertTemplateData{
		AppName:      s.appName,
		UserID:       userId,
		UserType:     userType,
		MinuteCount:  minuteCount,
		HourCount:    hourCount,
		TriggerTime:  time.Now().Format(time.DateTime),
		GenerateTime: time.Now().Format(time.RFC3339),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}
