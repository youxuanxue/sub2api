package service

import "net/http"

// tkExtraPoolModeRetryableStatusCodes allows pool-mode upstreams to retry
// transient overloads. Explicit account retry codes override these defaults;
// classified capacity exhaustion still switches accounts immediately.
var tkExtraPoolModeRetryableStatusCodes = []int{503, 529}

func tkIsAccountCapacityFailure(account *Account, statusCode int, upstreamMsg string, responseBody []byte) bool {
	if account == nil {
		return false
	}
	if account.Platform == PlatformAnthropic {
		return tkSkipDownstreamNoAvailableAccountsPenalty(statusCode, upstreamMsg, responseBody) ||
			tkSkipDownstreamFailoverExhaustedPenalty(statusCode, upstreamMsg, responseBody)
	}
	if IsOpenAICompatPlatform(account.Platform) {
		return shouldRecordOpenAICapacitySaturation(account, statusCode, upstreamMsg, responseBody)
	}
	return false
}

// tkIsPoolModeRetryableStatus 按 TK 默认（= upstream 默认 ∪ {503,529}）判断状态码
// 是否应触发同账号重试。作为 account.IsPoolModeRetryableStatus 在账号未显式配置
// 时的回退。
func tkIsPoolModeRetryableStatus(statusCode int) bool {
	if isPoolModeRetryableStatus(statusCode) {
		return true
	}
	for _, c := range tkExtraPoolModeRetryableStatusCodes {
		if c == statusCode {
			return true
		}
	}
	return false
}

// tkRetryableOnSameAccount excludes exhausted pools and non-authoritative
// Anthropic rate limits from retries against the same upstream.
func tkRetryableOnSameAccount(account *Account, resp *http.Response, responseBody []byte) bool {
	if account == nil || resp == nil {
		return false
	}
	if account.Platform == PlatformAnthropic && resp.StatusCode == http.StatusTooManyRequests &&
		!tkIsAnthropicNonAuthoritative429(resp.Header, responseBody) {
		return account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode)
	}
	if tkIsAccountCapacityFailure(account, resp.StatusCode, "", responseBody) {
		return false
	}
	if !account.IsPoolMode() || !account.IsPoolModeRetryableStatus(resp.StatusCode) {
		return false
	}
	if account.Platform == PlatformAnthropic && resp.StatusCode == http.StatusTooManyRequests &&
		tkIsAnthropicNonAuthoritative429(resp.Header, responseBody) {
		return false
	}
	return true
}
