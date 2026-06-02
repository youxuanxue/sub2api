package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// WriteCountTokensFailoverError 在 count_tokens 的 failover loop 耗尽（所有候选
// 账号都失败 / 选号落空）时，向客户端写出 count_tokens 形状的错误响应。
//
// 背景：ForwardCountTokens / forwardCountTokensAnthropicAPIKeyPassthrough 对可
// failover 的上游状态码返回 *UpstreamFailoverError 且**不写客户端响应**（留给
// handler 的 failover loop 决定换号 / 池内轮换 / 耗尽）。因此耗尽分支的响应写入
// 责任落到 handler，由本方法统一成与单发路径一致的 {type:error,error:{...}} 形状。
//
// 状态码沿用最后一次上游错误（401/403/429/529/5xx），message 与单发路径保持一致。
func (s *GatewayService) WriteCountTokensFailoverError(c *gin.Context, failoverErr *UpstreamFailoverError) {
	status := http.StatusBadGateway
	if failoverErr != nil && failoverErr.StatusCode > 0 {
		status = failoverErr.StatusCode
	}
	errMsg := "Upstream request failed"
	switch status {
	case http.StatusTooManyRequests:
		errMsg = "Rate limit exceeded"
	case 529:
		errMsg = "Service overloaded"
	}
	s.countTokensError(c, status, "upstream_error", errMsg)
}
