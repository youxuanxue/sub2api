package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func (h *GatewayHandler) tkHandleFailoverClientStatus(
	c *gin.Context,
	failoverErr *service.UpstreamFailoverError,
	statusCode int,
	responseBody []byte,
	streamStarted bool,
) bool {
	if failoverErr == nil || failoverErr.ClientStatusCode <= 0 {
		return false
	}
	// 记录原始上游状态码，以便 ops 错误日志捕获真实的上游错误。
	upstreamMsg := service.ExtractUpstreamErrorMessage(responseBody)
	service.SetOpsUpstreamError(c, statusCode, upstreamMsg, "")
	if retryAfter := failoverErr.ResponseHeaders.Get("Retry-After"); retryAfter != "" {
		c.Header("Retry-After", retryAfter)
	}
	message := failoverErr.ClientMessage
	if message == "" {
		message = service.GatewayFailoverClientMessage(failoverErr.ClientStatusCode)
	}
	errType := strings.TrimSpace(failoverErr.ClientErrorType)
	if errType == "" {
		errType = "api_error"
	}
	h.handleStreamingAwareError(c, failoverErr.ClientStatusCode, errType, message, streamStarted)
	return true
}

func (h *GatewayHandler) tkEnrichMappedUpstreamErrorMessage(
	c *gin.Context,
	platform string,
	statusCode int,
	responseBody []byte,
	errMsg string,
) string {
	if statusCode == http.StatusForbidden {
		errMsg = service.TkEnrichForbiddenMessage(c, errMsg)
	}
	if platform == service.PlatformAnthropic {
		if msg := service.ExtractUpstreamErrorMessage(responseBody); msg != "" {
			errMsg = msg
		}
		errMsg = service.TkEnrichClaudeIncidentMessage(errMsg, statusCode)
	}
	return errMsg
}

func (h *GatewayHandler) ensureForwardErrorResponseForError(c *gin.Context, err error, streamStarted bool) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	// A canceled inbound request means the caller has already gone away. Writing a
	// generic 502 here only corrupts access/ops semantics; there is no client left
	// to receive it. Keep the established 499 classification used by failover and
	// concurrency cancellation paths.
	if isClientClosedRequest(c, err) {
		markClientClosedForwardRequest(c)
		return false
	}
	if service.IsResponseCommitted(c) {
		return false
	}
	if c.Writer.Written() {
		streamStarted = true
	}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed", streamStarted)
	return true
}
