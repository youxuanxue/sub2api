package service

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAIStreamCreditExhaustedFields are the error-object code/type locations.
// Top-level type on an SSE envelope is "error" or "response.failed" and does
// not match these values. Message fields are intentionally absent.
var openAIStreamCreditExhaustedFields = []string{
	"response.error.code",
	"error.code",
	"code",
	"response.error.type",
	"error.type",
	"type",
}

// openAIStreamExplicitCreditExhausted is true only when an error object names
// credit_balance_exhausted or insufficient_quota as its code or type.
// Prose that happens to contain those words is not billing evidence: callers
// can echo them, and a normal 429 must keep its rate-limit handling.
func openAIStreamExplicitCreditExhausted(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	for _, path := range openAIStreamCreditExhaustedFields {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String())) {
		case "credit_balance_exhausted", "insufficient_quota":
			return true
		}
	}
	return false
}

// applyOpenAIStreamCreditExhaustedSideEffects stops scheduling an API-key
// account whose prepaid balance is gone. OAuth and setup-token accounts keep
// their subscription-window handling and are not permanently disabled.
func (s *OpenAIGatewayService) applyOpenAIStreamCreditExhaustedSideEffects(c *gin.Context, account *Account, payload []byte) bool {
	if !openAIStreamExplicitCreditExhausted(payload) || account == nil || account.Type != AccountTypeAPIKey {
		return false
	}
	if s == nil || s.rateLimitService == nil || s.rateLimitService.accountRepo == nil {
		return false
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractOpenAISSEErrorMessage(payload)))
	if upstreamMsg != "" {
		upstreamMsg = truncateForLog([]byte(upstreamMsg), 512)
	}
	msg := "Payment required (402): insufficient balance or billing issue"
	if upstreamMsg != "" {
		msg = "Payment required (402): " + upstreamMsg
	}
	s.rateLimitService.handleAuthError(stateCtx, account, msg)
	s.BlockAccountScheduling(account, time.Time{}, "upstream_disable")
	return true
}
