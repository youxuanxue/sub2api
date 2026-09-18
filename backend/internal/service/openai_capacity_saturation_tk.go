package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// OpenAI account capacity failures feed the same rolling soft scheduling
// preference as prod edge-mirror empty-pool saturation: IncrementSaturation on
// the selected account, never hard cooldown.
//
//   - Edge OAuth / setup-token: deprioritize the unhealthy OAuth among peers
//   - Prod edge-mirror stub: deprioritize the usN relay so traffic prefers other edges
//
// TokenKey's sanitized envelope "Upstream service temporarily unavailable" is
// only treated as a capacity signal on mirror stubs (edge already collapsed the
// native upstream status). OAuth paths require native HTTP 503 overloaded or
// temporary/service-unavailable wording; non-503 request-scoped model capacity
// and generic 5xx sanitization do not punish healthy accounts.

func eligibleForOpenAICapacitySaturationPreference(account *Account) bool {
	if account == nil {
		return false
	}
	if tkIsOpenAICompatEdgeMirrorStub(account) {
		return true
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

func isOpenAITokenKeyUpstreamUnavailableEnvelope(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusBadGateway && statusCode != http.StatusServiceUnavailable {
		return false
	}
	match := func(text string) bool {
		return strings.EqualFold(strings.TrimSpace(text), "Upstream service temporarily unavailable")
	}
	if match(upstreamMsg) {
		return true
	}
	for _, path := range []string{"error.message", "message"} {
		if match(gjson.GetBytes(upstreamBody, path).String()) {
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
	// Prod stub observing an edge-sanitized capacity failure.
	return tkIsOpenAICompatEdgeMirrorStub(account) &&
		isOpenAITokenKeyUpstreamUnavailableEnvelope(statusCode, upstreamMsg, upstreamBody)
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
