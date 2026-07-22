package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const middlewareInternalErrorDetailMaxLen = 1024

const postgresCanceledByCallerMessage = "canceling statement due to user request"

// StatusClientClosedRequest mirrors nginx's 499: the caller disconnected before
// the gateway could finish local auth/body handling. net/http has no constant.
const StatusClientClosedRequest = 499

// ContextKey 定义上下文键类型
type ContextKey string

const (
	// ContextKeyUser 用户上下文键
	ContextKeyUser ContextKey = "user"
	// ContextKeyUserRole 当前用户角色（string）
	ContextKeyUserRole ContextKey = "user_role"
	// ContextKeyAPIKey API密钥上下文键
	ContextKeyAPIKey ContextKey = "api_key"
	// ContextKeySubscription 订阅上下文键
	ContextKeySubscription ContextKey = "subscription"
	// ContextKeyForcePlatform 强制平台（用于 /antigravity 路由）
	ContextKeyForcePlatform ContextKey = "force_platform"
	// ContextKeyOpsFallbackAPIKey 运维错误日志专用回退键。
	// 鉴权早退（分组停用/删除、Key 停用/过期/额度、用户停用、IP 限制等）时，
	// apiKey 已加载但尚未写入 ContextKeyAPIKey；该键让 Ops 错误日志仍能取到
	// user/group/platform。仅供 Ops 错误日志读取，不代表请求已通过鉴权。
	ContextKeyOpsFallbackAPIKey ContextKey = "ops_fallback_api_key"
)

// ForcePlatform 返回设置强制平台的中间件
// 同时设置 request.Context（供 Service 使用）和 gin.Context（供 Handler 快速检查）
func ForcePlatform(platform string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 设置到 request.Context，使用 ctxkey.ForcePlatform 供 Service 层读取
		ctx := context.WithValue(c.Request.Context(), ctxkey.ForcePlatform, platform)
		c.Request = c.Request.WithContext(ctx)
		// 同时设置到 gin.Context，供 Handler 快速检查
		c.Set(string(ContextKeyForcePlatform), platform)
		c.Next()
	}
}

// HasForcePlatform 检查是否有强制平台（用于 Handler 跳过分组检查）
func HasForcePlatform(c *gin.Context) bool {
	_, exists := c.Get(string(ContextKeyForcePlatform))
	return exists
}

// GetForcePlatformFromContext 从 gin.Context 获取强制平台
func GetForcePlatformFromContext(c *gin.Context) (string, bool) {
	value, exists := c.Get(string(ContextKeyForcePlatform))
	if !exists {
		return "", false
	}
	platform, ok := value.(string)
	return platform, ok
}

// ErrorResponse 标准错误响应结构
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewErrorResponse 创建错误响应
func NewErrorResponse(code, message string) ErrorResponse {
	return ErrorResponse{
		Code:    code,
		Message: message,
	}
}

// AbortWithError 中断请求并返回JSON错误
func AbortWithError(c *gin.Context, statusCode int, code, message string) {
	c.JSON(statusCode, NewErrorResponse(code, message))
	c.Abort()
}

// AbortWithErrorDetail behaves like AbortWithError but additionally records a
// sanitized representation of internalErr on the gin context so that
// OpsErrorLoggerMiddleware can persist it into ops_error_logs.error_body for
// post-hoc RCA. The client-facing response is identical to AbortWithError —
// no detail is leaked to callers.
func AbortWithErrorDetail(c *gin.Context, statusCode int, code, message string, internalErr error) {
	if c != nil && internalErr != nil {
		if detail := sanitizeMiddlewareInternalErrorDetail(internalErr); detail != "" {
			c.Set(service.OpsInternalErrorDetailKey, detail)
		}
	}
	AbortWithError(c, statusCode, code, message)
}

func AbortClientClosedRequest(c *gin.Context, internalErr error) {
	if c != nil {
		service.MarkOpsClientClosedRequest(c)
		if detail := sanitizeMiddlewareInternalErrorDetail(internalErr); detail != "" {
			c.Set(service.OpsInternalErrorDetailKey, detail)
		}
	}
	AbortWithError(c, StatusClientClosedRequest, "CLIENT_CLOSED_REQUEST", "context canceled")
}

func IsClientClosedRequestError(c *gin.Context, err error) bool {
	// A server-side deadline is not caller-owned even when the database driver
	// reports a cancellation while unwinding the query.
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if c != nil && c.Request != nil {
		switch requestErr := c.Request.Context().Err(); {
		case errors.Is(requestErr, context.DeadlineExceeded):
			return false
		case errors.Is(requestErr, context.Canceled):
			return true
		}
	}
	// lib/pq returns a PostgreSQL 57014 error instead of wrapping
	// context.Canceled after database/sql sends the cancellation request.
	return err != nil && strings.Contains(strings.ToLower(err.Error()), postgresCanceledByCallerMessage)
}

// sanitizeMiddlewareInternalErrorDetail trims and length-caps an internal error
// string so it is safe to persist in ops_error_logs. Internal errors from
// Postgres/Redis/context cancellation generally do not carry caller secrets,
// but we still cap length to keep error_body bounded and utf8-safe.
func sanitizeMiddlewareInternalErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if s == "" {
		return ""
	}
	if len(s) > middlewareInternalErrorDetailMaxLen {
		s = s[:middlewareInternalErrorDetailMaxLen]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "...(truncated)"
	}
	return s
}

// ──────────────────────────────────────────────────────────
// RequireGroupAssignment — 未分组 Key 拦截中间件
// ──────────────────────────────────────────────────────────

// GatewayErrorWriter 定义网关错误响应格式（不同协议使用不同格式）
type GatewayErrorWriter func(c *gin.Context, status int, message string)

// AnthropicErrorWriter 按 Anthropic API 规范输出错误
func AnthropicErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": "permission_error", "message": message},
	})
}

// GoogleErrorWriter 按 Google API 规范输出错误
func GoogleErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  googleapi.HTTPStatusToGoogleStatus(status),
		},
	})
}

// RequireGroupAssignment 检查 API Key 是否已分配到分组，
// 如果未分组且系统设置不允许未分组 Key 调度则返回 403。
func RequireGroupAssignment(settingService *service.SettingService, writeError GatewayErrorWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey.GroupID != nil || apiKey.IsUniversal() {
			// 全能 Key（universal）授权由请求级解析按权限跨度裁决：可解析端点已在认证内
			// 替换为后端组（GroupID != nil 自然放行）；元数据端点未替换（GroupID == nil）也放行，
			// 由 handler 回落默认。两种情况都不应被“未分组拦截”挡住。
			c.Next()
			return
		}
		// 未分组 Key — 检查系统设置
		if settingService.IsUngroupedKeySchedulingAllowed(c.Request.Context()) {
			c.Next()
			return
		}
		service.MarkOpsClientPolicyDenied(c, service.OpsClientPolicyDeniedReasonAPIKeyGroupUnassigned)
		writeError(c, http.StatusForbidden, "API Key is not assigned to any group and cannot be used. Please contact the administrator to assign it to a group.")
		c.Abort()
	}
}
