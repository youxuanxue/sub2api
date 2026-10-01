package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// MaybeBlockTrialUnpaidMedia rejects image/video submissions from unpaid
// trial-shaped wallets. Returns true when the request was aborted.
//
// Called from API key auth after the user is loaded and before universal
// routing, so both Direct and Universal keys are covered.
func MaybeBlockTrialUnpaidMedia(c *gin.Context, apiKey *service.APIKey, settingService *service.SettingService) bool {
	if c == nil || apiKey == nil || apiKey.User == nil {
		return false
	}
	method := http.MethodGet
	fullPath := ""
	requestPath := ""
	if c.Request != nil {
		method = c.Request.Method
		if c.Request.URL != nil {
			requestPath = c.Request.URL.Path
		}
	}
	fullPath = c.FullPath()
	if fullPath == "" {
		fullPath = requestPath
	}
	if isTokenKeyVideoTaskRead(method, requestPath) || isAsyncImageTaskRead(method, requestPath) {
		return false
	}
	shape := service.UniversalShapeForRequest(fullPath, method)
	decision := service.EvaluateTrialUnpaidMedia(c.Request.Context(), apiKey.User, shape, method, settingService)
	if !decision.Blocked {
		return false
	}
	writeTrialUnpaidMediaError(c, shape, decision.Message)
	return true
}

func writeTrialUnpaidMediaError(c *gin.Context, shape service.UniversalShape, message string) {
	const status = http.StatusPaymentRequired // 402: recharge required
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "Image and video generation require a completed recharge."
	}
	service.MarkOpsClientPolicyDenied(c, service.OpsClientPolicyDeniedReasonLocalPolicyDenied)
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, msg)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		AnthropicErrorWriter(c, status, msg)
	default:
		c.AbortWithStatusJSON(status, gin.H{
			"error": gin.H{
				"message": msg,
				"type":    "invalid_request_error",
				"code":    "trial_unpaid_media_blocked",
			},
		})
		return
	}
	c.Abort()
}
