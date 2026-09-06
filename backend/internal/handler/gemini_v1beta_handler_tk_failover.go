package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func (h *GatewayHandler) handleGeminiFailoverExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError) {
	if failoverErr == nil {
		googleError(c, http.StatusBadGateway, "Upstream request failed")
		return
	}

	statusCode := failoverErr.StatusCode
	responseBody := failoverErr.ResponseBody
	if failoverErr.ClientStatusCode > 0 {
		copyFailoverRetryAfter(c, failoverErr.ResponseHeaders)
		message := failoverErr.ClientMessage
		if message == "" {
			message = service.GatewayFailoverClientMessage(statusCode)
		}
		service.SetOpsUpstreamError(c, statusCode, service.ExtractUpstreamErrorMessage(responseBody), "")
		googleError(c, failoverErr.ClientStatusCode, message)
		return
	}

	// 先检查透传规则
	if h.errorPassthroughService != nil && len(responseBody) > 0 {
		if rule := h.errorPassthroughService.MatchRule(service.PlatformGemini, statusCode, responseBody); rule != nil {
			// 确定响应状态码
			respCode := statusCode
			if !rule.PassthroughCode && rule.ResponseCode != nil {
				respCode = *rule.ResponseCode
			}

			// 确定响应消息
			msg := service.ExtractUpstreamErrorMessage(responseBody)
			if !rule.PassthroughBody && rule.CustomMessage != nil {
				msg = *rule.CustomMessage
			}

			if rule.SkipMonitoring {
				c.Set(service.OpsSkipPassthroughKey, true)
			}

			googleError(c, respCode, msg)
			return
		}
	}

	// 记录原始上游状态码，以便 ops 错误日志捕获真实的上游错误
	upstreamMsg := service.ExtractUpstreamErrorMessage(responseBody)
	service.SetOpsUpstreamError(c, statusCode, upstreamMsg, "")

	// 使用默认的错误映射
	status, message := mapGeminiUpstreamError(statusCode)
	if statusCode == http.StatusForbidden {
		message = service.TkEnrichForbiddenMessage(c, message)
	}
	googleError(c, status, message)
}
