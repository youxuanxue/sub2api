package service

import (
	"log/slog"
	"strings"
)

// tkOpenAI403PrePenaltyGates collapses the request-level OpenAI 403 short-circuits
// that must run before the consecutive-403 counter / temp_unschedulable path.
//
// Returns handled=true when the caller must return immediately with shouldDisable.
func (s *RateLimitService) tkOpenAI403PrePenaltyGates(
	account *Account,
	upstreamMsg string,
	responseBody []byte,
) (handled, shouldDisable bool) {
	if account == nil {
		return true, false
	}
	if matched := matchTempUnschedKeyword(strings.ToLower(string(responseBody)), openAICloudflareChallengeKeywords); matched != "" {
		slog.Warn(
			"openai_403_cf_challenge_skip_cooldown",
			"account_id", account.ID,
			"matched_keyword", matched,
			"upstream_msg", upstreamMsg,
		)
		return true, true
	}
	if matched := tkIsOpenAIClientInducedCapability403(upstreamMsg, responseBody); matched != "" {
		slog.Info("openai_403_client_induced_capability_skip_cooldown",
			"account_id", account.ID,
			"matched", matched)
		return true, true
	}
	if openAIIsHTMLBody(responseBody) {
		slog.Warn(
			"openai_403_html_body_skip_cooldown",
			"account_id", account.ID,
			"upstream_msg", upstreamMsg,
		)
		return true, true
	}
	// 上游代理 / CDN 在请求到达 OpenAI API 之前就拦下时，回的是 HTML 403 页面而不是
	// {"error":{...}} 结构化错误。这类响应描述的是「这条链路 / 这个端点被挡了」，
	// 不构成账号凭据或权限失效的证据——例如无效的 /v1/responses 子路径（#5334）。
	//
	// 据此写账号状态会把请求级错误放大成账号级处罚：首次即 temp-unschedulable，
	// 连续 openAI403DisableThreshold 次直接永久禁用；而 403 又在 failover 状态集里，
	// 同一个坏请求会被逐个账号重放，足以把整组账号打下线。
	//
	// 与既有口径一致：count_tokens 路径的 isOpenAIOAuthInputTokensUnsupported 已把
	// 「HTML 403 page without a structured error」按端点级响应处理；
	// shouldApplyOpenAIAlphaSearchAccountErrorSideEffects 的不变式也是端点级错误
	// 只换号、不写账号错误状态。这里只跳过账号处罚，不改变 failover 行为——
	// 换个走不同代理的账号仍有可能成功。
	if isHTMLResponse(responseBody) {
		slog.Warn(
			"openai_403_html_body_skips_account_penalty",
			"account_id", account.ID,
			"upstream_message", upstreamMsg,
		)
		return true, false
	}
	return false, false
}
