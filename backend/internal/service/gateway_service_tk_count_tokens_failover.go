package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// tkCountTokensSkipBreaker 判断某个 count_tokens 上游状态码是否应被排除在
// anthropic_upstream_error 熔断器之外（即不调用 HandleUpstreamError）。
//
// count_tokens 是请求前的预检端点，不应因它的错误熔断主力账号：
//   - 400：客户端 body schema 错误（Anthropic invalid_request_error），与账号
//     容量/凭证无关（生产事故 2026-05-18）。
//   - 429/529/503：上游限流/过载/无可用账号是 transient，轮换交给 count_tokens 的
//     failover loop，状态写入交给真正的 /v1/messages 路径（现场：edge us1 单次
//     count_tokens 529 罚下 acct1 而 acct4 空闲）。503 与主路径口径对齐——
//     shouldFailoverUpstreamError 对 >=500 一律 failover，pool stub（prod→edge）
//     透回的 503（no available accounts）同属容量类瞬时错误，不应熔断主力账号。
//
// 由 ForwardCountTokens 与 forwardCountTokensAnthropicAPIKeyPassthrough 共用，
// 保证两条路径的豁免集合不漂移。
func tkCountTokensSkipBreaker(statusCode int) bool {
	return statusCode == http.StatusBadRequest ||
		statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == 529
}

// tkCountTokensFailoverError 为 count_tokens 上游错误响应构建账号 failover 错误，
// 由 ForwardCountTokens 与 passthrough 变体共用以保持同步。状态码不可 failover
// 时返回 nil（调用方此时按原逻辑直接写客户端）。
//
// 返回非 nil 时调用方必须**不向客户端写任何字节**直接返回该错误，交由 handler 的
// failover loop 决定换号 / 池内轮换 / 耗尽。pool_mode stub（prod cc-us1 → edge）
// 透回的 529/503 经 RetryableOnSameAccount 命中时在同一 stub 上重试 = 池内轮换。
func (s *GatewayService) tkCountTokensFailoverError(account *Account, resp *http.Response, respBody []byte) *UpstreamFailoverError {
	if resp == nil || !s.shouldFailoverUpstreamError(resp.StatusCode) {
		return nil
	}
	return &UpstreamFailoverError{
		StatusCode:      resp.StatusCode,
		ResponseBody:    respBody,
		ResponseHeaders: resp.Header,
		// Use the same carve-out as the main /v1/messages path (tkRetryableOnSameAccount,
		// account_tk_pool_retry.go) instead of the bare pool check: a header-less
		// anthropic capacity-envelope 429 ("No available accounts") relayed by a
		// dead edge is NON-authoritative, so it must NOT burn pool_mode_retry_count
		// in-place same-account retries holding the dead stub's slot — it must
		// switch accounts immediately. Without this, count_tokens on a pool_mode
		// stub held the dead stub (part of the cc-us2 2/70 amplifier) before
		// switching; an authoritative window-limit 429 still carries the headers and
		// is unaffected.
		RetryableOnSameAccount: tkRetryableOnSameAccount(account, resp, respBody),
	}
}

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
