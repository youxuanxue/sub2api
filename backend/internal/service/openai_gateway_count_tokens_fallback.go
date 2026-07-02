package service

import (
	"net/http"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const openAIInputTokensFallbackMinimum = 1

type openAIInputTokensFallbackKind int

const (
	openAIInputTokensFallbackNone openAIInputTokensFallbackKind = iota
	openAIInputTokensFallbackOAuthEstimate
	openAIInputTokensFallbackAnthropicEstimate
)

type openAIInputTokensFallbackDecision struct {
	Kind            openAIInputTokensFallbackKind
	UpstreamMessage string
}

func shouldEstimateOpenAIInputTokensForAuthError(account *Account, err error) bool {
	if account == nil || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if account.Platform == PlatformNewAPI {
		if account.Type == AccountTypeServiceAccount && account.ChannelType == newapiconstant.ChannelTypeVertexAi {
			return true
		}
		return strings.Contains(msg, "api_key not found")
	}
	return false
}

func classifyOpenAIInputTokensFallback(account *Account, statusCode int, body []byte) openAIInputTokensFallbackDecision {
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if account != nil && account.Type == AccountTypeOAuth && isOpenAIOAuthInputTokensUnsupported(statusCode, body) {
		return openAIInputTokensFallbackDecision{Kind: openAIInputTokensFallbackOAuthEstimate, UpstreamMessage: upstreamMsg}
	}
	if isOpenAIInputTokensUnsupported(statusCode, body) {
		return openAIInputTokensFallbackDecision{Kind: openAIInputTokensFallbackAnthropicEstimate, UpstreamMessage: upstreamMsg}
	}
	if isOpenAICompatInputTokensCapabilityGap(account, statusCode, upstreamMsg, body) {
		return openAIInputTokensFallbackDecision{Kind: openAIInputTokensFallbackAnthropicEstimate, UpstreamMessage: upstreamMsg}
	}
	return openAIInputTokensFallbackDecision{Kind: openAIInputTokensFallbackNone, UpstreamMessage: upstreamMsg}
}

func isOpenAIInputTokensUnsupported(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	return strings.Contains(msg, "input_tokens") && strings.Contains(msg, "not found")
}

func writeOpenAIOAuthInputTokensFallback(c *gin.Context, account *Account, prepared *openAIInputTokensCountPrepared, statusCode int) {
	estimated := openAIInputTokensFallbackMinimum
	if got, err := estimateOpenAIInputTokens(prepared.Request); err == nil {
		if got > 0 {
			estimated = got
		}
		logger.L().Info("openai count_tokens: oauth fallback to local tiktoken estimate",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", statusCode),
			zap.Int("estimated_input_tokens", estimated),
			zap.String("upstream_model", prepared.UpstreamModel),
		)
	} else {
		logger.L().Warn("openai count_tokens: oauth local tiktoken fallback failed, using minimum estimate",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", statusCode),
			zap.Int("estimated_input_tokens", estimated),
			zap.String("upstream_model", prepared.UpstreamModel),
			zap.Error(err),
		)
	}

	c.JSON(http.StatusOK, gin.H{
		"input_tokens": estimated,
	})
}

func isOpenAIOAuthInputTokensUnsupported(statusCode int, body []byte) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
	default:
		return false
	}

	bodyLower := strings.ToLower(string(body))
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(body)))

	if code == "missing_scope" ||
		strings.Contains(bodyLower, "api.responses.write") ||
		strings.Contains(bodyLower, "missing scopes") ||
		strings.Contains(bodyLower, "insufficient_scope") {
		return true
	}

	if statusCode == http.StatusNotFound && isOpenAIInputTokensUnsupported(statusCode, body) {
		return true
	}

	return strings.Contains(msg, "input_tokens") &&
		(strings.Contains(msg, "not found") ||
			strings.Contains(msg, "not supported") ||
			strings.Contains(msg, "unsupported"))
}

func isOpenAICompatInputTokensCapabilityGap(account *Account, statusCode int, upstreamMsg string, body []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(upstreamMsg))
	if msg == "" {
		msg = strings.ToLower(strings.TrimSpace(string(body)))
	}
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "input_tokens") &&
		(strings.Contains(msg, "missing") || strings.Contains(msg, "not found") || strings.Contains(msg, "unsupported")) {
		return true
	}
	if statusCode == http.StatusNotFound && openAICompatInputTokensBare404CanEstimate(account) && strings.Contains(msg, "not found") {
		return true
	}
	if strings.Contains(msg, "upstream returned 403") &&
		(strings.Contains(msg, "access/policy rejection") || strings.Contains(msg, "rejected by upstream")) {
		return true
	}
	return false
}

func openAICompatInputTokensBare404CanEstimate(account *Account) bool {
	if account == nil || account.Type == AccountTypeOAuth {
		return false
	}
	switch account.Platform {
	case PlatformOpenAI, PlatformNewAPI, PlatformGrok:
		return true
	default:
		return false
	}
}
