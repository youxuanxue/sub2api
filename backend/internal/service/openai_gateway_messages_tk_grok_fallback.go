package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// tkTryGrokNativeCatalogChatFallback skips /v1/responses for Grok native-catalog
// SKUs that are provisioned on /v1/chat/completions. Returns handled=true when
// the early chat-completions fallback path owns the request.
func (s *OpenAIGatewayService) tkTryGrokNativeCatalogChatFallback(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	anthropicReq *apicompat.AnthropicRequest,
	clientStream bool,
	originalModel string,
	billingModel string,
	upstreamModel string,
	normalizedModel string,
	token string,
	grokCacheIdentity string,
	startTime time.Time,
	compactCandidate bool,
) (*OpenAIForwardResult, bool, error) {
	// Native-catalog Grok SKUs are provisioned on /v1/chat/completions; /v1/responses
	// often returns opaque 400s. Skip the responses hop for SSOT / universal parity.
	// Use the client-facing model id: mapped aliases like grok -> grok-4.6 still
	// ride /v1/responses; only explicit catalog ids skip the hop.
	if protocolExecutionBound(ctx) || account.Platform != PlatformGrok || !grokGroupServesNativeCatalogModel(normalizedModel) {
		return nil, false, nil
	}
	fallbackResult, fallbackErr := s.fallbackAnthropicToGrokChatCompletions(
		ctx,
		c,
		account,
		anthropicReq,
		clientStream,
		originalModel,
		billingModel,
		upstreamModel,
		token,
		grokCacheIdentity,
		startTime,
	)
	if fallbackResult != nil {
		fallbackResult.CompactCandidate = compactCandidate
	}
	return fallbackResult, true, fallbackErr
}

// tkTryGrokResponsesModelNotSupportedChatFallback retries via chat completions
// when Grok /v1/responses rejects the model as unsupported.
func (s *OpenAIGatewayService) tkTryGrokResponsesModelNotSupportedChatFallback(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	anthropicReq *apicompat.AnthropicRequest,
	clientStream bool,
	originalModel string,
	billingModel string,
	upstreamModel string,
	token string,
	grokCacheIdentity string,
	startTime time.Time,
	compactCandidate bool,
	statusCode int,
	upstreamMsg string,
	respBody []byte,
) (*OpenAIForwardResult, bool, error) {
	if protocolExecutionBound(ctx) || account.Platform != PlatformGrok || !isGrokResponsesModelNotSupportedRetryable(statusCode, upstreamMsg, respBody) {
		return nil, false, nil
	}
	logger.L().Info("openai messages: grok responses model not supported, fallback to chat completions",
		zap.Int64("account_id", account.ID),
		zap.Int("upstream_status", statusCode),
		zap.String("upstream_model", upstreamModel),
	)
	fallbackResult, fallbackErr := s.fallbackAnthropicToGrokChatCompletions(
		ctx,
		c,
		account,
		anthropicReq,
		clientStream,
		originalModel,
		billingModel,
		upstreamModel,
		token,
		grokCacheIdentity,
		startTime,
	)
	if fallbackResult != nil {
		fallbackResult.CompactCandidate = compactCandidate
	}
	return fallbackResult, true, fallbackErr
}

func isGrokResponsesModelNotSupportedRetryable(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	check := func(message string) bool {
		lower := strings.ToLower(strings.TrimSpace(message))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "model_not_supported") || strings.Contains(lower, "unsupported_model") {
			return true
		}
		if strings.Contains(lower, "model") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "unsupported")) {
			return true
		}
		if strings.Contains(lower, "responses") && strings.Contains(lower, "not support") {
			return true
		}
		return false
	}
	if check(upstreamMsg) || check(string(upstreamBody)) {
		return true
	}
	return check(gjson.GetBytes(upstreamBody, "error.code").String()) ||
		check(gjson.GetBytes(upstreamBody, "error.message").String())
}

func (s *OpenAIGatewayService) fallbackAnthropicToGrokChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	anthropicReq *apicompat.AnthropicRequest,
	clientStream bool,
	originalModel string,
	billingModel string,
	upstreamModel string,
	token string,
	grokCacheIdentity string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	chatBody, err := anthropicToChatCompletionsBody(anthropicReq, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("fallback convert anthropic to chat completions: %w", err)
	}
	if strippedBody, stripErr := stripRedundantGrokChatViewImageTool(chatBody); stripErr != nil {
		return nil, fmt.Errorf("fallback strip redundant Grok Chat view_image tool: %w", stripErr)
	} else {
		chatBody = strippedBody
	}
	targetURL, err := s.resolveGrokChatCompletionsUpstream(account)
	if err != nil {
		return nil, err
	}
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := buildGrokChatCompletionsRequest(upstreamCtx, c, targetURL, chatBody, token)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("fallback build grok chat completions request: %w", err)
	}
	applyGrokCacheHeaders(upstreamReq.Header, grokCacheIdentity)

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	hwka := s.beginAnthropicClientHeaderWaitKeepalive(c, clientStream)
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	hwka.stop()
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	s.updateGrokUsageSnapshot(ctx, account, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	upstreamBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fallback read grok chat completions body: %w", err)
	}

	buf := bytes.NewBuffer(upstreamBody)
	if clientStream {
		return convertBufferedChatCompletionsToAnthropicSSE(c, buf, originalModel, billingModel, upstreamModel, startTime)
	}
	return convertBufferedChatCompletionsToAnthropicJSON(c, buf, originalModel, billingModel, upstreamModel, startTime)
}

func (s *OpenAIGatewayService) resolveGrokChatCompletionsUpstream(account *Account) (string, error) {
	if s == nil {
		return "", fmt.Errorf("openai gateway service is nil")
	}
	if account == nil {
		return "", fmt.Errorf("grok chat completions: account is nil")
	}
	switch {
	case account.IsGrokOAuth():
		return buildOpenAIChatCompletionsURL(account.GetGrokBaseURL()), nil
	case account.IsGrokAPIKey():
		baseURL := account.GetOpenAIBaseURL()
		if strings.TrimSpace(baseURL) == "" {
			return "", fmt.Errorf("grok relay account %d missing base_url", account.ID)
		}
		validatedURL, err := s.validateUpstreamBaseURLForAccount(account, baseURL)
		if err != nil {
			return "", err
		}
		return buildOpenAIChatCompletionsURL(validatedURL), nil
	default:
		return "", fmt.Errorf("grok account type %s is not supported for chat completions fallback", account.Type)
	}
}

func buildGrokChatCompletionsRequest(ctx context.Context, c *gin.Context, targetURL string, body []byte, token string) (*http.Request, error) {
	if strings.TrimSpace(targetURL) == "" {
		return nil, fmt.Errorf("grok chat completions target URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c != nil {
		if v := strings.TrimSpace(c.GetHeader("User-Agent")); v != "" {
			req.Header.Set("User-Agent", v)
		}
		if v := strings.TrimSpace(c.GetHeader("OpenAI-Beta")); v != "" {
			req.Header.Set("OpenAI-Beta", v)
		}
	}
	applyGrokCLIHeaders(req.Header)
	return req, nil
}
