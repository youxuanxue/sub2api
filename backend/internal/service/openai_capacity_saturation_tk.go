package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// Native OpenAI account capacity failures feed rolling scheduling preference
// without imposing a hard cooldown. HTTP and stream-terminal failures share this gate.
func eligibleForOpenAICapacitySaturationPreference(account *Account) bool {
	if account == nil {
		return false
	}
	return account.Platform == PlatformOpenAI && account.IsOpenAIOAuthLike()
}

func isOpenAITemporaryUnavailableMessage(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	// Exclude TokenKey's generic 5xx sanitization so OAuth does not soft-penalize
	// on connection-lost / unclassified 5xx that share this envelope text.
	if strings.Contains(lower, "upstream service temporarily unavailable") {
		return false
	}
	return strings.Contains(lower, "temporarily unavailable") ||
		strings.Contains(lower, "service unavailable") ||
		strings.Contains(lower, "service_unavailable") ||
		strings.Contains(lower, "service temporarily unavailable")
}

func openAICapacityErrorText(upstreamMsg string, upstreamBody []byte) []string {
	out := make([]string, 0, 5)
	if strings.TrimSpace(upstreamMsg) != "" {
		out = append(out, upstreamMsg)
	}
	if len(upstreamBody) == 0 {
		return out
	}
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"message",
		"error.type",
		"response.error.type",
	} {
		if v := gjson.GetBytes(upstreamBody, path).String(); strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	if !gjson.ValidBytes(upstreamBody) {
		out = append(out, string(upstreamBody))
	}
	return out
}

// isOpenAINativeCapacityUnavailable reports native OpenAI HTTP 503
// account-capacity pressure. Non-503 capacity shed remains request-scoped.
func isOpenAINativeCapacityUnavailable(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	for _, text := range openAICapacityErrorText(upstreamMsg, upstreamBody) {
		if isOpenAICapacityShedMessage(text) || isOpenAITemporaryUnavailableMessage(text) {
			return true
		}
	}
	return false
}

func shouldRecordOpenAICapacitySaturation(account *Account, statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if !eligibleForOpenAICapacitySaturationPreference(account) {
		return false
	}
	if isOpenAINativeCapacityUnavailable(statusCode, upstreamMsg, upstreamBody) {
		return true
	}
	return false
}

func (s *OpenAIGatewayService) maybeRecordOpenAICapacitySaturation(
	ctx context.Context,
	account *Account,
	statusCode int,
	upstreamMsg string,
	responseBody []byte,
	reason string,
) {
	if s == nil || s.rateLimitService == nil {
		return
	}
	if !shouldRecordOpenAICapacitySaturation(account, statusCode, upstreamMsg, responseBody) {
		return
	}
	if reason == "" {
		reason = "native_capacity"
	}
	s.rateLimitService.recordOpenAIStubSaturation(ctx, account.ID, statusCode, reason)
}

// Generic account throttling feeds shared candidate failure ranking even when
// the weekly budget remains available. Exclude quota, image and model limits.
func isOpenAIAccountRateLimitPressure(account *Account, headers http.Header, body []byte) bool {
	if !eligibleForOpenAICapacitySaturationPreference(account) || account.IsShadow() ||
		isOpenAIImageRateLimitError(http.StatusTooManyRequests, body) || isCodexBengalfoxActiveLimit(headers) ||
		tkOpenAIOAuth429ShouldSkipAccountStorm(account, headers, body) {
		return false
	}
	if disposition, _ := classifyOpenAIOAuth429(headers, body); disposition != openAIOAuth429Transient {
		return false
	}
	for _, path := range []string{"detail", "error.message", "response.error.message", "message"} {
		if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, path).String()), "Rate limit exceeded") {
			return true
		}
	}
	return false
}
